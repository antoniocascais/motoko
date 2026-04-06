APP_NAME    := motoko
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST_DIR    := dist

GO_IMAGE    := golang:1.23-alpine
LINT_IMAGE  := golangci/golangci-lint:v2.1.6
CACHE_DIR   := .cache

DOCKER_RUN  := docker run --rm \
	--user $(shell id -u):$(shell id -g) \
	--read-only \
	--tmpfs /tmp:rw,exec \
	-v $(CURDIR):/src \
	-v $(CURDIR)/$(CACHE_DIR)/go-build:/tmp/go-build \
	-v $(CURDIR)/$(CACHE_DIR)/go-mod:/tmp/go-mod \
	-e HOME=/tmp \
	-e GOCACHE=/tmp/go-build \
	-e GOMODCACHE=/tmp/go-mod \
	-w /src

LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test lint tidy clean help

## build: Compile the binary to dist/
build:
	@mkdir -p $(DIST_DIR) $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) $(GO_IMAGE) go build -buildvcs=false $(LDFLAGS) -o $(DIST_DIR)/$(APP_NAME) .

## test: Run all tests
test:
	@mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod
	$(DOCKER_RUN) $(GO_IMAGE) go test ./... -v -count=1

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
