# Local Plugin Deployment Guide

This guide describes how to manually build and replace the `docker-net-dhcp` plugin on a local Docker host without deleting existing networks.

## Prerequisites
- Go 1.24+
- Docker installed and running
- `sudo` privileges for Docker and filesystem operations

## Deployment Steps

### 1. Build the Binaries
Build the Go binaries locally to ensure there are no compilation errors.
```bash
make bin/net-dhcp bin/udhcpc-handler
```

### 2. Build the RootFS Image
Build the Docker image that contains the plugin environment (Alpine-based).
```bash
sudo docker build -t ghcr.io/thehaven/docker-net-dhcp:rootfs .
```

### 3. Extract the RootFS
The Docker plugin system requires a flattened directory of the filesystem.
```bash
# Clean up old build artifacts
rm -rf plugin/rootfs && mkdir -p plugin/rootfs

# Create a temporary container to export the filesystem
sudo docker create --name tmp ghcr.io/thehaven/docker-net-dhcp:rootfs
sudo docker export tmp | tar xC plugin/rootfs
sudo docker rm -vf tmp

# Ensure the config.json is in the plugin directory
cp config.json plugin/
```

### 4. Hot-Swap the Plugin
Since Docker networks are tied to the plugin name/tag, we replace the plugin in-place.

**Note:** With the new Endpoint Persistence logic, existing containers will **not** lose connectivity during this swap as the new plugin will automatically re-adopt them and resume DHCP renewals.

```bash
# 1. Force disable the plugin
sudo docker plugin disable -f ghcr.io/thehaven/docker-net-dhcp:golang

# 2. Remove the old version
sudo docker plugin rm -f ghcr.io/thehaven/docker-net-dhcp:golang

# 3. Create the plugin from the local 'plugin' directory
sudo docker plugin create ghcr.io/thehaven/docker-net-dhcp:golang plugin

# 4. Re-enable the plugin
sudo docker plugin enable ghcr.io/thehaven/docker-net-dhcp:golang
```

### 5. Verification
Check that your existing network is still correctly associated:
```bash
sudo docker network inspect vlan107
```

Check the plugin logs to ensure it started correctly and re-adopted existing endpoints:
```bash
sudo tail -f /var/lib/docker/plugins/*/rootfs/var/log/net-dhcp.log
```
