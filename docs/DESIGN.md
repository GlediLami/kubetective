# KubeDoctor — Technical Design Document

> Status: **v0.0 (architecture draft)** · Source spec: `../prompt.txt` · License: Apache-2.0 (proposed)

---

## 1. Executive Summary

KubeDoctor is an **open-source Kubernetes incident investigation engine**. Given a target
(pod, deployment, namespace) and a time window, it collects *facts* from Kubernetes and
(optionally) observability backends, connects them into an **evidence graph** with an explicit
**timeline**, generates *hypotheses* for what happened, scores them with an **explainable,
deterministic model**, and renders an **evidence-backed explanation** — with the LLM as an
optional component, never as the pipeline.

The product bar, from the master prompt, is worth repeating:

> *"I don't want to operate Kubernetes without this installed."*

This document is the complete design: architecture, data model, scoring algorithm, analyzer
and collector APIs, security model, roadmap, and the open-source strategy. It also records
where this design **deviates from the master prompt** and why (§2).

---

## 2. Verdict & Key Deviations from the Master Prompt

The master prompt is unusually well-considered: *deterministic first, AI second*, an evidence
graph with typed edges, explainable scoring, a kubectl-plugin CLI, and a realistic phased
roadmap. I agree with the core thesis. A few things I would **change** — each is a deliberate
engineering decision, not cosmetics:

| # | Master prompt says | KubeDoctor does | Why |
|---|---|---|---|
| D1 | Roadmap: evidence graph + scoring in v0.3 | **v0.1/v0.2**: evidence model, graph, timeline, scoring are the core from day one | They are not features; they are the data model. Building analyzers without them means rebuilding them twice. |
| D2 | Incident replay in v0.7 | **v0.1**: every investigation is recorded as JSONL | Recording is ~50 lines of code and makes the benchmark, replay, and debugging possible from day one. You cannot improve what you cannot replay. |
| D3 | Controller-runtime in the stack | **client-go only** (no controller-runtime) | controller-runtime is for controllers/watches. KubeDoctor is pull-based, on-demand investigation. It adds manager plumbing, leader election, and cache machinery we do not need. |
| D4 | gRPC in v0.6 | **In-process Go API first; REST only when a server exists; gRPC optional later** | gRPC is interface complexity with zero benefit for a CLI-first tool. The CLI, REST, MCP, and web UI can all wrap the same Go `Engine` interface. |
| D5 | Storage: SQLite/DuckDB for MVP | **In-memory investigation graph + SQLite only for incident records** | An investigation is a *temporary graph* over a bounded window. DuckDB is justified only when we query historical incidents analytically (v0.8+). |
| D6 | `--predict` mode listed as near-term future | **Deferred to v1.x** | It is static manifest risk analysis — a different product. It looks great in a README and absorbs weeks. Ship diagnosis first. |
| D7 | "Investigation" as a vague pipeline stage | **Concrete: a staged collection loop** (scope → cheap collect → analyze → targeted collect → …) | An investigation is an *adaptive* process: collect broadly, then drill down where hypotheses point. Making this explicit is what keeps investigations <5s. |
| D8 | Collectors return raw data | **Collectors emit normalized `Observation`s; raw data never leaves the collector boundary** | This is the trust boundary (injection defense) and the portability boundary (Prometheus vs. nothing) at the same time. |
| D9 | Multi-tenant / cluster-wide server | **Out of scope until v0.8+** | RBAC-respecting *single-user, own-kubeconfig* CLI is the right first model. |
| D10 | Analyzer API with `Explain(finding)` | Keep, but add **`NeedsEvidence()`** — analyzers declare what evidence would strengthen/refute their hypothesis | This is what drives D7's adaptive collection and what makes hypothesis generation more than a list of canned strings. |

**What I kept unchanged** (because it is right): deterministic-first pipeline; typed graph
edges with `CAUSED_BY` reserved for strong evidence; explainable scoring with visible
breakdown; minimum-viable-evidence presentation ("collect more than you show"); read-only
default with phased remediation; untrusted-data discipline; no graph database; Apache-2.0.

**Honest risk assessment (§4):** the differentiation is real but not unassailable. k8sgpt owns
"AI summary of one resource" and is 5 minutes to copy on the surface. The defensible moat is
the **deterministic evidence model + open benchmark + terminal UX**, not the LLM. The design
optimizes for those.

---

## 3. Problem, Target Users, Product Principles

### 3.1 Problem

Kubernetes produces enormous evidence but no reconstruction. When `checkout` starts 503ing,
the causal chain — config change → memory growth → OOMKill → restart → latency → 503 — lives
in 6 different systems. Engineers manually replay it every time. The problem is not missing
data; it is **fragmented evidence and manual reconstruction**.

### 3.2 Target users

| User | Primary job | What they need |
|---|---|---|
| On-call SRE/platform engineer | Triage incidents fast | 30-second answer: what most likely happened, with evidence, and what to do |
| App dev owning a workload | Understand why their service failed | A timeline they can read without knowing 15 APIs |
| Platform team / org | Learn from incidents | Recorded, replayable investigations; eventually "have we seen this before?" |

### 3.3 Product principles

1. **Deterministic first, AI second.** The engine answers correctly with zero LLM involvement.
2. **Evidence over opinion.** Every conclusion traces to observations; every observation traces
   to a source query. Confidence is the engine's assessment of available evidence, never a
   claim of mathematical truth.
3. **Temporal reconstruction, not snapshots.** "What changed before X" is the highest-value
   question in incident response.
4. **Correlation ≠ causation.** Explicitly typed edges; `CAUSED_BY` requires mechanism +
   direction + temporal order (and ideally reproduction).
5. **Collect broadly, present narrowly.** 3–5 supporting + 1–2 contradicting pieces per
   hypothesis; links to raw data behind every claim.
6. **Respect the user's identity and permissions.** No escalation, read-only by default,
   every mutation auditable.
7. **Excellent terminal UX.** The first experience is `kubectl investigate <target>` — zero
   config, beautiful output.

---

## 4. Competitive Landscape & Differentiation

| Project | What it does | Gap KubeDoctor exploits |
|---|---|---|
| **k8sgpt** | LLM summarizes status of one resource | No timeline, no evidence graph, no temporal reasoning, no scoring; conclusions are unverifiable |
| **Robusta** | Event-driven automation + runbooks, in-cluster agent | Automation platform, not reconstruction; needs deployment into cluster; no evidence model |
| **kagent (CNCF)** | Agent framework for K8s ops, chat-first | Agent architecture, not investigation pipeline; deterministic core missing |
| **Kubescape** | Security/compliance scanning | Different problem (static posture, not incidents) |
| **Grafana / OTel / Prometheus** | Telemetry storage + dashboards | Store and show signals; correlation is manual. KubeDoctor *consumes* them |
| **Incident-management tools (FireHydrant, Grafana IR)** | Workflow, comms, timelines of humans | No machine-generated evidence reconstruction |
| **kubectl itself** | Single-resource views (`describe`, `get events`) | The manual archaeology KubeDoctor automates |
| **LLM chatbots / k8s copilots** | Chat + tool calls against cluster | Unconstrained, non-reproducible; no deterministic analyzers underneath |

**Differentiation statement:** *KubeDoctor is the only tool whose core artifact is a
deterministic, replayable, evidence-graph reconstruction of an incident, with an explainable
scoring model and an optional LLM on top — usable with zero configuration and no in-cluster
deployment.*

**Honest challenges:**
- k8sgpt could add a timeline in one release. Our answer: the *benchmark* (§23) and the
  evidence model are the durable assets, and our terminal output is the demo.
- Robusta already does event correlation; but it is an automation platform (agent, deployment,
  config), which is a different adoption curve.
- The LLM layer is a commodity. We say so in public docs; that honesty is itself positioning.

---

## 5. User Stories & CLI Specification

### 5.1 User stories (MVP)

1. As an on-call engineer, I run `kubectl investigate pod/checkout-7f84c9` and get a diagnosis
   of its CrashLoopBackOff with the 5 strongest pieces of evidence and a timeline.
2. As an SRE, I run `kubectl investigate deployment/checkout --since=2h` after a deploy and see
   the config change that preceded OOMKills, ranked by relevance.
3. As a developer, I run `kubectl investigate "why is checkout returning 503?"` (LLM enabled)
   and get a synthesis of the deterministic findings plus uncertainty, not a guess.
4. As a platform lead, I record an incident, replay it after an engine upgrade, and confirm the
   diagnosis changed (or didn't).
5. As a contributor, I add an analyzer in ~100 lines using the public analyzer API and it is
   exercised by the scenario benchmark.

### 5.2 CLI spec

**Binary 1 — kubectl plugin:** `kubectl-investigate` (so `kubectl investigate …` works via
kubectl plugin discovery). Same code as the main binary; the main binary is the shell.

**Binary 2 — `kubedoctor`:** the full CLI.

```
kubectl investigate <resource> [flags]          # diagnose target
kubectl investigate --since=30m                 # namespace/context-wide "what changed?"
kubectl investigate "why is checkout 503?"      # natural-language target (LLM mode)
kubedoctor replay <incident-id>                 # replay a recorded investigation
kubedoctor benchmark [suite]                    # run scenario benchmark
kubedoctor doctor                               # quick cluster health scan (static analyzers only)
```

Flags: `--namespace/-n`, `--since`, `--window start,end`, `--format text|json|jsonl`,
`--no-llm`, `--no-logs` (skip container logs), `--max-evidence N`, `--redact on|off|strict`.

**Output contract (text mode):** header card (incident, status, severity, confidence) → ROOT
CAUSE → EVIDENCE (✓/✗ lines with values and timestamps) → TIMELINE (compact) → IMPACT →
RECOMMENDATION. JSON mode emits the full `InvestigationResult` for scripting.

**Performance target:** typical single-workload investigation < 5s on clusters up to ~500
nodes; hard cap on log bytes; parallel collectors; staged collection (§8.3).

---

## 6. Architecture

### 6.1 Component diagram (refined)

```
                    ┌─────────────────────────────┐
                    │  kubectl investigate / kubedoctor  │
                    └──────────────┬──────────────┘
                                   │
                    ┌──────────────▼──────────────┐
                    │   Engine (internal/engine)  │  orchestrates the pipeline;
                    │   stages: scope → collect → │  exposes Investigation API
                    │   build → analyze → score → │  (in-process Go interface)
                    │   explain → recommend       │
                    └──────┬───────────┬──────────┘
                           │           │
              ┌────────────▼───┐   ┌───▼──────────────┐
              │  Collectors    │   │  Change detector │  (new: "what changed" is a
              │  (normalize →  │   │  diff over window│   first-class stage)
              │   Observation) │   └───┬──────────────┘
              └────────────┬───┘       │
                           │           │
              ┌────────────▼───────────▼──────────┐
              │  Evidence Graph + Timeline (model)│  in-memory, bounded window
              └────────────┬───────────┬──────────┘
                           │           │
              ┌────────────▼───┐   ┌───▼──────────────┐
              │ Analyzers      │   │ Hypothesis Engine│  deterministic rule-based;
              │ (deterministic)│   │ + evidence needs │  LLM adds hypotheses later
              └────────────┬───┘   └───┬──────────────┘
                           │           │
              ┌────────────▼───────────▼──────────┐
              │         Evidence Scorer           │  explainable weighted model
              └────────────┬──────────────────────┘
                           │
              ┌────────────▼───────────┐   ┌────────────────────┐
              │   InvestigationResult  │──▶│ Record/Replay (JSONL)│
              └────────────┬───────────┘   └────────────────────┘
                           │
              ┌────────────▼───────────┐   ┌────────────────────┐
              │ Report renderer (CLI)  │   │ LLM explainer (opt) │  structured digest only
              └────────────────────────┘   └────────────────────┘
```

**Missing components I added to the master prompt's diagram:** scope resolution (§8.1),
normalization at the collector boundary (D8), the change detector, a record/replay store, and
an explicit `NeedsEvidence` loop between hypothesis engine and collectors.

### 6.2 Investigation API (in-process, public)

```go
// pkg/api — the stable public contract. CLI, REST, MCP, web UI are adapters over this.
type Investigator interface {
    Investigate(ctx context.Context, req *InvestigationRequest) (*InvestigationResult, error)
}

type InvestigationRequest struct {
    Target     ResourceRef      // kind, namespace, name
    Window     time.Range       // start, end; default = --since
    Scope      ScopeOptions     // follow owners? selectors? logs? metrics?
    Redaction  RedactionPolicy
}

type InvestigationResult struct {
    Incident       *IncidentSummary   // status, severity, confidence
    Facts          []Observation      // normalized observations (deduped)
    Timeline       []TimelineEvent    // merged, sorted, anchored
    Graph          *Graph             // bounded evidence graph
    Changes        []Change           // ranked "what changed" list
    Hypotheses     []Hypothesis       // scored, with breakdowns
    Findings       []Finding          // analyzer outputs (evidence-anchored)
    Recommendations []Recommendation
    EvidenceGaps   []EvidenceGap      // what we expected but could not collect
    Meta           ResultMeta         // timing, sources hit, record ID
}
```

This API is the foundation for CLI, web UI, REST, gRPC, and MCP — exactly as the prompt
requires, minus the premature transport.

---

## 7. Data Model

### 7.1 Observation (the normalized fact — collector output)

```go
type Observation struct {
    ID        string            // stable, content-hashed for dedup
    Kind      ObservationKind   // e.g. container.terminated, pod.scheduled, config.changed,
                                // metric.breach, event.recorded, log.line (structured only)
    Source    SourceRef         // {system: k8s|prometheus|git|…, query: "GET /pods/…", raw_ref}
    Timestamp time.Time
    Resource  ResourceRef       // pod/checkout-7f84c9
    Payload   map[string]any    // TYPED, normalized, redacted — no raw text by default
    Confidence float64          // source confidence (1.0 for API state, lower for heuristics)
    Provenance string           // collector id + version (auditability)
}
```

**Rule:** raw log lines, annotations, and labels never flow downstream as free text. They are
either parsed into `Payload` fields (e.g. `exit_code: 137`, `reason: OOMKilled`) or quoted as
opaque blobs behind `RedactionPolicy` (see §14).

### 7.2 Evidence

```go
type Evidence struct {
    ID          string
    Observation ObservationRef
    Claim       string            // what this evidence supports, human-readable
    Supports    []HypothesisID    // hypothesis claims
    Contradicts []HypothesisID
    Weight      float64           // per-claim weight (scoring table)
    Strength    float64           // -1..1, analyzer-assigned (how strongly it supports)
    RawLink     string            // deep link / query to re-fetch raw data
}
```

### 7.3 Graph

Nodes = `ResourceRef` (typed: Deployment, ReplicaSet, Pod, Node, Service, ConfigMap, PVC, …).
Edges = typed, directed:

```
OWNS, RUNS_ON, SELECTS, ROUTES_TO, MOUNTS, CONFIGURED_BY,
DEPENDS_ON, CHANGED_BEFORE, TEMPORALLY_CORRELATED,
SUPPORTS, CONTRADICTS, CAUSED_BY
```

Edge rules (enforced in code, not convention):
- `OWNS`/`RUNS_ON`/`SELECTS`/`ROUTES_TO`/`MOUNTS`/`CONFIGURED_BY` — from live API relationships (owner refs, selectors, endpoints, volume mounts, env/volume config sources). These are *structural*, high-confidence.
- `CHANGED_BEFORE` — from the change detector: resource X changed before incident start.
- `TEMPORALLY_CORRELATED` — statistical: metric/event pattern moves with another (e.g. memory and error rate rise together). **Explicitly not causal.**
- `SUPPORTS`/`CONTRADICTS` — evidence-to-hypothesis edges.
- `CAUSED_BY` — **only** added by analyzers that satisfy the causality discipline (§10). By default the engine never emits it; the LLM can never emit it.

**Bounding the graph (prompt Q2/Q3):** scope resolution starts from the target and expands
only via explicit rules (§8.1): owners (up 2 levels), selected workloads, node, PVCs + their
storage class, services/ingress routing to it, config sources. Expansion is bounded by the
time window and by a configurable budget (default: ≤ 200 nodes, ≤ 2000 edges). Everything
outside is dropped, not stored.

### 7.4 Timeline

- One merged, deduplicated, time-sorted list of observations for the window.
- **Anchored rendering:** relative offsets to the incident onset (e.g. `t-14m`, `t+3m`) so the
  same replay reads identically across timezones.
- **Reconciliation (D-new):** timestamps from events, pod status transitions, and metrics have
  different clocks; the timeline builder aligns by (resource, transition) keys and treats
  sub-second ordering between sources as unordered. Skew > 30s between sources is surfaced as
  an `EvidenceGap`, not silently trusted.
- **Dedup/aggregation:** 17 identical `OOMKilled` terminations collapse to one finding
  `OOMKilled ×17` with first/last timestamps, while the raw list stays in the record.

### 7.5 Hypothesis

```go
type Hypothesis struct {
    ID          string
    Claim       string            // "Memory exhaustion after deployment checkout-v42"
    Category    HypothesisCategory // memory, scheduling, image, config-regression, dns, …
    Score       ScoreBreakdown    // margin + sigmoid + per-evidence lines (see §9)
    Evidence    []EvidenceRef     // supporting
    Contradictions []EvidenceRef  // contradicting
    Missing     []EvidenceRef     // expected but not collectable (evidence gaps)
    Status      HypothesisStatus  // candidate | likely | ruled-out
}
```

Hypothesis generation is deterministic first: analyzers register candidate claims with
activation conditions (e.g. OOM analyzer activates on `container.terminated` with
`reason=OOMKilled`). Only when *no* analyzer activates does the engine consider LLM-generated
hypotheses (v0.5+, §13), and those are scored identically.

### 7.6 Incident record (replay + benchmark substrate)

```go
type Incident struct {
    ID           string            // incident-<timestamp>-<slug>
    Meta         IncidentMeta      // cluster id (anonymized), k8s version, engine version, user note
    Request      InvestigationRequest
    Observations []Observation     // everything collected (the replay source of truth)
    Result       InvestigationResult
    RawRefs      []SourceRef       // queries + raw payload pointers (never embedded if large)
}
```

Stored as **JSONL** (`~/.kubedoctor/incidents/`) — one observation per line, append-only,
gzip-able, diff-able. Replay = re-run the pipeline over the recorded observations instead of
live collectors.

---

## 8. Pipeline Stages

### 8.1 Scope resolution (new stage)

Input: `InvestigationRequest.Target` + window. Output: ordered worklist of resources.
Rules: follow owner refs upward (Deployment ← ReplicaSet ← Pod), downward (Pods of a
Deployment via RS selector), `RUNS_ON` node, PVCs + PVs + storage class, Services/Ingress
routing to pods (via endpoints + selectors), config sources (envFrom, volumes, configmaps,
secrets *metadata only*), HPA. Stops at budgets. Namespace-wide investigations start from all
workloads and prune to those with any observation in the window.

### 8.2 Collection (staged)

```
Stage A (always):   resource state + status, events in window, owner chains, node status
Stage B (targeted): container logs (tail, capped), rollout history, HPA/PVC details
Stage C (adaptive): driven by NeedsEvidence() — e.g. OOM hypothesis asks for memory
                    limit/usage numbers; ImagePull hypothesis asks for image + registry hints
```

Collectors run in parallel, dedupe by `Observation.ID`, and each returns
`(observations, source_refs, error_or_gap)`. A collector failure is recorded as an
`EvidenceGap`, never fatal — the engine must work with partial data (§8.5).

### 8.3 Change detector ("what changed?")

Checks, in order of cost: `kubectl rollout history` equivalents (deployment revisions +
reasons), `Events` of kind `Scaled/Updated/Changed`, resource `generation`/`resourceVersion`
diffs across two API reads (begin/end of window), ConfigMap/Secret version references in pod
env. Output: ranked `Change` list.

**Ranking (prompt §26):**
```
relevance(C) = 0.45 · temporal_proximity(C, incident)
             + 0.30 · graph_proximity(C, target)      // hops in evidence graph
             + 0.15 · ownership_chain(C, target)      // direct owner/ancestor
             + 0.10 · anomaly_correlation(C)          // change co-occurs with metric anomaly
```
Weights configurable; each change line in the CLI shows its scoring factors.

### 8.4 Analysis & hypothesis

Analyzers (§11) run over observations, emit `Finding`s and hypothesis candidates. The
hypothesis engine dedupes claims across analyzers, merges evidence, and marks
contradictions. Then the **adaptive loop** (D7): for each live hypothesis, evaluate
`NeedsEvidence()`; if the most-needed evidence is cheap and missing, request Stage C
collection and re-analyze. Bounded: max 2 iterations per investigation.

### 8.5 Scoring (§9)

### 8.6 Explanation & recommendation

- Deterministic path: the top hypothesis renders as ROOT CAUSE with its breakdown; the top
  recommendation comes from a rule table (e.g. OOM-after-deploy → "roll back deployment X",
  with risk level and the evidence that supports it).
- LLM path (optional): receives a **redacted, structured digest** (JSON of top-3 hypotheses,
  evidence lines, timeline, gaps, changes) + a strict prompt (§13). It explains and
  paraphrases; it does not decide, score, or act.

---

## 9. Scoring Algorithm

### 9.1 Formula

For each hypothesis `H`:

```
margin(H) = Σ_{e ∈ support(H)}   w_e · s_e        // s_e ∈ [-1, 1], w_e from scoring table
          − Σ_{e ∈ contradict(H)} w_e · |s_e|     // symmetric penalty
          − λ_miss · |missing(H)|                 // evidence gaps: expected but absent
score(H) = sigmoid(margin(H) / T)                 // T = temperature, calibrated (§9.4)
```

`sigmoid(x) = 1 / (1 + e^(−x))`.

### 9.2 Scoring table (initial, tunable)

| Evidence pattern (analyzer-provided) | `w_e` | Example line rendered |
|---|---|---|
| Strong temporal correlation (change → symptom within window) | 30 | `+30 strong temporal correlation` |
| Direct ownership (target owned by changed resource) | 25 | `+25 direct ownership` |
| Mechanism match (e.g. memory growth → limit breach → OOMKilled) | 20 | `+20 mechanism: memory grew past limit` |
| Config/state change immediately before onset | 15 | `+15 configuration changed` |
| Reproduction (recurs after restarts / across replicas) | 10 | `+10 reproduced after restart` |
| Contradicting observation (e.g. CPU normal, memory fine) | −(w) | `−9 competing node-level evidence` |
| Missing expected evidence (e.g. no metrics source) | −λ_miss each | `−5 missing expected evidence: memory metrics` |

Example (from the prompt, made concrete):

```
HYPOTHESIS: deployment checkout-v42 → memory exhaustion
SCORE: 96%   (margin 85, T = 26)

+30  strong temporal correlation (deploy 14m before first OOM)
+25  direct ownership (pod owned by checkout-v42)
+20  mechanism: memory 410Mi → 1.02Gi vs limit 1Gi
+15  configuration changed (CACHE_SIZE 5000 → 50000)
+10  reproduced after restart (OOMKilled ×17)
 −9  contradicting: node-level memory pressure on 1/3 nodes
 −6  missing: per-container memory metrics (Prometheus absent)
```

Note: the margin is always exactly the sum of the visible lines — the score
is fully reproducible by hand.

### 9.3 Why this model

- **Explainable:** every point in the score maps to a visible line with a real observation
  behind it. Nothing is hidden in a vector.
- **Deterministic & testable:** same observations → same score. The scenario benchmark asserts
  exact margins.
- **Configurable:** weights live in one table per analyzer family; operators can tune.
- **Honest:** sigmoid output is *calibrated assessment*, not probability. The CLI says so.
- **Upgrade path to learning:** the margin is a single real number per hypothesis; fitting
  temperature `T` (and later per-category weights) against benchmark ground truth is
  legitimate ML with one parameter, then a handful. We get learning without opacity.

### 9.4 Calibration

After each benchmark run: map margins → empirical accuracy (binning) and fit `T` (temperature
scaling) + per-category bias. Report Expected Calibration Error (ECE) in the benchmark output.
If ECE > 0.1, the engine *lowers* displayed confidence toward 50% (conservative default) and
flags the categories that are mis-calibrated.

### 9.5 Missing telemetry

Expected-but-missing evidence (per analyzer `NeedsEvidence()`) both **penalizes** the score
and is **surfaced as an EvidenceGap** ("Prometheus not configured — memory growth unverified").
Confidence never exceeds a ceiling when a gap affects the top hypothesis (default ceiling 0.85
per affected gap).

---

## 10. Causality Discipline

`CAUSED_BY` edges are only emitted when **all** of:

1. **Direction:** the cause precedes the effect (`CHANGED_BEFORE` + `TEMPORALLY_CORRELATED`
   both present).
2. **Mechanism:** the analyzer can name the chain (config → memory → OOMKilled → restart →
   latency), with every hop backed by an observation. No mechanism → `SUPPORTS`, not
   `CAUSED_BY`.
3. **Exclusivity check:** no equally-scored competing hypothesis; if a competitor is within
   `0.85 × top score`, the verdict is "multiple plausible causes" and the CLI says so.
4. **Reproduction (when possible):** pattern recurs across replicas/restarts, or a revert
   (manual, user-initiated) resolves it — noted as `+reproduction` evidence.

Everything else is `SUPPORTS` / `TEMPORALLY_CORRELATED` / `CONTRADICTS`. The LLM is
structurally incapable of emitting `CAUSED_BY` — only analyzers can, and they must pass the
checks. The CLI renders correlation honestly: "memory rose with error rate (correlated;
causation unverified)".

---

## 11. Analyzer API

```go
// internal/analyze — public-ish contract (in-repo first; plugin mechanism later)
type Analyzer interface {
    ID() string
    Name() string
    // Activates on observations; cheap predicate evaluated for every observation.
    Supports(o Observation) bool
    // Emits findings + hypothesis candidates + evidence (support/contradict).
    Analyze(ctx context.Context, in *AnalysisInput) ([]Finding, error)
    // What evidence would strengthen/refute the claim (drives adaptive collection).
    NeedsEvidence(h HypothesisID) []EvidenceRequest
    // Human-readable explanation of a finding (used by renderer even without LLM).
    Explain(f Finding) string
}
```

Initial analyzers (v0.1–v0.3):

| Analyzer | Activates on | Covers |
|---|---|---|
| `oom` | `container.terminated reason=OOMKilled` | memory exhaustion, limit analysis, growth detection |
| `crashloop` | restart count > 3 in window | CrashLoopBackOff, exit-code patterns |
| `imagepull` | `ImagePullBackOff` / `ErrImagePull` | registry, tag, auth, image-not-found |
| `scheduling` | pod Pending > 30s | unschedulable, taints, resource, node selector |
| `nodepressure` | node conditions | memory/disk/pid pressure, NotReady |
| `probe` | probe failure events | liveness/readiness, bad endpoints, deadlock |
| `hpa` | HPA exists | scaling behavior, metric errors, max replicas |
| `pvc` | pod pending w/ PVC | capacity, storage class, attach errors |
| `service` | endpoints mismatch | selector mismatch, missing ports |
| `ingress` | 503s + ingress resources | backend config, cert expiration |
| `configregression` | rollout + symptom | change-before-incident correlation (v0.4 with Git) |
| `dns` / `netpol` / `cnifailure` | events / failures | later versions |

**Contribution rule:** a new analyzer must (a) ship with a scenario in `scenarios/`, and (b)
pass the benchmark gate (no regressions, adds a green case). This is what keeps hundreds of
analyzers maintainable (prompt Q20): the benchmark is the review gate.

---

## 12. Collector API

```go
// internal/collect
type Collector interface {
    ID() string
    // Normalize raw data into Observations. Raw payloads never leave here.
    Collect(ctx context.Context, scope *ScopePlan) ([]Observation, []SourceRef, error)
}
```

v0.1: `KubernetesCollector` (API + events + pod/container status + node status + rollout
history; logs behind a flag). v0.2: `PrometheusCollector`. v0.4: `GitCollector`
(commits touching manifests), `GitOpsCollector` (ArgoCD/Flux application state). Later:
`LokiCollector`, `OTelCollector` (traces), `CiliumCollector`. Analyzers see only
`Observation`s — a hypothesis must not care whether memory numbers came from the API,
Prometheus, or a kubelet scrape.

**Collector contract:** return `(observations, refs, err)`; on error, record `EvidenceGap`
and continue. Never fail the investigation because one source is down.

---

## 13. LLM Architecture

- **Interface:** `internal/llm.Provider` with adapters: OpenAI-compatible (OpenAI, vLLM,
  Ollama, local llama.cpp servers), Anthropic, Gemini. Default: **off**. `--llm` / config
  enables it.
- **Input:** a redacted structured digest — JSON of the top-3 hypotheses with score
  breakdowns, evidence lines (quoted, truncated), timeline, changes, evidence gaps. **No raw
  cluster data, no raw logs, no kubeconfig, no secrets.**
- **Output contract:** structured JSON: `{summary, explanation, uncertainty, followups,
  recommended_action (nullable), confidence_in_own_answer}`. Rendered into the CLI output as
  "AI synthesis" — visually separate from deterministic findings.
- **Constraint layer:** system prompt states the engine's verdicts are authoritative and the
  LLM may not change scores, emit `CAUSED_BY`, or propose mutations outside the typed
  recommendation table. The renderer *verifies* output shape; malformed output is dropped,
  not trusted.
- **No tool access in v0.5.** When MCP/tools arrive (v0.6+), every tool is typed, goes
  through the policy engine (§14), and the LLM still cannot emit `CAUSED_BY` or approve
  actions.
- **Hypothesis generation (v0.5+, optional):** only when no deterministic analyzer activates,
  the engine may ask the LLM for candidate claims over the digest; candidates are scored by
  the same deterministic scorer and clearly labeled "AI-generated, unverified".

---

## 14. Security Architecture

### 14.1 Trust boundaries

```
untrusted data (logs, annotations, labels, events, git commits, telemetry)
      │  (never executed, never instructions; parsed into typed fields or quoted)
      ▼
normalized evidence (Observation, redacted, schema-validated)
      │  (the only thing analyzers and LLM see)
      ▼
reasoning (deterministic analyzers; optional LLM on digest only)
      │
      ▼
tools / actions (typed, policy-checked, human-approved, audited)
```

### 14.2 Prompt-injection defense (concrete)

1. **Log lines are data, never instructions.** Raw text is quoted with explicit delimiters in
   the digest (`<evidence>` blocks); the system prompt forbids acting on evidence content;
   the parser strips anything that looks like instruction-following syntax from *display*
   (e.g. "ignore previous instructions") — and the *engine's* behavior never depends on the
   LLM's compliance anyway, because the LLM has no authority (no tools in v0.5; typed
   tools later).
2. **The LLM cannot act.** No tool calls until v0.6, and then only typed tools behind policy.
3. **Anchoring:** the digest is JSON with a fixed schema; free text is a quoted string field,
   not instructions; the prompt tells the model to treat all evidence fields as untrusted
   data.
4. **Defense in depth:** even a fully-prompt-injected LLM can only produce *text*; scores,
   `CAUSED_BY`, recommendations, and actions are all engine-side.

### 14.3 Redaction

- Secrets: metadata only (names, keys, sizes) — never `data`/`stringData`.
- Log lines: regex + entropy-based redaction (tokens, JWTs, keys) with `strict` mode that
  quotes/truncates aggressively.
- `--redact` levels: `on` (default), `off` (explicit, warned), `strict`.
- Redaction happens at the collector boundary; downstream never sees unredacted text.

### 14.4 RBAC & identity

- The engine uses **the caller's kubeconfig identity**. It never escalates, never uses
  service-account impersonation, never suggests credentials.
- Missing permissions → `EvidenceGap` ("no permission to read events in this namespace"),
  not failure and not workarounds.
- If the caller is read-only (verified via `SelfSubjectAccessReview`), the engine notes
  "read-only session" and disables recommendation mutation previews.

### 14.5 Remediation model (phased, no autonomy in v0.x)

```
Phase 1 (v0.1–0.4): read-only findings
Phase 2 (v0.4+):    recommendations (risk-leveled, evidence-linked)
Phase 3 (v0.6+):    preview actions (diff-style, `kubectl rollout undo … --dry-run=client`)
Phase 4 (v0.6+):    human approval (`kubedoctor apply <action-id>` after explicit approval)
Phase 5 (v1.x):     policy-controlled automatic remediation (user-authored policies ONLY)
```

Every action record: `{user, timestamp, cluster (anonymized), resource, action, arguments,
evidence_ids, reason, risk, approval, result}` — appended to the incident record. Risk levels
are computed from blast radius in the evidence graph (how many dependent workloads).

---

## 15. Storage Strategy

| Data | Store | Why |
|---|---|---|
| Investigation graph, timeline, hypotheses | **in-memory** | Bounded window, per-investigation; no persistence needed |
| Incident records (replay/benchmark) | **SQLite via JSONL files** (`~/.kubedoctor/incidents/`) | Append-only, diff-able, greppable; SQLite index for "have we seen this" later |
| Historical incident memory ("seen this before?") | SQLite (v0.8+) | Similarity = hash of signature (symptom set + change set) → ranked by overlap; no embeddings needed |
| Analytical queries over history | **DuckDB** (v1.x, only if needed) | When users ask "what correlates with OOMKills across 6 months" |
| Graph database (Neo4j-style) | **Never required** | Justified only for cross-incident graph analytics at enterprise scale — a different product; revisit with a concrete workload, not vibes |

---

## 16. Performance Strategy

- **Staged + adaptive collection** (§8.2): typical investigation touches < 100 API calls.
- **Parallel collectors** with a shared budget (default 10s wall clock).
- **Log caps:** tail ≤ 200 lines/container, ≤ 1 MB total, on demand only (Stage B).
- **Dedup at the boundary:** identical observations collapse; the graph never stores
  duplicates.
- **Bounded graph:** budgets (§7.3) — hard caps, configurable.
- **Cancellation everywhere:** `ctx` propagates; Ctrl-C aborts cleanly and still renders
  partial findings with gaps.
- **Warm mode (later):** long-running daemon watches `Events` and keeps a rolling window hot,
  so investigations are near-instant. Optional, v0.8+.

---

## 17. Incident Replay & Synthetic Benchmark

### 17.1 Replay

`kubedoctor replay <incident-id>` runs the pipeline over the recorded observations. Every
stage logs its input/output count and duration (the prompt's 9-step replay output). Guarantees:
same record + same engine version → same result (determinism test). Record schema version is
stored; old records replay with the current engine or fail loudly.

### 17.2 Scenario benchmark

`scenarios/` — one directory per scenario:

```
scenarios/oom-after-deploy/
├── scenario.yaml     # ground truth: root cause, expected evidence, expected
│                     # recommendation, severity, allowed score margin
├── record.jsonl      # recorded investigation (CI: replay-based benchmark)
└── manifests/        # kind cluster setup (integration benchmark)
```

Initial suite (v0.1): `oom-after-deploy`, `crashloop`, `imagepull`, `pending-unschedulable`.
By v0.3: broken DNS, NetworkPolicy failure, CPU throttling, node pressure, PVC exhaustion,
bad readiness/liveness probe, HPA misconfig, service selector mismatch, ingress failure,
certificate expiration, config regression, CNI failure. Each scenario defines expected
evidence IDs and hypothesis margins ± tolerance, so `kubedoctor benchmark` is a strict gate.

### 17.3 Live-cluster integration

`make scenario-oom` → `kind create cluster` → apply manifests → `kubectl investigate` →
assert. Docker + kind already available in this environment; CI runs the replay-based
benchmark (fast, no cluster) and the kind suite nightly.

---

## 18. Evaluation Methodology

Benchmark harness compares four configurations on the same suite:

| Config | What it measures |
|---|---|
| deterministic analyzers only | correctness of the core engine |
| LLM-only baseline (no engine) | the prompt's "AI wrapper" counterfactual — expected to do poorly and prove the point |
| KubeDoctor without LLM | the default product |
| KubeDoctor with LLM | added value of synthesis |

Metrics: root-cause accuracy (top-1 / top-3), evidence precision, evidence recall,
false-positive rate, time-to-diagnosis, recommendation correctness, unsafe-action rate
(should be 0 in v0.x), confidence calibration (ECE). Reported per scenario and per category.

---

## 19. Technology Choices (with challenges)

| Choice | Decision | Challenge/alternative |
|---|---|---|
| Language | **Go** | Uncontested: k8s ecosystem, single binary, fast startup, easy `kubectl` plugin |
| K8s client | **client-go** (dynamic + typed) | Rejected controller-runtime (D3) — we are pull-based, not controller-based |
| CLI | **Cobra** | Standard; pflag gives kubectl-compatible flags |
| API transport | in-process Go first; **REST** when server exists (v0.6); gRPC optional (v0.8+) | gRPC adds contract complexity with no CLI benefit (D4) |
| Storage | in-memory graph; SQLite for records | DuckDB only when history analytics arrive (D5) |
| LLM | interface + adapters (OpenAI-compatible first) | Local-first story: Ollama/vLLM work via the same adapter |
| Rendering | ANSI text (custom renderer); JSON/JSONL for machine use | Bubble Tea deferred — we want stable, diff-able output for tests and replay |
| Observability of the tool itself | OpenTelemetry + `log/slog` | The tool must be debuggable; spans per pipeline stage |
| Packaging | Homebrew tap, `krew` (kubectl plugin manager), binary releases, Helm chart for the optional daemon | krew is the discovery channel for kubectl plugins |
| Test infra | Go tests + scenario benchmark + kind | Docker/kind already available in dev env |

---

## 20. Repository Structure

```
KubeDoctor/
├── cmd/
│   ├── kubectl-investigate/      # kubectl plugin entry (kubectl investigate …)
│   └── kubedoctor/               # full CLI: investigate, replay, benchmark, doctor
├── internal/
│   ├── engine/                   # pipeline orchestration, Investigation API impl
│   ├── model/                    # Observation, Evidence, Graph, Timeline, Hypothesis, Incident
│   ├── scope/                    # related-resource resolution
│   ├── collect/                  # Collector interface, registry, k8s implementation
│   ├── change/                   # "what changed" detector + ranking
│   ├── analyze/                  # Analyzer interface, registry, all analyzers
│   ├── hypothesis/               # candidate dedup/merge, NeedsEvidence loop
│   ├── score/                    # scoring tables, margin, calibration
│   ├── timeline/                 # merge, dedup, anchor, skew reconciliation
│   ├── record/                   # incident JSONL write/replay
│   ├── redact/                   # secret/entropy redaction
│   ├── llm/                      # Provider interface + adapters + digest builder
│   ├── report/                   # terminal renderer (text/json)
│   └── policy/                   # risk levels, action records, approval gates (v0.6+)
├── pkg/
│   └── api/                      # PUBLIC Investigation API types (stable contract)
├── scenarios/                    # benchmark suite (one dir per scenario)
├── testdata/                     # fixtures for unit tests
├── docs/                         # this design, roadmap, architecture diagrams
├── charts/                       # Helm chart for optional daemon/server (v0.6+)
├── examples/                     # demo scenarios (kind-compatible)
├── Makefile
└── go.mod
```

**Why no top-level `analyzers/` or `integrations/` (prompt §31):** third-party analyzers via
`go plugin` are fragile (version-locked, Linux-only) and subprocess plugins add a protocol
for a problem we don't have yet. In-repo analyzers behind a tiny interface + the benchmark
gate keep contribution friction low — the thing that actually grows a community.

---

## 21. Roadmap

### 21.1 30 / 60 / 90 days

| Window | Deliverables |
|---|---|
| **0–30d** | Repo + CI + Makefile; model types; k8s collector (Stage A); OOM analyzer; record/replay; `kubectl investigate pod/…` text output; scenario `oom-after-deploy`; benchmark harness (replay mode) |
| **30–60d** | CrashLoop + ImagePull analyzers; timeline builder (dedup/anchoring); change detector v1; graph builder; scoring + calibration v1; 3 more scenarios; `--format json` |
| **60–90d** | Adaptive collection (NeedsEvidence); HPA/PVC/scheduling/probe analyzers; `--since` cluster-wide "what changed"; docs + krew/Homebrew packaging; blog + demo video; kind integration suite |

### 21.2 Versions

| Version | Scope |
|---|---|
| **v0.1** | Evidence model, k8s collector, OOM/CrashLoop/ImagePull analyzers, timeline v1, record/replay, CLI text output, first 3 scenarios + benchmark gate. **No LLM. No graph DB. No server.** |
| **v0.2** | Evidence graph + typed edges, scoring + calibration, change detector, Prometheus collector (metric.breach observations), 5 more scenarios |
| **v0.3** | Adaptive collection loop, hypothesis engine (rule-based), remaining core analyzers, "what changed" ranking, JSON output |
| **v0.4** | Git + ArgoCD/Flux collectors, config-regression analyzer, recommendations (risk-leveled, read-only) |
| **v0.5** | LLM explainer (digest-only, no tools), local-LLM support, "AI synthesis" rendering |
| **v0.6** | REST API + server mode, MCP server (thin wrapper), preview actions + human approval, action audit records |
| **v0.7** | Expanded scenario suite (all §17.2), evaluation report CI, calibration hardening |
| **v0.8+** | Loki/OTel/tracing collectors, historical incident memory ("seen this before"), warm daemon, web UI (read-only), DuckDB analytics |
| **v1.x** | Policy-controlled remediation, `--predict` (static manifest risk), OpenStack/additional infra collectors, multi-tenant server |

---

## 22. Open-Source Strategy

- **License: Apache-2.0.** Rationale: the ecosystem standard (k8s, client-go, Argo), permissive
  for enterprise adoption, includes a patent grant, and cloud providers offering it as a
  service is *fine* — the moat is the benchmark, brand, and integrations, not code scarcity.
  AGPL would suppress enterprise adoption; dual-license is premature for an unproven project.
  Revisit only if a commercial layer emerges with real demand.
- **5-minute install:** `brew install kubedoctor` / `kubectl krew install investigate` /
  single static binary.
- **30-second demo:** `kind create cluster && kubectl apply -f examples/oom-after-deploy &&
  kubectl investigate deployment/checkout` → evidence-backed diagnosis. This is the README
  GIF.
- **Contribution gate:** new analyzer = analyzer + scenario + benchmark green. Review is
  mechanical and kind.
- **Community assets:** open benchmark as a standalone repo eventually; blog posts dissecting
  real incidents (redacted); "how we calibrated confidence" posts; conference demos with the
  kind workflow; MCP integration so AI agents (Claude, Cursor, kagent) can consume
  investigations.
- **Ecosystem posture:** we consume OTel/Prometheus/Loki standards, integrate with k8s
  tooling, and don't pretend to replace observability. CNCF alignment is natural.

---

## 23. Naming Recommendation

| Name | Strengths | Risks |
|---|---|---|
| **KubeDoctor** | Self-explanatory ("doctor" = diagnose), matches "I don't want to operate Kubernetes without this installed", easy to say | Some existing small projects ("kube-doctor"); SEO crowded; generic |
| **KubeSherlock** | Memorable, investigation framing, CLI personality | "Sherlock" implies detection not treatment; playful vs. enterprise |
| **Kausal** | Causality is the core differentiator, short, brandable | Loses "kube" discoverability; hard to guess what it does |
| **IncidentGraph** | Describes the artifact precisely | Generic-sounding; weak CLI appeal |

**Recommendation:** ship as **KubeDoctor** (working name, matches the doctor metaphor end to
end: diagnose → evidence → treat), with `kubectl investigate` as the plugin verb. If
trademark search finds collisions, **KubeSherlock** is the fallback. Legal availability must
be checked separately (trademark + GitHub org name) before public launch.

---

## 24. Risks & Failure Modes

| Risk | Mitigation |
|---|---|
| k8sgpt/kagent add timelines & evidence overnight | The moat is the benchmark + determinism + record/replay; move fast on scenarios and docs; LLM is deliberately a small layer |
| Scope creep (predict, web UI, multi-agent, history) | §25 "what not to build" is enforced in review; roadmap gates |
| Analyzer explosion becomes unmaintainable | Benchmark gate + tiny interface + shared evidence vocab |
| Confidence over-trusted by users | Calibration + explicit "engine assessment" language + evidence gaps surfaced |
| False positives erode trust | Benchmark FP gate; conservative default confidence; contradictions always shown |
| Prompt injection | §14.2 defense-in-depth; LLM has no authority in v0.x |
| Performance death on big clusters | Staged/bounded collection; budgets; cancellation; warm-mode later |
| Single-maintainer bus factor | Apache-2.0 from day one, contribution gate, docs-first culture |
| "Another kubectl wrapper" perception | The terminal output *is* the marketing; record/replay + benchmark prove depth |

**Known failure modes to design for:** collector down → gaps not failures; clock skew between
sources → reconciliation + surfaced gap; no metrics → ceiling on confidence; RBAC-limited
identity → gaps; huge namespaces → scope budgets + "truncated" markers.

---

## 25. What Not to Build (first 6 months)

| Feature | Why not |
|---|---|
| Generic chatbot | Kills the evidence-first identity; k8sgpt owns that hill |
| New metrics/logs/tracing database or agent | We consume standards; building storage is a 5-year distraction |
| Multi-agent framework | Complexity with no evidence benefit; the pipeline is not an agent loop |
| Autonomous remediation | Trust-killer; v0.x is read-only by design |
| Web dashboard | Terminal UX is the demo; dashboards are a v0.8+ optional |
| Graph database requirement | In-memory bounded graph is sufficient and honest (§15) |
| `--predict` mode | Different product (static risk analysis); v1.x |
| Proprietary cloud dependency | Open-source trust requirement; cloud collectors later and optional |
| Historical incident ML | SQLite signature matching first; embeddings only if it fails |

---

## 26. Long-Term Vision

KubeDoctor becomes the **investigation layer of the cloud-native stack**: a small binary that
every on-call engineer installs, that turns fragmented telemetry into replayable,
evidence-backed explanations, and that grows organizational memory ("have we seen this
before?"). The Kubernetes implementation is the first adapter; the evidence model, analyzer
API, and benchmark are infrastructure — the same engine can later speak OpenStack, AWS, and
Linux. The bar remains: *I don't want to operate Kubernetes without this installed.*

---

## 27. Answers to the 20 Hard Questions

1. **Causality vs. correlation:** typed edges + the four-gate discipline (§10): direction,
   mechanism, exclusivity, reproduction. Correlation alone yields
   `TEMPORALLY_CORRELATED`, never `CAUSED_BY`.
2. **Graph size:** bounded expansion via scope rules + budgets (§8.1, §7.3); window-bounded;
   dedup at boundary.
3. **Evidence pruning:** per-hypothesis minimum-viable-evidence selection (3–5 support, 1–2
   contradict); raw stays in record, not in the graph.
4. **Missing telemetry:** EvidenceGap + confidence ceiling + penalty term (λ_miss); the CLI
   names what it couldn't see.
5. **Contradictory telemetry:** surfaced explicitly; scoring subtracts; if contradiction
   flips the top hypothesis, output says so ("evidence conflicts").
6. **Confidence calibration:** benchmark-driven temperature scaling + ECE reporting + a
   conservative floor (never claim >85% with evidence gaps) (§9.4).
7. **False positives:** benchmark FP gate; analyzers must activate on concrete observations;
   severity thresholds; contradictions mandatory in output.
8. **Historical influence on scoring:** v0.x none (purity); v0.8+ priors = per-category
   base rates from incident records, added as a single bias term — still explainable.
9. **Unknown unknowns:** the adaptive loop's `NeedsEvidence`; LLM hypothesis generation only
   when no analyzer activates, scored identically and labeled unverified; output explicitly
   lists what remains unexplained.
10. **Analyzer interaction:** analyzers are pure functions over observations; they share
    evidence, never state; the hypothesis engine merges/dedupes claims. No ordering
    dependencies.
11. **LLM constraints:** digest-only input, JSON output contract, no authority over scores/
    edges/actions, renderer-side validation, no tools in v0.5.
12. **Prompt injection:** trust boundaries + quoted evidence + no tool authority + anchor
    prompts (§14.2); engine correctness never depends on LLM behavior.
13. **Secret redaction:** metadata-only secrets, entropy+regex redaction at collector
    boundary, `strict` mode, redaction before anything downstream.
14. **Multi-tenancy:** out of scope for v0.x (D9); server mode (v0.6+) scopes to the
    caller's identity and kubeconfig context; no shared caches across tenants.
15. **RBAC:** caller identity only, `SelfSubjectAccessReview` check, missing perms → gaps.
16. **Large clusters:** staged collection + budgets + parallel collectors + dedup; namespace
    investigations prune to window-active workloads.
17. **Fast investigations:** target < 5s typical; adaptive staging avoids collecting what
    hypotheses don't need; cancellation-friendly.
18. **No Prometheus:** full deterministic function from API data alone; metric-dependent
    claims are downgraded with gaps, never skipped.
19. **No LLM:** the default; the CLI's ROOT CAUSE/EVIDENCE/TIMELINE come from analyzers.
20. **Hundreds of analyzers:** tiny interface + shared evidence vocabulary + benchmark gate +
    in-repo first, plugin mechanism later (§11 contribution rule).
