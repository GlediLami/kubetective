## What does this change?

<!-- One sentence: what the change does and why. -->

## Verification

- [ ] `make build vet test` green (or `go build ./... && go vet ./... && go test ./...`)
- [ ] `kubetective benchmark` passes (analyzer/scoring changes only)
- [ ] `hack/check-version.sh` green (runs in CI; local pre-commit hook via `make install-hooks`)
- [ ] Tests added/updated for the change

## Determinism rules

- No LLM in the engine/score path (AI is narrative-only, labeled `ai_generated`).
- No cluster mutation in the investigation path (actions are human-gated).
- New analyzer? Ships with a `scenarios/<name>/` fixture pair (positive + negative).

## Checklist

- [ ] Every exported symbol is documented
- [ ] `gofmt` + `go vet` clean
- [ ] CHANGELOG entry added under [Unreleased]