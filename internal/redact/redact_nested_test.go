package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

// Real Kubernetes payloads are not flat. A deployment's status carries
// conditions[] as a list of objects, each with a free-text message naming the
// replicaset; an event message quotes the container image. Both survived
// redaction until a recording from an actual cluster exposed them - every
// hand-authored scenario had flat payloads and no image-in-message events.
func TestRedactRewritesNestedPayloads(t *testing.T) {
	base := time.Date(2026, 8, 16, 13, 30, 0, 0, time.UTC)
	dep := model.ResourceRef{Kind: "deployment", Namespace: "acme-prod", Name: "checkout"}
	pod := model.ResourceRef{Kind: "pod", Namespace: "acme-prod", Name: "checkout-6868f86cc4-phft8"}

	inc := &model.Incident{
		ID:   "incident-1",
		Meta: model.IncidentMeta{Target: "deployment/acme-prod/checkout"},
		Observations: []model.Observation{
			{
				ID: "o1", Kind: "deployment.state", Timestamp: base, Resource: dep,
				Payload: map[string]any{
					"replicas": int64(1),
					"conditions": []any{
						map[string]any{
							"type":    "Progressing",
							"status":  "True",
							"reason":  "NewReplicaSetAvailable",
							"message": `ReplicaSet "checkout-6868f86cc4" has successfully progressed.`,
						},
						map[string]any{
							"type": "Available", "status": "False",
							"message": "Deployment does not have minimum availability.",
						},
					},
				},
			},
			{
				ID: "o2", Kind: "container.spec", Timestamp: base, Resource: pod,
				Payload: map[string]any{"container": "checkout", "image": "registry.acme.internal/stress:latest"},
			},
			{
				ID: "o3", Kind: "event.recorded", Timestamp: base, Resource: pod,
				Payload: map[string]any{
					"reason":  "Pulled",
					"message": `Container image "registry.acme.internal/stress:latest" already present on machine`,
				},
			},
		},
	}

	out, _ := New(Options{}).Incident(inc)
	blob, err := json.Marshal(out.Observations)
	if err != nil {
		t.Fatal(err)
	}
	body := string(blob)

	for _, leak := range []string{
		"checkout-6868f86cc4",    // nested inside conditions[].message
		"registry.acme.internal", // quoted inside an event message
		"acme-prod",              // namespace anywhere
	} {
		if strings.Contains(body, leak) {
			t.Errorf("%q survived redaction:\n%s", leak, body)
		}
	}

	// Structure and engine-meaningful values must be intact.
	conds, ok := out.Observations[0].Payload["conditions"].([]any)
	if !ok || len(conds) != 2 {
		t.Fatalf("conditions list lost its shape: %#v", out.Observations[0].Payload["conditions"])
	}
	first, ok := conds[0].(map[string]any)
	if !ok {
		t.Fatalf("condition is no longer an object: %#v", conds[0])
	}
	if first["type"] != "Progressing" || first["status"] != "True" {
		t.Errorf("structural fields rewritten: %#v", first)
	}
	if msg, _ := first["message"].(string); !strings.Contains(msg, "successfully progressed") {
		t.Errorf("condition message lost its meaning: %q", msg)
	}
}
