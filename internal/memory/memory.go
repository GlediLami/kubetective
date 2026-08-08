// Package memory implements incident memory v1 ("seen this before?",
// roadmap v0.8): incidents are fingerprinted by the SET of observation kinds
// (the symptom + change shape), and similarity is ranked by Jaccard overlap
// between those sets. Deliberately no embeddings: cheaper, explainable, and
// sufficient at this scale (the design's SQLite index is deferred until the
// store outgrows a linear scan).
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
)

// Match is one similar past incident.
type Match struct {
	IncidentID  string   `json:"incident_id"`
	Target      string   `json:"target"`
	Cluster     string   `json:"cluster,omitempty"`
	Overlap     float64  `json:"overlap"` // Jaccard similarity of kind sets
	SharedKinds []string `json:"shared_kinds,omitempty"`
}

// Signature hashes the sorted unique observation kinds of an incident.
// Two incidents with the same failure shape get the same signature; the
// hash is informational only (ranking uses the sets directly).
func Signature(inc *model.Incident) string {
	h := sha256.New()
	for _, k := range kindsOf(inc) {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// kindsOf returns the sorted unique observation kinds.
func kindsOf(inc *model.Incident) []string {
	seen := map[string]bool{}
	for _, o := range inc.Observations {
		seen[o.Kind] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jaccard is |A ∩ B| / |A ∪ B|; 0 for disjoint sets.
func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	as := map[string]bool{}
	for _, k := range a {
		as[k] = true
	}
	intersect := 0
	union := map[string]bool{}
	for _, k := range a {
		union[k] = true
	}
	for _, k := range b {
		union[k] = true
		if as[k] {
			intersect++
		}
	}
	if len(union) == 0 {
		return 0
	}
	return float64(intersect) / float64(len(union))
}

// Similar ranks past incidents by kind-set overlap with the given incident,
// newest first within equal overlap. topN ≤ 0 means all. When clusterID is
// non-empty, only incidents from that cluster are considered (multi-cluster
// memory scoping, v0.9).
func Similar(store *record.Store, incidentID string, topN int, clusterID string) ([]Match, error) {
	target, err := store.Load(incidentID)
	if err != nil {
		return nil, fmt.Errorf("load incident %q: %w", incidentID, err)
	}
	targetKinds := kindsOf(target)

	ids, err := store.List()
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, id := range ids {
		if id == incidentID {
			continue
		}
		other, err := store.Load(id)
		if err != nil {
			continue // skip unreadable records
		}
		if clusterID != "" && other.Meta.ClusterID != "" && other.Meta.ClusterID != clusterID {
			continue
		}
		overlap := jaccard(targetKinds, kindsOf(other))
		if overlap <= 0 {
			continue
		}
		m := Match{
			IncidentID: id,
			Target:     other.Meta.Target,
			Cluster:    other.Meta.ClusterID,
			Overlap:    overlap,
		}
		for _, k := range targetKinds {
			for _, ok := range kindsOf(other) {
				if k == ok {
					m.SharedKinds = append(m.SharedKinds, k)
				}
			}
		}
		matches = append(matches, m)
	}
	// Rank by overlap desc, then id (timestamp-prefixed) desc for newest-first.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Overlap != matches[j].Overlap {
			return matches[i].Overlap > matches[j].Overlap
		}
		return matches[i].IncidentID > matches[j].IncidentID
	})
	if topN > 0 && len(matches) > topN {
		matches = matches[:topN]
	}
	return matches, nil
}

// Describe renders the overlap as a human string ("strong" ≥ 0.7,
// "moderate" ≥ 0.4, else "weak").
func Describe(overlap float64) string {
	switch {
	case overlap >= 0.7:
		return "strong"
	case overlap >= 0.4:
		return "moderate"
	default:
		return "weak"
	}
}
