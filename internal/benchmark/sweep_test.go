package benchmark

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GlediLami/kubetective/internal/record"
)

// The sweep is what lets a new scenario propose its own causal claims instead
// of the author inventing them. It is only worth that if it finds the same
// evidence a human would: this asserts it rediscovers, unaided, the two
// mutations dns-failure declares by hand.
func TestSweepRediscoversDeclaredMutations(t *testing.T) {
	dir := filepath.Join(scenariosPath, "dns-failure")
	inc, err := record.NewStore(dir).Load("record")
	if err != nil {
		t.Skipf("scenario unavailable: %v", err)
	}
	sc, err := LoadScenario(filepath.Join(dir, "scenario.yaml"))
	if err != nil {
		t.Skipf("scenario spec unavailable: %v", err)
	}

	sweep, err := Sweep(context.Background(), inc.Observations, contractFactory)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if sweep.BaselineCategory != sc.GroundTruth.TopHypothesisCategory {
		t.Fatalf("baseline = %q, want the scenario's ground truth %q",
			sweep.BaselineCategory, sc.GroundTruth.TopHypothesisCategory)
	}

	found := map[string]string{}
	for _, e := range sweep.Entries {
		if e.LoadBearing {
			found[e.Kind] = e.Category
		}
	}
	if len(found) == 0 {
		t.Fatal("sweep found no load-bearing evidence; the scenario declares two")
	}

	for _, m := range sc.Mutations {
		for _, kind := range m.RemoveKinds {
			got, ok := found[kind]
			if !ok {
				t.Errorf("declared mutation %q removes %s, but the sweep did not flag it load-bearing",
					m.Name, kind)
				continue
			}
			if m.ExpectCategory != "" && got != m.ExpectCategory {
				t.Errorf("mutation %q expects %s after removing %s; sweep saw %s",
					m.Name, m.ExpectCategory, kind, got)
			}
		}
	}
}

// Evidence that does not change the verdict must not be reported as
// load-bearing - a sweep that flags everything proposes mutations that fail.
func TestSweepSeparatesLoadBearingFromInert(t *testing.T) {
	inc, err := record.NewStore(filepath.Join(scenariosPath, "dns-failure")).Load("record")
	if err != nil {
		t.Skipf("scenario unavailable: %v", err)
	}
	sweep, err := Sweep(context.Background(), inc.Observations, contractFactory)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	inert := 0
	for _, e := range sweep.Entries {
		if e.Removed == 0 {
			t.Errorf("kind %q swept but removed nothing", e.Kind)
		}
		if e.LoadBearing && e.Category == sweep.BaselineCategory {
			t.Errorf("kind %q marked load-bearing but the verdict did not change", e.Kind)
		}
		if !e.LoadBearing && e.Category != sweep.BaselineCategory {
			t.Errorf("kind %q changed the verdict to %q but was not marked load-bearing",
				e.Kind, e.Category)
		}
		if !e.LoadBearing && !e.Weakening {
			inert++
		}
	}
	if inert == 0 {
		t.Error("every kind was load-bearing or weakening; the sweep is not discriminating")
	}
}

// Determinism: the sweep drives generated scenario files, so two runs over the
// same record must produce the same proposal.
func TestSweepIsDeterministic(t *testing.T) {
	inc, err := record.NewStore(filepath.Join(scenariosPath, "config-regression")).Load("record")
	if err != nil {
		t.Skipf("scenario unavailable: %v", err)
	}
	a, err := Sweep(context.Background(), inc.Observations, contractFactory)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	b, err := Sweep(context.Background(), inc.Observations, contractFactory)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(a.Entries) != len(b.Entries) {
		t.Fatalf("entry count differs: %d vs %d", len(a.Entries), len(b.Entries))
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			t.Fatalf("entry %d differs: %+v vs %+v", i, a.Entries[i], b.Entries[i])
		}
	}
}
