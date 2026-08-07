// Package analyze defines the deterministic analyzer contract. Analyzers are
// pure functions over Observations: they share evidence, never state, so any
// number of them can run in parallel and in any order.
//
// Contribution rule (docs/DESIGN.md §11): a new analyzer must ship with a
// scenario in scenarios/ and keep the benchmark gate green.
package analyze

import (
	"context"

	"github.com/kubedoctor/kubedoctor/internal/model"
)

// AnalysisInput is everything an analyzer may look at. It is read-only.
type AnalysisInput struct {
	Observations []model.Observation
	Graph        *model.Graph
	Changes      []model.Change
	WindowStart  string // RFC3339; kept as string to keep the interface small in v0.1
	WindowEnd    string
}

// EvidenceRequest describes evidence an analyzer would need to strengthen or
// refute a live hypothesis — this drives the adaptive collection loop.
type EvidenceRequest struct {
	HypothesisID string
	Description  string
	Collector    string // e.g. "kubernetes", "prometheus"
	QueryHint    string
	Cost         int // rough relative cost, used by the engine's collection budget
}

// Analyzer is the unit of deterministic reasoning.
type Analyzer interface {
	ID() string
	Name() string
	// Supports is a cheap predicate evaluated for every observation.
	Supports(o model.Observation) bool
	// Analyze emits findings, hypothesis candidates, and evidence.
	Analyze(ctx context.Context, in *AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error)
	// NeedsEvidence asks for more evidence for live hypotheses (adaptive loop).
	NeedsEvidence(h model.Hypothesis) []EvidenceRequest
	// Explain renders a finding for the CLI even without an LLM.
	Explain(f model.Finding) string
}

// Registry holds the analyzers wired into an engine instance.
type Registry struct {
	analyzers []Analyzer
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) Register(a Analyzer) { r.analyzers = append(r.analyzers, a) }

func (r *Registry) All() []Analyzer { return r.analyzers }
