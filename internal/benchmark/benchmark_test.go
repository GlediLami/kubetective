package benchmark

import (
	"context"
	"testing"

	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

// fakeInvestigator returns canned results — isolates the gate's assertion
// logic from the engine.
type fakeInvestigator struct {
	status      string
	findings    []model.Finding
	hypotheses  []model.Hypothesis
}

var _ api.Investigator = (*fakeInvestigator)(nil)

func (f *fakeInvestigator) Investigate(_ context.Context, _ *api.InvestigationRequest) (*api.InvestigationResult, error) {
	return &api.InvestigationResult{
		Incident:   &model.IncidentSummary{Status: f.status},
		Findings:   f.findings,
		Hypotheses: f.hypotheses,
	}, nil
}

func scored(category model.HypothesisCategory, score float64, margin float64) model.Hypothesis {
	return model.Hypothesis{
		ID:       "h1",
		Claim:    "test claim",
		Category: category,
		Score:    &model.ScoreBreakdown{Score: score, Margin: margin},
	}
}

func TestRunScenarioPass(t *testing.T) {
	sc := &Scenario{Name: "pass", GroundTruth: GroundTruth{
		TopHypothesisCategory:    "memory",
		MinScore:                 0.8,
		ExpectedFindingAnalyzers: []string{"oom"},
		ExpectedStatus:           "OOMKILLED",
	}}
	eng := &fakeInvestigator{
		status:   "OOMKILLED",
		findings: []model.Finding{{ID: "oom.p1", Analyzer: "oom"}},
		hypotheses: []model.Hypothesis{scored(model.CatMemory, 0.94, 72)},
	}
	res, err := RunScenario(context.Background(), sc, eng)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected pass, got failures: %v", res.Reasons)
	}
	if !res.HasTruth || !res.Correct || res.Margin != 72 {
		t.Errorf("calibration capture wrong: %+v", res)
	}
}

func TestRunScenarioFailsOnCategoryMismatch(t *testing.T) {
	sc := &Scenario{Name: "wrong-category", GroundTruth: GroundTruth{
		TopHypothesisCategory: "memory",
	}}
	eng := &fakeInvestigator{
		status:     "OOMKILLED",
		hypotheses: []model.Hypothesis{scored(model.CatImage, 0.94, 72)},
	}
	res, err := RunScenario(context.Background(), sc, eng)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if res.Passed {
		t.Fatal("expected fail on category mismatch")
	}
	if !res.HasTruth || res.Correct {
		t.Error("mismatched category must be captured as incorrect")
	}
	if len(res.Reasons) == 0 {
		t.Error("failures must carry reasons (explainability)")
	}
}

func TestRunScenarioFailsOnLowScore(t *testing.T) {
	sc := &Scenario{Name: "low-score", GroundTruth: GroundTruth{
		TopHypothesisCategory: "memory",
		MinScore:              0.9,
	}}
	eng := &fakeInvestigator{
		status:     "OOMKILLED",
		hypotheses: []model.Hypothesis{scored(model.CatMemory, 0.5, 0)},
	}
	res, err := RunScenario(context.Background(), sc, eng)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if res.Passed {
		t.Fatal("expected fail on min score")
	}
}

func TestRunScenarioFailsOnMissingAnalyzer(t *testing.T) {
	sc := &Scenario{Name: "missing-analyzer", GroundTruth: GroundTruth{
		ExpectedFindingAnalyzers: []string{"oom"},
	}}
	eng := &fakeInvestigator{status: "OOMKILLED"} // no findings at all
	res, err := RunScenario(context.Background(), sc, eng)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if res.Passed {
		t.Fatal("expected fail on missing finding")
	}
}

func TestRunScenarioExpectNoFindings(t *testing.T) {
	sc := &Scenario{Name: "healthy", GroundTruth: GroundTruth{
		ExpectNoFindings: true,
		ExpectedStatus:   "INVESTIGATED",
	}}
	// Healthy: silent engine → pass.
	if res, err := RunScenario(context.Background(), sc, &fakeInvestigator{status: "INVESTIGATED"}); err != nil || !res.Passed {
		t.Fatalf("healthy must pass: %v %v", res, err)
	}
	// False positive: findings present → fail with reasons naming them.
	eng := &fakeInvestigator{
		status:   "OOMKILLED",
		findings: []model.Finding{{ID: "oom.p1", Analyzer: "oom", Title: "OOMKilled"}},
	}
	res, err := RunScenario(context.Background(), sc, eng)
	if err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if res.Passed {
		t.Fatal("expected fail on false positive")
	}
	if len(res.Reasons) == 0 {
		t.Error("false-positive failures must name the findings")
	}
}
