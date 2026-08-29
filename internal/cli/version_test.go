package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GlediLami/kubetective/internal/engine"
)

func TestVersionJSON(t *testing.T) {
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format=json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version --format=json: %v", err)
	}

	var got versionInfo
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("version output is not JSON: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
		t.Fatalf("version output is not a JSON object: %v", err)
	}
	if got.Version != engine.Version {
		t.Errorf("JSON version = %q, want %q", got.Version, engine.Version)
	}
	if got.Commit != engine.Commit {
		t.Errorf("JSON commit = %q, want %q", got.Commit, engine.Commit)
	}
	if _, ok := fields["commit"]; !ok {
		t.Error("JSON output is missing the commit key")
	}
	if engine.BuildDate == "" && fields["build_date"] != nil {
		t.Error("JSON output includes build_date without a stamped build date")
	}
	if got.Go == "" {
		t.Error("JSON Go version is empty")
	}
}

func TestVersionShortMatchesDefault(t *testing.T) {
	defaultOutput := runVersion(t)
	if want := engine.Version + "\n"; defaultOutput != want {
		t.Fatalf("default version output = %q, want %q", defaultOutput, want)
	}
	shortOutput := runVersion(t, "--short")
	if shortOutput != defaultOutput {
		t.Errorf("--short output = %q, want default output %q", shortOutput, defaultOutput)
	}
}

func runVersion(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version %v: %v", args, err)
	}
	return out.String()
}
