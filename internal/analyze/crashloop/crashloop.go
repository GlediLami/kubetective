// Package crashloop implements the CrashLoopBackOff analyzer: it activates on
// container.waiting with reason CrashLoopBackOff (or repeated non-zero exits)
// and builds the "application crash loop" hypothesis.
package crashloop

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
	weightWaiting  = 30.0
	weightExitCode = 15.0
	weightRestarts = 10.0
	weightLogs     = 10.0 // container logs captured (adaptive collection)
	weightOOM      = 20.0 // contradiction: OOMKilled is the more specific explanation
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "crashloop" }
func (a *Analyzer) Name() string { return "CrashLoopBackOff" }

func (a *Analyzer) Supports(o model.Observation) bool {
	if o.Kind == "container.waiting" {
		return o.Payload["reason"] == "CrashLoopBackOff"
	}
	if o.Kind == "container.terminated" {
		if r, ok := o.Payload["reason"].(string); ok {
			return r != "OOMKilled" && r != "Completed"
		}
	}
	return false
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	type crash struct {
		waiting   bool
		terminals []model.Observation
		restarts  int64
	}
	groups := map[string]*crash{}
	var order []string

	for _, o := range in.Observations {
		if o.Kind == "container.waiting" && o.Payload["reason"] == "CrashLoopBackOff" {
			key := o.Resource.String()
			if groups[key] == nil {
				groups[key] = &crash{}
				order = append(order, key)
			}
			groups[key].waiting = true
		}
	}
	for _, o := range in.Observations {
		g := groups[o.Resource.String()]
		if g == nil {
			continue
		}
		if o.Kind == "container.terminated" {
			g.terminals = append(g.terminals, o)
		}
		if n, ok := analyze.PayloadInt64(o.Payload, "restarts"); ok {
			g.restarts = n
		}
	}

	// OOMKilled presence → contradiction (memory exhaustion is more specific).
	oomOnResource := map[string]bool{}
	for _, o := range in.Observations {
		if o.Kind == "container.terminated" && o.Payload["reason"] == "OOMKilled" {
			oomOnResource[o.Resource.String()] = true
		}
	}
	// Log snippets (adaptive collection): captured logs corroborate the crash
	// and are kept for human inspection.
	logLines := map[string]int{}
	for _, o := range in.Observations {
		if o.Kind == "log.snippet" {
			if n, ok := analyze.PayloadInt64(o.Payload, "line_count"); ok {
				logLines[o.Resource.String()] = int(n)
			}
		}
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, key := range order {
		g := groups[key]
		res := findResource(in.Observations, key)
		if len(g.terminals) > 0 {
			res = g.terminals[0].Resource
		}
		var codes []int64
		for _, t := range g.terminals {
			if c, ok := analyze.PayloadInt64(t.Payload, "exit_code"); ok {
				codes = append(codes, c)
			}
		}
		exitLabel := "exit code(s) unknown"
		if len(codes) > 0 {
			exitLabel = fmt.Sprintf("exit codes %v", codes)
		}

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("crashloop.%s", res.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevHigh,
			Title:       fmt.Sprintf("CrashLoopBackOff (restarts: %d)", g.restarts),
			Description: fmt.Sprintf("container keeps crashing and backing off; %s", exitLabel),
			Timestamp:   lastTs(g.terminals),
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		eWaiting := model.Evidence{
			ID:     fmt.Sprintf("crashloop.%s.waiting", res.Name),
			Claim:  "container stuck in CrashLoopBackOff",
			Weight: weightWaiting, Strength: 1.0,
		}
		evs = append(evs, eWaiting)
		terms = append(terms, score.EvidenceTerm{ID: eWaiting.ID, Label: "CrashLoopBackOff state", Weight: weightWaiting, Strength: 1.0, Polarity: +1})

		eExit := model.Evidence{
			ID:     fmt.Sprintf("crashloop.%s.exit", res.Name),
			Claim:  "repeated non-zero exit codes",
			Weight: weightExitCode, Strength: 1.0,
		}
		evs = append(evs, eExit)
		if len(codes) > 0 {
			terms = append(terms, score.EvidenceTerm{ID: eExit.ID, Label: fmt.Sprintf("non-zero exit codes: %v", codes), Weight: weightExitCode, Strength: 1.0, Polarity: +1})
		}

		eRestart := model.Evidence{
			ID:     fmt.Sprintf("crashloop.%s.restarts", res.Name),
			Claim:  "reproduced after restart",
			Weight: weightRestarts, Strength: 1.0,
		}
		evs = append(evs, eRestart)
		if g.restarts > 1 {
			terms = append(terms, score.EvidenceTerm{ID: eRestart.ID, Label: fmt.Sprintf("reproduced after restart (×%d)", g.restarts), Weight: weightRestarts, Strength: 1.0, Polarity: +1})
		}

		if oomOnResource[key] {
			eOOM := model.Evidence{
				ID:          fmt.Sprintf("crashloop.%s.oom", res.Name),
				Claim:       "OOMKilled observed — memory exhaustion is more specific",
				Contradicts: []string{},
				Weight:      weightOOM, Strength: 1.0,
			}
			evs = append(evs, eOOM)
			terms = append(terms, score.EvidenceTerm{ID: eOOM.ID, Label: "contradicting: OOMKilled present (more specific explanation)", Weight: weightOOM, Strength: 1.0, Polarity: -1})
		}
		if n := logLines[key]; n > 0 {
			eLogs := model.Evidence{
				ID:     fmt.Sprintf("crashloop.%s.logs", res.Name),
				Claim:  "container logs captured",
				Weight: weightLogs, Strength: 1.0,
			}
			evs = append(evs, eLogs)
			terms = append(terms, score.EvidenceTerm{ID: eLogs.ID, Label: fmt.Sprintf("container logs captured (%d lines)", n), Weight: weightLogs, Strength: 1.0, Polarity: +1})
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("crashloop.%s", res.Name),
			Claim:    fmt.Sprintf("Application crash loop: container exits repeatedly (%s)", exitLabel),
			Category: model.CatCrashLoop,
			Status:   model.StatusLikely,
			Score:    scoreBreakdown(terms),
		}
		for _, e := range evs {
			h.Evidence = append(h.Evidence, e.ID)
		}
		if len(codes) == 0 && logLines[key] == 0 {
			// Exit cause unknown and no logs captured yet → the adaptive loop
			// will request them.
			h.Missing = append(h.Missing, fmt.Sprintf("crashloop.%s.exit-logs", res.Name))
		}
		hypotheses = append(hypotheses, h)
		evidence = append(evidence, evs...)
	}
	return findings, hypotheses, evidence, nil
}

func scoreBreakdown(terms []score.EvidenceTerm) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, 0)
	return &bd
}

func findResource(obs []model.Observation, key string) model.ResourceRef {
	for _, o := range obs {
		if o.Resource.String() == key {
			return o.Resource
		}
	}
	return model.ResourceRef{Name: key}
}

func lastTs(ts []model.Observation) time.Time {
	var t time.Time
	for _, o := range ts {
		if o.Timestamp.After(t) {
			t = o.Timestamp
		}
	}
	return t
}

func (a *Analyzer) NeedsEvidence(h model.Hypothesis) []analyze.EvidenceRequest {
	// Exit cause unknown → container logs (tail) may reveal it; the adaptive
	// loop fetches them in a second collection round (docs/DESIGN.md §8.4).
	var out []analyze.EvidenceRequest
	for _, m := range h.Missing {
		if strings.HasSuffix(m, ".exit-logs") {
			out = append(out, analyze.EvidenceRequest{
				HypothesisID: h.ID,
				Description:  "container logs to find the crash exit cause",
				Collector:    "kubernetes",
				QueryHint:    "logs",
				Cost:         2,
			})
		}
	}
	return out
}

func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s: %s", f.Title, strings.ToLower(f.Description))
}
