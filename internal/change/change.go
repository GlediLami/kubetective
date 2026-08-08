// Package change implements the "what changed?" detector: it turns
// observations into ranked Change entries so an investigation can answer
// "what happened right before the incident?" with a relevance score per
// change.
package change

import (
	"sort"
	"time"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/graph"
	"github.com/GlediLami/kubetective/internal/model"
)

// changeEventReasons are event reasons that signal a meaningful change.
var changeEventReasons = map[string]bool{
	"ReplicaSetCreated":  true,
	"ScalingReplicaSet":  true,
	"SuccessfulCreate":   true,
	"SuccessfulDelete":   true,
	"Created":            true,
	"Started":            true,
	"Pulled":             true,
	"RollingUpdate":      true,
	"Killing":            true,
	"BackOff":            true,
	"FailedScheduling":   true,
	"Unhealthy":          true,
	"OOMKilling":         true,
}

// Detect extracts candidate changes from observations.
func Detect(observations []model.Observation, windowStart time.Time) []model.Change {
	var out []model.Change
	for _, o := range observations {
		switch o.Kind {
		case "deployment.state":
			out = append(out, model.Change{
				Resource:    o.Resource,
				Description: "deployment state observed (created or updated)",
				Timestamp:   o.Timestamp,
			})
		case "event.recorded":
			reason, _ := o.Payload["reason"].(string)
			if !changeEventReasons[reason] {
				continue
			}
			desc := reason
			if msg, ok := o.Payload["message"].(string); ok && msg != "" {
				desc = reason + ": " + msg
			}
			out = append(out, model.Change{
				Resource:    o.Resource,
				Description: truncate(desc, 100),
				Timestamp:   o.Timestamp,
			})
		case "pod.state":
			// A pod that appeared inside the window is a rollout/deploy change.
			if !o.Timestamp.Before(windowStart) && o.Timestamp.Before(time.Now()) {
				out = append(out, model.Change{
					Resource:    o.Resource,
					Description: "pod created",
					Timestamp:   o.Timestamp,
				})
			}
		}
	}
	return dedup(out)
}

func dedup(changes []model.Change) []model.Change {
	seen := map[string]bool{}
	var out []model.Change
	for _, c := range changes {
		key := c.Resource.String() + "|" + c.Timestamp.Format(time.RFC3339Nano) + "|" + c.Description
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// AnomalyScore returns 1.0 when a growing metric series co-occurs with the
// change (within delta), else 0. This is the design's anomaly factor: metric
// movement near a change is weak, non-causal corroboration - it raises the
// change's relevance without claiming causation.
func AnomalyScore(observations []model.Observation, ch model.Change, delta time.Duration) float64 {
	for _, o := range observations {
		if o.Kind != "metric.series" {
			continue
		}
		if d := o.Timestamp.Sub(ch.Timestamp); d < -delta || d > delta {
			continue
		}
		first, _ := analyze.PayloadFloat(o.Payload, "first")
		last, _ := analyze.PayloadFloat(o.Payload, "last")
		if first > 0 && last > first*1.2 {
			return 1.0
		}
	}
	return 0
}

// Rank scores changes by relevance to the incident:
//
//	relevance = 0.45·temporal + 0.30·graph + 0.15·ownership + 0.10·anomaly
//
// temporal: how close the change is to the incident onset (before it);
// graph: hops between the changed resource and the target in the evidence
// graph; ownership: direct ancestor in the OWNS chain; anomaly: metric
// co-occurrence via anomalyFn (nil = 0).
func Rank(changes []model.Change, g *model.Graph, target model.ResourceRef, onset time.Time, windowSpan time.Duration, anomalyFn func(model.Change) float64) []model.Change {
	const (
		wTemporal = 0.45
		wGraph    = 0.30
		wOwn      = 0.15
		wAnomaly  = 0.10
	)
	span := windowSpan
	if span <= 0 {
		span = 2 * time.Hour
	}

	for i := range changes {
		c := &changes[i]
		t := 0.0
		if !onset.IsZero() && span > 0 {
			// Closer before onset → higher; after onset decays toward 0.
			d := onset.Sub(c.Timestamp)
			if d < 0 {
				d = -d
			}
			t = 1 - float64(d)/float64(span)
			if t < 0 {
				t = 0
			}
			if t > 1 {
				t = 1
			}
		}
		anom := 0.0
		if anomalyFn != nil {
			anom = anomalyFn(*c)
		}
		hops := graph.Hops(g, c.Resource, target)
		gp := 0.0
		if hops >= 0 {
			gp = 1 / float64(hops+1) // 0 hops = 1.0, 1 hop = 0.5, …
		}
		own := 0.0
		if owns(g, c.Resource, target) {
			own = 1.0
		}
		c.Relevance = wTemporal*t + wGraph*gp + wOwn*own + wAnomaly*anom
		// Weighted contributions, so factors sum exactly to relevance.
		c.Factors = map[string]float64{
			"temporal":  wTemporal * t,
			"graph":     wGraph * gp,
			"ownership": wOwn * own,
			"anomaly":   wAnomaly * anom,
		}
	}
	sort.SliceStable(changes, func(i, j int) bool {
		return changes[i].Relevance > changes[j].Relevance
	})
	return changes
}

// owns reports whether a is an ancestor of b following OWNS edges.
func owns(g *model.Graph, a, b model.ResourceRef) bool {
	queue := []model.ResourceRef{a}
	seen := map[string]bool{a.String(): true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == b {
			return true
		}
		for _, e := range g.Edges {
			if e.Kind != model.EdgeOwns {
				continue
			}
			if e.From == cur && !seen[e.To.String()] {
				seen[e.To.String()] = true
				queue = append(queue, e.To)
			}
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
