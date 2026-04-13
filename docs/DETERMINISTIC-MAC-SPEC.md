# Deterministic MAC Resolution: Design Specification

**Status**: Approved
**Date**: 2026-04-13
**Scope**: `pkg/plugin/` — plugin.go, network.go, cache.go, dhcp_manager.go

## Problem

Docker's `CreateEndpoint` API does not include the container ID or name. The plugin
must resolve the container name to generate a deterministic MAC (seed = container name)
and to register DNS via DHCP Option 12/81.

The current event-based pending queue works for `docker run` (create event fires before
CreateEndpoint), but **fails for restarts** — the `container.start` event fires AFTER
CreateEndpoint. This forces a cascade through fallback layers that can fail under
concurrency, producing wrong MACs and — critically — **empty hostnames that prevent DNS
registration entirely**.

## Two DHCP Phases

| Phase | When | Where | Purpose |
|-------|------|-------|---------|
| 1 — Initial | CreateEndpoint `setup()` | Host-side veth | Get IP for Docker, register DNS |
| 2 — Persistent | Join → `dhcpManager.setupClient()` | Container netns via nsenter | Renewals, re-registration |

Both phases pass `Hostname` to udhcpc as Option 12 (`-x hostname:NAME`) and Option 81
(`-F NAME`). **Empty hostname = no DNS record.**

Phase 2 performs a fresh DHCPDISCOVER (not just renewal), so it can independently register
DNS even if Phase 1 did not.

## Changes

### C1: Add `SeedName` to EndpointState

Store the container name (MAC seed) in the persistent cache so it survives the
Leave→CreateEndpoint transition and plugin restarts.

```go
type EndpointState struct {
    // ... existing fields ...
    SeedName string `json:"seed_name,omitempty"`
}
```

Stored during Join alongside existing fields. Consumed in Leave (C2).

### C2: Pre-populate pending queue from Leave

When Docker restarts a container, the call sequence is:
`Leave → DeleteEndpoint → CreateEndpoint → Join`.

Leave is synchronous and happens before CreateEndpoint for the **same** container.
By pushing the container name (from our own cache) into the pending queue during Leave,
CreateEndpoint always has the correct name available.

```
Leave(networkID, endpointID):
    ep = cache.GetEndpoint(networkID, endpointID)
    if ep.SeedName != "":
        pushPendingContainer(networkID, ep.SeedName, ep.Hostname)
    // ... existing cleanup ...
```

### C3: Handle `container.die` events

Belt-and-suspenders: also listen for `die` events and pre-populate the queue.
Covers the case where the cache is stale (plugin was restarted, cache lost endpoint).

### C4: Serialize CreateEndpoint with mutex

Add `createMu sync.Mutex` to Plugin. Lock for the duration of CreateEndpoint.
This enables process-of-elimination: after each CreateEndpoint completes and records
its endpoint, the next call sees one fewer untracked container.

### C5: Improved deep fallback

When the pending queue is empty and findInNetwork fails:
1. List all containers on the network.
2. **Exclude** containers whose EndpointID is tracked in `persistentDHCP` or `cache.Endpoints`.
3. If exactly 1 candidate remains → use it.
4. If multiple remain → sort by `Created` timestamp (most recent first) and pick first.
5. Log at WARN with candidate count for observability.

### C6: Join-time hostname recovery (safety net)

If hostname is still empty at Join time (all CreateEndpoint fallbacks failed), resolve
the container via SandboxKey (which IS available in Join). List containers, match
`NetworkSettings.SandboxKey`. Set `m.hostname` before Phase 2 starts. DNS gets
registered on the Phase 2 DHCPDISCOVER (~2-3s delay, acceptable).

### C7: Increase retry timeouts

- Pending queue poll: 10 × 100ms → 30 × 100ms (1s → 3s)
- findInNetwork poll: 5 × 200ms → 10 × 200ms (1s → 2s)

### C8: Debug log CreateEndpoint Options

One-line debug log of `r.Options` map. Future Docker versions may include DNS names
or other identifying info — this lets us detect it without code changes.

## Resolution Table (After Changes)

| Scenario | Resolution path | MAC | DNS | IP |
|----------|----------------|-----|-----|-----|
| `docker run` | create event → queue | Correct | Phase 1 | Correct |
| `docker restart` | Leave → queue (C2) | Correct | Phase 1 | Correct |
| `docker restart A B C` | Sequential Leave → queue | Correct | Phase 1 | Correct |
| `docker compose up` | create events → queue | Correct | Phase 1 | Correct |
| `docker compose restart` | Sequential Leave → queue | Correct | Phase 1 | Correct |
| `docker network connect` | network.connect event / queue | Correct | Phase 1 | Correct |
| Concurrent independent restarts | Leave queue + serialized elimination (C4+C5) | Correct* | Phase 1 | Correct* |
| Plugin restart mid-lifecycle | Persistent cache + improved fallback (C5) | Best effort | Phase 2 (C6) | Best effort |

*First concurrent call uses FIFO (may mismatch); subsequent calls use process-of-elimination.

## Implementation Order

1. C1 — SeedName in EndpointState + store in Join
2. C2 — Leave pre-population
3. C3 — Handle die events
4. C4 — CreateEndpoint serialization
5. C5 — Improved deep fallback
6. C6 — Join hostname recovery
7. C7 — Retry timeouts
8. C8 — Options debug log

Each change has a corresponding unit test addition/update. Run `go test ./pkg/...`
after each change to verify no regressions.
