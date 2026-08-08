# Security Policy

## Reporting a vulnerability

Please report security issues **privately** — do not open a public issue.

Email the maintainers at the address listed in the repository description /
maintainer profile, or use GitHub's private vulnerability reporting
("Report a vulnerability" button on the repository's Security tab).

Please include:

- the affected version,
- a description of the issue and its impact,
- reproduction steps (a scenario record or kubeconfig-less repro is ideal).

You will receive an acknowledgement within 48 hours and a fix plan as soon
as we can assess the issue.

## Security model

KubeDoctor is designed to be safe to run in production clusters. The invariants:

- **Read-only investigations.** The pipeline never mutates the cluster.
  Remediation actions (rollback, restart) are preview-only by default and
  require an explicit human approval flag (`--apply <id> --yes`); every apply
  is written to an audit record.
- **The collector boundary.** Raw data (log lines, annotations, secrets)
  never crosses into the analysis layer; analyzers only see normalized
  observations.
- **The LLM boundary.** The optional LLM receives only a redacted digest —
  no logs, no payload values, no kubeconfig, no secrets. Its output is
  validated strict JSON and can never change scores, assert causation, or
  propose actions. This boundary is regression-tested.
- **The replay boundary.** Incident records are treated as untrusted input:
  replaying a malicious record cannot produce cluster mutations.
- **Calibration honesty.** Confidence is calibrated against the scenario
  suite and validated leave-one-out; when calibration is bad, displayed
  confidence is dampened toward 50% rather than inflated.

## Supported versions

The latest release on the `main` branch. Security fixes land in the newest
tagged release.
