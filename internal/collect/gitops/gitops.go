// Package gitops implements the GitOps collector: it reads Flux
// Kustomization/HelmRelease and ArgoCD Application custom resources via the
// dynamic client and normalizes their sync/reconcile state into gitops.state
// observations — "what the GitOps controller thinks of the workload"
// (docs/DESIGN.md §12).
//
// The collector is failure-tolerant: missing CRDs or missing permissions
// become gaps, never failed investigations.
package gitops

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
)

var (
	fluxKustomizationGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	fluxHelmReleaseGVR   = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	argocdAppGVR         = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
)

// Collector reads GitOps custom resources from the cluster.
type Collector struct {
	dyn dynamic.Interface
}

var _ collect.Collector = (*Collector)(nil)

func New(dyn dynamic.Interface) *Collector { return &Collector{dyn: dyn} }

func (c *Collector) ID() string { return "gitops" }

func (c *Collector) Collect(ctx context.Context, scope *collect.ScopePlan) ([]model.Observation, []model.SourceRef, error) {
	if c.dyn == nil {
		return nil, nil, nil
	}
	var obs []model.Observation
	var refs []model.SourceRef

	// Flux Kustomizations (and HelmReleases) in the target's namespace.
	if list, err := c.dyn.Resource(fluxKustomizationGVR).Namespace(scope.Targets[0].Namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			if o, ok := kustomizationObservation(&list.Items[i], scope); ok {
				obs = append(obs, o)
				refs = append(refs, model.SourceRef{System: "gitops", Query: "flux kustomization status"})
			}
		}
	} else if !missingCRD(err) {
		return nil, refs, err
	}
	if list, err := c.dyn.Resource(fluxHelmReleaseGVR).Namespace(scope.Targets[0].Namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			if o, ok := helmReleaseObservation(&list.Items[i], scope); ok {
				obs = append(obs, o)
				refs = append(refs, model.SourceRef{System: "gitops", Query: "flux helmrelease status"})
			}
		}
	} else if !missingCRD(err) {
		return obs, refs, err
	}

	// ArgoCD Applications are cluster-scoped.
	if list, err := c.dyn.Resource(argocdAppGVR).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			if o, ok := argocdObservation(&list.Items[i], scope); ok {
				obs = append(obs, o)
				refs = append(refs, model.SourceRef{System: "gitops", Query: "argocd application status"})
			}
		}
	} else if !missingCRD(err) {
		return obs, refs, err
	}
	return obs, refs, nil
}

// missingCRD reports whether a list error means "the CRD is not installed" —
// a normal, silent condition for clusters without Flux/ArgoCD.
func missingCRD(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "the server could not find the requested resource") ||
		strings.Contains(msg, "no matches for kind")
}

func kustomizationObservation(u *unstructured.Unstructured, scope *collect.ScopePlan) (model.Observation, bool) {
	name := u.GetName()
	if !scopeRelatesTo(scope, name) {
		return model.Observation{}, false
	}
	status := nestedString(u, "status", "lastAppliedRevision")
	cond := conditionSummary(u)
	return collect.NewObservation(
		"gitops.state",
		model.SourceRef{System: "gitops", Query: "flux kustomization status"},
		u.GetCreationTimestamp().Time,
		model.ResourceRef{Kind: "kustomization", Namespace: u.GetNamespace(), Name: name},
		map[string]any{
			"controller": "flux",
			"kind":       "Kustomization",
			"revision":   status,
			"condition":  cond,
			"source_ref": nestedString(u, "spec", "sourceRef", "name"),
			"workloads":  []string{name},
		},
		1.0,
	), true
}

func helmReleaseObservation(u *unstructured.Unstructured, scope *collect.ScopePlan) (model.Observation, bool) {
	name := u.GetName()
	if !scopeRelatesTo(scope, name) {
		return model.Observation{}, false
	}
	revision := nestedString(u, "status", "lastAppliedRevision")
	return collect.NewObservation(
		"gitops.state",
		model.SourceRef{System: "gitops", Query: "flux helmrelease status"},
		u.GetCreationTimestamp().Time,
		model.ResourceRef{Kind: "helmrelease", Namespace: u.GetNamespace(), Name: name},
		map[string]any{
			"controller": "flux",
			"kind":       "HelmRelease",
			"revision":   revision,
			"condition":  conditionSummary(u),
			"workloads":  []string{name},
		},
		1.0,
	), true
}

func argocdObservation(u *unstructured.Unstructured, scope *collect.ScopePlan) (model.Observation, bool) {
	name := u.GetName()
	if !scopeRelatesTo(scope, name) {
		return model.Observation{}, false
	}
	health := nestedString(u, "status", "health", "status")
	sync := nestedString(u, "status", "sync", "status")
	revision := nestedString(u, "status", "sync", "revision")
	dest := nestedString(u, "spec", "destination", "namespace")
	return collect.NewObservation(
		"gitops.state",
		model.SourceRef{System: "gitops", Query: "argocd application status"},
		u.GetCreationTimestamp().Time,
		model.ResourceRef{Kind: "application", Namespace: dest, Name: name},
		map[string]any{
			"controller": "argocd",
			"kind":       "Application",
			"health":     health,
			"sync":       sync,
			"revision":   revision,
			"workloads":  []string{name},
		},
		1.0,
	), true
}

// scopeRelatesTo is a lenient relation check: the GitOps object's name (or
// namespace) overlaps the target's name/namespace.
func scopeRelatesTo(scope *collect.ScopePlan, name string) bool {
	t := scope.Targets[0]
	if t.Name != "" && (t.Name == name || strings.Contains(name, t.Name) || strings.Contains(t.Name, name)) {
		return true
	}
	return t.Namespace != "" && strings.EqualFold(t.Namespace, scope.Targets[0].Namespace)
}

// conditionSummary condenses the Ready/Reconciling condition into one string.
func conditionSummary(u *unstructured.Unstructured) string {
	conds, ok := u.Object["status"].(map[string]any)["conditions"].([]any)
	if !ok {
		return ""
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if typ == "Ready" {
			status, _ := m["status"].(string)
			reason, _ := m["reason"].(string)
			msg, _ := m["message"].(string)
			if msg != "" {
				return fmt.Sprintf("%s:%s (%s)", typ, status, truncate(msg, 80))
			}
			return fmt.Sprintf("%s:%s (%s)", typ, status, reason)
		}
	}
	return ""
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
	v, found, err := unstructured.NestedString(u.Object, fields...)
	if err != nil || !found {
		return ""
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
