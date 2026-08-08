package llm

import (
	"strings"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

// Digest is the ONLY thing the LLM ever sees: a redacted, capped, sanitized
// summary of the investigation. It deliberately excludes raw observations
// (especially log.snippet), secrets, kubeconfig data, and free-form payload
// values beyond short reason strings.
type Digest struct {
	Incident   IncidentDigest     `json:"incident"`
	Hypotheses []HypothesisDigest `json:"hypotheses"`
	Timeline   []TimelineDigest   `json:"timeline"`
	Changes    []ChangeDigest     `json:"changes"`
	Gaps       []string           `json:"evidence_gaps"`
}

type IncidentDigest struct {
	Target     string  `json:"target"`
	Status     string  `json:"status"`
	Severity   string  `json:"severity"`
	Confidence float64 `json:"confidence"`
}

type HypothesisDigest struct {
	Claim          string   `json:"claim"`
	Category       string   `json:"category"`
	ScorePercent   int      `json:"score_percent"`
	Evidence       []string `json:"evidence"` // score line labels (+/-), quoted
	Contradictions []string `json:"contradictions,omitempty"`
	Missing        []string `json:"missing,omitempty"`
}

type TimelineDigest struct {
	Time   string `json:"time"` // HH:MM:SS relative
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

type ChangeDigest struct {
	Resource    string  `json:"resource"`
	Description string  `json:"description"`
	Relevance   float64 `json:"relevance"`
}

// BuildDigest constructs the digest: top N hypotheses with their breakdown
// labels, the anchored timeline, top changes, and evidence gaps.
func BuildDigest(res *api.InvestigationResult, maxHypotheses, maxTimeline, maxChanges int) Digest {
	d := Digest{}
	if res.Incident != nil {
		d.Incident = IncidentDigest{
			Target:     res.Incident.Target.String(),
			Status:     res.Incident.Status,
			Severity:   string(res.Incident.Severity),
			Confidence: res.Incident.Confidence,
		}
	}

	for i := range res.Hypotheses {
		if len(d.Hypotheses) >= maxHypotheses {
			break
		}
		h := &res.Hypotheses[i]
		hd := HypothesisDigest{
			Claim:    sanitize(h.Claim, 240),
			Category: string(h.Category),
		}
		if h.Score != nil {
			hd.ScorePercent = int(h.Score.Score*100 + 0.5)
			for _, line := range h.Score.Lines {
				hd.Evidence = append(hd.Evidence, sanitize(line.Label, 140))
			}
			hd.Missing = append(hd.Missing, h.Missing...)
		}
		if len(h.Contradictions) > 0 {
			hd.Contradictions = h.Contradictions
		}
		d.Hypotheses = append(d.Hypotheses, hd)
	}

	// Timeline: only kind + reason strings; never raw values or logs.
	for _, ev := range res.Timeline {
		if ev.Observation.Kind == "log.snippet" {
			continue // raw log content never enters the digest
		}
		if len(d.Timeline) >= maxTimeline {
			break
		}
		td := TimelineDigest{
			Time:   ev.Observation.Timestamp.Format("15:04:05"),
			Kind:   ev.Observation.Kind,
			Reason: reasonOf(ev.Observation),
		}
		d.Timeline = append(d.Timeline, td)
	}

	for _, ch := range res.Changes {
		if len(d.Changes) >= maxChanges {
			break
		}
		d.Changes = append(d.Changes, ChangeDigest{
			Resource:    ch.Resource.String(),
			Description: sanitize(ch.Description, 140),
			Relevance:   ch.Relevance,
		})
	}

	for _, g := range res.EvidenceGaps {
		d.Gaps = append(d.Gaps, sanitize(g.Description, 140))
	}
	return d
}

// reasonOf extracts a short, safe reason string from an observation payload
// (phase/reason only - never arbitrary payload fields).
func reasonOf(o model.Observation) string {
	if r, ok := o.Payload["reason"].(string); ok && r != "" {
		return sanitize(r, 60)
	}
	if p, ok := o.Payload["phase"].(string); ok && p != "" {
		return sanitize(p, 60)
	}
	return ""
}

// sanitize makes strings digest-safe: valid UTF-8, no control characters
// (newlines become spaces), capped length. This is the injection-hardening
// boundary - everything the LLM sees passes through here.
func sanitize(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// strip other control characters
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if maxRunes > 0 {
		runes := []rune(out)
		if len(runes) > maxRunes {
			out = string(runes[:maxRunes-1]) + "…"
		}
	}
	return out
}
