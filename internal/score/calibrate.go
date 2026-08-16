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
	Temperature float64 // fitted T minimizing ECE
	ECE         float64 // ECE at the fitted temperature
	DefaultECE  float64 // ECE at DefaultTemperature (before fitting)
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
	LOOECE      float64 // out-of-sample ECE
	Points      int     // ground-truth points the fit saw
	Incorrect   int     // how many of them the engine got wrong
	// Degenerate reports a fit pinned to an end of the scan grid: the
	// optimiser wanted to go further and could not, which means the objective
	// was not minimised but run off its edge.
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
const (
	minTemperature  = 5.0
	maxTemperature  = 80.0
	temperatureStep = 0.5
)

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
	case rep.LOOECE >= rep.DefaultECE:
		return fmt.Sprintf("out-of-sample ECE %.1f%% does not beat the default %.1f%%", rep.LOOECE*100, rep.DefaultECE*100)
	}
	return ""
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

// fitTemperature scans T and returns the one minimizing ECE plus its ECE.
// Deterministic: same points → same temperature.
func fitTemperature(points []CalibrationPoint) (bestT, bestECE float64) {
	bestT = DefaultTemperature
	bestECE = ece(points, DefaultTemperature)
	for t := minTemperature; t <= maxTemperature; t += temperatureStep {
		if e := ece(points, t); e < bestECE {
			bestECE = e
			bestT = t
		}
	}
	return bestT, bestECE
}

// atScanBoundary reports whether a fitted temperature landed on an end of the
// scan grid — the signature of an objective that was never minimised.
func atScanBoundary(t float64) bool {
	return t <= minTemperature || t >= maxTemperature
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
	rep.ECE = rep.DefaultECE
	bestT, bestECE := fitTemperature(points)
	rep.Temperature = bestT
	rep.ECE = bestECE
	return rep
}

// CalibrateLOO cross-validates the temperature fit leave-one-out and decides
// whether the result is fit to adopt. Adoption is refused — with a reason —
// for too few points, a suite with no failures, a boundary fit, or a fit that
// fails to generalize. See adoptionRefusal.
func CalibrateLOO(points []CalibrationPoint) LOOReport {
	rep := LOOReport{
		DefaultECE:  ece(points, DefaultTemperature),
		FittedECE:   ece(points, DefaultTemperature),
		LOOECE:      ece(points, DefaultTemperature),
		Temperature: DefaultTemperature,
		Points:      len(points),
		Incorrect:   countIncorrect(points),
	}
	if len(points) < 3 {
		rep.RefusalReason = adoptionRefusal(rep)
		return rep // too few points to cross-validate meaningfully
	}
	bestT, bestECE := fitTemperature(points)
	rep.Temperature = bestT
	rep.FittedECE = bestECE
	rep.Degenerate = atScanBoundary(bestT)

	// Out-of-sample ECE: each point scored at the temperature fitted on the
	// other N−1 points (see eceLOO).
	rep.LOOECE = eceLOO(points)
	rep.RefusalReason = adoptionRefusal(rep)
	rep.Adopt = rep.RefusalReason == ""
	return rep
}

// eceLOO computes ECE over out-of-sample confidences: each point scored at
// the temperature fitted on the other N−1 points.
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
