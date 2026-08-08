// Package record persists investigations as append-only JSONL incident
// records (one Observation per line) - the replay and benchmark substrate.
// A replay runs the pipeline over recorded observations instead of live
// collectors, so investigations are reproducible and debuggable
//.
package record

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

const RecordVersion = 1

// DefaultDir is where incident records live (~/.kubetective/incidents).
func DefaultDir() string {
	if d := os.Getenv("KUBETECTIVE_INCIDENTS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kubetective/incidents"
	}
	return filepath.Join(home, ".kubetective", "incidents")
}

type Store struct{ dir string }

func NewStore(dir string) *Store { return &Store{dir: dir} }

func NewDefaultStore() *Store { return NewStore(DefaultDir()) }

// line is one JSONL record. Meta is the first line of a file.
type line struct {
	Type        string                    `json:"type"` // meta | observation | result | action.audit | ...
	Meta        *incidentMetaLine         `json:"meta,omitempty"`
	Observation *model.Observation        `json:"observation,omitempty"`
	// Result is lazy-parsed: the file is an append-only log that also carries
	// foreign line kinds (action.audit etc.) whose fields must never break
	// replay (regression: audit's string "result" field collided here).
	Result json.RawMessage `json:"result,omitempty"`
}

type incidentMetaLine struct {
	IncidentID    string `json:"incident_id"`
	RecordVersion int    `json:"record_version"`
	EngineVersion string `json:"engine_version"`
	Target        string `json:"target,omitempty"`
	ClusterID     string `json:"cluster_id,omitempty"` // memory scoping (v0.9)
	Created       string `json:"created"`
}

// BuildIncident wraps an investigation result into an Incident for saving.
func BuildIncident(engineVersion string, req *api.InvestigationRequest, res *api.InvestigationResult, clusterID string) *model.Incident {
	return &model.Incident{
		ID: fmt.Sprintf("incident-%d-%s", time.Now().Unix(), slug(req.Target)),
		Meta: model.IncidentMeta{
			ClusterID:     clusterID,
			EngineVersion: engineVersion,
			RecordVersion: RecordVersion,
			Target:        req.Target.String(),
		},
		Observations: res.Observations,
		Result: &model.IncidentResultRecord{
			Hypotheses: res.Hypotheses,
			Findings:   res.Findings,
			Changes:    res.Changes,
			Gaps:       res.EvidenceGaps,
		},
	}
}

func slug(r model.ResourceRef) string {
	name := r.Name
	if name == "" {
		name = r.Namespace
	}
	if name == "" {
		name = "cluster"
	}
	name = strings.ToLower(name)
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, name)
	return strings.Trim(name, "-")
}

// Save writes the incident as JSONL and returns its file path.
func (s *Store) Save(inc *model.Incident) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(s.dir, inc.ID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)

	meta := incidentMetaLine{
		IncidentID:    inc.ID,
		RecordVersion: inc.Meta.RecordVersion,
		EngineVersion: inc.Meta.EngineVersion,
		Target:        inc.Meta.Target,
		ClusterID:     inc.Meta.ClusterID,
		Created:       time.Now().UTC().Format(time.RFC3339),
	}
	if err := enc.Encode(line{Type: "meta", Meta: &meta}); err != nil {
		return "", err
	}
	for i := range inc.Observations {
		o := inc.Observations[i]
		if err := enc.Encode(line{Type: "observation", Observation: &o}); err != nil {
			return "", err
		}
	}
	if inc.Result != nil {
		raw, err := json.Marshal(inc.Result)
		if err != nil {
			return "", err
		}
		if err := enc.Encode(line{Type: "result", Result: raw}); err != nil {
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads an incident record by ID (from the store dir) or by direct path.
func (s *Store) Load(id string) (*model.Incident, error) {
	path := id
	if strings.ContainsAny(id, "/\\") {
		// Direct path: use as-is, appending .jsonl if the caller omitted it.
		if !strings.HasSuffix(path, ".jsonl") {
			path += ".jsonl"
		}
	} else {
		if !strings.HasSuffix(path, ".jsonl") {
			path += ".jsonl"
		}
		path = filepath.Join(s.dir, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	inc := &model.Incident{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		switch l.Type {
		case "meta":
			if l.Meta != nil {
				inc.Meta.RecordVersion = l.Meta.RecordVersion
				inc.Meta.EngineVersion = l.Meta.EngineVersion
				inc.Meta.Target = l.Meta.Target
				inc.Meta.ClusterID = l.Meta.ClusterID
			}
		case "observation":
			if l.Observation != nil {
				inc.Observations = append(inc.Observations, *l.Observation)
			}
		case "result":
			if len(l.Result) > 0 {
				var rr model.IncidentResultRecord
				if err := json.Unmarshal(l.Result, &rr); err == nil {
					inc.Result = &rr
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if inc.Meta.RecordVersion == 0 {
		inc.Meta.RecordVersion = RecordVersion
	}
	return inc, nil
}

// List returns incident IDs, newest first.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	// Incident IDs embed a unix timestamp, so lexical order is chronological;
	// newest first matches the documented contract.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

// ReplayCollector serves recorded observations back to the pipeline, making
// replay and the benchmark run the exact same code path as live investigation.
type ReplayCollector struct {
	observations []model.Observation
}

var _ collect.Collector = (*ReplayCollector)(nil)

func NewReplayCollector(observations []model.Observation) *ReplayCollector {
	return &ReplayCollector{observations: observations}
}

func (r *ReplayCollector) ID() string { return "replay" }

func (r *ReplayCollector) Collect(_ context.Context, _ *collect.ScopePlan) ([]model.Observation, []model.SourceRef, error) {
	refs := make([]model.SourceRef, 0, len(r.observations))
	seen := make(map[string]bool)
	for _, o := range r.observations {
		if !seen[o.Source.Query] {
			seen[o.Source.Query] = true
			refs = append(refs, o.Source)
		}
	}
	return r.observations, refs, nil
}
