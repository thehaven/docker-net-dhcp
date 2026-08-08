# OpenSpec Change Proposal: ARP Collision Prevention and Deterministic Phase-1 MAC Alignment

## Why

During a recent container launch incident on `vlan107`, two dynamic containers (`iplayarr` and `certbot`) launched without explicit `--ip` flags were assigned the exact same IP address (`192.168.107.88`) by the upstream network DHCP server. This caused ARP cache poisoning and intermittent 503 service outages when traffic intended for `iplayarr` was routed to `certbot`, which rejected TCP connections (`TCP RST`).

The root causes identified in `docker-net-dhcp` were:
1. **Phase-1 vs Phase-2 MAC Mismatch:** During `CreateEndpoint` (Phase 1), `docker-net-dhcp` ran `udhcpc` using a seed/transient MAC address derived from `EndpointID`. After `Join` (Phase 2), the plugin updated the interface MAC to the container's true deterministic MAC (`MD5(container_name)`). The upstream DHCP server recorded `.88` as assigned to the transient MAC, marked it stale when Phase 1 exited, and then offered `.88` to `certbot`.
2. **Missing ARP Probe & `DHCPDECLINE`:** When `udhcpc-handler` received a `bound` event for `.88` for `certbot`, it accepted the lease without verifying if `.88` was already active on `vlan107`. BusyBox `udhcpc` supports sending a `DHCPDECLINE` back to the server if the script handler fails during `bound`, but `udhcpc-handler` did not execute ARP conflict probes.

## What Changes

- Add an L2 ARP conflict probe (Gratuitous ARP / ARP ping) to `cmd/udhcpc-handler` executed on `bound` and `renew` events before accepting an IP lease.
- Configure `cmd/udhcpc-handler` to exit with a non-zero status when an IP conflict is detected, triggering BusyBox `udhcpc` to automatically transmit a `DHCPDECLINE` frame to the DHCP server and request a new IP.
- Emit a `collision` event JSON from `udhcpc-handler` to `pkg/plugin/dhcp_manager` so the plugin can log structured collision metrics and enforce back-off before retrying.
- Align Phase 1 (`GetIP`) and Phase 2 (`Start`) MAC address resolution: if container name pre-resolution is unpopulated during `CreateEndpoint`, compute and reuse the container's deterministic MAC hint across both phases so `udhcpc` never presents a transient MAC to the upstream DHCP server.

## Capabilities

### New Capabilities

- `arp-conflict-detection`: In-line L2 ARP verification in `udhcpc-handler` that rejects collided IP offers and issues `DHCPDECLINE` to upstream DHCP servers.
- `dhcp-collision-telemetry`: Structured JSON reporting of IP collision events between `udhcpc-handler` and `dhcp_manager`.

### Modified Capabilities

- `deterministic-mac-resolution`: Guarantee MAC parity between Phase 1 (`CreateEndpoint` / `GetIP`) and Phase 2 (`Join` / `Start`) to eliminate transient lease pollution.

## Impact

- `cmd/udhcpc-handler/main.go`
- `pkg/udhcpc/client.go`
- `pkg/udhcpc/handler.go`
- `pkg/plugin/dhcp_manager.go`
- `pkg/plugin/network.go`
- Unit and integration tests in `pkg/udhcpc/` and `pkg/plugin/`
