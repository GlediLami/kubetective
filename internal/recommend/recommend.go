// Package recommend implements the deterministic recommendation rule table:
// the top hypothesis's category maps to a risk-leveled, evidence-linked
// action (Phase 2, read-only). The LLM can paraphrase
// these later; it can never invent actions outside this table.
package recommend

import (
	"fmt"
	"strings"

	"github.com/GlediLami/kubetective/internal/model"
)

// ForTop returns recommendations for the top hypothesis. Evidence links are
// attached so every action is traceable to the observations behind it.
func ForTop(h *model.Hypothesis, target model.ResourceRef) []model.Recommendation {
	if h == nil || h.Score == nil {
		return nil
	}
	table := map[model.HypothesisCategory]rule{
		model.CatMemory: {
			Action: fmt.Sprintf("roll back %s to the last known-good revision", target.String()),
			Risk:   model.RiskMedium,
			Reason: "memory exhaustion after a change - rollback reverts the configuration that grew memory past the limit",
		},
		model.CatConfig: {
			Action: fmt.Sprintf("roll back %s - a commit/change immediately preceded the failure", target.String()),
			Risk:   model.RiskMedium,
			Reason: "configuration regression: the change is temporally correlated with the incident onset",
		},
		model.CatCrashLoop: {
			Action: fmt.Sprintf("inspect %s container logs, then fix or roll back the crashing revision", target.String()),
			Risk:   model.RiskMedium,
			Reason: "application exits repeatedly - logs (already captured in the record) show the exit cause",
		},
		model.CatImage: {
			Action: "verify the image tag exists and registry credentials are valid",
			Risk:   model.RiskLow,
			Reason: "image pull failure - the referenced image/manifest was not found or is unauthorized",
		},
		model.CatScheduling: {
			Action: "right-size the pod requests or add capacity to the nodes",
			Risk:   model.RiskLow,
			Reason: "the scheduler cannot place the pod - requests exceed available node capacity",
		},
		model.CatNode: {
			Action: "reduce node pressure: evict non-critical workloads, free disk, or add nodes",
			Risk:   model.RiskHigh,
			Reason: "node-level pressure affects every workload on the node - act at the node, not the pod",
		},
		model.CatProbe: {
			Action: "fix the probe (path/port/endpoint) or the application endpoint it checks",
			Risk:   model.RiskLow,
			Reason: "probes fail while the process runs - traffic is withheld or pods are restarted unnecessarily",
		},
		model.CatPVC: {
			Action: "provision a storage class / PV for the claim or reduce the requested size",
			Risk:   model.RiskMedium,
			Reason: "the PersistentVolumeClaim cannot bind - pods stay pending",
		},
		model.CatService: {
			Action: "fix the service selector to match the pods' labels",
			Risk:   model.RiskMedium,
			Reason: "zero ready endpoints while pods run - the selector does not match",
		},
		model.CatHPA: {
			Action: "raise maxReplicas or fix the workload so replicas stay healthy",
			Risk:   model.RiskLow,
			Reason: "the HPA is pinned at its maximum - scale-out cannot compensate",
		},
	}
	r, ok := table[h.Category]
	if !ok {
		return nil
	}
	return []model.Recommendation{{
		ID:       fmt.Sprintf("rec-%s-%s", strings.ToLower(string(h.Category)), target.Name),
		Action:   r.Action,
		Risk:     r.Risk,
		Reason:   r.Reason,
		Evidence: h.Evidence,
	}}
}

type rule struct {
	Action string
	Risk   model.Risk
	Reason string
}
