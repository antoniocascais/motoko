# Recipes use `set -o pipefail` (bash-only); CI runners default /bin/sh to dash.
SHELL       := /bin/bash

APP_NAME    := motoko
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST_DIR    := dist

GO_IMAGE    := golang:1.23-alpine
LINT_IMAGE  := golangci/golangci-lint:v2.1.6
# Matches the golden image's apt (2.6, gpgv, no gnupg), so apt behaves as it
# does during the bake.
APT_IMAGE   := debian:12-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
# debian:12-slim ships no CA certificates; the host's bundle is mounted read-only.
CA_BUNDLE   ?= /etc/ssl/certs/ca-certificates.crt
CACHE_DIR   := .cache

COVERAGE_MIN := 80

DOCKER_RUN  := docker run --rm \
	--user $(shell id -u):$(shell id -g) \
	--read-only \
	--tmpfs /tmp:rw,exec \
	-v $(CURDIR):/src \
	-v $(CURDIR)/$(CACHE_DIR)/go-build:/tmp/go-build \
	-v $(CURDIR)/$(CACHE_DIR)/go-mod:/tmp/go-mod \
	-e HOME=/tmp \
	-e GOPATH=/tmp \
	-e GOCACHE=/tmp/go-build \
	-e GOMODCACHE=/tmp/go-mod \
	-w /src

LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test test-apt lint tidy clean help

## build: Compile the binary to dist/
build:
	@mkdir -p $(DIST_DIR) $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) $(GO_IMAGE) go build -buildvcs=false $(LDFLAGS) -o $(DIST_DIR)/$(APP_NAME) .

## test: Run all tests; fail if coverage below $(COVERAGE_MIN)%
test:
	@mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) $(GO_IMAGE) go test ./... -v -count=1 -coverprofile=$(CACHE_DIR)/coverage.out
	@set -e -o pipefail; \
		cov=$$($(DOCKER_RUN) $(GO_IMAGE) go tool cover -func=$(CACHE_DIR)/coverage.out | awk '/^total:/ {print $$3}' | tr -d '%'); \
		if [ -z "$$cov" ]; then echo "ERROR: could not parse coverage from $(CACHE_DIR)/coverage.out" >&2; exit 2; fi; \
		awk -v c="$$cov" -v m="$(COVERAGE_MIN)" 'BEGIN { if (c+0 < m+0) { printf "FAIL: coverage %s%% < %s%% required\n", c, m; exit 1 } printf "OK: coverage %s%% >= %s%%\n", c, m }'

## test-apt: Run real apt against Anthropic's repo with the golden image's key, source and pin (needs network)
test-apt:
	@mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) -e CGO_ENABLED=0 $(GO_IMAGE) go test -c -tags apt -o $(CACHE_DIR)/apt.test ./pkg/vm
	docker run --rm \
		--user $(shell id -u):$(shell id -g) \
		--read-only \
		--tmpfs /tmp:rw,exec \
		-v $(CURDIR)/$(CACHE_DIR)/apt.test:/usr/local/bin/apt.test:ro \
		-v $(CA_BUNDLE):/etc/ssl/certs/ca-certificates.crt:ro \
		-e HOME=/tmp \
		-w /tmp \
		$(APT_IMAGE) apt.test -test.v -test.run '^TestApt_'

## lint: Run golangci-lint
lint:
	@mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) $(LINT_IMAGE) golangci-lint run --build-tags apt ./...

## tidy: Run go mod tidy
tidy:
	@mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) $(GO_IMAGE) go mod tidy

## clean: Remove build artifacts and cache
clean:
	rm -rf $(DIST_DIR) $(CACHE_DIR)

## help: Show available targets
help:
	@echo "Usage: make [target]"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':' | sed 's/^/  /'
