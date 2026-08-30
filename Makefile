SHELL := /bin/sh

GO ?= go
GIT_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION ?= dev
LDFLAGS := -s -w \
	-X github.com/mss-boot-io/mss-knowledge/internal/buildinfo.Version=$(VERSION) \
	-X github.com/mss-boot-io/mss-knowledge/internal/buildinfo.Commit=$(GIT_COMMIT) \
	-X github.com/mss-boot-io/mss-knowledge/internal/buildinfo.Date=$(BUILD_DATE)

GO_FILES := $(shell find . -type f -name '*.go' -not -path './vendor/*')

.PHONY: help fmt fmt-check vet test test-race build clean check

help:
	@printf '%s\n' \
		'make fmt        Format Go source files' \
		'make fmt-check  Fail when Go source is not formatted' \
		'make vet        Run go vet' \
		'make test       Run unit tests' \
		'make test-race  Run tests with the race detector' \
		'make build      Build gateway, worker, and ctl binaries' \
		'make check      Run format check, vet, tests, race tests, and build'

fmt:
	$(GO)fmt -w $(GO_FILES)

fmt-check:
	@test -z "$$($(GO)fmt -l $(GO_FILES))" || \
		{ echo 'Go files require formatting:'; $(GO)fmt -l $(GO_FILES); exit 1; }

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

build:
	@mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mss-knowledge-gateway ./cmd/gateway
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mss-knowledge-worker ./cmd/worker
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mss-knowledge-ctl ./cmd/ctl

check: fmt-check vet test test-race build

clean:
	rm -rf bin coverage*.txt
