# OpenSpec Design: Advanced DHCP Safety Nets for udhcpc-handler and Lifecycle Manager

## Context & Architecture

To prevent silent failures, stale ARP entry routing issues, packet-drop false negatives, and infinite decline loops during pool exhaustion, `udhcpc-handler` and `pkg/plugin/dhcp_manager` will incorporate five defensive layers:

```
                          [ udhcpc Event Trigger ]
                                     |
                                     v
                       +---------------------------+
                       |   Environment Validation  |  <-- Safety Net 2: Mask/Router Sanitization
                       +---------------------------+
                                     |
                                     v
                       +---------------------------+
                       |   Multi-Probe L2 Check    |  <-- Safety Net 3 (v4 ARP) / Safety Net 5 (v6 NS)
                       +---------------------------+
                               /           \
               [ Collision ]  /             \  [ Clear ]
                             /               \
                            v                 v
                 +-------------------+   +--------------------+
                 | Emit Collision &  |   | Emit GARP Broadcast| <-- Safety Net 1: Instant L2 Cache Update
                 | Exit Code 1       |   | & Return Success   |
                 +-------------------+   +--------------------+
                           |
                           v
                 +-------------------+
                 | dhcp_manager      |  <-- Safety Net 4: Throttling & Exponential Backoff
                 | Backoff Throttling|
                 +-------------------+
```

## Detailed Safety Net Specifications

### 1. Gratuitous ARP (GARP) Emission (Safety Net 1)
Upon successful `bound` or `renew` validation, `udhcpc-handler` constructs and sends two ARP Reply frames (`Opcode 2`, `Sender IP` = `Target IP` = `$ip`, `Sender MAC` = `Target MAC` = `$srcMAC`) out of `$interface`.
- **Packet Structure:** Ethernet broadcast (`FF:FF:FF:FF:FF:FF`) + ARP Reply payload.
- **Interval:** 2 frames spaced 50ms apart.

### 2. Defensive Environment Parsing (Safety Net 2)
When reading `$mask` and `$router` environment variables:
- If `$mask` is missing or invalid, default `$mask` to `255.255.255.0` (`/24`) for IPv4.
- If `$router` is empty, record gateway as empty string and log a warning without crashing.

### 3. Multi-Probe ARP Collision Probing (Safety Net 3)
Replace single-packet `WriteTo` in `hasConflict()` with a loop sending 3 ARP request frames (0ms, 100ms, 200ms) with a 350ms total socket deadline to guarantee resilience against single-packet network drops.

### 4. Exponential Backoff on Collision Cycles (Safety Net 4)
In `pkg/plugin/dhcp_manager.go`:
- Maintain a `collisionCount` integer in `dhcpManager`.
- Reset `collisionCount` to 0 on successful `bound`.
- On `collision` event, increment `collisionCount`. If `collisionCount >= 3`, sleep `time.Duration(collisionCount)*time.Second` before restarting `udhcpc`.

### 5. IPv6 ICMPv6 Neighbor Solicitation (Safety Net 5)
In `cmd/udhcpc-handler/main.go`, when `v6` is active during `bound`:
- Construct an ICMPv6 Neighbor Solicitation frame (Type 135) for target IPv6 address.
- Listen for ICMPv6 Neighbor Advertisement (Type 136) for 300ms. If a response is received from another DAD node, exit `1` to decline.
