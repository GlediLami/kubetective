package action

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/GlediLami/kubetective/internal/model"
)

const revisionAnnotation = "deployment.kubernetes.io/revision"

// Applier executes approved actions against a cluster. Every Apply result is
// recorded by the caller as an audit record.
type Applier struct {
	kc kubernetes.Interface
}

func NewApplier(kc kubernetes.Interface) *Applier { return &Applier{kc: kc} }

// Apply executes one action and returns a short human-readable result.
// Each branch is verified after the mutation.
func (a *Applier) Apply(ctx context.Context, act Action) (string, error) {
	switch act.Type {
	case Rollback:
		return a.rollback(ctx, act.Target)
	case RestartPod:
		return a.restartPod(ctx, act.Target)
	default:
		return "", fmt.Errorf("unsupported action type %q", act.Type)
	}
}

// rollback reverts the deployment's spec.template to the previous revision's
// template (the same effect as `kubectl rollout undo`): find the owned
// ReplicaSets, take the one with the highest revision below the current one,
// update the deployment with the previous template and drop the revision
// annotation.
func (a *Applier) rollback(ctx context.Context, ref model.ResourceRef) (string, error) {
	dep, err := a.kc.AppsV1().Deployments(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get deployment: %w", err)
	}
	rsList, err := a.kc.AppsV1().ReplicaSets(ref.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list replicasets: %w", err)
	}
	type owned struct {
		rev    int
		tmpl   corev1.PodTemplateSpec
		rsName string
	}
	var rss []owned
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		if !metav1.IsControlledBy(rs, dep) {
			continue
		}
		rev := 0
		if r := rs.Annotations[revisionAnnotation]; r != "" {
			rev, _ = strconv.Atoi(r)
		}
		rss = append(rss, owned{rev, *rs.Spec.Template.DeepCopy(), rs.Name})
	}
	if len(rss) < 2 {
		return "", fmt.Errorf("deployment %s has no previous revision to roll back to (%d revision(s) found)", ref.Name, len(rss))
	}
	sort.Slice(rss, func(i, j int) bool { return rss[i].rev > rss[j].rev })
	cur := rss[0].rev
	prev := -1
	var prevTmpl corev1.PodTemplateSpec
	var prevRS string
	for _, o := range rss {
		if o.rev < cur && o.rev > prev {
			prev, prevTmpl, prevRS = o.rev, o.tmpl, o.rsName
		}
	}
	if prev < 0 {
		return "", fmt.Errorf("deployment %s has no revision below current (%d)", ref.Name, cur)
	}

	// Strategic merge on DeploymentSpec.Template would recursively MERGE the
	// template (annotations maps merge; only lists replace) - kubectl rollout
	// undo therefore does a full Update with the previous template, and so do
	// we: replace the template wholesale, drop the revision annotation.
	dep.Spec.Template = prevTmpl
	delete(dep.Annotations, revisionAnnotation)
	if _, err := a.kc.AppsV1().Deployments(ref.Namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("update deployment: %w", err)
	}
	return fmt.Sprintf("rolled back deployment/%s to revision %d (template from replicaset %s)", ref.Name, prev, prevRS), nil
}

// restartPod deletes the pod; its controller recreates it.
func (a *Applier) restartPod(ctx context.Context, ref model.ResourceRef) (string, error) {
	if err := a.kc.CoreV1().Pods(ref.Namespace).Delete(ctx, ref.Name, metav1.DeleteOptions{}); err != nil {
		return "", fmt.Errorf("delete pod: %w", err)
	}
	return fmt.Sprintf("deleted pod/%s - controller will recreate it", ref.Name), nil
}
