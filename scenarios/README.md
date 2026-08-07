# Scenarios — the open benchmark suite

One directory per scenario (docs/DESIGN.md §17.2):

```
scenarios/oom-after-deploy/
├── scenario.yaml     # ground truth: root cause, expected evidence,
│                     # expected recommendation, severity, score tolerance
├── record.jsonl      # recorded investigation (CI: replay-based benchmark)
└── manifests/        # kind cluster setup (integration benchmark)
```

The benchmark gate (`kubedoctor benchmark`) is the contribution rule for new
analyzers: an analyzer ships with a scenario, and the suite must stay green.

Planned v0.1 suite: `oom-after-deploy`, `crashloop`, `imagepull`,
`pending-unschedulable`.
