// Package dns implements the DNS-failure analyzer: workloads that crash or
// hang because they cannot resolve names - most commonly because coreDNS /
// kube-dns is down or the sandbox cannot be created (v0.7: the "why" for
// crashloops whose events smell of DNS).
package dns

import (
	"context"
	"fmt"
	"strings"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/score"
)

const (
	weightCoreDNSDown = 30.0 // coreDNS / kube-dns deployment unavailable
	weightDNSEvents   = 25.0 // DNS-flavored events around the workload
	weightSymptom     = 20.0 // mechanism: workload failing after DNS errors
)

// dnsMarkers are kubelet/event strings that indicate a DNS-resolution
// problem rather than an application bug.
var dnsMarkers = []string{
	"failedtocreatepodsandbox",
	"network is not ready",
	"no such host",
	"nameserver",
	"coredns",
	"kube-dns",
	"dns lookup",
	"dns resolution",
	"dnsconfig",
	"resolv.conf",
}

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (a *Analyzer) ID() string   { return "dns" }
func (a *Analyzer) Name() string { return "DNS Failures" }

// NeedsEvidence: no adaptive requests - the collector always fetches
// coreDNS availability and events.
func (a *Analyzer) NeedsEvidence(_ model.Hypothesis) []analyze.EvidenceRequest { return nil }

// Explain renders the finding without an LLM.
func (a *Analyzer) Explain(f model.Finding) string {
	return fmt.Sprintf("%s - %s", f.Title, f.Description)
}

func (a *Analyzer) Supports(o model.Observation) bool {
	if o.Kind == "event.recorded" {
		if isDNSFlavored(o) {
			return true
		}
	}
	// Symptom observations (crash loop) - the analyzer needs them to confirm
	// the workload actually failed.
	if o.Kind == "container.waiting" {
		return o.Payload["reason"] == "CrashLoopBackOff"
	}
	if o.Kind == "container.terminated" {
		r, _ := o.Payload["reason"].(string)
		return r != "OOMKilled" && r != "Completed"
	}
	if o.Kind == "deployment.state" {
		return isCoreDNS(o)
	}
	return false
}

func isCoreDNS(o model.Observation) bool {
	if o.Resource.Namespace != "kube-system" {
		return false
	}
	n := strings.ToLower(o.Resource.Name)
	return n == "coredns" || n == "kube-dns" || strings.Contains(n, "coredns")
}

func isDNSFlavored(o model.Observation) bool {
	hay := strings.ToLower(o.Kind + " " + reasonOf(o) + " " + messageOf(o))
	for _, m := range dnsMarkers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

func reasonOf(o model.Observation) string {
	s, _ := o.Payload["reason"].(string)
	return s
}

func messageOf(o model.Observation) string {
	s, _ := o.Payload["message"].(string)
	return s
}

func (a *Analyzer) Analyze(_ context.Context, in *analyze.AnalysisInput) ([]model.Finding, []model.Hypothesis, []model.Evidence, error) {
	var (
		dnsEvents []model.Observation
		symptom   bool
		coredns   []model.Observation
	)
	for _, o := range in.Observations {
		switch {
		case o.Kind == "event.recorded" && isDNSFlavored(o):
			dnsEvents = append(dnsEvents, o)
		case isCoreDNS(o) && o.Kind == "deployment.state":
			coredns = append(coredns, o)
		case (o.Kind == "container.waiting" && o.Payload["reason"] == "CrashLoopBackOff") ||
			(o.Kind == "container.terminated" && o.Payload["reason"] != "OOMKilled" && o.Payload["reason"] != "Completed"):
			symptom = true
		}
	}
	if len(dnsEvents) == 0 && len(coredns) == 0 {
		return nil, nil, nil, nil
	}
	// The claim is about the workload whose events we saw; fall back to the
	// first affected resource.
	res := model.ResourceRef{Kind: "pod", Name: "unknown"}
	if len(dnsEvents) > 0 {
		res = dnsEvents[0].Resource
	}

	down := false
	for _, o := range coredns {
		if available, ok := analyze.PayloadInt64(o.Payload, "available_replicas"); ok && available == 0 {
			down = true
			break
		}
	}

	var evs []model.Evidence
	var terms []score.EvidenceTerm

	if down {
		e := model.Evidence{ID: fmt.Sprintf("dns.%s.coredns", res.Name), Claim: "coreDNS/kube-dns unavailable (0 ready replicas)", Weight: weightCoreDNSDown, Strength: 1.0}
		evs = append(evs, e)
		terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: "coreDNS/kube-dns unavailable", Weight: weightCoreDNSDown, Strength: 1.0, Polarity: +1})
	}
	if len(dnsEvents) > 0 {
		e := model.Evidence{ID: fmt.Sprintf("dns.%s.events", res.Name), Claim: "DNS-resolution errors around the workload", Weight: weightDNSEvents, Strength: 1.0}
		evs = append(evs, e)
		terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: "DNS-resolution errors observed", Weight: weightDNSEvents, Strength: 1.0, Polarity: +1})
	}
	if symptom {
		e := model.Evidence{ID: fmt.Sprintf("dns.%s.symptom", res.Name), Claim: "mechanism: workload failing after DNS errors", Weight: weightSymptom, Strength: 1.0}
		evs = append(evs, e)
		terms = append(terms, score.EvidenceTerm{ID: e.ID, Label: "mechanism: workload failing after DNS errors", Weight: weightSymptom, Strength: 1.0, Polarity: +1})
	}

	severity := model.SevWarning
	if down && symptom {
		severity = model.SevHigh
	}
	finding := model.Finding{
		ID:          fmt.Sprintf("dns.%s", res.Name),
		Analyzer:    a.ID(),
		Severity:    severity,
		Title:       "DNS resolution failure",
		Description: fmt.Sprintf("Workload %s cannot resolve names: coreDNS %v, %d DNS-flavored event(s), crash symptom %v", res.Name, map[bool]string{true: "DOWN", false: "up"}[down], len(dnsEvents), symptom),
		Evidence:    evidenceIDs(evs),
	}

	h := model.Hypothesis{
		ID:       fmt.Sprintf("dns.%s", res.Name),
		Claim:    fmt.Sprintf("DNS resolution failure: workload cannot resolve services (coreDNS %s, %d DNS error(s))", map[bool]string{true: "unavailable", false: "up"}[down], len(dnsEvents)),
		Category: model.CatDNS,
		Status:   model.StatusLikely,
		Score:    breakdown(terms),
		Evidence: evidenceIDs(evs),
	}
	return []model.Finding{finding}, []model.Hypothesis{h}, evs, nil
}

func evidenceIDs(evs []model.Evidence) []string {
	ids := make([]string, 0, len(evs))
	for _, e := range evs {
		ids = append(ids, e.ID)
	}
	return ids
}

func breakdown(terms []score.EvidenceTerm) *model.ScoreBreakdown {
	bd := score.Breakdown(model.Hypothesis{}, terms, 0)
	return &bd
}
