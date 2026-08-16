# Scenarios — the open benchmark suite

One directory per scenario:

```
scenarios/config-regression/
├── scenario.yaml     # ground truth + declared mutations
└── record.jsonl      # recorded investigation, replayed by the gate
```

`kubetective benchmark` is the contribution rule for new analyzers: an analyzer
ships with a scenario, and the suite must stay green.

## What the suite measures

Four gates, each answering a different question.

| Gate | Question | Failure means |
|---|---|---|
| **Scenario** | Does the engine reach the right verdict? | the diagnosis is wrong |
| **Mutation** | Is the verdict *caused* by the evidence? | the engine pattern-matched its way to a right answer |
| **Noise** | Does the verdict survive cluster scale? | the engine is distractible |
| **False positive** | Does the engine stay quiet when nothing is wrong? | it invents incidents |

The mutation gate is the one worth explaining. A suite where every scenario
passes cannot distinguish an engine that reasons from one that keys on which
analyzer happened to fire — both score a clean sweep. So each scenario also
declares what its verdict *depends on*:

```yaml
mutations:
  - name: without-the-commit
    reason: >-
      The commit is what makes this a regression rather than an ordinary OOM.
      Remove the attribution and the verdict must fall back to the mechanism.
    remove_kinds: [git.commit]
    expect_category: memory
```

The gate deletes that evidence, replays, and requires the verdict to move as
declared. Fifteen such claims are checked. One of them is inverted on purpose
(`oom-after-deploy/termination-records-are-redundant`) and asserts the verdict
must *not* move — documenting where the engine's redundancy actually lies.

## The hard set

Three scenarios are marked `advisory: true`. They are genuinely
under-determined incidents where the evidence does not settle the question, and
the engine is **not required to get them right**:

| Scenario | The ambiguity |
|---|---|
| `ambiguous-oom-or-node` | A pod OOMKills; the node reports memory pressure — afterwards. Did the node starve the pod, or the pod the node? |
| `ambiguous-stale-commit` | A commit landed 52 minutes before the crash. Present, plausible, and probably irrelevant. |
| `ambiguous-probe-or-crash` | Probes fail and the container exits 1 — the same fact from two angles. |

They exist for a reason that is easy to miss: **a benchmark the engine never
fails cannot calibrate confidence.** Expected calibration error is
`|confidence − accuracy|`, so at 100% accuracy the error-minimising policy is to
answer 100% every time — and a fit against such a suite learns exactly that.
Confidence can only be calibrated against predictions that were wrong.

The hard set supplies those. It reports, it calibrates, and it never breaks CI.

It took until the suite was 71% accurate for a calibrated temperature to be
adoptable at all. At 89% — the number before the live coverage-gap scenarios
landed — every fit was refused, correctly: a default temperature is hard to beat
out-of-sample when there is almost nothing for it to get wrong.

## Current suite (25)

**From a live cluster (6).** Recorded off real kind clusters against a real API
server, real kubelet events and real container logs, then sanitised for
publication. **Five of the six are misses**, and they are the most valuable
records here.

| Scenario | What it is | Engine says |
|---|---|---|
| `live-oom-config-regression` | OOMKill traced to the commit that caused it | ✅ correct, 97% |
| `live-cpu-throttle-probe` | A 10m CPU limit starves the liveness probe; the kubelet kills a healthy process | ❌ `probe`, 91% — the symptom |
| `live-upstream-dependency` | Frontend readiness fails because its backend never pulled its image | ❌ `probe`, 87% — blames the healthy workload |
| `live-ephemeral-storage-evict` | Container writes past its emptyDir sizeLimit; kubelet evicts it | ❌ `config-regression`, 79% — no change identified |
| `live-missing-secret-mount` | Pod mounts a Secret that was never created | ❌ `config-regression`, 79% |
| `live-init-blocked` | Init container waits forever for a Service nobody deployed | ❌ `config-regression`, 79% |

The bottom three are one bug seen from three unrelated incidents. The verdict
survives deleting every recorded event, which means it does not rest on evidence
about the failure — it rests on the deployment being present in the recording,
which is true of every investigation ever run. Those three carry **no
mutations**, deliberately: a mutation is a claim that a verdict depends on a
specific fact, and this one depends on none.

The middle two are coverage gaps rather than bugs: there is no CPU analyzer, and
causation does not travel across a Service edge to whatever is behind it. Both
are gaps made executable — when someone closes them, the scenarios start passing
on their own.

**Gated (17).** Including `live-oom-config-regression`, plus `oom-after-deploy`,
`oom-memory-growth`, `crashloop`, `imagepull`, `pending-unschedulable`,
`bad-readiness-probe`, `liveness-probe-failure`, `node-pressure`,
`pvc-unschedulable`, `service-selector-mismatch`, `hpa-at-max`,
`config-regression`, `gitops-drift`, `dns-failure`, `dns-events-only`
(secondary-signal lock), `healthy` (negative control — the engine must stay
silent).

**Hard set (8, advisory).** The three ambiguous scenarios above, plus the five
live misses.

Six of the gated scenarios are discriminative: the correct verdict has to beat
a plausible competitor. `config-regression` must beat plain memory,
`dns-failure` must beat crashloop, `node-pressure` must beat per-pod memory,
`pvc-unschedulable` must beat generic scheduling, `liveness-probe-failure` must
beat crashloop, `gitops-drift` must beat crashloop.

## Contributing a scenario

```sh
kubetective investigate deployment/checkout --since=30m   # 1. record it
kubetective scenario new <incident-id> --name my-incident # 2. draft it
$EDITOR scenarios/my-incident/scenario.yaml               # 3. correct it
kubetective benchmark                                     # 4. prove it
```

Step 2 does the mechanical work. It sanitises the record (pseudonymised
identifiers, scrubbed free text — verdict-preserving, with a gate asserting
it), replays it to see what the engine concludes, then removes each
observation kind in turn to find which evidence the verdict rests on. Kinds
that change the verdict become proposed mutations.

Step 3 is the part only you can do. **The draft carries the engine's own
answer, which is not ground truth.** Committing it unedited produces a
scenario that can only ever confirm what the engine already thinks. Decide
what actually happened; if the engine got it wrong, keep the true answer and
mark it `advisory: true`.

A scenario the engine fails is worth more than one it passes — see the hard
set above for why.

If the incident is one where the honest answer is "it could be either", mark it
`advisory: true` and say why in the description. Those are the most valuable
contributions the suite can receive.
