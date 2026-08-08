# Changelog

All notable changes to KubeDoctor are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
does **not** yet follow Semantic Versioning (0.x — API may change).

## [Unreleased]

### Added

- `v0.7` — DNS-failure analyzer (coreDNS-down + DNS-event evidence, live
  kube-system fetch); liveness-probe-failure scenario (suite: 15 scenarios);
  `kubedoctor evaluate` markdown evaluation report (per-scenario,
  per-category accuracy, calibration, false-positive check — CI gate);
  calibration hardening: leave-one-out validation, confidence dampening
  (ECE > 10%), and real adoption of the validated temperature via
  `~/.kubedoctor/config.json`.

## [0.6] — 2026-08-07

### Added

- REST server (`kubedoctor serve`): `POST /v1/investigate`,
  `GET /v1/incidents[/{id}]`, `/healthz`.
- MCP server over stdio (`kubedoctor mcp`): read-only tools
  (investigate, replay, list_incidents, read_incident, action_preview).
- Preview actions + human approval (`kubedoctor action <incident-id>`;
  `--apply <id> --yes`), audit records appended to incident files.

## [0.5] — 2026-08-07

### Added

- Optional LLM explainer: OpenAI-compatible providers, redacted digest-only
  input, constraint prompt, validated strict JSON output, `AI SYNTHESIS`
  rendering, graceful degradation.

## [0.4] — 2026-08-07

### Added

- Git collector (go-git) and GitOps collector (Flux + ArgoCD).
- Config-regression analyzer (the "who changed it" hypothesis).
- Risk-leveled, evidence-linked recommendations (read-only).

## [0.3] — 2026-08-07

### Added

- Adaptive collection loop (evidence requests → re-collect → re-analyze).
- Rule-based hypothesis engine (merge, rerank, outrank).
- PVC, service, HPA analyzers.

## [0.2] — 2026-08-07

### Added

- Evidence graph with typed edges, change detector.
- Prometheus collector (memory-metric evidence).
- Scheduling, node-pressure, probe analyzers; scoring calibration.

## [0.1] — 2026-08-07

### Added

- Evidence model, Kubernetes collector, OOM/CrashLoop/ImagePull analyzers.
- Timeline, record/replay (JSONL incident store), CLI text output.
- First benchmark scenarios and the scenario gate.
