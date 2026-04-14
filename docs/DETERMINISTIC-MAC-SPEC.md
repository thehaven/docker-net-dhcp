# Deterministic MAC Resolution: Design Specification

**Status**: Implemented and verified
**Date**: 2026-04-13
**Scope**: `pkg/plugin/` -- plugin.go, network.go, cache.go, dhcp_manager.go
**Prerequisites**: Read `docs/DOCKER-NETWORK-PLUGIN-API.md` first for Docker API constraints.

## Problem

Docker's `CreateEndpoint` API does not include the container ID or name. The plugin
must resolve the container name to generate a deterministic MAC (seed = container name)
and to register DNS via DHCP Option 12/81.

Additionally, Docker does NOT expose EndpointID in ContainerList/ContainerInspect until
AFTER Join returns. This means container identification via EndpointID is impossible at
both CreateEndpoint time and Join time.

Under concurrency (`docker compose up`), the order of `container.create` events does NOT
match the order of `CreateEndpoint` calls. A FIFO queue populated by create events
produces wrong assignments ~50% of the time.

## Solution Architecture

The solution uses a **two-phase approach**: best-effort FIFO at CreateEndpoint time,
followed by authoritative post-Join correction.

```
CreateEndpoint                          Join
    │                                    │
    ├─ FIFO pop (best-effort)            ├─ Return routes/interface
    ├─ Deep fallback (if FIFO empty)     └─ Spawn goroutine ──┐
    ├─ Generate MAC from seedName                              │
    ├─ Create veth + set MAC                          m.Start() succeeds
    └─ Run Phase 1 DHCP                                        │
                                                    ┌──────────┘
                                                    │
                                            Post-Join correction:
                                            ├─ ContainerList (EndpointID now visible)
                                            ├─ Match EndpointID → actual container
                                            ├─ If FIFO was wrong:
                                            │   ├─ Regenerate MAC from actual name
                                            │   └─ Set MAC on container veth (netHandle)
                                            ├─ Update hostname for DHCP
                                            └─ Start Phase 2 DHCP (uses correct MAC)
```

## Two DHCP Phases

| Phase | When | Where | Purpose |
|-------|------|-------|---------|
| 1 -- Initial | CreateEndpoint `setup()` | Host-side veth | Get IP for Docker, register DNS |
| 2 -- Persistent | Post-Join goroutine via `dhcpManager.setupClient()` | Container netns | Renewals, re-registration |

Both phases pass `Hostname` to udhcpc as Option 12 (`-x hostname:NAME`) and Option 81
(`-F NAME`). **Empty hostname = no DNS record.**

Phase 2 performs a fresh DHCPDISCOVER (not just renewal), so it can independently register
DNS even if Phase 1 used the wrong hostname.

## Implemented Changes

### C1: Add `SeedName` to EndpointState

Store the container name (MAC seed) in the persistent cache so it survives the
Leave->CreateEndpoint transition and plugin restarts.

```go
type EndpointState struct {
    // ... existing fields ...
    SeedName string `json:"seed_name,omitempty"`
}
```

Stored during the post-Join goroutine alongside existing fields. Consumed in Leave (C2).

### C2: Pre-populate pending queue from Leave

When Docker restarts a container, the call sequence is:
`Leave -> DeleteEndpoint -> CreateEndpoint -> Join`.

Leave is synchronous and happens before CreateEndpoint for the **same** container.
Entries pushed from Leave are marked `forward=false` (backward-looking).

```
Leave(networkID, endpointID):
    ep = cache.GetEndpoint(networkID, endpointID)
    if ep.SeedName != "":
        pushPendingContainer(networkID, ep.SeedName, ep.Hostname, forward=false)
        upsertPendingMeta(networkID, ep.SeedName, ep.Hostname)
```

### C3: Handle `container.die` events

Listen for `die` events and pre-populate the queue (backward-looking, `forward=false`).
Covers the case where the cache is stale (plugin was restarted, cache lost endpoint).

### C4: Serialize CreateEndpoint with mutex

`createMu sync.Mutex` locks for the duration of CreateEndpoint. Enables
process-of-elimination: after each call completes, the next sees one fewer
untracked container.

### C5: Improved deep fallback

When the pending queue is empty:
1. List all containers on the network.
2. **Exclude** containers whose name is already claimed in `joinHints`.
3. If exactly 1 candidate remains -> use it.
4. If multiple remain -> refuse to guess (log WARN).

### C6: Source-tagged FIFO with forward flush

Each FIFO entry carries a `forward` flag:
- `forward=true`: from `container.create` events (compose up, docker run)
- `forward=false`: from `container.die` / `Leave` events (restart, compose down)

When the first forward entry is pushed for a network, ALL backward entries are
flushed. This prevents compose-down die events from contaminating a subsequent
compose-up.

### C7: Start events excluded from FIFO

`container.start` events fire AFTER CreateEndpoint returns. Pushing them to
the FIFO re-appends names that were already popped by CreateEndpoint, corrupting
the queue for subsequent containers. Start events ONLY update the metadata map
(`registerPendingForNetworkMeta`).

### C8: Post-Join MAC correction (authoritative)

This is the key mechanism that makes deterministic MACs reliable under concurrency.

After `m.Start()` succeeds in the post-Join goroutine:
1. Docker has committed the EndpointID->container association
2. `ContainerList` returns the EndpointID in network settings
3. If the actual container name differs from `hint.SeedName`:
   - Generate correct MAC from actual name
   - Set MAC on container veth via `m.netHandle.LinkSetHardwareAddr(m.ctrLink, addr)`
   - Update `hint.SeedName` and `m.hostname` for cache persistence and DHCP
4. Phase 2 DHCP client starts AFTER correction (uses correct MAC + hostname)

**Important**: `docker inspect` MacAddress field will still show the original
CreateEndpoint MAC. The actual interface MAC (visible via
`docker exec <ctr> cat /sys/class/net/eth0/address`) is correct.

### C9: Retry timeouts

- FIFO queue poll: 15 x 200ms = 3s max wait
- Deep fallback runs once (no retry, relies on post-Join correction)

### C10: Debug log CreateEndpoint Options

One-line debug log of `r.Options` map. Future Docker versions may include DNS names
or other identifying info.

## Resolution Table

| Scenario | Resolution path | MAC | DNS |
|----------|----------------|-----|-----|
| `docker run` | create event -> FIFO | Correct (FIFO) | Phase 1 |
| `docker restart` | Leave -> FIFO (backward) | Correct (FIFO) | Phase 1 |
| `docker restart A B C` | Sequential Leave -> FIFO + serialized CE | Correct (FIFO) | Phase 1 |
| `docker compose up` (concurrent) | create events -> FIFO (may mismatch) + **post-Join correction** | Correct (post-Join) | Phase 2 |
| `docker compose down && up` | Forward flush + FIFO + **post-Join correction** | Correct (post-Join) | Phase 2 |
| `docker compose restart` | Sequential Leave -> FIFO | Correct (FIFO) | Phase 1 |
| `docker network connect` | network.connect -> metadata only; FIFO or deep fallback | Correct* | Phase 1 |
| Plugin restart mid-lifecycle | Persistent cache + deep fallback | Best effort | Phase 2 |

Verified: 3 consecutive `docker compose down && up` cycles, 6 containers each,
all 18 MAC assignments correct.

## Data Structures

### Per-network FIFO queue (`pendingQueue`)

```go
pendingQueue map[string][]pendingContainer  // key: networkID
type pendingContainer struct {
    name      string
    hostname  string
    createdAt time.Time
    forward   bool  // true = create event; false = die/Leave
}
```

- CreateEndpoint pops from front
- Forward entries flush all backward entries on push
- Deduplicates by name on push
- Pruned after 2 minutes by scavenger

### Per-network metadata map (`pendingMeta`)

```go
pendingMeta map[string]map[string]pendingContainer  // key: networkID -> containerName
```

- Updated by ALL events (create, start, die, network.connect, Leave)
- Survives FIFO flush
- Used for hostname enrichment after FIFO pop
- Pruned after 2 minutes by scavenger

### joinHints (transient, CreateEndpoint -> Join)

```go
joinHints map[string]joinHint  // key: EndpointID
type joinHint struct {
    IPv4, IPv6  *netlink.Addr
    Gateway     string
    Hostname    string
    SeedName    string  // container name used as MAC seed
}
```

Populated during CreateEndpoint, consumed and deleted during Join.

### EndpointState (persistent cache)

```go
type EndpointState struct {
    ID, SandboxKey, MacAddress, IP, IPv6, Gateway string
    Hostname  string  // for DHCP Option 12/81 on recovery
    SeedName  string  // for Leave pre-population across restarts
}
```

Persisted to `/var/lib/docker-net-dhcp/networks.json`. Survives plugin restarts.
