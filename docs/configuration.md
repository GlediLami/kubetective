# Configuration

KubeTective reads kubeconfig the same way kubectl does (`--kubeconfig`, `--context`,
or the default loading rules).

`kubetective.yaml` in the state directory (`~/.kubetective/kubetective.yaml`) sets
defaults for an investigation. Example:

```yaml
# ~/.kubetective/kubetective.yaml
context: prod-eu
namespace: payments
since: 30m
kubeconfig: ~/.kube/config
prometheus_url: http://localhost:9090
loki_url: http://localhost:3100
git_repo: ~/code/payments-manifests
cluster_id: prod-eu # optional: override the auto-derived cluster identity
# webhook_url: https://ops.example.com/kubetective-hook   # opt-in completion notification
# webhook_secret: s3cret                                # HMAC key for the notification signature
llm:
  provider: openai      # or ollama, vllm, llama.cpp
  model: gpt-4o-mini
  base_url: https://api.openai.com/v1
  api_key: sk-...       # or rely on KUBETECTIVE_LLM_API_KEY

# Per-context overrides: when --context (or KUBETECTIVE_CONTEXT) selects
# one of these, the profile merges onto the top-level defaults field by
# field. Everything above is the fallback for any other context.
clusters:
  staging:
    namespace: staging
    loki_url: http://staging-loki:3100
  drone-eu:
    context: drone-eu
    kubeconfig: ~/.kube/corp
    namespace: drone
    cluster_id: drone-eu
```

Precedence is CLI flag > environment variable > per-context profile
> top-level `kubetective.yaml` > default.

| Environment variable | Purpose |
|---|---|
| `KUBETECTIVE_PROMETHEUS` | Prometheus base URL for metric evidence (same as `--prometheus-url`) |
| `KUBETECTIVE_LOKI_URL` | Grafana Loki base URL for log evidence (same as `--loki-url`) |
| `KUBETECTIVE_GIT_REPO` | Path to the manifests git checkout (same as `--git-repo`) |
| `KUBETECTIVE_CONTEXT` | kubeconfig context to target (same as `--context`) |
| `KUBETECTIVE_CLUSTER_ID` | Override the incident-memory cluster id (default: sha256 of the API server host) |
| `KUBETECTIVE_LLM_MODEL` | LLM model name for the explainer |
| `KUBETECTIVE_LLM_BASE_URL` | OpenAI-compatible API base URL (default `https://api.openai.com/v1`) |
| `KUBETECTIVE_LLM_API_KEY` | API key (local servers like Ollama don't need one) |
| `KUBETECTIVE_WEBHOOK_URL` | POST a signed completion notification after every investigation (opt-in) |
| `KUBETECTIVE_WEBHOOK_SECRET` | Shared HMAC secret; the webhook body is signed with it (verify on the receiver side) |
| `KUBETECTIVE_LOG_FORMAT` | Structured logs to stderr: `json` or `text` (off unless set; same as `--log-format`) |
| `KUBETECTIVE_LOG_LEVEL` | Log level: `debug`, `info`, `warn` (default `info`) |
| `KUBETECTIVE_HOME` | State directory (default `~/.kubetective`: incidents, config) |
| `KUBETECTIVE_CONFIG` | Config file path (default `~/.kubetective/config.json`) |

Run `kubetective doctor` to check the whole wiring: engine version, config file,
calibration state, cluster connectivity, incident store, and Prometheus/Loki
reachability. It exits non-zero if anything fails, so it works in CI.

The calibrated scoring temperature is persisted to `~/.kubetective/config.json` by
`benchmark`/`evaluate` and loaded by every invocation.

## LLM explainer (optional)

The explainer rephrases the engine's verdict in plain language. It never
changes a score, invents a cause, or proposes an action — see
[architecture.md](architecture.md#safety-model) for the boundary.

```sh
# OpenAI, or any compatible endpoint: Ollama, vLLM, llama.cpp
export KUBETECTIVE_LLM_MODEL=gpt-4o-mini
export KUBETECTIVE_LLM_API_KEY=sk-...
kubetective investigate deployment/checkout --llm
```

Output gains an `AI SYNTHESIS (non-authoritative)` section carrying the
explanation, its uncertainty, follow-ups, and the model's own confidence — kept
visually and semantically separate from the engine's. An unreachable model
degrades quietly; the deterministic verdict always stands.
