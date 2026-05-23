APP_NAME    := motoko
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST_DIR    := dist

GO_IMAGE    := golang:1.23-alpine
LINT_IMAGE  := golangci/golangci-lint:v2.1.6
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

.PHONY: build test lint tidy clean help

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

## lint: Run golangci-lint
lint:
	@mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) $(LINT_IMAGE) golangci-lint run ./...

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
