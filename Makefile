PLUGIN_NAME ?= ghcr.io/thehaven/docker-net-dhcp
PLUGIN_TAG ?= golang
PLATFORMS ?= linux/amd64,linux/arm64/v8

SOURCES = $(shell find pkg/ cmd/ -name '*.go')
BINARY = bin/net-dhcp

.PHONY: help all debug build install create enable disable pdebug push clean test verify

.DEFAULT_GOAL := help

# Auto-generated help
help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

all: install enable ## Build, Install, and Enable the plugin (COMPLETE SWAP)

test: ## Run all unit tests with race detection
	go test -v -race ./cmd/... ./pkg/...

verify: ## Run static analysis (go vet)
	go vet ./...

bin/%: $(SOURCES) ## Build a specific binary locally
	go build -o $@ ./cmd/$(shell basename $@)

debug: $(BINARY) ## Run the plugin locally with sudo for debugging
	sudo $< -log debug

build: plugin ## Build the plugin RootFS image and prepare artifacts

plugin/rootfs: $(SOURCES) ## Build the plugin RootFS
	docker build -t $(PLUGIN_NAME):rootfs .
	mkdir -p plugin/rootfs
	docker create --name tmp $(PLUGIN_NAME):rootfs
	sudo sh -c "docker export tmp | tar xC plugin/rootfs"
	docker rm -vf tmp

plugin: plugin/rootfs config.json ## Prepare plugin directory with config (sudo required for rootfs access)
	sudo cp config.json $@/

install: create ## Register (create) the plugin with the local Docker daemon

# Perform a zero-interruption upgrade (keeps Plugin ID and existing networks)
# Requires the plugin to be pushed to a registry first
upgrade: ## Upgrade the plugin in-place
	docker plugin upgrade $(PLUGIN_NAME):$(PLUGIN_TAG) $(PLUGIN_NAME):$(PLUGIN_TAG)

create: plugin ## Register the plugin with Docker (COMPLETE SWAP)
	sudo mkdir -p /var/lib/docker-net-dhcp
	sudo chmod 0700 /var/lib/docker-net-dhcp
	docker plugin rm -f $(PLUGIN_NAME):$(PLUGIN_TAG) || true
	docker plugin rm -f ghcr.io/devplayer0/docker-net-dhcp:golang || true
	docker plugin create $(PLUGIN_NAME):$(PLUGIN_TAG) $<
	docker plugin set $(PLUGIN_NAME):$(PLUGIN_TAG) LOG_LEVEL=trace

enable: ## Activate the plugin
	docker plugin enable $(PLUGIN_NAME):$(PLUGIN_TAG)

disable: ## Deactivate the plugin
	docker plugin disable $(PLUGIN_NAME):$(PLUGIN_TAG)

pdebug: ## Tail the plugin logs from the Docker rootfs
	sudo sh -c 'tail -f /var/lib/docker/plugins/*/rootfs/var/log/net-dhcp.log'

push: install ## Push the plugin to the registry
	docker plugin push $(PLUGIN_NAME):$(PLUGIN_TAG)

multiarch: $(SOURCES) ## Build multi-arch RootFS (amd64, arm64)
	docker buildx build --platform=$(PLATFORMS) -o type=local,dest=$@ .

push-multiarch: multiarch config.json ## Push multi-arch plugin to registry
	scripts/push_multiarch_plugin.py -p $(PLATFORMS) config.json multiarch $(PLUGIN_NAME):$(PLUGIN_TAG)

clean: ## Remove build artifacts and root-owned plugin directory
	-sudo rm -rf multiarch/
	-sudo rm -rf plugin/
	-rm -rf bin/*
