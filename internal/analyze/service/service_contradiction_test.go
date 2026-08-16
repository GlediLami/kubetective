package service

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

// Empty endpoints only indict the selector when there are healthy pods that
// should have been selected. With every pod down, the empty endpoint list is a
// consequence of the pods, and the service hypothesis must say so.
func TestServiceContradictedByFailingPods(t *testing.T) {
	svc := model.ResourceRef{Kind: "service", Namespace: "prod", Name: "checkout"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	svcObs := cobs("service.state", svc, base, map[string]any{
		"ready_endpoints": int64(0),
		"total_endpoints": int64(0),
		"selector":        map[string]string{"app": "checkout"},
	})

	// Healthy pods present: the selector is genuinely suspect.
	healthy := &analyze.AnalysisInput{Observations: []model.Observation{
		svcObs,
		cobs("pod.state", model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-a"}, base, map[string]any{"phase": "Running"}),
		cobs("pod.state", model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-b"}, base, map[string]any{"phase": "Running"}),
	}}
	// Every pod down: the empty endpoints follow from that, not the selector.
	broken := &analyze.AnalysisInput{Observations: []model.Observation{
		svcObs,
		cobs("pod.state", model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-a"}, base, map[string]any{"phase": "Pending"}),
		cobs("pod.state", model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-b"}, base, map[string]any{"phase": "Pending"}),
	}}

	_, hs, _, err := New().Analyze(context.Background(), healthy)
	if err != nil || len(hs) != 1 {
		t.Fatalf("healthy: %v, hypotheses=%d", err, len(hs))
	}
	_, hs2, _, err := New().Analyze(context.Background(), broken)
	if err != nil || len(hs2) != 1 {
		t.Fatalf("broken: %v, hypotheses=%d", err, len(hs2))
	}

	if hs2[0].Score.Score >= hs[0].Score.Score {
		t.Errorf("score with no ready pods (%.3f) must be below the score with healthy pods (%.3f)",
			hs2[0].Score.Score, hs[0].Score.Score)
	}
	neg := false
	for _, l := range hs2[0].Score.Lines {
		if l.Delta < 0 {
			neg = true
		}
	}
	if !neg {
		t.Error("no contradicting line recorded for the failing pods")
	}
}
