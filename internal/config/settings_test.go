package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSettingsPathHonorsHome(t *testing.T) {
	t.Setenv("KUBETECTIVE_HOME", "/tmp/kt-home-test")
	if got := SettingsPath(); got != filepath.Join("/tmp/kt-home-test", "kubetective.yaml") {
		t.Fatalf("SettingsPath = %q, want $KUBETECTIVE_HOME/kubetective.yaml", got)
	}
}

func TestLoadSettingsMissingFile(t *testing.T) {
	t.Setenv("KUBETECTIVE_HOME", t.TempDir())
	s, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings on missing file: %v", err)
	}
	if s.PrometheusURL != "" || s.LokiURL != "" || s.Namespace != "" {
		t.Fatalf("expected zero settings for missing file, got %+v", s)
	}
}

func TestLoadSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBETECTIVE_HOME", dir)
	want := Settings{
		Kubeconfig:    "/k/cfg",
		Context:       "prod",
		Namespace:     "payments",
		Since:         "2h",
		PrometheusURL: "http://prom:9090",
		LokiURL:       "http://loki:3100",
		GitRepo:       "/code/manifests",
		ClusterID:     "cluster-prod",
		LLM:           LLMSettings{Model: "gpt-4o-mini", BaseURL: "http://llm:8000/v1"},
		ServerListen:  ":9090",
	}
	b, err := yaml.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsPath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if got.Context != "prod" || got.PrometheusURL != "http://prom:9090" || got.LLM.BaseURL != "http://llm:8000/v1" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestLoadSettingsMalformed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBETECTIVE_HOME", dir)
	if err := os.WriteFile(SettingsPath(), []byte("not: [valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(); err == nil {
		t.Fatal("LoadSettings on malformed yaml must fail loudly")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "env", "file"); got != "env" {
		t.Fatalf("FirstNonEmpty = %q, want env (flag first wins over file)", got)
	}
	if got := FirstNonEmpty("flag", "env", "file"); got != "flag" {
		t.Fatalf("FirstNonEmpty = %q, want flag", got)
	}
	if got := FirstNonEmpty("", "", "file"); got != "file" {
		t.Fatalf("FirstNonEmpty = %q, want file", got)
	}
	if got := FirstNonEmpty("", "", ""); got != "" {
		t.Fatalf("FirstNonEmpty = %q, want empty", got)
	}
}
