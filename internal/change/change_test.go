package change

import (
	"testing"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/graph"
	"github.com/kubedoctor/kubedoctor/internal/model"
)

func mk(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestDetectFromEventsAndDeployments(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	dep := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}

	changes := Detect([]model.Observation{
		mk("deployment.state", dep, base.Add(-14*time.Minute), map[string]any{}),
		mk("event.recorded", dep, base.Add(-14*time.Minute), map[string]any{"reason": "ScalingReplicaSet", "message": "Scaled up replica set checkout-7f84c9 to 3"}),
		mk("event.recorded", dep, base.Add(-2*time.Minute), map[string]any{"reason": "Scheduled", "message": "pod scheduled"}), // not a change reason
		mk("pod.state", pod, base.Add(-13*time.Minute), map[string]any{"phase": "Running"}),
	}, base.Add(-30*time.Minute))

	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3 (deployment.state, ScalingReplicaSet, pod created): %+v", len(changes), changes)
	}
}

func TestRankPrefersTemporalAndOwnership(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	dep := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}
	rs := model.ResourceRef{Kind: "replicaset", Namespace: "prod", Name: "checkout-7f84c9"}
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}
	node := model.ResourceRef{Kind: "node", Name: "node-a"}
	other := model.ResourceRef{Kind: "configmap", Namespace: "prod", Name: "unrelated"}

	g := graph.Build([]model.Observation{
		mk("resource.owner", pod, base, map[string]any{"owner_kind": "ReplicaSet", "owner_name": "checkout-7f84c9"}),
		mk("resource.owner", rs, base, map[string]any{"owner_kind": "Deployment", "owner_name": "checkout"}),
		mk("pod.state", pod, base, map[string]any{"node": "node-a"}),
	}, nil, nil, graph.Options{})

	changes := []model.Change{
		{Resource: dep, Timestamp: base.Add(-14 * time.Minute), Description: "deployment v41 → v42"},
		{Resource: other, Timestamp: base.Add(-15 * time.Minute), Description: "configmap unrelated"},
		{Resource: node, Timestamp: base.Add(-16 * time.Minute), Description: "node thing"},
	}
	ranked := Rank(changes, g, pod, base, 30*time.Minute, nil)

	if ranked[0].Resource != dep {
		t.Fatalf("top change = %v, want deployment checkout (ownership + temporal)", ranked[0].Resource)
	}
	// Deterministic check of the formula:
	// temporal = 1 − 14m/30m = 0.533; graph hops dep→rs→pod = 2 → 1/3 = 0.333;
	// ownership = 1.0. relevance = 0.45·0.533 + 0.30·0.333 + 0.15·1.0 = 0.49.
	if got, want := ranked[0].Relevance, 0.49; abs(got-want) > 0.01 {
		t.Errorf("deployment relevance = %.2f, want ≈ %.2f", got, want)
	}
	if ranked[1].Resource != other && ranked[1].Resource != node {
		t.Errorf("second change = %v, want one of the unrelated ones", ranked[1].Resource)
	}
	// Deterministic ordering by relevance desc.
	for i := 1; i < len(ranked); i++ {
		if ranked[i].Relevance > ranked[i-1].Relevance {
			t.Fatalf("not sorted by relevance desc at %d", i)
		}
	}
	// Factors must be recorded and sum to the relevance (explainability).
	sum := 0.0
	for _, v := range ranked[0].Factors {
		sum += v
	}
	if abs(sum-ranked[0].Relevance) > 0.001 {
		t.Errorf("factors sum = %.3f, want %.3f", sum, ranked[0].Relevance)
	}
}

func TestAnomalyScoreCooccurringMetricGrowth(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"}
	ch := model.Change{Resource: pod, Timestamp: base.Add(-3 * time.Minute)}

	// Growing series within delta → anomaly 1.0.
	with := []model.Observation{
		mk("metric.series", pod, base.Add(-4*time.Minute), map[string]any{"first": 4.10e+08, "last": 1.02e+09}),
	}
	if got := AnomalyScore(with, ch, 5*time.Minute); got != 1.0 {
		t.Errorf("AnomalyScore(growing, near) = %v, want 1.0", got)
	}
	// Flat series → no anomaly.
	flat := []model.Observation{
		mk("metric.series", pod, base.Add(-4*time.Minute), map[string]any{"first": 4.10e+08, "last": 4.20e+08}),
	}
	if got := AnomalyScore(flat, ch, 5*time.Minute); got != 0 {
		t.Errorf("AnomalyScore(flat, near) = %v, want 0", got)
	}
	// Growing but far in time → no anomaly.
	far := []model.Observation{
		mk("metric.series", pod, base.Add(-2*time.Hour), map[string]any{"first": 4.10e+08, "last": 1.02e+09}),
	}
	if got := AnomalyScore(far, ch, 5*time.Minute); got != 0 {
		t.Errorf("AnomalyScore(growing, far) = %v, want 0", got)
	}
	// Anomaly factor must lift relevance by 0.10.
	ranked := Rank([]model.Change{ch}, &model.Graph{}, pod, base, 30*time.Minute, func(model.Change) float64 { return 1.0 })
	if ranked[0].Factors["anomaly"] != 0.10 {
		t.Errorf("anomaly factor = %v, want 0.10", ranked[0].Factors["anomaly"])
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
