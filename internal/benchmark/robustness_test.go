package benchmark

import (
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

func obsAt(id, kind string, ts time.Time) model.Observation {
	return model.Observation{
		ID:         id,
		Kind:       kind,
		Timestamp:  ts,
		Resource:   model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout"},
		Payload:    map[string]any{},
		Confidence: 1.0,
	}
}

func sampleObs() []model.Observation {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	return []model.Observation{
		obsAt("o1", "pod.state", base),
		obsAt("o2", "container.terminated", base.Add(2*time.Minute)),
		obsAt("o3", "container.terminated", base.Add(4*time.Minute)),
		obsAt("o4", "git.commit", base.Add(-6*time.Minute)),
	}
}

func TestApplyMutationRemovesByKindAndID(t *testing.T) {
	obs := sampleObs()

	got, n := applyMutation(obs, Mutation{RemoveKinds: []string{"container.terminated"}})
	if n != 2 || len(got) != 2 {
		t.Fatalf("removed %d leaving %d, want 2 and 2", n, len(got))
	}
	for _, o := range got {
		if o.Kind == "container.terminated" {
			t.Errorf("kind %s survived removal", o.Kind)
		}
	}

	got, n = applyMutation(obs, Mutation{RemoveIDs: []string{"o1", "o4"}})
	if n != 2 || len(got) != 2 {
		t.Fatalf("id removal: removed %d leaving %d, want 2 and 2", n, len(got))
	}

	// A mutation that matches nothing must be visible as such — the gate is
	// worthless if it silently passes on evidence the scenario never had.
	if _, n = applyMutation(obs, Mutation{RemoveKinds: []string{"metric.series"}}); n != 0 {
		t.Errorf("removed %d for an absent kind, want 0", n)
	}
}

func TestInjectNoiseIsAdditiveAndDeterministic(t *testing.T) {
	obs := sampleObs()

	a := InjectNoise(obs, 500)
	if len(a) != len(obs)+500 {
		t.Fatalf("len = %d, want %d", len(a), len(obs)+500)
	}

	// Deterministic: a noisy replay must be as reproducible as a clean one.
	b := InjectNoise(obs, 500)
	for i := range a {
		if a[i].ID != b[i].ID || !a[i].Timestamp.Equal(b[i].Timestamp) {
			t.Fatalf("noise is not deterministic at index %d: %q@%v vs %q@%v",
				i, a[i].ID, a[i].Timestamp, b[i].ID, b[i].Timestamp)
		}
	}

	// Noise must not collide with real observation IDs, or dedup would drop
	// real evidence and the gate would be measuring the wrong thing.
	real := map[string]bool{}
	for _, o := range obs {
		real[o.ID] = true
	}
	start, end := timeSpan(obs)
	for _, o := range a[len(obs):] {
		if real[o.ID] {
			t.Fatalf("noise observation reused real ID %q", o.ID)
		}
		if o.Resource.Name == "checkout" {
			t.Fatalf("noise landed on the target resource: %+v", o.Resource)
		}
		if o.Timestamp.Before(start) || o.Timestamp.After(end) {
			t.Errorf("noise at %v falls outside the incident span %v..%v", o.Timestamp, start, end)
		}
	}
}

func TestInjectNoiseHandlesDegenerateInput(t *testing.T) {
	if got := InjectNoise(sampleObs(), 0); len(got) != 4 {
		t.Errorf("n=0 must be a no-op, got %d", len(got))
	}
	// All-zero timestamps must not panic or produce a zero-width span.
	obs := []model.Observation{{ID: "x", Kind: "pod.state"}}
	got := InjectNoise(obs, 10)
	if len(got) != 11 {
		t.Fatalf("len = %d, want 11", len(got))
	}
	seen := map[time.Time]int{}
	for _, o := range got[1:] {
		seen[o.Timestamp]++
	}
	if len(seen) < 2 {
		t.Error("noise collapsed onto a single timestamp; the span fallback did not apply")
	}
}

func TestMutationTotalsCountsEveryDeclaredClaim(t *testing.T) {
	rep := &RobustnessReport{Mutations: map[string][]MutationResult{
		"a": {{Passed: true}, {Passed: false}},
		"b": {{Passed: true}},
	}}
	passed, total := rep.MutationTotals()
	if passed != 2 || total != 3 {
		t.Errorf("totals = %d/%d, want 2/3", passed, total)
	}
}

func TestNoiseTotals(t *testing.T) {
	rep := &RobustnessReport{Noise: []NoiseResult{{Stable: true}, {Stable: false}, {Stable: true}}}
	stable, total := rep.NoiseTotals()
	if stable != 2 || total != 3 {
		t.Errorf("totals = %d/%d, want 2/3", stable, total)
	}
}
