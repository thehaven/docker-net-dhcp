## Task 1.1 — Write unit test verifying setupClient terminates when netns is missing

**Goal:** Provide a failing/regression test confirming that `dhcpManager.setupClient` halts when the network namespace path does not exist.
**Files:** `pkg/plugin/dhcp_manager_test.go`
**Steps:**
1. Create a test `TestDHCPManager_MissingNetNSBailsOut` with a non-existent `SandboxKey`.
2. Verify `setupClient` returns or cleans up without spawning endless retry goroutines.
**Verify:** `go test -v -run TestDHCPManager_MissingNetNSBailsOut ./pkg/plugin/...`
**Commit:** `test(plugin): add regression test for missing netns bailout in setupClient`

---

## Task 1.2 — Write unit test verifying DeleteEndpoint cleans up persistentDHCP and cache

**Goal:** Ensure `DeleteEndpoint` stops the endpoint's `dhcpManager` and removes it from `p.cache`.
**Files:** `pkg/plugin/pending_test.go`
**Steps:**
1. Populate `p.persistentDHCP` and `p.cache` with an endpoint.
2. Call `p.DeleteEndpoint`.
3. Assert that `p.persistentDHCP` does not contain the endpoint and `p.cache.GetEndpoint` returns false.
**Verify:** `go test -v -run TestDeleteEndpoint_Cleanup ./pkg/plugin/...`
**Commit:** `test(plugin): add regression test for DeleteEndpoint cache and manager cleanup`

---

## Task 1.3 — Write unit test verifying cache.Reconcile prunes orphaned endpoints

**Goal:** Verify `Reconcile` removes cached endpoints that no longer exist in Docker's container list for that network.
**Files:** `pkg/plugin/cache_test.go`
**Steps:**
1. Populate `NetworkCache` with active and orphaned endpoints.
2. Call `Reconcile` with a mock Docker client returning only active containers.
3. Assert that orphaned endpoints are pruned from `NetworkState.Endpoints`.
**Verify:** `go test -v -run TestCache_ReconcilePrunesOrphanedEndpoints ./pkg/plugin/...`
**Commit:** `test(cache): add regression test for pruning orphaned endpoints during reconcile`

---

## Task 2.1 — Add netns existence check in dhcpManager.setupClient

**Goal:** Prevent `setupClient` from executing `nsenter` when `m.nsPath` does not exist on disk.
**Files:** `pkg/plugin/dhcp_manager.go`
**Steps:**
1. Check `os.Stat(m.nsPath)` in `setupClient` before creating and starting the client.
2. If `os.IsNotExist(err)` and `m.nsPath` starts with `/var/run/docker/netns/` or `/proc/`, stop and exit without retrying.
**Verify:** `go test -v -run TestDHCPManager ./pkg/plugin/...`
**Commit:** `fix(plugin): check netns existence before launching udhcpc supervisor`

---

## Task 2.2 — Terminate retry loop on deleted netns or unrecoverable failure

**Goal:** Break the `setupClient` supervisor retry loop when the network namespace is deleted during execution.
**Files:** `pkg/plugin/dhcp_manager.go`
**Steps:**
1. Inside the retry loop in `setupClient`, check whether `m.nsPath` still exists.
2. If the netns file was deleted or cannot be opened, log an info message, call `m.Stop()`, and terminate the goroutine.
**Verify:** `go test -v -run TestDHCPManager ./pkg/plugin/...`
**Commit:** `fix(plugin): terminate dhcpManager retry loop when container netns disappears`

---

## Task 3.1 — Update DeleteEndpoint to clean up cache and stop persistentDHCP

**Goal:** Ensure every `DeleteEndpoint` API call stops running DHCP managers and purges the endpoint from cache.
**Files:** `pkg/plugin/network.go`
**Steps:**
1. In `DeleteEndpoint`, acquire `p.Lock()`, check `p.persistentDHCP[r.EndpointID]`, delete it, and call `manager.Stop()`.
2. Call `p.cache.DeleteEndpoint(r.NetworkID, r.EndpointID)`.
**Verify:** `go test -v -run TestDeleteEndpoint ./pkg/plugin/...`
**Commit:** `fix(plugin): purge endpoint cache and stop dhcpManager on DeleteEndpoint`

---

## Task 3.2 — Update Reconcile to prune orphaned endpoints from cache

**Goal:** Periodically remove endpoints from `networks.json` when the container is no longer attached in Docker.
**Files:** `pkg/plugin/cache.go`
**Steps:**
1. In `Reconcile`, compare each endpoint in `existing.Endpoints` against `n.Containers`.
2. Delete endpoints whose corresponding container no longer exists in `n.Containers`.
3. Save cache when endpoints are pruned.
**Verify:** `go test -v -run TestCache_Reconcile ./pkg/plugin/...`
**Commit:** `fix(cache): reconcile and prune orphaned endpoints against active containers`

---

## Task 3.3 — Update Recover to validate netns existence before resumeDHCP

**Goal:** Avoid launching DHCP managers for dead endpoints during warm plugin recovery.
**Files:** `pkg/plugin/network.go`
**Steps:**
1. In `resumeDHCP` / `Recover`, check if the endpoint's `SandboxKey` netns path exists on the host.
2. If not, log debug info and delete the stale endpoint from cache instead of starting `resumeDHCP`.
**Verify:** `go test -v ./pkg/plugin/...`
**Commit:** `fix(plugin): skip and clean up dead endpoints during warm recovery`

---

## Task 4.1 — Integrate lumberjack log rotation with compression

**Goal:** Bound the plugin log file size with automatic gzip compression and retention limits.
**Files:** `cmd/net-dhcp/main.go`, `go.mod`, `go.sum`
**Steps:**
1. Configure `lumberjack.Logger` as `log.StandardLogger().Out`.
2. Configure `MaxSize` (MB), `MaxBackups`, `MaxAge` (days), and `Compress: true`.
**Verify:** `go test ./...`
**Commit:** `feat(logging): integrate lumberjack log rotation with gzip compression`

---

## Task 4.2 — Support CLI flags and environment variables for log rotation

**Goal:** Allow configuring log rotation parameters via command-line flags and environment variables.
**Files:** `cmd/net-dhcp/main.go`
**Steps:**
1. Add flags `-log-max-size`, `-log-max-backups`, `-log-max-age`, `-log-compress`.
2. Read fallback values from `LOG_MAX_SIZE`, `LOG_MAX_BACKUPS`, `LOG_MAX_AGE`, `LOG_COMPRESS`.
**Verify:** `go test ./...`
**Commit:** `feat(logging): add CLI flags and environment variables for log rotation`

---

## Task 5.1 — Run race-enabled test suite

**Goal:** Verify all unit tests pass with zero race conditions.
**Files:** All
**Steps:**
1. Run `go test -race ./...`.
**Verify:** `go test -race ./...`
**Commit:** `test: verify test suite passes cleanly with race detector`

---

## Task 5.2 — Validate static analysis

**Goal:** Ensure `go vet` is clean.
**Files:** All
**Steps:**
1. Run `go vet ./...`.
**Verify:** `go vet ./...`
**Commit:** `chore: verify go vet cleanliness`

---

## Task 5.3 — Validate binary build with Go 1.26.1 CGO_ENABLED=0

**Goal:** Confirm production binary compiles without error using the required toolchain.
**Files:** `cmd/net-dhcp`
**Steps:**
1. Build with `GOROOT=~/.local/share/mise/installs/go/1.26.1 CGO_ENABLED=0 ...`.
**Verify:** `~/.local/share/mise/installs/go/1.26.1/bin/go build -o /tmp/net-dhcp-test ./cmd/net-dhcp`
**Commit:** `build: confirm clean binary build with go1.26.1`
