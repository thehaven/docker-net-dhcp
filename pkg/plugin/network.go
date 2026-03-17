package plugin

import (
	"context"
	"fmt"
	"net"
	"time"

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
	if err != nil { return err }
	if opts.Bridge == "" { return util.ErrBridgeRequired }
	link, err := netlink.LinkByName(opts.Bridge)
	if err != nil { return fmt.Errorf("failed to lookup interface %v: %w", opts.Bridge, err) }
	if link.Type() != "bridge" { return util.ErrNotBridge }

	if !opts.IgnoreConflicts {
		ctxNets, cancelNets := context.WithTimeout(context.Background(), 5*time.Second)
		nets, err := p.docker.NetworkList(ctxNets, network.ListOptions{})
		cancelNets()
		if err == nil {
			for _, n := range nets {
				if IsDHCPPlugin(n.Driver) {
					otherOpts, err := decodeOpts(n.Options)
					if err == nil && otherOpts.Bridge == opts.Bridge && n.ID != r.NetworkID { return util.ErrBridgeUsed }
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
	if ok { return state.Options, nil }
	n, err := p.docker.NetworkInspect(ctx, id, network.InspectOptions{})
	if err != nil { return dummy, fmt.Errorf("failed to get info from Docker: %w", err) }
	opts, _ := decodeOpts(n.Options)
	_ = p.cache.Set(NetworkState{ID: id, Options: opts})
	return opts, nil
}

func (p *Plugin) CreateEndpoint(ctx context.Context, r CreateEndpointRequest) (CreateEndpointResponse, error) {
	reqLog := log.WithFields(log.Fields{
		"endpoint_id": r.EndpointID[:12],
		"network_id":  r.NetworkID[:12],
	})
	res := CreateEndpointResponse{Interface: &EndpointInterface{}}

	opts, err := p.netOptions(ctx, r.NetworkID)
	if err != nil { return res, err }
	bridge, err := netlink.LinkByName(opts.Bridge)
	if err != nil { return res, err }

	hostName, ctrName := vethPairNames(r.EndpointID)
	la := netlink.NewLinkAttrs()
	la.Name = hostName
	hostLink := &netlink.Veth{LinkAttrs: la, PeerName: ctrName}

	// Always pop from the pending queue — even for static-MAC containers.
	// This consumes the entry so it cannot poison subsequent dynamic-MAC
	// containers on the same network (BUG-7 fix).
	// The hostname is also propagated for DNS registration regardless of MAC source.
	seedName, hostname := p.popPendingContainer(r.NetworkID)

	// Store hostname in joinHints so Join can pass it to DHCP client for
	// DNS registration (Option 12 + Option 81).
	p.Lock()
	hint := p.joinHints[r.EndpointID]
	hint.Hostname = hostname
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
		if err != nil { return res, fmt.Errorf("MAC generation failed: %w", err) }
		appliedMac = detMac
		addr, _ := net.ParseMAC(appliedMac)
		hostLink.PeerHardwareAddr = addr
		res.Interface.MacAddress = appliedMac
		reqLog.WithField("mac", appliedMac).WithField("seed", seedName).Info("Generated deterministic MAC")
	}

	if err := netlink.LinkAdd(hostLink); err != nil { return res, err }

	setup := func() error {
		_ = netlink.LinkSetUp(hostLink)
		ctrLink, err := netlink.LinkByName(ctrName)
		if err != nil { return err }
		_ = netlink.LinkSetUp(ctrLink)

		addr, _ := net.ParseMAC(appliedMac)
		_ = netlink.LinkSetHardwareAddr(ctrLink, addr)
		_ = netlink.LinkSetMaster(hostLink, bridge)

		timeout := defaultLeaseTimeout
		if opts.LeaseTimeout != 0 { timeout = opts.LeaseTimeout }

		// Retrieve the hostname stored above (may be empty for user-specified MAC path).
		p.RLock()
		dhcpHostname := p.joinHints[r.EndpointID].Hostname
		p.RUnlock()

		initialIP := func(v6 bool) error {
			timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			info, err := udhcpc.GetIP(timeoutCtx, ctrName, &udhcpc.DHCPClientOptions{
				Hostname: dhcpHostname,
				V6:       opts.IPv6,
			})
			if err != nil { return err }
			ip, _ := netlink.ParseAddr(info.IP)

			p.Lock()
			hint := p.joinHints[r.EndpointID]
			if opts.IPv6 {
				if r.Interface == nil || r.Interface.AddressIPv6 == "" { res.Interface.AddressIPv6 = info.IP }
				hint.IPv6 = ip
			} else {
				if r.Interface == nil || r.Interface.Address == "" { res.Interface.Address = info.IP }
				hint.IPv4 = ip
				hint.Gateway = info.Gateway
			}
			p.joinHints[r.EndpointID] = hint
			p.Unlock()
			return nil
		}

		if err := initialIP(false); err != nil { return err }
		if opts.IPv6 { if err := initialIP(true); err != nil { return err } }
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
	if err != nil { return res, err }
	info := operInfo{Bridge: opts.Bridge, HostVEth: hostName, HostVEthMAC: hostLink.Attrs().HardwareAddr.String()}
	_ = mapstructure.Decode(info, &res.Value)
	return res, nil
}

func (p *Plugin) DeleteEndpoint(r DeleteEndpointRequest) error {
	hostName, _ := vethPairNames(r.EndpointID)
	link, err := netlink.LinkByName(hostName)
	if err == nil { _ = netlink.LinkDel(link) }
	log.WithField("endpoint", r.EndpointID[:12]).Info("Endpoint deleted")
	return nil
}

func (p *Plugin) addRoutes(opts *DHCPNetworkOptions, v6 bool, bridge netlink.Link, r JoinRequest, hint joinHint, res *JoinResponse) error {
	family := unix.AF_INET
	if v6 { family = unix.AF_INET6 }
	routes, err := netlink.RouteListFiltered(family, &netlink.Route{
		LinkIndex: bridge.Attrs().Index,
		Type:      unix.RTN_UNICAST,
	}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_TYPE)
	if err != nil { return err }

	for _, route := range routes {
		if route.Dst == nil {
			if family == unix.AF_INET && res.Gateway == "" { res.Gateway = route.Gw.String() }
			if family == unix.AF_INET6 && res.GatewayIPv6 == "" { res.GatewayIPv6 = route.Gw.String() }
			continue
		}
		if opts.SkipRoutes || route.Protocol == unix.RTPROT_KERNEL { continue }
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
	if err != nil { return res, err }
	_, ctrName := vethPairNames(r.EndpointID)
	res.InterfaceName = InterfaceName{SrcName: ctrName, DstPrefix: "eth"}

	p.Lock()
	hint, ok := p.joinHints[r.EndpointID]
	if ok { delete(p.joinHints, r.EndpointID) }
	p.Unlock()

	if !ok { return res, util.ErrNoHint }
	if hint.Gateway != "" { res.Gateway = hint.Gateway }
	bridge, err := netlink.LinkByName(opts.Bridge)
	if err == nil {
		_ = p.addRoutes(&opts, false, bridge, r, hint, &res)
		if opts.IPv6 { _ = p.addRoutes(&opts, true, bridge, r, hint, &res) }
	}

	m := newDHCPManager(p.docker, r, opts)
	m.LastIP = hint.IPv4
	m.LastIPv6 = hint.IPv6
	// Hostname from CreateEndpoint (via popPendingContainer) propagated
	// through joinHints — used for DHCP Option 12 and Option 81 registration.
	m.hostname = hint.Hostname

	go func() {
		ctxBG, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Start(ctxBG); err != nil { return }

		_ = m.setupClient(false)
		if opts.IPv6 { _ = m.setupClient(true) }
		p.Lock()
		p.persistentDHCP[r.EndpointID] = m
		p.Unlock()

		// Persist endpoint state including hostname for recovery after restart.
		var ipStr string
		if m.LastIP != nil { ipStr = m.LastIP.String() }
		ep := EndpointState{
			ID:         r.EndpointID,
			SandboxKey: r.SandboxKey,
			MacAddress: m.ctrLink.Attrs().HardwareAddr.String(),
			IP:         ipStr,
			Gateway:    hint.Gateway,
			Hostname:   hint.Hostname,
		}
		_ = p.cache.SetEndpoint(r.NetworkID, ep)
	}()
	reqLog.Info("Joined sandbox")
	return res, nil
}

func (p *Plugin) Leave(ctx context.Context, r LeaveRequest) error {
	p.Lock()
	manager, ok := p.persistentDHCP[r.EndpointID]
	if ok { delete(p.persistentDHCP, r.EndpointID) }
	p.Unlock()
	if ok { _ = manager.Stop() }
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
	r := JoinRequest{NetworkID: networkID, EndpointID: ep.ID, SandboxKey: ep.SandboxKey}
	m := newDHCPManager(p.docker, r, opts)
	// Hostname is persisted in EndpointState; no Docker API call required.
	m.hostname = ep.Hostname

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), p.awaitTimeout)
		defer cancel()
		if err := m.Start(ctx); err == nil {
			_ = m.setupClient(false)
			if opts.IPv6 { _ = m.setupClient(true) }
			p.Lock()
			p.persistentDHCP[ep.ID] = m
			p.Unlock()
		}
	}()
}
