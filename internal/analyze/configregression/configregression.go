// Package configregression implements the config-regression analyzer: it
// links a change — a git commit touching the workload's manifests or a
// GitOps reconcile — to the incident onset and builds the "configuration
// regression" hypothesis (docs/DESIGN.md §11, v0.4).
//
// This is the analyzer that answers "what changed" with "who changed it and
// why": the commit is the mechanism, its recency before the onset is the
// temporal correlation, ownership makes it contextual.
package configregression

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/internal/score"
)

const (
	weightCommit  = 30.0 // git commit touching the workload
	weightRecency = 25.0 // commit close before the incident onset
	weightGitOps  = 25.0 // GitOps controller reports trouble around the onset
	weightChange  = 10.0 // cluster-observed change on the workload
	weightSymptom = 30.0 // mechanism: the failure follows the change
)

// maxRegressionAge is how far before the onset a commit still counts as a
// plausible regression trigger.
const maxRegressionAge = 30 * time.Minute

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "configregression" }
func (a *Analyzer) Name() string { return "Configuration Regression" }

// Supports activates on anything that signals a behavior change: git commits,
// GitOps reconciles, or cluster changes (deployment.state).
func (a *Analyzer) Supports(o model.Observation) bool {
	switch o.Kind {
	case "git.commit", "gitops.state", "deployment.state":
		return true
	}
	return false
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	// The incident onset is the timeline anchor; the engine passes the
	// earliest event's timestamp via the graph? No — recompute from
	// observations: the earliest critical observation.
	onset := onsetOf(in.Observations)

	// Symptom: is anything actually wrong on this workload? A regression
	// hypothesis is only meaningful when the workload is failing.
	if !hasSymptom(in.Observations) {
		return nil, nil, nil, nil
	}

	// Relevant changes: commits/GitOps state on the failing resources.
	commits := map[string]model.Observation{} // resource → newest commit
	gitops := map[string]model.Observation{}  // resource → gitops state
	changes := map[string]model.Observation{} // resource → newest deployment.state
	for _, o := range in.Observations {
		key := o.Resource.String()
		switch o.Kind {
		case "git.commit":
			if cur, ok := commits[key]; !ok || o.Timestamp.After(cur.Timestamp) {
				commits[key] = o
			}
		case "gitops.state":
			gitops[key] = o
		case "deployment.state":
			if cur, ok := changes[key]; !ok || o.Timestamp.After(cur.Timestamp) {
				changes[key] = o
			}
		}
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	// One hypothesis per affected resource (not per observation).
	keys := map[string]bool{}
	for k := range commits {
		keys[k] = true
	}
	for k := range gitops {
		keys[k] = true
	}
	for k := range changes {
		keys[k] = true
	}
	// All changed resources (deployment.state) for name-overlap resolution:
	// a gitops object "checkout-app" relates to the change on "checkout".
	var changedResources []model.ResourceRef
	for _, o := range in.Observations {
		if o.Kind == "deployment.state" {
			changedResources = append(changedResources, o.Resource)
		}
	}
	hasRelatedChange := func(r model.ResourceRef) bool {
		for _, c := range changedResources {
			if c.Namespace != r.Namespace {
				continue
			}
			if c.Name == r.Name || strings.Contains(r.Name, c.Name) || strings.Contains(c.Name, r.Name) {
				return true
			}
		}
		return false
	}
	for key := range keys {
		var terms []score.EvidenceTerm
		var evs []model.Evidence

		// Git commit evidence: the strongest regression signal.
		var commit *model.Observation
		if c, ok := commits[key]; ok {
			commit = &c
		}
		res := model.ResourceRef{}
		if commit != nil {
			res = commit.Resource
		} else if g, ok := gitops[key]; ok {
			res = g.Resource
		} else if ch, ok := changes[key]; ok {
			res = ch.Resource
		}
		if commit != nil {
			sha, _ := commit.Payload["sha"].(string)
			msg, _ := commit.Payload["message"].(string)
			e := model.Evidence{
				ID:     fmt.Sprintf("configregression.%s.commit", resName(res)),
				Claim:  "git commit touching the workload",
				Weight: weightCommit, Strength: 1.0,
			}
			evs = append(evs, e)
			terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: fmt.Sprintf("commit %s: %s", sha, firstLine(msg)), Weight: weightCommit, Strength: 1.0, Polarity: +1})

			// Recency: commit close before the onset is the regression trigger.
			if !onset.IsZero() {
				age := onset.Sub(commit.Timestamp)
				if age >= 0 && age <= maxRegressionAge {
					eRec := model.Evidence{
						ID:     fmt.Sprintf("configregression.%s.recency", resName(res)),
						Claim:  "commit immediately before the incident",
						Weight: weightRecency, Strength: 1.0,
					}
					evs = append(evs, eRec)
					terms = append(terms, score.EvidenceTerm{ID: eRec.ID, Label: fmt.Sprintf("commit %d min before onset", int(age.Minutes())), Weight: weightRecency, Strength: 1.0, Polarity: +1})
				}
			}
		}

		// GitOps state evidence (only when it indicates trouble).
		if g, ok := gitops[key]; ok && gitopsTroubled(g) {
			cond, _ := g.Payload["condition"].(string)
			e := model.Evidence{
				ID:     fmt.Sprintf("configregression.%s.gitops", resName(res)),
				Claim:  "GitOps controller reports a problem",
				Weight: weightGitOps, Strength: 1.0,
			}
			evs = append(evs, e)
			terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: fmt.Sprintf("gitops: %s", cond), Weight: weightGitOps, Strength: 1.0, Polarity: +1})
		}

		// Cluster-observed change on the workload itself (name-overlap
		// tolerant: a gitops object may be named checkout-app while the
		// deployment is checkout).
		if _, ok := changes[key]; ok || hasRelatedChange(res) {
			e := model.Evidence{
				ID:     fmt.Sprintf("configregression.%s.change", resName(res)),
				Claim:  "workload changed around the incident",
				Weight: weightChange, Strength: 1.0,
			}
			evs = append(evs, e)
			terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: "workload observed changed in window", Weight: weightChange, Strength: 1.0, Polarity: +1})
		}

		// Mechanism: the failure follows the change. This is what makes the
		// regression hypothesis the *explanation* rather than a coincidence:
		// change applied → symptom observed, temporally ordered.
		eSymptom := model.Evidence{
			ID:     fmt.Sprintf("configregression.%s.symptom", resName(res)),
			Claim:  "failure observed after the change",
			Weight: weightSymptom, Strength: 1.0,
		}
		evs = append(evs, eSymptom)
		terms = append(terms, score.EvidenceTerm{ID: eSymptom.ID, Label: "mechanism: failure follows the change", Weight: weightSymptom, Strength: 1.0, Polarity: +1})

		if len(terms) == 0 {
			continue
		}

		missing := 0
		if commit == nil {
			missing = 1 // no git evidence — regression claim rests on weaker signals
		}

		claim := "Configuration regression: a change preceded the incident"
		if commit != nil {
			sha, _ := commit.Payload["sha"].(string)
			msg, _ := commit.Payload["message"].(string)
			claim = fmt.Sprintf("Configuration regression: commit %s (%s) preceded the failure", sha, firstLine(msg))
		}
		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("configregression.%s", resName(res)),
			Analyzer:    a.ID(),
			Severity:    model.SevHigh,
			Title:       "Configuration regression suspected",
			Description: claim,
			Timestamp:   timestampOf(commit, gitops[key], changes[key]),
		})

		h := model.Hypothesis{
			ID:       fmt.Sprintf("configregression.%s", resName(res)),
			Claim:    claim,
			Category: model.CatConfig,
			Status:   model.StatusLikely,
			Score:    breakdown(terms, missing),
		}
		for _, e := range evs {
			h.Evidence = append(h.Evidence, e.ID)
		}
		if missing > 0 {
			h.Missing = append(h.Missing, fmt.Sprintf("configregression.%s.commit", resName(res)))
		}
		hypotheses = append(hypotheses, h)
		evidence = append(evidence, evs...)
	}
	return findings, hypotheses, evidence, nil
}

// onsetOf finds the earliest failure signal (termination or waiting state) —
// the regression's "onset" is when the workload started failing, not when it
// was created.
func onsetOf(obs []model.Observation) time.Time {
	var onset time.Time
	for _, o := range obs {
		switch o.Kind {
		case "container.terminated", "container.waiting":
			if onset.IsZero() || o.Timestamp.Before(onset) {
				onset = o.Timestamp
			}
		}
	}
	return onset
}

// hasSymptom reports whether the workload is failing (a regression claim
// needs a symptom to explain).
func hasSymptom(obs []model.Observation) bool {
	for _, o := range obs {
		switch o.Kind {
		case "container.terminated":
			return true
		case "container.waiting":
			return true
		case "pod.state":
			phase, _ := o.Payload["phase"].(string)
			if phase == "Pending" {
				return true
			}
		}
	}
	return false
}

func gitopsTroubled(o model.Observation) bool {
	cond, _ := o.Payload["condition"].(string)
	if strings.Contains(cond, "False") || strings.Contains(cond, "Failed") {
		return true
	}
	health, _ := o.Payload["health"].(string)
	return health == "Degraded" || health == "Missing"
}

func resName(r model.ResourceRef) string { return r.Name }

// timestampOf picks the newest relevant timestamp for the finding.
func timestampOf(commit *model.Observation, gitops, change model.Observation) (t time.Time) {
	pick := func(o model.Observation) {
		if o.Timestamp.After(t) {
			t = o.Timestamp
		}
	}
	if commit != nil {
		pick(*commit)
	}
	if !gitops.Timestamp.IsZero() {
		pick(gitops)
	}
	if !change.Timestamp.IsZero() {
		pick(change)
	}
	return t
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func breakdown(terms []score.EvidenceTerm, missing int) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, missing)
	return &bd
}

func (a *Analyzer) NeedsEvidence(_ model.Hypothesis) []analyze.EvidenceRequest { return nil }

func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s: %s", f.Title, f.Description)
}
