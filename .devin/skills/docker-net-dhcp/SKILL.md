---
name: docker-net-dhcp
description: "Load Docker network plugin API constraints and deterministic MAC spec. Invoke BEFORE modifying network.go, plugin.go, dhcp_manager.go, or cache.go."
triggers:
  - user
  - model
allowed-tools:
  - read
  - grep
  - glob
  - edit
  - exec
---

# docker-net-dhcp: Network Plugin Context

You are working on a Docker network plugin that manages DHCP and deterministic
MAC address generation. Before making ANY changes to the network plugin code,
you MUST understand Docker's behavioral constraints.

## Full Spec Documents

Read these before making changes (paths relative to project root):

- `docs/DOCKER-NETWORK-PLUGIN-API.md` — Empirical Docker API behavioral constraints
- `docs/DETERMINISTIC-MAC-SPEC.md` — Deterministic MAC resolution design and implementation

## Container Start Lifecycle (Plugin Perspective)

```
container.create event          <- async, fires BEFORE CreateEndpoint
    |
CreateEndpoint(NetworkID, EndpointID, Interface, Options)
    |  -- NO container ID/name available
    |  -- Docker BLOCKS here until plugin returns
    |  -- ContainerList for THIS endpoint deadlocks
    |
Docker updates internal state (NOT yet API-visible)
    |
Join(NetworkID, EndpointID, SandboxKey, Options)
    |  -- Docker BLOCKS here until plugin returns
    |  -- ContainerList still does NOT show this EndpointID
    |
Docker moves veth peer into container namespace
Docker commits endpoint association to API-visible state
    |  -- ContainerList NOW shows EndpointID
    |
container.start event          <- async, fires AFTER Join returns
```

## Critical Rules — NEVER Violate These

1. **NEVER attempt container identification at Join time via ContainerList/Inspect.**
   Docker has NOT committed the EndpointID association yet. Returns zero matches.
   Verified across 18 consecutive Join calls (72-77 containers scanned, 0 matches).

2. **NEVER push start events to the FIFO queue.**
   Start events fire AFTER CreateEndpoint. Pushing them re-appends already-popped
   names, corrupting the queue for subsequent containers.

3. **NEVER call findInNetwork or similar during CreateEndpoint.**
   Docker is blocked waiting for the plugin response. This deadlocks.

4. **NEVER assume FIFO order matches CreateEndpoint order.**
   Under concurrency (`docker compose up`), Docker's goroutine scheduler determines
   CreateEndpoint call order independently of event delivery order. Empirically
   verified: create events fire as `qdrant, mcp, neo4j, postgres, redis` but
   CreateEndpoint calls arrive as `mcp, neo4j, qdrant, redis, postgres`.
   FIFO is best-effort (~50% hit rate); post-Join correction is authoritative.

5. **Post-Join goroutine is the ONLY reliable point for EndpointID-based container
   identification.** After `m.Start()` succeeds but BEFORE `m.setupClient()`:
   Docker has committed the endpoint association. Use
   `m.netHandle.LinkSetHardwareAddr(m.ctrLink, addr)` for MAC correction
   (namespace-aware netlink handle required — veth is in container namespace).

6. **`docker inspect` MacAddress is stale after post-Join correction.**
   Docker records the MAC from CreateEndpoint and never updates it.
   Verify actual MACs via: `docker exec <ctr> cat /sys/class/net/eth0/address`

## Resolution Architecture

```
CreateEndpoint (best-effort)          Post-Join goroutine (authoritative)
  |                                     |
  +- FIFO pop                           +- m.Start() (container ns ready)
  +- Deep fallback (if empty)           +- ContainerList: EndpointID NOW visible
  +- Generate MAC from seedName         +- If actual name != FIFO name:
  +- Create veth + set MAC              |    +- Regenerate MAC from actual name
  +- Phase 1 DHCP (host-side)           |    +- Set MAC via netHandle (container ns)
                                        |    +- Update hint.SeedName + m.hostname
                                        +- Phase 2 DHCP (container ns, correct MAC)
                                        +- Persist EndpointState to cache
```

## Event Handling Rules

| Event | FIFO push? | Metadata map? | forward flag |
|-------|-----------|--------------|--------------|
| container.create | YES | YES | true (flushes backward entries) |
| container.start | NO | YES | n/a |
| container.die | YES | YES | false |
| Leave (from cache) | YES | YES | false |
| network.connect | NO | YES | n/a |

## Build & Deploy

- Build: `GOROOT=~/.local/share/mise/installs/go/1.26.1 GOPATH=/tmp/go126-path HOME=/tmp/go126-home CGO_ENABLED=0 ~/.local/share/mise/installs/go/1.26.1/bin/go build -o net-dhcp ./cmd/net-dhcp`
- Tests: `go test ./pkg/...` (can use system Go)
- Deploy: disable plugin -> `sudo cp` binary to rootfs -> enable plugin
- Plugin rootfs: `/var/lib/docker/plugins/f094d0913c1775bb54da0b8a5dbc06ccfaaab7bb37464b2550a4d2ef59f5369f/rootfs/`
- Plugin log: `$ROOTFS/var/log/net-dhcp.log`
