// Package git implements the Git collector: it reads a local repository and
// emits git.commit observations for commits that touched manifests matching
// the investigation's target (workload name). This is the "who changed it and
// why" half of the config-regression story.
//
// The collector is optional and failure-tolerant: no repository configured or
// an unreadable repo becomes a gap, never a failed investigation.
package git

import (
	"context"
	"path"
	"strings"
	"time"

	gitgo "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

// maxCommits caps the walk (large repositories).
const maxCommits = 200

// Collector reads one git repository.
type Collector struct {
	repoPath string
	// maxAge bounds the walk to commits within the window (plus slack).
	maxAge time.Duration
}

var _ collect.Collector = (*Collector)(nil)

// New creates a collector for a local git checkout.
func New(repoPath string) *Collector {
	return &Collector{repoPath: repoPath, maxAge: 48 * time.Hour}
}

func (c *Collector) ID() string { return "git" }

// Collect walks the repository's history and emits git.commit observations
// for commits whose changed files match the target workload.
func (c *Collector) Collect(_ context.Context, scope *collect.ScopePlan) ([]model.Observation, []model.SourceRef, error) {
	if c.repoPath == "" {
		return nil, nil, nil
	}
	repo, err := gitgo.PlainOpen(c.repoPath)
	if err != nil {
		return nil, nil, err
	}
	ref, err := repo.Head()
	if err != nil {
		return nil, nil, err
	}
	iter, err := repo.Log(&gitgo.LogOptions{From: ref.Hash()})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	refs := []model.SourceRef{{System: "git", Query: "git log --name-only (matching target)"}}
	cutoff := time.Now().Add(-c.maxAge)
	var obs []model.Observation

	count := 0
	err = iter.ForEach(func(commit *object.Commit) error {
		count++
		if count > maxCommits || commit.Committer.When.Before(cutoff) {
			return storer.ErrStop
		}
		// Which target workloads could this commit have touched? Stats =
		// changed files vs first parent (the diff, not the whole tree).
		stats, err := commit.Stats()
		if err != nil {
			return nil
		}
		files := make([]string, 0, len(stats))
		for _, s := range stats {
			files = append(files, s.Name)
		}
		targets := matchingTargets(scope, files)
		if len(targets) == 0 {
			return nil
		}
		short := commit.Hash.String()[:8]
		for _, t := range targets {
			obs = append(obs, collect.NewObservation(
				"git.commit",
				model.SourceRef{System: "git", Query: "git log --name-only (matching target)"},
				commit.Committer.When.UTC(),
				t,
				map[string]any{
					"sha":      short,
					"author":   commit.Author.Name,
					"email":    commit.Author.Email,
					"message":  commit.Message,
					"files":    files,
					"workload": t.Name,
				},
				1.0,
			))
		}
		return nil
	})
	if err != nil && err != storer.ErrStop {
		return obs, refs, err
	}
	return obs, refs, nil
}

// workloadNames collects the workload names relevant to the investigation:
// explicit targets plus owner-chain names from prior observations (a pod
// target api-abc is owned by deployment api → manifests name the workload,
// not the pod).
func workloadNames(scope *collect.ScopePlan) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, strings.ToLower(name))
	}
	for _, t := range scope.Targets {
		add(t.Name)
	}
	for _, o := range scope.Prior {
		if o.Kind == "pod.state" {
			add(str(o.Payload, "owner_name"))
		}
		if o.Kind == "resource.owner" {
			add(str(o.Payload, "owner_name"))
		}
		if o.Kind == "deployment.state" {
			add(o.Resource.Name)
		}
	}
	return out
}

func str(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	if s, ok := p[key].(string); ok {
		return s
	}
	return ""
}

// matchingTargets reports which scope targets a set of changed files plausibly
// touch: a manifest whose basename contains the workload name (deployment
// checkout → checkout.yaml, checkout-deployment.yaml, kustomization for
// dirs named checkout/...). Also matches files under a directory named after
// the workload.
func matchingTargets(scope *collect.ScopePlan, files []string) []model.ResourceRef {
	names := workloadNames(scope)
	var out []model.ResourceRef
	for _, t := range scope.Targets {
		if t.Name == "" {
			continue
		}
		name := strings.ToLower(t.Name)
		for _, f := range files {
			lower := strings.ToLower(f)
			base := strings.ToLower(path.Base(f))
			if matches(base, lower, name, names) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// matches reports whether a file plausibly belongs to the target: its
// basename contains the target name or any derived workload name, or the path
// contains a /<workload>/ directory segment.
func matches(base, lower, targetName string, workloadNames []string) bool {
	if strings.Contains(base, targetName) || strings.Contains(lower, "/"+targetName+"/") {
		return true
	}
	for _, n := range workloadNames {
		if n != "" && (strings.Contains(base, n) || strings.Contains(lower, "/"+n+"/")) {
			return true
		}
	}
	return false
}
