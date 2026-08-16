# Alert integrations (inbound)

Point a PagerDuty/Grafana/Slack webhook at `kubetective alert` and every alert
becomes an investigation with **zero API keys**: the payload is parsed locally,
the engine uses its existing cluster access, and the opt-in completion webhook
reports the result back out.

```sh
# Grafana alert webhook (body from the alert, e.g. a captured POST body):
kubetective alert grafana --file=alert.json --format=json

# PagerDuty v2 webhook:
echo '{...}' | kubetective alert pagerduty

# Slack slash-command payload:
echo '{"text":"deployment/checkout since=2h"}' | kubetective alert slack
```

The target is extracted from the payload: Grafana `kubernetes_pod_name` /
`kubernetes_namespace_name` alert labels (also `pod`, `deployment`, `namespace`;
legacy `evalMatches` and unified `alerts[].labels` shapes), PagerDuty incident
titles carrying a resource name (plus `impacted_services`/`service.summary` and
Events API v2 `details` fields), and Slack command text (`deployment/checkout
since=2h`, the `since=` window is honored). Payloads without a Kubernetes target
fail with a readable message instead of guessing. Window precedence:
`--since` flag > payload > `kubetective.yaml` > 30m.
