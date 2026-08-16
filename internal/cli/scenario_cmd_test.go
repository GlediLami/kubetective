package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/GlediLami/kubetective/internal/benchmark"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/pkg/api"
)

// testFactory mirrors the engine the CLI builds, so a test exercises the
// shipped analyzer registry rather than a convenient subset.
func testFactory(cs ...collect.Collector) api.Investigator { return newEngine(cs...) }

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"deployment/prod/checkout": "deployment-prod-checkout",
		"  DNS Outage (prod) ":     "dns-outage-prod",
		"already-a-slug":           "already-a-slug",
		"---":                      "",
		"pod.state":                "pod-state",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// min_score is floored so an unrelated scoring change of a point or two does
// not turn every contributed scenario red.
func TestFloorScoreLeavesHeadroom(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{0.95, 0.90},
		{0.97, 0.95},
		{0.89, 0.85},
		{0.90, 0.85},
		{0.0, 0.0},
	} {
		got := floorScore(tc.in)
		if got != tc.want {
			t.Errorf("floorScore(%.2f) = %.2f, want %.2f", tc.in, got, tc.want)
		}
		if got > tc.in {
			t.Errorf("floorScore(%.2f) = %.2f is above its input", tc.in, got)
		}
	}
}

// End to end: an existing scenario's record goes back through the drafter and
// comes out as a scenario the benchmark can load and pass. This is the whole
// contribution path, and it is the test that would catch the drafter emitting
// YAML the suite cannot parse.
func TestScenarioNewRoundTripsThroughTheBenchmark(t *testing.T) {
	src, err := filepath.Abs("../../scenarios/dns-failure/record.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Skipf("suite unavailable: %v", err)
	}
	out := t.TempDir()

	cmd := newScenarioCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"new", src, "--name", "roundtrip", "--dir", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("scenario new: %v\n%s", err, buf.String())
	}

	dir := filepath.Join(out, "roundtrip")
	for _, f := range []string{"record.jsonl", "scenario.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}

	// The draft must parse as a scenario and carry the observed verdict.
	sc, err := benchmark.LoadScenario(filepath.Join(dir, "scenario.yaml"))
	if err != nil {
		t.Fatalf("generated scenario.yaml does not load: %v", err)
	}
	if sc.Name != "roundtrip" {
		t.Errorf("name = %q, want roundtrip", sc.Name)
	}
	if sc.GroundTruth.TopHypothesisCategory != "dns" {
		t.Errorf("category = %q, want dns (the observed verdict)", sc.GroundTruth.TopHypothesisCategory)
	}
	if len(sc.Mutations) == 0 {
		t.Fatal("no mutations proposed; the sweep found two load-bearing kinds")
	}
	for _, m := range sc.Mutations {
		if len(m.RemoveKinds) == 0 {
			t.Errorf("mutation %q removes nothing", m.Name)
		}
		if m.ExpectCategory == "" && !m.ExpectNoFindings {
			t.Errorf("mutation %q asserts nothing", m.Name)
		}
	}

	// And the whole thing must actually run.
	suite, err := benchmark.RunSuite(t.Context(), out, testFactory)
	if err != nil {
		t.Fatalf("RunSuite over the generated scenario: %v", err)
	}
	if len(suite.Results) != 1 || !suite.Results[0].Passed {
		t.Fatalf("generated scenario did not pass: %+v", suite.Results)
	}
	if p, total := suite.Robustness.MutationTotals(); p != total || total == 0 {
		t.Errorf("proposed mutations do not hold: %d/%d", p, total)
	}
}

// The draft must not read as finished work. Every generated file carries TODOs
// and the tautology warning, because a contributor who commits the engine's own
// answer unedited has added a scenario that can only ever confirm it.
func TestScenarioDraftIsMarkedUnfinished(t *testing.T) {
	src, err := filepath.Abs("../../scenarios/dns-failure/record.jsonl")
	if err != nil || func() bool { _, e := os.Stat(src); return e != nil }() {
		t.Skip("suite unavailable")
	}
	out := t.TempDir()

	cmd := newScenarioCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"new", src, "--name", "draft", "--dir", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("scenario new: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(out, "draft", "scenario.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{"DRAFT", "TODO", "tautology", "advisory"} {
		if !strings.Contains(body, want) {
			t.Errorf("draft does not mention %q:\n%s", want, body)
		}
	}
	// It must still be valid YAML despite the commentary.
	var any map[string]any
	if err := yaml.Unmarshal(b, &any); err != nil {
		t.Fatalf("draft is not valid YAML: %v", err)
	}

	// And the terminal output must tell the contributor what to do next.
	stdout := buf.String()
	for _, want := range []string{"EVIDENCE SWEEP", "load-bearing", "not ground truth"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("command output missing %q:\n%s", want, stdout)
		}
	}
}

// Sanitisation is the default. A scenario is published by definition, so the
// drafter must not write a live namespace name into the suite.
func TestScenarioNewSanitisesByDefault(t *testing.T) {
	src, err := filepath.Abs("../../scenarios/config-regression/record.jsonl")
	if err != nil || func() bool { _, e := os.Stat(src); return e != nil }() {
		t.Skip("suite unavailable")
	}
	out := t.TempDir()

	cmd := newScenarioCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"new", src, "--name", "sanitised", "--dir", out})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("scenario new: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(out, "sanitised", "record.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	// The source record names prod/checkout-7f84c9; neither may survive.
	for _, leak := range []string{"checkout-7f84c9", "\"prod\""} {
		if strings.Contains(body, leak) {
			t.Errorf("unsanitised identifier %q written into the suite", leak)
		}
	}
}

func TestScenarioNewRefusesToClobber(t *testing.T) {
	src, err := filepath.Abs("../../scenarios/dns-failure/record.jsonl")
	if err != nil || func() bool { _, e := os.Stat(src); return e != nil }() {
		t.Skip("suite unavailable")
	}
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newScenarioCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"new", src, "--name", "taken", "--dir", out})
	if err := cmd.Execute(); err == nil {
		t.Fatal("overwrote an existing scenario directory without --force")
	}
}
