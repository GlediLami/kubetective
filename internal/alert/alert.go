// Package alert turns PagerDuty / Grafana / Slack webhook payloads into an
// investigation request (roadmap v1.0 integration surfaces). It is inbound
// only: it parses a payload and extracts the Kubernetes target, so wiring a
// webhook costs zero API keys - the engine investigates with its existing
// cluster access, and the opt-in completion webhook reports back out.
//
// Payload support is best-effort by design: alert schemas vary between
// product versions, so extraction degrades to a readable error instead of
// guessing. See Parse and the per-provider sections for the supported
// fields.
package alert

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

// Kind is a supported alert provider.
type Kind string

const (
	PagerDuty Kind = "pagerduty"
	Grafana   Kind = "grafana"
	Slack     Kind = "slack"
)

// ErrNoTarget is returned when a well-formed payload carries no Kubernetes
// target to investigate.
var ErrNoTarget = errors.New("no Kubernetes target in payload")

// Payload is the parsed investigation seed.
type Payload struct {
	Kind        Kind
	Title       string
	Description string
	Target      model.ResourceRef
	Window      time.Duration // 0 = caller default
	SourceID    string        // provider-side alert identifier, if any
}

// Parse extracts an investigation seed from a provider webhook payload.
// The kind argument selects the parser; the payload self-describes by
// provider format (no extra configuration).
func Parse(kind Kind, body []byte) (*Payload, error) {
	var (
		p   *Payload
		err error
	)
	switch kind {
	case PagerDuty:
		p, err = parsePagerDuty(body)
	case Grafana:
		p, err = parseGrafana(body)
	case Slack:
		p, err = parseSlack(body)
	default:
		return nil, fmt.Errorf("unknown alert type %q (want pagerduty, grafana, slack)", kind)
	}
	if err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Payload) validate() error {
	if (p.Target == model.ResourceRef{}) {
		return ErrNoTarget
	}
	return nil
}

// --- PagerDuty -------------------------------------------------------------
//
// Supported: the v2 webhook firehose shape (event_type incident.*) and the
// Events API v2 push shape. The Kubernetes target is read from, in order:
//
//   data.incident.title                 e.g. "pod checkout-7f84c9 in CrashLoopBackOff"
//   data.incident.impacted_services[].  "checkout" (pod or deployment name)
//   data.service.summary
//   event.data.details.{pod,namespace}  custom k8s source fields
//   data.incident.trigger_summary_data.subject
//
// Titles are the most reliable carrier for an on-call workflow, so they win
// over service names.

type pdWebhook struct {
	Event json.RawMessage `json:"event"`
	Data  struct {
		Incident struct {
			ID                 string         `json:"id"`
			Title              string         `json:"title"`
			TriggerSummaryData map[string]any `json:"trigger_summary_data"`
			ImpactedServices   []struct {
				Summary string `json:"summary"`
			} `json:"impacted_services"`
		} `json:"incident"`
		Service struct {
			Summary string `json:"summary"`
		} `json:"service"`
	} `json:"data"`
}

type pdEventV2 struct {
	Event struct {
		ID   string `json:"id"`
		Data struct {
			Details map[string]any `json:"details"`
		} `json:"data"`
	} `json:"event"`
}

func parsePagerDuty(body []byte) (*Payload, error) {
	p := &Payload{Kind: PagerDuty}
	var w pdWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("pagerduty payload: %w", err)
	}
	p.Title = strings.TrimSpace(w.Data.Incident.Title)
	p.SourceID = firstNonEmpty(p.SourceID, w.Data.Incident.ID)

	if p.Title == "" {
		// No incident title: try the Events API *push* shape (event is an
		// object, not a string) which carries custom k8s details fields.
		var ev pdEventV2
		if err := json.Unmarshal(body, &ev); err == nil {
			p.SourceID = firstNonEmpty(p.SourceID, ev.Event.ID)
			for _, key := range []string{"pod", "pod_name", "kubernetes_pod_name"} {
				if s, ok := ev.Event.Data.Details[key].(string); ok && s != "" {
					p.Target = model.ResourceRef{Kind: "pod", Name: s}
				}
			}
			if p.Target.Name == "" {
				for _, key := range []string{"deployment", "workload", "statefulset"} {
					if s, ok := ev.Event.Data.Details[key].(string); ok && s != "" {
						p.Target = model.ResourceRef{Kind: key, Name: s}
						break
					}
				}
			}
		}
	}

	var candidates []string
	if p.Title != "" {
		candidates = append(candidates, p.Title)
	}
	if len(w.Data.Incident.ImpactedServices) > 0 {
		candidates = append(candidates, w.Data.Incident.ImpactedServices[0].Summary)
	}
	if w.Data.Service.Summary != "" {
		candidates = append(candidates, w.Data.Service.Summary)
	}
	if s, ok := w.Data.Incident.TriggerSummaryData["subject"].(string); ok && s != "" {
		candidates = append(candidates, s)
	}
	if (p.Target == model.ResourceRef{}) {
		for _, c := range candidates {
			if ref, ok := targetFromText(c); ok {
				p.Target = ref
				break
			}
		}
	}
	return p, nil
}

// --- Grafana ---------------------------------------------------------------
//
// Supported: the legacy notification webhook (title/state/evalMatches) and
// the unified alerting webhook (title/state/alerts[].labels). Kubernetes
// labels are the primary target source:
//
//   kubernetes_pod_name / pod          -> pod target
//   kubernetes_deployment_name / deployment_name / deployment / workload
//   kubernetes_namespace_name / namespace -> namespace (or scopes the above)
//
// evalMatches[].tags and alerts[].labels are scanned in order; the first
// label set with a workload keeps it. ruleName/text fall back to free-text
// target extraction.

type grafanaLegacy struct {
	Title      string `json:"title"`
	State      string `json:"state"`
	Message    string `json:"message"`
	RuleName   string `json:"ruleName"`
	RuleURL    string `json:"ruleUrl"`
	EvalMatches []struct {
		Tags map[string]any `json:"tags"`
	} `json:"evalMatches"`
}

type grafanaUnified struct {
	Title   string `json:"title"`
	State   string `json:"state"`
	Message string `json:"message"`
	RuleID  string `json:"ruleId"`
	Alerts  []struct {
		Status string         `json:"status"`
		Labels map[string]any `json:"labels"`
	} `json:"alerts"`
}

func parseGrafana(body []byte) (*Payload, error) {
	p := &Payload{Kind: Grafana}
	var un grafanaUnified
	if err := json.Unmarshal(body, &un); err != nil {
		return nil, fmt.Errorf("grafana payload: %w", err)
	}
	if len(un.Alerts) > 0 {
		p.Title = strings.TrimSpace(un.Title)
		p.SourceID = un.RuleID
		p.Description = strings.TrimSpace(un.Message)
		for _, a := range un.Alerts {
			if ref, ok := targetFromLabels(a.Labels); ok {
				p.Target = ref
				break
			}
		}
		if (p.Target == model.ResourceRef{}) {
			for _, c := range []string{un.Title, un.Message} {
				if ref, ok := targetFromText(c); ok {
					p.Target = ref
					break
				}
			}
		}
		return p, nil
	}
	var lg grafanaLegacy
	if err := json.Unmarshal(body, &lg); err != nil {
		return nil, fmt.Errorf("grafana payload: %w", err)
	}
	if lg.Title == "" && lg.State == "" {
		return nil, errors.New("grafana payload: neither unified alerts[] nor legacy title found")
	}
	p.Title = strings.TrimSpace(lg.Title)
	p.Description = strings.TrimSpace(lg.Message)
	for _, m := range lg.EvalMatches {
		if ref, ok := targetFromLabels(m.Tags); ok {
			p.Target = ref
			break
		}
	}
	if (p.Target == model.ResourceRef{}) {
		for _, c := range []string{lg.Title, lg.Message, lg.RuleName} {
			if ref, ok := targetFromText(c); ok {
				p.Target = ref
				break
			}
		}
	}
	return p, nil
}

// --- Slack ----------------------------------------------------------------
//
// Supported: the slash-command payload (what a /kubetective command posts)
// and the outgoing-webhook text shape. The command text is the argument
// string, e.g. "deployment/checkout since=2h"; the plain text shape carries
// the same string in "text". Read-only by construction: it only starts an
// investigation and never mutates the cluster without approval.

type slackCommand struct {
	Token       string `json:"token"`
	TeamDomain  string `json:"team_domain"`
	UserName    string `json:"user_name"`
	Command     string `json:"command"`
	Text        string `json:"text"`
	TriggerID   string `json:"trigger_id"`
	ResponseURL string `json:"response_url"`
}

func parseSlack(body []byte) (*Payload, error) {
	p := &Payload{Kind: Slack}
	var sc slackCommand
	if err := json.Unmarshal(body, &sc); err != nil {
		return nil, fmt.Errorf("slack payload: %w", err)
	}
	text := strings.TrimSpace(sc.Text)
	if sc.UserName == "" && text == "" {
		return nil, fmt.Errorf("slack payload: no command text found")
	}
	p.Description = text
	p.SourceID = firstNonEmpty(sc.TriggerID, sc.TeamDomain)
	ref, ok := targetFromText(text)
	if !ok {
		if token, single := singleToken(text); single {
			ref = model.ResourceRef{Kind: "pod", Name: token}
			ok = true
		}
	}
	if !ok {
		return p, nil // ErrNoTarget via validate: no workload in the text
	}
	p.Target = ref
	if since := sinceFromText(text); since > 0 {
		p.Window = since
	}
	return p, nil
}

// --- shared extraction helpers ---------------------------------------------

// labelKeys are ordered so workload kinds win over namespace-only targets.
type labelHint struct {
	key string
	typ string
}

var labelKeys = []labelHint{
	{"kubernetes_pod_name", "pod"},
	{"pod_name", "pod"},
	{"pod", "pod"},
	{"kubernetes_deployment_name", "deployment"},
	{"deployment_name", "deployment"},
	{"deployment", "deployment"},
	{"workload", "deployment"},
	{"statefulset", "statefulset"},
	{"kubernetes_namespace_name", "namespace"},
	{"namespace", "namespace"},
}

// targetFromLabels resolves a Grafana-style label map to a target. A
// workload label wins and scopes the namespace into the 3-part target form
// (kind/ns/name) that incident records store; namespace alone yields a
// namespace-scoped investigation.
func targetFromLabels(labels map[string]any) (model.ResourceRef, bool) {
	ns, _ := labels["kubernetes_namespace_name"].(string)
	if ns == "" {
		ns, _ = labels["namespace"].(string)
	}
	for _, k := range labelKeys {
		if k.typ == "namespace" {
			continue
		}
		if s, ok := labels[k.key].(string); ok && s != "" {
			return model.ResourceRef{Kind: k.typ, Namespace: ns, Name: s}, true
		}
	}
	if ns != "" {
		return model.ResourceRef{Kind: "namespace", Namespace: ns, Name: ns}, true
	}
	return model.ResourceRef{}, false
}

// kindNameRE recognizes "deployment/checkout", "pod checkout-7f84c9" etc.
var (
	kindSlashRE = regexp.MustCompile(`(?:^|\s|\()((?:deployment|pod|daemonset|statefulset|service|pvc|hpa))/([a-z0-9][a-z0-9.-]{0,63})`)
	kindSpaceRE = regexp.MustCompile(`(?:^|\s|\()((?:deployment|pod|daemonset|statefulset|service))\s+([a-z0-9][a-z0-9.-]{0,63})`)
	barePodRE   = regexp.MustCompile(`(?:^|\s|\()([a-z0-9][a-z0-9.-]{0,63})`)
)

// targetFromText recognizes a k8s-style kind/name in free text. The bare
// name fallback is deliberately conservative: a name must look generated
// (contain a hyphen or a digit) so prose words like "back" or "now" are not
// mistaken for pods. Structured command text (Slack) relaxes this to a
// single token, see parseSlack.
func targetFromText(text string) (model.ResourceRef, bool) {
	clean := func(s string) string { return strings.Trim(s, ".,;:!?") }
	for _, m := range []struct {
		re *regexp.Regexp
		fn func([]string) (model.ResourceRef, bool)
	}{
		{kindSlashRE, func(g []string) (model.ResourceRef, bool) {
			return model.ResourceRef{Kind: g[1], Name: clean(g[2])}, true
		}},
		{kindSpaceRE, func(g []string) (model.ResourceRef, bool) {
			return model.ResourceRef{Kind: g[1], Name: clean(g[2])}, true
		}},
	} {
		if g := m.re.FindStringSubmatch(text); len(g) == 3 && g[1] != "" && g[2] != "" {
			return m.fn(g)
		}
	}
	if m := barePodRE.FindStringSubmatch(text); len(m) == 2 {
		name := clean(m[1])
		if strings.ContainsAny(name, "-0123456789") {
			return model.ResourceRef{Kind: "pod", Name: name}, true
		}
	}
	return model.ResourceRef{}, false
}

// singleToken reports whether text is exactly one resource name (no spaces),
// e.g. a Slack command argument like "checkout" or "shop-7f8842".
func singleToken(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" || strings.ContainsAny(t, " \t/") {
		return "", false
	}
	return t, true
}

// sinceFromText extracts "since=2h" / "since = 30m" durations from command
// text, mirroring the CLI flag grammar.
var sinceRE = regexp.MustCompile(`since\s*=\s*(\d+(?:\.\d+)?)([mhd])`)

func sinceFromText(text string) time.Duration {
	m := sinceRE.FindStringSubmatch(text)
	if len(m) != 3 {
		return 0
	}
	d, err := time.ParseDuration(m[1] + m[2])
	if err != nil {
		return 0
	}
	return d
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}