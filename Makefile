# Copyright (c) Microsoft Corporation.
# Licensed under the MIT license.

REGISTRY ?= YOUR_REGISTRY
VERSION ?= v0.0.1
IMG_TAG ?= $(subst v,,$(VERSION))
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
	$(CONTROLLER_GEN) crd paths="./..." output:crd:artifacts:config=charts/grit-manager/crds
	$(CONTROLLER_GEN) rbac:roleName=grit-manager-clusterrole paths="./pkg/gritmanager/..." output:rbac:artifacts:config=charts/grit-manager/templates
	$(CONTROLLER_GEN) webhook paths="./pkg/gritmanager/..." output:webhook:artifacts:config=charts/grit-manager/templates
	mv charts/grit-manager/templates/role.yaml charts/grit-manager/templates/clusterrole-auto-generated.yaml
	mv charts/grit-manager/templates/manifests.yaml charts/grit-manager/templates/webhooks-auto-generated.yaml

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

.PHONY: bin/grit-manager
bin/grit-manager:
	@mkdir -p $(OUTPUT_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(OUTPUT_DIR)/grit-manager ./cmd/grit-manager/grit-manager.go

.PHONY: bin/grit-agent
bin/grit-agent:
	@mkdir -p $(OUTPUT_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(OUTPUT_DIR)/grit-agent ./cmd/grit-agent/grit-agent.go

.PHONY: bin/containerd-shim-grit-v1
bin/containerd-shim-grit-v1: cmd/containerd-shim-grit-v1
	@mkdir -p $(OUTPUT_DIR)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(OUTPUT_DIR)/containerd-shim-grit-v1 -tags "urfave_cli_no_docs no_grpc" ./cmd/containerd-shim-grit-v1

## --------------------------------------
## Image Docker Build
## --------------------------------------
BUILDX_BUILDER_NAME ?= img-builder
OUTPUT_TYPE ?= type=registry
QEMU_VERSION ?= 7.2.0-1
ARCH ?= amd64,arm64
BUILDKIT_VERSION ?= v0.18.1

GRIT_AGENT_IMG_NAME ?= grit-agent

.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	@if ! docker buildx ls | grep $(BUILDX_BUILDER_NAME); then \
		docker run --rm --privileged mcr.microsoft.com/mirror/docker/multiarch/qemu-user-static:$(QEMU_VERSION) --reset -p yes; \
		docker buildx create --name $(BUILDX_BUILDER_NAME) --driver-opt image=mcr.microsoft.com/oss/v2/moby/buildkit:$(BUILDKIT_VERSION) --use; \
		docker buildx inspect $(BUILDX_BUILDER_NAME) --bootstrap; \
	fi

.PHONY: docker-build-grit-agent
docker-build-grit-agent: docker-buildx
	docker buildx build \
		--file ./docker/grit-agent/Dockerfile \
		--output=$(OUTPUT_TYPE) \
		--platform="linux/$(ARCH)" \
		--pull \
		--tag $(REGISTRY)/$(GRIT_AGENT_IMG_NAME):$(IMG_TAG) .

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(OUTPUT_DIR)

.PHONY: version
version:
	@echo "Version      : $(VERSION)"
	@echo "Git Commit   : $(GIT_COMMIT)"
	@echo "Build Date   : $(BUILD_DATE)"
