// Robustness gates: the suite's answer to "does the engine reason, or does it
// pattern-match?"
//
// A pass-only benchmark cannot tell those apart. Every scenario asserts the
// right verdict comes out; none asserts the verdict is actually *caused* by
// the evidence. An engine that keyed on "which analyzer fired" would score a
// clean sweep. Two gates close that gap:
//
//	mutation — remove the decisive evidence, require the verdict to move.
//	noise    — bury the incident in irrelevant observations, require it not to.
//
// Together they bracket the engine: it must be sensitive to evidence that
// matters and insensitive to evidence that does not.
package benchmark

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/pkg/api"
)

// Mutation declares a perturbation of a scenario's evidence together with the
// verdict change it must produce. Declared in scenario.yaml so the claim
// "this evidence is what drives the conclusion" is data, not prose.
type Mutation struct {
	Name string `yaml:"name"`
	// Reason states why removing this evidence should change the answer —
	// it is the causal claim the gate verifies.
	Reason      string   `yaml:"reason"`
	RemoveKinds []string `yaml:"remove_kinds,omitempty"`
	RemoveIDs   []string `yaml:"remove_ids,omitempty"`

	// Expectations. At least one must be set.
	ExpectCategory    string  `yaml:"expect_category,omitempty"`     // verdict must become this
	ExpectCategoryNot string  `yaml:"expect_category_not,omitempty"` // verdict must stop being this
	ExpectNoFindings  bool    `yaml:"expect_no_findings,omitempty"`  // engine must fall silent
	MaxScore          float64 `yaml:"max_score,omitempty"`           // confidence must drop below this
}

// MutationResult is one mutation's verdict.
type MutationResult struct {
	Name     string
	Reason   string
	Passed   bool
	Reasons  []string
	Removed  int
	Category string  // top category after mutation ("-" when silent)
	Score    float64 // top score after mutation
}

func (r *MutationResult) fail(format string, args ...any) {
	r.Passed = false
	r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
}

// applyMutation returns the observation set with the mutation's targets
// removed, plus how many were dropped.
func applyMutation(obs []model.Observation, m Mutation) ([]model.Observation, int) {
	kinds := make(map[string]bool, len(m.RemoveKinds))
	for _, k := range m.RemoveKinds {
		kinds[k] = true
	}
	ids := make(map[string]bool, len(m.RemoveIDs))
	for _, id := range m.RemoveIDs {
		ids[id] = true
	}
	out := make([]model.Observation, 0, len(obs))
	removed := 0
	for _, o := range obs {
		if kinds[o.Kind] || ids[o.ID] {
			removed++
			continue
		}
		out = append(out, o)
	}
	return out, removed
}

// RunMutation applies one mutation and checks the verdict moved as declared.
func RunMutation(ctx context.Context, m Mutation, obs []model.Observation, factory EngineFactory) (*MutationResult, error) {
	res := &MutationResult{Name: m.Name, Reason: m.Reason, Passed: true, Category: "-"}

	mutated, removed := applyMutation(obs, m)
	res.Removed = removed
	if removed == 0 {
		res.fail("mutation removed nothing — the scenario carries no %v evidence, so the gate proves nothing", m.RemoveKinds)
		return res, nil
	}

	eng := factory(record.NewReplayCollector(mutated))
	out, err := eng.Investigate(ctx, &api.InvestigationRequest{Target: model.ResourceRef{Kind: "pod", Name: "scenario"}})
	if err != nil {
		return nil, fmt.Errorf("mutation %s: investigate: %w", m.Name, err)
	}

	if top := topHypothesis(out); top != nil {
		res.Category = string(top.Category)
		res.Score = top.Score.Score
	}

	switch {
	case m.ExpectNoFindings && len(out.Findings) > 0:
		res.fail("expected silence after removing the evidence, got %d finding(s)", len(out.Findings))
	case m.ExpectCategoryNot != "" && res.Category == m.ExpectCategoryNot:
		res.fail("verdict is still %q after removing its supporting evidence — the conclusion does not depend on it",
			res.Category)
	case m.ExpectCategory != "" && res.Category != m.ExpectCategory:
		res.fail("verdict = %q, want %q once the evidence is gone", res.Category, m.ExpectCategory)
	}
	if m.MaxScore > 0 && res.Score > m.MaxScore {
		res.fail("confidence %.3f did not fall below %.3f after removing the evidence", res.Score, m.MaxScore)
	}
	return res, nil
}

// --- noise ---------------------------------------------------------------

// benignKinds are observation kinds that carry no failure signal. Noise is
// built only from these: the gate tests whether irrelevant volume perturbs the
// verdict, so anything an analyzer could legitimately fire on would make the
// test dishonest.
var benignKinds = []string{"pod.state", "container.running", "resource.owner", "deployment.state"}

// InjectNoise returns the observation set padded with n benign observations
// from unrelated workloads, spread across the same time span as the real
// evidence. Deterministic: same input, same output, so a noisy run is as
// replayable as a clean one.
func InjectNoise(obs []model.Observation, n int) []model.Observation {
	if n <= 0 {
		return obs
	}
	start, end := timeSpan(obs)
	span := end.Sub(start)
	if span <= 0 {
		span = 30 * time.Minute
	}

	out := make([]model.Observation, 0, len(obs)+n)
	out = append(out, obs...)
	for i := 0; i < n; i++ {
		kind := benignKinds[i%len(benignKinds)]
		name := fmt.Sprintf("bystander-%02d", i%47)
		ref := model.ResourceRef{Kind: "pod", Namespace: fmt.Sprintf("team-%d", i%7), Name: name}
		// Spread evenly across the incident window so noise competes for the
		// engine's attention rather than sitting harmlessly outside it.
		ts := start.Add(time.Duration(float64(span) * float64(i) / float64(n)))
		out = append(out, model.Observation{
			ID:         fmt.Sprintf("noise-%04d", i),
			Kind:       kind,
			Source:     model.SourceRef{System: "k8s", Query: "GET pods (unrelated)"},
			Timestamp:  ts,
			Resource:   ref,
			Payload:    benignPayload(kind, name),
			Confidence: 1.0,
		})
	}
	return out
}

func benignPayload(kind, name string) map[string]any {
	switch kind {
	case "pod.state":
		return map[string]any{"phase": "Running", "restarts": int64(0)}
	case "container.running":
		return map[string]any{"container": name, "ready": true}
	case "resource.owner":
		return map[string]any{"owner_kind": "ReplicaSet", "owner_name": name + "-rs"}
	default: // deployment.state
		return map[string]any{"replicas": int64(1), "available": int64(1), "ready": int64(1)}
	}
}

func timeSpan(obs []model.Observation) (time.Time, time.Time) {
	var start, end time.Time
	for _, o := range obs {
		if o.Timestamp.IsZero() {
			continue
		}
		if start.IsZero() || o.Timestamp.Before(start) {
			start = o.Timestamp
		}
		if end.IsZero() || o.Timestamp.After(end) {
			end = o.Timestamp
		}
	}
	if start.IsZero() {
		now := time.Unix(0, 0).UTC()
		return now, now.Add(30 * time.Minute)
	}
	return start, end
}

// NoiseResult records whether a scenario's verdict survived burial.
type NoiseResult struct {
	Scenario         string
	Injected         int
	BaselineCategory string
	NoisyCategory    string
	BaselineScore    float64
	NoisyScore       float64
	Stable           bool
	Reason           string
}

// ScoreDrift is how far confidence moved under noise.
func (n NoiseResult) ScoreDrift() float64 { return n.NoisyScore - n.BaselineScore }

// RunNoise replays a scenario buried under n irrelevant observations and
// reports whether the verdict held.
func RunNoise(ctx context.Context, name string, obs []model.Observation, n int, baselineCategory string, baselineScore float64, factory EngineFactory) (*NoiseResult, error) {
	res := &NoiseResult{
		Scenario:         name,
		Injected:         n,
		BaselineCategory: baselineCategory,
		BaselineScore:    baselineScore,
		NoisyCategory:    "-",
	}

	eng := factory(record.NewReplayCollector(InjectNoise(obs, n)))
	out, err := eng.Investigate(ctx, &api.InvestigationRequest{Target: model.ResourceRef{Kind: "pod", Name: "scenario"}})
	if err != nil {
		return nil, fmt.Errorf("noise %s: investigate: %w", name, err)
	}
	if top := topHypothesis(out); top != nil {
		res.NoisyCategory = string(top.Category)
		res.NoisyScore = top.Score.Score
	}

	// A healthy scenario has no verdict to preserve; for it, stability means
	// staying silent under noise.
	if baselineCategory == "" {
		res.Stable = len(out.Findings) == 0
		if !res.Stable {
			res.Reason = fmt.Sprintf("healthy control produced %d finding(s) once buried in noise", len(out.Findings))
		}
		return res, nil
	}

	res.Stable = res.NoisyCategory == baselineCategory
	if !res.Stable {
		res.Reason = fmt.Sprintf("verdict moved %s → %s under %d irrelevant observations",
			baselineCategory, res.NoisyCategory, n)
	}
	return res, nil
}

// --- suite plumbing ------------------------------------------------------

// RobustnessReport bundles both gates across the suite.
type RobustnessReport struct {
	Mutations map[string][]MutationResult // scenario → results
	Noise     []NoiseResult
	NoiseN    int
}

// MutationTotals counts declared mutations and how many held.
func (r *RobustnessReport) MutationTotals() (passed, total int) {
	names := make([]string, 0, len(r.Mutations))
	for k := range r.Mutations {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, m := range r.Mutations[n] {
			total++
			if m.Passed {
				passed++
			}
		}
	}
	return passed, total
}

// NoiseTotals counts scenarios whose verdict survived noise injection.
func (r *RobustnessReport) NoiseTotals() (stable, total int) {
	for _, n := range r.Noise {
		total++
		if n.Stable {
			stable++
		}
	}
	return stable, total
}

// noiseScenarioCount is how many irrelevant observations each scenario is
// buried under. Real namespaces carry thousands; the recorded scenarios carry
// 4–25, so this is the only gate that probes behaviour at cluster scale.
const noiseScenarioCount = 500

// runRobustness executes both gates for one scenario.
func runRobustness(ctx context.Context, sc *Scenario, base *Result, obs []model.Observation, factory EngineFactory, rep *RobustnessReport) error {
	for _, m := range sc.Mutations {
		mr, err := RunMutation(ctx, m, obs, factory)
		if err != nil {
			return err
		}
		rep.Mutations[sc.Name] = append(rep.Mutations[sc.Name], *mr)
	}

	// Baseline is the engine's own clean-run verdict. Comparing against ground
	// truth instead would conflate two different failures: "noise changed the
	// answer" and "the answer was already wrong".
	baseCategory := base.TopCategory
	nr, err := RunNoise(ctx, sc.Name, obs, noiseScenarioCount, baseCategory, base.Score, factory)
	if err != nil {
		return err
	}
	rep.Noise = append(rep.Noise, *nr)
	return nil
}
