package graph

import (
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func mk(kind string, res model.ResourceRef, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, time.Now(), res, payload, 1.0)
}

func mkat(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestBuildStructuralEdges(t *testing.T) {
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9-abcde"}
	rs := model.ResourceRef{Kind: "replicaset", Namespace: "prod", Name: "checkout-7f84c9"}
	dep := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}
	node := model.ResourceRef{Kind: "node", Name: "node-a"}

	g := Build([]model.Observation{
		mk("resource.owner", pod, map[string]any{"owner_kind": "ReplicaSet", "owner_name": "checkout-7f84c9"}),
		mk("resource.owner", rs, map[string]any{"owner_kind": "Deployment", "owner_name": "checkout"}),
		mk("pod.state", pod, map[string]any{"node": "node-a"}),
		mk("deployment.state", dep, map[string]any{}),
	}, nil, nil, Options{})

	if len(g.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (%v)", len(g.Nodes), g.Nodes)
	}
	wantEdges := map[string]bool{
		dep.String() + " --OWNS--> " + rs.String():      true,
		rs.String() + " --OWNS--> " + pod.String():      true,
		pod.String() + " --RUNS_ON--> " + node.String(): true,
	}
	if len(g.Edges) != 3 {
		t.Fatalf("edges = %d, want 3: %v", len(g.Edges), g.Edges)
	}
	for _, e := range g.Edges {
		key := e.From.String() + " --" + string(e.Kind) + "--> " + e.To.String()
		if !wantEdges[key] {
			t.Errorf("unexpected edge %s", key)
		}
	}
}

func TestChangedBeforeEdgesAndHops(t *testing.T) {
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "p1"}
	dep := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}
	node := model.ResourceRef{Kind: "node", Name: "node-a"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)

	g := Build([]model.Observation{
		mk("resource.owner", pod, map[string]any{"owner_kind": "Deployment", "owner_name": "checkout"}),
		mk("pod.state", pod, map[string]any{"node": "node-a"}),
	}, []model.Change{
		{Resource: dep, Timestamp: base.Add(-5 * time.Minute), Description: "checkout v41 → v42"},
	}, &model.TimelineEvent{Observation: model.Observation{Timestamp: base}}, Options{})

	// dep --OWNS--> pod + dep --CHANGED_BEFORE--> pod (via ownership) + pod --RUNS_ON--> node.
	if len(g.Edges) != 3 {
		t.Fatalf("edges = %d, want 3: %v", len(g.Edges), g.Edges)
	}
	hasChangedBefore := false
	for _, e := range g.Edges {
		if e.Kind == model.EdgeChangedBefore && e.From == dep && e.To == pod {
			hasChangedBefore = true
		}
	}
	if !hasChangedBefore {
		t.Error("expected dep --CHANGED_BEFORE--> pod edge")
	}
	if h := Hops(g, dep, node); h != 2 {
		t.Errorf("Hops(dep, node) = %d, want 2 (dep→pod→node)", h)
	}
	if h := Hops(g, pod, pod); h != 0 {
		t.Errorf("Hops(pod, pod) = %d, want 0", h)
	}
	if h := Hops(g, pod, model.ResourceRef{Kind: "pod", Name: "unrelated"}); h != -1 {
		t.Errorf("Hops to unrelated = %d, want -1", h)
	}
}

func TestTemporallyCorrelatedEdges(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"}
	dep := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}
	ch := model.Change{Resource: dep, Timestamp: base.Add(-14 * time.Minute), Description: "checkout v41 → v42"}

	g := Build([]model.Observation{
		mk("resource.owner", pod, map[string]any{"owner_kind": "Deployment", "owner_name": "checkout"}),
		mkat("metric.series", pod, base.Add(-13*time.Minute), map[string]any{"first": 4.10e+08, "last": 1.02e+09}),
		mkat("metric.series", pod, base.Add(-6*time.Hour), map[string]any{"first": 4.10e+08, "last": 1.02e+09}), // too far → no edge
	}, []model.Change{ch}, nil, Options{})

	found := false
	for _, e := range g.Edges {
		if e.Kind == model.EdgeTemporallyCorrelated && e.From == dep && e.To == pod {
			found = true
		}
	}
	if !found {
		t.Errorf("missing TEMPORALLY_CORRELATED dep→pod edge: %v", g.Edges)
	}
	// Only one correlated edge: the far series must not correlate.
	correlated := 0
	for _, e := range g.Edges {
		if e.Kind == model.EdgeTemporallyCorrelated {
			correlated++
		}
	}
	if correlated != 1 {
		t.Errorf("TEMPORALLY_CORRELATED edges = %d, want 1", correlated)
	}
}

func TestBuildNodeBudget(t *testing.T) {
	var obs []model.Observation
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "p"}
	for i := 0; i < 10; i++ {
		obs = append(obs, mk("pod.state", pod, map[string]any{"node": "node-" + string(rune('a'+i))}))
	}
	g := Build(obs, nil, nil, Options{MaxNodes: 3})
	if len(g.Nodes) > 3 {
		t.Fatalf("nodes = %d, want ≤ 3 (budget)", len(g.Nodes))
	}
	if !g.Bounds.Truncated {
		t.Error("truncation must be recorded (visible, not silent)")
	}
}
