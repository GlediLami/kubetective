package memory

import (
	"path/filepath"
	"testing"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
)

// An incident must never be its own "seen this before?" match. The live
// investigate path passes the record's *filename* (it has the saved path in
// hand), while Store.List yields bare ids - so the self-exclusion silently
// stopped matching and every real investigation opened with a 100%-overlap
// note pointing at itself. The unit tests missed it because they all passed
// bare ids.
func TestSimilarNeverMatchesItself(t *testing.T) {
	dir := t.TempDir()
	store := record.NewStore(dir)

	save := func(id string, kinds ...string) {
		inc := &model.Incident{
			ID:   id,
			Meta: model.IncidentMeta{ClusterID: "c1", Target: "deployment/prod/checkout", RecordVersion: record.RecordVersion},
		}
		for i, k := range kinds {
			inc.Observations = append(inc.Observations, model.Observation{
				ID: id + "-o" + string(rune('a'+i)), Kind: k,
			})
		}
		if _, err := store.Save(inc); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	save("incident-100-checkout", "pod.state", "container.terminated", "git.commit")
	save("incident-200-checkout", "pod.state", "container.terminated", "git.commit")

	// Every form the callers actually use must behave identically.
	for _, form := range []string{
		"incident-200-checkout",
		"incident-200-checkout.jsonl",
		filepath.Join(dir, "incident-200-checkout.jsonl"),
	} {
		matches, err := Similar(store, form, 5, "c1")
		if err != nil {
			t.Fatalf("Similar(%q): %v", form, err)
		}
		for _, m := range matches {
			if m.IncidentID == "incident-200-checkout" {
				t.Errorf("Similar(%q) returned the query incident as its own match (overlap %.2f)",
					form, m.Overlap)
			}
		}
		if len(matches) != 1 || matches[0].IncidentID != "incident-100-checkout" {
			t.Errorf("Similar(%q) = %+v, want exactly the other incident", form, matches)
		}
	}
}
