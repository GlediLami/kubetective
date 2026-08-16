package configregression

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

// A workload failing on a starved node would fail whatever its config said, so
// node pressure must argue against blaming the change.
func TestConfigRegressionContradictedByNodePressure(t *testing.T) {
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout"}
	node := model.ResourceRef{Kind: "node", Name: "node-a"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)

	regression := []model.Observation{
		cobs("git.commit", pod, base.Add(-5*time.Minute), map[string]any{
			"sha": "9f2c1a7d", "message": "raise cache size", "workload": "checkout"}),
		cobs("container.terminated", pod, base, map[string]any{"reason": "OOMKilled", "exit_code": int64(137)}),
	}

	_, clean, _, err := New().Analyze(context.Background(), &analyze.AnalysisInput{Observations: regression})
	if err != nil || len(clean) != 1 {
		t.Fatalf("clean: %v, hypotheses=%d", err, len(clean))
	}

	pressured := append(append([]model.Observation{}, regression...),
		cobs("node.condition", node, base, map[string]any{"type": "MemoryPressure", "status": "True"}))
	_, contradicted, _, err := New().Analyze(context.Background(), &analyze.AnalysisInput{Observations: pressured})
	if err != nil || len(contradicted) != 1 {
		t.Fatalf("pressured: %v, hypotheses=%d", err, len(contradicted))
	}

	if contradicted[0].Score.Score >= clean[0].Score.Score {
		t.Errorf("score %.3f did not fall below the uncontradicted %.3f",
			contradicted[0].Score.Score, clean[0].Score.Score)
	}
	neg := false
	for _, l := range contradicted[0].Score.Lines {
		if l.Delta < 0 {
			neg = true
		}
	}
	if !neg {
		t.Error("no contradicting line recorded for node pressure")
	}
}
