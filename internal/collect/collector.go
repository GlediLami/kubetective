// Package collect defines the collector boundary: raw data enters here and is
// normalized into Observations. Nothing raw (log lines, annotations, secrets)
// flows past this boundary. See docs/DESIGN.md §12.
package collect

import (
	"context"

	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// ScopePlan is what the engine hands a collector: the bounded worklist of
// resources plus collection options for this investigation. Prior holds the
// observations earlier collectors produced (staged collection, docs/DESIGN.md
// §8.2) — e.g. the Prometheus collector derives pod targets from the
// Kubernetes collector's pod.state observations. EvidenceRequests carries the
// adaptive loop's targeted asks (docs/DESIGN.md §8.4).
type ScopePlan struct {
	Targets          []model.ResourceRef
	Window           api.Window
	Logs             bool
	MaxLogLines      int
	Prior            []model.Observation
	EvidenceRequests []model.EvidenceRequest
}

// WantsHint reports whether an analyzer requested evidence with the given
// query hint (e.g. "logs") — collectors honor what they understand.
func (s *ScopePlan) WantsHint(hint string) bool {
	for _, r := range s.EvidenceRequests {
		if r.QueryHint == hint {
			return true
		}
	}
	return false
}

// Collector normalizes one data source into Observations. A collector failure
// is recorded as an EvidenceGap by the engine — it must never fail the whole
// investigation.
type Collector interface {
	ID() string
	Collect(ctx context.Context, scope *ScopePlan) ([]model.Observation, []model.SourceRef, error)
}

// Registry holds the collectors wired into an engine instance.
type Registry struct {
	collectors []Collector
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) Register(c Collector) { r.collectors = append(r.collectors, c) }

func (r *Registry) All() []Collector { return r.collectors }
