package gitops

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func kustomization(name, ns, revision, condStatus string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata": map[string]any{
			"name":              name,
			"namespace":         ns,
			"creationTimestamp": "2026-08-07T13:00:00Z",
		},
		"spec": map[string]any{"sourceRef": map[string]any{"name": "apps-repo", "kind": "GitRepository"}},
		"status": map[string]any{
			"lastAppliedRevision": revision,
			"conditions": []any{
				map[string]any{"type": "Ready", "status": condStatus, "reason": "ReconciliationSucceeded", "message": "Applied revision: main@sha1:abc123"},
			},
		},
	}}
	return u
}

func argocdApp(name, health, sync, revision string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":              name,
			"creationTimestamp": "2026-08-07T13:00:00Z",
		},
		"spec": map[string]any{"destination": map[string]any{"namespace": "prod"}},
		"status": map[string]any{
			"health": map[string]any{"status": health},
			"sync":   map[string]any{"status": sync, "revision": revision},
		},
	}}
	return u
}

func TestGitOpsCollectorFluxAndArgoCD(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			fluxKustomizationGVR: "KustomizationList",
			fluxHelmReleaseGVR:   "HelmReleaseList",
			argocdAppGVR:         "ApplicationList",
		},
		kustomization("checkout", "prod", "main@sha1:abc123", "True"),
		kustomization("unrelated", "other", "main@sha1:def456", "True"),
		argocdApp("checkout-app", "Degraded", "OutOfSync", "abc123"),
	)

	c := New(dyn)
	obs, refs, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "deployment", Namespace: "prod", Name: "checkout"}},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("missing source refs")
	}

	// checkout kustomization + checkout-app application; unrelated excluded.
	if len(obs) != 2 {
		t.Fatalf("observations = %d, want 2 (kustomization checkout + application checkout-app): %+v", len(obs), obs)
	}
	var fluxObs, argoObs model.Observation
	for _, o := range obs {
		switch o.Payload["controller"] {
		case "flux":
			fluxObs = o
		case "argocd":
			argoObs = o
		}
	}
	if fluxObs.Payload["kind"] != "Kustomization" || fluxObs.Payload["revision"] != "main@sha1:abc123" {
		t.Errorf("flux observation = %+v", fluxObs.Payload)
	}
	if cond, _ := fluxObs.Payload["condition"].(string); cond != "Ready:True (Applied revision: main@sha1:abc123)" {
		t.Errorf("flux condition = %q, want Ready:True with message", cond)
	}
	if argoObs.Payload["health"] != "Degraded" || argoObs.Payload["sync"] != "OutOfSync" {
		t.Errorf("argocd observation = %+v", argoObs.Payload)
	}
}

func TestGitOpsCollectorEmptyCluster(t *testing.T) {
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			fluxKustomizationGVR: "KustomizationList",
			fluxHelmReleaseGVR:   "HelmReleaseList",
			argocdAppGVR:         "ApplicationList",
		})
	c := New(dyn)
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "deployment", Namespace: "prod", Name: "checkout"}},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("observations = %d, want 0 (no GitOps objects)", len(obs))
	}
	// Unconfigured (nil dynamic client) → silent.
	if obs, _, err := New(nil).Collect(context.Background(), &collect.ScopePlan{}); err != nil || len(obs) != 0 {
		t.Fatalf("nil client: %v %d", err, len(obs))
	}
}
