## Hypothesis

The excessive CPU utilization, multi-hundred-gigabyte memory consumption, and disk bloat are caused by the accumulation of dead container endpoints that remain indefinitely in `networks.json` and memory, triggering thousands of failing `nsenter` retry loops per minute against deleted network namespaces, compounded by unrotated trace logging.

## Evidence

1. **System Process Statistics**:
   ```
       PID USER     %CPU %MEM    VSZ        RSS        STAT TIME         COMMAND
    405903 root      386 53.8    4552614724 573020288  Ssl  66-01:37:17  /usr/sbin/net-dhcp
   ```
   - Total OS threads: 329
   - RSS: ~573 GB (53.8% of 1.0 TiB host RAM)
   - Continuous CPU burn: 386% across 4 cores

2. **Endpoint Cache Analysis**:
   - Total endpoints in `/var/lib/docker-net-dhcp/networks.json`: 1,121
   - Active containers on Docker network: 76
   - Dead / missing network namespaces: **1,045**

3. **Plugin Log Output (`/var/log/net-dhcp.log` - 68.8 GB)**:
   ```
   time="2026-08-25T16:40:04Z" level=trace msg="new udhcpc client" cmd="/usr/bin/nsenter --net=/var/run/docker/netns/aeec54227d3d udhcpc ..."
   time="2026-08-25T16:40:04Z" level=debug msg="nsenter: cannot open /var/run/docker/netns/aeec54227d3d: No such file or directory"
   time="2026-08-25T16:40:04Z" level=warning msg="DHCP client pipe closed (udhcpc exited)" endpoint=7342fed8eafe is_ipv6=false
   time="2026-08-25T16:40:04Z" level=warning msg="DHCP client exited unexpectedly; restarting in 5 s" endpoint=7342fed8eafe is_ipv6=false
   ```

4. **Codebase Flaws**:
   - `pkg/plugin/network.go:429` (`DeleteEndpoint`): Deletes host veth but does NOT remove the endpoint from `p.cache` and does NOT invoke `manager.Stop()` on `p.persistentDHCP[r.EndpointID]`.
   - `pkg/plugin/cache.go:278` (`Reconcile`): Reconciles network IDs against Docker, but never purges dead endpoints from `NetworkState.Endpoints`.
   - `pkg/plugin/dhcp_manager.go:141-185` (`setupClient`): Unconditionally retries `udhcpc` via `nsenter` every 5 seconds forever, even when `/var/run/docker/netns/<id>` returns `ENOENT`.
   - `pkg/udhcpc/client.go:79`: Each retry creates a new `io.Copy` goroutine on stderr, runtime timers, and heap allocations.
   - `cmd/net-dhcp/main.go:37`: Log file is opened directly with `os.OpenFile(..., O_APPEND)` with no rotation, compression, or size bounding.

## Root Cause

The plugin fails to prune terminated container endpoints from memory (`persistentDHCP`) and disk cache (`networks.json`), while running an unbounded retry loop in `setupClient` that repeatedly forks `nsenter` against deleted network namespaces every 5 seconds, resulting in massive goroutine/timer churn, allocator heap expansion, and unbounded uncompressed log generation.

## Blast Radius

- **Active container DHCP renewals**: `renew()` and lease maintenance for genuinely running containers must remain completely uninterrupted.
- **Deterministic MAC resolution**: `Leave` pre-population and MAC generation for restarting containers (`docker restart` / compose up/down) must not be corrupted when dead endpoints are pruned.
- **Plugin startup recovery**: `Recover()` must safely filter out dead endpoints during warm startup instead of launching defunct managers.
- **Log consumers**: Log paths remain `/var/log/net-dhcp.log`, but will be rotated to compressed `.gz` archives with size and count caps.
