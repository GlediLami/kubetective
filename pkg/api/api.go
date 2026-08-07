// Package api is the stable public KubeDoctor contract. The CLI, REST server,
// MCP server, and web UI are all adapters over the Investigator interface —
// there is exactly one investigation pipeline, with one request/result shape.
// See docs/DESIGN.md §6.2.
package api

import (
	"context"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/model"
)

// Investigator runs the full investigation pipeline:
// scope → collect → build (graph/timeline) → analyze → score → report.
type Investigator interface {
	Investigate(ctx context.Context, req *InvestigationRequest) (*InvestigationResult, error)
}

// InvestigationRequest is the entire input surface of the engine.
type InvestigationRequest struct {
	// Target is what the user points at: pod/name, deployment/name, or an
	// empty Namespace-only request (cluster/namespace-wide "what changed?").
	Target model.ResourceRef

	// Window bounds collection. Zero values mean "last 30 minutes" (or --since).
	Window Window

	// Scope controls how far the engine expands beyond the target
	// (owner chains, selectors, routes, config sources) and which telemetry
	// sources are consulted. Defaults are safe and cheap.
	Scope ScopeOptions

	// Redaction level: on (default) | off | strict.
	Redaction RedactionPolicy
}

type Window struct {
	Start time.Time
	End   time.Time
}

// Contains reports whether t falls inside the window. A zero window contains
// everything (callers are expected to default it via Since before use).
func (w Window) Contains(t time.Time) bool {
	if w.Start.IsZero() && w.End.IsZero() {
		return true
	}
	if !w.Start.IsZero() && t.Before(w.Start) {
		return false
	}
	if !w.End.IsZero() && t.After(w.End) {
		return false
	}
	return true
}

// Since is a convenience: returns a window ending now.
func Since(d time.Duration) Window {
	end := time.Now()
	return Window{Start: end.Add(-d), End: end}
}

type ScopeOptions struct {
	FollowOwners    bool `json:"follow_owners,omitempty"`    // default true
	FollowSelectors bool `json:"follow_selectors,omitempty"` // default true
	Logs            bool `json:"logs,omitempty"`             // tail container logs (capped)
	MaxLogLines     int  `json:"max_log_lines,omitempty"`    // default 200
	MaxGraphNodes   int  `json:"max_graph_nodes,omitempty"`  // default 200
}

type RedactionPolicy string

const (
	RedactionOn     RedactionPolicy = "on"
	RedactionOff    RedactionPolicy = "off"
	RedactionStrict RedactionPolicy = "strict"
)

// InvestigationResult is the complete engine output. The CLI renders a narrow
// slice of it ("collect more than you show"); JSON format emits all of it.
type InvestigationResult struct {
	Incident        *model.IncidentSummary `json:"incident,omitempty"`
	Observations    []model.Observation    `json:"observations,omitempty"`
	Evidence        []model.Evidence       `json:"evidence,omitempty"`
	Timeline        []model.TimelineEvent  `json:"timeline,omitempty"`
	Graph           *model.Graph           `json:"graph,omitempty"`
	Changes         []model.Change         `json:"changes,omitempty"`
	Hypotheses      []model.Hypothesis     `json:"hypotheses,omitempty"`
	Findings        []model.Finding        `json:"findings,omitempty"`
	Recommendations []model.Recommendation `json:"recommendations,omitempty"`
	EvidenceGaps    []model.EvidenceGap    `json:"evidence_gaps,omitempty"`
	Meta            model.ResultMeta       `json:"meta"`
}
