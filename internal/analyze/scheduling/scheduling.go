// Package scheduling implements the unschedulable-pod analyzer: it activates
// on pod.state with phase Pending (or FailedScheduling events) and builds the
// "pod cannot be scheduled" hypothesis with the scheduler's message as the
// key evidence.
package scheduling

import (
	"context"
	"fmt"
	"strings"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/score"
)

const (
	weightPending = score.WeightPrimary
	weightMessage = score.WeightCorroborating
	weightRequest = score.WeightContextual
	weightPVC     = score.WeightCorroborating // contradiction: an unbound claim is the more specific reason to be Pending
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "scheduling" }
func (a *Analyzer) Name() string { return "Scheduling / Pending Pods" }

// StatusLabel: generic unschedulability: the fallback for a Pending pod.
func (a *Analyzer) StatusLabel() string { return "PENDING" }
func (a *Analyzer) Precedence() int     { return 3 }

func (a *Analyzer) Supports(o model.Observation) bool {
	if o.Kind == "pod.state" {
		phase, _ := o.Payload["phase"].(string)
		return phase == "Pending"
	}
	if o.Kind == "event.recorded" {
		reason, _ := o.Payload["reason"].(string)
		return reason == "FailedScheduling"
	}
	return false
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	type pending struct {
		state     model.Observation
		failedMsg string
		hasMsg    bool
		requests  map[string]string
	}
	groups := map[string]*pending{}
	var order []string

	// An unbound claim in the same namespace is a named reason the scheduler
	// is stuck; it competes with (and outranks) generic unschedulability.
	unboundNamespaces := map[string]bool{}
	for _, o := range in.Observations {
		if o.Kind != "pvc.state" {
			continue
		}
		if phase, _ := o.Payload["phase"].(string); phase != "Bound" {
			unboundNamespaces[o.Resource.Namespace] = true
		}
	}
	unboundPVC := map[string]bool{}

	for _, o := range in.Observations {
		key := o.Resource.String()
		if o.Kind == "pod.state" && a.Supports(o) {
			if groups[key] == nil {
				groups[key] = &pending{}
				order = append(order, key)
			}
			groups[key].state = o
			if unboundNamespaces[o.Resource.Namespace] {
				unboundPVC[key] = true
			}
		}
		if o.Kind == "event.recorded" && a.Supports(o) {
			if groups[key] == nil {
				groups[key] = &pending{}
				order = append(order, key)
			}
			if msg, ok := o.Payload["message"].(string); ok && msg != "" {
				groups[key].failedMsg = msg
				groups[key].hasMsg = true
			}
		}
	}
	for _, o := range in.Observations {
		g := groups[o.Resource.String()]
		if g == nil || o.Kind != "container.spec" {
			continue
		}
		if r, ok := analyze.PayloadStringMap(o.Payload, "requests"); ok {
			g.requests = r
		}
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, key := range order {
		g := groups[key]
		res := g.state.Resource

		msg := g.failedMsg
		if msg == "" {
			if m, ok := g.state.Payload["message"].(string); ok {
				msg = m
			}
		}

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("scheduling.%s", res.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevWarning,
			Title:       "Pod unschedulable (Pending)",
			Description: msg,
			Timestamp:   g.state.Timestamp,
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		ePending := model.Evidence{
			ID:     fmt.Sprintf("scheduling.%s.pending", res.Name),
			Claim:  "pod stuck in Pending",
			Weight: weightPending, Strength: 1.0,
		}
		evs = append(evs, ePending)
		terms = append(terms, score.EvidenceTerm{ID: ePending.ID, Label: "pod Pending (not scheduled)", Weight: weightPending, Strength: 1.0, Polarity: +1})

		if g.hasMsg {
			eMsg := model.Evidence{
				ID:     fmt.Sprintf("scheduling.%s.message", res.Name),
				Claim:  "scheduler failure message",
				Weight: weightMessage, Strength: 1.0,
			}
			evs = append(evs, eMsg)
			terms = append(terms, score.EvidenceTerm{ID: eMsg.ID, Label: fmt.Sprintf("scheduler: %s", truncate(msg, 90)), Weight: weightMessage, Strength: 1.0, Polarity: +1})
		}
		if len(g.requests) > 0 {
			eReq := model.Evidence{
				ID:     fmt.Sprintf("scheduling.%s.requests", res.Name),
				Claim:  "resource requests",
				Weight: weightRequest, Strength: 1.0,
			}
			evs = append(evs, eReq)
			terms = append(terms, score.EvidenceTerm{ID: eReq.ID, Label: fmt.Sprintf("requests: %s", fmtRequests(g.requests)), Weight: weightRequest, Strength: 1.0, Polarity: +1})
		}

		// An unbound PVC on the same pod is a more specific explanation than
		// generic unschedulability: the scheduler is blocked for a reason we
		// can name, so the generic hypothesis should not win on its own.
		if unboundPVC[key] {
			ePVC := model.Evidence{
				ID:          fmt.Sprintf("scheduling.%s.pvc", res.Name),
				Claim:       "unbound PersistentVolumeClaim - a more specific reason to be Pending",
				Contradicts: []string{},
				Weight:      weightPVC, Strength: 1.0,
			}
			evs = append(evs, ePVC)
			terms = append(terms, score.EvidenceTerm{ID: ePVC.ID, Label: "contradicting: unbound PVC explains the Pending state more specifically", Weight: weightPVC, Strength: 1.0, Polarity: -1})
		}

		missing := 0
		if !g.hasMsg {
			missing = 1 // no scheduler message collected → gap
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("scheduling.%s", res.Name),
			Claim:    fmt.Sprintf("Pod cannot be scheduled: %s", msg),
			Category: model.CatScheduling,
			Status:   model.StatusLikely,
			Score:    breakdown(terms, missing),
		}
		for _, e := range evs {
			h.Evidence = append(h.Evidence, e.ID)
		}
		if missing > 0 {
			h.Missing = append(h.Missing, "scheduling."+res.Name+".message")
		}
		hypotheses = append(hypotheses, h)
		evidence = append(evidence, evs...)
	}
	return findings, hypotheses, evidence, nil
}

func breakdown(terms []score.EvidenceTerm, missing int) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, missing)
	return &bd
}

func fmtRequests(r map[string]string) string {
	parts := make([]string, 0, len(r))
	for k, v := range r {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ", ")
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
