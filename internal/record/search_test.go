package record

import (
	"strconv"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func seedStore(t *testing.T, incidents ...*model.Incident) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	for _, inc := range incidents {
		if _, err := s.Save(inc); err != nil {
			t.Fatalf("Save %s: %v", inc.ID, err)
		}
	}
	return s
}

func incident(id, target, cluster string, findings ...model.Finding) *model.Incident {
	return &model.Incident{
		ID:   id,
		Meta: model.IncidentMeta{Target: target, ClusterID: cluster, RecordVersion: RecordVersion},
		Observations: []model.Observation{
			collect.NewObservation("pod.state", model.SourceRef{System: "k8s"}, time.Now(), model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout"}, nil, 1.0),
		},
		Result: &model.IncidentResultRecord{Findings: findings},
	}
}

func f(analyzer string, sev model.Severity) model.Finding {
	return model.Finding{ID: analyzer + ".1", Analyzer: analyzer, Severity: sev, Title: analyzer, Timestamp: time.Now()}
}

func TestSearchFilters(t *testing.T) {
	now := time.Now().UTC()
	a := now.Add(-1 * time.Hour)
	b := now.Add(-3 * time.Hour)
	c := now.Add(-7 * 24 * time.Hour)
	store := seedStore(t,
		incident("incident-"+strconv.FormatInt(a.Unix(), 10)+"-checkout", "deployment/prod/checkout", "prod-cluster-a",
			f("oom", model.SevCritical), f("crashloop", model.SevHigh)),
		incident("incident-"+strconv.FormatInt(b.Unix(), 10)+"-payments", "deployment/prod/payments", "prod-cluster-a",
			f("crashloop", model.SevHigh)),
		incident("incident-"+strconv.FormatInt(c.Unix(), 10)+"-staging", "deployment/staging/web", "stage-cluster",
			f("imagepull", model.SevWarning)),
	)

	tests := []struct {
		name string
		q    SearchQuery
		want []string
	}{
		{"no filters", SearchQuery{}, []string{"incident-" + strconv.FormatInt(a.Unix(), 10) + "-checkout", "incident-" + strconv.FormatInt(b.Unix(), 10) + "-payments", "incident-" + strconv.FormatInt(c.Unix(), 10) + "-staging"}},
		{"target substring", SearchQuery{Target: "payments"}, []string{"incident-" + strconv.FormatInt(b.Unix(), 10) + "-payments"}},
		{"cluster substring", SearchQuery{Cluster: "stage"}, []string{"incident-" + strconv.FormatInt(c.Unix(), 10) + "-staging"}},
		{"analyzer crashloop", SearchQuery{Analyzer: "crashloop"}, []string{"incident-" + strconv.FormatInt(a.Unix(), 10) + "-checkout", "incident-" + strconv.FormatInt(b.Unix(), 10) + "-payments"}},
		{"severity option", SearchQuery{Severity: model.SevHigh}, []string{"incident-" + strconv.FormatInt(a.Unix(), 10) + "-checkout", "incident-" + strconv.FormatInt(b.Unix(), 10) + "-payments"}},
		{"severity critical", SearchQuery{Severity: model.SevCritical}, []string{"incident-" + strconv.FormatInt(a.Unix(), 10) + "-checkout"}},
		{"since window", SearchQuery{Since: now.Add(-2 * time.Hour)}, []string{"incident-" + strconv.FormatInt(a.Unix(), 10) + "-checkout"}},
		{"until window", SearchQuery{Until: now.Add(-2 * time.Hour)}, []string{"incident-" + strconv.FormatInt(b.Unix(), 10) + "-payments", "incident-" + strconv.FormatInt(c.Unix(), 10) + "-staging"}},
		{"combined", SearchQuery{Target: "checkout", Analyzer: "crashloop"}, []string{"incident-" + strconv.FormatInt(a.Unix(), 10) + "-checkout"}},
		{"limit", SearchQuery{Limit: 1}, []string{"incident-" + strconv.FormatInt(a.Unix(), 10) + "-checkout"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Search(tc.q)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Search = %d hits, want %d", len(got), len(tc.want))
			}
			for i, g := range got {
				if g.IncidentID != tc.want[i] {
					t.Errorf("hit %d = %s, want %s", i, g.IncidentID, tc.want[i])
				}
			}
		})
	}
}

func TestSearchSkipsUnreadable(t *testing.T) {
	store := seedStore(t, incident("incident-1000000000-x", "deployment/prod/x", "c",
		f("oom", model.SevCritical)))
	// A future-version record must not break the scan (mirrors memory).
	if _, err := store.Save(&model.Incident{
		ID:           "incident-2000000000-future",
		Meta:         model.IncidentMeta{RecordVersion: RecordVersion + 1},
		Observations: []model.Observation{},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Search(SearchQuery{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].IncidentID != "incident-1000000000-x" {
		t.Fatalf("Search = %+v, want only the readable record", got)
	}
}

func TestIDTimestamp(t *testing.T) {
	if ts := idTimestamp("incident-1786199326-crashy-pod"); ts.Unix() != 1786199326 {
		t.Errorf("idTimestamp = %v, want unix 1786199326", ts)
	}
	if !idTimestamp("bogus").IsZero() {
		t.Error("idTimestamp of malformed id must be zero")
	}
}
