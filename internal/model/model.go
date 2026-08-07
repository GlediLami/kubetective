// Package model defines the core KubeDoctor data model: normalized observations,
// evidence, the evidence graph, timeline, hypotheses, and incident records.
//
// Everything downstream of the collector boundary works with these types only —
// raw API payloads, log lines, and annotations never leave the collector layer.
// See docs/DESIGN.md §7.
package model

import "time"

// ResourceRef identifies any resource (Kubernetes today, infra resources later).
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func (r ResourceRef) String() string {
	if r.Namespace == "" {
		return r.Kind + "/" + r.Name
	}
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// SourceRef points at the raw data behind an observation (auditability).
type SourceRef struct {
	System string `json:"system"` // k8s | prometheus | git | argocd | ...
	Query  string `json:"query"`  // e.g. "GET /api/v1/namespaces/prod/pods/checkout-7f84c9"
	RawRef string `json:"raw_ref,omitempty"`
}

// Observation is the normalized fact emitted by a collector. Raw text never
// flows downstream unredacted; it is parsed into Payload or quoted behind
// the redaction policy (docs/DESIGN.md §14.3).
type Observation struct {
	ID         string         `json:"id"`   // content-hashed, stable for dedup
	Kind       string         `json:"kind"` // e.g. "container.terminated", "config.changed", "metric.breach"
	Source     SourceRef      `json:"source"`
	Timestamp  time.Time      `json:"timestamp"`
	Resource   ResourceRef    `json:"resource"`
	Payload    map[string]any `json:"payload"`
	Confidence float64        `json:"confidence"` // 1.0 for API state, lower for heuristics
	Provenance string         `json:"provenance"` // collector id + version
}

// Evidence ties observations to hypothesis claims with an explicit weight and
// strength so every conclusion is traceable and every score explainable.
type Evidence struct {
	ID          string   `json:"id"`
	Observation string   `json:"observation"`           // Observation.ID
	Claim       string   `json:"claim"`                 // human-readable claim this evidence supports
	Supports    []string `json:"supports,omitempty"`    // hypothesis IDs
	Contradicts []string `json:"contradicts,omitempty"` // hypothesis IDs
	Weight      float64  `json:"weight"`                // from the scoring table
	Strength    float64  `json:"strength"`              // -1..1, analyzer-assigned
	RawLink     string   `json:"raw_link,omitempty"`
}

// EdgeKind enumerates typed evidence-graph edges. CAUSED_BY is reserved for
// analyzers that pass the causality discipline (docs/DESIGN.md §10); the LLM
// can never emit it.
type EdgeKind string

const (
	EdgeOwns                 EdgeKind = "OWNS"
	EdgeRunsOn               EdgeKind = "RUNS_ON"
	EdgeSelects              EdgeKind = "SELECTS"
	EdgeRoutesTo             EdgeKind = "ROUTES_TO"
	EdgeMounts               EdgeKind = "MOUNTS"
	EdgeConfiguredBy         EdgeKind = "CONFIGURED_BY"
	EdgeDependsOn            EdgeKind = "DEPENDS_ON"
	EdgeChangedBefore        EdgeKind = "CHANGED_BEFORE"
	EdgeTemporallyCorrelated EdgeKind = "TEMPORALLY_CORRELATED"
	EdgeSupports             EdgeKind = "SUPPORTS"
	EdgeContradicts          EdgeKind = "CONTRADICTS"
	EdgeCausedBy             EdgeKind = "CAUSED_BY"
)

// Edge is a typed, directed relationship in the evidence graph.
type Edge struct {
	From       ResourceRef `json:"from"`
	To         ResourceRef `json:"to"`
	Kind       EdgeKind    `json:"kind"`
	Evidence   []string    `json:"evidence,omitempty"` // Evidence.IDs backing this edge
	Confidence float64     `json:"confidence"`
}

// Graph is the bounded, in-memory evidence graph for one investigation.
type Graph struct {
	Nodes []ResourceRef `json:"nodes"`
	Edges []Edge        `json:"edges"`
	// Bounds records the expansion budgets that were applied, so truncation is
	// visible rather than silent.
	Bounds GraphBounds `json:"bounds"`
}

type GraphBounds struct {
	NodeBudget    int    `json:"node_budget"`
	EdgeBudget    int    `json:"edge_budget"`
	Truncated     bool   `json:"truncated"`
	TruncatedKind string `json:"truncated_kind,omitempty"`
}

// TimelineEvent is one merged, deduplicated, anchored observation.
type TimelineEvent struct {
	Observation Observation `json:"observation"`
	// Offset is relative to the incident onset (e.g. -14m, +3m) so replays read
	// identically across timezones.
	Offset time.Duration `json:"offset"`
}

// HypothesisCategory groups claims for priors, calibration, and rendering.
type HypothesisCategory string

const (
	CatMemory     HypothesisCategory = "memory"
	CatScheduling HypothesisCategory = "scheduling"
	CatImage      HypothesisCategory = "image"
	CatConfig     HypothesisCategory = "config-regression"
	CatCrashLoop  HypothesisCategory = "crashloop"
	CatNode       HypothesisCategory = "node"
	CatProbe      HypothesisCategory = "probe"
	CatHPA        HypothesisCategory = "hpa"
	CatPVC        HypothesisCategory = "pvc"
	CatService    HypothesisCategory = "service"
	CatNetwork    HypothesisCategory = "network"
	CatDNS        HypothesisCategory = "dns"
	CatUnknown    HypothesisCategory = "unknown"
)

type HypothesisStatus string

const (
	StatusCandidate HypothesisStatus = "candidate"
	StatusLikely    HypothesisStatus = "likely"
	StatusRuledOut  HypothesisStatus = "ruled-out"
)

// Hypothesis is a scored candidate explanation. The Score breakdown lists every
// contributing evidence line so the score is fully explainable.
type Hypothesis struct {
	ID             string             `json:"id"`
	Claim          string             `json:"claim"`
	Category       HypothesisCategory `json:"category"`
	Status         HypothesisStatus   `json:"status"`
	Score          *ScoreBreakdown    `json:"score,omitempty"`
	Evidence       []string           `json:"evidence,omitempty"`       // Evidence.IDs supporting
	Contradictions []string           `json:"contradictions,omitempty"` // Evidence.IDs contradicting
	Missing        []string           `json:"missing,omitempty"`        // Evidence.IDs expected but absent
	AIGenerated    bool               `json:"ai_generated,omitempty"`   // labeled, never CAUSED_BY-capable
}

// ScoreBreakdown is the explainable scoring output (docs/DESIGN.md §9).
type ScoreBreakdown struct {
	Margin      float64     `json:"margin"`
	Temperature float64     `json:"temperature"`
	Score       float64     `json:"score"` // sigmoid(margin / temperature)
	Lines       []ScoreLine `json:"lines"`
	GapPenalty  float64     `json:"gap_penalty"`
	Calibrated  bool        `json:"calibrated"`
}

// ScoreLine is one visible contribution to a score.
type ScoreLine struct {
	Label      string  `json:"label"`
	Delta      float64 `json:"delta"` // signed contribution
	EvidenceID string  `json:"evidence_id,omitempty"`
}

// Finding is a deterministic analyzer output, always evidence-anchored.
type Finding struct {
	ID          string    `json:"id"`
	Analyzer    string    `json:"analyzer"`
	Severity    Severity  `json:"severity"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Evidence    []string  `json:"evidence,omitempty"` // Evidence.IDs
	Timestamp   time.Time `json:"timestamp"`
}

type Severity string

const (
	SevInfo     Severity = "INFO"
	SevWarning  Severity = "WARNING"
	SevHigh     Severity = "HIGH"
	SevCritical Severity = "CRITICAL"
)

// Change is one entry in the "what changed?" ranking (docs/DESIGN.md §8.3).
type Change struct {
	Resource    ResourceRef        `json:"resource"`
	Description string             `json:"description"`
	Timestamp   time.Time          `json:"timestamp"`
	Relevance   float64            `json:"relevance"` // 0..1
	Factors     map[string]float64 `json:"factors,omitempty"`
}

// EvidenceGap is expected-but-missing evidence: a collector was down, a
// permission was missing, or telemetry is not configured. Gaps cap confidence
// and are surfaced in the CLI — never silent.
type EvidenceGap struct {
	Description string `json:"description"`
	Category    string `json:"category"`           // e.g. "permission", "collector-down", "no-metrics"
	Expected    string `json:"expected,omitempty"` // what we wanted (e.g. metric memory.usage)
}

// Recommendation is deterministic first (rule table); the LLM can paraphrase
// but never invent actions outside the typed table.
type Recommendation struct {
	ID       string   `json:"id"`
	Action   string   `json:"action"` // e.g. "rollback deployment/checkout"
	Risk     Risk     `json:"risk"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence,omitempty"`
}

type Risk string

const (
	RiskLow    Risk = "LOW"
	RiskMedium Risk = "MEDIUM"
	RiskHigh   Risk = "HIGH"
)

// IncidentSummary is the header card of an investigation.
type IncidentSummary struct {
	ID         string      `json:"id"`
	Target     ResourceRef `json:"target"`
	Status     string      `json:"status"` // e.g. CRASHLOOPBACKOFF
	Severity   Severity    `json:"severity"`
	Confidence float64     `json:"confidence"`
	Onset      time.Time   `json:"onset,omitempty"`
}

// ResultMeta records how the investigation ran (auditability + performance).
type ResultMeta struct {
	EngineVersion string        `json:"engine_version"`
	Started       time.Time     `json:"started"`
	Duration      time.Duration `json:"duration"`
	SourcesHit    []string      `json:"sources_hit,omitempty"`
	RecordID      string        `json:"record_id,omitempty"` // incident record for replay
	Truncated     bool          `json:"truncated,omitempty"`
}

// Incident is the recorded investigation — the replay and benchmark substrate.
// Stored as JSONL: one Observation per line, append-only (docs/DESIGN.md §7.6).
type Incident struct {
	ID           string                `json:"id"`
	Meta         IncidentMeta          `json:"meta"`
	Observations []Observation         `json:"observations"`
	Result       *IncidentResultRecord `json:"result,omitempty"`
}

type IncidentMeta struct {
	ClusterID     string `json:"cluster_id"` // anonymized
	K8sVersion    string `json:"k8s_version,omitempty"`
	EngineVersion string `json:"engine_version"`
	RecordVersion int    `json:"record_version"`
	Target        string `json:"target,omitempty"` // request target (for replay)
	UserNote      string `json:"user_note,omitempty"`
}

// IncidentResultRecord is a re-importable result snapshot.
type IncidentResultRecord struct {
	Hypotheses []Hypothesis  `json:"hypotheses,omitempty"`
	Findings   []Finding     `json:"findings,omitempty"`
	Changes    []Change      `json:"changes,omitempty"`
	Gaps       []EvidenceGap `json:"gaps,omitempty"`
}
