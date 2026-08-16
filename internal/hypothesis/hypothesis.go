// Package hypothesis implements the rule-based hypothesis engine: it dedups
// and merges analyzer-emitted candidates, reranks them, and applies
// deterministic status rules.
//
// The engine never fabricates hypotheses or evidence - it consolidates what
// analyzers produced. Status rules:
//
//   - same ID → merge evidence lists (an analyzer ran twice across adaptive
//     rounds);
//   - on the same resource, a hypothesis outranked by ≥ outrankGap is marked
//     RuledOut (deterministic, explainable: the breakdown shows the gap);
//   - near ties stay Candidate ("multiple plausible causes" - the CLI says so).
package hypothesis

import (
	"sort"
	"strings"

	"github.com/GlediLami/kubetective/internal/model"
)

// outrankGap is the minimum score gap for the ruled-out rule: 0.25 on the
// 0..1 score scale. Below that, the verdict is "multiple plausible causes".
const outrankGap = 0.25

// Merge consolidates hypotheses and returns them ranked by score (desc),
// with evidence merged per ID and statuses updated by the outranking rule.
func Merge(hs []model.Hypothesis) []model.Hypothesis {
	// 1. Merge by ID (adaptive rounds re-emit the same IDs).
	byID := map[string]*model.Hypothesis{}
	var order []string
	for i := range hs {
		h := &hs[i]
		if h.ID == "" {
			continue
		}
		if cur, ok := byID[h.ID]; ok {
			cur.Evidence = mergeStrings(cur.Evidence, h.Evidence)
			cur.Contradictions = mergeStrings(cur.Contradictions, h.Contradictions)
			cur.Missing = mergeStrings(cur.Missing, h.Missing)
			if h.Score != nil && cur.Score != nil && h.Score.Margin > cur.Score.Margin {
				cur.Score = h.Score
				cur.Claim = h.Claim
			}
			continue
		}
		cp := h
		byID[h.ID] = cp
		order = append(order, h.ID)
	}

	out := make([]model.Hypothesis, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}

	// 2. Rank by score (desc), stable.
	sort.SliceStable(out, func(i, j int) bool {
		return scoreOf(&out[i]) > scoreOf(&out[j])
	})

	// 3. Outranking rule per resource: the runner-up is RuledOut when the
	// gap is decisive, else Candidate (multiple plausible causes).
	for i := range out {
		h := &out[i]
		status := model.StatusCandidate
		if i == 0 || !sameResource(&out[0], h) {
			status = model.StatusLikely
		}
		if i > 0 && sameResource(&out[0], h) {
			gap := scoreOf(&out[0]) - scoreOf(h)
			if gap >= outrankGap {
				status = model.StatusRuledOut
			}
		}
		h.Status = status
	}
	return out
}

// ID contract.
//
// A hypothesis ID is "<prefix>.<resource>" — "memory.checkout-7f84c9",
// "crashloop.checkout-7f84c9". The outranking rule depends on it: two
// hypotheses compete only when they concern the same resource, and that is
// decided by everything after the first dot.
//
// The split is on the FIRST dot, not the last, so a resource name containing
// dots still resolves correctly. What would break the rule is a prefix
// containing one — hence ValidID, and the conformance test that runs every
// analyzer's real output through it.
const idSeparator = "."

// NewID builds a conforming hypothesis ID. Analyzers should use it rather than
// formatting the string themselves.
func NewID(prefix, resource string) string {
	return prefix + idSeparator + resource
}

// ValidID reports whether an ID can carry the outranking rule: a non-empty
// dot-free prefix, a separator, and a non-empty resource.
func ValidID(id string) bool {
	i := strings.Index(id, idSeparator)
	return i > 0 && i < len(id)-1
}

// SplitID returns the prefix and resource halves of a hypothesis ID.
func SplitID(id string) (prefix, resource string) {
	i := strings.Index(id, idSeparator)
	if i < 0 {
		return "", id
	}
	return id[:i], id[i+1:]
}

// sameResource reports whether two hypotheses concern the same resource,
// inferred from their IDs ("memory.checkout-7f84c9" vs "crashloop.checkout-7f84c9").
func sameResource(a, b *model.Hypothesis) bool {
	return resourceOf(a.ID) == resourceOf(b.ID)
}

func resourceOf(id string) string {
	_, resource := SplitID(id)
	return resource
}

func scoreOf(h *model.Hypothesis) float64 {
	if h.Score == nil {
		return 0
	}
	return h.Score.Score
}

func mergeStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
