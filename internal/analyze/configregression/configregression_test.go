package configregression

import (
	"context"
	"testing"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
)

func mk(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestConfigRegressionAnalyzerWithCommit(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pod.state", pod, base.Add(-15*time.Minute), map[string]any{"phase": "Running"}),
		mk("container.terminated", pod, base, map[string]any{"reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(2)}),
		// The regression: a commit 6 minutes before the first OOM.
		mk("git.commit", pod, base.Add(-6*time.Minute), map[string]any{
			"sha": "abc12345", "author": "Engineer", "message": "bump CACHE_SIZE to 50000\n", "files": []string{"api.yaml"},
		}),
	}}

	a := New()
	findings, hypotheses, _, err := a.Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	if h.Category != model.CatConfig {
		t.Errorf("category = %s, want config-regression", h.Category)
	}
	// commit 30 + recency 25 = 55 → 0.88.
	if h.Score == nil || h.Score.Score < 0.8 {
		t.Errorf("score = %v, want ≥ 0.8", h.Score)
	}
	hasRecency := false
	for _, line := range h.Score.Lines {
		if line.EvidenceID == "configregression.api-abc.recency" {
			hasRecency = true
		}
	}
	if !hasRecency {
		t.Error("missing recency evidence (commit 6 min before onset)")
	}
	if findings[0].Description != "Configuration regression: commit abc12345 (bump CACHE_SIZE to 50000) preceded the failure" {
		t.Errorf("finding = %q", findings[0].Description)
	}
}

func TestConfigRegressionSilentWithoutSymptom(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pod.state", pod, base, map[string]any{"phase": "Running"}),
		mk("git.commit", pod, base.Add(-6*time.Minute), map[string]any{"sha": "abc12345", "message": "bump cache"}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("regression claim without a symptom must stay silent: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}

func TestConfigRegressionWithoutGitIsWeaker(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pod.state", pod, base, map[string]any{"phase": "Running"}),
		mk("container.waiting", pod, base.Add(5*time.Minute), map[string]any{"reason": "CrashLoopBackOff"}),
		mk("deployment.state", model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "api"}, base.Add(-10*time.Minute), map[string]any{}),
	}}
	_, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	if len(h.Missing) != 1 {
		t.Errorf("missing = %v, want the git-commit gap recorded", h.Missing)
	}
	// change 10 + symptom 30 = 40, minus the git gap penalty (6) → honest but
	// clearly weaker than the git-backed case (85).
	if h.Score.Score >= 0.85 {
		t.Errorf("score = %.3f, want < 0.85 without git evidence", h.Score.Score)
	}
	if h.Score.Score < 0.6 {
		t.Errorf("score = %.3f, want ≥ 0.6 (symptom mechanism is still real evidence)", h.Score.Score)
	}
}

func TestConfigRegressionGitOpsTrouble(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}
	app := model.ResourceRef{Kind: "application", Namespace: "prod", Name: "checkout-app"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pod.state", pod, base, map[string]any{"phase": "Running"}),
		mk("container.waiting", pod, base.Add(3*time.Minute), map[string]any{"reason": "CrashLoopBackOff"}),
		mk("gitops.state", app, base.Add(-2*time.Minute), map[string]any{
			"controller": "argocd", "kind": "Application", "health": "Degraded", "sync": "OutOfSync", "revision": "abc123",
		}),
	}}
	_, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	hasGitOps := false
	for _, line := range h.Score.Lines {
		if line.EvidenceID == "configregression.checkout-app.gitops" {
			hasGitOps = true
		}
	}
	if !hasGitOps {
		t.Error("missing gitops evidence for a Degraded application")
	}
}
