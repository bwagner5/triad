VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_DIR ?= ./build

GOLANGCI_LINT_VERSION ?= latest
GORELEASER_VERSION ?= latest

.PHONY: tools build test lint ci attribution snapshot clean docker help

tools: ## Install required dev tools
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

ci: lint build test ## Run lint, build, and test (used by CI and local dev)

build: ## Build the binary
	@mkdir -p $(BUILD_DIR)
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BUILD_DIR)/triad ./cmd/triad

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

docker: ## Build Docker image
	docker build --build-arg GO_VERSION=$$(grep '^go ' go.mod | awk '{print $$2}') -t triad .

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
