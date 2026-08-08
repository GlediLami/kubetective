// Package timeline merges observations into a deduplicated, time-sorted,
// anchored timeline. The anchor is the earliest "critical" observation
// (terminations, waiting states) — everything is rendered relative to it
// (t-14m, t+3m) so replays read identically across timezones
//.
package timeline

import (
	"sort"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

// criticalKinds are observation kinds that signal incident onset.
var criticalKinds = map[string]bool{
	"container.terminated": true,
	"container.waiting":    true,
	"pod.state":            true,
}

// Build merges, dedups (by Observation.ID), sorts, and anchors observations.
// Zero-timestamp observations are skipped: they carry no temporal information
// and would corrupt the anchor (defense against data-quality issues).
func Build(observations []model.Observation) []model.TimelineEvent {
	// Dedup by ID, keep the first occurrence (identical facts).
	seen := make(map[string]bool, len(observations))
	events := make([]model.TimelineEvent, 0, len(observations))
	for _, o := range observations {
		if seen[o.ID] || o.Timestamp.IsZero() {
			continue
		}
		seen[o.ID] = true
		events = append(events, model.TimelineEvent{Observation: o})
	}

	// Sort by timestamp; same-timestamp events are unordered (clock skew is
	// surfaced as a gap upstream, never silently trusted here).
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Observation.Timestamp.Before(events[j].Observation.Timestamp)
	})
	if len(events) == 0 {
		return nil
	}

	// Anchor: the earliest critical observation (termination/waiting/pod
	// state). A non-critical event before it (e.g. a node condition from
	// hours ago) must not drag the anchor back — fall back to the earliest
	// event only when no critical observation exists.
	var anchor time.Time
	for _, ev := range events {
		if criticalKinds[ev.Observation.Kind] && (anchor.IsZero() || ev.Observation.Timestamp.Before(anchor)) {
			anchor = ev.Observation.Timestamp
		}
	}
	if anchor.IsZero() {
		anchor = events[0].Observation.Timestamp
	}
	for i := range events {
		events[i].Offset = events[i].Observation.Timestamp.Sub(anchor)
	}
	return events
}

// Anchor returns the onset time the timeline is anchored at.
func Anchor(events []model.TimelineEvent) (time.Time, bool) {
	for _, ev := range events {
		if ev.Offset == 0 {
			return ev.Observation.Timestamp, true
		}
	}
	if len(events) == 0 {
		return time.Time{}, false
	}
	return events[0].Observation.Timestamp, true
}
