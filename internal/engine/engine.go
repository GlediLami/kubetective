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
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/score"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// ErrNoCollectors is returned when the engine is invoked without any wired
// data source — the v0.1 Kubernetes collector is the first thing to land.
var ErrNoCollectors = errors.New("engine: no collectors registered")

// Version is stamped at build time (-ldflags "-X github.com/kubedoctor/kubedoctor/internal/engine.Version=...").
var Version = "v0.0.0-dev"

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
	// v0.1: owner chains + selectors (implementation lands with the k8s collector).
	targets := []model.ResourceRef{req.Target}

	// Stage 2 — collection: parallel collectors normalize raw data into
	// Observations. A collector failure is a gap, never a fatal error.
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

	// Stage 3 — build: timeline (merge/dedup/anchor) + evidence graph.
	// v0.1: graph/timeline builders land with the model tests; the result
	// shape is already the stable api contract.
	graph := &model.Graph{Nodes: targets}

	// Stage 4 — analysis: deterministic analyzers emit findings, hypotheses,
	// and evidence over the normalized observations.
	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence
	for _, a := range e.analyzers.All() {
		fs, hs, evs, err := a.Analyze(ctx, &analyze.AnalysisInput{
			Observations: observations,
			Graph:        graph,
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

	// Stage 5 — scoring: explainable margin + sigmoid per hypothesis.
	// v0.1: scoring is wired; evidence terms are supplied by analyzers.
	for i := range hypotheses {
		if hypotheses[i].Score == nil {
			continue
		}
	}

	// Stage 6 — record + report: the result is the api contract; the incident
	// recorder (JSONL) lands with record/ in the v0.1 milestone.

	// Outcome summary: engine is read-only; recommendations come from the
	// deterministic rule table once analyzers ship.
	summary := &model.IncidentSummary{
		ID:       fmt.Sprintf("incident-%d", started.Unix()),
		Target:   req.Target,
		Status:   "INVESTIGATED",
		Severity: model.SevInfo,
	}

	res := &api.InvestigationResult{
		Incident:     summary,
		Observations: observations,
		Timeline:     nil, // timeline builder lands in v0.1 milestone
		Graph:        graph,
		Changes:      nil,
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

	// Confidence: top hypothesis score, if any.
	if top := score.Top(hypotheses); top != nil {
		summary.Confidence = top.Score.Score
		summary.Status = "DIAGNOSED"
	}
	return res, nil
}
