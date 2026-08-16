// Package nodepressure implements the node-pressure analyzer: it activates on
// node.condition observations (MemoryPressure/DiskPressure/PIDPressure) and
// builds the "node under pressure" hypothesis - the classic root cause that
// sits above per-pod symptoms.
package nodepressure

import (
	"context"
	"fmt"
	"strings"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/score"
)

const (
	weightCondition   = score.WeightDecisive   // node pressure is the root cause above per-pod symptoms
	weightCorroborate = score.WeightSupporting // second pressure type on the same node
	weightMessage     = score.WeightContextual
)

// pressureTypes are the node conditions this analyzer understands.
var pressureTypes = []string{"MemoryPressure", "DiskPressure", "PIDPressure"}

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "nodepressure" }
func (a *Analyzer) Name() string { return "Node Pressure" }

// StatusLabel: a node under pressure explains every per-pod symptom on it.
func (a *Analyzer) StatusLabel() string { return "NODEPRESSURE" }
func (a *Analyzer) Precedence() int     { return 8 }

func (a *Analyzer) Supports(o model.Observation) bool {
	if o.Kind != "node.condition" {
		return false
	}
	status, _ := o.Payload["status"].(string)
	if status != "True" {
		return false
	}
	typ, _ := o.Payload["type"].(string)
	return isPressure(typ)
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	type nodePressure struct {
		conditions []model.Observation
	}
	nodes := map[string]*nodePressure{}
	var order []string
	for _, o := range in.Observations {
		if !a.Supports(o) {
			continue
		}
		key := o.Resource.String()
		if nodes[key] == nil {
			nodes[key] = &nodePressure{}
			order = append(order, key)
		}
		nodes[key].conditions = append(nodes[key].conditions, o)
	}

	var findings []model.Finding
	var hypotheses []model.Hypothesis
	var evidence []model.Evidence

	for _, key := range order {
		n := nodes[key]
		res := n.conditions[0].Resource
		var types []string
		for _, c := range n.conditions {
			types = append(types, fmt.Sprintf("%v", c.Payload["type"]))
		}

		findings = append(findings, model.Finding{
			ID:          fmt.Sprintf("nodepressure.%s", res.Name),
			Analyzer:    a.ID(),
			Severity:    model.SevHigh,
			Title:       fmt.Sprintf("Node under pressure (%s)", strings.Join(types, ", ")),
			Description: fmt.Sprintf("node %s reports %s", res.Name, strings.Join(types, ", ")),
			Timestamp:   n.conditions[len(n.conditions)-1].Timestamp,
		})

		var terms []score.EvidenceTerm
		var evs []model.Evidence

		for i, c := range n.conditions {
			typ, _ := c.Payload["type"].(string)
			weight := weightCondition
			label := fmt.Sprintf("node condition: %s=True", typ)
			if i > 0 {
				weight = weightCorroborate
				label = fmt.Sprintf("corroborating: %s=True", typ)
			}
			e := model.Evidence{
				ID:     fmt.Sprintf("nodepressure.%s.%s", res.Name, strings.ToLower(typ)),
				Claim:  label,
				Weight: weight, Strength: 1.0,
			}
			evs = append(evs, e)
			terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: label, Weight: weight, Strength: 1.0, Polarity: +1})
		}
		var msg string
		for _, c := range n.conditions {
			if m, ok := c.Payload["message"].(string); ok && m != "" {
				msg = m
				break
			}
		}
		if msg != "" {
			e := model.Evidence{
				ID:     fmt.Sprintf("nodepressure.%s.message", res.Name),
				Claim:  "kubelet pressure message",
				Weight: weightMessage, Strength: 1.0,
			}
			evs = append(evs, e)
			terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: fmt.Sprintf("kubelet: %s", truncate(msg, 80)), Weight: weightMessage, Strength: 1.0, Polarity: +1})
		}

		h := model.Hypothesis{
			ID:       fmt.Sprintf("nodepressure.%s", res.Name),
			Claim:    fmt.Sprintf("Node %s is under pressure (%s)", res.Name, strings.Join(types, ", ")),
			Category: model.CatNode,
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

func isPressure(typ string) bool {
	for _, p := range pressureTypes {
		if p == typ {
			return true
		}
	}
	return false
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
	return fmt.Sprintf("%s: %s", f.Title, f.Description)
}
