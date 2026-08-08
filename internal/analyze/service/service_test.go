package service

import (
	"context"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func mk(kind string, res model.ResourceRef, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, time.Now(), res, payload, 1.0)
}

func TestServiceAnalyzerMismatch(t *testing.T) {
	svc := model.ResourceRef{Kind: "service", Namespace: "prod", Name: "checkout-svc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("service.state", svc, map[string]any{"selector": map[string]string{"app": "checkout"}, "ready_endpoints": int64(0), "total_endpoints": int64(0), "matching_pods": 1}),
		mk("pod.state", model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}, map[string]any{"phase": "Running"}),
	}}

	a := New()
	findings, hypotheses, _, err := a.Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	if findings[0].Severity != model.SevHigh {
		t.Errorf("severity = %s, want HIGH (selector matches running pods)", findings[0].Severity)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	if h.Category != model.CatService {
		t.Errorf("category = %s, want service", h.Category)
	}
	// noendpoints 30 + mismatch 20 + selector 10 = 60 → 0.91.
	if h.Score == nil || h.Score.Score < 0.85 {
		t.Errorf("score = %v, want ≥ 0.85", h.Score)
	}
}

func TestServiceAnalyzerSilentWhenEndpointsExist(t *testing.T) {
	svc := model.ResourceRef{Kind: "service", Namespace: "prod", Name: "checkout-svc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("service.state", svc, map[string]any{"selector": map[string]string{"app": "checkout"}, "ready_endpoints": int64(2), "total_endpoints": int64(2)}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("analyzer must stay silent when endpoints exist: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}
