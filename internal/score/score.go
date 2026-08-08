// Package score implements the explainable scoring model:
//
//	margin(H) = Σ support w·s − Σ contradict w·|s| − λ_miss · |missing|
//	score(H)  = sigmoid(margin(H) / T)
//
// Every point of margin maps to a visible ScoreLine backed by an Observation,
// and the temperature T is calibrated against the scenario benchmark
// (docs/DESIGN.md §9).
package score

import (
	"math"

	"github.com/kubedoctor/kubedoctor/internal/model"
)

// DefaultTemperature is the initial T. Calibration fits it (and per-category
// biases) against benchmark ground truth; until then this is the honest
// default that keeps scores from saturating on typical margins.
const DefaultTemperature = 26.0

// CurrentTemperature is the engine's operating temperature. It starts at
// DefaultTemperature and is replaced only when calibration validation adopts
// a fitted value (see benchmark). Callers passing 0 to Sigmoid/Breakdown get
// this value; explicit temperatures always win.
var CurrentTemperature = DefaultTemperature

// SetTemperature adopts a calibrated temperature. Only call with a
// leave-one-out-validated fit (score.CalibrateLOO).
func SetTemperature(t float64) {
	if t > 0 {
		CurrentTemperature = t
	}
}

// DefaultLambdaMissing is the per-gap penalty applied to the margin.
const DefaultLambdaMissing = 6.0

// EvidenceTerm is one signed contribution to a hypothesis margin.
type EvidenceTerm struct {
	ID       string  // Evidence.ID
	Label    string  // human-readable line, e.g. "strong temporal correlation"
	Weight   float64 // from the scoring table
	Strength float64 // -1..1, analyzer-assigned
	Polarity int     // +1 supports, -1 contradicts
}

// Margin computes the raw margin for a hypothesis.
func Margin(terms []EvidenceTerm, missingCount int, lambdaMissing float64) float64 {
	var m float64
	for _, t := range terms {
		m += t.Weight * t.Strength * float64(t.Polarity)
	}
	if lambdaMissing > 0 {
		m -= lambdaMissing * float64(missingCount)
	}
	return m
}

// Sigmoid maps a margin to a 0..1 assessment score.
func Sigmoid(margin, temperature float64) float64 {
	if temperature <= 0 {
		temperature = CurrentTemperature
	}
	return 1.0 / (1.0 + math.Exp(-margin/temperature))
}

// Breakdown scores a hypothesis and produces the full explainable breakdown.
func Breakdown(h model.Hypothesis, terms []EvidenceTerm, missing int) model.ScoreBreakdown {
	lines := make([]model.ScoreLine, 0, len(terms))
	for _, t := range terms {
		delta := t.Weight * t.Strength * float64(t.Polarity)
		lines = append(lines, model.ScoreLine{
			Label:      t.Label,
			Delta:      delta,
			EvidenceID: t.ID,
		})
	}
	gapPenalty := DefaultLambdaMissing * float64(missing)
	margin := Margin(terms, missing, DefaultLambdaMissing)
	t := CurrentTemperature
	return model.ScoreBreakdown{
		Margin:      margin,
		Temperature: t,
		Score:       Sigmoid(margin, t),
		Lines:       lines,
		GapPenalty:  gapPenalty,
	}
}

// Top returns the highest-scoring hypothesis, or nil if none.
func Top(hs []model.Hypothesis) *model.Hypothesis {
	var top *model.Hypothesis
	for i := range hs {
		h := &hs[i]
		if h.Score == nil {
			continue
		}
		if top == nil || h.Score.Score > top.Score.Score {
			top = h
		}
	}
	return top
}
