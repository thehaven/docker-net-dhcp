package plugin

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/mitchellh/mapstructure"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/thehaven/docker-net-dhcp/pkg/macgen"
	"github.com/thehaven/docker-net-dhcp/pkg/udhcpc"
	"github.com/thehaven/docker-net-dhcp/pkg/util"
)

// CLIOptionsKey is the key used in create network options by the CLI for custom options
const CLIOptionsKey string = "com.docker.network.generic"

func (p *Plugin) CreateNetwork(r CreateNetworkRequest) error {
	log.WithField("options", r.Options).Debug("CreateNetwork options")
	opts, err := decodeOpts(r.Options[util.OptionsKeyGeneric])
	if err != nil {
		return err
	}
	if opts.Bridge == "" {
		return util.ErrBridgeRequired
	}
	link, err := netlink.LinkByName(opts.Bridge)
	if err != nil {
		return fmt.Errorf("failed to lookup interface %v: %w", opts.Bridge, err)
	}
	if link.Type() != "bridge" {
		return util.ErrNotBridge
	}

	if !opts.IgnoreConflicts {
		ctxNets, cancelNets := context.WithTimeout(context.Background(), 5*time.Second)
		nets, err := p.docker.NetworkList(ctxNets, network.ListOptions{})
		cancelNets()
		if err == nil {
			for _, n := range nets {
				if IsDHCPPlugin(n.Driver) {
					otherOpts, err := decodeOpts(n.Options)
					if err == nil && otherOpts.Bridge == opts.Bridge && n.ID != r.NetworkID {
						return util.ErrBridgeUsed
					}
				}
			}
		}
	}

	if err := p.cache.Set(NetworkState{ID: r.NetworkID, Options: opts}); err != nil {
		return fmt.Errorf("failed to save network state to cache: %w", err)
	}
	log.WithFields(log.Fields{"network": r.NetworkID, "bridge": opts.Bridge}).Info("Network created")
	return nil
}

func (p *Plugin) DeleteNetwork(r DeleteNetworkRequest) error {
	_ = p.cache.Delete(r.NetworkID)
	log.WithField("network", r.NetworkID).Info("Network deleted")
	return nil
}

func vethPairNames(id string) (string, string) {
	return "dh-" + id[:12], id[:12] + "-dh"
}

func (p *Plugin) netOptions(ctx context.Context, id string) (DHCPNetworkOptions, error) {
	dummy := DHCPNetworkOptions{}
	state, ok := p.cache.Get(id)
	if ok {
		return state.Options, nil
	}
	n, err := p.docker.NetworkInspect(ctx, id, network.InspectOptions{})
	if err != nil {
		return dummy, fmt.Errorf("failed to get info from Docker: %w", err)
	}
	opts, _ := decodeOpts(n.Options)
	_ = p.cache.Set(NetworkState{ID: id, Options: opts})
	return opts, nil
}

func (p *Plugin) CreateEndpoint(ctx context.Context, r CreateEndpointRequest) (CreateEndpointResponse, error) {
	// Serialize CreateEndpoint calls to enable process-of-elimination when
	// multiple containers restart concurrently (C4).
	p.createMu.Lock()
	defer p.createMu.Unlock()

	reqLog := log.WithFields(log.Fields{
		"endpoint_id": r.EndpointID[:12],
		"network_id":  r.NetworkID[:12],
	})
	reqLog.Debugf("CreateEndpoint request: %+v", r)
	reqLog.Debugf("CreateEndpoint Options map: %+v", r.Options)
	res := CreateEndpointResponse{Interface: &EndpointInterface{}}

	opts, err := p.netOptions(ctx, r.NetworkID)
	if err != nil {
		return res, err
	}
	bridge, err := netlink.LinkByName(opts.Bridge)
	if err != nil {
		return res, err
	}

	hostName, ctrName := vethPairNames(r.EndpointID)
	la := netlink.NewLinkAttrs()
	la.Name = hostName
	hostLink := &netlink.Veth{LinkAttrs: la, PeerName: ctrName}

	// ---------------------------------------------------------------------------
	// Container identification: FIFO queue → deep fallback → EndpointID seed
	// ---------------------------------------------------------------------------
	// Docker's CreateEndpoint API does not include the container ID or name.
	// Docker also blocks on our response before updating its network state, so
	// findInNetwork CANNOT resolve at this point (it's a deadlock: Docker waits
	// for us, we wait for Docker). The FIFO queue populated by create/start
	// events is the primary resolution mechanism.
	//
	// Forward-looking events (create/start) flush backward-looking entries
	// (die/Leave) on push, preventing compose-down contamination while
	// preserving docker-restart support (die entries survive when no create
	// event follows).
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
		// PRIMARY: Pop from the per-network FIFO queue. The queue is populated by
		// create/start events (forward) and die/Leave events (backward). Forward
		// events flush backward entries on push to prevent compose-down contamination.
		// Retry briefly to allow the event listener to process concurrent events.
		for attempt := 0; attempt < 15; attempt++ {
			if name, hn, ok := p.popPendingContainer(r.NetworkID); ok {
				seedName = name
				hostname = hn
				reqLog.WithFields(log.Fields{
					"container": seedName,
					"hostname":  hostname,
					"attempt":   attempt,
				}).Debug("Resolved container via FIFO queue")
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	if seedName == "" {
		// SECONDARY: Deep fallback (C5) — list all containers on this network
		// and use process-of-elimination to find the one without an endpoint.
		ctrs, err := p.docker.ContainerList(ctx, container.ListOptions{All: true})
		if err == nil {
			p.RLock()
			claimedNames := make(map[string]bool, len(p.joinHints))
			for _, hint := range p.joinHints {
				if hint.SeedName != "" {
					claimedNames[hint.SeedName] = true
				}
			}
			p.RUnlock()

			var candidates []container.Summary
			for _, c := range ctrs {
				var settings *network.EndpointSettings
				for name, netSettings := range c.NetworkSettings.Networks {
					if netSettings.NetworkID == r.NetworkID || name == r.NetworkID {
						settings = netSettings
						break
					}
				}
				if settings == nil {
					continue
				}

				ctrName := ""
				if len(c.Names) > 0 {
					ctrName = strings.TrimPrefix(c.Names[0], "/")
				}

				reqLog.Debugf("Candidate container %s (%s) endpointID=%q", ctrName, c.ID[:12], settings.EndpointID)

				if settings.EndpointID != "" && settings.EndpointID != r.EndpointID {
					continue
				}
				if claimedNames[ctrName] {
					reqLog.Debugf("Skipping %s — already claimed in joinHints", ctrName)
					continue
				}

				candidates = append(candidates, c)
			}

			reqLog.Debugf("Deep fallback found %d candidates after elimination", len(candidates))

			if len(candidates) == 1 {
				c := candidates[0]
				if len(c.Names) > 0 {
					seedName = strings.TrimPrefix(c.Names[0], "/")
					hostname = seedName

					reqLog.WithField("container", seedName).Debug("Deep fallback resolved single remaining container")

					go func(id string) {
						inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						ctr, err := p.docker.ContainerInspect(inspectCtx, id)
						if err == nil {
							p.Lock()
							hint := p.joinHints[r.EndpointID]
							if hint.Hostname == "" || hint.Hostname == seedName {
								hint.Hostname = ctr.Config.Hostname
								if len(ctr.ID) >= 12 && hint.Hostname == ctr.ID[:12] {
									hint.Hostname = seedName
								}
								p.joinHints[r.EndpointID] = hint
							}
							p.Unlock()
						}
					}(c.ID)
				}
			} else if len(candidates) > 1 {
				reqLog.Warnf("Deep fallback: %d candidates remain after elimination; refusing to guess", len(candidates))
			}
		}

		if seedName != "" {
			reqLog.WithFields(log.Fields{
				"container": seedName,
				"hostname":  hostname,
			}).Debug("Resolved container info via deep fallback")
		}
	}

	// Enrich hostname from metadata map if the FIFO pop didn't provide it.
	if hostname == "" && seedName != "" {
		if cachedHostname, ok := p.lookupPendingMeta(r.NetworkID, seedName); ok && cachedHostname != "" {
			hostname = cachedHostname
			reqLog.WithField("hostname", hostname).Debug("Enriched hostname from metadata map")
		}
	}

	// Store hostname in joinHints so Join can pass it to DHCP client for
	// DNS registration (Option 12 + Option 81).
	// Also store seedName so Join can persist it in the cache for Leave pre-population.
	p.Lock()
	hint := p.joinHints[r.EndpointID]
	hint.Hostname = hostname
	hint.SeedName = seedName
	p.joinHints[r.EndpointID] = hint
	p.Unlock()

	var appliedMac string

	if r.Interface != nil && r.Interface.MacAddress != "" {
		// User-specified MAC: honour it exactly. Do NOT echo it back in the
		// response — Docker treats a non-nil MacAddress in the response as a
		// driver-initiated MAC modification and rejects it.
		appliedMac = r.Interface.MacAddress
		addr, _ := net.ParseMAC(appliedMac)
		hostLink.PeerHardwareAddr = addr
		res.Interface.MacAddress = "" // omitempty ensures this is absent from JSON

		// Persist the flag so post-Join MAC correction knows to skip.
		p.Lock()
		hint := p.joinHints[r.EndpointID]
		hint.UserSpecifiedMAC = true
		p.joinHints[r.EndpointID] = hint
		p.Unlock()

		reqLog.WithField("mac", appliedMac).Info("Using user-specified MAC")
	} else {
		// Deterministic MAC: seed is the container name, matching generate_mac.func.
		if seedName == "" {
			// Fallback: use EndpointID as seed. This is deterministic for this
			// endpoint but will NOT match generate_mac <container-name>.
			// Logged at WARN so operators can detect the race.
			seedName = r.EndpointID
			reqLog.Warn("No pending container found for network; using EndpointID as MAC seed — container name may not match generate_mac output")
		}

		macFormat := macFormatFromOpts(opts)
		detMac, err := macgen.Generate(macgen.Options{Seed: seedName, Format: macFormat})
		if err != nil {
			return res, fmt.Errorf("MAC generation failed: %w", err)
		}
		appliedMac = detMac
		addr, _ := net.ParseMAC(appliedMac)
		hostLink.PeerHardwareAddr = addr
		res.Interface.MacAddress = appliedMac
		reqLog.WithField("mac", appliedMac).WithField("seed", seedName).Info("Generated deterministic MAC")
	}

	if err := netlink.LinkAdd(hostLink); err != nil {
		return res, err
	}

	setup := func() error {
		_ = netlink.LinkSetUp(hostLink)
		ctrLink, err := netlink.LinkByName(ctrName)
		if err != nil {
			return err
		}
		_ = netlink.LinkSetUp(ctrLink)

		addr, _ := net.ParseMAC(appliedMac)
		_ = netlink.LinkSetHardwareAddr(ctrLink, addr)
		_ = netlink.LinkSetMaster(hostLink, bridge)

		timeout := defaultLeaseTimeout
		if opts.LeaseTimeout != 0 {
			timeout = opts.LeaseTimeout
		}

		// Retrieve the hostname stored above (may be empty for user-specified MAC path).
		p.RLock()
		dhcpHostname := p.joinHints[r.EndpointID].Hostname
		p.RUnlock()

		initialIP := func(v6 bool) error {
			timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			info, err := udhcpc.GetIP(timeoutCtx, ctrName, &udhcpc.DHCPClientOptions{
				Hostname:   dhcpHostname,
				MacAddress: appliedMac,
				V6:         opts.IPv6,
			})
			if err != nil {
				return err
			}
			ip, _ := netlink.ParseAddr(info.IP)

			p.Lock()
			hint := p.joinHints[r.EndpointID]
			if opts.IPv6 {
				if r.Interface == nil || r.Interface.AddressIPv6 == "" {
					res.Interface.AddressIPv6 = info.IP
				}
				hint.IPv6 = ip
			} else {
				if r.Interface == nil || r.Interface.Address == "" {
					res.Interface.Address = info.IP
				}
				hint.IPv4 = ip
				hint.Gateway = info.Gateway
			}
			p.joinHints[r.EndpointID] = hint
			p.Unlock()
			return nil
		}

		if err := initialIP(false); err != nil {
			return err
		}
		if opts.IPv6 {
			if err := initialIP(true); err != nil {
				return err
			}
		}
		return nil
	}

	if err := setup(); err != nil {
		_ = netlink.LinkDel(hostLink)
		return res, err
	}

	if res.Interface != nil && res.Interface.Address == "" && res.Interface.AddressIPv6 == "" && res.Interface.MacAddress == "" {
		res.Interface = nil
	}

	return res, nil
}

// macFormatFromOpts converts the network MacFormat option string to a macgen.Format.
func macFormatFromOpts(opts DHCPNetworkOptions) macgen.Format {
	switch opts.MacFormat {
	case "hyphen":
		return macgen.FormatHyphen
	case "dot":
		return macgen.FormatDot
	default:
		return macgen.FormatColon
	}
}

type operInfo struct {
	Bridge      string `mapstructure:"bridge"`
	HostVEth    string `mapstructure:"veth_host"`
	HostVEthMAC string `mapstructure:"veth_host_mac"`
}

func (p *Plugin) EndpointOperInfo(ctx context.Context, r InfoRequest) (InfoResponse, error) {
	res := InfoResponse{}
	opts, _ := p.netOptions(ctx, r.NetworkID)
	hostName, _ := vethPairNames(r.EndpointID)
	hostLink, err := netlink.LinkByName(hostName)
	if err != nil {
		return res, err
	}
	info := operInfo{Bridge: opts.Bridge, HostVEth: hostName, HostVEthMAC: hostLink.Attrs().HardwareAddr.String()}
	_ = mapstructure.Decode(info, &res.Value)
	return res, nil
}

func (p *Plugin) DeleteEndpoint(r DeleteEndpointRequest) error {
	hostName, _ := vethPairNames(r.EndpointID)
	link, err := netlink.LinkByName(hostName)
	if err == nil {
		_ = netlink.LinkDel(link)
	}

	p.Lock()
	manager, ok := p.persistentDHCP[r.EndpointID]
	if ok {
		delete(p.persistentDHCP, r.EndpointID)
	}
	p.Unlock()
	if ok && manager != nil {
		_ = manager.Stop()
	}

	if p.cache != nil {
		_ = p.cache.DeleteEndpoint(r.NetworkID, r.EndpointID)
	}

	log.WithField("endpoint", r.EndpointID[:12]).Info("Endpoint deleted")
	return nil
}

func (p *Plugin) addRoutes(opts *DHCPNetworkOptions, v6 bool, bridge netlink.Link, r JoinRequest, hint joinHint, res *JoinResponse) error {
	family := unix.AF_INET
	if v6 {
		family = unix.AF_INET6
	}
	routes, err := netlink.RouteListFiltered(family, &netlink.Route{
		LinkIndex: bridge.Attrs().Index,
		Type:      unix.RTN_UNICAST,
	}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_TYPE)
	if err != nil {
		return err
	}

	for _, route := range routes {
		if route.Dst == nil {
			if family == unix.AF_INET && res.Gateway == "" {
				res.Gateway = route.Gw.String()
			}
			if family == unix.AF_INET6 && res.GatewayIPv6 == "" {
				res.GatewayIPv6 = route.Gw.String()
			}
			continue
		}
		if opts.SkipRoutes || route.Protocol == unix.RTPROT_KERNEL {
			continue
		}
		res.StaticRoutes = append(res.StaticRoutes, &StaticRoute{
			Destination: route.Dst.String(),
			NextHop:     route.Gw.String(),
			RouteType:   map[bool]int{true: 0, false: 1}[route.Gw != nil],
		})
	}
	return nil
}

func (p *Plugin) Join(ctx context.Context, r JoinRequest) (JoinResponse, error) {
	reqLog := log.WithField("endpoint_id", r.EndpointID[:12])
	res := JoinResponse{}
	opts, err := p.netOptions(ctx, r.NetworkID)
	if err != nil {
		return res, err
	}
	_, ctrName := vethPairNames(r.EndpointID)
	res.InterfaceName = InterfaceName{SrcName: ctrName, DstPrefix: "eth"}

	p.Lock()
	hint, ok := p.joinHints[r.EndpointID]
	if ok {
		delete(p.joinHints, r.EndpointID)
	}
	p.Unlock()

	if !ok {
		return res, util.ErrNoHint
	}

	// NOTE: Docker does NOT expose EndpointID in ContainerList/Inspect until
	// AFTER Join returns. MAC correction is deferred to the post-Join goroutine
	// where m.Start() has completed and the endpoint association is visible.

	if hint.Gateway != "" {
		res.Gateway = hint.Gateway
	}
	bridge, err := netlink.LinkByName(opts.Bridge)
	if err == nil {
		_ = p.addRoutes(&opts, false, bridge, r, hint, &res)
		if opts.IPv6 {
			_ = p.addRoutes(&opts, true, bridge, r, hint, &res)
		}
	}

	m := newDHCPManager(p.docker, r, opts)
	m.LastIP = hint.IPv4
	m.LastIPv6 = hint.IPv6
	m.hostname = hint.Hostname

	go func() {
		ctxBG, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Start(ctxBG); err != nil {
			reqLog.WithError(err).Error("Failed to start DHCP manager in background")
			return
		}

		// ---------------------------------------------------------------
		// Post-Join MAC correction
		// ---------------------------------------------------------------
		// Docker commits EndpointID→Container association AFTER Join
		// returns, so ContainerList can NOW resolve the real container.
		if true {
			var actualName, actualHostname string
			for attempt := 0; attempt < 10; attempt++ {
				time.Sleep(200 * time.Millisecond)
				corrCtx, corrCancel := context.WithTimeout(context.Background(), 2*time.Second)
				corrCtrs, corrErr := p.docker.ContainerList(corrCtx, container.ListOptions{All: true})
				corrCancel()
				if corrErr == nil {
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
				}
				if actualName != "" {
					break
				}
			}

			if actualName != "" {
				if actualName != hint.SeedName {
					// Bug fix: do NOT override a user-specified MAC address.
					// Docker propagates mac_address from compose files, and
					// the post-Join correction was unconditionally replacing
					// it with a deterministic MAC based on the corrected name.
					if !hint.UserSpecifiedMAC {
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
					} else {
						reqLog.WithFields(log.Fields{
							"old_seed": hint.SeedName,
							"new_seed": actualName,
						}).Info("Post-Join: FIFO was wrong but user-specified MAC preserved (not overwritten)")
					}
					hint.SeedName = actualName
				}
				if actualHostname != "" {
					hint.Hostname = actualHostname
				} else {
					hint.Hostname = actualName
				}
				m.hostname = hint.Hostname
				reqLog.WithFields(log.Fields{
					"container": actualName,
					"hostname":  m.hostname,
				}).Info("Post-Join: identified container via EndpointID")
			} else {
				reqLog.Debug("Post-Join: could not identify container via EndpointID")
			}
		}

		_ = m.setupClient(false)
		if opts.IPv6 {
			_ = m.setupClient(true)
		}
		p.Lock()
		p.persistentDHCP[r.EndpointID] = m
		p.Unlock()

		// Persist endpoint state including hostname for recovery after restart.
		var ipStr string
		if m.LastIP != nil {
			ipStr = m.LastIP.String()
		}
		ep := EndpointState{
			ID:         r.EndpointID,
			SandboxKey: r.SandboxKey,
			MacAddress: m.ctrLink.Attrs().HardwareAddr.String(),
			IP:         ipStr,
			Gateway:    hint.Gateway,
			Hostname:   hint.Hostname,
			SeedName:   hint.SeedName,
		}
		_ = p.cache.SetEndpoint(r.NetworkID, ep)
	}()
	reqLog.Info("Joined sandbox")
	return res, nil
}

func (p *Plugin) Leave(ctx context.Context, r LeaveRequest) error {
	// Cache container metadata for potential restart. Docker's restart sequence
	// is Leave → DeleteEndpoint → CreateEndpoint → Join. Pushed as backward-
	// looking (forward=false) so that compose-up create events flush these.
	if ep, ok := p.cache.GetEndpoint(r.NetworkID, r.EndpointID); ok && ep.SeedName != "" {
		p.pushPendingContainer(r.NetworkID, ep.SeedName, ep.Hostname, false)
		p.upsertPendingMeta(r.NetworkID, ep.SeedName, ep.Hostname)
		log.WithFields(log.Fields{
			"endpoint":  r.EndpointID[:12],
			"network":   r.NetworkID[:12],
			"container": ep.SeedName,
		}).Debug("Cached container metadata from Leave for potential restart")
	}

	p.Lock()
	manager, ok := p.persistentDHCP[r.EndpointID]
	if ok {
		delete(p.persistentDHCP, r.EndpointID)
	}
	p.Unlock()
	if ok {
		_ = manager.Stop()
	}
	_ = p.cache.DeleteEndpoint(r.NetworkID, r.EndpointID)
	return nil
}

func (p *Plugin) Recover(ctx context.Context) {
	log.Info("Starting warm recovery...")
	nets := p.cache.GetAll()
	for _, n := range nets {
		for _, ep := range n.Endpoints {
			p.resumeDHCP(ep, n.Options, n.ID)
		}
	}
}

func (p *Plugin) resumeDHCP(ep EndpointState, opts DHCPNetworkOptions, networkID string) {
	// Skip recovery if the network namespace path does not exist on disk
	if ep.SandboxKey != "" {
		nsPath := ep.SandboxKey
		if !strings.HasPrefix(nsPath, "/") {
			nsPath = fmt.Sprintf("/var/run/docker/netns/%s", ep.SandboxKey)
		}
		if _, err := os.Stat(nsPath); os.IsNotExist(err) {
			log.WithFields(log.Fields{
				"endpoint": ep.ID[:12],
				"network":  networkID[:12],
				"sandbox":  ep.SandboxKey,
			}).Debug("Skipping recovery for endpoint with missing network namespace")
			return
		}
	}

	r := JoinRequest{NetworkID: networkID, EndpointID: ep.ID, SandboxKey: ep.SandboxKey}
	m := newDHCPManager(p.docker, r, opts)
	// Hostname is persisted in EndpointState; no Docker API call required.
	m.hostname = ep.Hostname

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), p.awaitTimeout)
		defer cancel()
		if err := m.Start(ctx); err == nil {
			_ = m.setupClient(false)
			if opts.IPv6 {
				_ = m.setupClient(true)
			}
			p.Lock()
			p.persistentDHCP[ep.ID] = m
			p.Unlock()
		}
	}()
}
