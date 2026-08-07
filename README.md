# KubeDoctor — Kubernetes Incident Investigation Engine

> **Status: v0.4 — complete.** Git collector (commits touching target
> manifests via go-git), GitOps collector (Flux Kustomization/HelmRelease +
> ArgoCD Application via dynamic client, silent when CRDs are absent),
> config-regression analyzer (commit/recency/GitOps-drift + symptom mechanism
> — the "who changed it and why" hypothesis), risk-leveled read-only
> recommendations, 13-scenario benchmark (calibration: 12 ground-truth
> points, accuracy 100%). Next: v0.5 LLM explainer, v0.6 REST/MCP + approved
> actions. Design: [`docs/DESIGN.md`](docs/DESIGN.md).

KubeDoctor is an open-source Kubernetes incident investigation engine: given a target
(`pod/checkout-7f84c9`, `deployment/checkout`, `--since=30m`) it collects facts, builds a
timeline and an evidence graph, generates hypotheses, scores them with an explainable,
deterministic model, and renders an evidence-backed explanation.

**Deterministic first, AI second.** The engine is fully functional without an LLM; the LLM is
an optional explainer on top.

```
kubectl investigate pod/checkout-7f84c9        # kubectl plugin
kubectl investigate deployment/checkout --since=2h
kubedoctor replay <incident-id>                # replay a recorded investigation
kubedoctor benchmark                           # scenario benchmark gate
```

## Quick start

```bash
make build          # builds bin/kubedoctor and bin/kubectl-investigate
make test           # unit tests
make install-plugin # kubectl investigate … now works (installs to ~/.local/bin)
```

### 30-second live demo (kind)

```bash
kind create cluster --name kubedoctor-demo
kubectl apply -k scenarios/oom-after-deploy/manifests   # breaks a workload on purpose
sleep 90
kubectl investigate deployment/checkout -n prod --since=10m
```

You get an evidence-backed OOM diagnosis: root cause, scored evidence, timeline, and a
recorded incident you can replay (`kubedoctor replay <incident-id>`).

## Design

- [`docs/DESIGN.md`](docs/DESIGN.md) — full architecture: data model (Observation / Evidence /
  Graph / Timeline / Hypothesis), scoring algorithm, analyzer & collector APIs, security
  model (prompt-injection defense, RBAC, redaction), roadmap, open-source strategy.
- [`prompt.txt`](prompt.txt) — the original master prompt this project was designed from.

## Roadmap (summary)

| Version | Scope |
|---|---|
| v0.1 | ✅ Evidence model, k8s collector, OOM/CrashLoop/ImagePull analyzers, timeline, record/replay, CLI, first 3 benchmark scenarios |
| v0.2 | ✅ Evidence graph, scoring + calibration, change detector, Prometheus collector, scheduling/node-pressure/probe analyzers, 8 scenarios |
| v0.3 | ✅ Adaptive collection (NeedsEvidence loop), rule-based hypothesis engine, pvc/service/hpa analyzers, 11 scenarios |
| v0.4 | ✅ Git + Flux/ArgoCD collectors, config-regression analyzer, risk-leveled recommendations |
| v0.5 | LLM explainer (digest-only, local-LLM support) |
| v0.6+ | REST, MCP server, preview + approved actions, incident memory |

Full roadmap: [`docs/DESIGN.md`](docs/DESIGN.md#21-roadmap).

## License

Apache-2.0. See [LICENSE](LICENSE).
