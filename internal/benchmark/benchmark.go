// Package benchmark implements the scenario benchmark gate: each scenario
// (scenarios/<name>/) carries a ground-truth spec (scenario.yaml) and a
// recorded investigation (record.jsonl). The gate replays the record through
// the real engine and asserts the diagnosis — the contribution rule for new
// analyzers (docs/DESIGN.md §11, §17.2).
package benchmark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/record"
	"github.com/kubedoctor/kubedoctor/internal/score"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// Scenario is the ground-truth spec of one benchmark scenario.
type Scenario struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	GroundTruth GroundTruth `yaml:"ground_truth"`
}

type GroundTruth struct {
	RootCause                string   `yaml:"root_cause"`
	TopHypothesisCategory    string   `yaml:"top_hypothesis_category"`
	MinScore                 float64  `yaml:"min_score"`
	ExpectedFindingAnalyzers []string `yaml:"expected_finding_analyzers"`
	ExpectedStatus           string   `yaml:"expected_status,omitempty"`
	// ExpectNoFindings asserts the engine stays silent (false-positive gate).
	ExpectNoFindings bool `yaml:"expect_no_findings,omitempty"`
}

// Result is one scenario's verdict with the reasons behind it.
type Result struct {
	Scenario      string
	Passed        bool
	Reasons       []string
	Duration      time.Duration
	TopHypothesis string
	Score         float64
	Status        string
	// Margin and Correct feed confidence calibration (docs/DESIGN.md §9.4):
	// did the top hypothesis match ground truth, and at what margin?
	Margin           float64
	Correct          bool
	HasTruth         bool
	ExpectedCategory string // ground-truth top hypothesis category (per-category accuracy)
	// False-positive gate bookkeeping: did this scenario expect silence, and
	// did the engine produce findings anyway?
	ExpectNoFindings bool
	FoundFindings    bool
}

func (r *Result) Fail(format string, args ...any) {
	r.Passed = false
	r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
}

// LoadScenario reads a scenario.yaml.
func LoadScenario(path string) (*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sc Scenario
	if err := yaml.Unmarshal(b, &sc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if sc.Name == "" {
		return nil, fmt.Errorf("%s: scenario missing name", path)
	}
	return &sc, nil
}

// RunScenario replays recorded observations through the engine and asserts
// the ground truth. The caller wires the engine with a ReplayCollector for
// the scenario's observations (no live collectors).
func RunScenario(ctx context.Context, sc *Scenario, eng api.Investigator) (*Result, error) {
	started := time.Now()
	res := &Result{Scenario: sc.Name, Passed: true}

	req := &api.InvestigationRequest{Target: model.ResourceRef{Kind: "pod", Name: "scenario"}}
	out, err := eng.Investigate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: investigate: %w", sc.Name, err)
	}
	res.Duration = time.Since(started)
	res.Status = ""
	if out.Incident != nil {
		res.Status = out.Incident.Status
	}

	gt := sc.GroundTruth
	top := topHypothesis(out)

	if gt.TopHypothesisCategory != "" || gt.MinScore > 0 {
		if top == nil {
			res.Fail("no hypothesis generated")
		} else {
			res.TopHypothesis = top.Claim
			res.Score = top.Score.Score
			if gt.TopHypothesisCategory != "" {
				res.HasTruth = true
				res.Margin = top.Score.Margin
				res.Correct = string(top.Category) == gt.TopHypothesisCategory
				res.ExpectedCategory = gt.TopHypothesisCategory
			}
			if gt.TopHypothesisCategory != "" && string(top.Category) != gt.TopHypothesisCategory {
				res.Fail("top hypothesis category = %s, want %s", top.Category, gt.TopHypothesisCategory)
			}
			if gt.MinScore > 0 && top.Score.Score < gt.MinScore {
				res.Fail("top hypothesis score = %.3f, want ≥ %.3f", top.Score.Score, gt.MinScore)
			}
		}
	}
	if gt.ExpectedStatus != "" && res.Status != gt.ExpectedStatus {
		res.Fail("incident status = %s, want %s", res.Status, gt.ExpectedStatus)
	}
	for _, want := range gt.ExpectedFindingAnalyzers {
		if !hasAnalyzer(out.Findings, want) {
			res.Fail("missing finding from analyzer %q", want)
		}
	}
	if gt.ExpectNoFindings {
		res.ExpectNoFindings = true
		res.FoundFindings = len(out.Findings) > 0 || len(out.Hypotheses) > 0
		if len(out.Findings) != 0 {
			res.Fail("expected no findings, got %d (false positive): %v", len(out.Findings), findingTitles(out.Findings))
		}
		if len(out.Hypotheses) != 0 {
			res.Fail("expected no hypotheses, got %d (false positive)", len(out.Hypotheses))
		}
	}
	return res, nil
}

func findingTitles(fs []model.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Analyzer+":"+f.Title)
	}
	return out
}

func topHypothesis(out *api.InvestigationResult) *model.Hypothesis {
	var top *model.Hypothesis
	for i := range out.Hypotheses {
		h := &out.Hypotheses[i]
		if h.Score == nil {
			continue
		}
		if top == nil || h.Score.Score > top.Score.Score {
			top = h
		}
	}
	return top
}

func hasAnalyzer(fs []model.Finding, id string) bool {
	for _, f := range fs {
		if f.Analyzer == id {
			return true
		}
	}
	return false
}

// SuiteResult bundles per-scenario results with the confidence calibration
// computed across all scenarios that carry ground truth.
type SuiteResult struct {
	Results     []Result
	Calibration *score.CalibrationReport // nil when no scenario carries truth
	LOO         *score.LOOReport         // leave-one-out validation of the fit (v0.7)
}

// CategoryAccuracy is the top-1 category accuracy per ground-truth category.
type CategoryAccuracy struct {
	Category  string
	Correct   int
	Total     int
	Accuracy  float64
}

// EngineFactory builds an engine with the given collectors wired (plus the
// analyzer set). Used by the suite to give each scenario its own replay data.
type EngineFactory func(collectors ...collect.Collector) api.Investigator

// RunSuite replays every scenario under scenariosDir and returns per-scenario
// results in name order, plus the calibration report.
func RunSuite(ctx context.Context, scenariosDir string, factory EngineFactory) (*SuiteResult, error) {
	entries, err := os.ReadDir(scenariosDir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	out := &SuiteResult{}
	for _, d := range dirs {
		dir := filepath.Join(scenariosDir, d)
		sc, err := LoadScenario(filepath.Join(dir, "scenario.yaml"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		inc, err := record.NewStore(dir).Load("record")
		if err != nil {
			return nil, fmt.Errorf("%s: load record.jsonl: %w", d, err)
		}
		eng := factory(record.NewReplayCollector(inc.Observations))
		r, err := RunScenario(ctx, sc, eng)
		if err != nil {
			return nil, err
		}
		out.Results = append(out.Results, *r)
	}

	// Calibration across all scenarios with ground truth (temperature fit).
	var points []score.CalibrationPoint
	for _, r := range out.Results {
		if r.HasTruth {
			points = append(points, score.CalibrationPoint{Margin: r.Margin, Correct: r.Correct})
		}
	}
	if len(points) > 0 {
		rep := score.Calibrate(points)
		out.Calibration = &rep
		loo := score.CalibrateLOO(points)
		out.LOO = &loo
	}
	return out, nil
}

// ByCategory groups truth-carrying results by their expected category.
func (s *SuiteResult) ByCategory() []CategoryAccuracy {
	groups := map[string]*CategoryAccuracy{}
	var order []string
	for _, r := range s.Results {
		if !r.HasTruth || r.ExpectedCategory == "" {
			continue
		}
		if groups[r.ExpectedCategory] == nil {
			groups[r.ExpectedCategory] = &CategoryAccuracy{Category: r.ExpectedCategory}
			order = append(order, r.ExpectedCategory)
		}
		g := groups[r.ExpectedCategory]
		g.Total++
		if r.Correct {
			g.Correct++
		}
	}
	sort.Strings(order)
	out := make([]CategoryAccuracy, 0, len(order))
	for _, c := range order {
		g := groups[c]
		if g.Total > 0 {
			g.Accuracy = float64(g.Correct) / float64(g.Total)
		}
		out = append(out, *g)
	}
	return out
}

// FalsePositiveCount counts scenarios that were expected to stay silent but
// produced findings (the healthy control gate).
func (s *SuiteResult) FalsePositiveCount() int {
	n := 0
	for _, r := range s.Results {
		if r.ExpectNoFindings && r.FoundFindings {
			n++
		}
	}
	return n
}
