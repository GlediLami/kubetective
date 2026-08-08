// Package action implements Phase 3/4 of the remediation model
//: deterministic preview actions and human-approved
// application, with audit records appended to the incident file.
//
// Safety rules enforced here:
//   - actions are ALWAYS derived from the deterministic engine output
//     (top hypothesis + evidence), never from free-form model text;
//   - apply requires an explicit human approval flag at the CLI layer;
//   - every apply appends an audit record (user, timestamp, resource,
//     arguments, evidence, risk, approval, result) to the incident record;
//   - the planner is read-only — Plan() never touches the cluster.
package action

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

// Type of a remediation action.
type Type string

const (
	// Rollback reverts a Deployment to its previous revision.
	Rollback Type = "rollback"
	// RestartPod deletes a Pod so its controller recreates it.
	RestartPod Type = "restart-pod"
)

// Action is a deterministic, evidence-linked remediation step.
type Action struct {
	ID          string            `json:"id"`
	Type        Type              `json:"type"`
	Target      model.ResourceRef `json:"target"`
	Args        map[string]string `json:"args,omitempty"`
	Reason      string            `json:"reason"`
	Risk        model.Risk        `json:"risk"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
	// DryRun shows the kubectl-equivalent command (preview rendering).
	DryRun string `json:"dry_run"`
}

// Actionable categories: change/regression or crash behavior where reverting
// or restarting is the standard first move.
func rollbackable(cat model.HypothesisCategory) bool {
	switch cat {
	case model.CatMemory, model.CatConfig, model.CatCrashLoop, model.CatProbe:
		return true
	}
	return false
}

// Plan derives the preview action set from an investigation result.
// Deterministic, read-only, and derived solely from engine output.
func Plan(res *api.InvestigationResult) []Action {
	if res == nil || res.Incident == nil || len(res.Hypotheses) == 0 {
		return nil
	}
	top := res.Hypotheses[0] // engine ranks top hypothesis first
	var actions []Action

	// Rollback: top hypothesis is change/regression-related AND a deployment
	// change was detected near the incident.
	if rollbackable(top.Category) && top.Status != model.StatusRuledOut {
		for _, ch := range res.Changes {
			if ch.Resource.Kind != "deployment" {
				continue
			}
			act := Action{
				Type:        Rollback,
				Target:      ch.Resource,
				Args:        map[string]string{"revision": "previous"},
				Reason:      fmt.Sprintf("%s after a change — reverting the configuration that preceded the failure", top.Claim),
				Risk:        riskFor(top.Category),
				EvidenceIDs: evidenceIDs(top),
				DryRun:      fmt.Sprintf("kubectl rollout undo deployment/%s --dry-run=client", ch.Resource.Name),
			}
			act.ID = idOf(act)
			actions = append(actions, act)
			break // one rollback per investigation
		}
	}

	// Restart pod: pod-target investigations where the pod is in a bad state.
	if res.Incident.Target.Kind == "pod" {
		bad := res.Incident.Status == "OOMKILLED" || res.Incident.Status == "CRASHLOOPBACKOFF" || res.Incident.Status == "IMAGEPULLBACKOFF"
		if bad {
			act := Action{
				Type:        RestartPod,
				Target:      res.Incident.Target,
				Reason:      fmt.Sprintf("pod in state %s — a restart is the standard first recovery step", res.Incident.Status),
				Risk:        model.RiskLow,
				EvidenceIDs: evidenceIDs(top),
				DryRun:      fmt.Sprintf("kubectl delete pod/%s --dry-run=client", res.Incident.Target.Name),
			}
			act.ID = idOf(act)
			actions = append(actions, act)
		}
	}
	return actions
}

// riskFor maps the top category to a blast-radius-informed risk level.
func riskFor(cat model.HypothesisCategory) model.Risk {
	switch cat {
	case model.CatMemory, model.CatConfig:
		return model.RiskMedium
	case model.CatCrashLoop, model.CatProbe:
		return model.RiskLow
	}
	return model.RiskLow
}

func evidenceIDs(h model.Hypothesis) []string {
	// h.Evidence holds Evidence.IDs (the hypothesis engine's score-supporting
	// evidence list); the raw claims live in the breakdown lines.
	return append([]string(nil), h.Evidence...)
}

// idOf derives a stable, short action ID from type + target + args.
func idOf(a Action) string {
	sum := sha256.Sum256([]byte(string(a.Type) + "|" + a.Target.String() + "|" + fmt.Sprint(a.Args)))
	return fmt.Sprintf("act-%s", hex.EncodeToString(sum[:4]))
}
