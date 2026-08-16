# KubeTective Evaluation Report

- engine: `v1.0.1`
- date: 2026-08-16 20:55:22 UTC
- suite: 25 scenarios

## Scenario results

| scenario | status | top category | score | time | failures |
|---|---|---|---:|---:|---|
| ambiguous-oom-or-node | MISS (advisory) | memory | 91% | 0s | only 1 live hypothesis on the target; an under-determined incident must leave competitors standing |
| ambiguous-probe-or-crash | MISS (advisory) | crashloop | 91% | 0s | only 1 live hypothesis on the target; an under-determined incident must leave competitors standing; top hypothesis category = probe, want crashloop |
| ambiguous-stale-commit | MISS (advisory) | crashloop | 94% | 0s | top hypothesis category = config-regression, want crashloop |
| bad-readiness-probe | PASS | probe | 91% | 0s | - |
| config-regression | PASS | config-regression | 97% | 0s | - |
| crashloop | PASS | crashloop | 89% | 0s | - |
| dns-events-only | PASS | crashloop | 89% | 0s | - |
| dns-failure | PASS | dns | 95% | 0s | - |
| gitops-drift | PASS | config-regression | 91% | 0s | - |
| healthy | PASS | - | - | 0s | - |
| hpa-at-max | PASS | crashloop | 89% | 0s | - |
| imagepull | PASS | image | 89% | 0s | - |
| live-cpu-throttle-probe | MISS (advisory) | cpu-throttling | 91% | 0s | top hypothesis score = 0.910, want ≤ 0.800 — the evidence does not support this much confidence; top hypothesis category = probe, want cpu-throttling |
| live-ephemeral-storage-evict | MISS (advisory) | storage-exhaustion | 79% | 0s | top hypothesis score = 0.787, want ≤ 0.600 — the evidence does not support this much confidence; top hypothesis category = config-regression, want storage-exhaustion |
| live-init-blocked | MISS (advisory) | init-dependency | 79% | 0s | top hypothesis score = 0.787, want ≤ 0.600 — the evidence does not support this much confidence; top hypothesis category = config-regression, want init-dependency |
| live-missing-secret-mount | MISS (advisory) | missing-secret | 79% | 0s | top hypothesis score = 0.787, want ≤ 0.600 — the evidence does not support this much confidence; top hypothesis category = config-regression, want missing-secret |
| live-oom-config-regression | PASS | config-regression | 97% | 0s | - |
| live-upstream-dependency | MISS (advisory) | upstream-dependency | 87% | 1ms | top hypothesis score = 0.872, want ≤ 0.800 — the evidence does not support this much confidence; top hypothesis category = probe, want upstream-dependency |
| liveness-probe-failure | PASS | probe | 91% | 0s | - |
| node-pressure | PASS | node | 92% | 0s | - |
| oom-after-deploy | PASS | memory | 94% | 0s | - |
| oom-memory-growth | PASS | memory | 98% | 0s | - |
| pending-unschedulable | PASS | scheduling | 91% | 0s | - |
| pvc-unschedulable | PASS | pvc | 91% | 0s | - |
| service-selector-mismatch | PASS | service | 91% | 0s | - |

**Gate: 17/25 scenarios passed**

## Per-category accuracy (top-1)

| category | correct | total | accuracy |
|---|---:|---:|---:|
| config-regression | 3 | 3 | 100% |
| cpu-throttling | 0 | 1 | 0% |
| crashloop | 3 | 5 | 60% |
| dns | 1 | 1 | 100% |
| image | 1 | 1 | 100% |
| init-dependency | 0 | 1 | 0% |
| memory | 3 | 3 | 100% |
| missing-secret | 0 | 1 | 0% |
| node | 1 | 1 | 100% |
| probe | 2 | 2 | 100% |
| pvc | 1 | 1 | 100% |
| scheduling | 1 | 1 | 100% |
| service | 1 | 1 | 100% |
| storage-exhaustion | 0 | 1 | 0% |
| upstream-dependency | 0 | 1 | 0% |

## Confidence calibration

| metric | value |
|---|---:|
| ground-truth points | 24 |
| of which incorrect | 7 |
| top-1 accuracy | 71% |
| ECE @ default T=26 | 19.5% |
| ECE @ fitted T=54.0 | 11.7% |
| out-of-sample NLL (fitted vs default) | 0.5855 vs 0.6520 |
| out-of-sample Brier (fitted vs default) | 0.1986 vs 0.2196 |
| LOO ECE (reported, does not gate) | 15.1% |
| scan grid | [17.8, 535.0] |
| recalibrated T adopted | yes |
| operating T | 54.0 |

> Adoption is decided on out-of-sample NLL **and** Brier, both of which must
> beat the default by at least 2%. ECE is reported because it is the
> interpretable number — "displayed confidence is off by this much" — but it is
> a binned statistic, and gating on it made the decision turn on which side of a
> bucket edge a single scenario landed. See `score.adoptionRefusal`.

> ⚠️ ECE exceeds 10% - displayed confidence is dampened toward 50% (calibration rule).

## Robustness

### Mutation gate (is the verdict caused by the evidence?)

Each mutation deletes the evidence a verdict rests on and requires the verdict to move.
A verdict that survives the loss of its own support was never caused by it.

| scenario | mutation | removed | verdict after | result |
|---|---|---:|---|---|
| bad-readiness-probe | without-probe-events | 3 obs | - (0%) | ✅ as declared |
| config-regression | without-the-commit | 1 obs | memory (94%) | ✅ as declared |
| dns-failure | without-coredns-state | 1 obs | crashloop (89%) | ✅ as declared |
| dns-failure | without-sandbox-events | 4 obs | crashloop (89%) | ✅ as declared |
| gitops-drift | without-argocd-state | 1 obs | crashloop (89%) | ✅ as declared |
| imagepull | without-waiting-state | 1 obs | scheduling (79%) | ✅ as declared |
| live-cpu-throttle-probe | without-the-probe-events | 7 obs | dns (50%) | ✅ as declared |
| live-oom-config-regression | without-the-commit | 2 obs | memory (94%) | ✅ as declared |
| live-oom-config-regression | without-the-kill | 1 obs | dns (50%) | ✅ as declared |
| live-upstream-dependency | without-the-probe-events | 13 obs | dns (50%) | ✅ as declared |
| liveness-probe-failure | without-probe-events | 3 obs | crashloop (89%) | ✅ as declared |
| node-pressure | without-node-conditions | 2 obs | memory (94%) | ✅ as declared |
| oom-after-deploy | termination-records-are-redundant | 17 obs | memory (94%) | ✅ as declared |
| oom-memory-growth | without-metrics | 1 obs | memory (94%) | ✅ as declared |
| oom-memory-growth | without-terminations | 3 obs | - (0%) | ✅ as declared |
| pvc-unschedulable | without-the-claim | 1 obs | scheduling (72%) | ✅ as declared |
| service-selector-mismatch | without-the-service | 1 obs | - (0%) | ✅ as declared |

**Mutation gate: 17/17 held**

### Noise gate (does the verdict survive scale?)

Every scenario is replayed buried under 500 irrelevant observations from unrelated
workloads, spread across the same window. Recorded scenarios carry 4–25 observations;
a production namespace carries thousands. This is the only gate that probes that gap.

| scenario | verdict | under noise | confidence drift | result |
|---|---|---|---:|---|
| ambiguous-oom-or-node | memory | memory | +0.0% | ✅ held |
| ambiguous-probe-or-crash | probe | probe | +0.0% | ✅ held |
| ambiguous-stale-commit | config-regression | config-regression | +0.0% | ✅ held |
| bad-readiness-probe | probe | probe | +0.0% | ✅ held |
| config-regression | config-regression | config-regression | +0.0% | ✅ held |
| crashloop | crashloop | crashloop | +0.0% | ✅ held |
| dns-events-only | crashloop | crashloop | +0.0% | ✅ held |
| dns-failure | dns | dns | +0.0% | ✅ held |
| gitops-drift | config-regression | config-regression | +0.0% | ✅ held |
| healthy | (silent) | - | +0.0% | ✅ held |
| hpa-at-max | crashloop | crashloop | +0.0% | ✅ held |
| imagepull | image | image | +0.0% | ✅ held |
| live-cpu-throttle-probe | probe | probe | +0.0% | ✅ held |
| live-ephemeral-storage-evict | config-regression | config-regression | +0.0% | ✅ held |
| live-init-blocked | config-regression | config-regression | +0.0% | ✅ held |
| live-missing-secret-mount | config-regression | config-regression | +0.0% | ✅ held |
| live-oom-config-regression | config-regression | config-regression | +0.0% | ✅ held |
| live-upstream-dependency | probe | probe | +0.0% | ✅ held |
| liveness-probe-failure | probe | probe | +0.0% | ✅ held |
| node-pressure | node | node | +0.0% | ✅ held |
| oom-after-deploy | memory | memory | +0.0% | ✅ held |
| oom-memory-growth | memory | memory | +0.0% | ✅ held |
| pending-unschedulable | scheduling | scheduling | +0.0% | ✅ held |
| pvc-unschedulable | pvc | pvc | +0.0% | ✅ held |
| service-selector-mismatch | service | service | +0.0% | ✅ held |

**Noise gate: 25/25 verdicts held at 500× scale**

## False-positive check

Healthy control stayed silent: **0 findings on 1 healthy scenario(s)** ✅

## Action safety

| scenario | actions planned | types | unsafe on healthy |
|---|---:|---|---|
| ambiguous-oom-or-node | 0 | - | - |
| ambiguous-probe-or-crash | 0 | - | - |
| ambiguous-stale-commit | 2 | rollback, restart-pod | - |
| bad-readiness-probe | 0 | - | - |
| config-regression | 2 | rollback, restart-pod | - |
| crashloop | 2 | rollback, restart-pod | - |
| dns-events-only | 1 | restart-pod | - |
| dns-failure | 1 | restart-pod | - |
| gitops-drift | 2 | rollback, restart-pod | - |
| healthy | 0 | - | - |
| hpa-at-max | 2 | rollback, restart-pod | - |
| imagepull | 1 | restart-pod | - |
| live-cpu-throttle-probe | 1 | rollback | - |
| live-ephemeral-storage-evict | 1 | rollback | - |
| live-init-blocked | 1 | rollback | - |
| live-missing-secret-mount | 1 | rollback | - |
| live-oom-config-regression | 2 | rollback, restart-pod | - |
| live-upstream-dependency | 1 | rollback | - |
| liveness-probe-failure | 1 | restart-pod | - |
| node-pressure | 0 | - | - |
| oom-after-deploy | 2 | rollback, restart-pod | - |
| oom-memory-growth | 2 | rollback, restart-pod | - |
| pending-unschedulable | 0 | - | - |
| pvc-unschedulable | 0 | - | - |
| service-selector-mismatch | 0 | - | - |

Unsafe-action rate: **0/25** ✅ (no remediation action is ever planned on a healthy scenario; applies are additionally human-gated with --yes and audited)

---
*Generated by `kubetective evaluate` - the CI gate (exit code 1 on failure).*
