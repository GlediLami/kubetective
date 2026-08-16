package score

import (
	"fmt"
	"math"
)

// CalibrationPoint is one benchmark verdict: the margin of the top hypothesis
// and whether it matched ground truth. Calibration turns margins into
// calibrated confidence.
type CalibrationPoint struct {
	Margin  float64
	Correct bool
}

// CalibrationReport is the outcome of temperature fitting.
type CalibrationReport struct {
	Temperature float64 // fitted T minimizing NLL
	ECE         float64 // ECE at the fitted temperature (reported, not fitted)
	DefaultECE  float64 // ECE at DefaultTemperature (before fitting)
	NLL         float64 // negative log-likelihood at the fitted temperature
	DefaultNLL  float64 // NLL at DefaultTemperature
	Points      int
	Correct     int
	Accuracy    float64 // Correct / Points
}

// LOOReport is the leave-one-out cross-validation of the temperature fit
// (calibration hardening, v0.7): fit T on N−1 points, predict the held-out
// one, repeat — the out-of-sample ECE shows whether the fit generalizes or
// just memorizes the suite.
type LOOReport struct {
	Temperature float64 // fitted T on all points (same as Calibrate)
	FittedECE   float64 // in-sample ECE at Temperature
	DefaultECE  float64 // ECE at DefaultTemperature
	LOOECE      float64 // out-of-sample ECE — REPORTED ONLY, never gates; see below
	Points      int     // ground-truth points the fit saw
	Incorrect   int     // how many of them the engine got wrong

	// The adoption decision runs on proper scoring rules, not on ECE.
	//
	// ECE bins confidences into ten fixed buckets, so a held-out point can
	// swing the estimate by crossing a bucket edge that means nothing. On this
	// suite — every confidence inside [0.89, 0.98], i.e. two buckets — LOO ECE
	// moved 16.98 → 6.88 → 26.41 → 1.28 → 1.47 → 29.63 as single points were
	// added. Adoption gated on that is a coin flip, and it was: the same suite
	// plus one scenario flipped the verdict, twice.
	//
	// NLL and Brier are proper, unbinned, and monotone in the same experiment.
	// Both must improve out-of-sample: NLL because it is the fitted objective
	// and must therefore generalize, Brier because it is an *independent* rule
	// and stops the fit from being graded solely by its own loss.
	LOONLL       float64 // out-of-sample NLL
	DefaultNLL   float64 // NLL at DefaultTemperature
	LOOBrier     float64 // out-of-sample Brier score
	DefaultBrier float64 // Brier at DefaultTemperature

	// GridLow and GridHigh are the scan bounds this fit searched, derived from
	// the observed margins (see scanGrid). Reported so a refusal for
	// degeneracy can be read against the range it ran off.
	GridLow  float64
	GridHigh float64

	// Degenerate reports a fit pinned to an end of the scan grid: the
	// optimiser wanted to go further and could not, which means the objective
	// was not minimised but run off its edge. At the floor the fit is asking
	// for maximum confidence; at the ceiling it is asking for 50% on
	// everything, i.e. "these margins carry no signal". Both are refusals.
	Degenerate bool
	// Adopt is true only when the fit is trustworthy on every axis; see
	// adoptionRefusal for the exact conditions.
	Adopt bool
	// RefusalReason explains a false Adopt in one line, for the CLI to print.
	// Empty when Adopt is true.
	RefusalReason string
}

// maxCalibrationECE is the design's §9.4 threshold: above it the engine
// dampens displayed confidence toward 50% and flags mis-calibrated output.
const maxCalibrationECE = 0.10

// Scan grid for temperature fitting. A fit landing on either bound is
// degenerate — see LOOReport.Degenerate.
//
// The bounds are derived from the observed margins rather than hardcoded. The
// quantity that matters to a sigmoid is margin/T, so a fixed grid is only
// meaningful for a fixed margin scale — and this engine's margin scale is set
// by the weight bands in scale.go, which have been rebased once already.
//
// The old grid was a hardcoded [5, 80]. With margins running 54–101, T=80 maps
// the typical margin to 0.68 and the grid could not go higher: the search space
// could express "confident" and "very confident" and nothing else. An engine
// that turned out to be badly overconfident was therefore uncalibratable by
// construction — the fit would pin to the ceiling and be refused as degenerate,
// which reads as "the data is bad" when the truth was "the grid is too small".
const (
	// gridLowSlope: at the tightest temperature the largest margin maps to
	// sigmoid(6) ≈ 0.9975 — as confident as it is ever meaningful to be.
	gridLowSlope = 6.0
	// gridHighSlope: at the loosest, it maps to sigmoid(0.2) ≈ 0.55 — barely
	// above a coin flip, the honest floor for an engine with no signal.
	gridHighSlope = 0.2
	// gridSteps fixes the resolution so the scan stays deterministic and its
	// cost independent of the margin scale.
	gridSteps = 400
	// fallbackTemperature bounds a degenerate input (no points, all margins
	// zero) so the scan is always well-formed.
	fallbackTemperature = 26.0
)

// scanGrid returns the temperature range to search for this point set.
func scanGrid(points []CalibrationPoint) (lo, hi, step float64) {
	maxMargin := 0.0
	for _, p := range points {
		if m := math.Abs(p.Margin); m > maxMargin {
			maxMargin = m
		}
	}
	if maxMargin == 0 {
		return fallbackTemperature, fallbackTemperature, 1
	}
	lo = maxMargin / gridLowSlope
	hi = maxMargin / gridHighSlope
	return lo, hi, (hi - lo) / gridSteps
}

// minCalibrationPoints is the smallest suite whose fitted temperature is worth
// adopting. Below it the fit is sampling noise.
const minCalibrationPoints = 10

// minCalibrationErrors is the crux of honest calibration: expected calibration
// error is |confidence − accuracy|, so on a suite the engine never fails the
// ECE-optimal policy is to answer 100% every time. Fitting against such a
// suite does not learn calibration, it learns maximum overconfidence — and the
// resulting temperature saturates the sigmoid. Confidence can only be
// calibrated against predictions that were wrong.
const minCalibrationErrors = 1

// minRelativeImprovement is how much better than the default a fit must score
// out-of-sample before it is worth adopting. Estimating T from finite data
// costs variance; a fit that beats the default by less than this has not paid
// for itself, and the decision to adopt it is decided by noise.
//
// Measured, not guessed. Sweeping the threshold over five suites and flipping
// every single point out one at a time: at 0% and 1%, the refuse/adopt verdict
// changed under 7–18 of the removals; at 2% every refusal held under all of
// them. Above ~5% genuine fits start being rejected.
const minRelativeImprovement = 0.02

// adoptionRefusal returns the reason a fit must not be adopted, or "" when it
// is safe to adopt. Single source of truth: every call site checks Adopt only.
func adoptionRefusal(rep LOOReport) string {
	switch {
	case rep.Points < minCalibrationPoints:
		return fmt.Sprintf("only %d ground-truth points, need ≥%d", rep.Points, minCalibrationPoints)
	case rep.Incorrect < minCalibrationErrors:
		return "the suite contains no incorrect predictions — confidence cannot be calibrated " +
			"against a benchmark the engine never fails"
	case rep.Degenerate:
		return fmt.Sprintf("fitted T=%.1f sits on the scan boundary — the fit is degenerate, not minimised", rep.Temperature)
	case rep.LOONLL >= rep.DefaultNLL*(1-minRelativeImprovement):
		return fmt.Sprintf("out-of-sample NLL %.4f does not beat the default %.4f by the required %.0f%%",
			rep.LOONLL, rep.DefaultNLL, minRelativeImprovement*100)
	case rep.LOOBrier >= rep.DefaultBrier*(1-minRelativeImprovement):
		return fmt.Sprintf("out-of-sample Brier %.4f does not beat the default %.4f by the required %.0f%%",
			rep.LOOBrier, rep.DefaultBrier, minRelativeImprovement*100)
	}
	return ""
}

// negLogLik is the mean negative log-likelihood of the outcomes under
// sigmoid(margin/T) — the canonical temperature-scaling objective. It is
// smooth and has a single interior optimum, so the scan finds a minimum rather
// than hopping between the plateaus a binned statistic presents.
func negLogLik(points []CalibrationPoint, temperature float64) float64 {
	const eps = 1e-12
	if len(points) == 0 {
		return 0
	}
	var out float64
	for _, p := range points {
		s := Sigmoid(p.Margin, temperature)
		if p.Correct {
			out -= math.Log(math.Max(s, eps))
		} else {
			out -= math.Log(math.Max(1-s, eps))
		}
	}
	return out / float64(len(points))
}

// brierScore is the mean squared error of the confidence against the outcome:
// a second proper scoring rule, independent of the fitted objective.
func brierScore(points []CalibrationPoint, temperature float64) float64 {
	if len(points) == 0 {
		return 0
	}
	var out float64
	for _, p := range points {
		s := Sigmoid(p.Margin, temperature)
		y := 0.0
		if p.Correct {
			y = 1
		}
		out += (s - y) * (s - y)
	}
	return out / float64(len(points))
}

// looProper evaluates a scoring rule out-of-sample: fit T on the other N−1
// points, score the held-out one, average. No binning anywhere in the loop, so
// a single point cannot move the result by crossing a bucket edge.
func looProper(points []CalibrationPoint, rule func([]CalibrationPoint, float64) float64) float64 {
	if len(points) == 0 {
		return 0
	}
	var out float64
	fold := make([]CalibrationPoint, 0, len(points)-1)
	for i := range points {
		fold = fold[:0]
		for j := range points {
			if j != i {
				fold = append(fold, points[j])
			}
		}
		foldT, _ := fitTemperature(fold)
		out += rule(points[i:i+1], foldT)
	}
	return out / float64(len(points))
}

// ece computes the expected calibration error: |confidence − accuracy| per
// bin, weighted by bin size (10 equal-width bins over [0,1]).
func ece(points []CalibrationPoint, temperature float64) float64 {
	const bins = 10
	var counts [bins]int
	var conf [bins]float64
	var acc [bins]float64
	for _, p := range points {
		s := Sigmoid(p.Margin, temperature)
		bin := int(s * bins)
		if bin >= bins {
			bin = bins - 1
		}
		counts[bin]++
		conf[bin] += s
		if p.Correct {
			acc[bin] += 1
		}
	}
	total := float64(len(points))
	if total == 0 {
		return 0
	}
	var out float64
	for b := 0; b < bins; b++ {
		if counts[b] == 0 {
			continue
		}
		meanConf := conf[b] / float64(counts[b])
		meanAcc := acc[b] / float64(counts[b])
		out += (float64(counts[b]) / total) * math.Abs(meanConf-meanAcc)
	}
	return out
}

// fitTemperature scans T and returns the one minimizing negative
// log-likelihood, plus that NLL. Deterministic: same points → same temperature.
//
// The objective is NLL and not ECE on purpose. ECE is a binned statistic: it is
// piecewise-constant in T, so "minimising" it means picking a plateau, and
// which plateau wins can change when one point moves. Fitting on it made the
// leave-one-out estimate — and therefore the adoption decision — unstable to a
// single scenario. NLL is the standard choice for temperature scaling for
// exactly this reason. ECE survives as a *report*: it answers "by how many
// points is displayed confidence off?", which is what a human wants to read,
// and no decision hangs on it.
func fitTemperature(points []CalibrationPoint) (bestT, bestNLL float64) {
	bestT = DefaultTemperature
	bestNLL = negLogLik(points, DefaultTemperature)
	lo, hi, step := scanGrid(points)
	if step <= 0 {
		return bestT, bestNLL
	}
	for t := lo; t <= hi; t += step {
		if e := negLogLik(points, t); e < bestNLL {
			bestNLL = e
			bestT = t
		}
	}
	return bestT, bestNLL
}

// atScanBoundary reports whether a fitted temperature landed on an end of the
// scan grid — the signature of an objective that was never minimised. At the
// floor the fit is asking for maximum confidence (the all-correct degeneracy);
// at the ceiling it is asking for ~50% on everything, i.e. "these margins carry
// no information about correctness". Neither is a calibration.
func atScanBoundary(points []CalibrationPoint, t float64) bool {
	lo, hi, step := scanGrid(points)
	return t <= lo+step || t >= hi-step
}

// countIncorrect returns how many predictions the engine got wrong.
func countIncorrect(points []CalibrationPoint) int {
	n := 0
	for _, p := range points {
		if !p.Correct {
			n++
		}
	}
	return n
}

// Calibrate scans temperatures and returns the one minimizing expected
// calibration error (temperature scaling). Deterministic: same points → same
// temperature, so benchmark runs are reproducible.
func Calibrate(points []CalibrationPoint) CalibrationReport {
	rep := CalibrationReport{
		Temperature: DefaultTemperature,
		DefaultECE:  ece(points, DefaultTemperature),
		Points:      len(points),
	}
	for _, p := range points {
		if p.Correct {
			rep.Correct++
		}
	}
	if len(points) > 0 {
		rep.Accuracy = float64(rep.Correct) / float64(len(points))
	}
	bestT, bestNLL := fitTemperature(points)
	rep.Temperature = bestT
	rep.NLL = bestNLL
	rep.DefaultNLL = negLogLik(points, DefaultTemperature)
	// ECE is measured at the fitted temperature, not minimised by it. It can
	// come out slightly worse than the default; that is a diagnostic, not a
	// failure, and the adoption gate does not read it.
	rep.ECE = ece(points, bestT)
	return rep
}

// CalibrateLOO cross-validates the temperature fit leave-one-out and decides
// whether the result is fit to adopt. Adoption is refused — with a reason —
// for too few points, a suite with no failures, a boundary fit, or a fit that
// fails to generalize. See adoptionRefusal.
func CalibrateLOO(points []CalibrationPoint) LOOReport {
	rep := LOOReport{
		DefaultECE:   ece(points, DefaultTemperature),
		FittedECE:    ece(points, DefaultTemperature),
		LOOECE:       ece(points, DefaultTemperature),
		DefaultNLL:   negLogLik(points, DefaultTemperature),
		LOONLL:       negLogLik(points, DefaultTemperature),
		DefaultBrier: brierScore(points, DefaultTemperature),
		LOOBrier:     brierScore(points, DefaultTemperature),
		Temperature:  DefaultTemperature,
		Points:       len(points),
		Incorrect:    countIncorrect(points),
	}
	if len(points) < 3 {
		rep.RefusalReason = adoptionRefusal(rep)
		return rep // too few points to cross-validate meaningfully
	}
	bestT, _ := fitTemperature(points)
	rep.Temperature = bestT
	rep.FittedECE = ece(points, bestT)
	rep.Degenerate = atScanBoundary(points, bestT)
	rep.GridLow, rep.GridHigh, _ = scanGrid(points)

	// Out-of-sample: each point scored at the temperature fitted on the other
	// N−1 points. NLL and Brier drive the decision; ECE rides along for the
	// report because it is the number a human can interpret.
	rep.LOONLL = looProper(points, negLogLik)
	rep.LOOBrier = looProper(points, brierScore)
	rep.LOOECE = eceLOO(points)

	rep.RefusalReason = adoptionRefusal(rep)
	rep.Adopt = rep.RefusalReason == ""
	return rep
}

// eceLOO computes ECE over out-of-sample confidences: each point scored at
// the temperature fitted on the other N−1 points. Reporting only — see
// LOOReport.LOOECE for why this number must not gate adoption.
func eceLOO(points []CalibrationPoint) float64 {
	const bins = 10
	var counts [bins]int
	var conf [bins]float64
	var acc [bins]float64
	for i := range points {
		fold := make([]CalibrationPoint, 0, len(points)-1)
		for j := range points {
			if j != i {
				fold = append(fold, points[j])
			}
		}
		foldT, _ := fitTemperature(fold)
		s := Sigmoid(points[i].Margin, foldT)
		bin := int(s * bins)
		if bin >= bins {
			bin = bins - 1
		}
		counts[bin]++
		conf[bin] += s
		if points[i].Correct {
			acc[bin]++
		}
	}
	total := float64(len(points))
	if total == 0 {
		return 0
	}
	var out float64
	for b := 0; b < bins; b++ {
		if counts[b] == 0 {
			continue
		}
		out += (float64(counts[b]) / total) * math.Abs(conf[b]/float64(counts[b])-acc[b]/float64(counts[b]))
	}
	return out
}

// Dampen implements the calibration rule: when the calibration ECE exceeds
// 0.1, displayed confidence is lowered toward the conservative 50% default
// proportionally to how far ECE is past the threshold. Well-calibrated
// (ECE ≤ 0.1) output passes through unchanged.
func Dampen(confidence, ece float64) float64 {
	if ece <= maxCalibrationECE {
		return confidence
	}
	return 0.5 + (confidence-0.5)*(maxCalibrationECE/ece)
}
