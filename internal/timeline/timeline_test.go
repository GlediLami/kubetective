package timeline

import (
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

func mk(kind string, ts time.Time) model.Observation {
	return collect.NewObservation(kind, model.SourceRef{System: "test"}, ts, model.ResourceRef{Kind: "pod", Name: "p1"}, map[string]any{}, 1.0)
}

func TestBuildSortsDedupsAndAnchors(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	// Duplicate (same content → same ID) plus out-of-order input.
	dupe := mk("container.terminated", base.Add(6*time.Minute))
	dupe.Payload = map[string]any{"reason": "OOMKilled"}
	evs := Build([]model.Observation{
		mk("event.recorded", base.Add(7*time.Minute)),
		dupe,
		mk("container.terminated", base.Add(6*time.Minute)), // same content → dedup
		mk("container.terminated", base.Add(12*time.Minute)),
		mk("pod.state", base.Add(1*time.Minute)),
	})

	if len(evs) != 4 {
		t.Fatalf("len = %d, want 4 (one dupe removed)", len(evs))
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].Observation.Timestamp.Before(evs[i-1].Observation.Timestamp) {
			t.Fatalf("timeline not sorted at %d", i)
		}
	}
	// Anchor = earliest critical observation (pod.state at t+1m is critical;
	// container.terminated at t+6m; the earliest critical is pod.state t+1m -
	// wait: pod.state is critical too and earlier than terminated → anchor).
	// The earliest observation overall is pod.state t+1m, which is critical.
	anchor, ok := Anchor(evs)
	if !ok {
		t.Fatal("no anchor")
	}
	if !anchor.Equal(base.Add(1 * time.Minute)) {
		t.Fatalf("anchor = %v, want %v", anchor, base.Add(1*time.Minute))
	}
	if evs[0].Offset != 0 {
		t.Fatalf("first event offset = %v, want 0 (anchored)", evs[0].Offset)
	}
	if evs[len(evs)-1].Offset != 11*time.Minute {
		t.Fatalf("last offset = %v, want 11m", evs[len(evs)-1].Offset)
	}
}

func TestBuildEmpty(t *testing.T) {
	if evs := Build(nil); len(evs) != 0 {
		t.Fatalf("empty input must produce empty timeline, got %d", len(evs))
	}
	if _, ok := Anchor(nil); ok {
		t.Fatal("empty timeline must have no anchor")
	}
}

// A non-critical observation hours before the incident must not drag the
// anchor back: the anchor is the earliest critical observation.
func TestBuildAnchorIgnoresOldNonCriticalEvents(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	evs := Build([]model.Observation{
		mk("node.condition", base.Add(-2*time.Hour)),
		mk("pod.state", base),
		mk("container.terminated", base.Add(6*time.Minute)),
	})
	anchor, ok := Anchor(evs)
	if !ok {
		t.Fatal("no anchor")
	}
	if !anchor.Equal(base) {
		t.Fatalf("anchor = %v, want %v (old node condition must not shift it)", anchor, base)
	}
}

// Zero-timestamp observations are data-quality noise: they must be skipped,
// not anchored on.
func TestBuildSkipsZeroTimestamp(t *testing.T) {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	zero := mk("pod.state", time.Time{})
	evs := Build([]model.Observation{zero, mk("pod.state", base)})
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1 (zero-timestamp skipped)", len(evs))
	}
	anchor, ok := Anchor(evs)
	if !ok || !anchor.Equal(base) {
		t.Fatalf("anchor = %v, %v; want %v", anchor, ok, base)
	}
}
