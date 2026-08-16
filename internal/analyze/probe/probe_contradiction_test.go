package probe

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

// A container the kernel killed for memory fails its probes on the way down.
// The probe hypothesis must record that as evidence against itself.
func TestProbeContradictedByOOM(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "web-1"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)

	clean := &analyze.AnalysisInput{Observations: []model.Observation{
		cobs("event.recorded", res, base, map[string]any{"reason": "Unhealthy", "message": "Readiness probe failed: 503"}),
		cobs("container.running", res, base, map[string]any{"restarts": int64(3)}),
	}}
	withOOM := &analyze.AnalysisInput{Observations: append(append([]model.Observation{}, clean.Observations...),
		cobs("container.terminated", res, base, map[string]any{"reason": "OOMKilled", "exit_code": int64(137)}))}

	_, hs, _, err := New().Analyze(context.Background(), clean)
	if err != nil || len(hs) != 1 {
		t.Fatalf("clean: %v, hypotheses=%d", err, len(hs))
	}
	cleanScore := hs[0].Score.Score

	_, hs2, _, err := New().Analyze(context.Background(), withOOM)
	if err != nil || len(hs2) != 1 {
		t.Fatalf("withOOM: %v, hypotheses=%d", err, len(hs2))
	}
	if hs2[0].Score.Score >= cleanScore {
		t.Errorf("score %.3f did not fall below the uncontradicted %.3f", hs2[0].Score.Score, cleanScore)
	}
	if !hasContradiction(hs2[0]) {
		t.Error("no contradicting line recorded for the OOMKill")
	}
}

func hasContradiction(h model.Hypothesis) bool {
	for _, l := range h.Score.Lines {
		if l.Delta < 0 {
			return true
		}
	}
	return false
}
