VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_DIR ?= ./build

.PHONY: build test lint attribution snapshot clean help

build: ## Build the binary
	@mkdir -p $(BUILD_DIR)
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BUILD_DIR)/go-cli-template ./cmd/go-cli-template

test: ## Run tests
	go test ./... -v -count=1

lint: ## Run golangci-lint
	golangci-lint run

attribution: ## Generate ATTRIBUTION.md from dependency licenses
	./hack/gen_licenses.sh
	cp ATTRIBUTION.md pkg/attribution/ATTRIBUTION.md

snapshot: ## Create a snapshot release (no publish)
	goreleaser release --snapshot --clean

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR) dist/

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
