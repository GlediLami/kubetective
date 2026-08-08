package score

import (
	"math"
	"testing"

	"github.com/GlediLami/kubetective/internal/model"
)

// The worked example: margin 75 at T=26 → ≈94%.
func TestPromptExample(t *testing.T) {
	terms := []EvidenceTerm{
		{ID: "e1", Label: "strong temporal correlation", Weight: 30, Strength: 1.0, Polarity: +1},
		{ID: "e2", Label: "direct ownership", Weight: 25, Strength: 1.0, Polarity: +1},
		{ID: "e3", Label: "mechanism: memory grew past limit", Weight: 20, Strength: 1.0, Polarity: +1},
		{ID: "e4", Label: "configuration changed", Weight: 15, Strength: 1.0, Polarity: +1},
		{ID: "e5", Label: "reproduced after restart", Weight: 10, Strength: 1.0, Polarity: +1},
		{ID: "e6", Label: "competing node-level evidence", Weight: 9, Strength: 1.0, Polarity: -1},
	}
	bd := Breakdown(model.Hypothesis{ID: "H5"}, terms, 1) // one evidence gap

	// Sum of visible lines: +30+25+20+15+10 −9 −6(gap) = 85.
	if got, want := bd.Margin, 85.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("margin = %v, want %v (must equal the sum of the visible breakdown lines)", got, want)
	}
	if got, want := bd.GapPenalty, 6.0; math.Abs(got-want) > 1e-9 {
		t.Fatalf("gap penalty = %v, want %v", got, want)
	}
	if got, want := bd.Score, 0.96; math.Abs(got-want) > 0.01 {
		t.Fatalf("score = %v, want ≈ %v (sigmoid(85/26))", got, want)
	}
	if len(bd.Lines) != 6 {
		t.Fatalf("breakdown lines = %d, want 6 (every contribution must be visible)", len(bd.Lines))
	}
}

func TestNoEvidenceIsFiftyFifty(t *testing.T) {
	bd := Breakdown(model.Hypothesis{ID: "H"}, nil, 0)
	if got, want := bd.Score, 0.5; math.Abs(got-want) > 1e-9 {
		t.Fatalf("empty evidence score = %v, want %v (no evidence ⇒ no confidence)", got, want)
	}
}

func TestContradictionReducesScore(t *testing.T) {
	with := Breakdown(model.Hypothesis{ID: "H"}, []EvidenceTerm{
		{ID: "s", Label: "support", Weight: 30, Strength: 1.0, Polarity: +1},
		{ID: "c", Label: "contradict", Weight: 20, Strength: 1.0, Polarity: -1},
	}, 0)
	without := Breakdown(model.Hypothesis{ID: "H"}, []EvidenceTerm{
		{ID: "s", Label: "support", Weight: 30, Strength: 1.0, Polarity: +1},
	}, 0)
	if with.Score >= without.Score {
		t.Fatalf("contradicting evidence must reduce the score: %v >= %v", with.Score, without.Score)
	}
}

func TestMissingEvidencePenalizes(t *testing.T) {
	withGap := Breakdown(model.Hypothesis{ID: "H"}, []EvidenceTerm{
		{ID: "s", Label: "support", Weight: 30, Strength: 1.0, Polarity: +1},
	}, 1)
	noGap := Breakdown(model.Hypothesis{ID: "H"}, []EvidenceTerm{
		{ID: "s", Label: "support", Weight: 30, Strength: 1.0, Polarity: +1},
	}, 0)
	if withGap.Score >= noGap.Score {
		t.Fatalf("missing evidence must penalize: %v >= %v", withGap.Score, noGap.Score)
	}
}

func TestTop(t *testing.T) {
	hs := []model.Hypothesis{
		{ID: "low", Score: &model.ScoreBreakdown{Score: 0.3}},
		{ID: "high", Score: &model.ScoreBreakdown{Score: 0.9}},
		{ID: "none"},
	}
	if got := Top(hs); got == nil || got.ID != "high" {
		t.Fatalf("Top = %v, want high", got)
	}
	if Top(nil) != nil {
		t.Fatal("Top of nil must be nil")
	}
}
