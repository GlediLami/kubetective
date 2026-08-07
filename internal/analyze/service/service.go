// Package service implements the service-endpoints analyzer: it activates on
// service.state observations and builds the "service has no ready endpoints"
// hypothesis — the 503 / selector-mismatch root cause.
package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/score"
)

const (
	weightNoEndpoints = 30.0
	weightMismatch    = 20.0 // selector matches pods but endpoints are empty
	weightPods        = 10.0 // healthy pods that should be endpoints
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "service" }
func (a *Analyzer) Name() string { return "Service Endpoints" }

func (a *Analyzer) Supports(o model.Observation) bool {
	return o.Kind == "service.state"
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	type svcState struct {
		obs    model.Observation
		readyPods int
	}
	svcs := map[string]*svcState{}
	var order []string

	for _, o := range in.Observations {
		if !a.Supports(o) {
			continue
		}
		key := o.Resource.String()
		if svcs[key] == nil {
			svcs[key] = &svcState{}
			order = append(order, key)
		}
		svcs[key].obs = o
	}
	// Healthy running pods in the same namespace (potential endpoints).
	for _, o := range in.Observations {
		if o.Kind != "pod.state" {
			continue
		}
		phase, _ := o.Payload["phase"].(string)
		if phase != "Running" {
			continue
		}
		for _, s := range svcs {
			if s.obs.Resource.Namespace == o.Resource.Namespace {
				s.readyPods++
			}
		}
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, key := range order {
		s := svcs[key]
		res := s.obs.Resource
		ready, _ := analyze.PayloadInt64(s.obs.Payload, "ready_endpoints")
		total, _ := analyze.PayloadInt64(s.obs.Payload, "total_endpoints")
		if ready > 0 {
			continue // endpoints exist — nothing to diagnose
		}
		selector, _ := analyze.PayloadStringMap(s.obs.Payload, "selector")

		severity := model.SevWarning
		title := "Service has no ready endpoints"
		if len(selector) > 0 && s.readyPods > 0 {
			severity = model.SevHigh
			title = "Service selector mismatch — ready pods not in endpoints"
		}

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("service.%s", res.Name),
			Analyzer:    a.ID(),
			Severity:    severity,
			Title:       title,
			Description: fmt.Sprintf("service %s: %d endpoints (0 ready) while %d pod(s) run", res.Name, total, s.readyPods),
			Timestamp:   s.obs.Timestamp,
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		eNoEndpoints := model.Evidence{
			ID:     fmt.Sprintf("service.%s.noendpoints", res.Name),
			Claim:  "zero ready endpoints",
			Weight: weightNoEndpoints, Strength: 1.0,
		}
		evs = append(evs, eNoEndpoints)
		terms = append(terms, score.EvidenceTerm{ID: eNoEndpoints.ID, Label: fmt.Sprintf("0 ready endpoints (of %d)", total), Weight: weightNoEndpoints, Strength: 1.0, Polarity: +1})

		if len(selector) > 0 && s.readyPods > 0 {
			eMismatch := model.Evidence{
				ID:     fmt.Sprintf("service.%s.mismatch", res.Name),
				Claim:  "selector matches running pods, endpoints empty",
				Weight: weightMismatch, Strength: 1.0,
			}
			evs = append(evs, eMismatch)
			terms = append(terms, score.EvidenceTerm{ID: eMismatch.ID, Label: fmt.Sprintf("selector matches %d running pod(s) but endpoints are empty", s.readyPods), Weight: weightMismatch, Strength: 1.0, Polarity: +1})
		}
		if len(selector) > 0 {
			ePods := model.Evidence{
				ID:     fmt.Sprintf("service.%s.selector", res.Name),
				Claim:  "service selector",
				Weight: weightPods, Strength: 1.0,
			}
			evs = append(evs, ePods)
			terms = append(terms, score.EvidenceTerm{ID: ePods.ID, Label: fmt.Sprintf("selector: %s", fmtSelector(selector)), Weight: weightPods, Strength: 1.0, Polarity: +1})
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("service.%s", res.Name),
			Claim:    fmt.Sprintf("Service %s has no ready endpoints (%d pod(s) running)", res.Name, s.readyPods),
			Category: model.CatService,
			Status:   model.StatusLikely,
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

func fmtSelector(sel map[string]string) string {
	parts := make([]string, 0, len(sel))
	for k, v := range sel {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ", ")
}

func breakdown(terms []score.EvidenceTerm) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, 0)
	return &bd
}

func (a *Analyzer) NeedsEvidence(_ model.Hypothesis) []analyze.EvidenceRequest { return nil }

func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s: %s", f.Title, f.Description)
}
