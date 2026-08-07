package score

import (
	"math"
	"testing"
)

// syntheticPoints generates deterministic pseudo-random points where
// correctness follows sigmoid(margin / T_true): a well-calibrated engine.
// T_true = 13.
func syntheticPoints(n int, seed int64) []CalibrationPoint {
	var state int64 = seed
	next := func() float64 {
		// Linear congruential generator, deterministic across runs.
		state = (state*6364136223846793005 + 1442695040888963407) & 0x7fffffffffffffff
		return float64(state) / float64(0x7fffffffffffffff)
	}
	points := make([]CalibrationPoint, 0, n)
	for i := 0; i < n; i++ {
		margin := -60 + 120*next()
		p := next()
		points = append(points, CalibrationPoint{Margin: margin, Correct: p < Sigmoid(margin, 13.0)})
	}
	return points
}

func TestCalibrateRecoversTemperature(t *testing.T) {
	points := syntheticPoints(400, 42)

	rep := Calibrate(points)
	if rep.Points != 400 {
		t.Fatalf("points = %d, want 400", rep.Points)
	}
	// The fitted temperature should land near the true T=13, and the ECE at
	// the fitted T must be at or below the default T=26.
	if rep.Temperature < 8 || rep.Temperature > 20 {
		t.Errorf("fitted temperature = %.1f, want near 13", rep.Temperature)
	}
	if rep.ECE > rep.DefaultECE+1e-9 {
		t.Errorf("fitted ECE = %.4f > default ECE %.4f — fitting must not hurt", rep.ECE, rep.DefaultECE)
	}
	// Well-calibrated synthetic data → low ECE after fitting.
	if rep.ECE > 0.05 {
		t.Errorf("ECE = %.4f, want ≤ 0.05 on calibrated synthetic data", rep.ECE)
	}
}

func TestCalibrateEmptyAndTrivial(t *testing.T) {
	rep := Calibrate(nil)
	if rep.Points != 0 || rep.Accuracy != 0 {
		t.Fatalf("empty calibration = %+v", rep)
	}
	// Margin 0 → score 0.5 at every temperature → no signal to fit; the
	// default temperature is kept and ECE is the same everywhere.
	rep = Calibrate([]CalibrationPoint{{Margin: 0, Correct: true}})
	if rep.Temperature != DefaultTemperature {
		t.Errorf("temperature = %.1f, want default (no signal to fit)", rep.Temperature)
	}
}

func TestECEMonotonicUnderCompression(t *testing.T) {
	// Perfectly calibrated at T=26: score == accuracy exactly → ECE ≈ 0
	// (up to binomial sampling noise; 500 points → ~50 per bin).
	points := make([]CalibrationPoint, 0, 500)
	for i := 0; i < 500; i++ {
		margin := -50 + float64(i)/5 // covers the full score range
		s := Sigmoid(margin, DefaultTemperature)
		points = append(points, CalibrationPoint{Margin: margin, Correct: randBool(i, s)})
	}
	if e := ece(points, DefaultTemperature); e > 0.12 {
		t.Errorf("ECE at calibrated T = %.3f, want ≈ 0 (≤0.12 incl. sampling noise)", e)
	}
	// Compressing scores toward 0.5 (huge T) must inflate ECE.
	if e := ece(points, 1000); e <= ece(points, DefaultTemperature) {
		t.Error("compressed scores must increase ECE")
	}
}

// randBool returns true with probability p, deterministically.
func randBool(i int, p float64) bool {
	x := math.Sin(float64(i)*12.9898)*43758.5453 - math.Floor(math.Sin(float64(i)*12.9898)*43758.5453)
	return x < p
}
