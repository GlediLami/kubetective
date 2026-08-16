package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/GlediLami/kubetective/internal/benchmark"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/engine"
	"github.com/GlediLami/kubetective/internal/score"
	"github.com/GlediLami/kubetective/pkg/api"
)

// newEvaluateCmd renders the full evaluation report (v0.7): per-scenario results, per-category accuracy, calibration incl.
// leave-one-out hardening, and the false-positive check. Exit code 1 if the
// gate fails - the CI entry point.
func newEvaluateCmd() *cobra.Command {
	var (
		scenariosDir string
		outFile      string
	)
	cmd := &cobra.Command{
		Use:   "evaluate [suite]",
		Short: "Render the evaluation report (markdown) and enforce the gate",
		Long: `Runs the full scenario suite and renders a markdown evaluation report:
per-scenario results, per-category accuracy, confidence calibration with
leave-one-out validation (v0.7 hardening), and the false-positive check.
Exit code 1 on any failure - CI gate.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				scenariosDir = args[0]
			}
			suite, err := benchmark.RunSuite(cmd.Context(), scenariosDir, func(cs ...collect.Collector) api.Investigator {
				return newEngine(cs...)
			})
			if err != nil {
				return err
			}
			// Calibration hardening: adopt a leave-one-out-validated
			// temperature, persisted for every future invocation (server/MCP/CLI
			// all load it at startup). adoptCalibration owns the whole rule.
			adoptCalibration(io.Discard, suite)
			report := evaluateMarkdown(suite)
			if outFile != "" {
				if werr := os.WriteFile(outFile, []byte(report), 0o644); werr != nil {
					return werr
				}
				fmt.Printf("evaluation report written to %s\n", outFile)
			} else {
				fmt.Print(report)
			}
			failures := 0
			for i := range suite.Results {
				if suite.Results[i].GateFailed() {
					failures++
				}
			}
			if failures > 0 {
				return fmt.Errorf("evaluation gate: %d/%d scenarios failed", failures, len(suite.Results))
			}
			if n := suite.UnsafeActionCount(); n > 0 {
				return fmt.Errorf("evaluation gate: %d unsafe action(s) planned on healthy scenarios (must be 0)", n)
			}
			if rb := suite.Robustness; rb != nil {
				if p, total := rb.MutationTotals(); p != total {
					return fmt.Errorf("mutation gate: %d/%d causal claims failed — a verdict survived the loss of its own evidence", total-p, total)
				}
				if s, total := rb.NoiseTotals(); s != total {
					return fmt.Errorf("noise gate: %d/%d verdicts changed under %d irrelevant observations", total-s, total, rb.NoiseN)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scenariosDir, "scenarios", "scenarios", "scenario suite directory")
	cmd.Flags().StringVar(&outFile, "out", "", "write the markdown report to this file instead of stdout")
	return cmd
}

// evaluateMarkdown renders the evaluation report.
func evaluateMarkdown(suite *benchmark.SuiteResult) string {
	var b []byte
	p := func(format string, args ...any) { b = append(b, []byte(fmt.Sprintf(format, args...))...) }

	p("# KubeTective Evaluation Report\n\n")
	p("- engine: `%s`\n", engine.Version)
	p("- date: %s\n", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))
	p("- suite: %d scenarios\n\n", len(suite.Results))

	// Per-scenario results.
	p("## Scenario results\n\n")
	p("| scenario | status | top category | score | time | failures |\n")
	p("|---|---|---|---:|---:|---|\n")
	passed, total := 0, len(suite.Results)
	for _, r := range suite.Results {
		status := "PASS"
		switch {
		case r.Passed:
			passed++
		case r.Advisory:
			status = "MISS (advisory)"
		default:
			status = "FAIL"
		}
		cat := r.ExpectedCategory
		if cat == "" {
			cat = "-"
		}
		fails := stringsJoin(r.Reasons, "; ")
		if fails == "" {
			fails = "-"
		}
		score := fmt.Sprintf("%.0f%%", r.Score*100)
		if r.Score == 0 {
			score = "-"
		}
		p("| %s | %s | %s | %s | %s | %s |\n", r.Scenario, status, cat, score, r.Duration.Round(time.Millisecond), fails)
	}
	p("\n**Gate: %d/%d scenarios passed**\n\n", passed, total)

	// Per-category accuracy.
	p("## Per-category accuracy (top-1)\n\n")
	p("| category | correct | total | accuracy |\n")
	p("|---|---:|---:|---:|\n")
	for _, c := range suite.ByCategory() {
		p("| %s | %d | %d | %.0f%% |\n", c.Category, c.Correct, c.Total, c.Accuracy*100)
	}
	p("\n")

	// Calibration incl. LOO hardening.
	p("## Confidence calibration\n\n")
	if c := suite.Calibration; c != nil {
		p("| metric | value |\n|---|---:|\n")
		p("| ground-truth points | %d |\n", c.Points)
		p("| of which incorrect | %d |\n", incorrectCount(suite))
		p("| top-1 accuracy | %.0f%% |\n", c.Accuracy*100)
		p("| ECE @ default T=%.0f | %.1f%% |\n", score.DefaultTemperature, c.DefaultECE*100)
		p("| ECE @ fitted T=%.1f | %.1f%% |\n", c.Temperature, c.ECE*100)
		if l := suite.LOO; l != nil {
			p("| out-of-sample NLL (fitted vs default) | %.4f vs %.4f |\n", l.LOONLL, l.DefaultNLL)
			p("| out-of-sample Brier (fitted vs default) | %.4f vs %.4f |\n", l.LOOBrier, l.DefaultBrier)
			p("| LOO ECE (reported, does not gate) | %.1f%% |\n", l.LOOECE*100)
			p("| scan grid | [%.1f, %.1f] |\n", l.GridLow, l.GridHigh)
			adopt := "no"
			if l.Adopt {
				adopt = "yes"
			}
			p("| recalibrated T adopted | %s |\n", adopt)
			p("| operating T | %.1f |\n", score.CurrentTemperature)
			p("\n> Adoption is decided on out-of-sample NLL **and** Brier, both of which must\n")
			p("> beat the default by at least 2%%. ECE is reported because it is the\n")
			p("> interpretable number — \"displayed confidence is off by this much\" — but it is\n")
			p("> a binned statistic, and gating on it made the decision turn on which side of a\n")
			p("> bucket edge a single scenario landed. See `score.adoptionRefusal`.\n")
			if !l.Adopt {
				p("\n> **Calibration refused:** %s\n>\n", l.RefusalReason)
				p("> The engine keeps its default temperature T=%.0f.\n", score.DefaultTemperature)
			}
		}
		if c.DefaultECE > 0.10 {
			p("\n> ⚠️ ECE exceeds 10%% - displayed confidence is dampened toward 50%% (calibration rule).\n")
		}
	} else {
		p("_No scenario carries ground truth - calibration not computable._\n")
	}
	p("\n")

	// Robustness: does the evidence cause the verdict, and does the verdict
	// survive cluster-scale volume?
	if rb := suite.Robustness; rb != nil {
		p("## Robustness\n\n")

		mPassed, mTotal := rb.MutationTotals()
		p("### Mutation gate (is the verdict caused by the evidence?)\n\n")
		if mTotal == 0 {
			p("_No mutations declared._\n\n")
		} else {
			p("Each mutation deletes the evidence a verdict rests on and requires the verdict to move.\n")
			p("A verdict that survives the loss of its own support was never caused by it.\n\n")
			p("| scenario | mutation | removed | verdict after | result |\n")
			p("|---|---|---:|---|---|\n")
			for _, r := range suite.Results {
				for _, m := range rb.Mutations[r.Scenario] {
					outcome := "✅ as declared"
					if !m.Passed {
						outcome = "❌ " + stringsJoin(m.Reasons, "; ")
					}
					p("| %s | %s | %d obs | %s (%.0f%%) | %s |\n",
						r.Scenario, m.Name, m.Removed, m.Category, m.Score*100, outcome)
				}
			}
			p("\n**Mutation gate: %d/%d held**\n\n", mPassed, mTotal)
		}

		nStable, nTotal := rb.NoiseTotals()
		p("### Noise gate (does the verdict survive scale?)\n\n")
		p("Every scenario is replayed buried under %d irrelevant observations from unrelated\n", rb.NoiseN)
		p("workloads, spread across the same window. Recorded scenarios carry 4–25 observations;\n")
		p("a production namespace carries thousands. This is the only gate that probes that gap.\n\n")
		p("| scenario | verdict | under noise | confidence drift | result |\n")
		p("|---|---|---|---:|---|\n")
		for _, n := range rb.Noise {
			base := n.BaselineCategory
			if base == "" {
				base = "(silent)"
			}
			outcome := "✅ held"
			if !n.Stable {
				outcome = "❌ " + n.Reason
			}
			p("| %s | %s | %s | %+.1f%% | %s |\n",
				n.Scenario, base, n.NoisyCategory, n.ScoreDrift()*100, outcome)
		}
		p("\n**Noise gate: %d/%d verdicts held at %d× scale**\n\n", nStable, nTotal, rb.NoiseN)
	}

	// False-positive check.
	p("## False-positive check\n\n")
	fp := suite.FalsePositiveCount()
	if fp == 0 {
		p("Healthy control stayed silent: **0 findings on %d healthy scenario(s)** ✅\n", healthyCount(suite))
	} else {
		p("**%d healthy scenario(s) produced findings - false positives!** ❌\n", fp)
	}
	p("\n")

	// Action safety (roadmap v1.0): planned remediation actions per scenario
	// and the unsafe-action rate (actions on healthy scenarios must be 0).
	p("## Action safety\n\n")
	p("| scenario | actions planned | types | unsafe on healthy |\n")
	p("|---|---:|---|---|\n")
	totalActions := 0
	for _, r := range suite.Results {
		types := make([]string, 0, len(r.PlannedActions))
		for _, a := range r.PlannedActions {
			types = append(types, string(a.Type))
			totalActions++
		}
		if len(types) == 0 {
			types = append(types, "-")
		}
		unsafe := "-"
		if r.ExpectNoFindings && len(r.PlannedActions) > 0 {
			unsafe = "YES"
		}
		p("| %s | %d | %s | %s |\n", r.Scenario, len(r.PlannedActions), stringsJoin(types, ", "), unsafe)
	}
	p("\n")
	unsafe := suite.UnsafeActionCount()
	if unsafe == 0 {
		p("Unsafe-action rate: **0/%d** ✅ (no remediation action is ever planned on a healthy scenario; applies are additionally human-gated with --yes and audited)\n", totalActions)
	} else {
		p("**Unsafe-action rate: %d/%d** ❌ - the gate fails.\n", unsafe, totalActions)
	}
	p("\n")
	p("---\n*Generated by `kubetective evaluate` - the CI gate (exit code 1 on failure).*\n")
	return string(b)
}

func healthyCount(suite *benchmark.SuiteResult) int {
	n := 0
	for _, r := range suite.Results {
		if r.ExpectNoFindings {
			n++
		}
	}
	return n
}
