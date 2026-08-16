// Package analyze defines the deterministic analyzer contract. Analyzers are
// pure functions over Observations: they share evidence, never state, so any
// number of them can run in parallel and in any order.
//
// Contribution rule: a new analyzer must ship with a
// scenario in scenarios/ and keep the benchmark gate green.
package analyze

import (
	"context"
	"encoding/json"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/GlediLami/kubetective/internal/model"
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
// Alias of model.EvidenceRequest: kept here so analyzer implementations use
// the analyze package name only.
type EvidenceRequest = model.EvidenceRequest

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

	// StatusLabel is the incident status card this analyzer claims when its
	// finding wins ("OOMKILLED", "PENDING", …). Empty means the analyzer
	// contributes evidence but never sets the card — config regression and
	// DNS explain *why* a pod is in a state, not what state it is in.
	StatusLabel() string
	// Precedence ranks analyzers competing for the status card: highest wins.
	// The ordering encodes specificity — a node under pressure explains an
	// OOMKill, which in turn explains a crash loop, so node > memory > crash.
	// Zero means "never claims the card".
	//
	// Keeping this on the analyzer (rather than in a table the engine owns)
	// means adding an analyzer is a single-file change; the engine cannot go
	// stale because there is nothing engine-side to update.
	Precedence() int
}

// PayloadInt64 reads an int64 field tolerating both live (int64) and
// JSON-round-tripped (float64) payloads — analyzers must not depend on the
// in-memory representation, because replay serves JSON-decoded observations.
func PayloadInt64(p map[string]any, key string) (int64, bool) {
	switch v := p[key].(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int:
		return int64(v), true
	case int16:
		return int64(v), true
	case int8:
		return int64(v), true
	case float64:
		return int64(v), true
	case float32:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}
	return 0, false
}

// PayloadStringMap reads a string→string map field tolerating both live
// (map[string]string) and JSON-round-tripped (map[string]any) payloads.
func PayloadStringMap(p map[string]any, key string) (map[string]string, bool) {
	switch m := p[key].(type) {
	case map[string]string:
		return m, true
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			s, ok := v.(string)
			if !ok {
				return nil, false
			}
			out[k] = s
		}
		return out, true
	}
	return nil, false
}

// PayloadFloat reads a float64 field tolerating live (float64/int64), JSON
// (float64), and string-encoded ("4.10e+08") payloads.
func PayloadFloat(p map[string]any, key string) (float64, bool) {
	switch v := p[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	return 0, false
}

// ParseBytes converts a Kubernetes resource quantity string ("1Gi", "512Mi")
// to bytes. Returns false on malformed input.
func ParseBytes(s string) (int64, bool) {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, false
	}
	return q.Value(), true
}

// Registry holds the analyzers wired into an engine instance.
type Registry struct {
	analyzers []Analyzer
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) Register(a Analyzer) { r.analyzers = append(r.analyzers, a) }

func (r *Registry) All() []Analyzer { return r.analyzers }
