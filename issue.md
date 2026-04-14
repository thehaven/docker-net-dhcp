# Issue: DHCP Leases Expiring and Not Renewing

## Problem Description
Containers issued IPs via `docker-net-dhcp` are losing their leases and associated DNS records while remaining active. Specifically, `litellm-proxy` was found to have an "invalid IP" in `dip` and no record in the DHCP/DNS server (NXDOMAIN).

## Identified Root Causes

### 1. Premature Lease Release on Plugin Stop/Restart
In `pkg/udhcpc/client.go`, background `udhcpc` instances were started with the `-R` flag. This flag instructs BusyBox `udhcpc` to send a DHCP release packet upon receiving a `SIGTERM`. Since the plugin sends `SIGTERM` to the client when it stops (e.g., during an upgrade or restart), the DHCP server immediately marks the lease as available and removes DNS records, even though the container is still running.

### 2. Incomplete Event Handling in `dhcpManager`
The plugin's event loop in `pkg/plugin/dhcp_manager.go` only processed events of type `renew`. If `udhcpc` emitted a `bound` event during its T1/T2 renewal cycles, the plugin would ignore it, failing to update internal state.

### 3. Missing Failure Signaling
- The `udhcpc-handler` (`cmd/udhcpc-handler/main.go`) explicitly ignored `deconfig`, `leasefail`, and `nak` events, meaning no JSON was sent to the plugin when renewal failed.
- `dhcp_manager.go` did not handle these failure states, leading to silent expiration of IPs.

### 4. Namespace Access Restrictions (New)
The plugin container lacks a mount for `/var/run/docker/netns`, preventing `util.AwaitNetNS` from accessing the network namespace paths provided by Docker. This caused all background DHCP renewal clients to fail to start silently.

### 5. Race Conditions in Hostname Resolution (New)
The `pendingContainer` queue (populated by Docker events) is frequently empty during container or plugin restarts. When empty, `CreateEndpoint` defaulted to the `EndpointID` as the MAC seed and provided no hostname to `udhcpc`, causing DNS registration to fail (NXDOMAIN).

## Fix Strategy

### 1. Lease Stability & Event Handling
- **Remove `-R` Flag**: Background clients no longer release leases on plugin exit.
- **Process `bound` Events**: `dhcpManager` now treats `bound` events identically to `renew`.
- **Failure Visibility**: `udhcpc-handler` and `dhcpManager` now log `nak`, `leasefail`, and `deconfig` events.

### 2. Namespace Access Fallback
- **PID-based Lookup**: If `/var/run/docker/netns` is inaccessible (due to missing mounts), the plugin now looks up the container's Host PID via the Docker API and accesses the namespace via `/proc/<pid>/ns/net`. This ensures background DHCP clients can start regardless of mount configuration.

### 3. Robust Hostname & MAC Seeding
- **API-based Fallback**: If the `pendingContainer` queue is empty, `CreateEndpoint` now performs an active lookup via `NetworkInspect` (and `ContainerList` as a deep fallback) to resolve the container's true name and hostname. This ensures deterministic MACs and correct DNS registration even during rapid restarts or recovery scenarios.

### 4. System Integrity
- **Static Builds**: All binaries are now built with `CGO_ENABLED=0` to ensure they run correctly within the Alpine rootfs regardless of the host's libc (e.g. Gentoo).
- **Nil Safety**: Added guards for network handles to prevent panics during partial failures.

## Current Status
Fixes have been implemented and verified via unit tests. Application to the running plugin is in progress via binary replacement and "warm recovery" restart.
