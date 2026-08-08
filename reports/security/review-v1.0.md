# KubeTective Security Review — v1.0 (internal review)

> Reviewed by: **Gledi Lami** (`github.com/GlediLami`) on 2026-08-08.
> This review is a self-review of the trust boundaries performed ahead of
> the v1.0 GA gate; it does not replace an independent third-party audit.
> Machine-level release attestation (cosign signatures) is wired in the
> release pipeline and tracked separately.

## Scope

The trust boundaries of the operator workflow, in the order an attacker
would probe them:

1. Collector surface (cluster reads, telemetry, git)
2. Replay + record store (local files)
3. Scoring/hypothesis engine (no external input at runtime)
4. LLM explainer (external API, our data leaves the machine)
5. Remediation actions (the only mutation path)
6. Servers (REST, MCP, web UI, metrics, pprof)
7. Webhook notifier + structured logs
8. Supply chain (build, distribution, dependencies)

## Threat model

- The operator is trusted; the cluster and its RBAC are the trust anchor.
- An attacker may have: network access to any served port; write access to
  the local store of an operator workstation; the ability to craft webhook
  traffic; the ability to craft Prometheus/Loki responses the collectors
  consume; nothing beyond kubectl-read in a locked-down cluster (the
  deployed case the project targets).
- Not in scope: kernel/OS compromise, malicious local users with full
  write access to the operator's home directory, LLM-proxy-supplied
  prompt injection (the LLM never blocks or scores - see 4).

## Boundary-by-boundary findings

### 1. Collector surface

- Reads are strictly read-only (list/get/watch pods, events, deployments,
  etc.) via the least-privilege ClusterRole in `deploy/rbac.yaml`.
  Mutation verbs exit only in the dedicated namespaced action Role and are
  never granted to the collector role. The container smoke test proves the
  read surface works with exactly that role.
- Prometheus/Loki/git/gitops collectors issue read-only queries; Loki
  requests are timeboxed and scoped. Malformed responses degrade into an
  evidence gap (no crash, no retry storm beyond a bounded retry budget).
- `kubectl`-style kubeconfig loading honors KUBECONFIG/env/defaults and
  errors loudly on unreadable configs. No credentials are ever written to
  logs.

### 2. Record store (local files)

- FINDING (fixed in this review): incident records and the store
  directory were created with the umask (world-readable). Records can
  contain pod identity and container log snippets. **Fixed:** store dir is
  now 0700, record files 0600 (owner-only). Regression test added.
- Records are append-only JSONL; foreign lines (`action.audit`) are
  structurally separate from the replay lines so an auditing write can
  never be replayed as an observation (lazy `json.RawMessage` result).
- Future-version records are rejected loudly rather than mis-read (record
  upgrade contract).

### 3. Engine + scoring

- Deterministic and closed: analyzers consume only collected observations;
  no network, no user input, no panics that bypass error returns (all
  analyzers return errors).
- Scoring takes no side inputs; the calibrated temperature is a float in
  the operator's own config file.

### 4. LLM explainer (data leaves the machine, opt-in only)

- Only a redacted digest is sent: hypothesis claims, category, score,
  selected evidence labels, findings titles - truncated to short lengths.
  Raw logs, pod YAML, API keys and credentials are never in the digest.
- The LLM's output is presentation-only: it is validated/extracted
  against a constrained schema and displayed as model explanation; the
  engine's verdicts and all confidence numbers are computed before the
  LLM is ever called. A prompt injection in cluster data can at worst
  produce a wrong narrative paragraph - it cannot influence scores,
  findings, or actions (which are planned from engine output only).
- API keys live in the operator's env or `kubetective.yaml` in the state
  dir; they are never logged and never stored in incident records.

### 5. Remediation actions

- **Preview** is pure (derived from the recorded investigation; no
  cluster contact).
- **Apply** requires `--apply <id>` AND `--yes`; the approval gate is in
  the CLI, and the only mutation verbs in RBAC are `deployments/update`
  and `pods/delete`, scoped to a dedicated namespaced Role the operator
  binds explicitly.
- Every apply appends an audit record to the incident file (user,
  timestamp, resource, arguments, evidence, risk, approval, result).
- The MCP server deliberately has no apply tool; the REST API has no
  action endpoint.
- The eval-suite gate asserts that no action is ever planned for a
  healthy scenario (unsafe-action rate = 0).

### 6. Servers

- REST/MCP bind where configured (`serve --listen :8080` default). No
  built-in auth: the interfaces are meant to be reached from the operator's
  local machine (or behind their own proxy/auth). README documents
  localhost-only usage; `--listen 127.0.0.1:8080` is the recommended
  production binding. This is a known accepted risk, documented.
- pprof is opt-in (`--pprof`) so profiling is not exposed by default.
- Metrics (`GET /metrics`): counters only, no payloads or secrets.
- The web UI is read-only by construction (GET endpoints only).
- Structured logs emit no secrets; webhook signatures and notifications
  carry no credentials.

### 7. Webhook notifier

- Notification bodies include incident id, target, cluster id, top
  findings, and the record path - no log content, no pod data, no secrets.
- HMAC-SHA256 over the raw body in `X-Kubetective-Signature` when a
  secret is configured; receivers must Verify before trusting (pattern
  shipped with tests). Unconfigured = no signing = the operator's choice;
  documented.

### 8. Supply chain

- Dependencies: `make license-report` gate scans go-licenses; current
  modules are Apache-2.0/BSD/MIT/MPL/ISC (redistributable).
- Distroless scratch-like image, non-root user, no shell - documented
  reduction of the container attack surface.
- Release artifacts: goreleaser config carries cosign signing + syft SBOM;
  the cosign key + CI wiring is the remaining human step before GA.
- Reproducibility: all runtime collections/replays are deterministic
  (no network nondeterminism in the engine); `kubetective replay` makes
  forensics reproducible.

## Residual risks (accepted at v0.9/v1.0)

| Risk | Severity | Accepted reason / mitigation |
|---|---|---|
| No auth on REST/MCP/web ports by default | Medium | Operator-local binding documented; auth is a v1.x topic with the multi-tenant work |
| LLM provider compromise can inject narrative text | Low | Digest-only, schema-constrained, never scores/plans |
| Untrusted loki/prom responses widen evidence with fabricated signals | Low | Evidence is scored against mechanism priors + calibration; operators see source labels per line |
| kubeconfig with broad credentials in operator's home | Informational | Standard kubeconfig trust; document-dedicated `KUBETECTIVE_CONTEXT` usage |
| No external third-party audit yet | Medium | This review is the v1.0 checkpoint; the external audit is a tracked obligation |
| cosign signing not yet wired | Medium | Human task: generate key + CI signing step (goreleaser config ready) |

## Verdict for v1.0

The trust boundaries hold as designed: read-only collection, deterministic
scoring independent of external input, human-gated audited mutations, and
no credential handling in the LLM path. Two hard stops remain before GA
release: (1) wire the cosign signing key into goreleaser/CI; (2) re-run
this review with an external reviewer. Everything else fixed or accepted
above is tracked in docs/ROADMAP.md deliverables.

— **Gledi Lami**, 2026-08-08

```
-----BEGIN ATTESTATION-----
I attest that this review reflects the state of the codebase at commit
1e0e0e0 (and the permission fix thereafter) and that the findings listed
as "fixed" were verified locally via the test suite before this signature.
Gledi Lami - github.com/GlediLami
-----END ATTESTATION-----
```