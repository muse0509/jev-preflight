SHELL := /bin/bash
GO ?= go
ROOT_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
GOFILES = $(shell find cmd internal policies scripts -name '*.go' -type f 2>/dev/null)
VERSION ?= v0.1.0
TARGET = $(shell $(GO) env GOOS)-$(shell $(GO) env GOARCH)
EXE = $(shell $(GO) env GOEXE)

.PHONY: fmt fmt-check vet test test-race check build cross-build dev-runtime plugin-validate package clean
fmt:
	gofmt -w $(GOFILES)
fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))" || { gofmt -l $(GOFILES); exit 1; }
vet:
	$(GO) vet ./...
test:
	$(GO) test ./...
test-race:
	$(GO) test -race ./...
check: fmt-check vet test
build:
	@mkdir -p dist/$(TARGET)
	CGO_ENABLED=0 $(GO) build -trimpath -o dist/$(TARGET)/jev-preflight$(EXE) ./cmd/jev-preflight
cross-build:
	@set -e; for os in darwin linux windows; do for arch in amd64 arm64; do \
	  suffix=""; if [ "$$os" = windows ]; then suffix=.exe; fi; \
	  mkdir -p "dist/$$os-$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -o "dist/$$os-$$arch/jev-preflight$$suffix" ./cmd/jev-preflight; \
	done; done
dev-runtime:
	@mkdir -p .tmp/runtime/$(TARGET)
	CGO_ENABLED=0 $(GO) build -trimpath -o .tmp/runtime/$(TARGET)/jev-preflight$(EXE) ./cmd/jev-preflight
plugin-validate:
	@if command -v claude >/dev/null 2>&1; then claude plugin validate . --strict; else echo 'SKIP: Claude Code CLI unavailable'; fi
package: cross-build
	$(GO) run ./scripts/package -version $(VERSION)
clean:
	@cd "$(ROOT_DIR)" && for dir in dist coverage .tmp; do if [ -L "$$dir" ]; then echo 'Refusing to clean a symlink' >&2; exit 1; fi; done && rm -rf -- ./dist ./coverage ./.tmp
