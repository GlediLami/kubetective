// Package graph builds the bounded in-memory evidence graph from normalized
// observations: typed edges (OWNS, RUNS_ON, CHANGED_BEFORE) that the
// investigation pipeline and the "what changed" ranking both consume
// (docs/DESIGN.md §7.3).
//
// Edge discipline: structural edges (OWNS/RUNS_ON) come from API relationships
// via resource.owner / pod.state observations; CHANGED_BEFORE comes from the
// change detector. Causality (CAUSED_BY) is never emitted here — only
// analyzers that pass the causality discipline may add it.
package graph

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/model"
)

// Options bound graph size (large-cluster guard, docs/DESIGN.md §7.3).
type Options struct {
	MaxNodes int
	MaxEdges int
}

// correlateDelta is the co-occurrence window for TEMPORALLY_CORRELATED edges.
const correlateDelta = 5 * time.Minute

func (o Options) withDefaults() Options {
	if o.MaxNodes <= 0 {
		o.MaxNodes = 200
	}
	if o.MaxEdges <= 0 {
		o.MaxEdges = 2000
	}
	return o
}

// Build constructs the evidence graph for one investigation.
// changes are ranked Change entries; CHANGED_BEFORE edges are added from a
// change whose resource owns (or is) the target and whose timestamp precedes
// the incident onset.
func Build(observations []model.Observation, changes []model.Change, onset *model.TimelineEvent, opts Options) *model.Graph {
	opts = opts.withDefaults()
	g := &model.Graph{Bounds: model.GraphBounds{NodeBudget: opts.MaxNodes, EdgeBudget: opts.MaxEdges}}

	// Kubernetes API kinds arrive capitalized (ReplicaSet, Deployment); our
	// observation kinds are lowercase (deployment.state). Normalize so one
	// resource is one node regardless of which observation introduced it.

	ensureNode := func(r model.ResourceRef) {
		r = normKind(r)
		if r.Name == "" || r.Kind == "" {
			return
		}
		if len(g.Nodes) >= opts.MaxNodes {
			g.Bounds.Truncated = true
			g.Bounds.TruncatedKind = "node"
			return
		}
		for _, n := range g.Nodes {
			if n == r {
				return
			}
		}
		g.Nodes = append(g.Nodes, r)
	}

	addEdge := func(from, to model.ResourceRef, kind model.EdgeKind, evidence string, confidence float64) {
		from, to = normKind(from), normKind(to)
		if len(g.Edges) >= opts.MaxEdges {
			g.Bounds.Truncated = true
			g.Bounds.TruncatedKind = "edge"
			return
		}
		for _, e := range g.Edges {
			if e.From == from && e.To == to && e.Kind == kind {
				return // dedup
			}
		}
		g.Edges = append(g.Edges, model.Edge{From: from, To: to, Kind: kind, Evidence: []string{evidence}, Confidence: confidence})
	}

	// Structural edges from observations.
	for _, o := range observations {
		switch o.Kind {
		case "resource.owner":
			// owner → resource (OWNS). resource = o.Resource, owner = payload.
			owner := model.ResourceRef{
				Kind:      str(o.Payload, "owner_kind"),
				Namespace: o.Resource.Namespace,
				Name:      str(o.Payload, "owner_name"),
			}
			if owner.Name == "" {
				continue
			}
			ensureNode(owner)
			ensureNode(o.Resource)
			addEdge(owner, o.Resource, model.EdgeOwns, o.ID, 1.0)
		case "pod.state":
			// pod → node (RUNS_ON).
			if node := str(o.Payload, "node"); node != "" {
				nodeRef := model.ResourceRef{Kind: "node", Name: node}
				ensureNode(o.Resource)
				ensureNode(nodeRef)
				addEdge(o.Resource, nodeRef, model.EdgeRunsOn, o.ID, 1.0)
			}
		case "deployment.state":
			ensureNode(o.Resource)
		}
	}

	// CHANGED_BEFORE edges: changes on resources related to the target.
	if onset != nil {
		onsetTs := onset.Observation.Timestamp
		for _, ch := range changes {
			if !ch.Timestamp.Before(onsetTs) {
				continue
			}
			ensureNode(ch.Resource)
			if len(g.Nodes) == 0 {
				continue
			}
			// Link the changed resource to the resources it owns.
			for _, e := range g.Edges {
				if e.Kind == model.EdgeOwns && e.From == ch.Resource {
					addEdge(ch.Resource, e.To, model.EdgeChangedBefore, "", 0.9)
				}
			}
		}
	}

	// TEMPORALLY_CORRELATED edges: metric movement near a change (weak,
	// explicitly non-causal — the edge kind says so, docs/DESIGN.md §7.3).
	for _, ch := range changes {
		for _, o := range observations {
			if o.Kind != "metric.series" {
				continue
			}
			if d := o.Timestamp.Sub(ch.Timestamp); d < -correlateDelta || d > correlateDelta {
				continue
			}
			first, _ := payloadFloat(o.Payload, "first")
			last, _ := payloadFloat(o.Payload, "last")
			if first <= 0 || last <= first*1.2 {
				continue
			}
			ensureNode(ch.Resource)
			ensureNode(o.Resource)
			addEdge(ch.Resource, o.Resource, model.EdgeTemporallyCorrelated, o.ID, 0.6)
		}
	}

	// Deterministic ordering for stable output (diff-able replays).
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].String() < g.Nodes[j].String() })
	sort.Slice(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		if a.From.String() != b.From.String() {
			return a.From.String() < b.From.String()
		}
		if a.To.String() != b.To.String() {
			return a.To.String() < b.To.String()
		}
		return a.Kind < b.Kind
	})
	return g
}

// Hops returns the shortest directed distance from a to b following edges in
// either direction; -1 if unreachable. Used by the change-ranking relevance.
func Hops(g *model.Graph, a, b model.ResourceRef) int {
	if a == b {
		return 0
	}
	adj := map[string][]string{}
	for _, e := range g.Edges {
		adj[e.From.String()] = append(adj[e.From.String()], e.To.String())
		adj[e.To.String()] = append(adj[e.To.String()], e.From.String())
	}
	a, b = normKind(a), normKind(b)
	if a == b {
		return 0
	}
	type q struct {
		node string
		dist int
	}
	queue := []q{{a.String(), 0}}
	seen := map[string]bool{a.String(): true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, n := range adj[cur.node] {
			if n == b.String() {
				return cur.dist + 1
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			queue = append(queue, q{n, cur.dist + 1})
		}
	}
	return -1
}

// normKind lowercases a resource kind so one resource is one graph node
// regardless of which observation introduced it (k8s kinds are capitalized).
func normKind(r model.ResourceRef) model.ResourceRef {
	r.Kind = strings.ToLower(r.Kind)
	return r
}

func str(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	if s, ok := p[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// payloadFloat reads a float64 payload field (live or JSON-decoded).
func payloadFloat(p map[string]any, key string) (float64, bool) {
	switch v := p[key].(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}
