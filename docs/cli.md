# CLI reference

Run `kubetective <command> --help` for every flag.

| Command | Description |
|---|---|
| `kubetective investigate <resource>` | Run an investigation (flags: `--since`, `--namespace`, `--no-logs`, `--format=json`, `--prometheus-url`, `--loki-url`, `--git-repo`, `--llm*`) |
| `kubetective replay <incident-id>` | Re-run a recorded investigation through the current engine (deterministic) |
| `kubetective sanitize <incident-id>` | Redact a record for sharing: pseudonymised identifiers, scrubbed free text, verdict preserved |
| `kubetective incidents` | List recorded incident ids, newest first |
| `kubetective incidents similar <id> [--cluster <id>]` | Find similar past incidents (incident memory, Jaccard overlap; `--cluster` scopes the lookup to one cluster) |
| `kubetective alert <pagerduty\|grafana\|slack>` | Investigate from a webhook alert payload (stdin or `--file`; zero API keys) |
| `kubetective doctor` | Health check (version, config file, calibration, cluster connectivity, incident store, Prometheus/Loki reachability); exits non-zero on any failure |
| `kubetective action <incident-id>` | Preview remediation actions (read-only; `--apply <id> --yes` to execute with approval) |
| `kubetective benchmark [suite]` | Run the scenario suite gate. Exits 1 on any regression |
| `kubetective evaluate [suite]` | Markdown evaluation report (per-category accuracy, calibration, FP check) |
| `kubetective serve --listen :8080` | REST API server |
| `kubetective mcp` | MCP server over stdio (read-only tools) |
| `kubetective version` | Print the engine version |
