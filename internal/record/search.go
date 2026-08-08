package record

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

// SearchQuery filters the incident store. The store is a linear scan over
// JSONL by design ("v1.0 memory" scale); an indexed store (SQLite) replaces
// it once the scan is the bottleneck - roadmap v1.x memory v2.
type SearchQuery struct {
	Target   string         // substring match on Meta.Target
	Cluster  string         // substring match on Meta.ClusterID
	Analyzer string         // substring match on a finding analyzer
	Severity model.Severity // minimum finding severity ("" = any)
	Since    time.Time      // zero = unbounded
	Until    time.Time      // zero = unbounded
	Limit    int            // 0 = all
}

// SearchHit is one matching incident with the searchable facets.
type SearchHit struct {
	IncidentID string
	Created    time.Time // from the ID's unix prefix
	Target     string
	Cluster    string
	Analyzers  []string // unique analyzers, sorted
	Severity   model.Severity
}

var severityRank = map[model.Severity]int{
	model.SevInfo: 0, model.SevWarning: 1, model.SevHigh: 2, model.SevCritical: 3,
}

// Search returns matching incidents, newest first, limited to Limit when > 0.
// Records that fail to load (future schema, corruption) are skipped,
// mirroring memory.Similar's tolerance.
func (s *Store) Search(q SearchQuery) ([]SearchHit, error) {
	ids, err := s.List()
	if err != nil {
		return nil, err
	}
	var hits []SearchHit
	for _, id := range ids {
		inc, err := s.Load(id)
		if err != nil {
			continue
		}
		hit := SearchHit{
			IncidentID: id,
			Created:    idTimestamp(id),
			Target:     inc.Meta.Target,
			Cluster:    inc.Meta.ClusterID,
		}
		if !q.Since.IsZero() && hit.Created.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && hit.Created.After(q.Until) {
			continue
		}
		if q.Target != "" && !strings.Contains(hit.Target, q.Target) {
			continue
		}
		if q.Cluster != "" && !strings.Contains(hit.Cluster, q.Cluster) {
			continue
		}

		analyzers, sev := facets(inc)
		if q.Analyzer != "" && !strings.Contains(strings.Join(analyzers, " "), q.Analyzer) {
			continue
		}
		if q.Severity != "" && severityRank[sev] < severityRank[q.Severity] {
			continue
		}
		hit.Analyzers = analyzers
		hit.Severity = sev
		hits = append(hits, hit)
	}
	if q.Limit > 0 && len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

// facets returns the unique sorted analyzers and worst severity of an
// incident's findings.
func facets(inc *model.Incident) ([]string, model.Severity) {
	set := map[string]bool{}
	var worst model.Severity
	for _, f := range findingsOf(inc) {
		set[f.Analyzer] = true
		if severityRank[f.Severity] > severityRank[worst] {
			worst = f.Severity
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out, worst
}

func findingsOf(inc *model.Incident) []model.Finding {
	if inc == nil || inc.Result == nil {
		return nil
	}
	return inc.Result.Findings
}

// idTimestamp parses the unix seconds prefix of an incident ID
// ("incident-1786199326-slug"). Zero on malformed ids.
func idTimestamp(id string) time.Time {
	rest, ok := strings.CutPrefix(id, "incident-")
	if !ok {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(strings.SplitN(rest, "-", 2)[0], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}

// String renders one line of `incidents search` output.
func (h SearchHit) String() string {
	sev := string(h.Severity)
	if sev == "" {
		sev = "-"
	}
	return fmt.Sprintf("%-46s %s %-8s target=%-34s cluster=%s analyzers=%s",
		h.IncidentID, h.Created.Format("2006-01-02 15:04"), sev, h.Target, h.Cluster, strings.Join(h.Analyzers, ","))
}
