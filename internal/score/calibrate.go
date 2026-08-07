package score

import (
	"math"
)

// CalibrationPoint is one benchmark verdict: the margin of the top hypothesis
// and whether it matched ground truth. Calibration turns margins into
// calibrated confidence (docs/DESIGN.md §9.4).
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

	best := rep.DefaultECE
	for t := 5.0; t <= 80; t += 0.5 {
		e := ece(points, t)
		if e < best {
			best = e
			rep.Temperature = t
			rep.ECE = e
		}
	}
	return rep
}
