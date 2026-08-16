# KubeTective

[![ci](https://github.com/GlediLami/kubetective/actions/workflows/ci.yml/badge.svg)](https://github.com/GlediLami/kubetective/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/GlediLami/kubetective?sort=semver)](https://github.com/GlediLami/kubetective/releases/latest)
[![go report card](https://goreportcard.com/badge/github.com/GlediLami/kubetective)](https://goreportcard.com/report/github.com/GlediLami/kubetective)
[![go reference](https://pkg.go.dev/badge/github.com/GlediLami/kubetective.svg)](https://pkg.go.dev/github.com/GlediLami/kubetective)
[![license](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

**Kubernetes told you the pod was OOMKilled. KubeTective tells you which commit did it.**

An incident investigation engine that collects the facts, builds a timeline,
ranks the possible causes, and shows every point of confidence as a line of
evidence you can read. No LLM in the verdict path — the same incident produces
the same answer every time.

[Docs](https://gledilami.github.io/kubetective/) ·
[Changelog](CHANGELOG.md) ·
[Contributing](CONTRIBUTING.md)

![demo](demo.gif)

## What it looks like

```sh
$ kubectl investigate deployment/checkout --since=30m
╭──────────────────────────────────────────────────╮
│ INCIDENT: deployment/prod/checkout               │
│ Status: OOMKILLED                                │
│ Severity: HIGH                                   │
│ Confidence: 97%                                  │
╰──────────────────────────────────────────────────╯

ROOT CAUSE
  Configuration regression: commit 9f2c1a7d (checkout: bump CACHE_SIZE
  5000 -> 50000) preceded the failure

EVIDENCE
  ✓ commit 9f2c1a7d: checkout: bump CACHE_SIZE 5000 -> 50000 (+30)
  ✓ commit 6 min before onset (+25)
  ✓ workload observed changed in window (+10)
  ✓ mechanism: failure follows the change (+30)

RECOMMENDATION
  roll back deployment/prod/checkout to the last known-good revision [MEDIUM]
```

`OOMKilled` is the symptom, and `kubectl` already told you that. The answer is
the commit six minutes earlier, and the four evidence terms that got there.

## Try it in 2 minutes — no broken cluster needed

```sh
git clone https://github.com/GlediLami/kubetective.git && cd kubetective
make build
bin/kubetective replay scenarios/config-regression/record.jsonl
```

That is the incident above, replayed from a recorded JSONL file. Every scenario
in [`scenarios/`](scenarios/) works the same way — a real investigation you can
re-run without a cluster.

## Install

```sh
brew install gledilami/kubetective/kubetective   # Homebrew
go install github.com/GlediLami/kubetective/cmd/kubetective@latest
```

As a `kubectl` plugin — put a binary named `kubectl-investigate` on your `PATH`:

```sh
make install-plugin
kubectl investigate deployment/checkout --since=30m
```

Building from source needs Go 1.26+. No other build-time dependencies.

## Quick start

```sh
kubetective investigate deployment/checkout --since=30m   # investigate
kubetective incidents                                     # what have I looked at?
kubetective replay <incident-id>                          # re-run it
kubetective doctor                                        # is everything wired up?
```

Add evidence sources as you have them — each is optional and degrades quietly:

```sh
kubetective investigate deployment/checkout \
  --prometheus-url http://localhost:9090 \
  --loki-url http://localhost:3100 \
  --git-repo ~/code/manifests
```

## Why deterministic

The verdict comes from a rule-based engine, not a language model. Each score is
a sum of weighted evidence terms drawn from a
[documented six-band scale](internal/score/scale.go), and every term is printed.
An optional LLM layer can rephrase the verdict in plainer language — it can
never change a score, invent a cause, or propose an action.

That makes an investigation a test artifact: it replays byte-identically, so it
can gate CI. An LLM chat cannot.

## What the benchmark actually shows

Four gates run on every commit. These are the real numbers, not aspirations:

```
17/25 scenarios passed (8 hard-set scenarios are advisory: they calibrate, they do not gate)
mutation gate: 17/17 causal claims held (verdict moves when its evidence is removed)
noise gate:    25/25 verdicts held under 500 irrelevant observations
calibration:   24 ground-truth points (7 incorrect), accuracy 71%
  adopted: T=54, out-of-sample NLL 0.586 vs 0.652 and Brier 0.199 vs 0.220
```

**Accuracy is 71%, and it used to read 89%.** Nothing regressed. Six of those
scenarios were recorded off a live cluster instead of written in an editor, and
five of the six are cases the engine gets wrong — a CPU limit starving a
liveness probe, a frontend blamed for its backend's image pull, an evicted pod,
a missing Secret, a blocked init container. The 89% was measuring a suite built
from problems the engine already knew how to solve.

Three things worth saying plainly, because most benchmarks bury them:

**Confidence is calibrated now, and the number that made it calibratable was a
worse one.** Expected calibration error is `|confidence − accuracy|`, so on a
suite the engine never fails, the error-minimising answer is 100% every time —
a fit against such a suite learns overconfidence, not calibration. Adoption is
refused unless the suite contains real failures, the fit sits inside its search
grid, and it beats the default out-of-sample on two independent proper scoring
rules. It stayed refused for as long as the suite was too easy to fail.

**Passing is not the same as reasoning.** Each scenario declares what its
verdict depends on; the mutation gate deletes that evidence and requires the
verdict to move. An engine that keyed on "which analyzer fired" would pass every
solvable scenario and fail this.

**There is a known false positive, and it is in the suite as three failing
cases.** On the eviction, the missing Secret, and the blocked init container the
engine answers *"Configuration regression: a change preceded the incident"* at
79% — with no commit, no diff, and no change named. Deleting every recorded
event leaves the verdict untouched, which is the tell: it rests on the
deployment existing, not on anything that happened.
[`live-ephemeral-storage-evict`](scenarios/live-ephemeral-storage-evict/) and
its two siblings carry no mutations for exactly that reason — there is no causal
claim to make about a conclusion no fact supports.

The most useful thing you can contribute is a real incident, and
`kubetective scenario new <incident-id>` does the mechanical work: sanitises
the recording, replays it, sweeps the evidence, and drafts the scenario for
you to correct. See [scenarios/README.md](scenarios/README.md).

A scenario the engine gets **wrong** is worth more than one it gets right. Five
of the six live recordings are misses, and they are the reason confidence can
be calibrated at all.

## What it finds

11 analyzers: OOM kills, crash loops, image pull failures, unschedulable pods,
node pressure, liveness and readiness probes, PVC binding, service selector
mismatches, HPA ceilings, DNS failures, and configuration regressions traced to
a Git or GitOps commit.

Evidence comes from Kubernetes, Prometheus, Loki, Git, and GitOps controllers
(Flux, ArgoCD). Missing sources become visible gaps, never silent ones.

Runs as a CLI, a `kubectl` plugin, a REST server, or an MCP server. Every
investigation is recorded so it can be replayed, audited, and used as a
benchmark case.

## Documentation

| | |
|---|---|
| [CLI reference](docs/cli.md) | Every command and flag |
| [Configuration](docs/configuration.md) | Config file, env vars, per-context profiles |
| [API reference](docs/api.md) · [OpenAPI spec](docs/openapi.yaml) | REST and MCP servers |
| [Architecture](docs/architecture.md) | How the pipeline works, and the safety model |
| [Alert integrations](docs/alerts.md) | PagerDuty, Grafana, Slack — no API keys |
| [Benchmark suite](scenarios/README.md) | What the four gates measure, and why |
| [Comparison](docs/comparison.md) | Versus `kubectl`, LLM chat, and k8sgpt |

## Contributing

Good first issues:

- **Add a scenario.** `kubetective scenario new <incident-id>` sanitises a
  recording, replays it, and drafts the ground truth and mutations for you to
  correct. It becomes both a demo and a permanent regression test.
- **Harden an analyzer.** Find a false positive against the suite, fix the
  scoring, let `kubetective benchmark` prove it.
- **Add an output format.** `json` and `markdown` exist; `sarif` and `slack` are open.
- **Wire an evidence source** (Datadog, Grafana Cloud) behind the collector interface.

```sh
make build test vet fmt      # the whole loop
kubetective benchmark        # must stay green
```

Adding an analyzer means implementing `analyze.Analyzer`, registering it in
`internal/cli/root.go`, and shipping a scenario that proves it. See
[CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE)
