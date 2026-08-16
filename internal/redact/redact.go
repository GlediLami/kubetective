// Package redact removes identifying and secret material from a recorded
// incident so it can be shared — attached to a bug report, contributed as a
// benchmark scenario, or pasted into a postmortem.
//
// The problem this solves: `kubetective record` captures a real cluster.
// Namespaces and workload names identify the organisation; event and log text
// carries hostnames, internal URLs, customer identifiers, and occasionally
// credentials. A recording is safe to keep locally and unsafe to send anywhere,
// and nothing in the tool previously marked that boundary.
//
// Two mechanisms, deliberately different in kind:
//
//	Pseudonymisation — identifiers (namespace, workload, node, container,
//	image) are replaced with sequential aliases assigned in first-appearance
//	order: prod/checkout-7f84c9 → ns-1/pod-1. Deterministic, non-reversible,
//	and correlation-preserving, so the redacted record still replays to the
//	same verdict. Sequential rather than hashed on purpose: a hash of a short
//	name is a dictionary attack away from the original.
//
//	Scrubbing — free text (event messages, log snippets, commit subjects) is
//	pattern-matched for emails, IPs, URLs, tokens, and keys, and the matches
//	are replaced with typed placeholders. Free text cannot be pseudonymised
//	structurally, so this half is best-effort by nature, and the Report says so.
//
// The engine is never told a record was redacted: analyzers read pseudonyms as
// ordinary names, so replay exercises the same code path as the original.
package redact

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/GlediLami/kubetective/internal/model"
)

// Options controls how aggressively a record is cleaned.
type Options struct {
	// KeepImages leaves container image references intact. Registry hostnames
	// identify the organisation, so this is off by default; benchmark
	// scenarios where the image is the incident (imagepull) may want it on.
	KeepImages bool
	// KeepMessages leaves free text untouched. Off by default. Turning it on
	// keeps event and log text readable at the cost of every guarantee this
	// package makes about secrets.
	KeepMessages bool
}

// Report summarises what a redaction pass changed, so the user can verify
// before sharing rather than trusting the tool.
type Report struct {
	Observations int
	Namespaces   int
	Workloads    int
	Nodes        int
	Containers   int
	Images       int
	// Scrubbed counts pattern hits by kind ("email", "ipv4", …).
	Scrubbed map[string]int
	// TextFields is how many free-text fields were rewritten.
	TextFields int
}

func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d observations processed\n", r.Observations)
	fmt.Fprintf(&b, "  pseudonymised: %d namespace(s), %d workload(s), %d node(s), %d container(s), %d image(s)\n",
		r.Namespaces, r.Workloads, r.Nodes, r.Containers, r.Images)
	fmt.Fprintf(&b, "  free text: %d field(s) rewritten\n", r.TextFields)
	if len(r.Scrubbed) == 0 {
		fmt.Fprintf(&b, "  secrets: no pattern matches\n")
		return b.String()
	}
	keys := make([]string, 0, len(r.Scrubbed))
	for k := range r.Scrubbed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&b, "  secrets scrubbed:\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "    %-14s ×%d\n", k, r.Scrubbed[k])
	}
	return b.String()
}

// scrubbers are ordered: the most specific patterns run first so a private key
// is not partially eaten by the base64 rule.
var scrubbers = []struct {
	name    string
	re      *regexp.Regexp
	replace string
}{
	{"private-key", regexp.MustCompile(`(?s)-----BEGIN[^-]*PRIVATE KEY-----.*?-----END[^-]*PRIVATE KEY-----`), "<private-key>"},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "<jwt>"},
	{"aws-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "<aws-key>"},
	{"bearer", regexp.MustCompile(`(?i)\b(bearer|token|api[-_]?key|password|passwd|secret)\b\s*[:=]\s*\S+`), "$1=<redacted>"},
	{"url", regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"']+`), "<url>"},
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`), "<email>"},
	{"ipv4", regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b`), "<ip>"},
	// Long opaque strings are the residual risk: base64 blobs and hex digests
	// that no named pattern caught.
	{"opaque", regexp.MustCompile(`\b[A-Za-z0-9+/]{40,}={0,2}\b`), "<opaque>"},
}

// Well-known Kubernetes names are not organisational identifiers — every
// cluster has a kube-system namespace and a coredns deployment. Pseudonymising
// them removes no information about who runs the cluster and does remove
// information the engine needs: the DNS analyzer recognises coreDNS by name,
// so aliasing it turns a DNS outage into an ordinary crash loop.
//
// The rule is identity, not convenience: a name shared by every Kubernetes
// installation cannot identify one.
var preservedNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
	"default":         true,
}

var preservedWorkloads = map[string]bool{
	"coredns":                 true,
	"kube-dns":                true,
	"kube-proxy":              true,
	"etcd":                    true,
	"kube-apiserver":          true,
	"kube-scheduler":          true,
	"kube-controller-manager": true,
	"metrics-server":          true,
}

// preservedWorkload matches the analyzer-side convention that a name
// *containing* coredns counts (coredns-5d78c9, coredns-autoscaler).
func preservedWorkload(name string) bool {
	n := strings.ToLower(name)
	if preservedWorkloads[n] {
		return true
	}
	for known := range preservedWorkloads {
		if strings.HasPrefix(n, known+"-") {
			return true
		}
	}
	return false
}

// aliaser hands out stable sequential pseudonyms per category.
type aliaser struct {
	prefix string
	seen   map[string]string
	n      int
	// preserve marks names that pass through unchanged — see
	// preservedNamespaces / preservedWorkloads.
	preserve func(string) bool
}

func newAliaser(prefix string) *aliaser {
	return &aliaser{prefix: prefix, seen: map[string]string{}}
}

func newPreservingAliaser(prefix string, preserve func(string) bool) *aliaser {
	a := newAliaser(prefix)
	a.preserve = preserve
	return a
}

func (a *aliaser) get(original string) string {
	if original == "" {
		return ""
	}
	if a.preserve != nil && a.preserve(original) {
		return original
	}
	if v, ok := a.seen[original]; ok {
		return v
	}
	a.n++
	v := fmt.Sprintf("%s-%d", a.prefix, a.n)
	a.seen[original] = v
	return v
}

// count is the number of distinct originals aliased, including children
// assigned directly by prepareWorkloads rather than through get().
func (a *aliaser) count() int { return len(a.seen) }

// Redactor holds the alias tables for one document, so the same name always
// maps to the same pseudonym across every observation in the record.
type Redactor struct {
	opts       Options
	namespaces *aliaser
	workloads  *aliaser
	nodes      *aliaser
	containers *aliaser
	images     *aliaser
	report     Report
}

func New(opts Options) *Redactor {
	return &Redactor{
		opts:       opts,
		namespaces: newPreservingAliaser("ns", func(n string) bool { return preservedNamespaces[n] }),
		workloads:  newPreservingAliaser("workload", preservedWorkload),
		nodes:      newAliaser("node"),
		containers: newAliaser("container"),
		images:     newAliaser("image"),
		report:     Report{Scrubbed: map[string]int{}},
	}
}

// prepareWorkloads pre-assigns workload aliases so that naming *relationships*
// survive redaction, not just naming consistency.
//
// Kubernetes encodes ownership in names: deployment "checkout" owns pod
// "checkout-7f84c9". The config-regression analyzer reads that overlap to
// decide a commit touching the deployment is relevant to the pod. Aliasing the
// two independently — workload-1 and workload-2 — destroys the relationship and
// quietly costs the redacted record a point of confidence.
//
// Assigning shortest-first and deriving children from their parent's alias
// keeps the shape: checkout → workload-1, checkout-7f84c9 → workload-1-a.
func (r *Redactor) prepareWorkloads(inc *model.Incident) {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}
	for _, o := range inc.Observations {
		if !strings.EqualFold(o.Resource.Kind, "node") {
			add(o.Resource.Name)
		}
		for key, alias := range identityKeys {
			if alias != "workload" {
				continue
			}
			if v, ok := o.Payload[key].(string); ok {
				add(v)
			}
		}
	}
	// Shortest first so a parent is always aliased before its children; ties
	// broken lexically to stay deterministic.
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) < len(names[j])
		}
		return names[i] < names[j]
	})

	childSuffix := map[string]int{}
	for _, n := range names {
		if preservedWorkload(n) {
			continue // passes through unchanged; never a parent or a child
		}
		parent := r.longestAliasedPrefix(n)
		if parent == "" {
			r.workloads.get(n) // fresh top-level alias
			continue
		}
		base := r.workloads.seen[parent]
		childSuffix[base]++
		r.workloads.seen[n] = fmt.Sprintf("%s-%c", base, 'a'+childSuffix[base]-1)
	}
}

// longestAliasedPrefix returns the longest already-aliased name that is a
// prefix of n at a name boundary ("checkout" of "checkout-7f84c9", but not
// "check" of "checkout").
func (r *Redactor) longestAliasedPrefix(n string) string {
	best := ""
	for candidate := range r.workloads.seen {
		if len(candidate) >= len(n) || !strings.HasPrefix(n, candidate) {
			continue
		}
		if next := n[len(candidate)]; next != '-' && next != '.' {
			continue
		}
		if len(candidate) > len(best) || (len(candidate) == len(best) && candidate < best) {
			best = candidate
		}
	}
	return best
}

// Incident returns a redacted copy of the incident. The original is untouched.
func (r *Redactor) Incident(inc *model.Incident) (*model.Incident, Report) {
	if inc == nil {
		return nil, r.report
	}
	r.prepareWorkloads(inc)
	out := &model.Incident{
		ID:   inc.ID,
		Meta: inc.Meta,
	}
	// The target string names the workload under investigation.
	out.Meta.Target = r.resourceString(inc.Meta.Target)
	// A cluster ID is an organisational identifier in its own right.
	if out.Meta.ClusterID != "" {
		out.Meta.ClusterID = "cluster-1"
	}

	out.Observations = make([]model.Observation, 0, len(inc.Observations))
	for _, o := range inc.Observations {
		out.Observations = append(out.Observations, r.observation(o))
	}
	r.report.Observations = len(out.Observations)
	r.report.Namespaces = r.namespaces.count()
	r.report.Workloads = r.workloads.count()
	r.report.Nodes = r.nodes.count()
	r.report.Containers = r.containers.count()
	r.report.Images = r.images.count()

	// The recorded result is a derived view of observations that were just
	// rewritten; carrying it forward would leak the originals through claim
	// strings and evidence labels. Replay regenerates it.
	out.Result = nil
	return out, r.report
}

func (r *Redactor) observation(o model.Observation) model.Observation {
	out := o
	out.Resource = r.resource(o.Resource)
	out.Source = model.SourceRef{System: o.Source.System, Query: r.text(o.Source.Query)}
	out.Payload = r.payload(o.Payload)
	return out
}

func (r *Redactor) resource(ref model.ResourceRef) model.ResourceRef {
	out := ref
	out.Namespace = r.namespaces.get(ref.Namespace)
	if strings.EqualFold(ref.Kind, "node") {
		out.Name = r.nodes.get(ref.Name)
		return out
	}
	out.Name = r.workloads.get(ref.Name)
	return out
}

// resourceString rewrites a "kind/namespace/name" target string.
func (r *Redactor) resourceString(s string) string {
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 3:
		return strings.Join([]string{parts[0], r.namespaces.get(parts[1]), r.workloads.get(parts[2])}, "/")
	case 2:
		return strings.Join([]string{parts[0], r.workloads.get(parts[1])}, "/")
	}
	return s
}

// identityKeys are payload fields that name a thing rather than describe one.
// They are pseudonymised, never scrubbed, so correlation survives.
var identityKeys = map[string]string{
	"container":   "container",
	"node":        "node",
	"owner_name":  "workload",
	"workload":    "workload",
	"pod":         "workload",
	"volume_name": "workload",
}

// structuralKeys carry engine semantics ("OOMKilled", "Running", "MemoryPressure").
// Rewriting them would change the diagnosis, so they pass through untouched.
var structuralKeys = map[string]bool{
	"reason": true, "phase": true, "status": true, "type": true,
	"owner_kind": true, "kind": true, "metric": true, "unit": true,
	"health": true, "sync": true, "controller": true,
}

func (r *Redactor) payload(p map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	out := make(map[string]any, len(p))
	// Sorted traversal: aliases are numbered in the order they are first seen,
	// so a randomised map walk would make the output non-reproducible.
	for _, k := range sortedKeys(p) {
		v := p[k]
		switch val := v.(type) {
		case string:
			out[k] = r.payloadString(k, val)
		case map[string]string:
			m := make(map[string]string, len(val))
			for _, mk := range sortedStringKeys(val) {
				m[mk] = r.payloadString(mk, val[mk])
			}
			out[k] = m
		case map[string]any:
			out[k] = r.payload(val)
		case []any:
			list := make([]any, 0, len(val))
			for _, item := range val {
				if s, ok := item.(string); ok {
					list = append(list, r.payloadString(k, s))
					continue
				}
				list = append(list, item)
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *Redactor) payloadString(key, val string) string {
	if structuralKeys[key] {
		return val
	}
	switch key {
	case "image":
		if r.opts.KeepImages {
			return val
		}
		return r.images.get(val)
	case "sha", "revision":
		// A commit SHA identifies a repository as surely as its URL.
		return "0000000"
	case "author", "email":
		return "<redacted>"
	}
	if alias, ok := identityKeys[key]; ok {
		switch alias {
		case "node":
			return r.nodes.get(val)
		case "container":
			return r.containers.get(val)
		default:
			return r.workloads.get(val)
		}
	}
	return r.text(val)
}

// text scrubs free text and rewrites any identifier already aliased elsewhere
// in the document — a namespace named in a log line must match the namespace
// pseudonym in the resource ref, or the two stop correlating.
func (r *Redactor) text(s string) string {
	if s == "" {
		return s
	}
	if r.opts.KeepMessages {
		return s
	}
	before := s
	for _, sc := range scrubbers {
		if !sc.re.MatchString(s) {
			continue
		}
		r.report.Scrubbed[sc.name] += len(sc.re.FindAllString(s, -1))
		s = sc.re.ReplaceAllString(s, sc.replace)
	}
	s = r.replaceKnownNames(s)
	if s != before {
		r.report.TextFields++
	}
	return s
}

// replaceKnownNames swaps any already-aliased identifier appearing in free
// text for its pseudonym. Longest-first so "checkout-7f84c9" is replaced
// before "checkout".
func (r *Redactor) replaceKnownNames(s string) string {
	type pair struct{ from, to string }
	var pairs []pair
	for _, a := range []*aliaser{r.workloads, r.nodes, r.namespaces, r.containers} {
		for from, to := range a.seen {
			if len(from) < 3 {
				continue // too short to match safely
			}
			pairs = append(pairs, pair{from, to})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].from) != len(pairs[j].from) {
			return len(pairs[i].from) > len(pairs[j].from)
		}
		return pairs[i].from < pairs[j].from // stable across map walks
	})
	for _, p := range pairs {
		s = strings.ReplaceAll(s, p.from, p.to)
	}
	return s
}
