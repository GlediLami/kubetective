package crashloop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func obs(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestCrashLoopAnalyzer(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.terminated", res, base.Add(2*time.Minute), map[string]any{"reason": "Error", "exit_code": int64(1), "restarts": int64(1)}),
		obs("container.terminated", res, base.Add(4*time.Minute), map[string]any{"reason": "Error", "exit_code": int64(1), "restarts": int64(2)}),
		obs("container.waiting", res, base.Add(5*time.Minute), map[string]any{"reason": "CrashLoopBackOff", "restarts": int64(2)}),
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
	if h.Score == nil || h.Score.Score < 0.8 {
		t.Errorf("score = %v, want ≥ 0.8", h.Score)
	}
}

func TestCrashLoopContradictedByOOM(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.terminated", res, base.Add(2*time.Minute), map[string]any{"reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(1)}),
		obs("container.waiting", res, base.Add(5*time.Minute), map[string]any{"reason": "CrashLoopBackOff", "restarts": int64(1)}),
	}}

	_, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	hasNeg := false
	for _, line := range hypotheses[0].Score.Lines {
		if line.Delta < 0 {
			hasNeg = true
		}
	}
	if !hasNeg {
		t.Error("expected OOM contradiction line when OOMKilled is present")
	}
}

// Between two back-offs the container is reported as terminated, not waiting.
// The loop must still be found, so the verdict does not depend on timing.
func TestCrashLoopBetweenBackoffs(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.terminated", res, base, map[string]any{"reason": "Error", "exit_code": int64(1), "restarts": int64(3)}),
	}}
	_, hyps, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(hyps) != 1 || hyps[0].Category != model.CatCrashLoop {
		t.Fatalf("hyps = %+v, want one crashloop hypothesis", hyps)
	}
	if !strings.Contains(hyps[0].Claim, "exit codes [1]") {
		t.Errorf("claim = %q, want the exit code", hyps[0].Claim)
	}

	// A single failure is not a loop yet.
	in.Observations[0].Payload["restarts"] = int64(1)
	if _, hyps, _, _ = New().Analyze(context.Background(), in); len(hyps) != 0 {
		t.Errorf("one restart: hyps = %d, want 0", len(hyps))
	}
}

// While waiting in CrashLoopBackOff, the exit code comes from the last
// termination state.
func TestCrashLoopExitCodeFromLastState(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.waiting", res, time.Now(), map[string]any{"reason": "CrashLoopBackOff", "restarts": int64(4), "last_exit_code": int64(2)}),
	}}
	_, hyps, _, _ := New().Analyze(context.Background(), in)
	if len(hyps) != 1 || !strings.Contains(hyps[0].Claim, "exit codes [2]") {
		t.Fatalf("hyps = %+v, want claim with exit codes [2]", hyps)
	}
}
