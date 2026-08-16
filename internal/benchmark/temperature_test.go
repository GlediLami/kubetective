package benchmark

import (
	"context"
	"testing"

	"github.com/GlediLami/kubetective/internal/score"
)

// The benchmark produces the calibrated temperature, so it must not be graded
// at it. When it was, adopting a fit rescaled every displayed score while the
// `min_score` thresholds in scenario.yaml stayed where they were — and the
// suite failed on its own success: the first temperature this suite ever
// adopted (T=54) broke 14 of 25 scenarios on the next run, every one of them
// for "score below min_score" and none because a diagnosis had changed.
//
// Ground truth is a property of the incident. It must not move when the
// presentation layer is recalibrated.
func TestSuiteGradesAtFixedTemperatureRegardlessOfOperatingT(t *testing.T) {
	baseline, err := RunSuite(context.Background(), scenariosPath, contractFactory)
	if err != nil {
		t.Fatalf("baseline suite: %v", err)
	}

	// Simulate an adopted calibration well away from the default.
	restore := score.PinTemperature(score.DefaultTemperature * 2.5)
	got, err := RunSuite(context.Background(), scenariosPath, contractFactory)
	restore()
	if err != nil {
		t.Fatalf("suite under adopted temperature: %v", err)
	}

	if len(got.Results) != len(baseline.Results) {
		t.Fatalf("scenario count changed: %d → %d", len(baseline.Results), len(got.Results))
	}
	for i := range baseline.Results {
		b, g := baseline.Results[i], got.Results[i]
		if b.Scenario != g.Scenario {
			t.Fatalf("scenario order changed at %d: %s vs %s", i, b.Scenario, g.Scenario)
		}
		if b.Passed != g.Passed {
			t.Errorf("%s: pass/fail moved with the operating temperature (%v → %v): %v",
				b.Scenario, b.Passed, g.Passed, g.Reasons)
		}
		if b.Score != g.Score {
			t.Errorf("%s: score moved with the operating temperature: %.4f → %.4f",
				b.Scenario, b.Score, g.Score)
		}
		if b.Margin != g.Margin {
			t.Errorf("%s: margin moved with the operating temperature: %.4f → %.4f",
				b.Scenario, b.Margin, g.Margin)
		}
	}
}

// RunSuite pins the temperature for its own duration; it must hand back
// whatever the process was using, or a benchmark run would silently reset a
// calibration the operator had adopted.
func TestRunSuiteRestoresOperatingTemperature(t *testing.T) {
	const adopted = 61.5
	restore := score.PinTemperature(adopted)
	defer restore()

	if _, err := RunSuite(context.Background(), scenariosPath, contractFactory); err != nil {
		t.Fatalf("suite: %v", err)
	}
	if score.CurrentTemperature != adopted {
		t.Errorf("operating temperature left at %.2f, want %.2f restored",
			score.CurrentTemperature, adopted)
	}
}
