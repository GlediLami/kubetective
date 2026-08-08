package record

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

func testObservation(kind string, ts time.Time) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "k8s", Query: "GET /pods/x"}, ts,
		model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"},
		map[string]any{"reason": "OOMKilled"}, 1.0)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	obs := []model.Observation{
		testObservation("container.terminated", time.Date(2026, 8, 7, 14, 6, 3, 0, time.UTC)),
		testObservation("pod.state", time.Date(2026, 8, 7, 14, 2, 0, 0, time.UTC)),
	}
	inc := &model.Incident{
		ID: "incident-1-oom",
		Meta: model.IncidentMeta{
			ClusterID:     "test-cluster",
			EngineVersion: "v0.1.0-test",
			RecordVersion: RecordVersion,
		},
		Observations: obs,
		Result: &model.IncidentResultRecord{
			Findings: []model.Finding{{ID: "oom.p1", Analyzer: "oom", Severity: model.SevHigh, Title: "OOMKilled"}},
		},
	}
	path, err := s.Save(inc)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Base(path) != inc.ID+".jsonl" {
		t.Fatalf("saved file = %s, want %s.jsonl", filepath.Base(path), inc.ID)
	}

	got, err := s.Load(inc.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(got.Observations))
	}
	if got.Observations[0].ID != obs[0].ID {
		t.Errorf("observation ID changed across round trip: %s != %s", got.Observations[0].ID, obs[0].ID)
	}
	if got.Result == nil || len(got.Result.Findings) != 1 {
		t.Fatalf("result findings lost in round trip: %+v", got.Result)
	}
	if got.Meta.RecordVersion != RecordVersion {
		t.Errorf("record version = %d, want %d", got.Meta.RecordVersion, RecordVersion)
	}
}

func TestListAndMissing(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.Save(&model.Incident{ID: "incident-1-x", Observations: []model.Observation{testObservation("pod.state", time.Now())}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ids, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 1 || ids[0] != "incident-1-x" {
		t.Fatalf("List = %v, want [incident-1-x]", ids)
	}
	if _, err := s.Load("does-not-exist"); err == nil {
		t.Fatal("Load of missing incident must fail")
	}
	// Missing dir → empty list, not an error.
	empty, err := NewStore(filepath.Join(dir, "nope")).List()
	if err != nil || len(empty) != 0 {
		t.Fatalf("List on missing dir = %v, %v; want empty, nil", empty, err)
	}
}

func TestReplayCollector(t *testing.T) {
	obs := []model.Observation{testObservation("container.terminated", time.Now())}
	rc := NewReplayCollector(obs)
	got, refs, err := rc.Collect(context.Background(), &collect.ScopePlan{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got) != 1 || got[0].ID != obs[0].ID {
		t.Fatalf("replay collector returned %d observations, want 1 with same ID", len(got))
	}
	if len(refs) == 0 {
		t.Fatal("replay collector must return source refs (auditability)")
	}
}

func TestBuildIncidentUsesRequestTarget(t *testing.T) {
	req := &api.InvestigationRequest{Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}}
	res := &api.InvestigationResult{}
	inc := BuildIncident("v0.1.0", req, res)
	if inc.ID == "" || !strings.Contains(inc.ID, "checkout") {
		t.Fatalf("incident ID = %q, want slug from target", inc.ID)
	}
}

// TestTargetSurvivesSaveLoad is the regression for the meta-line bug where
// Save wrote UserNote instead of Target, breaking replay/action_preview of
// live incidents.
func TestTargetSurvivesSaveLoad(t *testing.T) {
	req := &api.InvestigationRequest{Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}}
	inc := BuildIncident("v0.1.0", req, &api.InvestigationResult{})
	inc.Meta.Target = "deployment/prod/checkout"

	store := NewStore(t.TempDir())
	if _, err := store.Save(inc); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta.Target != "deployment/prod/checkout" {
		t.Errorf("Meta.Target after round-trip = %q, want deployment/prod/checkout", got.Meta.Target)
	}
}
