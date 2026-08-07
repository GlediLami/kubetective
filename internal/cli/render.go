package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// renderText renders the narrow, human-visible slice of the result:
// header card → ROOT CAUSE → EVIDENCE → TIMELINE → GAPS → RECOMMENDATIONS.
// "Collect more than you show" (docs/DESIGN.md §9/§5.2).
func renderText(res *api.InvestigationResult) error {
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
	}

	if len(res.Timeline) > 0 {
		fmt.Fprintln(w, "\nTIMELINE")
		for _, ev := range res.Timeline {
			fmt.Fprintf(w, "  %s  %s\n", ev.Observation.Timestamp.Format("15:04"), ev.Observation.Kind)
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
			fmt.Fprintf(w, "  %s [%s] — %s\n", r.Action, r.Risk, r.Reason)
		}
	}

	if res.Meta.Duration > 0 {
		fmt.Fprintf(w, "\ninvestigation %s in %s (%d observations, %d sources)\n",
			res.Meta.RecordID, res.Meta.Duration.Round(time.Millisecond), len(res.Observations), len(res.Meta.SourcesHit))
	}
	return nil
}

// renderJSON emits the complete InvestigationResult for scripting.
func renderJSON(res *api.InvestigationResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
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

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

var _ = sort.Strings // reserved for change-ranking in v0.1 milestone

var _ io.Writer = os.Stdout
