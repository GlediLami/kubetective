GO      ?= go
BIN_DIR ?= bin
PREFIX  ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: build test vet fmt tidy license-report report install install-plugin scenarios clean check-version bump-version install-hooks release tarball

build:
	$(GO) build -o $(BIN_DIR)/kubetective ./cmd/kubetective
	$(GO) build -o $(BIN_DIR)/kubectl-investigate ./cmd/kubectl-investigate

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w cmd internal pkg

tidy:
	$(GO) mod tidy

# dependency license report (v1.0 hygiene): fails if any dependency is not
# redistributable. All current deps: Apache-2.0 / BSD / MIT / MPL-2.0 / ISC.
license-report:
	mkdir -p dist
	$(GO) run github.com/google/go-licenses@latest report ./... > dist/licenses.csv
	@echo "dist/licenses.csv written ($(shell wc -l < dist/licenses.csv) rows)"

# evaluation report artifact (v1.0): the published, per-release report.
# Committed at release time; CI asserts the gate via `evaluate` itself.
report:
	mkdir -p reports/evaluation
	./bin/kubetective evaluate --out reports/evaluation/latest.md
	@echo "reports/evaluation/latest.md written"

install: build
	install -m 0755 $(BIN_DIR)/kubetective $(PREFIX)/bin/kubetective

# kubectl plugin discovery: kubectl investigate <...> finds kubectl-investigate on PATH
install-plugin: build
	install -m 0755 $(BIN_DIR)/kubectl-investigate $(PREFIX)/bin/kubectl-investigate
	@echo "installed kubectl-investigate -> $(PREFIX)/bin (run: kubectl investigate --help)"

scenarios: build
	$(BIN_DIR)/kubetective benchmark

clean:
	rm -rf $(BIN_DIR)

# --- version maintenance (v0.9) -------------------------------------------------
# The canonical version lives in internal/engine/engine.go; these targets
# keep every other mention (Dockerfile, brew formula, tap repo) in lockstep. check-version also runs in CI and as a pre-commit
# hook (install-hooks).

check-version:
	./hack/check-version.sh

bump-version:
	hack/bump-version.sh $(VERSION)

install-hooks:
	git config core.hooksPath .githooks
	@echo "pre-commit hook installed (git config core.hooksPath = .githooks)"

# One-command release: bump -> verify -> build/test -> tag -> push main+tag
# -> brew formula -> tap repo sync. Use hack/release.sh vX.Y.Z --dry-run
# to preview. VERSION must be set, e.g. `make release VERSION=v0.9.0`.
release: check-version
	hack/release.sh $(VERSION)

# source tarball for GitHub releases (the brew formula fetches the tag
# tarball; the formula sha is kept in sync by hack/update-formula.sh)
tarball:
	mkdir -p dist
	git archive --format=tar.gz --prefix=kubetective-$(VERSION)/ -o dist/kubetective-$(VERSION).tar.gz HEAD
	shasum -a 256 dist/kubetective-$(VERSION).tar.gz | tee dist/kubetective-$(VERSION).tar.gz.sha256
	@echo "upload dist/kubetective-$(VERSION).tar.gz to the GitHub release"
