# Contributing to KubeTective

Thanks for contributing! This document covers the development workflow, the
contribution rule for analyzers, and how to cut a release.

## Development

Requirements: Go ≥ 1.26.

```sh
make build    # bin/kubetective + bin/kubectl-investigate
make test     # all unit tests
make vet
make fmt
```

Tests run without a cluster: collectors use fake clientsets, and the scenario
suite replays recorded incidents. A live kind cluster is only needed for
manual end-to-end checks (`kubectl investigate <resource>`).

## The analyzer contribution rule

Every new analyzer ships with a **scenario** that proves it:

1. Implement `analyze.Analyzer` - see `internal/analyze/` for worked examples
   (crashloop is the smallest; configregression shows multi-evidence scoring).
2. Register it in `internal/cli/root.go` (`ar.Register(...)`).
3. Create `scenarios/<name>/` with:
   - `record.jsonl` - the recorded observations the analyzer activates on
     (the incident record format; generate it with a real investigation and
     trim, or handcraft from an existing scenario),
   - `scenario.yaml` - ground truth: `root_cause`, `top_hypothesis_category`,
     `min_score`, `expected_finding_analyzers`, `expected_status`
     (or `expect_no_findings: true` for a negative control).
4. `kubetective benchmark` must pass 15/15 - this is the regression gate.

Weight discipline: evidence weights follow the shared scale in
`internal/score` (mechanism ≈20–30, temporal ≈25–30, corroboration ≈10–15).
Scores are calibrated against the suite; if your scenario changes calibration,
`benchmark` reports the new leave-one-out validation - keep it green.

## Code style

- `gofmt` clean, `go vet` clean.
- Every exported symbol documented.
- Tests for everything; keep the "deterministic first" rule: no LLM in the
  engine path, no cluster mutation in the investigation path.

## Releasing

1. Bump the version in `internal/engine/engine.go` (e.g. `v0.7.0`).
2. Commit, tag, push:

   ```sh
   git tag v0.7.0
   git push origin main --tags
   ```

3. (Optional) create a GitHub Release for the tag with the changelog entry.
4. Update the brew formula and push it:

   ```sh
   hack/update-formula.sh v0.7.0
   git add Formula/kubetective.rb && git commit -m "Formula: v0.7.0"
   git push origin main
   ```

5. Sync the formula to the Homebrew tap repo and push it:

   ```sh
   git clone https://github.com/GlediLami/homebrew-kubetective.git /tmp/homebrew-kubetective
   cp Formula/kubetective.rb /tmp/homebrew-kubetective/Formula/
   cd /tmp/homebrew-kubetective
   git add Formula/kubetective.rb && git commit -m "Formula: v0.7.0"
   git push
   ```

6. Users install with the one-liner (Homebrew auto-taps):

   ```sh
   brew install gledilami/kubetective/kubetective
   ```

## Security

Report vulnerabilities privately - see [`SECURITY.md`](SECURITY.md).
