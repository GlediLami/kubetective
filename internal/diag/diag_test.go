package diag

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GlediLami/kubetective/internal/config"
)

func TestRunHealthyNoExtras(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBECTIVE_HOME", dir)
	// An adopted temperature makes the calibration check OK.
	if err := config.Save(config.Config{Temperature: 5.0}); err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), Options{
		StateDir: dir,
		HubCheck: func(context.Context) error { return nil },
	})
	if rep.Failing() {
		t.Fatalf("unexpected failure: %+v", rep.Checks)
	}
	if rep.EngineVersion == "" {
		t.Fatal("engine version missing")
	}
	wantNames := []string{"version", "settings", "calibration", "cluster", "store", "source-prometheus", "source-loki"}
	names := make(map[string]Level)
	for _, c := range rep.Checks {
		names[c.Name] = c.Level
	}
	for _, n := range wantNames {
		if _, ok := names[n]; !ok {
			t.Errorf("missing check %q in %+v", n, names)
		}
	}
}

func TestRunReportsFailures(t *testing.T) {
	dir := t.TempDir()
	rep := Run(context.Background(), Options{
		StateDir:    dir,
		SettingsErr: errors.New("malformed yaml"),
		HubCheck:    func(context.Context) error { return errors.New("cluster unreachable") },
	})
	if !rep.Failing() {
		t.Fatal("expected failing report")
	}
	levels := map[string]Level{}
	for _, c := range rep.Checks {
		levels[c.Name] = c.Level
	}
	if levels["settings"] != FAIL {
		t.Errorf("settings = %s, want FAIL", levels["settings"])
	}
	if levels["cluster"] != FAIL {
		t.Errorf("cluster = %s, want FAIL", levels["cluster"])
	}
}

func TestRunProbesSources(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBECTIVE_HOME", dir)
	// Healthy source.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer ok.Close()
	// Unreachable source.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()

	rep := Run(context.Background(), Options{
		StateDir:      dir,
		HubCheck:      func(context.Context) error { return nil },
		PrometheusURL: ok.URL,
		LokiURL:       down.URL,
	})
	levels := map[string]Level{}
	for _, c := range rep.Checks {
		levels[c.Name] = c.Level
	}
	if levels["source-prometheus"] != OK {
		t.Errorf("source-prometheus = %s, want OK (reachable)", levels["source-prometheus"])
	}
	if levels["source-loki"] != WARN {
		t.Errorf("source-loki = %s, want WARN (unreachable degrades silently)", levels["source-loki"])
	}
	if rep.Failing() {
		t.Errorf("unreachable optional source must not fail the report")
	}
}

func TestStoreCheck(t *testing.T) {
	dir := t.TempDir()
	incDir := filepath.Join(dir, "incidents")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "incident-1-x.jsonl"),
		[]byte(`{"type":"meta","meta":{"incident_id":"incident-1-x","record_version":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), Options{StateDir: dir, HubCheck: func(context.Context) error { return nil }})
	levels := map[string]Level{}
	for _, c := range rep.Checks {
		levels[c.Name] = c.Level
	}
	if levels["store"] != OK {
		t.Errorf("store = %s, want OK (%+v)", levels["store"], rep.Checks)
	}
}
