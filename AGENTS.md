# docker-net-dhcp - Agent Notes

## Required Reading

Before modifying network plugin code (`network.go`, `plugin.go`, `dhcp_manager.go`, `cache.go`),
invoke the `/docker-net-dhcp` skill or read these specs:

- `docs/DOCKER-NETWORK-PLUGIN-API.md` — Empirical Docker API behavioral constraints (visibility
  windows, lifecycle ordering, anti-patterns). This is ground truth from instrumented testing.
- `docs/DETERMINISTIC-MAC-SPEC.md` — Deterministic MAC resolution design and data structures.

## Invariants — DO NOT BREAK THESE

These guardrails exist because each was broken at least once and caused production failures.

### 1. `Reap()` must NEVER send signals

`pkg/udhcpc/client.go`: `Reap()` is the wait-only companion to `Release()` (which sends SIGTERM
for DHCPRELEASE). `Reap()` is called by `GetIP()` on Once-mode processes — if it sends any signal,
the transient DHCP probe is killed before returning lease info, and new containers silently fail
to acquire IPs. This was broken on 2026-07-21.

**Enforcement**: `TestReapDoesNotSignal` and `TestGetIPPipeline` validate this contract.

### 2. `GetIP()` event reader must synchronise with `Reap()`

`GetIP()` uses `range events` (not `select` with a done channel) and waits on `readerDone` after
`Reap()` returns. This guarantees the "bound" event is consumed before `info` is checked. A
previous `select`/`done`-channel design had a scheduling race where `close(done)` could fire
before the reader goroutine processed the event, causing `ErrNoLease` with fast-exiting processes.

### 3. `Start()` uses buffered event channel

`events := make(chan Event, 1)` (not unbuffered). Prevents the scanner goroutine from blocking
on send before the caller's reader goroutine is scheduled. Without the buffer, the scanner
blocks and the caller's goroutine can race past it to `Reap()`.

### 4. Post-Join MAC correction must respect user-specified MACs

`pkg/plugin/plugin.go`: `joinHint.UserSpecifiedMAC` is set when `r.Interface.MacAddress != ""`
in `CreateEndpoint`. In the post-Join goroutine (`network.go`), MAC correction is skipped when
this flag is true — the user explicitly chose a MAC and it must not be overridden. Broken on
2026-07-21: post-Join correction unconditionally replaced user-specified MACs after FIFO
misidentification.

**Enforcement**: `TestJoinHint_UserSpecifiedMAC` in `pending_test.go`.

### 5. Pre-deploy checklist (mandatory)

Before copying the binary to the plugin rootfs:

```bash
go vet ./...           # must be clean
go test -race ./...    # all tests must pass
```

The binary MUST be built with go1.26.1 and CGO_ENABLED=0 (see Build section).

## Build

- Build requires `CGO_ENABLED=0` for Alpine compatibility (plugin runs in Alpine container)
- The plugin binary MUST be built with **go1.26.1** (matching the production binary). Building with go1.24 produces a binary that crash-loops inside the Alpine plugin container. Use: `GOROOT=~/.local/share/mise/installs/go/1.26.1 GOPATH=/tmp/go126-path HOME=/tmp/go126-home CGO_ENABLED=0 ~/.local/share/mise/installs/go/1.26.1/bin/go build -o net-dhcp ./cmd/net-dhcp`
- Tests can be run with the system Go version: `go test ./pkg/...`

## Deploy

1. `docker plugin disable ghcr.io/devplayer0/docker-net-dhcp:golang -f`
2. `sudo cp net-dhcp /var/lib/docker/plugins/f094d0913c17.../rootfs/usr/sbin/net-dhcp`
3. `docker plugin enable ghcr.io/devplayer0/docker-net-dhcp:golang`
4. Verify: `sudo ctr --namespace plugins.moby tasks list` should show RUNNING

Plugin rootfs: `/var/lib/docker/plugins/f094d0913c1775bb54da0b8a5dbc06ccfaaab7bb37464b2550a4d2ef59f5369f/rootfs/`
Plugin log: `$ROOTFS/var/log/net-dhcp.log`
Cache file: `/var/lib/docker-net-dhcp/networks.json`

## Architecture

- Docker's `CreateEndpoint` API does NOT include container ID or name
- Docker does NOT expose EndpointID in ContainerList/Inspect until AFTER Join returns (chicken-and-egg)
- Container name resolution at CreateEndpoint uses a multi-layer fallback:
  1. Per-network pending queue (FIFO, populated by create events + Leave pre-population)
  2. Deep fallback (list all containers, filter by network + empty endpointID, eliminate via joinHints)
  3. EndpointID seed (last resort, produces non-matching MAC)
- FIFO ordering does NOT match CreateEndpoint call order (~50% hit rate with concurrent containers)
- Post-Join MAC correction (in goroutine after m.Start): ContainerList can NOW match EndpointID to the
  actual container; if FIFO was wrong, the correct MAC is set on the container veth via netHandle before
  the DHCP client starts. This is the authoritative correction mechanism.
- CreateEndpoint calls are serialized with `createMu` to enable process-of-elimination
- The deep fallback refuses to guess when multiple candidates remain (safer than misidentification)
- SeedName is persisted in EndpointState for Leave pre-population across restarts
- Start events must NOT push to FIFO (they arrive during CreateEndpoint and disrupt ordering by
  re-appending already-popped names); they only upsert the metadata map

## Future Work

- **Fix `docker inspect` MacAddress drift**: After post-Join MAC correction, `docker inspect`
  still reports the stale MAC from CreateEndpoint. The plugin v2 API has no `UpdateEndpoint`.
  Investigate: (1) omitting MAC from CreateEndpoint response so Docker reads from interface
  after sandbox setup, (2) libnetwork source audit for post-creation MAC update paths,
  (3) upstream Docker PR for `UpdateEndpoint`. See `docs/DOCKER-NETWORK-PLUGIN-API.md` section 8
  for full analysis.
