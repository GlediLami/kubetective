package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitgo "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

// buildTestRepo creates a temp git repo with two commits:
//   - old: adds config for workload "api" (api.yaml)
//   - recent: bumps a setting in api.yaml (the regression)
//
// Returns the repo path.
func buildTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gitgo.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	work, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	sig := &object.Signature{Name: "Engineer", Email: "eng@example.com", When: time.Date(2026, 8, 7, 13, 45, 0, 0, time.UTC)}
	commit := func(name, content, msg string, when time.Time) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := work.Add(name); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		sig.When = when
		if _, err := work.Commit(msg, &gitgo.CommitOptions{Author: sig}); err != nil {
			t.Fatalf("commit %s: %v", msg, err)
		}
	}
	commit("api.yaml", "replicas: 1\n", "add api deployment", time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC))
	commit("api.yaml", "replicas: 1\nenv: CACHE_SIZE=50000\n", "bump api cache size", time.Date(2026, 8, 7, 13, 45, 0, 0, time.UTC))
	// Unrelated manifest must not match.
	commit("other.yaml", "replicas: 1\n", "unrelated change", time.Date(2026, 8, 7, 13, 50, 0, 0, time.UTC))
	return dir
}

// incidentWindow is the window the test repo's commits precede: the cache bump
// lands at 13:45, the incident opens at 14:00.
var incidentWindow = api.Window{
	Start: time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 8, 7, 14, 30, 0, 0, time.UTC),
}

func TestGitCollectorFindsMatchingCommits(t *testing.T) {
	dir := buildTestRepo(t)
	c := New(dir)

	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	// Prior carries the owner chain: the pod api-abc is owned by deployment
	// api - manifests name the workload, not the pod.
	obs, refs, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{pod},
		Window:  incidentWindow,
		Prior: []model.Observation{{
			ID:       "obs-pod",
			Kind:     "pod.state",
			Resource: pod,
			Payload:  map[string]any{"owner_kind": "Deployment", "owner_name": "api"},
		}},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("missing source refs")
	}
	if len(obs) != 2 {
		t.Fatalf("commits = %d, want 2 (both touched api.yaml): %+v", len(obs), obs)
	}
	// Most recent first (git log order).
	first := obs[0]
	if first.Payload["sha"] == "" || first.Payload["message"] != "bump api cache size" {
		t.Errorf("first commit = %+v, want the cache bump", first.Payload)
	}
	if first.Resource != pod {
		t.Errorf("commit resource = %v, want the api pod target", first.Resource)
	}
	// Author + files are recorded (auditability / who changed it).
	if first.Payload["author"] != "Engineer" {
		t.Errorf("author = %v, want Engineer", first.Payload["author"])
	}
}

// TestGitCollectorWindowAnchoredNotWallClock is the regression test for the
// collector's original defect: the walk cut off at time.Now()-48h, so git
// attribution silently vanished for any incident older than two days —
// including replayed ones, which broke the reproducibility guarantee. The
// cutoff is now anchored to the investigation window, so replaying an old
// incident years later must still surface the commits that caused it.
func TestGitCollectorWindowAnchoredNotWallClock(t *testing.T) {
	dir := buildTestRepo(t)
	// A clock far past the commits: under the old wall-clock cutoff every
	// commit would be pruned before the file matcher ever ran.
	future := func() time.Time { return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC) }
	c := New(dir).WithClock(future)

	dep := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "api"}
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{dep},
		Window:  incidentWindow,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("commits = %d, want 2 — the window, not the wall clock, must bound the walk", len(obs))
	}

	// With no window the collector has nothing to anchor to and falls back to
	// the clock. That path stays lookback-bounded by design.
	obs, _, err = c.Collect(context.Background(), &collect.ScopePlan{Targets: []model.ResourceRef{dep}})
	if err != nil {
		t.Fatalf("Collect (no window): %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("windowless commits = %d, want 0 (clock fallback bounds the walk)", len(obs))
	}
}

// TestGitCollectorSkipsCommitsAfterWindow: a commit landing after the incident
// closed cannot have caused it.
func TestGitCollectorSkipsCommitsAfterWindow(t *testing.T) {
	dir := buildTestRepo(t)
	dep := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "api"}
	// Window closes at 13:00 — before the 13:45 cache bump, after the 12:00 add.
	obs, _, err := New(dir).Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{dep},
		Window: api.Window{
			Start: time.Date(2026, 8, 7, 12, 30, 0, 0, time.UTC),
			End:   time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("commits = %d, want 1 (the 13:45 bump post-dates the window)", len(obs))
	}
	if got := obs[0].Payload["message"]; got != "add api deployment" {
		t.Errorf("commit = %v, want the 12:00 add", got)
	}
}

func TestGitCollectorNoMatch(t *testing.T) {
	dir := buildTestRepo(t)
	c := New(dir)
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "deployment", Namespace: "prod", Name: "unrelated-workload"}},
		Window:  incidentWindow,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(obs) != 0 {
		t.Fatalf("commits = %d, want 0 (no manifest touches that workload)", len(obs))
	}
}

func TestGitCollectorUnconfiguredAndMissingRepo(t *testing.T) {
	// Unconfigured → silent, no error.
	if obs, _, err := New("").Collect(context.Background(), &collect.ScopePlan{}); err != nil || len(obs) != 0 {
		t.Fatalf("unconfigured: %v %d", err, len(obs))
	}
	// Missing repo → error (surfaced as a collector-down gap).
	if _, _, err := New("/nonexistent/repo").Collect(context.Background(), &collect.ScopePlan{}); err == nil {
		t.Fatal("missing repo must error")
	}
}
