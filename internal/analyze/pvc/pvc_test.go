package pvc

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

func TestPVCAnalyzerPending(t *testing.T) {
	pvcRes := model.ResourceRef{Kind: "pvc", Namespace: "prod", Name: "checkout-data"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pvc.state", pvcRes, map[string]any{"phase": "Pending", "requested": "10Gi", "pod": "checkout-abc"}),
		mk("event.recorded", pvcRes, map[string]any{"reason": "FailedBinding", "message": "no volume plugin matched"}),
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
	if h.Category != model.CatPVC {
		t.Errorf("category = %s, want pvc", h.Category)
	}
	// pending 30 + event 20 + request 10 = 60 → 0.91.
	if h.Score == nil || h.Score.Score < 0.85 {
		t.Errorf("score = %v, want ≥ 0.85", h.Score)
	}
	if findings[0].Severity != model.SevHigh {
		t.Errorf("severity = %s, want HIGH", findings[0].Severity)
	}
}

func TestPVCAnalyzerInactiveWhenBound(t *testing.T) {
	pvcRes := model.ResourceRef{Kind: "pvc", Namespace: "prod", Name: "checkout-data"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pvc.state", pvcRes, map[string]any{"phase": "Bound", "capacity": "10Gi"}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("analyzer must stay silent on Bound claims: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}
