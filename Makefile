BINARY      := goma-k8s-provider
CMD_PATH    := ./cmd
BIN_DIR     := bin
IMAGE       ?= jkaninda/goma-k8s-provider
TAG         ?= latest
PLATFORMS   ?= linux/amd64,linux/arm64,linux/arm/v7

# Versioning
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE  := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(BUILD_DATE)

# Runtime env for `make run` (override with `make run VAR=value`)
GOMA_K8S_GATEWAY    ?= production
GOMA_K8S_NAMESPACE  ?= default
GOMA_K8S_OUTPUT_DIR ?= /tmp/goma-k8s
GOMA_K8S_LOG_LEVEL  ?= debug

.DEFAULT_GOAL := help

##@ General

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: tidy
tidy: ## Tidy go modules
	go mod tidy

.PHONY: test
test: ## Run tests
	go test ./... -race -count=1

.PHONY: lint
lint: ## Run golangci-lint (if installed)
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed, skipping"; exit 0)
	golangci-lint run ./...

##@ Build

.PHONY: build
build: fmt vet ## Build binary for current platform
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD_PATH)
	@echo "Built $(BIN_DIR)/$(BINARY) ($(VERSION))"

.PHONY: build-linux
build-linux: ## Build static Linux amd64 binary
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)-linux-amd64 $(CMD_PATH)

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	rm -rf $(GOMA_K8S_OUTPUT_DIR)

##@ Run

.PHONY: run
run: fmt vet ## Run locally (uses ~/.kube/config)
	@mkdir -p $(GOMA_K8S_OUTPUT_DIR)
	GOMA_K8S_GATEWAY=$(GOMA_K8S_GATEWAY) \
	GOMA_K8S_NAMESPACE=$(GOMA_K8S_NAMESPACE) \
	GOMA_K8S_OUTPUT_DIR=$(GOMA_K8S_OUTPUT_DIR) \
	GOMA_K8S_LOG_LEVEL=$(GOMA_K8S_LOG_LEVEL) \
	go run $(CMD_PATH)

##@ Docker

.PHONY: docker-build
docker-build: ## Build Docker image for current platform (production — no replace directives)
	docker build -t $(IMAGE):$(TAG) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		.

.PHONY: docker-build-dev
docker-build-dev: ## Build Docker image including local goma-operator (for dev with replace directive)
	docker build -f Dockerfile.dev -t $(IMAGE):$(TAG) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		..

.PHONY: docker-push
docker-push: ## Push Docker image
	docker push $(IMAGE):$(TAG)

.PHONY: docker-buildx
docker-buildx: ## Build & push multi-arch image (linux/amd64, arm64, arm/v7)
	docker buildx build --platform $(PLATFORMS) \
		-t $(IMAGE):$(TAG) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		--push \
		.

##@ Version

.PHONY: version
version: ## Print version info
	@echo "Version:    $(VERSION)"
	@echo "Commit:     $(COMMIT)"
	@echo "BuildDate:  $(BUILD_DATE)"
