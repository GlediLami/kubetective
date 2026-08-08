// Package probe implements the probe-failure analyzer: it activates on
// Unhealthy events (liveness/readiness probe failures) and builds the
// "probe failing" hypothesis — the most common cause of unnecessary restarts
// and traffic loss.
package probe

import (
	"context"
	"fmt"
	"strings"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/score"
)

const (
	weightEvent   = 30.0
	weightMessage = 20.0
	weightRestart = 10.0
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "probe" }
func (a *Analyzer) Name() string { return "Probe Failures" }

func (a *Analyzer) Supports(o model.Observation) bool {
	if o.Kind != "event.recorded" {
		return false
	}
	reason, _ := o.Payload["reason"].(string)
	if reason == "Unhealthy" {
		return true
	}
	msg, _ := o.Payload["message"].(string)
	return reason == "ProbeError" || strings.Contains(strings.ToLower(msg), "probe failed")
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	type probeFailure struct {
		events   []model.Observation
		restarts int64
	}
	groups := map[string]*probeFailure{}
	var order []string
	for _, o := range in.Observations {
		if !a.Supports(o) {
			continue
		}
		key := o.Resource.String()
		if groups[key] == nil {
			groups[key] = &probeFailure{}
			order = append(order, key)
		}
		groups[key].events = append(groups[key].events, o)
	}
	for _, o := range in.Observations {
		g := groups[o.Resource.String()]
		if g == nil {
			continue
		}
		if n, ok := analyze.PayloadInt64(o.Payload, "restarts"); ok && o.Kind == "container.running" {
			g.restarts = n
		}
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, key := range order {
		g := groups[key]
		res := g.events[len(g.events)-1].Resource
		last := g.events[len(g.events)-1]
		msg, _ := last.Payload["message"].(string)

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("probe.%s", res.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevHigh,
			Title:       "Probe failing",
			Description: msg,
			Timestamp:   last.Timestamp,
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		eEvent := model.Evidence{
			ID:     fmt.Sprintf("probe.%s.event", res.Name),
			Claim:  "kubelet reports unhealthy container",
			Weight: weightEvent, Strength: 1.0,
		}
		evs = append(evs, eEvent)
		terms = append(terms, score.EvidenceTerm{ID: eEvent.ID, Label: fmt.Sprintf("Unhealthy event ×%d", len(g.events)), Weight: weightEvent, Strength: 1.0, Polarity: +1})

		if msg != "" {
			eMsg := model.Evidence{
				ID:     fmt.Sprintf("probe.%s.message", res.Name),
				Claim:  "probe failure message",
				Weight: weightMessage, Strength: 1.0,
			}
			evs = append(evs, eMsg)
			terms = append(terms, score.EvidenceTerm{ID: eMsg.ID, Label: fmt.Sprintf("probe: %s", truncate(msg, 80)), Weight: weightMessage, Strength: 1.0, Polarity: +1})
		}
		if g.restarts > 0 {
			eRestart := model.Evidence{
				ID:     fmt.Sprintf("probe.%s.restart", res.Name),
				Claim:  "restarts follow probe failures",
				Weight: weightRestart, Strength: 1.0,
			}
			evs = append(evs, eRestart)
			terms = append(terms, score.EvidenceTerm{ID: eRestart.ID, Label: fmt.Sprintf("restarted after probe failures (×%d)", g.restarts), Weight: weightRestart, Strength: 1.0, Polarity: +1})
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("probe.%s", res.Name),
			Claim:    fmt.Sprintf("Probe failing: %s", msg),
			Category: model.CatProbe,
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
	return fmt.Sprintf("%s: %s", f.Title, f.Description)
}
