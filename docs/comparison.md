# How KubeTective compares

## vs. the status quo

| | `kubectl` + grep + docs | LLM chat | KubeTective |
|---|---|---|---|
| Collects and correlates the facts | ✗ you dig | ✗ you paste | ✓ automatic (k8s, Prometheus, Loki, Git/GitOps) |
| Ranked hypotheses with confidence | ✗ | ~ vibes | ✓ evidence-scored, calibration gated (see below) |
| Every verdict shows its evidence | ✗ | rarely | ✓ line-by-line score breakdown |
| Verifiable with a recorded replay | ✗ | ✗ | ✓ JSONL records, re-run deterministically |
| Change detection (what changed right before) | ✗ | ✗ | ✓ git commits, GitOps drift, pod changes |
| Regression-tested on a scenario suite | ✗ | ✗ | ✓ 4 gates in CI, incl. mutation + noise |
| Safe remediation with approval | ✗ | ✗ | ✓ read-only preview → explicit `--yes` |
| Shareable without leaking the cluster | ✗ | ✗ | ✓ `kubetective sanitize`, verdict-preserving |
| Works offline, no API key, no telemetry | ✓ | ✗ | ✓ |

## An honest note on AI triage tools (k8sgpt etc.)

There is a popular class of "AI kubectl" tools that scan the cluster and
explain failures with an LLM. They are genuinely useful for triage — but
the core difference is what kind of artifact they produce:

| | AI triage (e.g. k8sgpt) | KubeTective |
|---|---|---|
| Question it answers | "what might be wrong here?" | "what is wrong — how sure, and which commit did it?" |
| Verdict source | LLM interpretation | evidence-scored rule engine |
| Same incident twice | different prose | identical verdict (CI-reproducible) |
| Confidence you can audit | ✗ | ✓ every point traceable to an evidence term |
| Shows the reasoning | prose summary | every score as a readable evidence term |
| Points at a git change | ✗ scans current state | ✓ git/GitOps commit correlation |
| Needs an AI backend | `--explain` does; scan output stays unranked prose | **no** — verdicts require no AI; the explainer is optional |
| What leaves your machine | anonymized payloads; event messages currently unmasked | only a redacted digest — never raw logs, events, or secrets |
| Regression-tested against real incidents | ✗ | ✓ `kubetective benchmark` is the merge gate |
| Can undo / plan the fix with a trail | ✗ (text suggestions) | ✓ read-only preview → approved apply → audit record |

Where they shine and we don't (yet): cluster-wide scanning, a bigger analyzer
catalog, LLM backend breadth, and an operator for continuous monitoring —
all on our roadmap, all reproducible from our side.
