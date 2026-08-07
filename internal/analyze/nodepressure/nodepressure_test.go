package nodepressure

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

func TestNodePressureAnalyzer(t *testing.T) {
	node := model.ResourceRef{Kind: "node", Name: "node-a"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("node.condition", node, map[string]any{"type": "MemoryPressure", "status": "True", "message": "kubelet has pressure"}),
		mk("node.condition", node, map[string]any{"type": "DiskPressure", "status": "True"}),
		mk("node.condition", node, map[string]any{"type": "Ready", "status": "True"}), // must be ignored
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
	if h.Category != model.CatNode {
		t.Errorf("category = %s, want node", h.Category)
	}
	// Two pressure conditions (30 + 15) + message (10) = 55 → 0.88.
	if h.Score == nil || h.Score.Score < 0.8 {
		t.Errorf("score = %v, want ≥ 0.8", h.Score)
	}
	if len(h.Score.Lines) != 3 {
		t.Errorf("breakdown lines = %d, want 3 (2 conditions + message)", len(h.Score.Lines))
	}
	if findings[0].Severity != model.SevHigh {
		t.Errorf("severity = %s, want HIGH", findings[0].Severity)
	}
}

func TestNodePressureAnalyzerInactive(t *testing.T) {
	node := model.ResourceRef{Kind: "node", Name: "node-a"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		mk("node.condition", node, map[string]any{"type": "Ready", "status": "True"}),
		mk("node.condition", node, map[string]any{"type": "MemoryPressure", "status": "False"}),
	}}
	findings, hypotheses, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(findings) != 0 || len(hypotheses) != 0 {
		t.Fatalf("analyzer must stay silent on healthy nodes: %d findings, %d hypotheses", len(findings), len(hypotheses))
	}
}
