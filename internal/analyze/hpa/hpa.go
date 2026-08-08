// Package hpa implements the HorizontalPodAutoscaler analyzer: it activates
// on hpa.state observations and flags when the workload is pinned at
// maxReplicas — the capacity-ceiling context that amplifies per-pod failures.
package hpa

import (
	"context"
	"fmt"
	"strings"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/score"
)

const (
	weightAtMax = 30.0
	weightScale = 15.0 // scaling attempted but pinned
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "hpa" }
func (a *Analyzer) Name() string { return "Horizontal Autoscaling" }

func (a *Analyzer) Supports(o model.Observation) bool {
	return o.Kind == "hpa.state"
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, o := range in.Observations {
		if !a.Supports(o) {
			continue
		}
		atMax, _ := o.Payload["at_max"].(bool)
		if !atMax {
			continue
		}
		max, _ := analyze.PayloadInt64(o.Payload, "max_replicas")
		current, _ := analyze.PayloadInt64(o.Payload, "current_replicas")
		desired, _ := analyze.PayloadInt64(o.Payload, "desired_replicas")
		workload, _ := o.Payload["workload"].(string)

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("hpa.%s", o.Resource.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevWarning,
			Title:       "HPA at max replicas",
			Description: fmt.Sprintf("workload %s pinned at %d replicas (desired %d) — scale-out is exhausted", workload, current, desired),
			Timestamp:   o.Timestamp,
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		eAtMax := model.Evidence{
			ID:     fmt.Sprintf("hpa.%s.atmax", o.Resource.Name),
			Claim:  "replicas at maximum",
			Weight: weightAtMax, Strength: 1.0,
		}
		evs = append(evs, eAtMax)
		terms = append(terms, score.EvidenceTerm{ID: eAtMax.ID, Label: fmt.Sprintf("replicas at max (%d/%d)", current, max), Weight: weightAtMax, Strength: 1.0, Polarity: +1})

		if desired < max && desired > 0 {
			eScale := model.Evidence{
				ID:     fmt.Sprintf("hpa.%s.scaling", o.Resource.Name),
				Claim:  "scaling in progress",
				Weight: weightScale, Strength: 1.0,
			}
			evs = append(evs, eScale)
			terms = append(terms, score.EvidenceTerm{ID: eScale.ID, Label: fmt.Sprintf("scaling toward %d replicas", desired), Weight: weightScale, Strength: 1.0, Polarity: +1})
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("hpa.%s", o.Resource.Name),
			Claim:    fmt.Sprintf("Workload %s is pinned at the HPA maximum (%d replicas)", workload, max),
			Category: model.CatHPA,
			Status:   model.StatusCandidate, // context, not a root cause by itself
			Score:    breakdown(terms),
		}
		for _, e := range evs {
			h.Evidence = append(h.Evidence, e.ID)
		}
		hypotheses = append(hypotheses, h)
		evidence = append(evidence, evs...)
	}
	return findings, hypotheses, evidence, nil
}

func breakdown(terms []score.EvidenceTerm) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, 0)
	return &bd
}

func (a *Analyzer) NeedsEvidence(_ model.Hypothesis) []analyze.EvidenceRequest { return nil }

func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s: %s", f.Title, strings.ToLower(f.Description))
}
