# How it works

```
kubectl investigate deployment/checkout
        │
        ▼
┌─────────────┐   ┌────────────────┐   ┌──────────────────┐
│  Collect    │──▶│  Build         │──▶│  Analyze         │
│  (k8s, prom,│   │  timeline,     │   │  (11 rule-based  │
│  git, gitops│   │  evidence      │   │  analyzers)      │
│  (staged)   │   │  graph, changes│   │                  │
└─────────────┘   └────────────────┘   └────────┬─────────┘
                                                ▼
┌─────────────────┐   ┌──────────────────────────────────────┐
│  Record +       │◀──│  Score (weighted evidence -> margin → │
│  replay JSONL   │   │  calibrated confidence) + hypothesis  │
│  ~/.kubetective/│   │  engine (merge, rerank, outrank)      │
└─────────────────┘   └──────────────────────────────────────┘
```

1. **Collect.** Observations are normalized facts (`pod.state`, `container.waiting`,
   `event.recorded`, `git.commit`, ...). Nothing raw (log lines, secrets,
   annotations) crosses this boundary.
2. **Build.** The timeline anchors every observation in time. The evidence graph
   links resources (owns, runs-on, changed-before). The change detector answers
   "what changed?".
3. **Analyze.** Each analyzer activates on its observations and emits evidence with
   explicit weights (e.g. `+30` for a CrashLoopBackOff state).
4. **Score.** Margin = Σ weight·strength − gap penalties. Confidence = sigmoid
   (margin / T). T is fitted against the scenario suite by minimising negative
   log-likelihood and leave-one-out validated, but adopted only if the fit
   clears four conditions: the suite contains incorrect predictions, the fit is
   not pinned to the edge of the search grid, and it beats the default
   out-of-sample on **both** NLL and Brier by at least 2%. Two proper scoring
   rules rather than expected calibration error, because ECE is binned and its
   leave-one-out estimate moved by twenty points when a single scenario was
   added — see `internal/score/calibrate.go`. ECE is reported, not gated on.
   Every score carries its line-by-line breakdown.

   The benchmark itself always grades at the *default* temperature, never the
   adopted one. It is the instrument that produces the calibration, so grading
   it at its own output closes a loop — the first adopted fit rescaled every
   score and failed 14 of 25 scenarios against thresholds written at the old
   scale.
5. **Record.** The full investigation is appended to the incident store.

The adoption gate and the causality discipline (no `CAUSED_BY` claims without
mechanism, direction, and exclusivity) keep the output honest by construction.

## Safety model

- **Read-only by default.** Investigation never mutates the cluster.
- **Remediation is gated.** `kubetective action <incident-id>` only previews
  (`kubectl rollout undo ... --dry-run=client` equivalents). Applying requires
  `--apply <id> --yes`, an explicit human approval, and appends an audit record
  with user, timestamp, evidence, risk, and result to the incident file.
- **LLM is optional and constrained.** The model receives only a redacted digest
  (no logs, no payload values, no kubeconfig). Its output is validated strict JSON,
  and it cannot change scores or propose actions. Unreachable models degrade
  gracefully; the deterministic verdict always stands.
- **No secrets leave the machine.** The digest boundary is regression-tested.

## Development

```sh
make build test vet fmt tidy
make scenarios                 # alias for: kubetective benchmark
kubetective evaluate           # full markdown report, CI-friendly
```

- Go 1.26 or newer, no other build-time dependencies.
- Unit tests cover every package. Collectors use fake clientsets; scoring,
  calibration, redaction, timeline, graph, actions, REST and MCP protocols have
  dedicated suites.
- **Container smoke:** `hack/smoke-container.sh` builds the distroless image and
  runs `doctor`, a live investigation, and the remediation E2E in a throwaway
  kind cluster under `deploy/rbac.yaml`. Needs docker, kind and kubectl on
  `PATH`. CI runs it on every push.
- Adding an analyzer: implement `analyze.Analyzer` (see `internal/analyze/` for
  worked examples), register it in `internal/cli/root.go`, and ship a scenario
  proving it. `kubetective benchmark` must stay green.
