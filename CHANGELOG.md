# Changelog

All notable changes to KubeTective are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
does **not** yet follow Semantic Versioning (0.x - API may change).

## [Unreleased]

### Added

- **Mutation gate** — scenarios now declare what their verdict *depends on*
  (`mutations:` in `scenario.yaml`). The gate deletes that evidence, replays,
  and requires the verdict to move as declared. A pass-only suite cannot
  distinguish an engine that reasons from one that keys on which analyzer
  fired; 13 causal claims now check the difference.
- **Noise gate** — every scenario is additionally replayed buried under 500
  irrelevant observations from unrelated workloads, spread across the same
  window. Recorded scenarios carry 4-25 observations; a production namespace
  carries thousands, and nothing previously probed that gap.
- **Hard set** — three genuinely under-determined scenarios
  (`ambiguous-oom-or-node`, `ambiguous-stale-commit`,
  `ambiguous-probe-or-crash`) marked `advisory: true`: they report and feed
  calibration but never break CI. New ground-truth fields `max_score` and
  `expect_competing` assert hedging rather than correctness.
- **`kubetective sanitize`** — redacts a recorded incident for sharing.
  Identifiers become sequential pseudonyms that preserve their relationships
  (a deployment stays recognisably the parent of its pods, so change
  attribution still works); free text is scrubbed for emails, IPs, URLs,
  tokens, and keys; well-known Kubernetes names pass through. Verdict-
  preserving, with a gate asserting every scenario replays to an identical
  category and confidence after redaction.
- Contradicting evidence extended from 2 analyzers to 7 (`probe`, `dns`,
  `scheduling`, `service`, `configregression` join `oom` and `crashloop`),
  each with a test proving the contradiction fires and lowers the score.
- `internal/score/scale.go` documents the six-band weight scale; all 30-odd
  analyzer weights now reference a named band instead of a bare float.
- `StatusLabel()` / `Precedence()` on the `Analyzer` interface: the incident
  status card is derived from the registry, so adding an analyzer no longer
  means editing a rank map and a switch statement in the engine.
- **OpenAPI 3.1 spec** at `docs/openapi.yaml` — 7 paths, 24 schemas, every
  response type of the REST server described. Renders in Swagger UI or Redoc.

### Changed

- **README cut from 530 lines to 186.** It was a manual, not an introduction:
  the full command table, the config-file reference, the environment-variable
  table, alert-payload parsing rules, the REST endpoint table, the MCP tool
  list, and the architecture walkthrough all sat above the fold. Those moved to
  `docs/` (`cli.md`, `configuration.md`, `alerts.md`, `api.md`,
  `architecture.md`, `comparison.md`) and the README now leads with what the
  tool answers that `kubectl` does not.
- The docs site repeated the old "every hypothesis carries a calibrated
  confidence" claim; corrected to describe the adoption gate and the four CI
  gates.

### Fixed

- **Confidence calibration was degenerate and self-persisting.** Expected
  calibration error is `|confidence - accuracy|`, so on a suite the engine
  never fails the error-minimising policy is to answer 100% every time. The
  fit consequently ran to `T=5.0` — the floor of its own search grid — which
  saturates the sigmoid, and the result was written to
  `~/.kubetective/config.json` and loaded by every subsequent invocation. The
  `Dampen` safety net could not fire, because the degenerate fit reported
  ECE 0.0%. Adoption is now refused unless the suite contains incorrect
  predictions, the fit sits strictly inside the search grid, and it beats the
  default out-of-sample; the refusal and its reason are printed. Displayed
  confidences dropped from a uniform 100% to a realistic 89-98%.
- **A failing calibration run could still mutate engine config.** `benchmark`
  adopted and persisted the fitted temperature *before* the gate check, so a
  red suite rewrote the operating temperature anyway - contradicting the
  comment directly below it. Adoption now happens once, after the gate, in
  `adoptCalibration`.
- **Git evidence expired 48 hours after the incident.** The collector cut off
  at `time.Now()-48h` rather than the investigation window, so any incident
  older than two days silently lost its git attribution - including replayed
  ones, breaking the reproducibility guarantee. The cutoff is now anchored to
  `Window.Start`, with the clock used only when no window is supplied, and
  commits landing after the window closes are skipped.
- `TestGitCollectorFindsMatchingCommits` had been failing on `main` since
  2026-08-10: it built commits at fixed dates and asserted against the
  relative cutoff above. Same root cause; the test was right and the code was
  wrong. It now runs against an injected clock.
- The incident header card rendered with four mismatched widths against a
  50-character border, putting a stray `%` outside the box in the README's
  own example output. One constant, one row renderer.
- Hypothesis IDs now have a stated `<prefix>.<resource>` contract
  (`hypothesis.ValidID` / `SplitID`) with a conformance test running every
  analyzer's real output across the suite. The outranking rule silently
  depended on the format with nothing checking it.

### Fixed (earlier)

- The version-consistency gate no longer fails spuriously during the
  release window: a CI run that starts between a version-bump commit and
  the formula update (the release flow pushes main, tag, and formula in
  sequence) now passes while HEAD is the bump commit and the formula still
  pins the previous release; any older formula drift still fails.
- Smoke gate: the actions E2E and the deployment assertion retry the
  investigation until the recorded evidence carries the crash state,
  instead of racing the CrashLoopBackOff oscillation window.

## [1.0.0] - 2026-08-08

> **v1.0.0 GA declaration.** Three checklist items are **waived by owner
> decision** for this release, tracked for the next release:
>
> 1. **Calibration at scale**: 15 ground-truth points ship today, not 30
>    (the evaluation gate still passes: ECE within threshold, LOO-validated
>    temperature).
> 2. **Signed releases**: cosign keys + CI signing wiring remain open; the
>    goreleaser config is ready for them.
> 3. **External security audit**: a self-review is published instead
>    (`reports/security/review-v1.0.md`); third-party review is the first
>    stop on the next release.

### Added

- `v0.8` - Loki log collector (`--loki-url` / `KUBETECTIVE_LOKI_URL`):
  log evidence for the adaptive loop from Loki, silent degradation;
  incident memory v1 (`kubetective incidents similar <id>`, Jaccard
  overlap of failure shapes, "seen this before" note on every
  investigation); read-only web UI (`kubetective serve`: `GET /`,
  `GET /incidents/{id}`); self-telemetry (`GET /metrics`, expvar);
  GitHub Actions CI (build/vet/test/benchmark/evaluate gates);
  `dns-events-only` scenario (suite: 16 scenarios).
- `kubetective incidents search`: filter the incident store by target,
  cluster, analyzer, minimum severity, and time window
  (`--since`/`--until`), newest first (`--limit`); the linear-scan
  JSONL precursor to the indexed memory (roadmap v2).
- Container smoke test covers the full action-safety loop: preview plans
  the restart-pod action, `--apply` without `--yes` is rejected, and an
  approved apply deletes/recreates the pod and appends the audit record -
  all under the least-privilege RBAC (records persist across jobs via a
  PVC). Any gate failure fails the build.
- Record versioning contract: `record.Load` now rejects records written
  by a newer engine loudly (upgrade instead of silent mis-reads), with
  upgrade tests pinning older/no-meta/meta-version reads.
- Operational maturity: `kubetective serve` shuts down gracefully on
  SIGINT/SIGTERM and exposes pprof behind a `--pprof` flag.
- Dependency license report target (`make license-report`); CODEOWNERS
  and a pull request template.
- CLI target parsing accepts fully-qualified `kind/namespace/name`
  targets, the form incident records store.
- Completion webhook, opt-in and HMAC-secured (`webhook_url` +
  `webhook_secret`): a signed notification after every investigation in
  `X-Kubetective-Signature`; receivers must verify before parsing.
  Notification failure never fails the investigation.
- Structured logs (`--log-format json|text` or `KUBETECTIVE_LOG_FORMAT`)
  for investigations, HTTP requests, and action applies; byte-identical
  output when off.
- Evaluation report as a committed artifact (`reports/evaluation/latest.md`,
  `make report`): Action safety section + unsafe-action-rate = 0 gate.
- **Alert integrations (inbound)**: `kubetective alert pagerduty|grafana|slack`
  investigates from a webhook payload (stdin or `--file`), with **zero
  API keys**. Grafana `kubernetes_*` alert labels (unified + legacy
  shapes), PagerDuty incident titles / impacted services / Events API v2
  details, and Slack slash-command text (`deployment/checkout since=2h`)
  name the target; the `since=` window is honored (precedence: flag >
  payload > config > 30m). Payloads without a Kubernetes target fail with
  a readable message instead of guessing. Runs the same recorded,
  webhook-notified pipeline as `investigate`.
- **The uninstall test** (roadmap v1.0 #10): `hack/uninstall-test.sh`
  (CI job `uninstall-test`) clones fresh, builds per the docs, verifies
  both binaries, runs the offline benchmark, and asserts `doctor` fails
  without a cluster with an actionable message - never a stack trace.
- **Docs site**: `site/` (single-page static HTML, no framework)
  published to GitHub Pages by `.github/workflows/pages.yml` at
  https://gledilami.github.io/kubetective/ - install, quickstart,
  integrations, scoring, ops, security.
- **krew submission script**: `hack/submit-krew.sh` completes the
  plugin-index submission in one command - verifies release assets
  exist, fills the per-platform sha256 values, commits the manifest,
  and opens the krew-index PR. It refuses to run without real release
  assets (no placeholder hashes can ship).
- **Security hardening** (from the v1.0 self-review, the only finding):
  incident records are owner-only on disk (files 0600, store dir 0700;
  previously umask-dependent) with a regression test.

### Fixed

- `incidents similar --cluster` scoping was bypassed by incidents that
  carry no cluster id; untagged (legacy) records no longer leak into
  scoped queries.
- Container smoke gate: `--apply` outside the approval gate no longer
  selects the wrong recommended action when a preview plans several.
- `gofmt`, `go vet`, and `staticcheck` clean across the tree.

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
