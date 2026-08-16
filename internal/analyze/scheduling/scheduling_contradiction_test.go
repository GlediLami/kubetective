package scheduling

import (
	"context"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func cobs(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

// "Unschedulable" is the generic answer. An unbound claim in the same namespace
// is a specific one, and the generic hypothesis must yield ground to it.
func TestSchedulingContradictedByUnboundPVC(t *testing.T) {
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "batch-1"}
	pvc := model.ResourceRef{Kind: "persistentvolumeclaim", Namespace: "prod", Name: "data"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)

	pending := []model.Observation{
		cobs("pod.state", pod, base, map[string]any{"phase": "Pending"}),
		cobs("event.recorded", pod, base, map[string]any{"reason": "FailedScheduling", "message": "0/1 nodes are available"}),
	}

	_, clean, _, err := New().Analyze(context.Background(), &analyze.AnalysisInput{Observations: pending})
	if err != nil || len(clean) != 1 {
		t.Fatalf("clean: %v, hypotheses=%d", err, len(clean))
	}

	withPVC := append(append([]model.Observation{}, pending...),
		cobs("pvc.state", pvc, base, map[string]any{"phase": "Pending"}))
	_, contradicted, _, err := New().Analyze(context.Background(), &analyze.AnalysisInput{Observations: withPVC})
	if err != nil || len(contradicted) != 1 {
		t.Fatalf("withPVC: %v, hypotheses=%d", err, len(contradicted))
	}

	if contradicted[0].Score.Score >= clean[0].Score.Score {
		t.Errorf("score %.3f did not fall below the uncontradicted %.3f",
			contradicted[0].Score.Score, clean[0].Score.Score)
	}
}
