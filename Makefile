$(shell git fetch --tags)
BUILD_DIR ?= $(dir $(realpath -s $(firstword $(MAKEFILE_LIST))))/build
VERSION ?= $(shell git describe --tags --always --dirty)
PREV_VERSION ?= $(shell git describe --abbrev=0 --tags `git rev-list --tags --skip=1 --max-count=1`)
GOOS ?= $(shell uname | tr '[:upper:]' '[:lower:]')
GOARCH ?= $(shell [[ `uname -m` = "x86_64" ]] && echo "amd64" || echo "arm64" )
GOPROXY ?= "https://proxy.golang.org|direct"

$(shell mkdir -p ${BUILD_DIR})

.PHONY: all
all: fmt verify test build

.PHONY: goreleaser
goreleaser: ## Release snapshot
	goreleaser build --snapshot --rm-dist

.PHONY: build
build: ## build binary using current OS and Arch
	go build -a -ldflags="-s -w -X main.version=${VERSION}" -o ${BUILD_DIR}/go-cli-template-${GOOS}-${GOARCH} ./cmd/go-cli-template

.PHONY: test
test: ## run go tests and benchmarks
	go test -bench=. ${BUILD_DIR}/../... -v -coverprofile=coverage.out -covermode=atomic -outputdir=${BUILD_DIR}

.PHONY: version
version: ## Output version of local HEAD
	@echo ${VERSION}

.PHONY: verify
verify: licenses boilerplate ## Run Verifications like helm-lint and govulncheck
	govulncheck ./...
	golangci-lint run
	cd toolchain && go mod tidy

.PHONY: boilerplate
boilerplate: ## Add license headers
	go run hack/boilerplate.go ./

.PHONY: fmt
fmt: ## go fmt the code
	find . -iname "*.go" -exec go fmt {} \;

.PHONY: licenses
licenses: ## Verifies dependency licenses
	go mod download
	! go-licenses csv ./... | grep -v -e 'MIT' -e 'Apache-2.0' -e 'BSD-3-Clause' -e 'BSD-2-Clause' -e 'ISC' -e 'MPL-2.0'

.PHONY: update-readme
update-readme: ## Updates readme to refer to latest release
	sed -E -i.bak "s|$(shell echo ${PREV_VERSION} | tr -d 'v' | sed 's/\./\\./g')([\"_/])|$(shell echo ${VERSION} | tr -d 'v')\1|g" README.md
	rm -f *.bak

.PHONY: toolchain
toolchain:
	cd toolchain && go mod download && cat tools.go | grep _ | awk -F'"' '{print $$2}' | xargs -tI % go install %

.PHONY: generate
generate: ## Generate attribution
	# run generate twice, gen_licenses needs the ATTRIBUTION file or it fails.  The second run
	# ensures that the latest copy is embedded when we build.
	go generate ./...
	./hack/gen_licenses.sh
	go generate ./...

.PHONY: clean
clean: ## Clean artifacts
	rm -rf ${BUILD_DIR}
	rm -rf dist/

.PHONY: help
help: ## Display help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
