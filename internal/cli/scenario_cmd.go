package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/GlediLami/kubetective/internal/benchmark"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/internal/redact"
	"github.com/GlediLami/kubetective/pkg/api"
)

// newScenarioCmd turns a recorded incident into a benchmark scenario.
//
// This closes the last stretch of the contribution path. `investigate` already
// records; `sanitize` makes a recording shareable. What was missing was the
// step from "I have a JSONL file" to "the suite has a new case": authoring
// scenario.yaml by hand meant guessing a min_score and inventing mutations
// from nothing, which is why every scenario in the suite so far is synthetic
// and written by the maintainer.
func newScenarioCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scenario",
		Short: "Work with benchmark scenarios",
	}
	cmd.AddCommand(newScenarioNewCmd())
	return cmd
}

var slugPattern = regexp.MustCompile(`[^a-z0-9-]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugPattern.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func newScenarioNewCmd() *cobra.Command {
	var (
		name        string
		dir         string
		keepImages  bool
		noSanitize  bool
		forceWrite  bool
		description string
	)
	cmd := &cobra.Command{
		Use:   "new <incident-id|path>",
		Short: "Draft a benchmark scenario from a recorded incident",
		Long: `Turns a recorded incident into a scenario directory the benchmark can run.

Three things happen:

  1. The record is sanitised (identifiers pseudonymised, free text scrubbed)
     and written as record.jsonl. Redaction is verdict-preserving, so the
     scenario diagnoses exactly what the original did.
  2. The record is replayed to see what the engine currently concludes.
  3. Every observation kind is removed in turn to find which evidence the
     verdict actually rests on. Kinds that change the verdict become suggested
     mutations - the causal claims the benchmark will hold you to.

The generated scenario.yaml is a DRAFT. It carries the engine's own answer,
which is not the same thing as ground truth: accepting it unedited turns the
scenario into a tautology that can only ever pass. Read the verdict printed
below, decide whether it is right, and correct the file. A scenario the engine
gets wrong is worth more to the suite than one it gets right.

  kubetective scenario new incident-1754575200-checkout --name dns-outage
  kubetective scenario new ./record.jsonl --name cert-expiry --dir scenarios`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			src := args[0]

			store := record.NewDefaultStore()
			if strings.ContainsAny(src, "/\\") {
				store = record.NewStore(filepath.Dir(src))
				src = strings.TrimSuffix(filepath.Base(src), ".jsonl")
			}
			inc, err := store.Load(src)
			if err != nil {
				return fmt.Errorf("load %s: %w", args[0], err)
			}
			if len(inc.Observations) == 0 {
				return fmt.Errorf("%s carries no observations - nothing to build a scenario from", args[0])
			}

			if name == "" {
				name = slugify(inc.Meta.Target)
				if name == "" {
					name = slugify(inc.ID)
				}
			}
			name = slugify(name)
			if name == "" {
				return fmt.Errorf("could not derive a scenario name; pass --name")
			}

			// Sanitise unless explicitly refused. A scenario is published by
			// definition, so this is the safe default and opting out is loud.
			obs := inc.Observations
			if noSanitize {
				fmt.Fprintf(w, "WARNING: --no-sanitize - namespaces, workload names, images and\n")
				fmt.Fprintf(w, "         event text are being written verbatim. Do not commit this\n")
				fmt.Fprintf(w, "         unless you have read every line.\n\n")
			} else {
				clean, rep := redact.New(redact.Options{KeepImages: keepImages}).Incident(inc)
				obs = clean.Observations
				inc = clean
				fmt.Fprint(w, rep.String())
				fmt.Fprintln(w)
			}

			factory := func(cs ...collect.Collector) api.Investigator { return newEngine(cs...) }

			// What does the engine say about it today?
			eng := factory(record.NewReplayCollector(obs))
			res, err := eng.Investigate(cmd.Context(), &api.InvestigationRequest{
				// The replay collector serves the whole record regardless of
				// target; benchmark.RunScenario uses the same placeholder, so
				// the draft is scored exactly as the suite will score it.
				Target: model.ResourceRef{Kind: "pod", Name: "scenario"},
			})
			if err != nil {
				return fmt.Errorf("replay: %w", err)
			}
			top := topHypothesis(res)
			if top == nil {
				fmt.Fprintf(w, "The engine reaches no hypothesis on this record.\n")
				fmt.Fprintf(w, "That is a legitimate scenario - a false-positive control - but it is\n")
				fmt.Fprintf(w, "probably not what you recorded. Check the record carries the failure.\n\n")
			}

			sweep, err := benchmark.Sweep(cmd.Context(), obs, factory)
			if err != nil {
				return fmt.Errorf("evidence sweep: %w", err)
			}

			// Write the scenario directory.
			target := filepath.Join(dir, name)
			if _, statErr := os.Stat(target); statErr == nil && !forceWrite {
				return fmt.Errorf("%s already exists (pass --force to overwrite)", target)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			inc.ID = "record"
			if _, err := record.NewStore(target).Save(inc); err != nil {
				return fmt.Errorf("write record.jsonl: %w", err)
			}
			// Save() writes 0600 for incident records because they may carry
			// live cluster facts; a sanitised scenario is committed to a public
			// repo, so it reads like the rest of the suite.
			recordPath := filepath.Join(target, "record.jsonl")
			if err := os.Chmod(recordPath, 0o644); err != nil {
				return err
			}

			yaml := scenarioDraft(name, description, res, sweep, len(obs))
			specPath := filepath.Join(target, "scenario.yaml")
			if err := os.WriteFile(specPath, []byte(yaml), 0o644); err != nil {
				return err
			}

			renderSweep(w, sweep)
			fmt.Fprintf(w, "\nwrote %s\n     %s\n\n", recordPath, specPath)
			fmt.Fprintf(w, "NEXT - the draft carries the engine's own answer, not ground truth:\n")
			fmt.Fprintf(w, "  1. Is %q actually what happened? If not, correct top_hypothesis_category\n",
				sweep.BaselineCategory)
			fmt.Fprintf(w, "     and mark the scenario advisory - a miss the suite records is the\n")
			fmt.Fprintf(w, "     most valuable case it can hold.\n")
			fmt.Fprintf(w, "  2. Replace every TODO in %s.\n", specPath)
			fmt.Fprintf(w, "  3. Keep the mutations you believe; delete the rest. Each one is a claim\n")
			fmt.Fprintf(w, "     the benchmark will hold you to.\n")
			fmt.Fprintf(w, "  4. kubetective benchmark %s\n", dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "scenario directory name (default: derived from the record target)")
	cmd.Flags().StringVar(&dir, "dir", "scenarios", "suite directory to write into")
	cmd.Flags().StringVar(&description, "description", "", "one-line description for the draft")
	cmd.Flags().BoolVar(&keepImages, "keep-images", false, "keep container image references (registry hostnames identify the org)")
	cmd.Flags().BoolVar(&noSanitize, "no-sanitize", false, "skip redaction - only for records you have read in full")
	cmd.Flags().BoolVar(&forceWrite, "force", false, "overwrite an existing scenario directory")
	return cmd
}

// renderSweep prints which evidence the verdict rests on.
func renderSweep(w io.Writer, s *benchmark.SweepResult) {
	fmt.Fprintf(w, "VERDICT   %s (%.0f%%)\n\n", s.BaselineCategory, s.BaselineScore*100)
	fmt.Fprintf(w, "EVIDENCE SWEEP - what the verdict rests on\n")
	fmt.Fprintf(w, "  %-24s %-6s %s\n", "REMOVE KIND", "OBS", "VERDICT BECOMES")
	for _, e := range s.Entries {
		marker := "  "
		note := ""
		switch {
		case e.LoadBearing:
			marker = "→ "
			note = "  load-bearing"
		case e.Weakening:
			marker = "~ "
			note = "  weakens only"
		}
		fmt.Fprintf(w, "%s%-24s %-6d %s (%.0f%%)%s\n", marker, e.Kind, e.Removed, e.Category, e.Score*100, note)
	}
}

// scenarioDraft renders the scenario.yaml stub.
func scenarioDraft(name, description string, res *api.InvestigationResult, s *benchmark.SweepResult, obsCount int) string {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	p("# DRAFT - generated by `kubetective scenario new`.\n")
	p("#\n")
	p("# Every value below is what the engine ALREADY concludes about this record.\n")
	p("# That is not ground truth, and committing it unedited turns this scenario\n")
	p("# into a tautology: it can only ever confirm the engine's current opinion.\n")
	p("#\n")
	p("# Decide what actually happened, then correct this file. If the engine got\n")
	p("# it wrong, keep the true answer and add `advisory: true` - a recorded miss\n")
	p("# is worth more to the suite than another case it passes, because\n")
	p("# confidence cannot be calibrated against a benchmark that never fails.\n")
	p("#\n")
	p("# Source: %d observations.\n", obsCount)
	p("\n")
	p("name: %s\n", name)
	if description == "" {
		description = "TODO: one paragraph - what broke, what the evidence shows, and why this case is worth holding the engine to."
	}
	p("description: >-\n  %s\n", description)
	p("\n")
	p("ground_truth:\n")
	p("  root_cause: \"TODO: the real root cause, in a sentence\"\n")

	if s.BaselineCategory == "-" {
		p("  # The engine reached no hypothesis. If that is correct, this is a\n")
		p("  # false-positive control; if not, the record is missing the failure.\n")
		p("  expect_no_findings: true\n")
		p("  expected_status: INVESTIGATED\n")
	} else {
		p("  top_hypothesis_category: %s   # OBSERVED - verify\n", s.BaselineCategory)
		p("  min_score: %.2f                # OBSERVED %.0f%%, floored a little for headroom\n",
			floorScore(s.BaselineScore), s.BaselineScore*100)
		if analyzers := findingAnalyzers(res); len(analyzers) > 0 {
			p("  expected_finding_analyzers:\n")
			for _, a := range analyzers {
				p("    - %s\n", a)
			}
		}
		if res.Incident != nil && res.Incident.Status != "" {
			p("  expected_status: %s\n", res.Incident.Status)
		}
		p("\n")
		p("  # If the evidence genuinely does not settle this incident, prefer hedging\n")
		p("  # over correctness - uncomment these and drop min_score:\n")
		p("  # advisory: true\n")
		p("  # max_score: 0.95\n")
		p("  # expect_competing: true\n")
	}

	load := loadBearing(s)
	p("\n")
	if len(load) == 0 {
		p("# No single observation kind changes the verdict. That is worth\n")
		p("# understanding before committing: either the conclusion is over-determined\n")
		p("# (several independent routes to the same answer, which is fine and worth\n")
		p("# asserting with expect_category), or it does not depend on the evidence at\n")
		p("# all, which is a bug in an analyzer.\n")
		p("# mutations: []\n")
		return b.String()
	}

	p("# Suggested mutations, from the evidence sweep. Each is a causal claim the\n")
	p("# benchmark will hold you to: delete this evidence, and the verdict must\n")
	p("# move as stated. Keep the ones you believe; delete the rest. A mutation you\n")
	p("# cannot justify in the `reason` field is one you should not ship.\n")
	p("mutations:\n")
	for _, e := range load {
		p("  - name: without-%s\n", slugify(strings.ReplaceAll(e.Kind, ".", "-")))
		p("    reason: >-\n")
		p("      TODO: why does removing %s change the answer? Name the mechanism,\n", e.Kind)
		p("      not the observation.\n")
		p("    remove_kinds: [%s]\n", e.Kind)
		if e.Category == "-" {
			p("    expect_no_findings: true\n")
		} else {
			p("    expect_category: %s\n", e.Category)
		}
	}
	return b.String()
}

// floorScore picks a min_score strictly below the observed one, on a 0.05
// grid. Strictly below matters: a threshold equal to the observed score fails
// on any downward drift at all, which is the opposite of the headroom a
// contributed scenario needs against unrelated scoring changes.
func floorScore(s float64) float64 {
	n := int(math.Round(s * 20))
	if float64(n)/20 >= s {
		n--
	}
	if n < 0 {
		return 0
	}
	return float64(n) / 20
}

func loadBearing(s *benchmark.SweepResult) []benchmark.SweepEntry {
	var out []benchmark.SweepEntry
	for _, e := range s.Entries {
		if e.LoadBearing {
			out = append(out, e)
		}
	}
	return out
}

// findingAnalyzers lists the analyzers that fired, in stable order.
func findingAnalyzers(res *api.InvestigationResult) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range res.Findings {
		if f.Analyzer != "" && !seen[f.Analyzer] {
			seen[f.Analyzer] = true
			out = append(out, f.Analyzer)
		}
	}
	return out
}
