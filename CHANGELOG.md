# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.1] - 2026-03-17

### Fixed

- **DHCP lease stability**: Removed the `-R` flag from background `udhcpc` clients to prevent lease release on plugin stop/restart.
- **Incomplete DHCP event handling**: Updated `dhcpManager` to process `bound` events during renewal cycles and implemented logging for `deconfig`, `leasefail`, and `nak` events.
- **DHCP failure visibility**: Updated `udhcpc-handler` to encode failure events as JSON for plugin consumption.
- **Improved safety**: Added nil checks for network handles during IP renewal to prevent panics during partial failures.

## [1.0.0] - 2026-03-17

Fork of [devplayer0/docker-net-dhcp](https://github.com/devplayer0/docker-net-dhcp) updated and hardened for production use.

### Added

- **Deterministic MAC address generation** (`pkg/macgen`): every container without an explicit `--mac-address` receives a stable MAC derived from its name via `md5(name)[0:5]` prefixed with `02`. The same container name always produces the same MAC, enabling DHCP IP reservations without manual `--mac-address` flags.
- **`mac_format` network option**: MAC addresses can be formatted as `colon` (default, `02:xx:xx:xx:xx:xx`), `hyphen` (`02-xx-xx-xx-xx-xx`), or `dot` (`02xx.xxxx.xxxx`).
- **`docker-net-dhcp-macgen` CLI tool**: offline calculation of deterministic MACs for use in DHCP reservation scripts.
- **IPv6 deterministic DUID-LL**: stable `DUID-LL` generation from the container MAC for consistent IPv6 address assignment.
- **Persistent network cache** (`/var/lib/docker-net-dhcp/networks.json`): active network and endpoint state survives plugin restarts. On re-enable the plugin re-adopts existing containers and resumes DHCP renewals without requiring a container restart.
- **Docker event listener**: the plugin listens for container creation events and queues container names before `CreateEndpoint` is called, ensuring 100% reliable deterministic MACs even under load.
- **DNS hostname registration**: the container name is sent as the DHCP hostname (Options 12 and 81), so containers appear in DNS automatically when the DHCP server is configured to update DNS from leases. Pass `--hostname` to use a different name.
- **Per-network FIFO pending queue**: container-name entries are stored per-network and consumed in FIFO order, preventing any stale entry from being assigned to the wrong container.
- **`/health` HTTP endpoint**: returns plugin status for external monitoring.
- **`make test`** (unit tests with race detection) and **`make verify`** (go vet) build targets.
- **Multi-arch CI/CD**: GitHub Actions builds `amd64` and `arm64` images and publishes them to GHCR on every tagged release.

### Changed

- **Module path** migrated to `github.com/thehaven/docker-net-dhcp`.
- **Docker SDK** updated to v28 (latest stable); Go toolchain updated to 1.24.
- **Default lease timeout** increased from 10 s to 30 s for reliability on slower DHCP servers.
- **Container interface naming**: containers now consistently receive `eth0` (and `eth1`, etc. for multi-network) inside the namespace.
- **Cache reconciliation interval** set to 5 minutes (avoids log noise from the previous 10-second polling interval).
- **`udhcpc` execution** moved into the container network namespace via direct namespace handle, removing the dependency on `nsenter` and `util-linux`.
- **Thread safety**: all shared plugin state protected with `sync.RWMutex`.

### Fixed

- Static-MAC containers (`--mac-address`) now correctly consume their pending queue entry, preventing their container name from being used as the MAC seed for the *next* dynamic-MAC container on the same network.
- Default Docker hostname (short container ID) no longer propagated to DHCP; the container name is used instead, so DNS registers as `<name>.<domain>` rather than `<id>.<domain>`.
- `IsDHCPPlugin` now matches both `ghcr.io/thehaven/docker-net-dhcp` and `ghcr.io/devplayer0/docker-net-dhcp` driver strings, preventing existing networks from being ghost-pruned from the cache on plugin restart.
- Resolved long-standing startup deadlocks caused by circular dependencies between the plugin and the Docker daemon during container auto-restart.
- Corrected Docker API version negotiation errors on modern Docker Engine installations.

### Removed

- Removed the `mac_seed_source` network option (was documented but never functional).
- Removed synchronous blocking calls to `NetworkInspect` from the `CreateEndpoint` hot path.
- Removed dead `endpointWatchdog` goroutine.
- Removed dead Join-time MAC override code path.
