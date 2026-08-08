// Package config persists the small set of engine settings that calibration
// can adopt at runtime (docs/DESIGN.md §9.4): currently the calibrated
// temperature, stored in ~/.kubedoctor/config.json so every CLI invocation
// (and the server/MCP modes) scores at the validated temperature.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the persisted engine configuration.
type Config struct {
	// Temperature is the operating sigmoid temperature (score.CurrentTemperature).
	Temperature float64 `json:"temperature,omitempty"`
}

// Path returns the config file location (KUBEDOCTOR_CONFIG overrides).
func Path() string {
	if p := os.Getenv("KUBEDOCTOR_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(DefaultDir(), "config.json")
}

// DefaultDir returns the KubeDoctor state directory (mirrors the record
// store's default).
func DefaultDir() string {
	if d := os.Getenv("KUBEDOCTOR_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kubedoctor"
	}
	return filepath.Join(home, ".kubedoctor")
}

// Load reads the config; a missing file yields the defaults (zero values —
// callers fall back to score.DefaultTemperature).
func Load() (Config, error) {
	var cfg Config
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Save writes the config (creating the directory as needed).
func Save(cfg Config) error {
	if err := os.MkdirAll(DefaultDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), b, 0o644)
}
