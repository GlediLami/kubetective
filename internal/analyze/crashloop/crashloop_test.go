package crashloop

import (
	"context"
	"testing"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
)

func obs(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestCrashLoopAnalyzer(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.terminated", res, base.Add(2*time.Minute), map[string]any{"reason": "Error", "exit_code": int64(1), "restarts": int64(1)}),
		obs("container.terminated", res, base.Add(4*time.Minute), map[string]any{"reason": "Error", "exit_code": int64(1), "restarts": int64(2)}),
		obs("container.waiting", res, base.Add(5*time.Minute), map[string]any{"reason": "CrashLoopBackOff", "restarts": int64(2)}),
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
	if h.Score == nil || h.Score.Score < 0.8 {
		t.Errorf("score = %v, want ≥ 0.8", h.Score)
	}
}

func TestCrashLoopContradictedByOOM(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.terminated", res, base.Add(2*time.Minute), map[string]any{"reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(1)}),
		obs("container.waiting", res, base.Add(5*time.Minute), map[string]any{"reason": "CrashLoopBackOff", "restarts": int64(1)}),
	}}

	_, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	hasNeg := false
	for _, line := range hypotheses[0].Score.Lines {
		if line.Delta < 0 {
			hasNeg = true
		}
	}
	if !hasNeg {
		t.Error("expected OOM contradiction line when OOMKilled is present")
	}
}
