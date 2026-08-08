package action

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

func scored(cat model.HypothesisCategory) model.Hypothesis {
	return model.Hypothesis{
		ID: "h", Claim: "claim for " + string(cat), Category: cat, Status: model.StatusLikely,
		Score:    &model.ScoreBreakdown{Score: 0.9},
		Evidence: []string{"e1"},
	}
}

func TestPlanRollbackOnRegression(t *testing.T) {
	res := &api.InvestigationResult{
		Incident:   &model.IncidentSummary{Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}, Status: "OOMKILLED"},
		Hypotheses: []model.Hypothesis{scored(model.CatMemory)},
		Changes:    []model.Change{{Resource: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}, Relevance: 0.9}},
	}
	acts := Plan(res)
	if len(acts) != 1 || acts[0].Type != Rollback {
		t.Fatalf("actions = %+v, want 1 rollback", acts)
	}
	if acts[0].Target.Name != "checkout" {
		t.Errorf("target = %v", acts[0].Target)
	}
	if !strings.HasPrefix(acts[0].ID, "act-") || len(acts[0].ID) != 8+4 {
		t.Errorf("id = %q", acts[0].ID)
	}
	if !strings.Contains(acts[0].DryRun, "rollout undo") {
		t.Errorf("dry_run = %q", acts[0].DryRun)
	}
}

func TestPlanNoChangeNoAction(t *testing.T) {
	res := &api.InvestigationResult{
		Incident:   &model.IncidentSummary{Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}, Status: "OOMKILLED"},
		Hypotheses: []model.Hypothesis{scored(model.CatMemory)},
		// no changes → no rollback
	}
	if acts := Plan(res); len(acts) != 0 {
		t.Fatalf("actions = %+v, want none", acts)
	}
}

func TestPlanRestartPod(t *testing.T) {
	res := &api.InvestigationResult{
		Incident:   &model.IncidentSummary{Target: model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}, Status: "CRASHLOOPBACKOFF"},
		Hypotheses: []model.Hypothesis{scored(model.CatCrashLoop)},
	}
	acts := Plan(res)
	if len(acts) != 1 || acts[0].Type != RestartPod {
		t.Fatalf("actions = %+v, want 1 restart-pod", acts)
	}
	if !strings.Contains(acts[0].DryRun, "delete pod") {
		t.Errorf("dry_run = %q", acts[0].DryRun)
	}
}

func TestPlanRuledOutIsNotActionable(t *testing.T) {
	h := scored(model.CatMemory)
	h.Status = model.StatusRuledOut
	res := &api.InvestigationResult{
		Incident:   &model.IncidentSummary{Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}, Status: "OOMKILLED"},
		Hypotheses: []model.Hypothesis{h},
		Changes:    []model.Change{{Resource: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}, Relevance: 0.9}},
	}
	if acts := Plan(res); len(acts) != 0 {
		t.Fatalf("actions = %+v, want none for ruled-out hypothesis", acts)
	}
}

// --- appliers ---

func rs(ns, name, owner string, rev int, image string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Annotations:     map[string]string{revisionAnnotation: strconv.Itoa(rev)},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: owner, APIVersion: "apps/v1", Controller: boolPtr(true)}},
		},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: image}}}}},
	}
}

func TestApplyRollback(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"marker": "x"}}, // must be REPLACED by rollback
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "app:v42"}}},
			}},
	}
	kc := fake.NewSimpleClientset(dep,
		rs("prod", "checkout-7f84c9", "checkout", 2, "app:v42"),
		rs("prod", "checkout-6b4bf88", "checkout", 1, "app:v41"),
	)
	a := NewApplier(kc)
	out, err := a.Apply(context.Background(), Action{Type: Rollback, Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}})
	if err != nil {
		t.Fatalf("Apply rollback: %v", err)
	}
	if !strings.Contains(out, "revision 1") {
		t.Errorf("result = %q, want rollback to revision 1", out)
	}
	got, _ := kc.AppsV1().Deployments("prod").Get(context.Background(), "checkout", metav1.GetOptions{})
	if got.Spec.Template.Spec.Containers[0].Image != "app:v41" {
		t.Errorf("template image = %q, want app:v41 (previous revision)", got.Spec.Template.Spec.Containers[0].Image)
	}
	// Regression: the rollback must REPLACE the template wholesale, not merge
	// it - a stale annotation from the current revision must not survive.
	if _, has := got.Spec.Template.Annotations["marker"]; has {
		t.Errorf("template annotations = %v: stale 'marker' survived the rollback", got.Spec.Template.Annotations)
	}
}

func TestApplyRollbackNoPreviousRevision(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{}},
	}
	kc := fake.NewSimpleClientset(dep, rs("prod", "checkout-7f84c9", "checkout", 1, "app:v42"))
	a := NewApplier(kc)
	_, err := a.Apply(context.Background(), Action{Type: Rollback, Target: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}})
	if err == nil || !strings.Contains(err.Error(), "no previous revision") {
		t.Fatalf("error = %v, want no-previous-revision error", err)
	}
}

func TestApplyRestartPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "checkout-abc", Namespace: "prod"}}
	kc := fake.NewSimpleClientset(pod)
	a := NewApplier(kc)
	out, err := a.Apply(context.Background(), Action{Type: RestartPod, Target: model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}})
	if err != nil {
		t.Fatalf("Apply restart: %v", err)
	}
	if !strings.Contains(out, "deleted pod/checkout-abc") {
		t.Errorf("result = %q", out)
	}
	if _, err := kc.CoreV1().Pods("prod").Get(context.Background(), "checkout-abc", metav1.GetOptions{}); err == nil {
		t.Error("pod still exists after delete")
	}
}

func TestFileAuditSinkAppends(t *testing.T) {
	dir := t.TempDir()
	sink := FileAuditSink{Dir: dir}
	rec := AuditRecord{Kind: "action.audit", User: "alice", IncidentID: "inc-1", Action: "rollback", Resource: "deployment/prod/checkout", Approval: "explicit", Result: "ok"}
	if err := sink.AppendAudit("inc-1", rec); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "inc-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["user"] != "alice" || got["approval"] != "explicit" {
		t.Errorf("audit line = %s", raw)
	}
}

// small helpers
func boolPtr(b bool) *bool { return &b }
