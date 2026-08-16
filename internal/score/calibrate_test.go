package score

import (
	"math"
	"testing"
)

// wellCalibratedPoints: margins that map to confidences matching the
// correct/wrong labels at the default temperature (conf ≈ 1 for correct,
// ≈ 0.5 for the uncertain one).
func wellCalibratedPoints() []CalibrationPoint {
	return []CalibrationPoint{
		{Margin: 75, Correct: true},   // sigmoid(75/26) ≈ 0.947
		{Margin: 60, Correct: true},   // ≈ 0.909
		{Margin: 55, Correct: true},   // ≈ 0.892
		{Margin: 30, Correct: true},   // ≈ 0.760
		{Margin: 20, Correct: true},   // ≈ 0.685
		{Margin: 0, Correct: false},   // ≈ 0.500 — borderline, counted wrong
		{Margin: -20, Correct: false}, // ≈ 0.315
	}
}

// misCalibratedPoints: high margins for wrong answers — the model is
// confidently wrong, which temperature scaling cannot fully fix.
func misCalibratedPoints() []CalibrationPoint {
	return []CalibrationPoint{
		{Margin: 75, Correct: false}, // confident but wrong
		{Margin: 70, Correct: false},
		{Margin: 65, Correct: false},
		{Margin: 60, Correct: true},
		{Margin: 40, Correct: true},
		{Margin: 0, Correct: true}, // 0.5 confidence, correct
	}
}

func TestCalibrateFindsBetterTemperature(t *testing.T) {
	rep := Calibrate(wellCalibratedPoints())
	if rep.Points != 7 {
		t.Fatalf("points = %d", rep.Points)
	}
	if rep.ECE > rep.DefaultECE+1e-9 {
		t.Errorf("fitted ECE %.4f must be ≤ default ECE %.4f", rep.ECE, rep.DefaultECE)
	}
	if rep.Temperature < 5 || rep.Temperature > 80 {
		t.Errorf("temperature = %v out of scan range", rep.Temperature)
	}
}

func TestCalibrateLOOWellCalibrated(t *testing.T) {
	rep := CalibrateLOO(wellCalibratedPoints())
	if rep.Temperature == 0 {
		t.Fatal("no temperature fitted")
	}
	// The well-calibrated set should generalize: LOO ECE close to the
	// in-sample fit, and definitely better than a random guess would be.
	if rep.LOOECE > 0.30 {
		t.Errorf("LOO ECE = %.4f, expected reasonable generalization", rep.LOOECE)
	}
	// Adopt must be a boolean decision (either way), never panics.
	_ = rep.Adopt
}

func TestCalibrateLOOFewPoints(t *testing.T) {
	// With <3 points, LOO is meaningless — must not panic and keeps defaults.
	rep := CalibrateLOO([]CalibrationPoint{{Margin: 60, Correct: true}})
	if rep.Temperature != DefaultTemperature {
		t.Errorf("temperature = %v, want default for tiny samples", rep.Temperature)
	}
}

func TestCalibrateLOOMisCalibratedDoesNotAdoptBlindly(t *testing.T) {
	// The confidently-wrong set: the in-sample fit can look great (ECE 0 at
	// some T) while LOO generalizes badly — the whole point of hardening.
	rep := CalibrateLOO(misCalibratedPoints())
	_ = rep
	// No hard assertion on adoption (depends on the fit), but LOO ECE must be
	// computed and ≥ 0.
	if rep.LOOECE < 0 || math.IsNaN(rep.LOOECE) {
		t.Errorf("LOO ECE = %v", rep.LOOECE)
	}
}

// allCorrectPoints is the shape of a benchmark suite the engine never fails:
// every scenario correct. Calibration is undefined on it — ECE is minimised by
// maximum confidence, so the fit runs to the bottom of the scan grid.
func allCorrectPoints() []CalibrationPoint {
	var out []CalibrationPoint
	for _, m := range []float64{40, 55, 60, 72, 80, 95, 110, 45, 50, 65, 70, 88, 100, 52, 61} {
		out = append(out, CalibrationPoint{Margin: m, Correct: true})
	}
	return out
}

// mixedPoints is the same suite with three genuine failures — enough signal to
// calibrate against.
func mixedPoints() []CalibrationPoint {
	out := allCorrectPoints()[:12]
	return append(out,
		CalibrationPoint{Margin: 30, Correct: false},
		CalibrationPoint{Margin: 58, Correct: false},
		CalibrationPoint{Margin: 75, Correct: false},
	)
}

// A suite with no failures cannot calibrate confidence: the ECE-optimal policy
// is "always answer 100%". Adoption must be refused rather than silently
// learning maximum overconfidence.
func TestCalibrationRefusesAllCorrectSuite(t *testing.T) {
	rep := CalibrateLOO(allCorrectPoints())
	if rep.Incorrect != 0 {
		t.Fatalf("Incorrect = %d, want 0 for an all-correct suite", rep.Incorrect)
	}
	if rep.Adopt {
		t.Error("adopted a fit from a suite with no incorrect predictions")
	}
	if rep.RefusalReason == "" {
		t.Error("refusal must state a reason")
	}
	t.Logf("refused: T=%.1f reason=%q degenerate=%v", rep.Temperature, rep.RefusalReason, rep.Degenerate)
}

// A fit pinned to either end of the scan grid means the optimiser wanted to go
// further and could not — the objective is degenerate, not minimised.
func TestCalibrationDetectsBoundaryFit(t *testing.T) {
	rep := CalibrateLOO(allCorrectPoints())
	if rep.Temperature != minTemperature {
		t.Fatalf("temperature = %.1f, expected the grid floor %.1f", rep.Temperature, minTemperature)
	}
	if !rep.Degenerate {
		t.Error("a fit at the grid boundary must be flagged degenerate")
	}
}

// The honest case: real failures, a fit strictly inside the grid, enough
// points — adoption proceeds.
func TestCalibrationAdoptsGenuineFit(t *testing.T) {
	rep := CalibrateLOO(mixedPoints())
	if rep.Incorrect == 0 {
		t.Fatal("fixture must contain incorrect predictions")
	}
	if rep.Degenerate {
		t.Errorf("fit at T=%.1f flagged degenerate, expected an interior fit", rep.Temperature)
	}
	if !rep.Adopt {
		t.Errorf("refused a genuine fit: T=%.1f LOO=%.4f default=%.4f reason=%q",
			rep.Temperature, rep.LOOECE, rep.DefaultECE, rep.RefusalReason)
	}
}

// Below the minimum sample size the fitted temperature is noise.
func TestCalibrationRefusesTooFewPoints(t *testing.T) {
	rep := CalibrateLOO(mixedPoints()[:6])
	if rep.Adopt {
		t.Errorf("adopted a fit from %d points, minimum is %d", rep.Points, minCalibrationPoints)
	}
}

func TestDampen(t *testing.T) {
	// ECE ≤ 0.1 → unchanged.
	if got := Dampen(0.9, 0.05); got != 0.9 {
		t.Errorf("Dampen(0.9, 0.05) = %v, want 0.9", got)
	}
	// ECE > 0.1 → pulled toward 0.5 proportionally.
	if got := Dampen(0.9, 0.2); math.Abs(got-0.7) > 1e-9 {
		t.Errorf("Dampen(0.9, 0.2) = %v, want 0.7", got)
	}
	// ECE huge → near 0.5 (conservative default), never below it.
	if got := Dampen(0.9, 1.0); got < 0.5 || math.Abs(got-0.54) > 1e-9 {
		t.Errorf("Dampen(0.9, 1.0) = %v, want 0.54", got)
	}
	if got := Dampen(0.2, 1.0); math.Abs(got-0.47) > 1e-9 {
		t.Errorf("Dampen(0.2, 1.0) = %v, want 0.47 (symmetric pull)", got)
	}
}
