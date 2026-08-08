# OpenSpec Change Proposal: Advanced DHCP Safety Nets for udhcpc-handler and Lifecycle Manager

## Why

Following the ARP collision resolution on `vlan107`, analysis of `udhcpc-handler` and `pkg/plugin/dhcp_manager` revealed five critical edge-case failure modes in container DHCP network management:

1. **L2 Switch & ARP Cache Stale Delay:** When a container acquires an IP lease, neighboring hosts and L2 switches may temporarily retain stale ARP entries pointing to previous MAC addresses, leading to initial packet loss.
2. **Malformed Environment Fallback:** Upstream DHCP servers occasionally omit optional DHCP parameters (`$mask`, `$router`), resulting in malformed CIDR allocations (e.g. `192.168.107.88/`) or missing default gateways.
3. **Single ARP Probe Packet Loss:** A single L2 ARP probe frame can be dropped due to network jitter or switch Spanning Tree Protocol (STP) port forwarding delays, causing false-negative collision checks.
4. **DHCP Pool Exhaustion Spinning:** When a subnet/DHCP scope is completely full or heavily collided, `udhcpc` repeatedly retries `DISCOVER` $\rightarrow$ `DHCPDECLINE` in an unthrottled loop, consuming CPU and flooding the L2 network.
5. **IPv6 Duplicate Address Detection (DAD) Gap:** `udhcpc-handler` currently only performs IPv4 ARP checks, leaving IPv6 (`udhcpc6`) allocations vulnerable to duplicate address collisions on dual-stack networks.

## What Changes

- **Safety Net 1 (GARP Announcements):** Update `cmd/udhcpc-handler` to emit 2 Gratuitous ARP (GARP) broadcast frames upon every successful `bound` or `renew` event to immediately update neighboring ARP caches and L2 switch forwarding tables.
- **Safety Net 2 (Strict Environment Validation & Fallbacks):** Add validation in `cmd/udhcpc-handler` to fallback unpopulated `$mask` values to valid default IPv4 netmasks (`/24`) and log structured warnings when `$router` is omitted.
- **Safety Net 3 (Multi-Probe ARP Check with Jitter):** Upgrade `hasConflict()` in `cmd/udhcpc-handler` to issue 3 spaced ARP request probes (50ms interval, 300ms total window) to prevent single-packet drop false negatives.
- **Safety Net 4 (Collision Backoff Throttling):** Update `pkg/plugin/dhcp_manager.go` to track consecutive collision events and enforce a 5-second exponential backoff delay after 3 consecutive collision declines.
- **Safety Net 5 (IPv6 DAD Probe via ICMPv6):** Implement ICMPv6 Neighbor Solicitation (NS) Duplicate Address Detection in `cmd/udhcpc-handler` when invoked by `udhcpc6` on IPv6 `bound` events.

## Capabilities

### New Capabilities

- `gratuitous-arp-announcement`: Automatic L2 GARP broadcast emission post-lease binding.
- `dhcp-env-validation-fallback`: Defensively parse and sanitize missing/malformed DHCP options from `udhcpc`.
- `multi-probe-arp-checking`: Multi-packet ARP collision probing with jitter tolerance.
- `dhcp-exhaustion-throttling`: Backoff throttling and telemetry for DHCP pool exhaustion loop prevention.
- `ipv6-dad-verification`: In-line ICMPv6 Neighbor Solicitation probe for `udhcpc6` IPv6 address uniqueness.

### Modified Capabilities

- `arp-conflict-detection`: Extended from single-packet check to multi-probe + IPv6 dual-stack safety net.

## Impact

- `cmd/udhcpc-handler/main.go`
- `pkg/plugin/dhcp_manager.go`
- `pkg/udhcpc/client.go`
- `pkg/udhcpc/handler.go`
- Unit and integration tests in `pkg/udhcpc/` and `pkg/plugin/`
