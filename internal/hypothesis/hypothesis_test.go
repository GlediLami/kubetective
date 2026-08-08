package hypothesis

import (
	"testing"

	"github.com/GlediLami/kubetective/internal/model"
)

func scored(id string, category model.HypothesisCategory, score float64) model.Hypothesis {
	return model.Hypothesis{
		ID:       id,
		Claim:    "claim for " + id,
		Category: category,
		Status:   model.StatusLikely,
		Score:    &model.ScoreBreakdown{Score: score, Margin: score * 26},
		Evidence: []string{"e-" + id},
	}
}

func TestMergeDedupesAndRanks(t *testing.T) {
	hs := []model.Hypothesis{
		scored("crashloop.checkout-7f84c9", model.CatCrashLoop, 0.55),
		scored("memory.checkout-7f84c9", model.CatMemory, 0.94),
		scored("imagepull.worker-x1", model.CatImage, 0.89),
	}
	out := Merge(hs)
	if len(out) != 3 {
		t.Fatalf("merged = %d, want 3", len(out))
	}
	if out[0].ID != "memory.checkout-7f84c9" {
		t.Errorf("top = %s, want memory (0.94)", out[0].ID)
	}
	// Different resource → untouched (Likely).
	if out[1].ID != "imagepull.worker-x1" || out[1].Status != model.StatusLikely {
		t.Errorf("imagepull = %+v, want Likely (different resource)", out[1])
	}
	// Same-resource runner-up outranked by ≥0.25 → ruled out.
	if out[2].ID != "crashloop.checkout-7f84c9" || out[2].Status != model.StatusRuledOut {
		t.Errorf("crashloop = %+v, want RuledOut (gap 0.39 ≥ 0.25)", out[2])
	}
}

func TestMergeSameIDCombinesEvidence(t *testing.T) {
	a := scored("memory.checkout", model.CatMemory, 0.8)
	a.Evidence = []string{"e1"}
	b := scored("memory.checkout", model.CatMemory, 0.8)
	b.Evidence = []string{"e2"}
	out := Merge([]model.Hypothesis{a, b})
	if len(out) != 1 {
		t.Fatalf("merged = %d, want 1", len(out))
	}
	if len(out[0].Evidence) != 2 {
		t.Errorf("evidence = %v, want merged [e1 e2]", out[0].Evidence)
	}
}

func TestMergeNearTieStaysCandidate(t *testing.T) {
	hs := []model.Hypothesis{
		scored("crashloop.checkout", model.CatCrashLoop, 0.89),
		scored("memory.checkout", model.CatMemory, 0.80),
	}
	out := Merge(hs)
	if out[1].Status != model.StatusCandidate {
		t.Errorf("runner-up status = %s, want Candidate (gap 0.09 < 0.25 → multiple plausible causes)", out[1].Status)
	}
}

func TestMergeEmpty(t *testing.T) {
	if out := Merge(nil); len(out) != 0 {
		t.Fatalf("Merge(nil) = %d, want 0", len(out))
	}
}
