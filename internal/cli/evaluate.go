package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/GlediLami/kubetective/internal/benchmark"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/config"
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
			// temperature, persisted for every future
			// invocation (server/MCP/CLI all load it at startup).
			if c := suite.Calibration; c != nil && suite.LOO != nil && suite.LOO.Adopt && c.Points >= 10 {
				if serr := config.Save(config.Config{Temperature: suite.LOO.Temperature}); serr == nil {
					score.SetTemperature(suite.LOO.Temperature)
				}
			}
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
			for _, r := range suite.Results {
				if !r.Passed {
					failures++
				}
			}
			if failures > 0 {
				return fmt.Errorf("evaluation gate: %d/%d scenarios failed", failures, len(suite.Results))
			}
			if n := suite.UnsafeActionCount(); n > 0 {
				return fmt.Errorf("evaluation gate: %d unsafe action(s) planned on healthy scenarios (must be 0)", n)
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
		if !r.Passed {
			status = "FAIL"
		} else {
			passed++
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
		p("| top-1 accuracy | %.0f%% |\n", c.Accuracy*100)
		p("| ECE @ default T=26 | %.1f%% |\n", c.DefaultECE*100)
		p("| ECE @ fitted T=%.1f | %.1f%% |\n", c.Temperature, c.ECE*100)
		if l := suite.LOO; l != nil {
			p("| LOO ECE (hardening) | %.1f%% |\n", l.LOOECE*100)
			adopt := "no"
			if l.Adopt {
				adopt = "yes"
			}
			p("| recalibrated T adopted | %s |\n", adopt)
		}
		if c.DefaultECE > 0.10 {
			p("\n> ⚠️ ECE exceeds 10%% - displayed confidence is dampened toward 50%% (calibration rule).\n")
		}
	} else {
		p("_No scenario carries ground truth - calibration not computable._\n")
	}
	p("\n")

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
