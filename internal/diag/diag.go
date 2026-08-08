// Package diag implements `kubetective doctor` (issue #2, v1.0 checklist):
// a read-only environment preflight. Every check reports OK, WARN, or FAIL
// with a human fix hint; the command exits non-zero only on failures.
//
// Checks never mutate anything: kubeconfig is loaded read-only, the store is
// listed read-only, and optional sources (Prometheus, Loki) are probed with
// a GET only when configured.
package diag

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GlediLami/kubetective/internal/config"
	"github.com/GlediLami/kubetective/internal/engine"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/internal/score"
)

// Level of a check result.
type Level string

const (
	OK   Level = "OK"
	WARN Level = "WARN"
	FAIL Level = "FAIL"
)

// Check is one doctor finding.
type Check struct {
	Name   string `json:"name"`
	Level  Level  `json:"level"`
	Detail string `json:"detail,omitempty"`
}

// Report is the full doctor output.
type Report struct {
	EngineVersion string  `json:"engine_version"`
	Checks        []Check `json:"checks"`
}

// Failing reports whether any check reached FAIL.
func (r *Report) Failing() bool {
	for _, c := range r.Checks {
		if c.Level == FAIL {
			return true
		}
	}
	return false
}

// Options control which checks run. Zero-ish values degrade gracefully.
type Options struct {
	Settings config.Settings // kubetective.yaml contents
	// SettingsErr is the yaml parse error (nil when absent or clean).
	SettingsErr error
	// SettingsMissing marks a missing file: WARN (optional file), not FAIL.
	SettingsMissing bool
	// StateDir is the KubeTective home (default via config.DefaultDir).
	StateDir string
	// ConfigPath is config.json for calibration state.
	ConfigPath string
	// PrometheusURL/LokiURL from settings/env ("" = not configured -> OK).
	PrometheusURL string
	LokiURL       string
	// HubCheck verifies kubeconfig/cluster; when nil the kubeconfig check
	// only verifies the file exists.
	HubCheck func(ctx context.Context) error
}

// Run executes the check set and returns the report; exit code is the
// caller's (report.Failing()).
func Run(ctx context.Context, o Options) Report {
	r := Report{EngineVersion: engine.Version}

	r.check("version", OK, engine.Version)

	// 1. kubetective.yaml (user settings).
	if o.SettingsErr != nil {
		r.check("settings", FAIL, "kubetective.yaml: "+o.SettingsErr.Error())
	} else if o.SettingsMissing {
		r.check("settings", WARN, "no kubetective.yaml - using defaults, env, and flags")
	} else {
		r.check("settings", OK, config.SettingsPath())
	}

	// 2. Calibration adoption (config.json).
	r.calibration(o)

	// 3. Kubeconfig + cluster reachability.
	r.cluster(ctx, o)

	// 4. Incident store.
	r.store(o)

	// 5. Optional evidence sources.
	r.probe(ctx, o.PrometheusURL, "prometheus")
	r.probe(ctx, o.LokiURL, "loki")

	return r
}

func (r *Report) check(name string, level Level, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, Level: level, Detail: detail})
}

func (r *Report) calibration(o Options) {
	if o.ConfigPath == "" {
		o.ConfigPath = config.Path()
	}
	cfg, err := config.LoadWithPath(o.ConfigPath)
	if err != nil {
		r.check("calibration", WARN, "config.json unreadable: "+err.Error()+" (defaults apply)")
		return
	}
	if cfg.Temperature <= 0 {
		r.check("calibration", WARN, "no adopted temperature yet - run benchmark or evaluate once")
		return
	}
	if cfg.Temperature == score.DefaultTemperature {
		r.check("calibration", WARN, "temperature equals default - no adoption yet")
		return
	}
	r.check("calibration", OK, fmt.Sprintf("temperature %.1f (adopted)", cfg.Temperature))
}

func (r *Report) cluster(ctx context.Context, o Options) {
	if o.HubCheck == nil {
		r.check("cluster", WARN, "no cluster check configured (skip)")
		return
	}
	if err := o.HubCheck(ctx); err != nil {
		r.check("cluster", FAIL, err.Error())
		return
	}
	r.check("cluster", OK, "kubeconfig resolves and API is reachable")
}

func (r *Report) store(o Options) {
	dir := o.StateDir
	if dir == "" {
		dir = config.DefaultDir()
	}
	incDir := filepath.Join(dir, "incidents")
	if _, err := os.Stat(incDir); err != nil {
		r.check("store", WARN, "no incidents recorded yet ("+incDir+")")
		return
	}
	st := record.NewStore(incDir)
	ids, err := st.List()
	if err != nil {
		r.check("store", FAIL, "incident store unreadable: "+err.Error())
		return
	}
	r.check("store", OK, fmt.Sprintf("%d incident(s) in %s", len(ids), incDir))
}

// probe checks an optional evidence source with a connected GET. Unreachable
// sources are WARN: the engine degrades silently by design.
func (r *Report) probe(ctx context.Context, baseURL, name string) {
	if baseURL == "" {
		r.check("source-"+name, OK, name+" not configured - skipped")
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/ready", nil)
	if err != nil {
		r.check("source-"+name, FAIL, "invalid URL: "+err.Error())
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		r.check("source-"+name, WARN, name+" unreachable at "+baseURL+" (evidence degrades silently)")
		return
	}
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		r.check("source-"+name, OK, name+" reachable at "+baseURL)
		return
	}
	r.check("source-"+name, WARN, fmt.Sprintf("%s returned %s at %s (evidence degrades silently)", name, resp.Status, baseURL))
}
