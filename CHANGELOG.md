# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-03-15

### Added
- **Deterministic MAC Address Generation**: New `pkg/macgen` library using MD5 hashing of stable seeds (hostname, container name, or fallback hash) to provide predictable MAC addresses for external DHCP reservations. **Enabled by default** for all endpoints without a user-specified MAC address.
- **IPv6 Stability**: Implemented deterministic `DUID-LL` (Link-Layer) generation based on MAC addresses to ensure stable IPv6 address assignment.
- **Network State Cache**: Persistent thread-safe JSON cache for network configurations at `/var/lib/docker-net-dhcp/networks.json`.
- **Endpoint Persistence**: Active endpoints (IPs, SandboxKeys, MACs) are now persisted to the local cache to survive plugin restarts and upgrades.
- **Warm Recovery Logic**: Automated background recovery that re-adopts existing endpoints and restarts `udhcpc` managers upon plugin startup, enabling zero-interruption upgrades.
- **Lazy State Reconciliation**: Background worker to automatically prune "ghost" networks from the local cache 30s after plugin startup.
- **CLI Tool**: `docker-net-dhcp-macgen` for offline calculation of deterministic MACs and DUIDs.
- **Resilient Join Phase**: Added `SandboxKey` path resolution to find network namespaces without calling Docker's `ContainerInspect` API.
- **Quality Gates**: Added `make test` (Unit/Race detection) and `make verify` (Static analysis) to the `Makefile`.
- **Modern CI/CD**: Enhanced GitHub Actions with Go 1.24 environment, automated tests, and static analysis before building multi-arch images.

### Changed
- **Architecture**: Decoupled critical plugin paths (`CreateEndpoint`, `Join`) from the Docker Engine API to eliminate startup deadlocks.
- **Namespace Handling**: Refactored `udhcpc` execution to use `nsenter` for reliable namespace isolation, bypassing Go's thread-scheduling limitations.
- **Docker Client**: Updated to use lazy initialization and automatic API version negotiation.
- **Observability**: Implemented structured logging with request correlation IDs and explicit MAC derivation logging.
- **Configuration**: Updated `config.json` to include mandatory persistent volume mount for the network cache.

### Fixed
- Resolved long-standing startup hangs caused by circular dependencies between the plugin and Docker daemon during container auto-restart policies.
- Fixed Docker API version mismatch errors on modern Docker Engine installations.
- Corrected error handling in `udhcpc-handler` to prevent log-wrapping failures.

### Removed
- Removed synchronous blocking calls to `NetworkInspect` from the `CreateEndpoint` hot path.
- Removed reliance on `runtime.LockOSThread` for namespace transitions in favour of `nsenter`.
