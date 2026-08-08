package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/GlediLami/kubetective/internal/benchmark"
	"github.com/GlediLami/kubetective/internal/config"
	"github.com/GlediLami/kubetective/internal/diag"
	"github.com/GlediLami/kubetective/internal/llm"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/score"
	"github.com/GlediLami/kubetective/pkg/api"
)

// renderText renders the narrow, human-visible slice of the result:
// header card → ROOT CAUSE → EVIDENCE → TIMELINE → WHAT CHANGED →
// RELATIONSHIPS → GAPS → RECOMMENDATION → AI SYNTHESIS (optional).
// "Collect more than you show".
func renderText(res *api.InvestigationResult, explanation *llm.Explanation) error {
	w := os.Stdout
	inc := res.Incident

	fmt.Fprintln(w, "╭──────────────────────────────────────────────────╮")
	if inc != nil {
		fmt.Fprintf(w, "│ INCIDENT: %-41s│\n", truncate(inc.Target.String(), 41))
		fmt.Fprintf(w, "│ Status: %-44s│\n", truncate(inc.Status, 44))
		fmt.Fprintf(w, "│ Severity: %-43s│\n", truncate(string(inc.Severity), 43))
		if inc.Confidence > 0 {
			fmt.Fprintf(w, "│ Confidence: %-41.0f%%│\n", inc.Confidence*100)
		}
	}
	fmt.Fprintln(w, "╰──────────────────────────────────────────────────╯")

	if top := topHypothesis(res); top != nil {
		fmt.Fprintln(w, "\nROOT CAUSE")
		fmt.Fprintf(w, "  %s\n", top.Claim)
		fmt.Fprintln(w, "\nEVIDENCE")
		for _, line := range top.Score.Lines {
			mark := "✓"
			if line.Delta < 0 {
				mark = "✗"
			}
			fmt.Fprintf(w, "  %s %s (%+.0f)\n", mark, line.Label, line.Delta)
		}
		if top.Score.GapPenalty > 0 {
			fmt.Fprintf(w, "  ! missing-evidence penalty (%.0f)\n", -top.Score.GapPenalty)
		}
		if len(top.Missing) > 0 {
			fmt.Fprintf(w, "  ! missing expected evidence: %d item(s)\n", len(top.Missing))
		}
	}

	if len(res.Timeline) > 0 {
		fmt.Fprintln(w, "\nTIMELINE")
		shown := 0
		for _, ev := range res.Timeline {
			if shown >= 12 {
				fmt.Fprintf(w, "  … (%d more)\n", len(res.Timeline)-shown)
				break
			}
			shown++
			fmt.Fprintf(w, "  %s  (t%+dm)  %s %s\n",
				ev.Observation.Timestamp.Format("15:04:05"),
				int(ev.Offset.Minutes()),
				ev.Observation.Kind,
				shortPayload(ev.Observation.Payload),
			)
		}
	}

	if len(res.Changes) > 0 {
		fmt.Fprintln(w, "\nWHAT CHANGED")
		shown := 0
		for _, ch := range res.Changes {
			if shown >= 5 {
				break
			}
			shown++
			fmt.Fprintf(w, "  %d. %s - %s (relevance %.0f%%)\n",
				shown, ch.Resource.String(), ch.Description, ch.Relevance*100)
		}
	}

	if res.Graph != nil && len(res.Graph.Edges) > 0 {
		fmt.Fprintln(w, "\nRELATIONSHIPS")
		shown := 0
		for _, e := range res.Graph.Edges {
			if shown >= 8 {
				fmt.Fprintf(w, "  … (%d more)\n", len(res.Graph.Edges)-shown)
				break
			}
			shown++
			fmt.Fprintf(w, "  %s --%s--> %s\n", e.From.String(), e.Kind, e.To.String())
		}
		if res.Graph.Bounds.Truncated {
			fmt.Fprintf(w, "  ! graph truncated at budget (%s)\n", res.Graph.Bounds.TruncatedKind)
		}
	}

	if len(res.EvidenceGaps) > 0 {
		fmt.Fprintln(w, "\nEVIDENCE GAPS")
		for _, g := range res.EvidenceGaps {
			fmt.Fprintf(w, "  ! %s [%s]\n", g.Description, g.Category)
		}
	}

	if len(res.Recommendations) > 0 {
		fmt.Fprintln(w, "\nRECOMMENDATION")
		for _, r := range res.Recommendations {
			fmt.Fprintf(w, "  %s [%s] - %s\n", r.Action, r.Risk, r.Reason)
		}
	}

	// AI synthesis: clearly separate from the deterministic verdict, and the
	// model's own confidence is never confused with the engine's.
	if explanation != nil {
		fmt.Fprintln(w, "\nAI SYNTHESIS (non-authoritative)")
		fmt.Fprintf(w, "  Summary: %s\n", explanation.Summary)
		fmt.Fprintf(w, "  %s\n", explanation.Explanation)
		if explanation.Uncertainty != "" {
			fmt.Fprintf(w, "  Uncertainty: %s\n", explanation.Uncertainty)
		}
		if len(explanation.FollowUps) > 0 {
			fmt.Fprintf(w, "  Follow-ups: %s\n", stringsJoin(explanation.FollowUps, "; "))
		}
		if explanation.RecommendedAction != nil {
			fmt.Fprintf(w, "  Suggested action: %s\n", *explanation.RecommendedAction)
		}
		fmt.Fprintf(w, "  Model confidence: %.0f%% (engine confidence above is authoritative)\n", explanation.ConfidenceInOwnAnswer*100)
	}

	fmt.Fprintf(w, "\n%d observations · %d sources · %s",
		len(res.Observations), len(res.Meta.SourcesHit), res.Meta.Duration.Round(time.Millisecond))
	if res.Meta.RecordID != "" {
		fmt.Fprintf(w, "\nrecord: %s (replay with: kubetective replay <incident-id>)", res.Meta.RecordID)
	}
	fmt.Fprintln(w)
	return nil
}

// renderJSON emits the complete InvestigationResult for scripting.
func renderJSON(res *api.InvestigationResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// renderBenchmark prints the scenario gate table + confidence calibration.
func renderBenchmark(suite *benchmark.SuiteResult) error {
	w := os.Stdout
	results := suite.Results
	fmt.Fprintf(w, "%-28s %-8s %-9s %-7s %s\n", "SCENARIO", "STATUS", "SCORE", "TIME", "FAILURES")
	total, passed := 0, 0
	for _, r := range results {
		total++
		status, score := "PASS", fmt.Sprintf("%.0f%%", r.Score*100)
		if !r.Passed {
			status = "FAIL"
			if r.Score == 0 {
				score = "-"
			}
		} else {
			passed++
		}
		fmt.Fprintf(w, "%-28s %-8s %-9s %-7s %s\n", r.Scenario, status, score, r.Duration.Round(time.Millisecond), stringsJoin(r.Reasons, "; "))
	}
	fmt.Fprintf(w, "\n%d/%d scenarios passed\n", passed, total)

	if c := suite.Calibration; c != nil {
		fmt.Fprintf(w, "calibration: %d ground-truth points, accuracy %.0f%% - ECE %.1f%% @T=26 → ECE %.1f%% @T=%.1f\n",
			c.Points, c.Accuracy*100, c.DefaultECE*100, c.ECE*100, c.Temperature)
		if l := suite.LOO; l != nil {
			adopt := "not adopted"
			if l.Adopt && c.Points >= 10 {
				adopt = "adopted - persisted to " + config.Path()
				if err := config.Save(config.Config{Temperature: l.Temperature}); err == nil {
					score.SetTemperature(l.Temperature)
				}
			}
			fmt.Fprintf(w, "  hardening: LOO ECE %.1f%% (vs %.1f%% @T=26) → recalibrated T=%.1f %s\n",
				l.LOOECE*100, l.DefaultECE*100, l.Temperature, adopt)
		}
		// Calibration rule: ECE > 0.1 → displayed confidence is dampened
		// toward the conservative 50% default.
		if c.DefaultECE > 0.10 {
			fmt.Fprintf(w, "  warning: ECE %.1f%% exceeds 10%% - displayed confidence dampened toward 50%% (score.Dampen)\n", c.DefaultECE*100)
		}
		if c.Points < 10 {
			fmt.Fprintf(w, "  (advisory: <10 points - the fitted temperature is not yet trustworthy; adoption is skipped below 10 points)\n")
		}
	}

	if passed != total {
		return fmt.Errorf("benchmark gate: %d scenario(s) failed", total-passed)
	}

	// Calibration hardening: adopt a leave-one-out-validated temperature
	//, persisted for every future invocation - but only
	// when the gate passed, so a failing suite never mutates engine config.
	if c := suite.Calibration; c != nil && suite.LOO != nil && suite.LOO.Adopt && c.Points >= 10 {
		if err := config.Save(config.Config{Temperature: suite.LOO.Temperature}); err == nil {
			score.SetTemperature(suite.LOO.Temperature)
			fmt.Fprintf(w, "adopted calibrated temperature T=%.1f (persisted to %s)\n", suite.LOO.Temperature, config.Path())
		}
	}
	return nil
}

// renderDoctor prints the doctor report; exits non-zero only on failures.
func renderDoctor(rep *diag.Report) error {
	return renderDoctorReport(os.Stdout, rep)
}

// renderDoctorReport is the testable core: exit behavior is a returned
// error, output goes to the given writer.
func renderDoctorReport(w *os.File, rep *diag.Report) error {
	fmt.Fprintf(w, "kubetective %s\n", rep.EngineVersion)
	n := 0
	for _, c := range rep.Checks {
		if n > 0 {
			fmt.Fprintln(w)
		}
		n++
		fmt.Fprintf(w, "  %s  %s\n", c.Level, c.Name)
		fmt.Fprintf(w, "       %s\n", c.Detail)
	}
	fmt.Fprintln(w)
	if rep.Failing() {
		return fmt.Errorf("doctor found failures - see the FAIL checks above")
	}
	fmt.Fprintln(w, "all checks passed")
	return nil
}

func stringsJoin(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func topHypothesis(res *api.InvestigationResult) *model.Hypothesis {
	var top *model.Hypothesis
	for i := range res.Hypotheses {
		h := &res.Hypotheses[i]
		if h.Score == nil {
			continue
		}
		if top == nil || h.Score.Score > top.Score.Score {
			top = h
		}
	}
	return top
}

func shortPayload(p map[string]any) string {
	if p == nil {
		return ""
	}
	if r, ok := p["reason"].(string); ok && r != "" {
		return r
	}
	if p["phase"] != nil {
		return fmt.Sprintf("%v", p["phase"])
	}
	return ""
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
