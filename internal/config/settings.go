// User settings loaded from ~/.kubetective/kubetective.yaml (roadmap v0.9).
//
// Precedence, from highest to lowest: CLI flag > environment variable >
// config file > built-in default. The config file replaces the env-var
// sprawl as the documented place to persist settings; env vars remain as
// overrides (e.g. for CI and containers) and the CLI flags stay the final
// word.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LLMSettings groups the optional AI explainer options.
type LLMSettings struct {
	Model   string `yaml:"model,omitempty"`
	BaseURL string `yaml:"base_url,omitempty"`
	APIKey  string `yaml:"api_key,omitempty"`
}

// Settings is the user-facing configuration schema (kubetective.yaml).
type Settings struct {
	Kubeconfig    string      `yaml:"kubeconfig,omitempty"`
	Context       string      `yaml:"context,omitempty"`
	Namespace     string      `yaml:"namespace,omitempty"`
	Since         string      `yaml:"since,omitempty"` // duration, e.g. "30m"
	PrometheusURL string      `yaml:"prometheus_url,omitempty"`
	LokiURL       string      `yaml:"loki_url,omitempty"`
	GitRepo       string      `yaml:"git_repo,omitempty"`
	ClusterID     string      `yaml:"cluster_id,omitempty"`
	LLM           LLMSettings `yaml:"llm,omitempty"`
	ServerListen  string      `yaml:"server_listen,omitempty"`
}

// SettingsPath returns the settings file location:
// $KUBECTIVE_HOME/kubetective.yaml (default ~/.kubetective/kubetective.yaml).
func SettingsPath() string {
	return filepath.Join(DefaultDir(), "kubetective.yaml")
}

// LoadSettings reads kubetective.yaml; a missing file yields zero values
// (callers fall back to defaults / env / flags). A malformed file is an
// error so misconfiguration is loud, not silent.
func LoadSettings() (Settings, error) {
	var s Settings
	b, err := os.ReadFile(SettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := yaml.Unmarshal(b, &s); err != nil {
		return s, err
	}
	return s, nil
}

// FirstNonEmpty returns the first non-empty of the given values: call with
// (flag, env, fromFile) to apply the documented precedence.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
