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

## Current suite (20)

**From a live cluster (1).** `live-oom-config-regression` — a real OOMKill on a
real kind cluster, traced to the real commit that caused it. Recorded from an
actual API server (29 observations, 14 events, a captured log snippet, two git
commits) and sanitised for publication. Everything else below was written by
hand.

**Gated (17).** Including the live one, plus `oom-after-deploy`, `oom-memory-growth`, `crashloop`,
`imagepull`, `pending-unschedulable`, `bad-readiness-probe`,
`liveness-probe-failure`, `node-pressure`, `pvc-unschedulable`,
`service-selector-mismatch`, `hpa-at-max`, `config-regression`,
`gitops-drift`, `dns-failure`, `dns-events-only` (secondary-signal lock),
`healthy` (negative control — the engine must stay silent).

**Hard set (3, advisory).** `ambiguous-oom-or-node`, `ambiguous-stale-commit`,
`ambiguous-probe-or-crash`.

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
