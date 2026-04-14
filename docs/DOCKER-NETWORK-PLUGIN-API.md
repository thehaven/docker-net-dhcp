# Docker Network Plugin API: Empirical Capabilities & Constraints

**Status**: Living document (update when new behavior is discovered)
**Last verified**: 2026-04-13, Docker Engine 28.x, Docker API v1.44
**Scope**: Behavioral facts about the Docker network plugin v2 API that are NOT
documented in Docker's official references but critically affect plugin design.

Everything in this document was verified through instrumented plugin builds and
production `docker compose` cycles. Treat it as ground truth; do NOT write code
that contradicts these constraints without re-verifying empirically first.

---

## 1. Container Start Lifecycle (Plugin Perspective)

Docker's internal flow when starting a container on a plugin-managed network:

```
container.create event          ← Docker event stream (async)
    │
    ▼
CreateEndpoint(NetworkID, EndpointID, Interface, Options)
    │  ── Plugin creates veth pair, assigns MAC, runs initial DHCP
    │  ── Plugin returns IP + MAC to Docker
    │  ── Docker BLOCKS here until plugin returns
    │
    ▼
Docker updates internal endpoint state
    │  ── EndpointID associated with container
    │  ── BUT: not yet committed to API-visible state
    │
    ▼
Join(NetworkID, EndpointID, SandboxKey, Options)
    │  ── Plugin returns interface mapping + routes
    │  ── Docker BLOCKS here until plugin returns
    │
    ▼
Docker moves veth peer into container namespace
    │
    ▼
Docker commits endpoint association to API-visible state
    │  ── ContainerList / ContainerInspect NOW show EndpointID
    │
    ▼
container.start event           ← Docker event stream (async)
```

### Key ordering facts

| Fact | Verified |
|------|----------|
| `container.create` event fires BEFORE `CreateEndpoint` | Yes |
| `container.start` event fires AFTER `CreateEndpoint` returns | Yes |
| `container.start` event fires AFTER `Join` returns | Yes |
| `CreateEndpoint` is called BEFORE `Join` (same container) | Yes |
| Docker blocks on `CreateEndpoint` return before calling `Join` | Yes |
| Multiple concurrent `CreateEndpoint` calls can interleave across containers | Yes |
| Create event order does NOT match `CreateEndpoint` call order under concurrency | Yes |

---

## 2. What Data Is Available At Each API Call

### CreateEndpoint

```go
type CreateEndpointRequest struct {
    NetworkID  string                 // full 64-char hex
    EndpointID string                 // full 64-char hex
    Interface  *EndpointInterface     // non-nil only if user specified MAC
    Options    map[string]interface{} // typically empty (logged for future use)
}
```

**Available**: NetworkID, EndpointID, user-specified MAC (if any)
**NOT available**: Container ID, container name, hostname, sandbox key

**Critical constraint**: Docker is blocked waiting for our response. Any Docker
API call that depends on THIS endpoint completing (e.g. `ContainerList` looking
for this EndpointID, or `NetworkInspect` looking for this container) will
deadlock or return stale data.

### Join

```go
type JoinRequest struct {
    NetworkID  string
    EndpointID string                 // same as CreateEndpoint
    SandboxKey string                 // e.g. "/var/run/docker/netns/abc123"
    Options    map[string]interface{}
}
```

**Available**: NetworkID, EndpointID, SandboxKey
**NOT available**: Container ID, container name, hostname

**Critical constraint**: Docker has NOT yet committed the EndpointID-to-container
association to its API-visible state. `ContainerList` and `ContainerInspect` will
NOT show this EndpointID in the container's `NetworkSettings.Networks[].EndpointID`
until AFTER Join returns. This was verified empirically: scanning 72-77 containers
at Join time produced ZERO EndpointID matches across 6 consecutive Join calls.

### After Join returns (post-Join goroutine)

Docker commits the endpoint association. `ContainerList` NOW returns the
EndpointID in `container.NetworkSettings.Networks[networkName].EndpointID`.
This is the earliest point where EndpointID-based container identification works.

The veth peer has been moved into the container namespace by this point. MAC
changes must use a namespace-aware netlink handle (`netlink.NewHandleAt(nsHandle)`).

### Leave

```go
type LeaveRequest struct {
    NetworkID  string
    EndpointID string
}
```

**Available**: NetworkID, EndpointID
**NOT available**: Container ID, container name, hostname

Leave fires BEFORE `DeleteEndpoint` and BEFORE `CreateEndpoint` for the SAME
container during a restart. This ordering is reliable and is the basis for
Leave-based pre-population of the pending queue.

---

## 3. Docker Event Stream Behavior

### Event types relevant to network plugins

| Event | `Type` | `Action` | When it fires | Reliable ordering |
|-------|--------|----------|---------------|-------------------|
| Container create | `container` | `create` | Before CreateEndpoint | Yes (single container) |
| Container start | `container` | `start` | After Join returns | Yes |
| Container die | `container` | `die` | Before Leave | Yes |
| Network connect | `network` | `connect` | After CreateEndpoint | Yes, but too late |

### Event data available

All container events include `Actor.ID` (container ID) and `Actor.Attributes`
with `name` (container name). To get hostname and network mappings, a
`ContainerInspect` call is required.

### Concurrency: create event order vs CreateEndpoint order

**This is the most important behavioral fact in this document.**

When `docker compose up` starts N containers concurrently, Docker fires N
`container.create` events and then calls `CreateEndpoint` N times. The order
of create events does NOT match the order of CreateEndpoint calls.

Example from production (5 containers, same compose file):

```
Create events:     qdrant, mcp, neo4j, postgres, redis
CreateEndpoint:    mcp, neo4j, qdrant, redis, postgres
```

This means a FIFO queue populated by create events and consumed by CreateEndpoint
will assign the WRONG container name ~50% of the time under concurrency. This is
a structural property of Docker's goroutine scheduling, not a race condition that
can be fixed with retries.

---

## 4. Docker API Visibility Windows

| Data point | CreateEndpoint | Join | Post-Join goroutine | After container start |
|------------|---------------|------|--------------------|-----------------------|
| Container's EndpointID via ContainerList | No (deadlock) | No (not committed) | **Yes** | Yes |
| Container's EndpointID via ContainerInspect | No (deadlock) | No (not committed) | **Yes** | Yes |
| Container's EndpointID via NetworkInspect | No (deadlock) | No (not committed) | **Yes** | Yes |
| Veth peer in host namespace | Yes | Yes | No (moved to container) | No |
| Veth peer in container namespace | No | No | **Yes** | Yes |
| Container PID (for /proc ns access) | Sometimes | Sometimes | **Yes** | Yes |

### Implications for MAC address management

- MAC must be set on the veth peer during CreateEndpoint (host namespace)
- If MAC needs correction, it can ONLY be corrected:
  - At Join time: on host-side veth peer (but container identification impossible)
  - Post-Join: on container-side veth via namespace-aware netlink handle (container
    identification possible via EndpointID)
- `docker inspect` MacAddress field reflects what CreateEndpoint returned, NOT
  post-Join corrections. The actual interface MAC inside the container IS correct.

---

## 5. Compose Lifecycle Sequences

### `docker compose up` (fresh start)

```
For each service (concurrently):
  1. container.create event
  2. CreateEndpoint          ← concurrent across services
  3. Join                    ← concurrent across services
  4. container.start event
```

### `docker compose down`

```
For each service (concurrently):
  1. container.die event     ← plugin sees this
  2. Leave
  3. DeleteEndpoint
  4. container.destroy event
```

### `docker compose down && docker compose up`

The die events from `down` push backward-looking entries into the FIFO. The
create events from `up` MUST flush these stale entries. Without flush logic,
the FIFO will contain compose-down names that poison compose-up CreateEndpoint
calls.

### `docker restart <container>`

```
1. Leave                     ← plugin pre-populates FIFO from cache
2. DeleteEndpoint
3. CreateEndpoint            ← pops Leave-provided entry from FIFO
4. Join
5. container.start event
```

This is reliable because Leave and CreateEndpoint are for the SAME container
and execute sequentially.

---

## 6. Veth Pair Lifecycle

```
CreateEndpoint:
  ├─ netlink.LinkAdd(veth)        ← both ends in host namespace
  ├─ Set MAC on peer              ← host namespace
  ├─ Bring up both ends           ← host namespace
  ├─ Attach host end to bridge    ← host namespace
  └─ Run initial DHCP on peer     ← host namespace (peer not yet moved)

Join:
  └─ Return InterfaceName{SrcName: peerName, DstPrefix: "eth"}
     ← tells Docker which interface to move and what to rename it

[Docker moves peer into container namespace, renames to eth0]

Post-Join goroutine:
  ├─ m.Start() resolves container namespace
  ├─ m.ctrLink found via VethPeerIndex in container namespace
  └─ MAC correction via m.netHandle.LinkSetHardwareAddr(m.ctrLink, addr)
     ← namespace-aware netlink handle required
```

---

## 7. Anti-Patterns (Things That Do NOT Work)

### Do NOT attempt container identification at Join time via ContainerList
Docker has not committed the endpoint association. Scanning all containers for
EndpointID match returns zero results. Verified across 18 consecutive Join calls.

### Do NOT push start events to the FIFO queue
Start events fire AFTER CreateEndpoint. Pushing them to the FIFO re-appends
names that were already popped, causing the NEXT CreateEndpoint to get a stale
name from a previous container.

### Do NOT call findInNetwork during CreateEndpoint
Docker is blocked waiting for CreateEndpoint to return. The endpoint is not yet
visible in Docker's network state. This is a deadlock, not a race.

### Do NOT assume FIFO order matches CreateEndpoint order
Under concurrency, Docker's goroutine scheduler determines CreateEndpoint order
independently of event delivery order. The FIFO is best-effort; post-Join
correction is the authoritative mechanism.

### Do NOT rely on `docker inspect` MacAddress for verification
The MacAddress field in Docker's API reflects what CreateEndpoint returned.
Post-Join corrections change the actual interface MAC but Docker doesn't know
about it. Verify MACs by reading `/sys/class/net/eth0/address` inside the
container or via `ip link` in the container namespace.

---

## 8. Known Limitations & Future Work

### Docker metadata MacAddress drift after post-Join correction

**Problem**: After post-Join MAC correction, `docker inspect` reports the stale
MAC from CreateEndpoint while the actual interface inside the container has the
correct MAC. This is cosmetic (DHCP, ARP, and network traffic all use the real
interface MAC) but confusing for operators and breaks any tooling that reads
MacAddress from the Docker API.

**Root cause**: The Docker network plugin v2 API has no `UpdateEndpoint` or
equivalent. The only plugin API calls are:

| API call | Direction | Can update MAC? |
|----------|-----------|-----------------|
| `CreateEndpoint` response | Plugin -> Docker | Yes (initial assignment only) |
| `EndpointOperInfo` response | Plugin -> Docker | No (informational key-value, Docker ignores for MacAddress) |
| `Join` response | Plugin -> Docker | No (only InterfaceName, Gateway, StaticRoutes) |

**Potential approaches to investigate**:

1. **Omit MAC from CreateEndpoint response**: If `Interface.MacAddress` is empty
   (omitempty), Docker may read the MAC directly from the veth interface after
   sandbox setup. If so, the post-Join correction would already be applied by the
   time Docker reads it. **Risk**: Docker may read the veth MAC immediately after
   CreateEndpoint returns (before Join, before correction) or may assign a random
   MAC if the driver doesn't provide one. Needs libnetwork source verification.

2. **Docker libnetwork source audit**: Trace how `ep.Interface().MacAddress` is
   set and whether there is any post-creation update path. Docker's libnetwork
   code is in `github.com/moby/moby/libnetwork`. The relevant function is
   `ep.sbJoin()` which calls the driver's Join and then updates sandbox config.
   Check if MacAddress is re-read from the interface after Join.

3. **Upstream Docker PR**: Propose an `UpdateEndpoint` API extension to the
   network plugin v2 spec. This would allow drivers to push corrected metadata
   (MAC, IP) back to Docker after initial creation. Low likelihood of acceptance
   given Docker's stability posture on plugin APIs.

4. **Docker API side-channel**: Use the Docker Engine API (`POST /containers/{id}/update`
   or `POST /networks/{id}/disconnect` + `POST /networks/{id}/connect`) to force
   Docker to re-read the interface. Disconnecting and reconnecting is destructive
   (kills the veth, drops the IP). `ContainerUpdate` only handles resource limits,
   not network settings. Not viable without data loss.

**Current status**: Accepted limitation. Operators should verify MACs via
`docker exec <ctr> cat /sys/class/net/eth0/address`, not `docker inspect`.

---

## 9. Verification Commands

```bash
# Check actual MAC inside container (authoritative)
docker exec <container> cat /sys/class/net/eth0/address

# Check Docker's recorded MAC (may be stale after post-Join correction)
docker inspect --format '{{range .NetworkSettings.Networks}}{{.MacAddress}}{{end}}' <container>

# Check EndpointID mapping
docker inspect --format '{{range $n,$s := .NetworkSettings.Networks}}{{$n}}: {{$s.EndpointID}}{{end}}' <container>

# Plugin logs (inside plugin rootfs)
sudo cat /var/lib/docker/plugins/<plugin-id>/rootfs/var/log/net-dhcp.log

# Verify plugin is running
sudo ctr --namespace plugins.moby tasks list
```
