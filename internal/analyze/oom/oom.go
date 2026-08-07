// Package oom implements the memory-exhaustion analyzer: it activates on
// container.terminated observations with reason OOMKilled, counts them, checks
// the configured memory limit, and builds the "memory exhaustion" hypothesis
// with explainable evidence.
package oom

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect/prometheus"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/score"
)

const (
	weightTemporal     = 30.0
	weightMechanism    = 20.0
	weightMetricBreach = 20.0 // Prometheus: usage reached the limit
	weightMetricGrowth = 15.0 // Prometheus: usage grew within the window
	weightLimit        = 15.0
	weightReproduce    = 10.0
	weightNodePressure = 15.0 // contradiction when node is under pressure
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "oom" }
func (a *Analyzer) Name() string { return "Memory Exhaustion (OOMKilled)" }

func (a *Analyzer) Supports(o model.Observation) bool {
	return o.Kind == "container.terminated" && o.Payload["reason"] == "OOMKilled"
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	// Group terminations by pod resource. A termination is either the current
	// container state (container.terminated) or the historical record left by
	// the kubelet (event.recorded OOMKilling) — crash-looping pods lose their
	// current terminated state to CrashLoopBackOff, so the events are the
	// authoritative history.
	type crash struct {
		obs        []model.Observation
		limit      string
		hasLimit   bool
		restarts   int64
		fromEvents bool
	}
	groups := map[string]*crash{}
	var order []string
	for _, o := range in.Observations {
		isTerm := o.Kind == "container.terminated" && o.Payload["reason"] == "OOMKilled"
		isEvent := o.Kind == "event.recorded" && o.Payload["reason"] == "OOMKilling"
		if !isTerm && !isEvent {
			continue
		}
		key := o.Resource.String()
		if groups[key] == nil {
			groups[key] = &crash{}
			order = append(order, key)
		}
		if isEvent {
			groups[key].fromEvents = true
		}
		groups[key].obs = append(groups[key].obs, o)
	}
	// Attach spec/state context per resource.
	for _, o := range in.Observations {
		g := groups[o.Resource.String()]
		if g == nil {
			continue
		}
		if o.Kind == "container.spec" {
			if l, ok := analyze.PayloadStringMap(o.Payload, "limits"); ok {
				if v, ok := l["memory"]; ok {
					g.limit, g.hasLimit = v, true
				}
			}
		}
		if o.Kind == "container.terminated" || o.Kind == "container.running" {
			if n, ok := analyze.PayloadInt64(o.Payload, "restarts"); ok {
				g.restarts = n
			}
		}
	}

	// Node pressure contradiction: any node.condition MemoryPressure=True.
	nodePressure := false
	for _, o := range in.Observations {
		if o.Kind == "node.condition" && o.Payload["type"] == "MemoryPressure" && o.Payload["status"] == "True" {
			nodePressure = true
		}
	}
	// Metric evidence: Prometheus memory series per pod (when available).
	metrics := map[string]*metricEvidence{}
	for _, o := range in.Observations {
		if o.Kind != "metric.series" || o.Payload["metric"] != prometheus.MetricMemory {
			continue
		}
		m := parseSeries(o.Payload)
		metrics[o.Resource.String()] = m
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, key := range order {
		g := groups[key]
		res := g.obs[0].Resource
		n := len(g.obs)
		last := g.obs[len(g.obs)-1]
		restarts := g.restarts
		if restarts < int64(n) {
			restarts = int64(n)
		}

		claim := fmt.Sprintf("Memory exhaustion: container terminated with OOMKilled %d time(s)", n)
		if g.hasLimit {
			claim += fmt.Sprintf(" (memory limit %s)", g.limit)
		}
		if restarts > 1 {
			claim += fmt.Sprintf(" — %d restart(s)", restarts)
		}
		if n > 0 && g.fromEvents {
			claim += " (incl. kubelet OOMKilling events)"
		}
		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("oom.%s", res.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevHigh,
			Title:       fmt.Sprintf("OOMKilled ×%d", n),
			Description: claim,
			Evidence:    []string{},
			Timestamp:   last.Timestamp,
		})

		// Evidence terms (explainable scoring, docs/DESIGN.md §9).
		var terms []score.EvidenceTerm
		var evs []model.Evidence

		eTerminated := model.Evidence{
			ID:       fmt.Sprintf("oom.%s.terminated", res.Name),
			Claim:    fmt.Sprintf("container terminated OOMKilled ×%d", n),
			Supports: []string{},
			Weight:   weightMechanism,
			Strength: 1.0,
		}
		evs = append(evs, eTerminated)
		terms = append(terms, score.EvidenceTerm{ID: eTerminated.ID, Label: fmt.Sprintf("mechanism: OOMKilled ×%d", n), Weight: weightMechanism, Strength: 1.0, Polarity: +1})

		eLimit := model.Evidence{
			ID:       fmt.Sprintf("oom.%s.limit", res.Name),
			Claim:    "memory limit configured",
			Supports: []string{},
			Weight:   weightLimit,
			Strength: 1.0,
		}
		evs = append(evs, eLimit)
		if g.hasLimit {
			terms = append(terms, score.EvidenceTerm{ID: eLimit.ID, Label: fmt.Sprintf("memory limit configured: %s", g.limit), Weight: weightLimit, Strength: 1.0, Polarity: +1})
		}

		eRepro := model.Evidence{
			ID:       fmt.Sprintf("oom.%s.repro", res.Name),
			Claim:    "reproduced after restart",
			Supports: []string{},
			Weight:   weightReproduce,
			Strength: 1.0,
		}
		evs = append(evs, eRepro)
		if n > 1 || restarts > 1 {
			terms = append(terms, score.EvidenceTerm{ID: eRepro.ID, Label: fmt.Sprintf("reproduced after restart (×%d)", restarts), Weight: weightReproduce, Strength: 1.0, Polarity: +1})
		}

		eTemporal := model.Evidence{
			ID:       fmt.Sprintf("oom.%s.temporal", res.Name),
			Claim:    "terminations recent within investigation window",
			Supports: []string{},
			Weight:   weightTemporal,
			Strength: 0.9,
		}
		evs = append(evs, eTemporal)
		terms = append(terms, score.EvidenceTerm{ID: eTemporal.ID, Label: "strong temporal correlation (terminations in window)", Weight: weightTemporal, Strength: 0.9, Polarity: +1})

		// Prometheus corroboration: memory reaching the limit and growth
		// within the window (only present when a Prometheus collector ran).
		if m := metrics[key]; m != nil {
			if g.hasLimit {
				if limitBytes, ok := analyze.ParseBytes(g.limit); ok && m.max >= float64(limitBytes) {
					eBreach := model.Evidence{
						ID:     fmt.Sprintf("oom.%s.metric-breach", res.Name),
						Claim:  "memory usage reached the limit (Prometheus)",
						Weight: weightMetricBreach, Strength: 1.0,
					}
					evs = append(evs, eBreach)
					terms = append(terms, score.EvidenceTerm{ID: eBreach.ID, Label: fmt.Sprintf("metrics: memory peaked at %.0f Mi ≥ limit %s", m.max/1048576, g.limit), Weight: weightMetricBreach, Strength: 1.0, Polarity: +1})
				}
			}
			if m.growth {
				eGrowth := model.Evidence{
					ID:     fmt.Sprintf("oom.%s.metric-growth", res.Name),
					Claim:  "memory usage increased within the window",
					Weight: weightMetricGrowth, Strength: 1.0,
				}
				evs = append(evs, eGrowth)
				terms = append(terms, score.EvidenceTerm{ID: eGrowth.ID, Label: fmt.Sprintf("metrics: memory grew %.0f Mi → %.0f Mi", m.first/1048576, m.last/1048576), Weight: weightMetricGrowth, Strength: 1.0, Polarity: +1})
			}
		}

		// Contradiction: node memory pressure shifts suspicion to the node.
		eNode := model.Evidence{
			ID:          fmt.Sprintf("oom.%s.nodepressure", res.Name),
			Claim:       "node-level memory pressure observed",
			Contradicts: []string{},
			Weight:      weightNodePressure,
			Strength:    0.8,
		}
		if nodePressure {
			evs = append(evs, eNode)
			terms = append(terms, score.EvidenceTerm{ID: eNode.ID, Label: "contradicting: node memory pressure", Weight: weightNodePressure, Strength: 0.8, Polarity: -1})
		}

		// Missing expected evidence: a configured limit we could not see.
		missing := 0
		if !g.hasLimit {
			missing = 1
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("memory.%s", res.Name),
			Claim:    claim,
			Category: model.CatMemory,
			Status:   model.StatusLikely,
			Score:    breakdown(terms, missing),
		}
		for _, e := range evs {
			h.Evidence = append(h.Evidence, e.ID)
		}
		if missing > 0 {
			h.Missing = append(h.Missing, "oom."+res.Name+".limit")
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

// metricEvidence is the parsed Prometheus memory series summary.
type metricEvidence struct {
	first, last, max float64
	growth           bool // last > first * 1.2 (≥20% growth within the window)
}

func parseSeries(p map[string]any) *metricEvidence {
	first, _ := analyze.PayloadFloat(p, "first")
	last, _ := analyze.PayloadFloat(p, "last")
	max, _ := analyze.PayloadFloat(p, "max")
	m := &metricEvidence{first: first, last: last, max: max}
	m.growth = first > 0 && last > first*1.2
	return m
}

func (a *Analyzer) NeedsEvidence(_ model.Hypothesis) []analyze.EvidenceRequest {
	return nil // v0.1: all needed evidence is in the initial collection
}

func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s: %s", f.Title, strings.ToLower(f.Description))
}
