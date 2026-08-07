// Package imagepull implements the image-pull-failure analyzer: it activates on
// container.waiting with reason ErrImagePull / ImagePullBackOff and builds the
// "image cannot be pulled" hypothesis.
package imagepull

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/score"
)

const (
	weightWaiting = 30.0
	weightMessage = 15.0
	weightImage   = 10.0
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "imagepull" }
func (a *Analyzer) Name() string { return "Image Pull Failure" }

func (a *Analyzer) Supports(o model.Observation) bool {
	if o.Kind != "container.waiting" {
		return false
	}
	r, _ := o.Payload["reason"].(string)
	return r == "ErrImagePull" || r == "ImagePullBackOff"
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	seen := map[string]model.Observation{}
	for _, o := range in.Observations {
		if a.Supports(o) {
			key := o.Resource.String()
			if cur, ok := seen[key]; !ok || o.Timestamp.After(cur.Timestamp) {
				seen[key] = o
			}
		}
	}
	// Attach image from container.spec.
	images := map[string]string{}
	for _, o := range in.Observations {
		if o.Kind == "container.spec" {
			if img, ok := o.Payload["image"].(string); ok {
				images[o.Resource.String()] = img
			}
		}
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for key, o := range seen {
		reason, _ := o.Payload["reason"].(string)
		msg, _ := o.Payload["message"].(string)

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("imagepull.%s", o.Resource.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevHigh,
			Title:       reason,
			Description: fmt.Sprintf("container cannot start: %s", msg),
			Timestamp:   o.Timestamp,
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		eWaiting := model.Evidence{
			ID:     fmt.Sprintf("imagepull.%s.waiting", o.Resource.Name),
			Claim:  "container waiting on image pull",
			Weight: weightWaiting, Strength: 1.0,
		}
		evs = append(evs, eWaiting)
		terms = append(terms, score.EvidenceTerm{ID: eWaiting.ID, Label: fmt.Sprintf("image pull failure (%s)", reason), Weight: weightWaiting, Strength: 1.0, Polarity: +1})

		if msg != "" {
			eMsg := model.Evidence{
				ID:     fmt.Sprintf("imagepull.%s.message", o.Resource.Name),
				Claim:  "kubelet image pull error message",
				Weight: weightMessage, Strength: 1.0,
			}
			evs = append(evs, eMsg)
			terms = append(terms, score.EvidenceTerm{ID: eMsg.ID, Label: fmt.Sprintf("error: %s", truncate(msg, 80)), Weight: weightMessage, Strength: 1.0, Polarity: +1})
		}
		if img := images[key]; img != "" {
			eImg := model.Evidence{
				ID:     fmt.Sprintf("imagepull.%s.image", o.Resource.Name),
				Claim:  "image reference",
				Weight: weightImage, Strength: 1.0,
			}
			evs = append(evs, eImg)
			terms = append(terms, score.EvidenceTerm{ID: eImg.ID, Label: fmt.Sprintf("image: %s", img), Weight: weightImage, Strength: 1.0, Polarity: +1})
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("imagepull.%s", o.Resource.Name),
			Claim:    fmt.Sprintf("Image pull failure: %s (%s)", reason, msg),
			Category: model.CatImage,
			Status:   model.StatusLikely,
			Score:    breakdown(terms),
		}
		for _, e := range evs {
			h.Evidence = append(h.Evidence, e.ID)
		}
		hypotheses = append(hypotheses, h)
		evidence = append(evidence, evs...)
	}
	return findings, hypotheses, evidence, nil
}

func breakdown(terms []score.EvidenceTerm) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, 0)
	return &bd
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (a *Analyzer) NeedsEvidence(_ model.Hypothesis) []analyze.EvidenceRequest { return nil }

func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s: %s", f.Title, strings.ToLower(f.Description))
}
