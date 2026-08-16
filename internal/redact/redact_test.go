package redact

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

func sampleIncident() *model.Incident {
	base := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "acme-prod", Name: "checkout-7f84c9"}
	node := model.ResourceRef{Kind: "node", Name: "ip-10-0-3-17.eu-west-1.compute.internal"}
	return &model.Incident{
		ID: "incident-1",
		Meta: model.IncidentMeta{
			Target:    "pod/acme-prod/checkout-7f84c9",
			ClusterID: "acme-eu-prod-01",
		},
		Observations: []model.Observation{
			{
				ID: "o1", Kind: "pod.state", Timestamp: base, Resource: pod,
				Source:  model.SourceRef{System: "k8s", Query: "GET pods/checkout-7f84c9"},
				Payload: map[string]any{"phase": "Running", "restarts": int64(3), "node": node.Name},
			},
			{
				ID: "o2", Kind: "container.spec", Timestamp: base, Resource: pod,
				Payload: map[string]any{
					"container": "checkout",
					"image":     "registry.acme.internal/checkout:v41",
					"limits":    map[string]string{"memory": "1Gi"},
				},
			},
			{
				ID: "o3", Kind: "event.recorded", Timestamp: base, Resource: pod,
				Payload: map[string]any{
					"reason": "Failed",
					"message": "dial tcp 10.4.19.22:5432: connect refused; " +
						"DATABASE_URL=postgres://svc:hunter2@db.acme.internal/orders; " +
						"contact oncall@acme.example; token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijk",
				},
			},
			{
				ID: "o4", Kind: "git.commit", Timestamp: base, Resource: pod,
				Payload: map[string]any{
					"sha": "9f2c1a7d", "author": "Jane Roe", "email": "jane@acme.example",
					"message": "checkout: raise CACHE_SIZE", "workload": "checkout",
				},
			},
		},
		Result: &model.IncidentResultRecord{},
	}
}

func TestRedactPseudonymisesIdentifiers(t *testing.T) {
	out, rep := New(Options{}).Incident(sampleIncident())

	joined := dump(out)
	for _, leak := range []string{"acme-prod", "checkout-7f84c9", "acme-eu-prod-01",
		"registry.acme.internal", "jane@acme.example", "Jane Roe", "hunter2", "10.4.19.22"} {
		if strings.Contains(joined, leak) {
			t.Errorf("redacted record still contains %q", leak)
		}
	}
	if rep.Namespaces != 1 || rep.Workloads == 0 {
		t.Errorf("report = %+v, want at least one namespace and workload aliased", rep)
	}
	if out.Meta.ClusterID == "acme-eu-prod-01" {
		t.Error("cluster ID survived redaction")
	}
}

func TestRedactPreservesEngineSemantics(t *testing.T) {
	out, _ := New(Options{}).Incident(sampleIncident())

	// Structural values drive the diagnosis and must survive verbatim.
	if got := out.Observations[0].Payload["phase"]; got != "Running" {
		t.Errorf("phase = %v, want Running", got)
	}
	if got := out.Observations[2].Payload["reason"]; got != "Failed" {
		t.Errorf("reason = %v, want Failed", got)
	}
	if got := out.Observations[0].Payload["restarts"]; got != int64(3) {
		t.Errorf("restarts = %v, want 3", got)
	}
	if got := out.Observations[1].Payload["limits"].(map[string]string)["memory"]; got != "1Gi" {
		t.Errorf("memory limit = %v, want 1Gi", got)
	}
	// Kinds, IDs and timestamps are not identifying and anchor the timeline.
	if out.Observations[0].Kind != "pod.state" || out.Observations[0].ID != "o1" {
		t.Error("observation identity was rewritten")
	}
}

func TestRedactIsCorrelationPreserving(t *testing.T) {
	out, _ := New(Options{}).Incident(sampleIncident())

	// Every observation named the same pod; they must still agree afterwards.
	ns := out.Observations[0].Resource.Namespace
	name := out.Observations[0].Resource.Name
	for i, o := range out.Observations {
		if o.Resource.Namespace != ns || o.Resource.Name != name {
			t.Fatalf("observation %d landed on %s/%s, want %s/%s — correlation broken",
				i, o.Resource.Namespace, o.Resource.Name, ns, name)
		}
	}
	// The node named in a payload must match the alias used for the node
	// resource wherever else it appears.
	if got, ok := out.Observations[0].Payload["node"].(string); !ok || !strings.HasPrefix(got, "node-") {
		t.Errorf("node payload = %v, want a node alias", out.Observations[0].Payload["node"])
	}
}

func TestRedactScrubsSecretsInFreeText(t *testing.T) {
	out, rep := New(Options{}).Incident(sampleIncident())
	msg, _ := out.Observations[2].Payload["message"].(string)

	for _, want := range []string{"<ip>", "<email>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %s placeholder", msg, want)
		}
	}
	if strings.Contains(msg, "eyJhbGciOiJIUzI1NiJ9") {
		t.Error("JWT survived scrubbing")
	}
	if rep.Scrubbed["email"] == 0 || rep.Scrubbed["ipv4"] == 0 {
		t.Errorf("scrub report = %+v, want email and ipv4 counted", rep.Scrubbed)
	}
}

func TestRedactDropsDerivedResult(t *testing.T) {
	// The recorded result restates observations in prose; carrying it forward
	// would leak the originals through claim strings.
	out, _ := New(Options{}).Incident(sampleIncident())
	if out.Result != nil {
		t.Error("derived result survived redaction; replay must regenerate it")
	}
}

func TestRedactIsDeterministic(t *testing.T) {
	a, _ := New(Options{}).Incident(sampleIncident())
	b, _ := New(Options{}).Incident(sampleIncident())
	if dump(a) != dump(b) {
		t.Error("two redaction passes over identical input produced different output")
	}
}

func TestRedactKeepImagesOptOut(t *testing.T) {
	out, _ := New(Options{KeepImages: true}).Incident(sampleIncident())
	if got := out.Observations[1].Payload["image"]; got != "registry.acme.internal/checkout:v41" {
		t.Errorf("image = %v, want the original under KeepImages", got)
	}
}

func dump(inc *model.Incident) string {
	var b strings.Builder
	b.WriteString(inc.Meta.Target + "|" + inc.Meta.ClusterID + "\n")
	for _, o := range inc.Observations {
		b.WriteString(o.ID + o.Kind + o.Resource.Namespace + o.Resource.Name + o.Source.Query)
		keys := make([]string, 0, len(o.Payload))
		for k := range o.Payload {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(k)
			b.WriteString(strings.TrimSpace(strings.Join(strings.Fields(toString(o.Payload[k])), " ")))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := ""
		for _, k := range keys {
			out += k + t[k]
		}
		return out
	default:
		return ""
	}
}
