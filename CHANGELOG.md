# Changelog

All notable changes to KubeTective are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
does **not** yet follow Semantic Versioning (0.x - API may change).

## [Unreleased]

### Added

- `v0.8` - Loki log collector (`--loki-url` / `KUBETECTIVE_LOKI_URL`):
  log evidence for the adaptive loop from Loki, silent degradation;
  incident memory v1 (`kubetective incidents similar <id>`, Jaccard
  overlap of failure shapes, "seen this before" note on every
  investigation); read-only web UI (`kubetective serve`: `GET /`,
  `GET /incidents/{id}`); self-telemetry (`GET /metrics`, expvar);
  GitHub Actions CI (build/vet/test/benchmark/evaluate gates);
  `dns-events-only` scenario (suite: 16 scenarios).

## [0.9] - 2026-08-08

### Added

- Config file (`kubetective.yaml` in the state dir): kubeconfig,
  context, namespace, since, prometheus/loki URLs, git repo, cluster id,
  LLM provider settings. Precedence: CLI flag > env var > config file
  > default. Known env vars: `KUBETECTIVE_HOME`, `KUBECTIVE_PROMETHEUS`,
  `KUBETECTIVE_LOKI_URL`, `KUBECTIVE_GIT_REPO`, `KUBECTIVE_CLUSTER_ID`.
- Real doctor (`kubetective doctor`): health checks for version, config
  file, calibration state, cluster connectivity, incident store,
  prometheus and loki reachability; non-zero exit on any failing check
  (CI-friendly).
- Multi-cluster memory: incidents are tagged with an anonymized cluster
  id (sha256 of the API server host) and
  `kubetective incidents similar <id> --cluster <id>` scopes the lookup;
  untagged (legacy) incidents stay comparable from any scope.
- Multi-cluster profiles: `clusters:` in `kubetective.yaml` holds
  per-context settings (kubeconfig, namespace, sources, cluster id, LLM
  fields) merged field-by-field over the top-level defaults.
- Container smoke test (`hack/smoke-container.sh`, CI job `smoke`): the
  distroless image runs `kubetective doctor` and a live crash-loop
  investigation in a kind cluster under the least-privilege RBAC from
  `deploy/rbac.yaml` — any permission gap fails the build.
- Packaging: distroless Dockerfile (non-root, `KUBETECTIVE_HOME`),
  goreleaser config (linux/darwin, deb/rpm, container, cosign, SBOM),
  krew plugin manifest for `kubectl investigate`.
- Least-privilege RBAC manifests: read-only cluster role (collector
  surface), namespaced write role for human-gated rollback/restart
  actions.

### Fixed

- MCP `serverInfo` reported the hard-coded version; now reports the
  engine version (`kubetective mcp` and `kubetective version` can no
  longer drift).
- README advertised a 12th analyzer that does not exist; corrected to
  11 (per-analyzer table still authoritative).

## [0.7] - 2026-08-08

### Added

- DNS-failure analyzer (coreDNS-down + DNS-event evidence, live kube-system
  fetch); liveness-probe-failure scenario (suite: 15 scenarios);
  `kubetective evaluate` markdown evaluation report (per-scenario,
  per-category accuracy, calibration, false-positive check - CI gate);
  calibration hardening: leave-one-out validation, confidence dampening
  (ECE > 10%), and real adoption of the validated temperature via
  `~/.kubetective/config.json`.

### Added

- REST server (`kubetective serve`): `POST /v1/investigate`,
  `GET /v1/incidents[/{id}]`, `/healthz`.
- MCP server over stdio (`kubetective mcp`): read-only tools
  (investigate, replay, list_incidents, read_incident, action_preview).
- Preview actions + human approval (`kubetective action <incident-id>`;
  `--apply <id> --yes`), audit records appended to incident files.

## [0.5] - 2026-08-07

### Added

- Optional LLM explainer: OpenAI-compatible providers, redacted digest-only
  input, constraint prompt, validated strict JSON output, `AI SYNTHESIS`
  rendering, graceful degradation.

## [0.4] - 2026-08-07

### Added

- Git collector (go-git) and GitOps collector (Flux + ArgoCD).
- Config-regression analyzer (the "who changed it" hypothesis).
- Risk-leveled, evidence-linked recommendations (read-only).

## [0.3] - 2026-08-07

### Added

- Adaptive collection loop (evidence requests → re-collect → re-analyze).
- Rule-based hypothesis engine (merge, rerank, outrank).
- PVC, service, HPA analyzers.

## [0.2] - 2026-08-07

### Added

- Evidence graph with typed edges, change detector.
- Prometheus collector (memory-metric evidence).
- Scheduling, node-pressure, probe analyzers; scoring calibration.

## [0.1] - 2026-08-07

### Added

- Evidence model, Kubernetes collector, OOM/CrashLoop/ImagePull analyzers.
- Timeline, record/replay (JSONL incident store), CLI text output.
- First benchmark scenarios and the scenario gate.
