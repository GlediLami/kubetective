package memory

import (
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
)

func inc(id, target string, kinds ...string) *model.Incident {
	var obs []model.Observation
	for _, k := range kinds {
		obs = append(obs, model.Observation{Kind: k, Resource: model.ResourceRef{Kind: "pod", Name: "p"}, Timestamp: time.Now()})
	}
	return &model.Incident{
		ID:           id,
		Meta:         model.IncidentMeta{Target: target, ClusterID: "c1"},
		Observations: obs,
	}
}

func TestSignatureIsKindSetHash(t *testing.T) {
	a := inc("a", "deployment/prod/checkout", "container.terminated", "container.waiting", "event.recorded")
	b := inc("b", "deployment/prod/api", "event.recorded", "container.terminated", "container.waiting") // same set, other order
	if Signature(a) != Signature(b) {
		t.Error("same kind set must produce the same signature regardless of order")
	}
	c := inc("c", "deployment/prod/checkout", "container.terminated", "container.waiting", "git.commit")
	if Signature(a) == Signature(c) {
		t.Error("different kind sets must produce different signatures")
	}
}

func TestSimilarRanksByOverlap(t *testing.T) {
	store := record.NewStore(t.TempDir())
	// Two OOM-shaped incidents share the failure shape.
	oom1 := inc("inc-1", "deployment/prod/checkout", "pod.state", "container.spec", "container.terminated", "container.waiting", "event.recorded", "node.condition")
	oom2 := inc("inc-2", "deployment/prod/api", "pod.state", "container.spec", "container.terminated", "container.waiting", "event.recorded", "node.condition")
	// An image-pull-shaped incident shares less.
	img := inc("inc-3", "deployment/prod/worker", "pod.state", "container.waiting", "event.recorded", "node.condition")
	for _, i := range []*model.Incident{oom1, oom2, img} {
		if _, err := store.Save(i); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := Similar(store, "inc-1", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (self excluded)", len(matches))
	}
	if matches[0].IncidentID != "inc-2" {
		t.Errorf("top match = %s, want inc-2 (identical shape)", matches[0].IncidentID)
	}
	if matches[0].Overlap != 1.0 {
		t.Errorf("overlap = %v, want 1.0 for identical kind sets", matches[0].Overlap)
	}
	if matches[1].IncidentID != "inc-3" {
		t.Errorf("second match = %s, want inc-3", matches[1].IncidentID)
	}
	if matches[1].Overlap >= matches[0].Overlap {
		t.Errorf("overlaps not ranked: %v >= %v", matches[1].Overlap, matches[0].Overlap)
	}
	if len(matches[0].SharedKinds) != 6 {
		t.Errorf("shared kinds = %v", matches[0].SharedKinds)
	}
	if matches[0].Cluster != "c1" {
		t.Errorf("match cluster = %q, want c1", matches[0].Cluster)
	}
}

func TestSimilarScopedToCluster(t *testing.T) {
	store := record.NewStore(t.TempDir())
	// Same failure shape, different clusters.
	ours := inc("inc-a", "deployment/prod/checkout", "pod.state", "container.terminated")
	ours.Meta.ClusterID = "cluster-one"
	other := inc("inc-b", "deployment/prod/api", "pod.state", "container.terminated")
	other.Meta.ClusterID = "cluster-two"
	for _, i := range []*model.Incident{ours, other} {
		if _, err := store.Save(i); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := Similar(store, "inc-a", 5, "cluster-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (cluster-two incident excluded by scope)", len(matches))
	}
	// Strict scoping: untagged incidents (no cluster id) are excluded from
	// scoped queries too - unscoped memory must never leak into a scoped
	// query (regression: legacy records bypassed the cluster filter).
	legacy := inc("inc-c", "deployment/prod/pay", "pod.state", "container.terminated")
	legacy.Meta.ClusterID = ""
	if _, err := store.Save(legacy); err != nil {
		t.Fatal(err)
	}
	matches, err = Similar(store, "inc-a", 5, "cluster-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("matches = %+v, want none (untagged incident must not leak into the scope)", matches)
	}
	// Untagged incidents match when NO scope is requested, alongside every
	// other same-shape record.
	matches, err = Similar(store, "inc-a", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"inc-b": true, "inc-c": true}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want inc-b + inc-c", matches)
	}
	for _, m := range matches {
		if !want[m.IncidentID] {
			t.Errorf("unexpected match %s", m.IncidentID)
		}
	}
}

func TestSimilarEmptyStore(t *testing.T) {
	store := record.NewStore(t.TempDir())
	if _, err := store.Save(inc("only", "deployment/prod/x", "pod.state")); err != nil {
		t.Fatal(err)
	}
	matches, err := Similar(store, "only", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (no other incidents)", len(matches))
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		overlap float64
		want    string
	}{{0.9, "strong"}, {0.5, "moderate"}, {0.1, "weak"}}
	for _, c := range cases {
		if got := Describe(c.overlap); got != c.want {
			t.Errorf("Describe(%v) = %q, want %q", c.overlap, got, c.want)
		}
	}
}
