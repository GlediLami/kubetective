package benchmark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlediLami/kubetective/internal/hypothesis"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/pkg/api"
)

// The outranking rule ("a hypothesis beaten by ≥0.25 on the same resource is
// ruled out") reads the resource out of the hypothesis ID. That made the ID
// format an unwritten contract every analyzer had to honour, with nothing
// checking it: one analyzer emitting "oom.prod.checkout" instead of
// "oom.checkout" would silently stop competing with its neighbours, and the
// engine would report two confident verdicts instead of ruling one out.
//
// This test runs every analyzer's real output over every recorded scenario and
// holds it to the contract.
func TestHypothesisIDsConformToContract(t *testing.T) {
	entries, err := os.ReadDir(scenariosPath)
	if err != nil {
		t.Skipf("scenarios unavailable: %v", err)
	}

	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(scenariosPath, e.Name())
		inc, err := record.NewStore(dir).Load("record")
		if err != nil {
			continue
		}
		eng := contractFactory(record.NewReplayCollector(inc.Observations))
		out, err := eng.Investigate(context.Background(), &api.InvestigationRequest{
			Target: model.ResourceRef{Kind: "pod", Name: "scenario"},
		})
		if err != nil {
			t.Fatalf("%s: investigate: %v", e.Name(), err)
		}
		for _, h := range out.Hypotheses {
			checked++
			if !hypothesis.ValidID(h.ID) {
				t.Errorf("%s: hypothesis ID %q breaks the <prefix>.<resource> contract", e.Name(), h.ID)
				continue
			}
			prefix, resource := hypothesis.SplitID(h.ID)
			if prefix == "" || resource == "" {
				t.Errorf("%s: hypothesis ID %q splits to empty half (%q / %q)", e.Name(), h.ID, prefix, resource)
			}
			// A dot in the prefix would send the split to the wrong place and
			// break resource matching for every competitor.
			if strings.Contains(prefix, ".") {
				t.Errorf("%s: hypothesis ID %q has a dotted prefix %q", e.Name(), h.ID, prefix)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no hypotheses checked — the conformance test is not exercising anything")
	}
	t.Logf("checked %d hypothesis IDs across the suite", checked)
}
