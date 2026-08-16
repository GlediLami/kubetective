package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A test that means to redirect the config path and misspells the environment
// variable does not fail — it silently writes to the developer's real
// ~/.kubetective/config.json. That happened: internal/diag set
// "KUBECTIVE_HOME" (one character short), so every `go test ./...` run
// overwrote the user's operating temperature with the value the test needed.
//
// These lock both override paths down by name.
func TestPathHonoursConfigOverride(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "custom.json")
	t.Setenv("KUBETECTIVE_CONFIG", want)
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q — KUBETECTIVE_CONFIG did not apply", got, want)
	}
}

func TestPathHonoursHomeOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBETECTIVE_CONFIG", "")
	t.Setenv("KUBETECTIVE_HOME", dir)
	got := Path()
	if !strings.HasPrefix(got, dir) {
		t.Fatalf("Path() = %q, want a path under %q — KUBETECTIVE_HOME did not apply", got, dir)
	}
}

// Save must land inside the overridden directory, never in the real home.
func TestSaveStaysInsideTheOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBETECTIVE_CONFIG", filepath.Join(dir, "config.json"))
	if err := Save(Config{Temperature: 33}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("config not written to the override path: %v", err)
	}
	cfg, err := Load()
	if err != nil || cfg.Temperature != 33 {
		t.Fatalf("Load = %+v, %v; want temperature 33 from the override", cfg, err)
	}
}
