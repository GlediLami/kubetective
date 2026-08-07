package hpa

import (
	"context"
	"testing"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
)

func mk(kind string, res model.ResourceRef, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, time.Now(), res, payload, 1.0)
}

func TestHPAAnalyzerAtMax(t *testing.T) {
	hpaRes := model.ResourceRef{Kind: "hpa", Namespace: "prod", Name: "checkout-hpa"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("hpa.state", hpaRes, map[string]any{
			"min_replicas": int64(1), "max_replicas": int64(5),
			"current_replicas": int64(5), "desired_replicas": int64(5),
			"workload": "checkout", "at_max": true,
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
	if findings[0].Severity != model.SevWarning {
		t.Errorf("severity = %s, want WARNING (context, not root cause)", findings[0].Severity)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	if h.Category != model.CatHPA {
		t.Errorf("category = %s, want hpa", h.Category)
	}
	if h.Score == nil || h.Score.Score < 0.7 {
		t.Errorf("score = %v, want ≥ 0.7 (at max 30 → 0.76)", h.Score)
	}
}

func TestHPAAnalyzerSilentWhenNotAtMax(t *testing.T) {
	hpaRes := model.ResourceRef{Kind: "hpa", Namespace: "prod", Name: "checkout-hpa"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("hpa.state", hpaRes, map[string]any{
			"min_replicas": int64(1), "max_replicas": int64(5),
			"current_replicas": int64(3), "desired_replicas": int64(3),
			"workload": "checkout", "at_max": false,
		}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("analyzer must stay silent below max: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}
