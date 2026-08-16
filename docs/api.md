# API reference

KubeTective exposes the same investigation pipeline three ways: the CLI, a REST
server, and an MCP server. There is exactly one engine — `api.Investigator` —
and all three are thin adapters over it, so a verdict is identical whichever
door you come through.

## REST

Full machine-readable spec: **[`openapi.yaml`](openapi.yaml)** (OpenAPI 3.1).

```sh
kubetective serve --listen :8080
```

Browse it locally with any OpenAPI viewer:

```sh
npx @redocly/cli preview-docs docs/openapi.yaml
# or
docker run -p 8081:8080 -e SWAGGER_JSON=/spec/openapi.yaml \
  -v "$PWD/docs:/spec" swaggerapi/swagger-ui
```

### Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/investigate` | Run an investigation, return the full result |
| `GET` | `/v1/incidents` | List recorded incident ids, newest first |
| `GET` | `/v1/incidents/{id}` | Read a full incident record |
| `GET` | `/healthz` | Liveness |
| `GET` | `/metrics` | Self-telemetry (expvar counters) |
| `GET` | `/` | Read-only web UI (incident list) |
| `GET` | `/incidents/{id}` | Read-only web page for one incident |

### Example

```sh
curl -sS localhost:8080/v1/investigate \
  -H 'content-type: application/json' \
  -d '{"target":"deployment/checkout","namespace":"prod","since_minutes":30}' \
| jq '{
    status:     .incident.status,
    confidence: .incident.confidence,
    cause:      .hypotheses[0].claim,
    evidence:   [.hypotheses[0].score.lines[] | "\(.label) (\(.delta))"],
    replay:     .meta.record_id
  }'
```

```json
{
  "status": "OOMKILLED",
  "confidence": 0.97,
  "cause": "Configuration regression: commit 9f2c1a7d (checkout: bump CACHE_SIZE 5000 -> 50000) preceded the failure",
  "evidence": [
    "commit 9f2c1a7d: checkout: bump CACHE_SIZE 5000 -> 50000 (30)",
    "commit 6 min before onset (25)",
    "workload observed changed in window (10)",
    "mechanism: failure follows the change (30)"
  ],
  "replay": "incident-1754575200-checkout"
}
```

Negative `delta` values are contradicting evidence — the engine records what
argues *against* its own conclusion, not only what supports it.

### Things worth knowing

- **No authentication.** Run it inside a cluster behind an ingress or mesh that
  terminates auth, or on localhost. Do not expose it to untrusted networks.
- **Read-only.** There is no remediation endpoint. Applying an action stays
  human-gated in the CLI behind an explicit `--yes`.
- **Every investigation is recorded.** A successful `POST /v1/investigate`
  appends a JSONL record and returns its id in `meta.record_id`. Replay it with
  `kubetective replay <id>` and you get the same verdict.
- Request bodies are capped at 1 MiB.

## MCP

```sh
kubetective mcp   # JSON-RPC 2.0 over stdio
```

Tools: `investigate`, `replay`, `list_incidents`, `read_incident`,
`action_preview`. All read-only.

There is deliberately no apply tool. An agent can investigate, read history, and
see what a remediation *would* do; executing it requires a human at a terminal.

Wire it into an MCP client (Claude Code, Cursor, …):

```json
{
  "mcpServers": {
    "kubetective": { "command": "kubetective", "args": ["mcp"] }
  }
}
```

## The result shape

Every adapter returns the same `InvestigationResult`. The CLI renders a narrow
slice of it — "collect more than you show" — while `--format=json` and the REST
API return all of it:

| Field | What it holds |
|---|---|
| `incident` | The header card: status, severity, confidence |
| `hypotheses` | Ranked candidate causes, each with a full score breakdown |
| `findings` | Deterministic analyzer outputs, evidence-anchored |
| `evidence` | Claims tied to observations, with weight and strength |
| `observations` | Every normalized fact collected |
| `timeline` | Observations anchored to incident onset |
| `graph` | Resource relationships (owns, runs-on, selects) |
| `changes` | The "what changed?" ranking |
| `recommendations` | Typed actions from a rule table, with risk |
| `evidence_gaps` | What was expected and missing — never silent |
| `meta` | Engine version, duration, sources hit, record id |

Schemas for all of them are in [`openapi.yaml`](openapi.yaml).
