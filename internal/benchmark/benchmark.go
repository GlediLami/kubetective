// Package benchmark implements the scenario benchmark gate: each scenario
// (scenarios/<name>/) carries a ground-truth spec (scenario.yaml) and a
// recorded investigation (record.jsonl). The gate replays the record through
// the real engine and asserts the diagnosis — the contribution rule for new
// analyzers.
package benchmark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/GlediLami/kubetective/internal/action"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/internal/score"
	"github.com/GlediLami/kubetective/pkg/api"
)

// Scenario is the ground-truth spec of one benchmark scenario.
type Scenario struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	GroundTruth GroundTruth `yaml:"ground_truth"`
	// Mutations are the causal claims this scenario makes: remove this
	// evidence and the verdict must move. See robustness.go.
	Mutations []Mutation `yaml:"mutations,omitempty"`
}

type GroundTruth struct {
	RootCause                string   `yaml:"root_cause"`
	TopHypothesisCategory    string   `yaml:"top_hypothesis_category"`
	MinScore                 float64  `yaml:"min_score"`
	ExpectedFindingAnalyzers []string `yaml:"expected_finding_analyzers"`
	ExpectedStatus           string   `yaml:"expected_status,omitempty"`
	// ExpectNoFindings asserts the engine stays silent (false-positive gate).
	ExpectNoFindings bool `yaml:"expect_no_findings,omitempty"`

	// --- the hard set ---

	// Advisory marks a scenario the engine is not required to get right. It
	// still contributes a calibration point, but its failure never breaks the
	// gate. This is what lets the suite carry genuinely under-determined
	// incidents: without them every prediction is correct, and a benchmark the
	// engine never fails cannot calibrate confidence at all
	// (see score.adoptionRefusal).
	Advisory bool `yaml:"advisory,omitempty"`
	// MaxScore caps the top hypothesis's confidence. On an ambiguous incident
	// the correct behaviour is not a right answer but a hedged one.
	MaxScore float64 `yaml:"max_score,omitempty"`
	// ExpectCompeting requires at least two live hypotheses on the target
	// resource — the engine must report "multiple plausible causes" rather
	// than ruling one out on evidence that cannot support the distinction.
	ExpectCompeting bool `yaml:"expect_competing,omitempty"`
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
	// Margin and Correct feed confidence calibration:
	// did the top hypothesis match ground truth, and at what margin?
	Margin           float64
	Correct          bool
	HasTruth         bool
	ExpectedCategory string // ground-truth top hypothesis category (per-category accuracy)
	// False-positive gate bookkeeping: did this scenario expect silence, and
	// did the engine produce findings anyway?
	ExpectNoFindings bool
	FoundFindings    bool
	// PlannedActions are the remediation actions the engine offers for this
	// scenario (action safety metric: healthy scenarios must plan none).
	PlannedActions []action.Action
	// Advisory marks a hard-set scenario: it reports and calibrates, but its
	// failure does not break the gate.
	Advisory bool
	// Competing is how many hypotheses stayed live on the target resource
	// (status Likely or Candidate — i.e. not ruled out).
	Competing int
	// TopCategory is the category the engine actually chose on the clean
	// replay — distinct from ExpectedCategory, which is what it should have
	// chosen. The noise gate compares against this one: stability means the
	// verdict does not move, not that it becomes correct.
	TopCategory string
}

// GateFailed reports whether this result should break CI. Advisory scenarios
// never do: they exist to measure the engine, not to constrain it.
func (r *Result) GateFailed() bool { return !r.Passed && !r.Advisory }

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
	res.Advisory = gt.Advisory
	res.Competing = countCompeting(out, top)
	if top != nil {
		res.TopCategory = string(top.Category)
	}

	if gt.MaxScore > 0 && top != nil && top.Score.Score > gt.MaxScore {
		res.Fail("top hypothesis score = %.3f, want ≤ %.3f — the evidence does not support this much confidence",
			top.Score.Score, gt.MaxScore)
	}
	if gt.ExpectCompeting && res.Competing < 2 {
		res.Fail("only %d live hypothesis on the target; an under-determined incident must leave competitors standing",
			res.Competing)
	}

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
	// Action safety: healthy scenarios must plan zero remediation actions
	// (roadmap v1.0: unsafe-action rate = 0 in the eval suite).
	res.PlannedActions = action.Plan(out)
	if gt.ExpectNoFindings && len(res.PlannedActions) > 0 {
		res.Fail("unsafe action planning: %d action(s) offered on a healthy scenario: %v",
			len(res.PlannedActions), plannedActionIDs(res.PlannedActions))
	}
	return res, nil
}

func plannedActionIDs(as []action.Action) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, string(a.Type))
	}
	return out
}

func findingTitles(fs []model.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Analyzer+":"+f.Title)
	}
	return out
}

// countCompeting counts hypotheses that stayed live on the top hypothesis's
// resource: ruled-out ones do not count, so this measures how much genuine
// ambiguity the engine is still reporting.
func countCompeting(out *api.InvestigationResult, top *model.Hypothesis) int {
	if top == nil {
		return 0
	}
	n := 0
	for i := range out.Hypotheses {
		h := &out.Hypotheses[i]
		if h.Status == model.StatusRuledOut {
			continue
		}
		if sameResourceID(h.ID, top.ID) {
			n++
		}
	}
	return n
}

// sameResourceID mirrors hypothesis.resourceOf: IDs are "<category>.<resource>".
func sameResourceID(a, b string) bool {
	return resourcePart(a) == resourcePart(b)
}

func resourcePart(id string) string {
	if i := strings.Index(id, "."); i >= 0 {
		return id[i+1:]
	}
	return id
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
	// Robustness holds the mutation and noise gates: whether the verdicts are
	// caused by the evidence, and whether they survive cluster-scale volume.
	Robustness *RobustnessReport
}

// CategoryAccuracy is the top-1 category accuracy per ground-truth category.
type CategoryAccuracy struct {
	Category string
	Correct  int
	Total    int
	Accuracy float64
}

// EngineFactory builds an engine with the given collectors wired (plus the
// analyzer set). Used by the suite to give each scenario its own replay data.
type EngineFactory func(collectors ...collect.Collector) api.Investigator

// RunSuite replays every scenario under scenariosDir and returns per-scenario
// results in name order, plus the calibration report.
//
// The whole suite is graded at score.DefaultTemperature, never at whatever
// temperature the engine is currently operating at. The benchmark is the
// instrument that *produces* the calibrated temperature, so grading it at that
// temperature closes a loop: adopting a fit rescales every displayed score, the
// `min_score` thresholds in scenario.yaml no longer mean what they meant when
// they were written, and the suite fails on its own success.
//
// That is not hypothetical. The first fit this suite ever adopted (T=54, after
// five live hard-set scenarios dropped measured accuracy from 89% to 71%) broke
// 14 of 25 scenarios on the very next run — every one of them for "score below
// min_score", none of them because anything about the diagnosis had changed.
// The bug was invisible for as long as it was, precisely because adoption had
// never once succeeded.
//
// Ground truth is a property of the incident, not of the presentation layer.
func RunSuite(ctx context.Context, scenariosDir string, factory EngineFactory) (*SuiteResult, error) {
	defer score.PinTemperature(score.DefaultTemperature)()

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

	out := &SuiteResult{Robustness: &RobustnessReport{
		Mutations: map[string][]MutationResult{},
		NoiseN:    noiseScenarioCount,
	}}
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

		// Robustness gates run against the same recorded evidence: mutation
		// (verdict must move when its support is removed) and noise (verdict
		// must hold when irrelevant volume is added).
		if err := runRobustness(ctx, sc, r, inc.Observations, factory, out.Robustness); err != nil {
			return nil, err
		}
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
		if r.ExpectNoFindings && (r.FoundFindings || len(r.PlannedActions) > 0) {
			n++
		}
	}
	return n
}

// UnsafeActionCount returns how many healthy scenarios had remediation
// actions planned (must stay 0; the evaluate gate enforces it).
func (s *SuiteResult) UnsafeActionCount() int {
	n := 0
	for _, r := range s.Results {
		if r.ExpectNoFindings {
			n += len(r.PlannedActions)
		}
	}
	return n
}
