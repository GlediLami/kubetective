package probe

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

func TestProbeAnalyzer(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("event.recorded", res, map[string]any{"reason": "Unhealthy", "message": "Readiness probe failed: HTTP probe failed with statuscode: 500"}),
		mk("event.recorded", res, map[string]any{"reason": "Unhealthy", "message": "Readiness probe failed: HTTP probe failed with statuscode: 500"}),
		mk("container.running", res, map[string]any{"restarts": int64(2)}),
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
	if h.Category != model.CatProbe {
		t.Errorf("category = %s, want probe", h.Category)
	}
	// events 30 + message 20 + restarts 10 = 60 → 0.91.
	if h.Score == nil || h.Score.Score < 0.85 {
		t.Errorf("score = %v, want ≥ 0.85", h.Score)
	}
}

func TestProbeAnalyzerInactive(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("event.recorded", res, map[string]any{"reason": "Scheduled", "message": "Successfully assigned"}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("analyzer must stay silent without probe failures: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}
