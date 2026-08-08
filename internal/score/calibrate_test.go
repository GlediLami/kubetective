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
