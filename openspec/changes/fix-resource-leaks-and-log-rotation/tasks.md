## 1. Regression Testing

- [x] 1.1 Write unit test in `pkg/plugin/dhcp_manager_test.go` verifying `setupClient` terminates when the network namespace does not exist [unit] [spec: Non-Existent Network Namespace Bailout in DHCP Manager]
- [x] 1.2 Write unit test in `pkg/plugin/pending_test.go` verifying `DeleteEndpoint` cleans up `persistentDHCP` and prunes `p.cache` [unit] [spec: Endpoint Resource Cleanup on Termination and Deletion]
- [x] 1.3 Write unit test in `pkg/plugin/cache_test.go` verifying `cache.Reconcile` prunes orphaned endpoints [unit] [spec: Automatic Cache Reconciliation for Dead Endpoints]

## 2. DHCP Manager & Netns Bailout

- [x] 2.1 Add netns existence check (`os.Stat`) and bailout condition in `dhcpManager.setupClient` [spec: Non-Existent Network Namespace Bailout in DHCP Manager]
- [x] 2.2 Terminate retry loop when `nsenter` fails with `ENOENT` or when container netns is removed [spec: Non-Existent Network Namespace Bailout in DHCP Manager]

## 3. Endpoint Lifecycle & Cache Reconciliation

- [x] 3.1 Update `DeleteEndpoint` in `pkg/plugin/network.go` to stop active `persistentDHCP` managers and remove endpoints from `cache` [spec: Endpoint Resource Cleanup on Termination and Deletion]
- [x] 3.2 Update `Reconcile` in `pkg/plugin/cache.go` to prune dead endpoints from `NetworkState.Endpoints` when they no longer exist in Docker's network container list [spec: Automatic Cache Reconciliation for Dead Endpoints]
- [x] 3.3 Update `Recover` in `pkg/plugin/network.go` to validate netns existence before spawning `resumeDHCP` managers [spec: Endpoint Resource Cleanup on Termination and Deletion]

## 4. Log Rotation, Compression, and Maximum Log Controls

- [x] 4.1 Integrate `lumberjack.Logger` into `cmd/net-dhcp/main.go` for log rotation and gzip compression [spec: Bounded Log Rotation, Compression, and Retention]
- [x] 4.2 Support CLI flags and environment variables for `max_size` (MB), `max_backups`, `max_age` (days), and `compress` (boolean) [spec: Bounded Log Rotation, Compression, and Retention]

## 5. Build and Verification

- [x] 5.1 Run `go test -race ./...` and verify all tests pass cleanly [unit]
- [x] 5.2 Validate static analysis with `go vet ./...` [unit]
- [x] 5.3 Validate build with Go 1.26.1 and `CGO_ENABLED=0` [smoke]
