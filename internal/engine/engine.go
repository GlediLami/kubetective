// Package engine orchestrates the investigation pipeline:
//
//	scope → collect → build (timeline + graph + changes) → analyze → score
//	→ adaptive rounds (NeedsEvidence → collect more → re-analyze)
//	→ record/report
//
// It is the only component that knows the pipeline order; collectors and
// analyzers know nothing about each other.
package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/change"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/graph"
	"github.com/GlediLami/kubetective/internal/hypothesis"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/recommend"
	"github.com/GlediLami/kubetective/internal/score"
	"github.com/GlediLami/kubetective/internal/timeline"
	"github.com/GlediLami/kubetective/pkg/api"
)

// ErrNoCollectors is returned when the engine is invoked without any wired
// data source.
var ErrNoCollectors = errors.New("engine: no collectors registered")

// Version is stamped at build time (-ldflags "-X github.com/GlediLami/kubetective/internal/engine.Version=...").
var Version = "v0.7.0"

// Adaptive collection bounds: at most two targeted
// rounds, ≤5 requests, total cost ≤ requestBudget.
const (
	maxAdaptiveRounds = 2
	maxRequests       = 5
	requestBudget     = 6
)

// Engine implements api.Investigator.
type Engine struct {
	collectors *collect.Registry
	analyzers  *analyze.Registry
}

func New(collectors *collect.Registry, analyzers *analyze.Registry) *Engine {
	return &Engine{collectors: collectors, analyzers: analyzers}
}

func (e *Engine) Investigate(ctx context.Context, req *api.InvestigationRequest) (*api.InvestigationResult, error) {
	started := time.Now()
	if req == nil {
		return nil, errors.New("engine: nil investigation request")
	}
	if len(e.collectors.All()) == 0 {
		return nil, ErrNoCollectors
	}

	// Stage 1 — scope: the target itself; owner-chain expansion happens
	// inside the Kubernetes collector.
	scopePlan := &collect.ScopePlan{
		Targets:     []model.ResourceRef{req.Target},
		Window:      req.Window,
		Logs:        req.Scope.Logs,
		MaxLogLines: req.Scope.MaxLogLines,
	}

	// Stage 2 — initial collection.
	observations, gaps, sources := e.collectAll(ctx, scopePlan)
	observations = dedup(observations)

	// Stage 3–5 — build + analyze + score.
	events, changes, g := e.buildStage(observations, req)
	findings, hypotheses, evidence, agaps := e.analyzeAll(ctx, observations, g, changes)
	gaps = append(gaps, agaps...)
	hypotheses = hypothesis.Merge(hypotheses)

	// Stage 6 — adaptive rounds: analyzers ask for targeted evidence
	// (NeedsEvidence), collectors fetch it, the pipeline re-runs. Bounded.
	for round := 1; round <= maxAdaptiveRounds; round++ {
		requests := e.pendingRequests(hypotheses)
		if len(requests) == 0 {
			break
		}
		scopePlan.EvidenceRequests = requests
		newObs, ngaps, _ := e.collectAll(ctx, scopePlan)
		gaps = append(gaps, ngaps...)
		merged := dedup(append(observations, newObs...))
		if len(merged) == len(observations) {
			break // nothing new arrived; re-requesting would spin
		}
		observations = merged
		events, changes, g = e.buildStage(observations, req)
		findings, hypotheses, evidence, agaps = e.analyzeAll(ctx, observations, g, changes)
		gaps = append(gaps, agaps...)
		hypotheses = hypothesis.Merge(hypotheses)
	}

	// Outcome summary: status/severity from findings, confidence from the
	// top hypothesis.
	summary := &model.IncidentSummary{
		ID:       fmt.Sprintf("incident-%d", started.Unix()),
		Target:   req.Target,
		Status:   statusFromFindings(findings),
		Severity: severityFromFindings(findings),
	}
	res := &api.InvestigationResult{
		Incident:     summary,
		Observations: observations,
		Evidence:     evidence,
		Timeline:     events,
		Graph:        g,
		Changes:      changes,
		Hypotheses:   hypotheses,
		Findings:     findings,
		EvidenceGaps: gaps,
		Meta: model.ResultMeta{
			EngineVersion: Version,
			Started:       started,
			Duration:      time.Since(started),
			SourcesHit:    sources,
		},
	}
	if top := score.Top(hypotheses); top != nil {
		summary.Confidence = top.Score.Score
		// Deterministic, risk-leveled recommendations (Phase 2, read-only).
		res.Recommendations = recommend.ForTop(top, req.Target)
	}
	return res, nil
}

// collectAll runs every collector with the current scope plan (staged:
// Prior carries earlier collectors' output; EvidenceRequests carries the
// adaptive asks). Collector failures become gaps, never fatal errors.
func (e *Engine) collectAll(ctx context.Context, scopePlan *collect.ScopePlan) ([]model.Observation, []model.EvidenceGap, []string) {
	var observations []model.Observation
	var gaps []model.EvidenceGap
	var sources []string
	for _, c := range e.collectors.All() {
		scopePlan.Prior = observations
		obs, refs, err := c.Collect(ctx, scopePlan)
		if err != nil {
			gaps = append(gaps, model.EvidenceGap{
				Description: fmt.Sprintf("collector %s failed: %v", c.ID(), err),
				Category:    "collector-down",
			})
			continue
		}
		observations = append(observations, obs...)
		for _, r := range refs {
			sources = append(sources, r.System)
		}
	}
	return observations, gaps, sources
}

// buildStage builds the timeline, "what changed" ranking, and evidence graph
// from the current observation set.
func (e *Engine) buildStage(observations []model.Observation, req *api.InvestigationRequest) ([]model.TimelineEvent, []model.Change, *model.Graph) {
	events := timeline.Build(observations)
	onsetTs := time.Time{}
	if len(events) > 0 {
		if ts, ok := timeline.Anchor(events); ok {
			onsetTs = ts
		}
	}
	// Change detection needs the structural graph (OWNS/RUNS_ON) for ranking.
	structural := graph.Build(observations, nil, nil, graph.Options{MaxNodes: scopeMaxNodes(req)})
	changes := change.Rank(change.Detect(observations, req.Window.Start), structural, req.Target, onsetTs, windowSpan(req),
		func(ch model.Change) float64 { return change.AnomalyScore(observations, ch, 5*time.Minute) })
	g := graph.Build(observations, changes, anchorEvent(events), graph.Options{MaxNodes: scopeMaxNodes(req)})
	return events, changes, g
}

// analyzeAll runs every analyzer over the current observations.
func (e *Engine) analyzeAll(ctx context.Context, observations []model.Observation, g *model.Graph, changes []model.Change) ([]model.Finding, []model.Hypothesis, []model.Evidence, []model.EvidenceGap) {
	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence
	var gaps []model.EvidenceGap
	for _, a := range e.analyzers.All() {
		fs, hs, evs, err := a.Analyze(ctx, &analyze.AnalysisInput{
			Observations: observations,
			Graph:        g,
			Changes:      changes,
		})
		if err != nil {
			gaps = append(gaps, model.EvidenceGap{
				Description: fmt.Sprintf("analyzer %s failed: %v", a.ID(), err),
				Category:    "analyzer-error",
			})
			continue
		}
		findings = append(findings, fs...)
		hypotheses = append(hypotheses, hs...)
		evidence = append(evidence, evs...)
	}
	return findings, hypotheses, evidence, gaps
}

// pendingRequests collects NeedsEvidence asks from all analyzers for the
// live hypotheses, deduplicated and capped by the request budget.
func (e *Engine) pendingRequests(hypotheses []model.Hypothesis) []model.EvidenceRequest {
	var out []model.EvidenceRequest
	seen := map[string]bool{}
	cost, count := 0, 0
	for _, a := range e.analyzers.All() {
		for i := range hypotheses {
			h := &hypotheses[i]
			if h.Status != model.StatusLikely && h.Status != model.StatusCandidate {
				continue
			}
			for _, r := range a.NeedsEvidence(*h) {
				key := h.ID + "|" + r.QueryHint
				if seen[key] || cost+r.Cost > requestBudget || count >= maxRequests {
					continue
				}
				seen[key] = true
				cost += r.Cost
				count++
				out = append(out, r)
			}
		}
	}
	return out
}

// dedup keeps the first observation per content-hashed ID.
func dedup(obs []model.Observation) []model.Observation {
	seen := make(map[string]bool, len(obs))
	out := make([]model.Observation, 0, len(obs))
	for _, o := range obs {
		if seen[o.ID] {
			continue
		}
		seen[o.ID] = true
		out = append(out, o)
	}
	return out
}

// statusFromFindings derives the incident status card from analyzer findings.
// Precedence: node pressure > OOM > PVC > image pull > crash loop > pending >
// probe > service (the more specific/root-cause finding wins the card).
func statusFromFindings(fs []model.Finding) string {
	rank := map[string]int{
		"nodepressure": 8,
		"oom":          7,
		"pvc":          6,
		"imagepull":    5,
		"crashloop":    4,
		"scheduling":   3,
		"probe":        2,
		"service":      1,
	}
	best, bestRank := "INVESTIGATED", 0
	for _, f := range fs {
		r, ok := rank[f.Analyzer]
		if !ok || r <= bestRank {
			continue
		}
		bestRank = r
		switch f.Analyzer {
		case "nodepressure":
			best = "NODEPRESSURE"
		case "oom":
			best = "OOMKILLED"
		case "pvc":
			best = "PVCUNBOUND"
		case "imagepull":
			best = "IMAGEPULLBACKOFF"
		case "crashloop":
			best = "CRASHLOOPBACKOFF"
		case "scheduling":
			best = "PENDING"
		case "probe":
			best = "UNHEALTHY"
		case "service":
			best = "NOENDPOINTS"
		}
	}
	return best
}

func severityFromFindings(fs []model.Finding) model.Severity {
	sev := model.SevInfo
	for _, f := range fs {
		if f.Severity == model.SevCritical {
			return model.SevCritical
		}
		if f.Severity == model.SevHigh && sev != model.SevCritical {
			sev = model.SevHigh
		}
		if f.Severity == model.SevWarning && sev == model.SevInfo {
			sev = model.SevWarning
		}
	}
	return sev
}

func scopeMaxNodes(req *api.InvestigationRequest) int {
	if req.Scope.MaxGraphNodes > 0 {
		return req.Scope.MaxGraphNodes
	}
	return 200
}

func windowSpan(req *api.InvestigationRequest) time.Duration {
	if !req.Window.Start.IsZero() && !req.Window.End.IsZero() {
		return req.Window.End.Sub(req.Window.Start)
	}
	return 30 * time.Minute
}

func anchorEvent(events []model.TimelineEvent) *model.TimelineEvent {
	for i := range events {
		if events[i].Offset == 0 {
			return &events[i]
		}
	}
	if len(events) == 0 {
		return nil
	}
	return &events[0]
}
