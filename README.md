# KubeTective

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
brew install GlediLami/tap/kubetective          # Homebrew
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
16/19 scenarios passed (3 hard-set scenarios are advisory: they calibrate, they do not gate)
mutation gate: 13/13 causal claims held (verdict moves when its evidence is removed)
noise gate:    19/19 verdicts held under 500 irrelevant observations
calibration:   18 ground-truth points (2 incorrect), accuracy 89%
  not adopted: out-of-sample ECE 19.9% does not beat the default 7.8%
```

Three things worth saying plainly, because most benchmarks bury them:

**Confidence is not currently calibrated, and the engine says so.** Expected
calibration error is `|confidence − accuracy|`, so on a suite the engine never
fails, the error-minimising answer is 100% every time — a fit against such a
suite learns overconfidence, not calibration. Adoption is refused unless the
suite contains real failures, the fit sits inside its search grid, and it beats
the default out-of-sample. Today it refuses on the third. The number you see is
an evidence-margin score at a hand-set temperature until the suite can support
better.

**Passing is not the same as reasoning.** Each scenario declares what its
verdict depends on; the mutation gate deletes that evidence and requires the
verdict to move. An engine that keyed on "which analyzer fired" would pass all
19 scenarios and fail this.

**The suite is small and the records are miniatures** — 4 to 25 observations,
where a production namespace carries thousands. The noise gate closes part of
that gap. It is a proxy, not the real thing, and this is the project's weakest
axis.

The most useful thing you can contribute is a real incident.
`kubetective sanitize` redacts one for sharing — pseudonymised identifiers,
scrubbed free text, and a gate asserting the verdict survives unchanged.

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

- **Add a scenario.** Record an incident, sanitize it, add ground truth — it
  becomes both a demo and a permanent regression test.
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
