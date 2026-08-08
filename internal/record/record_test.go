package record

import (
	"context"
	"encoding/json"
	"os"
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
	inc := BuildIncident("v0.1.0", req, res, "cluster-abc")
	if inc.ID == "" || !strings.Contains(inc.ID, "checkout") {
		t.Fatalf("incident ID = %q, want slug from target", inc.ID)
	}
	if inc.Meta.ClusterID != "cluster-abc" {
		t.Fatalf("ClusterID = %q, want cluster-abc", inc.Meta.ClusterID)
	}
}

// TestTargetSurvivesSaveLoad is the regression for the meta-line bug where
// Save wrote UserNote instead of Target, breaking replay/action_preview of
// live incidents.
func TestTargetSurvivesSaveLoad(t *testing.T) {
	req := &api.InvestigationRequest{Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}}
	inc := BuildIncident("v0.1.0", req, &api.InvestigationResult{}, "cluster-abc")
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
	if got.Meta.ClusterID != "cluster-abc" {
		t.Errorf("Meta.ClusterID after round-trip = %q, want cluster-abc", got.Meta.ClusterID)
	}
}

// TestRecordVersionContract pins the upgrade rules in the load path:
//
//   - a record with a version that is OLDER (or equal) than the build reads
//     cleanly (zero-migration forward reads) — including legacy v0 records
//     that have no meta line at all, which default to the current version;
//   - a record with a NEWER version must be rejected, not silently read as
//     if it were current (that is the contract that lets us ship schema
//     changes as version bumps without a migration engine).
func TestRecordVersionContract(t *testing.T) {
	t.Run("older version reads", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		inc := &model.Incident{
			ID:           "incident-v1-old",
			Meta:         model.IncidentMeta{RecordVersion: 1},
			Observations: []model.Observation{testObservation("pod.state", time.Now())},
		}
		if _, err := store.Save(inc); err != nil {
			t.Fatal(err)
		}
		// Rewrite the version down to simulate a record from an older engine.
		if err := rewriteMetaVersion(store, inc.ID, 1); err != nil {
			t.Fatal(err)
		}
		got, err := store.Load(inc.ID)
		if err != nil {
			t.Fatalf("older-version record must load, got: %v", err)
		}
		if got.Meta.RecordVersion != 1 {
			t.Errorf("RecordVersion = %d, want 1 preserved", got.Meta.RecordVersion)
		}
		if len(got.Observations) != 1 {
			t.Errorf("observations lost on older-version load")
		}
	})

	t.Run("no meta line defaults to current", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		inc := &model.Incident{
			ID:           "incident-v0-nometa",
			Observations: []model.Observation{testObservation("pod.state", time.Now())},
		}
		if _, err := store.Save(inc); err != nil {
			t.Fatal(err)
		}
		if err := stripMetaLine(store, inc.ID); err != nil {
			t.Fatal(err)
		}
		got, err := store.Load(inc.ID)
		if err != nil {
			t.Fatalf("v0-style record must load, got: %v", err)
		}
		if got.Meta.RecordVersion != RecordVersion {
			t.Errorf("RecordVersion = %d, want default %d", got.Meta.RecordVersion, RecordVersion)
		}
	})

	t.Run("newer version rejected", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		inc := &model.Incident{
			ID:           "incident-future",
			Meta:         model.IncidentMeta{RecordVersion: RecordVersion + 1},
			Observations: []model.Observation{testObservation("pod.state", time.Now())},
		}
		if _, err := store.Save(inc); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(inc.ID); err == nil {
			t.Fatal("future-version record must be rejected")
		}
	})
}

// rewriteMetaVersion rewrites the meta line of a saved record to the given
// record version (simulating a record produced by an older engine).
func rewriteMetaVersion(store *Store, id string, version int) error {
	path := filepath.Join(store.dir, id+".jsonl")
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	var out []string
	for _, l := range lines {
		var meta incidentMetaLine
		if json.Unmarshal([]byte(l), &meta) == nil && meta.IncidentID == id {
			raw, err := json.Marshal(map[string]any{
				"type": "meta",
				"meta": map[string]any{"incident_id": id, "record_version": version},
			})
			if err != nil {
				return err
			}
			out = append(out, string(raw))
			continue
		}
		out = append(out, l)
	}
	return writeLines(path, out)
}

// stripMetaLine removes the meta line entirely, emulating v0-era records.
func stripMetaLine(store *Store, id string) error {
	path := filepath.Join(store.dir, id+".jsonl")
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	var out []string
	for _, l := range lines {
		var meta incidentMetaLine
		if json.Unmarshal([]byte(l), &meta) == nil && meta.IncidentID == id {
			continue
		}
		out = append(out, l)
	}
	return writeLines(path, out)
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

func writeLines(path string, lines []string) error {
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// TestRecordFilePermissions pins the security-review fix: incident files and
// the store directory must be owner-only (they may hold log snippets).
func TestRecordFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s := NewStore(dir)
	inc := &model.Incident{ID: "incident-perm", Observations: []model.Observation{testObservation("pod.state", time.Now())}}
	if _, err := s.Save(inc); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, inc.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("record file mode = %o, want 600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir mode = %o, want 700", perm)
	}
}
