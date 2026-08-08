package scheduling

import (
	"context"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func mk(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestSchedulingAnalyzer(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "worker-x1"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pod.state", res, base, map[string]any{"phase": "Pending"}),
		mk("event.recorded", res, base.Add(1*time.Minute), map[string]any{
			"reason":  "FailedScheduling",
			"message": "0/1 nodes are available: 1 Insufficient memory.",
		}),
		mk("container.spec", res, base, map[string]any{"requests": map[string]string{"memory": "4Gi", "cpu": "2"}}),
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
	if h.Category != model.CatScheduling {
		t.Errorf("category = %s, want scheduling", h.Category)
	}
	if h.Score == nil || h.Score.Score < 0.85 {
		t.Errorf("score = %v, want ≥ 0.85 (pending + message + requests = 60 → 0.91)", h.Score)
	}
	if len(h.Score.Lines) != 3 {
		t.Errorf("breakdown lines = %d, want 3 (pending + message + requests)", len(h.Score.Lines))
	}
	if findings[0].Severity != model.SevWarning {
		t.Errorf("severity = %s, want WARNING (pending is not a crash)", findings[0].Severity)
	}
}

func TestSchedulingAnalyzerGapWithoutMessage(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "worker-x1"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pod.state", res, time.Now(), map[string]any{"phase": "Pending"}),
	}}
	_, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	if len(hypotheses[0].Missing) != 1 {
		t.Errorf("missing = %v, want the scheduler message listed as a gap", hypotheses[0].Missing)
	}
	if hypotheses[0].Score.Score >= 0.8 {
		t.Errorf("score = %.3f, want < 0.8 with the key message evidence missing", hypotheses[0].Score.Score)
	}
}

func TestSchedulingAnalyzerInactive(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "running-1"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("pod.state", res, time.Now(), map[string]any{"phase": "Running"}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("analyzer must stay silent for Running pods: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}
