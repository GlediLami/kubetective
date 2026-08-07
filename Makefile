GO      ?= go
BIN_DIR ?= bin
PREFIX  ?= $(HOME)/.local

.PHONY: build test vet fmt tidy install install-plugin scenarios clean

build:
	$(GO) build -o $(BIN_DIR)/kubedoctor ./cmd/kubedoctor
	$(GO) build -o $(BIN_DIR)/kubectl-investigate ./cmd/kubectl-investigate

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w cmd internal pkg

tidy:
	$(GO) mod tidy

install: build
	install -m 0755 $(BIN_DIR)/kubedoctor $(PREFIX)/bin/kubedoctor

# kubectl plugin discovery: kubectl investigate <...> finds kubectl-investigate on PATH
install-plugin: build
	install -m 0755 $(BIN_DIR)/kubectl-investigate $(PREFIX)/bin/kubectl-investigate
	@echo "installed kubectl-investigate -> $(PREFIX)/bin (run: kubectl investigate --help)"

scenarios: build
	$(BIN_DIR)/kubedoctor benchmark

clean:
	rm -rf $(BIN_DIR)
