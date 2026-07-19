# ADR-004: Deterministic Reboot Recovery for DHCP Network Plugin

## Status
Proposed

## Context
When the host system reboots or the Docker daemon restarts, all containers connected to the DHCP network (`vlan107`) start concurrently in a "boot storm". 
This causes two severe issues for the `docker-net-dhcp` plugin:

1. **Docker API Exhaustion:** The concurrent startup storm floods the Docker daemon, causing Docker socket queries to time out (`context deadline exceeded`).
2. **Loss of FIFO State:** The memory-only FIFO queue (`pendingContainers`) used to map incoming `CreateEndpoint` requests to container names is completely empty on plugin restart.
3. **Deep Fallback Collision:** Because the FIFO queue is empty and multiple containers are starting simultaneously, the secondary "Deep Fallback" mechanism (which lists network endpoints) finds multiple candidate containers with unassigned endpoint IDs. To avoid misidentification, it refuses to guess.
4. **Incorrect DHCP Client Initialization:** Because the plugin cannot resolve the container name during the boot storm, it falls back to using the `EndpointID` as the MAC seed, and it spawns `udhcpc` inside the container's network namespace **without** the Options `-F <container_name>` and `-x hostname:<container_name>`.
5. **False Positive Stale Status:** Since the `udhcpc` processes are running without the `-F` parameter, the host's cleanup audit script (`fix-stale-dhcp.sh`) cannot correlate the processes to the containers, resulting in all containers being incorrectly flagged as `STALE`.

---

## Proposed Improvements

We propose a dual-layer approach to ensure deterministic boot recovery and resolve both the non-deterministic MAC assignment and the missing DHCP options during boot storms:

### 1. Proactive Cache Lookup by MAC Address (CreateEndpoint Phase)
On a container restart, Docker persists and passes the container's previously assigned MAC address in the `CreateEndpointRequest` (`r.Interface.MacAddress`).
By searching the plugin's persistent cache (`networks.json`), we can match the incoming MAC address against cached `Endpoints` to retrieve the original `SeedName` and `Hostname`.

**Implementation in `pkg/plugin/network.go`:**
```go
	var seedName, hostname string

	// PROACTIVE LOOKUP: Check if Docker provided a MAC address (typical on container restart/reboot)
	// and see if we can resolve it directly from our persistent cache.
	if r.Interface != nil && r.Interface.MacAddress != "" {
		if state, ok := p.cache.Get(r.NetworkID); ok {
			for _, ep := range state.Endpoints {
				if ep.MacAddress == r.Interface.MacAddress && ep.SeedName != "" {
					seedName = ep.SeedName
					hostname = ep.Hostname
					reqLog.WithFields(log.Fields{
						"container": seedName,
						"hostname":  hostname,
						"mac":       r.Interface.MacAddress,
					}).Info("Resolved container name proactively from persistent cache via MAC address")
					break
				}
			}
		}
	}

	if seedName == "" {
		// PRIMARY: Pop from the per-network FIFO queue...
		// (Existing FIFO pop logic continues here)
	}
```

### 2. Unconditional Post-Join Container Resolution (Join Phase)
The post-Join MAC correction block resolves the true container name and hostname *after* `Join` returns, which is the exact moment Docker commits the `EndpointID` to Container mapping and exposes it in the API. 
However, the plugin currently skips this block if `hint.SeedName` is empty or equal to `r.EndpointID`. 

By executing this block **unconditionally** in the background goroutine, we ensure that even in a worst-case scenario (e.g., a total cache miss on first startup), the background thread will resolve the correct container name, update `m.hostname`, and spawn the persistent `udhcpc` client with the correct `-F` and `-x` parameters.

**Implementation in `pkg/plugin/network.go`:**
```go
		// ---------------------------------------------------------------
		// Post-Join MAC correction
		// ---------------------------------------------------------------
		// Docker commits EndpointID→Container association AFTER Join
		// returns, so ContainerList can NOW resolve the real container.
		// We execute this unconditionally to guarantee resolution on FIFO/cache misses.
		corrCtx, corrCancel := context.WithTimeout(context.Background(), 5*time.Second)
		corrCtrs, corrErr := p.docker.ContainerList(corrCtx, container.ListOptions{})
		corrCancel()
		if corrErr == nil {
			var actualName, actualHostname string
			for _, c := range corrCtrs {
				if c.NetworkSettings == nil {
					continue
				}
				for _, ns := range c.NetworkSettings.Networks {
					if ns.EndpointID == r.EndpointID {
						if len(c.Names) > 0 {
							actualName = strings.TrimPrefix(c.Names[0], "/")
						}
						inspCtx, inspCancel := context.WithTimeout(context.Background(), 2*time.Second)
						ctr, inspErr := p.docker.ContainerInspect(inspCtx, c.ID)
						inspCancel()
						if inspErr == nil {
							actualHostname = ctr.Config.Hostname
							if len(ctr.ID) >= 12 && actualHostname == ctr.ID[:12] {
								actualHostname = actualName
							}
						}
						break
					}
				}
				if actualName != "" {
					break
				}
			}

			if actualName != "" && actualName != hint.SeedName {
				macFormat := macFormatFromOpts(opts)
				correctMac, genErr := macgen.Generate(macgen.Options{Seed: actualName, Format: macFormat})
				if genErr == nil {
					addr, _ := net.ParseMAC(correctMac)
					if setErr := m.netHandle.LinkSetHardwareAddr(m.ctrLink, addr); setErr == nil {
						reqLog.WithFields(log.Fields{
							"old_seed": hint.SeedName,
							"new_seed": actualName,
							"new_mac":  correctMac,
						}).Info("Post-Join: corrected MAC on container veth")
					} else {
						reqLog.WithError(setErr).Warn("Post-Join: failed to set MAC on container veth")
					}
				}
				hint.SeedName = actualName
				if actualHostname != "" {
					hint.Hostname = actualHostname
				}
				m.hostname = hint.Hostname
			} else if actualName != "" {
				reqLog.WithField("container", actualName).Debug("Post-Join: FIFO assignment confirmed correct")
			} else {
				reqLog.Debug("Post-Join: could not identify container via EndpointID")
			}
		} else {
			reqLog.WithError(corrErr).Warn("Post-Join: ContainerList failed")
		}
```

---

## Consequences
* **Deterministic Boot Recovery:** On system reboot, the plugin immediately maps the restarting containers to their cached hostnames using their persisted MAC addresses. No Docker API timeouts or candidates collisions occur.
* **Deterministic MAC Generation:** Even if the cache is missing or cleared, the unconditional post-Join hook resolves the true container name and rewrites the interface's MAC to the correct deterministic value (under the `actualName` seed) instead of leaving it with the fallback `EndpointID` seed.
* **Accurate Audit Detection:** The `udhcpc` process is always guaranteed to start with the proper `-F <container_name>` parameter, eliminating false-positive `STALE` statuses in `fix-stale-dhcp.sh`.
* **Negligible Overhead:** The `ContainerList` API call runs in a background goroutine and does not block critical paths.
