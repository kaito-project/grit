# Copyright (c) Microsoft Corporation.
# Licensed under the MIT license.

VERSION ?= v0.0.1
GRIT_ROOT ?= $(shell pwd)
OUTPUT_DIR := $(GRIT_ROOT)/_output
LOCALBIN ?= $(GRIT_ROOT)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

CONTROLLER_TOOLS_VERSION ?= v0.17.2
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

GOLANGCI_LINT_VERSION ?= v1.61.0
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

# injection variables
INJECTION_ROOT := github.com/kaito-project/grit/pkg/injections
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT := $(shell git rev-parse --short HEAD)
LDFLAGS := -X '$(INJECTION_ROOT).Version=$(VERSION)' \
		   -X '$(INJECTION_ROOT).BuildDate=$(BUILD_DATE)' \
		   -X '$(INJECTION_ROOT).GitCommit=$(GIT_COMMIT)'


.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(CONTROLLER_GEN) && $(CONTROLLER_GEN) --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary. If wrong version is installed, it will be overwritten.
$(GOLANGCI_LINT): $(LOCALBIN)
	test -s $(GOLANGCI_LINT) && $(GOLANGCI_LINT) --version | grep -q $(GOLANGCI_LINT_VERSION) || \
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=_output/crds
	cp _output/crds/kaito.sh_checkpoints.yaml charts/grit-manager/crds/
	cp _output/crds/kaito.sh_restores.yaml charts/grit-manager/crds/
	rm -rf _output/crds

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: verify-mod
verify-mod:
	@echo "verifying go.mod and go.sum"
	go mod tidy
	@if [ -n "$$(git status --porcelain go.mod go.sum)" ]; then \
		echo "Error: go.mod/go.sum is not up-to-date. please run `go mod tidy` and commit the changes."; \
		git diff go.mod go.sum; \
		exit 1; \
	fi

.PHONY: verify-manifests
verify-manifests: manifests
	@echo "verifying manifests"
	@if [ -n "$$(git status --porcelain ./charts/grit-manager/crds)" ]; then \
		echo "Error: manifests are not up-to-date. please run 'make manifests' and commit the changes."; \
		git diff ./charts/grit-manager/crds; \
		exit 1; \
	fi

.PHONY: lint
lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run -v

.PHONY: build-grit-manager
build-grit-manager:
	@mkdir -p $(OUTPUT_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(OUTPUT_DIR)/grit-manager ./cmd/grit-manager/grit-manager.go

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(OUTPUT_DIR)

.PHONY: version
version:
	@echo "Version      : $(VERSION)"
	@echo "Git Commit   : $(GIT_COMMIT)"
	@echo "Build Date   : $(BUILD_DATE)"