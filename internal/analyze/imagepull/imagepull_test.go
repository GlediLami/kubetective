package imagepull

import (
	"context"
	"testing"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
)

func obs(kind string, res model.ResourceRef, ts time.Time, payload map[string]any) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, res, payload, 1.0)
}

func TestImagePullAnalyzer(t *testing.T) {
	res := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "worker-x1"}
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.spec", res, base, map[string]any{"image": "registry.example/worker:latest"}),
		obs("container.waiting", res, base.Add(3*time.Minute), map[string]any{
			"reason":  "ImagePullBackOff",
			"message": "Back-off pulling image \"registry.example/worker:latest\": manifest unknown",
		}),
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
	if h.Category != model.CatImage {
		t.Errorf("category = %s, want image", h.Category)
	}
	if h.Score == nil || h.Score.Score < 0.8 {
		t.Errorf("score = %v, want ≥ 0.8", h.Score)
	}
	// Image + message must appear in the breakdown.
	text := ""
	for _, line := range h.Score.Lines {
		text += line.Label + " "
	}
	if len(h.Score.Lines) != 3 {
		t.Errorf("breakdown lines = %d, want 3 (waiting + message + image)", len(h.Score.Lines))
	}
}
