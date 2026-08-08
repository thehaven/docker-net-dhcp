# OpenSpec Design: ARP Collision Prevention and Deterministic Phase-1 MAC Alignment

## Context & Architecture

`docker-net-dhcp` manages container network interfaces by spawning BusyBox `udhcpc` instances to request IP leases from an upstream L2 network DHCP server. 

```
+-----------------------------------------------------------------------------------+
| Docker API / Plugin API                                                           |
|                                                                                   |
| 1. CreateEndpoint  ---> GetIP() ---> udhcpc (Phase 1 Probe)                       |
| 2. Join            ---> Start() ---> udhcpc-handler (Phase 2 Daemon)             |
+-----------------------------------------------------------------------------------+
                                       |
                                       v
                     +-----------------------------------+
                     | cmd/udhcpc-handler               |
                     | - Executes ARP conflict probe     |
                     | - Exits non-zero if occupied      |
                     +-----------------------------------+
                                       |
                   [ Conflict ] /      \ [ Clean / Unique ]
                               /        \
                              v          v
                 BusyBox sends           Accept IP & output
                 DHCPDECLINE to          JSON bound event
                 upstream DHCP           to dhcp_manager
```

## Detailed Design Decisions

### 1. In-Line L2 ARP Probe in `udhcpc-handler`

When `udhcpc` receives a `bound` or `renew` event from the DHCP server, it invokes `cmd/udhcpc-handler`. Before outputting JSON or returning success (`0` exit code):

1. **Read Environment:** Extract `$ip` and `$interface` from `udhcpc` environment variables.
2. **ARP Probing:** Send 2 raw ARP Request frames (`ARPing`) for `$ip` out of `$interface` with a 250ms timeout.
3. **Collision Handling:**
   - **If an ARP Reply is received from a different MAC address:**
     - Log `IP address collision detected: <ip> already claimed by <mac>` to stderr.
     - Emit JSON event `{"type": "collision", "ip": "<ip>", "mac": "<mac>"}` on stdout.
     - **Exit with code 1**.
   - **BusyBox `udhcpc` Reaction:** When the handler script returns a non-zero exit code during `bound`, BusyBox `udhcpc` automatically sends a `DHCPDECLINE` packet to the DHCP server and restarts discovery for a non-conflicting IP.

### 2. Elimination of Transient MACs in Phase 1 (`GetIP`)

To prevent Phase 1 from requesting leases on transient MAC addresses:

1. **Explicit MAC Passing:** Update `DHCPClientOptions` to accept an explicit `MacAddress` field for Phase 1 `GetIP()`.
2. **MAC Hash Consistency:** When `CreateEndpoint` is called, calculate the deterministic MAC (`MD5(container_name)`) immediately if pre-resolved, or pass the pre-computed deterministic seed MAC to Phase 1 so `udhcpc` presents the exact same MAC to the DHCP server in both Phase 1 and Phase 2.
3. **Lease Preservation:** Ensure Phase 1 `udhcpc` process cleanup calls `Reap()` (wait/exit) without issuing a `DHCPRELEASE` signal (`SIGTERM`).

## Open Questions & Risks

- **Performance Overhead:** Adding a 250ms–500ms ARP probe delay inside `udhcpc-handler` during `bound` slightly increases container networking initialization time. This is an acceptable tradeoff for avoiding IP collisions.
