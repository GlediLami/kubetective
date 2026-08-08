# KubeTective — Kubernetes Incident Investigation Engine

KubeTective investigates Kubernetes incidents the way a good SRE would: it collects
facts about the target, builds a timeline and an evidence graph, generates ranked
hypotheses with **explainable, deterministic scores**, and tells you *why* it thinks
what it thinks — every point of confidence is backed by a visible list of evidence.

It runs as a CLI, a `kubectl` plugin, a REST server, or an MCP server, and every
investigation is recorded so it can be replayed, audited, and used as a benchmark.

```
$ kubectl investigate deployment/checkout --since=30m
╭──────────────────────────────────────────────────╮
│ INCIDENT: deployment/prod/checkout                 │
│ Status: OOMKILLED                                   │
│ Severity: HIGH                                       │
│ Confidence: 94%                                      │
╰──────────────────────────────────────────────────╯

ROOT CAUSE
  Memory exhaustion: container terminated with OOMKilled 1 time(s) (memory limit 1Gi) — 4 restart(s)

EVIDENCE
  ✓ mechanism: OOMKilled ×1 (+20)
  ✓ memory limit configured: 1Gi (+15)
  ✓ reproduced after restart (×4) (+10)
  ✓ strong temporal correlation (terminations in window) (+27)

RECOMMENDATION
  roll back deployment/prod/checkout to the last known-good revision [MEDIUM]
```

## Why deterministic first?

KubeTective's verdicts come from a rule-based engine, not an LLM. Each hypothesis
score is a sum of weighted evidence terms that you can read line by line. Confidence
is calibrated against a scenario benchmark (15 recorded incidents with ground truth),
and the calibration is validated leave-one-out before it is adopted. The optional
LLM layer only *explains* the engine's verdict in plain language — it can never
change scores, invent causes, or propose actions.

## Features

- **Evidence-based investigation** — 12 analyzers for the most common failure modes:
  OOM kills, crash loops, image pull failures, scheduling/unschedulable, node
  pressure, liveness/readiness probe failures, PVC issues, service selector
  mismatches, HPA at max, DNS failures, configuration regressions (Git/GitOps).
- **Collectors** — Kubernetes (pods, deployments, events, PVCs, services, HPAs,
  coreDNS), Prometheus (memory-metric corroboration), Git (commits touching your
  manifests), GitOps (Flux `Kustomization`/`HelmRelease`, ArgoCD `Application`).
- **Explainable scoring** — every hypothesis ships its evidence breakdown; scores
  are calibrated against the scenario suite and validated with leave-one-out ECE.
- **Timeline + evidence graph + change detection** — what happened, in what order,
  what owns what, and *what changed* right before the incident.
- **Record / replay** — every investigation is appended to `~/.kubetective/incidents/`
  as JSONL: replay it, diff engine versions, audit it.
- **Scenario benchmark + evaluation report** — `kubetective benchmark` is the
  regression gate for every analyzer; `kubetective evaluate` renders a markdown
  evaluation report (per-scenario, per-category accuracy, calibration, false-positive
  check) suitable for CI.
- **Safe remediation** — actions (rollback, restart) are previewed read-only and
  applied only after explicit human approval; every apply writes an audit record.
- **REST server + MCP server** — the same pipeline behind an HTTP API and an MCP
  stdio server with read-only tools.
- **Optional LLM explainer** — digest-only, redacted, constrained: works with any
  OpenAI-compatible endpoint (OpenAI, Ollama, vLLM, llama.cpp).
- **kubectl plugin** — drop the binary named `kubectl-investigate` on your `PATH`
  and run `kubectl investigate <resource>`.

## Install

### Homebrew

```sh
brew install gledilami/kubetective/kubetective
```

This installs both binaries: `kubetective` and `kubectl-investigate`
(the kubectl plugin).

### Go

```sh
go install github.com/GlediLami/kubetective/cmd/kubetective@latest
```

### From source

Requires Go ≥ 1.26 and a Kubernetes cluster (for live investigations; the
benchmark runs without one).

```sh
git clone https://github.com/GlediLami/kubetective.git
cd kubetective
make build            # bin/kubetective + bin/kubectl-investigate
make install          # kubetective → ~/.local/bin/kubetective
make install-plugin   # kubectl-investigate → ~/.local/bin (kubectl plugin)
```

Point `PREFIX=/usr/local` at `make install` to install system-wide.

### kubectl plugin

kubectl auto-discovers plugins named `kubectl-*` on `PATH`:

```sh
make install-plugin
kubectl investigate pod/checkout-7f84c9   # same as: kubetective investigate ...
```

## Quick start

Investigate a crash-looping deployment (uses your current kubeconfig context):

```sh
kubectl investigate deployment/checkout --since=30m
# or without the plugin:
kubetective investigate deployment/checkout --since=30m
```

Target forms: `pod/<name>`, `deployment/<name>`, `namespace/<name>` (or a bare
name, which defaults to a pod), optionally scoped with `--namespace` and a window
(`--since=2h`, default 30m).

Optional evidence sources:

```sh
kubetective investigate deployment/checkout \
  --since=30m \
  --prometheus-url=http://localhost:9090 \   # metric corroboration
  --git-repo=~/code/checkout-manifests       # commits touching your manifests
```

## Command reference

| Command | Description |
|---|---|
| `kubetective investigate <resource>` | Run an investigation (flags: `--since`, `--namespace`, `--no-logs`, `--format=json`, `--prometheus-url`, `--git-repo`, `--llm*`) |
| `kubetective replay <incident-id>` | Re-run a recorded investigation through the current engine (deterministic) |
| `kubetective incidents` | List recorded incident ids, newest first |
| `kubetective action <incident-id>` | Preview remediation actions (read-only; `--apply <id> --yes` to execute with approval) |
| `kubetective benchmark [suite]` | Run the scenario suite gate — exit 1 on any regression |
| `kubetective evaluate [suite]` | Markdown evaluation report (per-category accuracy, calibration, FP check) |
| `kubetective serve --listen :8080` | REST API server |
| `kubetective mcp` | MCP server over stdio (read-only tools) |
| `kubetective version` | Print the engine version |

Run `kubetective <command> --help` for every flag.

## Configuration

KubeTective reads kubeconfig the same way kubectl does (`--kubeconfig`,
`--context`, or the default loading rules).

| Environment variable | Purpose |
|---|---|
| `KUBETECTIVE_PROMETHEUS` | Prometheus base URL for metric evidence (same as `--prometheus-url`) |
| `KUBETECTIVE_GIT_REPO` | Path to the manifests git checkout (same as `--git-repo`) |
| `KUBETECTIVE_LLM_MODEL` | LLM model name for the explainer |
| `KUBETECTIVE_LLM_BASE_URL` | OpenAI-compatible API base URL (default `https://api.openai.com/v1`) |
| `KUBETECTIVE_LLM_API_KEY` | API key (local servers like Ollama don't need one) |
| `KUBETECTIVE_HOME` | State directory (default `~/.kubetective`: incidents, config) |
| `KUBETECTIVE_CONFIG` | Config file path (default `~/.kubetective/config.json`) |

The calibrated scoring temperature is persisted to `~/.kubetective/config.json`
by `benchmark`/`evaluate` and loaded by every invocation.

## How it works

```
kubectl investigate deployment/checkout
        │
        ▼
┌─────────────┐   ┌────────────────┐   ┌──────────────────┐
│  Collect    │──▶│  Build         │──▶│  Analyze         │
│  (k8s, prom,│   │  timeline,     │   │  (12 rule-based  │
│  git, gitops│   │  evidence      │   │  analyzers)      │
│  — staged)  │   │  graph, changes│   │                  │
└─────────────┘   └────────────────┘   └────────┬─────────┘
                                                ▼
┌─────────────────┐   ┌──────────────────────────────────────┐
│  Record +       │◀──│  Score (weighted evidence → margin →  │
│  replay JSONL   │   │  calibrated confidence) + hypothesis  │
│  ~/.kubetective/ │   │  engine (merge, rerank, outrank)      │
└─────────────────┘   └──────────────────────────────────────┘
```

1. **Collect** — observations are normalized facts (`pod.state`, `container.waiting`,
   `event.recorded`, `git.commit`, …). Nothing raw (log lines, secrets, annotations)
   crosses this boundary.
2. **Build** — the timeline anchors every observation in time; the evidence graph
   links resources (owns, runs-on, changed-before); the change detector answers
   "what changed?".
3. **Analyze** — each analyzer activates on its observations and emits evidence
   with explicit weights (e.g. `+30` for a CrashLoopBackOff state).
4. **Score** — margin = Σ weight·strength − gap penalties; confidence = sigmoid
   (margin / T), where T is calibrated against the scenario suite and validated
   leave-one-out. Every score carries its line-by-line breakdown.
5. **Record** — the full investigation is appended to the incident store.

Confidence calibration and the causality discipline (no `CAUSED_BY` claims without
mechanism + direction + exclusivity) keep the output honest by construction.

## Safety model

- **Read-only by default.** Investigation never mutates the cluster.
- **Remediation is gated.** `kubetective action <incident-id>` only previews
  (`kubectl rollout undo … --dry-run=client` equivalents). Applying requires
  `--apply <id> --yes` — an explicit human approval — and appends an audit
  record with user, timestamp, evidence, risk, and result to the incident file.
- **LLM is optional and constrained.** The model receives only a redacted digest
  (no logs, no payload values, no kubeconfig); its output is validated strict
  JSON; it cannot change scores or propose actions. Unreachable models degrade
  gracefully — the deterministic verdict always stands.
- **No secrets leave the machine.** The digest boundary is regression-tested.

## REST server

```sh
kubetective serve --listen :8080
```

| Endpoint | Description |
|---|---|
| `POST /v1/investigate` | Run an investigation; body: `{"target":"deployment/checkout","namespace":"prod","since_minutes":30}` |
| `GET /v1/incidents` | List recorded incident ids |
| `GET /v1/incidents/{id}` | Full incident record |
| `GET /healthz` | Liveness |

## MCP server

```sh
kubetective mcp   # JSON-RPC 2.0 over stdio
```

Tools: `investigate`, `replay`, `list_incidents`, `read_incident`,
`action_preview`. All read-only — there is deliberately no apply tool;
remediation stays human-gated in the CLI.

## LLM explainer

```sh
# OpenAI (or any compatible endpoint: Ollama, vLLM, llama.cpp)
export KUBETECTIVE_LLM_MODEL=gpt-4o-mini
export KUBETECTIVE_LLM_API_KEY=sk-...
kubetective investigate deployment/checkout --llm
```

The output adds an `AI SYNTHESIS (non-authoritative)` section with a plain-language
explanation, uncertainty, follow-ups, and the model's own confidence — kept
visually and semantically separate from the engine's calibrated confidence.

## Scenario suite & evaluation

`scenarios/` contains 15 recorded incidents with ground truth (root cause,
expected top category, minimum score, expected findings). `kubetective benchmark`
replays all of them and fails (exit 1) if any assertion regresses — this is the
gate every analyzer and scoring change must pass.

```sh
make scenarios        # alias for: kubetective benchmark
kubetective evaluate   # full markdown report, CI-friendly
```

Each scenario lives in its own directory: `scenario.yaml` (ground truth) +
`record.jsonl` (recorded observations). Adding a scenario is the contribution
rule for new analyzers.

## Development

```sh
make build test vet fmt tidy
```

- Go ≥ 1.26, no other build-time dependencies.
- Unit tests cover every package (collectors use fake clientsets; scoring,
  calibration, timeline, graph, actions, REST/MCP protocols have dedicated suites).
- Adding an analyzer: implement `analyze.Analyzer` (see `internal/analyze/` for
  worked examples), register it in `internal/cli/root.go`, and add a scenario
  proving it — `kubetective benchmark` must stay green.

See [`CONTRIBUTING.md`](CONTRIBUTING.md) and [`SECURITY.md`](SECURITY.md).

## License

[Apache-2.0](LICENSE)
