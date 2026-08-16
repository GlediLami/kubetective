package benchmark

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/internal/redact"
	"github.com/GlediLami/kubetective/pkg/api"
)

// Redaction makes a claim that is easy to state and easy to get wrong: a
// sanitised record still replays to the same verdict. It is not obviously true
// — the engine reads names (a deployment's name overlapping its pods' is how
// config-regression attributes a commit), so a careless pseudonymiser silently
// changes the diagnosis it was supposed to preserve.
//
// This runs every scenario twice, raw and redacted, and requires the category
// and the confidence to match.
func TestRedactionPreservesEveryVerdict(t *testing.T) {
	entries, err := os.ReadDir(scenariosPath)
	if err != nil {
		t.Skipf("scenarios unavailable: %v", err)
	}

	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		inc, err := record.NewStore(filepath.Join(scenariosPath, name)).Load("record")
		if err != nil {
			continue
		}

		rawCat, rawScore := verdict(t, inc.Observations)
		clean, _ := redact.New(redact.Options{}).Incident(inc)
		redCat, redScore := verdict(t, clean.Observations)

		if rawCat != redCat {
			t.Errorf("%s: verdict changed under redaction: %s → %s", name, rawCat, redCat)
		}
		if math.Abs(rawScore-redScore) > 1e-9 {
			t.Errorf("%s: confidence changed under redaction: %.4f → %.4f", name, rawScore, redScore)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no scenarios checked")
	}
	t.Logf("verdict preserved across %d redacted scenarios", checked)
}

// The other half of the claim: the redacted record must not carry the original
// identifiers it was supposed to remove.
func TestRedactionRemovesOriginalIdentifiers(t *testing.T) {
	inc, err := record.NewStore(filepath.Join(scenariosPath, "config-regression")).Load("record")
	if err != nil {
		t.Skipf("scenario unavailable: %v", err)
	}

	originals := map[string]bool{}
	for _, o := range inc.Observations {
		if o.Resource.Namespace != "" {
			originals[o.Resource.Namespace] = true
		}
		if o.Resource.Name != "" {
			originals[o.Resource.Name] = true
		}
	}

	clean, _ := redact.New(redact.Options{}).Incident(inc)
	blob := renderObservations(clean.Observations)
	for orig := range originals {
		if len(orig) < 4 {
			continue // too short to assert on without false positives
		}
		if strings.Contains(blob, orig) {
			t.Errorf("original identifier %q survived redaction", orig)
		}
	}
}

func verdict(t *testing.T, obs []model.Observation) (string, float64) {
	t.Helper()
	eng := contractFactory(record.NewReplayCollector(obs))
	out, err := eng.Investigate(context.Background(), &api.InvestigationRequest{
		Target: model.ResourceRef{Kind: "pod", Name: "scenario"},
	})
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	top := topHypothesis(out)
	if top == nil {
		return "(silent)", 0
	}
	return string(top.Category), top.Score.Score
}

func renderObservations(obs []model.Observation) string {
	var b strings.Builder
	for _, o := range obs {
		b.WriteString(o.Kind)
		b.WriteString(o.Resource.Namespace)
		b.WriteString(o.Resource.Name)
		b.WriteString(o.Source.Query)
		for k, v := range o.Payload {
			b.WriteString(k)
			if s, ok := v.(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}
