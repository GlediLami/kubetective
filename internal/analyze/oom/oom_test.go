package oom

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
)

func obs(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestOOMAnalyzerFindsMemoryExhaustion(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.spec", res, base, map[string]any{
			"container": "checkout",
			"limits":    map[string]string{"memory": "1Gi"},
		}),
		obs("container.terminated", res, base.Add(6*time.Minute), map[string]any{"container": "checkout", "reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(1)}),
		obs("container.terminated", res, base.Add(9*time.Minute), map[string]any{"container": "checkout", "reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(2)}),
		obs("container.terminated", res, base.Add(12*time.Minute), map[string]any{"container": "checkout", "reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(3)}),
		obs("node.condition", model.ResourceRef{Kind: "node", Name: "node-a"}, base, map[string]any{"type": "Ready", "status": "True"}),
	}}

	a := New()
	findings, hypotheses, evidence, err := a.Analyze(context.Background(), in)
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
	if h.Category != model.CatMemory {
		t.Errorf("category = %s, want memory", h.Category)
	}
	if h.Score == nil {
		t.Fatal("hypothesis must be scored")
	}
	// 3 OOMKilled + limit + reproduction + temporal = 30+20+15+10 = 75 → high.
	if h.Score.Score < 0.9 {
		t.Errorf("score = %.3f, want ≥ 0.9 (strong mechanism evidence)", h.Score.Score)
	}
	// Every score line must reference evidence (explainability contract).
	ids := map[string]bool{}
	for _, e := range evidence {
		ids[e.ID] = true
	}
	for _, line := range h.Score.Lines {
		if !ids[line.EvidenceID] {
			t.Errorf("score line %q references unknown evidence %q", line.Label, line.EvidenceID)
		}
	}
	// Finding severity must be HIGH.
	if findings[0].Severity != model.SevHigh {
		t.Errorf("severity = %s, want HIGH", findings[0].Severity)
	}
	if findings[0].Title != "OOMKilled ×3" {
		t.Errorf("title = %q, want %q", findings[0].Title, "OOMKilled ×3")
	}
}

func TestOOMAnalyzerContradictsOnNodePressure(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "p1"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.terminated", res, base.Add(6*time.Minute), map[string]any{"reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(1)}),
		obs("node.condition", model.ResourceRef{Kind: "node", Name: "node-a"}, base, map[string]any{"type": "MemoryPressure", "status": "True"}),
	}}
	_, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	hasContradiction := false
	for _, line := range hypotheses[0].Score.Lines {
		if line.Delta < 0 {
			hasContradiction = true
		}
	}
	if !hasContradiction {
		t.Error("expected a negative (contradicting) score line for node memory pressure")
	}
}

func TestOOMAnalyzerInactiveWithoutOOM(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "p1"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.running", res, time.Now(), map[string]any{"restarts": int64(0)}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("analyzer must stay silent without OOMKilled: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}

// TestOOMAnalyzerAfterJSONRoundTrip is the replay regression: observations
// served from a record.jsonl have JSON-decoded payloads (map[string]any,
// float64 numbers). Analyzers must produce identical evidence either way.
func TestOOMAnalyzerAfterJSONRoundTrip(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.spec", res, base, map[string]any{
			"limits": map[string]string{"memory": "1Gi"},
		}),
		obs("container.terminated", res, base.Add(6*time.Minute), map[string]any{"reason": "OOMKilled", "exit_code": int64(137), "restarts": int64(3)}),
	}}
	// JSON round trip (as record.Load would serve it).
	b, err := json.Marshal(in.Observations)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []model.Observation
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	_, hypotheses, _, err := New().Analyze(context.Background(), &analyze.AnalysisInput{Observations: decoded})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	hasLimit := false
	for _, line := range h.Score.Lines {
		if line.Delta > 0 && line.EvidenceID == "oom.checkout-7f84c9.limit" {
			hasLimit = true
		}
	}
	if !hasLimit {
		t.Error("memory-limit evidence lost after JSON round trip — payload type assertions must be tolerant")
	}
	if h.Score.Score < 0.8 {
		t.Errorf("score = %.3f, want ≥ 0.8 with limit evidence present", h.Score.Score)
	}
}
