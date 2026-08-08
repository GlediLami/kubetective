package kubernetes

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
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
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: ns},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: name, Namespace: ns},
		Type:           "Warning",
		Reason:         "OOMKilling",
		Message:        "Killed container checkout",
		Count:          3,
		LastTimestamp:  metav1.Time{Time: time.Date(2026, 8, 7, 14, 6, 3, 0, time.UTC)},
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
			Name:              "checkout",
			Namespace:         ns,
			UID:               types.UID("dep-uid-1"),
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

func TestCollectPvcServiceHPAObservations(t *testing.T) {
	ns := "prod"
	pod := fakePod(t, ns, "checkout-7f84c9")
	// The pod needs the app label for service matching, a PVC volume, and a
	// Deployment owner chain for the HPA lookup.
	pod.Labels = map[string]string{"app": "checkout"}
	pod.Spec.Volumes = []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "checkout-data"}},
	}}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout", Namespace: ns, UID: types.UID("dep-uid-1"),
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)},
		},
	}
	// RS owned by the deployment; pod owned by the RS.
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "checkout-7f84c9", UID: types.UID("rs-uid-1"), Controller: boolPtr(true)}}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-7f84c9", Namespace: ns, UID: types.UID("rs-uid-1"),
			OwnerReferences:   []metav1.OwnerReference{{Kind: "Deployment", Name: "checkout", UID: types.UID("dep-uid-1"), Controller: boolPtr(true)}},
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-data", Namespace: ns, CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)}},
		Spec:       corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-svc", Namespace: ns, CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)}},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "checkout"}, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	eps := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-svc", Namespace: ns},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}},
			Ports:     []corev1.EndpointPort{{Port: 80}},
		}},
	}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-hpa", Namespace: ns, CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)}},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "checkout"},
			MinReplicas:    int32Ptr(1),
			MaxReplicas:    5,
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 5, DesiredReplicas: 5},
	}

	client := fake.NewSimpleClientset(pod, rs, dep, pvc, svc, eps, hpa, fakeNode())
	c := New(client)
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: ns, Name: "checkout-7f84c9"}},
		Window: api.Window{
			Start: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	byKind := map[string]int{}
	for _, o := range obs {
		byKind[o.Kind]++
	}
	if byKind["pvc.state"] != 1 {
		t.Errorf("pvc.state = %d, want 1", byKind["pvc.state"])
	}
	if byKind["service.state"] != 1 {
		t.Errorf("service.state = %d, want 1", byKind["service.state"])
	}
	if byKind["hpa.state"] != 1 {
		t.Errorf("hpa.state = %d, want 1", byKind["hpa.state"])
	}
	// Endpoints: 1 ready of 1 total; HPA at max (5/5); PVC pending.
	for _, o := range obs {
		switch o.Kind {
		case "service.state":
			if o.Payload["ready_endpoints"] != 1 {
				t.Errorf("ready_endpoints = %v, want 1", o.Payload["ready_endpoints"])
			}
		case "hpa.state":
			if o.Payload["at_max"] != true {
				t.Errorf("hpa at_max = %v, want true (5/5)", o.Payload["at_max"])
			}
		case "pvc.state":
			if o.Payload["phase"] != "Pending" {
				t.Errorf("pvc phase = %v, want Pending", o.Payload["phase"])
			}
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func int32Ptr(v int32) *int32 { return &v }

func TestCollectCoreDNSAvailability(t *testing.T) {
	ns := "prod"
	pod := fakePod(t, ns, "checkout-7f84c9")
	coredns := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system",
			CreationTimestamp: metav1.Time{Time: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)}},
		Status: appsv1.DeploymentStatus{Replicas: 2, AvailableReplicas: 0},
	}
	client := fake.NewSimpleClientset(pod, coredns)
	c := New(client)
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: ns, Name: "checkout-7f84c9"}},
		Window: api.Window{
			Start: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var corednsObs []model.Observation
	for _, o := range obs {
		if o.Kind == "deployment.state" && o.Resource.Namespace == "kube-system" {
			corednsObs = append(corednsObs, o)
		}
	}
	if len(corednsObs) != 1 {
		t.Fatalf("kube-system deployment.state = %d, want 1 (coredns)", len(corednsObs))
	}
	avail, ok := analyze.PayloadInt64(corednsObs[0].Payload, "available_replicas")
	if !ok || avail != 0 {
		t.Errorf("coredns available_replicas = %v, want 0", corednsObs[0].Payload["available_replicas"])
	}
}

func TestCollectSkipsMissingCoreDNS(t *testing.T) {
	ns := "prod"
	pod := fakePod(t, ns, "checkout-7f84c9")
	// No kube-system deployments in the fake — the collector must not fail.
	client := fake.NewSimpleClientset(pod)
	c := New(client)
	_, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: ns, Name: "checkout-7f84c9"}},
		Window: api.Window{
			Start: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("Collect without coredns must not fail: %v", err)
	}
}
