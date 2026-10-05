#!make

## Tool versions (single source of truth, shared with CI)
include versions.env

SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

# LOCALBIN must be absolute: setup-envtest returns paths relative to --bin-dir and
# a relative KUBEBUILDER_ASSETS breaks envtest when tests run from package dirs (notably on macOS).
LOCALBIN ?= $(CURDIR)/bin
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
GO_LICENSES ?= $(LOCALBIN)/go-licenses

COVERPROFILE ?= cover.out
MODULES := $(sort $(patsubst %/go.mod,%,$(shell find . -name go.mod -not -path './bin/*' -not -path './vendor/*')))

##@ General

.PHONY: all
all: tidy-check fmt vet lint test test-examples reuse-lint vuln licenses fuzz ## Run every check CI runs.

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	@set -e; for module in $(MODULES); do (cd $$module && go fmt ./...); done

.PHONY: vet
vet: ## Run go vet against code.
	@set -e; for module in $(MODULES); do (cd $$module && go vet ./...); done

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	@set -e; for module in $(MODULES); do (cd $$module && $(GOLANGCI_LINT) run --config $(CURDIR)/.golangci.yml --timeout 10m); done

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply fixes.
	@set -e; for module in $(MODULES); do (cd $$module && $(GOLANGCI_LINT) run --config $(CURDIR)/.golangci.yml --timeout 10m --fix); done

.PHONY: tidy
tidy: ## Run go mod tidy.
	@set -e; for module in $(MODULES); do (cd $$module && go mod tidy); done

.PHONY: tidy-check
tidy-check: ## Fail if go.mod/go.sum are not tidy.
	@set -e; for module in $(MODULES); do (cd $$module && go mod tidy -diff); done

##@ Test

.PHONY: test
test: envtest ## Run unit and envtest tests with race detector and coverage.
	@set -e; assets="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)"; export KUBEBUILDER_ASSETS="$$assets"; \
	for module in $(MODULES); do \
		(cd $$module && CGO_ENABLED=1 go test -race -covermode=atomic -coverprofile=$(COVERPROFILE) ./...); \
	done

.PHONY: test-examples
test-examples: envtest ## Build and test the runnable samples under examples/.
	@set -e; assets="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)"; export KUBEBUILDER_ASSETS="$$assets"; \
	for module in $(MODULES); do \
		if [[ "$$module" == ./examples/* ]]; then \
			(cd $$module && go build -o /dev/null ./... && go test ./...); \
		elif [ -d "$$module/examples" ] && [ -n "$$(cd $$module && go list ./examples/... 2>/dev/null)" ]; then \
			(cd $$module && go build -o /dev/null ./examples/... && go test ./examples/...); \
		fi; \
	done

.PHONY: test-e2e-dynamiccache
test-e2e-dynamiccache: ## Run the dynamiccache auth-operator E2E suite on a throwaway kind cluster (requires docker).
	bash e2e/dynamiccache/kind-e2e.sh

.PHONY: coverage
coverage: test ## Print a per-function coverage summary.
	@set -e; for module in $(MODULES); do (cd $$module && go tool cover -func=$(COVERPROFILE)); done

.PHONY: fuzz
fuzz: ## Briefly fuzz IP arithmetic and subdivision.
	go test ./pkg/netutil -run='^$$' -fuzz='^FuzzArithmetic$$' -fuzztime=5s
	go test ./pkg/netutil -run='^$$' -fuzz='^FuzzSubdivide$$' -fuzztime=5s

##@ Compliance & Security

.PHONY: reuse-lint
reuse-lint: ## Check REUSE/SPDX licensing compliance (uses reuse, uvx or pipx).
	@if command -v reuse >/dev/null 2>&1; then reuse lint; \
	elif command -v uvx >/dev/null 2>&1; then uvx reuse lint; \
	elif command -v pipx >/dev/null 2>&1; then pipx run reuse lint; \
	else echo "install reuse (pip install reuse), uv or pipx"; exit 1; fi

.PHONY: vuln
vuln: govulncheck ## Scan for known vulnerabilities with govulncheck.
	@set -e; for module in $(MODULES); do (cd $$module && $(GOVULNCHECK) ./...); done

.PHONY: licenses
licenses: go-licenses ## Check all modules' production and test dependency licenses.
	@set -e; for module in $(MODULES); do \
		(cd $$module && $(GO_LICENSES) check --include_tests ./... \
			--allowed_licenses=Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0); \
	done

##@ Tools

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: golangci-lint
golangci-lint: $(LOCALBIN) ## Install pinned golangci-lint into ./bin.
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION),github.com/golangci/golangci-lint/v2)

.PHONY: envtest
envtest: $(LOCALBIN) ## Install pinned setup-envtest into ./bin.
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION),sigs.k8s.io/controller-runtime/tools/setup-envtest)

.PHONY: govulncheck
govulncheck: $(LOCALBIN) ## Install pinned govulncheck into ./bin.
	$(call go-install-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION),golang.org/x/vuln)

.PHONY: go-licenses
go-licenses: $(LOCALBIN) ## Install pinned go-licenses into ./bin.
	$(call go-install-tool,$(GO_LICENSES),github.com/google/go-licenses/v2,$(GO_LICENSES_VERSION),github.com/google/go-licenses/v2)

.PHONY: clean
clean: ## Remove local tools and coverage output.
	rm -rf $(LOCALBIN)
	@for module in $(MODULES); do rm -f $$module/$(COVERPROFILE); done

# go-install-tool installs a Go binary into LOCALBIN unless the exact package/version is already present.
# $1 - target binary path, $2 - package, $3 - version, $4 - module
define go-install-tool
@{ \
set -e; \
tool="$(1)"; package="$(2)"; version="$(3)"; module="$(4)"; \
if [ -f "$${tool}" ] && \
	go version -m "$${tool}" 2>/dev/null | grep -Eq "^[[:space:]]*path[[:space:]]+$${package}$$" && \
	go version -m "$${tool}" 2>/dev/null | grep -Eq "^[[:space:]]*mod[[:space:]]+$${module}[[:space:]]+$${version}([[:space:]]|$$)"; then \
	exit 0; \
fi; \
echo "Downloading $${package}@$${version}"; \
GOBIN=$(LOCALBIN) go install "$${package}@$${version}"; \
}
endef
