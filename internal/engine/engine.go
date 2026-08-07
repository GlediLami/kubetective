// Package engine orchestrates the investigation pipeline:
//
//	scope → collect → build (timeline + graph) → analyze → score → record/report
//
// It is the only component that knows the pipeline order; collectors and
// analyzers know nothing about each other (docs/DESIGN.md §6, §8).
package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/change"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/graph"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/score"
	"github.com/kubedoctor/kubedoctor/internal/timeline"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// ErrNoCollectors is returned when the engine is invoked without any wired
// data source.
var ErrNoCollectors = errors.New("engine: no collectors registered")

// Version is stamped at build time (-ldflags "-X github.com/kubedoctor/kubedoctor/internal/engine.Version=...").
var Version = "v0.1.0-dev"

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

	var gaps []model.EvidenceGap

	// Stage 1 — scope resolution: bounded worklist of related resources.
	// v0.1: the target itself; owner-chain expansion happens inside the
	// Kubernetes collector.
	targets := []model.ResourceRef{req.Target}

	// Stage 2 — collection: collectors normalize raw data into Observations.
	// A collector failure is a gap, never a fatal error.
	scopePlan := &collect.ScopePlan{
		Targets:     targets,
		Window:      req.Window,
		Logs:        req.Scope.Logs,
		MaxLogLines: req.Scope.MaxLogLines,
	}
	var observations []model.Observation
	var sources []string
	for _, c := range e.collectors.All() {
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
	// Dedup by content-hashed ID (identical facts from overlapping queries).
	observations = dedup(observations)

	// Stage 3 — build: timeline (merge/dedup/anchor) + "what changed" ranking
	// + the bounded evidence graph (docs/DESIGN.md §7.3, §8.3).
	events := timeline.Build(observations)
	onsetTs := time.Time{}
	if len(events) > 0 {
		if ts, ok := timeline.Anchor(events); ok {
			onsetTs = ts
		}
	}

	// Change detection needs the structural graph (OWNS/RUNS_ON) for ranking.
	structural := graph.Build(observations, nil, nil, graph.Options{MaxNodes: scopeMaxNodes(req)})
	changes := change.Rank(change.Detect(observations, req.Window.Start), structural, req.Target, onsetTs, windowSpan(req))
	g := graph.Build(observations, changes, anchorEvent(events), graph.Options{MaxNodes: scopeMaxNodes(req)})

	// Stage 4 — analysis: deterministic analyzers emit findings, hypotheses,
	// and evidence over the normalized observations.
	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence
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

	// Stage 5 — outcome: status/severity from findings, confidence from the
	// top hypothesis. (Scoring happens inside analyzers via internal/score.)
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
	}
	return res, nil
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
func statusFromFindings(fs []model.Finding) string {
	status := "INVESTIGATED"
	for _, f := range fs {
		switch f.Analyzer {
		case "oom":
			status = "OOMKILLED"
		case "crashloop":
			if status == "INVESTIGATED" {
				status = "CRASHLOOPBACKOFF"
			}
		case "imagepull":
			if status == "INVESTIGATED" {
				status = "IMAGEPULLBACKOFF"
			}
		case "scheduling":
			if status == "INVESTIGATED" {
				status = "PENDING"
			}
		}
	}
	return status
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
