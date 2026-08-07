package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitgo "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
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

func TestGitCollectorFindsMatchingCommits(t *testing.T) {
	dir := buildTestRepo(t)
	c := New(dir)

	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "api-abc"}
	// Prior carries the owner chain: the pod api-abc is owned by deployment
	// api — manifests name the workload, not the pod.
	obs, refs, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{pod},
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

func TestGitCollectorNoMatch(t *testing.T) {
	dir := buildTestRepo(t)
	c := New(dir)
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "deployment", Namespace: "prod", Name: "unrelated-workload"}},
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
