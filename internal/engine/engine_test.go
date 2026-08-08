package engine

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/analyze/crashloop"
	"github.com/GlediLami/kubetective/internal/analyze/imagepull"
	"github.com/GlediLami/kubetective/internal/analyze/oom"
	"github.com/GlediLami/kubetective/internal/analyze/scheduling"
	"github.com/GlediLami/kubetective/internal/collect"
	k8scollect "github.com/GlediLami/kubetective/internal/collect/kubernetes"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

// testEngine wires the production collector + analyzer set (same as the CLI).
func testEngine(client kubernetes.Interface) *Engine {
	reg := collect.NewRegistry()
	reg.Register(k8scollect.New(client))
	ar := analyze.NewRegistry()
	ar.Register(oom.New())
	ar.Register(crashloop.New())
	ar.Register(imagepull.New())
	ar.Register(scheduling.New())
	return New(reg, ar)
}

// oomFixture builds a fake cluster: deployment checkout → RS → pod whose
// container was OOMKilled 3 times (restart count 3), on a healthy node.
func oomFixture(t *testing.T) (client *fake.Clientset, podName string) {
	t.Helper()
	ns := "prod"
	start := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	podName = "checkout-7f84c9-abcde"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			UID:       types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "checkout-7f84c9", UID: types.UID("rs-uid"), Controller: boolPtr(true)},
			},
			CreationTimestamp: metav1.Time{Time: start},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{{
				Name:  "checkout",
				Image: "registry.example/checkout:v42",
				Resources: corev1.ResourceRequirements{
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: start},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "checkout",
				RestartCount: 3,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason:     "OOMKilled",
					ExitCode:   137,
					StartedAt:  metav1.Time{Time: start.Add(2 * time.Minute)},
					FinishedAt: metav1.Time{Time: start.Add(6 * time.Minute)},
				}},
			}},
		},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout-7f84c9",
			Namespace: ns,
			UID:       types.UID("rs-uid"),
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "checkout", UID: types.UID("dep-uid"), Controller: boolPtr(true)},
			},
		},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "checkout",
			Namespace:         ns,
			UID:               types.UID("dep-uid"),
			CreationTimestamp: metav1.Time{Time: start.Add(-14 * time.Minute)},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", CreationTimestamp: metav1.Time{Time: start.Add(-2 * time.Hour)}},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourceCPU:    resource.MustParse("8"),
			},
			Conditions: []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.Time{Time: start.Add(-2 * time.Hour)},
			}},
		},
	}
	ev := &corev1.Event{
		ObjectMeta:      metav1.ObjectMeta{Name: "e1", Namespace: ns},
		InvolvedObject:  corev1.ObjectReference{Kind: "Pod", Name: podName, Namespace: ns},
		Type:            "Warning",
		Reason:          "OOMKilling",
		Message:         "Killed container checkout",
		Count:           3,
		LastTimestamp:   metav1.Time{Time: start.Add(6 * time.Minute)},
	}
	return fake.NewSimpleClientset(pod, rs, dep, node, ev), podName
}

// TestInvestigatePodTarget is the pod-target investigation test: a real
// engine run against a fake cluster, targeting the pod directly (not the
// deployment). It asserts the full result contract: status card, findings,
// evidence, timeline, graph edges, and ranked changes.
func TestInvestigatePodTarget(t *testing.T) {
	client, podName := oomFixture(t)
	eng := testEngine(client)

	req := &api.InvestigationRequest{
		Target: model.ResourceRef{Kind: "pod", Namespace: "prod", Name: podName},
		Window: api.Window{
			Start: time.Date(2026, 8, 7, 13, 30, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
		},
		Scope: api.ScopeOptions{Logs: false},
	}
	res, err := eng.Investigate(context.Background(), req)
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}

	// Status card.
	if res.Incident == nil || res.Incident.Status != "OOMKILLED" {
		t.Fatalf("status = %+v, want OOMKILLED", res.Incident)
	}
	if res.Incident.Target.Name != podName {
		t.Errorf("target = %s, want the pod", res.Incident.Target.Name)
	}

	// Findings: OOM analyzer must fire for the pod target.
	oomFindings := 0
	for _, f := range res.Findings {
		if f.Analyzer == "oom" {
			oomFindings++
		}
	}
	if oomFindings != 1 {
		t.Errorf("oom findings = %d, want 1", oomFindings)
	}

	// Top hypothesis: memory, scored, evidence-anchored.
	top := topHypothesisForTest(res)
	if top == nil {
		t.Fatal("no scored hypothesis")
	}
	if top.Category != model.CatMemory {
		t.Errorf("top category = %s, want memory", top.Category)
	}
	if top.Score.Score < 0.85 {
		t.Errorf("score = %.3f, want ≥ 0.85", top.Score.Score)
	}
	if len(top.Score.Lines) < 3 {
		t.Errorf("evidence lines = %d, want ≥ 3 (mechanism + limit + temporal)", len(top.Score.Lines))
	}

	// Timeline: non-empty, sorted, and anchored - one event at offset 0 is
	// the incident onset (earliest critical observation); earlier facts (node
	// conditions from before the window's incidents) carry negative offsets.
	if len(res.Timeline) == 0 {
		t.Fatal("timeline empty")
	}
	foundAnchor := false
	prev := time.Time{}
	for _, ev := range res.Timeline {
		if ev.Observation.Timestamp.Before(prev) {
			t.Fatalf("timeline not sorted at %s", ev.Observation.Timestamp)
		}
		prev = ev.Observation.Timestamp
		if ev.Offset == 0 {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Error("timeline missing the anchored onset event (offset 0)")
	}

	// Evidence graph: pod→node RUNS_ON, RS→pod OWNS, deployment→RS OWNS.
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: podName}
	node := model.ResourceRef{Kind: "node", Name: "node-a"}
	if !hasEdge(res.Graph, pod, node, model.EdgeRunsOn) {
		t.Errorf("graph missing pod --RUNS_ON--> node: %v", res.Graph.Edges)
	}
	if !hasEdge(res.Graph, model.ResourceRef{Kind: "replicaset", Namespace: "prod", Name: "checkout-7f84c9"}, pod, model.EdgeOwns) {
		t.Errorf("graph missing RS --OWNS--> pod: %v", res.Graph.Edges)
	}
	if !hasEdge(res.Graph, model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}, model.ResourceRef{Kind: "replicaset", Namespace: "prod", Name: "checkout-7f84c9"}, model.EdgeOwns) {
		t.Errorf("graph missing deployment --OWNS--> RS: %v", res.Graph.Edges)
	}

	// What changed: the owning deployment (created 14m before the incident)
	// must be surfaced as a change with explainable factors. The pod's own
	// creation is temporally closest for a pod target, so the top entry may
	// be either - the deployment's presence is the meaningful assertion.
	if len(res.Changes) == 0 {
		t.Fatal("no changes detected")
	}
	for _, c := range res.Changes {
		if c.Relevance < 0 || c.Relevance > 1 {
			t.Errorf("change relevance out of range: %+v", c)
		}
		if len(c.Factors) != 4 {
			t.Errorf("change factors = %v, want temporal/graph/ownership/anomaly", c.Factors)
		}
	}
	foundDeploymentChange := false
	for _, c := range res.Changes {
		if c.Resource.Kind == "deployment" && c.Resource.Name == "checkout" {
			foundDeploymentChange = true
			if c.Description == "" {
				t.Error("deployment change missing description")
			}
		}
	}
	if !foundDeploymentChange {
		t.Errorf("deployment change not surfaced: %+v", res.Changes)
	}
	if res.Changes[0].Relevance < res.Changes[len(res.Changes)-1].Relevance {
		t.Error("changes not sorted by relevance desc")
	}

	// Auditability: every observation carries a source ref.
	for _, o := range res.Observations {
		if o.Source.System == "" || o.Source.Query == "" {
			t.Errorf("observation %s missing source ref", o.ID)
		}
	}
	if res.Meta.Duration <= 0 {
		t.Error("meta duration missing")
	}
}

// TestInvestigatePodTargetHealthyStaysSilent is the pod-target false-positive
// gate: a healthy pod must produce no findings and no hypotheses.
func TestInvestigatePodTargetHealthyStaysSilent(t *testing.T) {
	client, podName := oomFixture(t)
	// Heal the pod: running container, zero restarts, no OOMKilled.
	pod, err := client.CoreV1().Pods("prod").Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "checkout",
		RestartCount: 0,
		State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Time{Time: time.Now()}}},
	}}
	if _, err := client.CoreV1().Pods("prod").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update pod: %v", err)
	}
	// Remove the OOM history (the fixture's OOMKilling event).
	if err := client.CoreV1().Events("prod").Delete(context.Background(), "e1", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete event: %v", err)
	}

	res, err := testEngine(client).Investigate(context.Background(), &api.InvestigationRequest{
		Target: model.ResourceRef{Kind: "pod", Namespace: "prod", Name: podName},
		Window: api.Window{
			Start: time.Date(2026, 8, 7, 13, 30, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("healthy pod produced findings: %v", res.Findings)
	}
	if len(res.Hypotheses) != 0 {
		t.Errorf("healthy pod produced hypotheses: %v", res.Hypotheses)
	}
	if res.Incident.Status != "INVESTIGATED" {
		t.Errorf("status = %s, want INVESTIGATED", res.Incident.Status)
	}
}

// adaptiveCollector wraps the kubernetes collector, counts Collect calls, and
// simulates log snippets arriving on the adaptive round (the fake clientset
// cannot serve GetLogs content).
type adaptiveCollector struct {
	inner *k8scollect.Collector
	calls int
}

func (c *adaptiveCollector) ID() string { return c.inner.ID() }

func (c *adaptiveCollector) Collect(ctx context.Context, scope *collect.ScopePlan) ([]model.Observation, []model.SourceRef, error) {
	c.calls++
	obs, refs, err := c.inner.Collect(ctx, scope)
	if c.calls >= 2 && scope.WantsHint("logs") {
		obs = append(obs, collect.NewObservation(
			"log.snippet",
			model.SourceRef{System: "k8s", Query: "GET logs (simulated)"},
			time.Now(),
			model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"},
			map[string]any{"container": "api", "lines": []string{"panic: boom"}, "line_count": 3, "truncated": false},
			1.0,
		))
	}
	return obs, refs, err
}

// TestAdaptiveCollectionLoop: a crash loop with unknown exit cause requests
// logs (NeedsEvidence); the engine runs a second, targeted collection round;
// the arriving logs become evidence and satisfy the request.
func TestAdaptiveCollectionLoop(t *testing.T) {
	ns := "prod"
	start := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "api-abc",
			Namespace:         ns,
			UID:               types.UID("pod-uid"),
			CreationTimestamp: metav1.Time{Time: start},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{{
				Name:  "api",
				Image: "registry.example/api:v1",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &metav1.Time{Time: start},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "api",
				RestartCount: 3,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", CreationTimestamp: metav1.Time{Time: start.Add(-2 * time.Hour)}},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Gi")},
			Conditions: []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.Time{Time: start.Add(-2 * time.Hour)},
			}},
		},
	}
	client := fake.NewSimpleClientset(pod, node)
	wrapped := &adaptiveCollector{inner: k8scollect.New(client)}

	reg := collect.NewRegistry()
	reg.Register(wrapped)
	ar := analyze.NewRegistry()
	ar.Register(crashloop.New())
	eng := New(reg, ar)

	res, err := eng.Investigate(context.Background(), &api.InvestigationRequest{
		Target: model.ResourceRef{Kind: "pod", Namespace: ns, Name: "api-abc"},
		Window: api.Window{Start: start.Add(-30 * time.Minute), End: start.Add(30 * time.Minute)},
		Scope:  api.ScopeOptions{Logs: false}, // logs must arrive via the adaptive ask
	})
	if err != nil {
		t.Fatalf("Investigate: %v", err)
	}

	if wrapped.calls < 2 {
		t.Fatalf("collector calls = %d, want ≥ 2 (initial + adaptive round)", wrapped.calls)
	}
	// The requested evidence arrived.
	foundLog := false
	for _, o := range res.Observations {
		if o.Kind == "log.snippet" {
			foundLog = true
		}
	}
	if !foundLog {
		t.Fatal("adaptive round did not deliver the requested log evidence")
	}
	// The crashloop hypothesis consumed it: logs evidence present, the
	// exit-logs missing entry satisfied.
	if len(res.Hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(res.Hypotheses))
	}
	h := res.Hypotheses[0]
	hasLogsEvidence := false
	for _, e := range h.Evidence {
		if e == "crashloop.api-abc.logs" {
			hasLogsEvidence = true
		}
	}
	if !hasLogsEvidence {
		t.Errorf("crashloop hypothesis missing logs evidence: %v", h.Evidence)
	}
	for _, m := range h.Missing {
		if m == "crashloop.api-abc.exit-logs" {
			t.Error("exit-logs missing entry must be satisfied once logs arrived")
		}
	}
	// And the score reflects the extra evidence (waiting 30 + restarts 10 + logs 10).
	if h.Score == nil || h.Score.Score < 0.85 {
		t.Errorf("score = %v, want ≥ 0.85 with logs evidence", h.Score)
	}
}

func topHypothesisForTest(res *api.InvestigationResult) *model.Hypothesis {
	var top *model.Hypothesis
	for i := range res.Hypotheses {
		h := &res.Hypotheses[i]
		if h.Score == nil {
			continue
		}
		if top == nil || h.Score.Score > top.Score.Score {
			top = h
		}
	}
	return top
}

func hasEdge(g *model.Graph, from, to model.ResourceRef, kind model.EdgeKind) bool {
	if g == nil {
		return false
	}
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }
