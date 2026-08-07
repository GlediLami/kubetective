package kubernetes

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

func fakePod(t *testing.T, ns, name string) *corev1.Pod {
	t.Helper()
	start := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       types.UID("pod-uid-1"),
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "checkout-7f84c9", UID: types.UID("rs-uid-1"), Controller: boolPtr(true)},
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
}

func fakeNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)}},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourceCPU:    resource.MustParse("8"),
			},
			Conditions: []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)},
			}},
		},
	}
}

func TestCollectPodNormalizesObservations(t *testing.T) {
	ns, name := "prod", "checkout-7f84c9"
	pod := fakePod(t, ns, name)
	client := fake.NewSimpleClientset(pod, fakeNode(), &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "e1", Namespace: ns},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: name, Namespace: ns},
		Type:        "Warning",
		Reason:      "OOMKilling",
		Message:     "Killed container checkout",
		Count:       3,
		LastTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 14, 6, 3, 0, time.UTC)},
	})

	c := New(client)
	// Explicit window covering the fake data (14:00–14:10 UTC) — not the real clock.
	obs, refs, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: ns, Name: name}},
		Window: api.Window{
			Start: time.Date(2026, 8, 7, 13, 30, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected source refs (auditability)")
	}

	byKind := map[string]int{}
	var terminated *model.Observation
	for i := range obs {
		o := &obs[i]
		byKind[o.Kind]++
		if o.Kind == "container.terminated" {
			terminated = o
		}
	}
	if byKind["pod.state"] != 1 {
		t.Errorf("pod.state count = %d, want 1", byKind["pod.state"])
	}
	if byKind["container.spec"] != 1 {
		t.Errorf("container.spec count = %d, want 1", byKind["container.spec"])
	}
	if byKind["container.terminated"] != 1 {
		t.Errorf("container.terminated count = %d, want 1", byKind["container.terminated"])
	}
	if byKind["node.condition"] != 1 {
		t.Errorf("node.condition count = %d, want 1", byKind["node.condition"])
	}
	if byKind["event.recorded"] != 1 {
		t.Errorf("event.recorded count = %d, want 1 (window-filtered)", byKind["event.recorded"])
	}
	if terminated == nil {
		t.Fatal("missing container.terminated observation")
	}
	if got := terminated.Payload["reason"]; got != "OOMKilled" {
		t.Errorf("terminated reason = %v, want OOMKilled", got)
	}
	if got := terminated.Payload["exit_code"]; got != int32(137) {
		t.Errorf("exit_code = %v, want 137", got)
	}
	// Owner chain must be visible to analyzers (pod → RS → deployment context).
	if got := obs[0].Payload["owner_kind"]; got != "ReplicaSet" {
		t.Errorf("owner_kind = %v, want ReplicaSet", got)
	}
	// Stable content-hashed IDs.
	if terminated.ID == "" {
		t.Errorf("observation ID must be non-empty")
	}
}

func TestCollectDeploymentExpandsOwnerChain(t *testing.T) {
	ns := "prod"
	pod := fakePod(t, ns, "checkout-7f84c9-abcde")
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout-7f84c9",
			Namespace: ns,
			UID:       types.UID("rs-uid-1"),
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "checkout", UID: types.UID("dep-uid-1"), Controller: boolPtr(true)},
			},
		},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout",
			Namespace: ns,
			UID:       types.UID("dep-uid-1"),
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 14, 2, 1, 0, time.UTC)},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1},
	}
	client := fake.NewSimpleClientset(pod, rs, dep, fakeNode())

	c := New(client)
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "deployment", Namespace: ns, Name: "checkout"}},
		Window:  api.Since(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	byKind := map[string]int{}
	for _, o := range obs {
		byKind[o.Kind]++
	}
	if byKind["deployment.state"] != 1 {
		t.Errorf("deployment.state count = %d, want 1", byKind["deployment.state"])
	}
	if byKind["container.terminated"] != 1 {
		t.Errorf("owner chain not expanded: container.terminated count = %d, want 1", byKind["container.terminated"])
	}
	if byKind["pod.state"] != 1 {
		t.Errorf("pod.state count = %d, want 1", byKind["pod.state"])
	}
}

func boolPtr(b bool) *bool { return &b }
