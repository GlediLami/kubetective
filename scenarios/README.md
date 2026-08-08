# Scenarios - the open benchmark suite

One directory per scenario:

```
scenarios/oom-after-deploy/
├── scenario.yaml     # ground truth: root cause, expected evidence,
│                     # expected recommendation, severity, score tolerance
├── record.jsonl      # recorded investigation (CI: replay-based benchmark)
└── manifests/        # kind cluster setup (integration benchmark)
```

The benchmark gate (`kubetective benchmark`) is the contribution rule for new
analyzers: an analyzer ships with a scenario, and the suite must stay green.

Current suite (16): `oom-after-deploy`, `oom-memory-growth`, `crashloop`,
`imagepull`, `pending-unschedulable`, `bad-readiness-probe`,
`liveness-probe-failure`, `node-pressure`, `pvc-unschedulable`,
`service-selector-mismatch`, `hpa-at-max`, `config-regression`,
`gitops-drift`, `dns-failure`, `dns-events-only` (secondary-signal lock),
and `healthy` (negative control - the engine must stay silent).
