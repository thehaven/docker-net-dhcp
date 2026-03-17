PLUGIN_NAME = ghcr.io/devplayer0/docker-net-dhcp
PLUGIN_TAG ?= golang
PLATFORMS ?= linux/amd64,linux/arm64/v8

SOURCES = $(shell find pkg/ cmd/ -name '*.go')
BINARY = bin/net-dhcp

.PHONY: help all debug build install create enable disable pdebug push clean test verify

# Default target: show help
help:
	@echo "docker-net-dhcp Build System"
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@echo "Safety Targets:"
	@echo "  test           Run all unit tests with race detection"
	@echo "  verify         Run static analysis (go vet)"
	@echo "  clean          Remove all build artifacts"
	@echo ""
	@echo "Deployment Targets:"
	@echo "  all            Build, Install, and Enable the plugin (COMPLETE SWAP)"
	@echo "  build          Build the plugin RootFS image and prepare artifacts"
	@echo "  install        Register (create) the plugin with the local Docker daemon"
	@echo "  enable         Activate the plugin"
	@echo "  disable        Deactivate the plugin"
	@echo ""
	@echo "Development Targets:"
	@echo "  bin/net-dhcp   Build the plugin binary locally"
	@echo "  debug          Run the plugin locally with sudo for debugging"
	@echo "  pdebug         Tail the plugin logs from the Docker rootfs"
	@echo ""
	@echo "Distribution Targets:"
	@echo "  multiarch      Build multi-arch RootFS (amd64, arm64)"
	@echo "  push-multiarch Push multi-arch plugin to registry"

all: install enable

test:
	go test -v -race ./...

verify:
	go vet ./...

bin/%: $(SOURCES)
	go build -o $@ ./cmd/$(shell basename $@)

debug: $(BINARY)
	sudo $< -log debug

build: plugin

plugin/rootfs: $(SOURCES)
	docker build -t $(PLUGIN_NAME):rootfs .
	mkdir -p plugin/rootfs
	docker create --name tmp $(PLUGIN_NAME):rootfs
	docker export tmp | tar xC plugin/rootfs
	docker rm -vf tmp

plugin: plugin/rootfs config.json
	cp config.json $@/

install: create

# Perform a zero-interruption upgrade (keeps Plugin ID and existing networks)
# Requires the plugin to be pushed to a registry first
upgrade:
	docker plugin upgrade $(PLUGIN_NAME):$(PLUGIN_TAG) $(PLUGIN_NAME):$(PLUGIN_TAG)

create: plugin
	sudo mkdir -p /var/lib/docker-net-dhcp
	docker plugin rm -f $(PLUGIN_NAME):$(PLUGIN_TAG) || true
	docker plugin create $(PLUGIN_NAME):$(PLUGIN_TAG) $<
	docker plugin set $(PLUGIN_NAME):$(PLUGIN_TAG) LOG_LEVEL=trace

enable:
	docker plugin enable $(PLUGIN_NAME):$(PLUGIN_TAG)

disable:
	docker plugin disable $(PLUGIN_NAME):$(PLUGIN_TAG)

pdebug:
	sudo sh -c 'tail -f /var/lib/docker/plugins/*/rootfs/var/log/net-dhcp.log'

push: install
	docker plugin push $(PLUGIN_NAME):$(PLUGIN_TAG)

multiarch: $(SOURCES)
	docker buildx build --platform=$(PLATFORMS) -o type=local,dest=$@ .

push-multiarch: multiarch config.json
	scripts/push_multiarch_plugin.py -p $(PLATFORMS) config.json multiarch $(PLUGIN_NAME):$(PLUGIN_TAG)

clean:
	-rm -rf multiarch/
	-rm -rf plugin/
	-rm -rf bin/*
