// Package pvc implements the PersistentVolumeClaim analyzer: it activates on
// pvc.state observations and builds the "volume cannot bind" hypothesis when
// the claim is Pending/Lost or binding events report failures.
package pvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/score"
)

const (
	weightPending = 30.0
	weightEvent   = 20.0 // FailedBinding / FailedMount event
	weightRequest = 10.0 // requested storage
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "pvc" }
func (a *Analyzer) Name() string { return "PersistentVolumeClaim Binding" }

func (a *Analyzer) Supports(o model.Observation) bool {
	if o.Kind == "pvc.state" {
		phase, _ := o.Payload["phase"].(string)
		return phase == "Pending" || phase == "Lost"
	}
	if o.Kind == "event.recorded" {
		reason, _ := o.Payload["reason"].(string)
		return reason == "FailedBinding" || reason == "FailedMount" || reason == "ProvisioningFailed"
	}
	return false
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	type claim struct {
		state   model.Observation
		events  []model.Observation
		request string
	}
	claims := map[string]*claim{}
	var order []string

	for _, o := range in.Observations {
		if o.Kind == "pvc.state" && a.Supports(o) {
			key := o.Resource.String()
			if claims[key] == nil {
				claims[key] = &claim{}
				order = append(order, key)
			}
			claims[key].state = o
			if r, ok := o.Payload["requested"].(string); ok {
				claims[key].request = r
			}
		}
	}
	for _, o := range in.Observations {
		if !a.Supports(o) {
			continue
		}
		// FailedBinding events target the PVC resource (or the pod volume).
		if c := claims[o.Resource.String()]; c != nil {
			c.events = append(c.events, o)
		}
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, key := range order {
		c := claims[key]
		res := c.state.Resource
		phase, _ := c.state.Payload["phase"].(string)
		msg := ""
		for _, e := range c.events {
			if m, ok := e.Payload["message"].(string); ok && m != "" {
				msg = m
				break
			}
		}

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("pvc.%s", res.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevHigh,
			Title:       fmt.Sprintf("PersistentVolumeClaim %s", phase),
			Description: msg,
			Timestamp:   c.state.Timestamp,
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		ePending := model.Evidence{
			ID:     fmt.Sprintf("pvc.%s.pending", res.Name),
			Claim:  "claim not bound",
			Weight: weightPending, Strength: 1.0,
		}
		evs = append(evs, ePending)
		terms = append(terms, score.EvidenceTerm{ID: ePending.ID, Label: fmt.Sprintf("claim phase %s (not Bound)", phase), Weight: weightPending, Strength: 1.0, Polarity: +1})

		if len(c.events) > 0 {
			eEvent := model.Evidence{
				ID:     fmt.Sprintf("pvc.%s.event", res.Name),
				Claim:  "binding failure event",
				Weight: weightEvent, Strength: 1.0,
			}
			evs = append(evs, eEvent)
			label := "binding failure event"
			if msg != "" {
				label = fmt.Sprintf("binding: %s", truncate(msg, 80))
			}
			terms = append(terms, score.EvidenceTerm{ID: eEvent.ID, Label: label, Weight: weightEvent, Strength: 1.0, Polarity: +1})
		}
		if c.request != "" {
			eReq := model.Evidence{
				ID:     fmt.Sprintf("pvc.%s.request", res.Name),
				Claim:  "requested storage",
				Weight: weightRequest, Strength: 1.0,
			}
			evs = append(evs, eReq)
			terms = append(terms, score.EvidenceTerm{ID: eReq.ID, Label: fmt.Sprintf("requested storage: %s", c.request), Weight: weightRequest, Strength: 1.0, Polarity: +1})
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("pvc.%s", res.Name),
			Claim:    fmt.Sprintf("PersistentVolumeClaim cannot bind: %s", msg),
			Category: model.CatPVC,
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

func breakdown(terms []score.EvidenceTerm) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, 0)
	return &bd
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (a *Analyzer) NeedsEvidence(_ model.Hypothesis) []analyze.EvidenceRequest { return nil }

func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s: %s", f.Title, strings.ToLower(f.Description))
}
