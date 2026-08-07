// Package timeline merges observations into a deduplicated, time-sorted,
// anchored timeline. The anchor is the earliest "critical" observation
// (terminations, waiting states) — everything is rendered relative to it
// (t-14m, t+3m) so replays read identically across timezones
// (docs/DESIGN.md §7.4).
package timeline

import (
	"sort"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/model"
)

// criticalKinds are observation kinds that signal incident onset.
var criticalKinds = map[string]bool{
	"container.terminated": true,
	"container.waiting":    true,
	"pod.state":            true,
}

// Build merges, dedups (by Observation.ID), sorts, and anchors observations.
func Build(observations []model.Observation) []model.TimelineEvent {
	// Dedup by ID, keep the first occurrence (identical facts).
	seen := make(map[string]bool, len(observations))
	events := make([]model.TimelineEvent, 0, len(observations))
	for _, o := range observations {
		if seen[o.ID] {
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

	// Anchor: earliest critical observation; fall back to the earliest event.
	anchor := events[0].Observation.Timestamp
	for _, ev := range events {
		if criticalKinds[ev.Observation.Kind] && ev.Observation.Timestamp.Before(anchor) {
			anchor = ev.Observation.Timestamp
		}
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
