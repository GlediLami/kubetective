package dns

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

// DNS events beside an OOMKill do not explain the crash: the memory kill does.
func TestDNSContradictedByOOM(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-1"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	dnsEvidence := []model.Observation{
		cobs("event.recorded", res, base, map[string]any{"reason": "Failed", "message": "no such host"}),
		cobs("container.waiting", res, base, map[string]any{"reason": "CrashLoopBackOff"}),
	}

	_, clean, _, err := New().Analyze(context.Background(), &analyze.AnalysisInput{Observations: dnsEvidence})
	if err != nil || len(clean) == 0 {
		t.Fatalf("clean: %v, hypotheses=%d", err, len(clean))
	}

	withOOM := append(append([]model.Observation{}, dnsEvidence...),
		cobs("container.terminated", res, base, map[string]any{"reason": "OOMKilled", "exit_code": int64(137)}))
	_, contradicted, _, err := New().Analyze(context.Background(), &analyze.AnalysisInput{Observations: withOOM})
	if err != nil || len(contradicted) == 0 {
		t.Fatalf("withOOM: %v, hypotheses=%d", err, len(contradicted))
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
		t.Error("no contradicting line recorded for the OOMKill")
	}
}
