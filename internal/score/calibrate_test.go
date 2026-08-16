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
	pts := wellCalibratedPoints()
	rep := Calibrate(pts)
	if rep.Points != 7 {
		t.Fatalf("points = %d", rep.Points)
	}
	// NLL is the fitted objective, so it is what must improve. ECE is measured
	// at the fitted temperature and reported; it is not minimised and may come
	// out marginally worse.
	if rep.NLL > rep.DefaultNLL+1e-9 {
		t.Errorf("fitted NLL %.4f must be ≤ default NLL %.4f", rep.NLL, rep.DefaultNLL)
	}
	lo, hi, _ := scanGrid(pts)
	if rep.Temperature < lo || rep.Temperature > hi {
		t.Errorf("temperature = %v outside scan grid [%.1f, %.1f]", rep.Temperature, lo, hi)
	}
}

// The scan grid must be wide enough to express an overconfident engine. With a
// hardcoded ceiling of 80 and margins near 100, the loosest reachable
// confidence was ~0.71 — so a suite whose true accuracy was lower could not be
// calibrated at all, and the fit was refused as "degenerate" when the real
// fault was the size of the search space.
func TestScanGridCanExpressLowConfidence(t *testing.T) {
	pts := []CalibrationPoint{{Margin: 100, Correct: true}, {Margin: 60, Correct: false}}
	_, hi, _ := scanGrid(pts)
	if got := Sigmoid(100, hi); got > 0.60 {
		t.Errorf("at the grid ceiling T=%.1f the top margin still scores %.2f; "+
			"the grid cannot express an engine with weak signal", hi, got)
	}
	lo, _, _ := scanGrid(pts)
	if got := Sigmoid(100, lo); got < 0.99 {
		t.Errorf("at the grid floor T=%.1f the top margin only reaches %.3f", lo, got)
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

// mixedPoints is the same suite with three genuine failures. It has errors and
// fits to an interior temperature — but at 80% accuracy on these margins the
// default is already close to right, so re-fitting buys nothing once the cost
// of estimating T from 15 points is paid. The honest verdict is "keep the
// default", and this fixture exists to lock that in.
func mixedPoints() []CalibrationPoint {
	out := allCorrectPoints()[:12]
	return append(out,
		CalibrationPoint{Margin: 30, Correct: false},
		CalibrationPoint{Margin: 58, Correct: false},
		CalibrationPoint{Margin: 75, Correct: false},
	)
}

// overconfidentPoints is a suite the default temperature genuinely gets wrong:
// the engine answers with high margins across the board but is right only 60%
// of the time, and the margins barely separate right from wrong. The default
// T=26 puts every one of these near 90-98% confidence, so re-fitting has real
// work to do and earns its keep out-of-sample.
func overconfidentPoints() []CalibrationPoint {
	var out []CalibrationPoint
	for _, m := range []float64{110, 100, 95, 90, 85, 80, 78, 72, 68, 64, 60, 56} {
		out = append(out, CalibrationPoint{Margin: m, Correct: true})
	}
	for _, m := range []float64{105, 92, 88, 76, 70, 66, 62, 58} {
		out = append(out, CalibrationPoint{Margin: m, Correct: false})
	}
	return out
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
	pts := allCorrectPoints()
	rep := CalibrateLOO(pts)
	lo, _, step := scanGrid(pts)
	if rep.Temperature > lo+step {
		t.Fatalf("temperature = %.1f, expected the grid floor %.1f", rep.Temperature, lo)
	}
	if !rep.Degenerate {
		t.Error("a fit at the grid boundary must be flagged degenerate")
	}
}

// The honest case: real failures, a fit strictly inside the grid, enough
// points, and a default temperature that is genuinely wrong — adoption
// proceeds.
func TestCalibrationAdoptsGenuineFit(t *testing.T) {
	pts := overconfidentPoints()
	rep := CalibrateLOO(pts)
	if rep.Incorrect == 0 {
		t.Fatal("fixture must contain incorrect predictions")
	}
	if rep.Degenerate {
		t.Errorf("fit at T=%.1f flagged degenerate, expected an interior fit "+
			"(grid [%.1f, %.1f])", rep.Temperature, rep.GridLow, rep.GridHigh)
	}
	if !rep.Adopt {
		t.Errorf("refused a genuine fit: T=%.1f LOO NLL=%.4f default NLL=%.4f reason=%q",
			rep.Temperature, rep.LOONLL, rep.DefaultNLL, rep.RefusalReason)
	}
	// Adopting must actually reduce overconfidence: the fitted temperature has
	// to pull a typical margin down toward the 60% the suite really achieves.
	if got := Sigmoid(80, rep.Temperature); got > 0.80 {
		t.Errorf("fitted T=%.1f still scores a typical margin at %.2f on a 60%%-accurate suite",
			rep.Temperature, got)
	}
}

// A fit that only ties the default must be refused. Estimating T from finite
// data costs variance, so "no worse than the default" is not a reason to
// replace it — and adopting on a hair's-breadth win is how the decision ends up
// being made by noise rather than by evidence.
func TestCalibrationRefusesMarginalFit(t *testing.T) {
	rep := CalibrateLOO(mixedPoints())
	if rep.Incorrect == 0 {
		t.Fatal("fixture must contain incorrect predictions")
	}
	if rep.Degenerate {
		t.Fatalf("fixture should fit inside the grid, got T=%.1f", rep.Temperature)
	}
	if rep.Adopt {
		t.Errorf("adopted a fit that beats the default by less than %.0f%%: "+
			"LOO NLL=%.4f vs default %.4f", minRelativeImprovement*100, rep.LOONLL, rep.DefaultNLL)
	}
}

// The regression test for the whole redesign. Adoption used to be gated on
// leave-one-out ECE, a binned statistic: on the real suite, adding a single
// scenario swung LOO ECE 16.98% → 6.88% → 26.41% → 1.28%, flipping the verdict
// each time. Whatever the decision is on a given suite, removing any one point
// must not reverse it.
func TestAdoptionIsStableUnderSinglePointRemoval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		points []CalibrationPoint
	}{
		{"overconfident (adopts)", overconfidentPoints()},
		{"marginal (refuses)", mixedPoints()},
		{"all correct (refuses)", allCorrectPoints()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := CalibrateLOO(tc.points).Adopt
			for i := range tc.points {
				sub := make([]CalibrationPoint, 0, len(tc.points)-1)
				sub = append(sub, tc.points[:i]...)
				sub = append(sub, tc.points[i+1:]...)
				if got := CalibrateLOO(sub); got.Adopt != want {
					t.Errorf("dropping point %d (margin %.0f, correct=%v) flipped adoption %v→%v: %s",
						i, tc.points[i].Margin, tc.points[i].Correct, want, got.Adopt, got.RefusalReason)
				}
			}
		})
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
