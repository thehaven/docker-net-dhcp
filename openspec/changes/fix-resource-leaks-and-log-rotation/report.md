## Symptom

`net-dhcp` plugin consumes ~386% CPU, leaks 573 GB RSS memory, and writes an unrotated 68.8 GB log file due to dead endpoint accumulation and an unbounded 5-second respawn retry loop.

## Environment

- **OS**: Linux 6.x x86_64
- **Docker Engine**: Docker 28.x with Docker network plugin v2 (`ghcr.io/devplayer0/docker-net-dhcp:golang` / `ghcr.io/thehaven/docker-net-dhcp`)
- **Plugin Runtime**: Go 1.24+ binary running in containerized plugin rootfs
- **Network Mode**: DHCP plugin network bridge (`vlan107` / `fd5a5429...`)

## Reproduction Steps

1. Start `net-dhcp` managing a Docker network (e.g. `vlan107`).
2. Run transient or ephemeral containers over time (e.g., CI runners such as GitLab runner containers, `docker compose up`/`down`, or stopped containers).
3. Containers exit or get removed with `docker rm` / `docker rm -f`.
4. Observe that endpoints remain in `/var/lib/docker-net-dhcp/networks.json` and in `p.persistentDHCP`.
5. Observe `setupClient` in `pkg/plugin/dhcp_manager.go` attempting to spawn `nsenter --net=/var/run/docker/netns/<deleted-key> udhcpc ...` every 5 seconds per dead container.
6. Over days/weeks, CPU hits ~400%, RSS climbs to hundreds of gigabytes, and `net-dhcp.log` grows unboundedly to tens of gigabytes.

## Expected vs Actual

**Expected:**
- Stale/dead endpoints are cleanly pruned upon container exit or network/endpoint deletion (`DeleteEndpoint`, cache reconciliation).
- `setupClient` detects when the container's network namespace no longer exists and halts the retry loop.
- The plugin logs are bounded by automatic rotation, compression (`.gz`), maximum backup count, and maximum file size limits.
- Process CPU usage remains near 0% when idle and memory RSS stays under 50 MB.

**Actual:**
- `DeleteEndpoint` only removes host veth links; it never deletes endpoints from the persistent cache and never stops `dhcpManager`.
- Cache reconciliation (`Reconcile`) never prunes orphaned endpoints from `NetworkState.Endpoints`.
- `setupClient` enters an infinite loop for 1,045+ dead containers, spawning `nsenter` and pipe goroutines every 5 seconds.
- CPU usage is ~386%, memory usage is ~573 GB RSS, and log file size reaches 68.8 GB.
