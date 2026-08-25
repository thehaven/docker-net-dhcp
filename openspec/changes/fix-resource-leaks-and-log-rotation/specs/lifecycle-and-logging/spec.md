# Lifecycle and Logging Specifications

## ADDED Requirements

### Requirement: Endpoint Resource Cleanup on Termination and Deletion
The network plugin MUST clean up all in-memory persistent DHCP managers and MUST remove cached endpoint states from persistent storage when an endpoint is deleted via `DeleteEndpoint` or when a container leaves a network.

#### Scenario: Docker invokes DeleteEndpoint for a removed container
- **GIVEN** an active container endpoint tracked in `p.persistentDHCP` and `p.cache`
- **WHEN** Docker issues a `DeleteEndpoint` request for that endpoint ID
- **THEN** the plugin MUST stop the endpoint's `dhcpManager`, free associated netlink and netns handles, remove it from `p.persistentDHCP`, and delete it from `p.cache`.

### Requirement: Non-Existent Network Namespace Bailout in DHCP Manager
The DHCP client supervisor in `dhcpManager.setupClient` MUST inspect the container network namespace path before and during retries. If the network namespace no longer exists or if `nsenter` fails with a non-recoverable error (e.g. `ENOENT`), the supervisor MUST terminate immediately rather than looping forever.

#### Scenario: Container network namespace is deleted while DHCP client is running
- **GIVEN** a running DHCP manager supervising `udhcpc`
- **WHEN** the container stops and its `/var/run/docker/netns/<sandboxKey>` file is deleted
- **THEN** `setupClient` MUST detect that the netns file does not exist, log an informational notice, clean up resources, and exit the goroutine without retrying.

### Requirement: Automatic Cache Reconciliation for Dead Endpoints
The cache reconciliation loop MUST periodically cross-reference all cached endpoints in `NetworkState.Endpoints` against active containers returned by Docker daemon network inspection and MUST remove orphaned entries.

#### Scenario: Cache reconciler encounters dead endpoints in networks.json
- **GIVEN** `networks.json` contains endpoint entries for containers that no longer exist in Docker
- **WHEN** `cache.Reconcile` runs (at startup or during the periodic loop)
- **THEN** orphaned endpoints not present in the Docker network's container list MUST be pruned from `networks.json` and in-memory cache.

### Requirement: Bounded Log Rotation, Compression, and Retention
The plugin logging subsystem MUST write to a rotating log sink that enforces maximum file size, gzip compression for archived logs, maximum retained backup count, and maximum retention age.

#### Scenario: Log file reaches maximum size threshold
- **GIVEN** `net-dhcp` is writing logs to a file destination (e.g., `/var/log/net-dhcp.log`)
- **WHEN** the active log file exceeds `MaxSize` (default 50 MB)
- **THEN** the active log file MUST be rotated, compressed with gzip (`.gz`), and old archives exceeding `MaxBackups` (default 5) or `MaxAge` (default 14 days) MUST be automatically deleted.
