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

The release flow is script-driven: the canonical version lives in
`internal/engine/engine.go`, and `hack/check-version.sh` keeps every other
mention — the Dockerfile stamp, the krew manifest, the brew formula — in
lockstep. The check runs in CI and, once you run `make install-hooks`, as a
pre-commit hook (no stale version can be committed).

1. Install the pre-commit hook (once):

   ```sh
   make install-hooks
   ```

2. Cut the release — this bumps the version everywhere, verifies
   (build + vet + tests), commits, tags, pushes main + the tag, refreshes
   the brew formula sha, and syncs the Homebrew tap repo:

   ```sh
   make release VERSION=v0.9.0
   ```

   Preview every step without changing anything:

   ```sh
   hack/release.sh v0.9.0 --dry-run
   ```

3. Create the GitHub Release for the tag with the changelog entry
   (`gh release create v1.0.0 --notes ...`), attach the release assets
   (goreleaser artifacts: kubectl-investigate tarballs per platform + the
   container image), then finish the krew submission in one command:

   ```sh
   hack/submit-krew.sh            # fills sha256 from the release assets,
   #                              # commits the manifest, opens the krew-index PR
   hack/submit-krew.sh --dry-run  # preview without touching anything
   ```

   The script refuses to run until the release assets exist, so the krew
   manifest can never ship placeholder hashes.

4. Between releases, bump to a dev version (engine + Dockerfile + krew
   manifest stay in lockstep):

   ```sh
   make bump-version VERSION=v0.9.0-dev
   ```

5. Users install with the one-liner (Homebrew auto-taps):

   ```sh
   brew install gledilami/kubetective/kubetective
   ```

## Security

Report vulnerabilities privately - see [`SECURITY.md`](SECURITY.md).
