package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/mitchellh/mapstructure"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/devplayer0/docker-net-dhcp/pkg/macgen"
	"github.com/devplayer0/docker-net-dhcp/pkg/udhcpc"
	"github.com/devplayer0/docker-net-dhcp/pkg/util"
)

// CLIOptionsKey is the key used in create network options by the CLI for custom options
const CLIOptionsKey string = "com.docker.network.generic"

func (p *Plugin) CreateNetwork(r CreateNetworkRequest) error {
	opts, err := decodeOpts(r.Options[util.OptionsKeyGeneric])
	if err != nil { return err }
	if opts.Bridge == "" { return util.ErrBridgeRequired }
	if err := p.cache.Set(NetworkState{ID: r.NetworkID, Options: opts}); err != nil { return err }
	log.WithFields(log.Fields{"network": r.NetworkID, "bridge": opts.Bridge}).Info("Network created")
	return nil
}

func (p *Plugin) DeleteNetwork(r DeleteNetworkRequest) error {
	_ = p.cache.Delete(r.NetworkID)
	return nil
}

func vethPairNames(id string) (string, string) {
	return "dh-" + id[:12], id[:12] + "-dh"
}

func (p *Plugin) netOptions(ctx context.Context, id string) (DHCPNetworkOptions, error) {
	state, ok := p.cache.Get(id)
	if ok { return state.Options, nil }
	n, err := p.docker.NetworkInspect(ctx, id, network.InspectOptions{})
	if err != nil { return DHCPNetworkOptions{}, err }
	opts, _ := decodeOpts(n.Options)
	_ = p.cache.Set(NetworkState{ID: id, Options: opts})
	return opts, nil
}

func (p *Plugin) CreateEndpoint(ctx context.Context, r CreateEndpointRequest) (CreateEndpointResponse, error) {
	reqLog := log.WithField("endpoint_id", r.EndpointID[:12]).WithField("network_id", r.NetworkID[:12])
	res := CreateEndpointResponse{Interface: &EndpointInterface{}}

	opts, err := p.netOptions(ctx, r.NetworkID)
	if err != nil { return res, err }
	bridge, err := netlink.LinkByName(opts.Bridge)
	if err != nil { return res, err }

	hostName, ctrName := vethPairNames(r.EndpointID)
	la := netlink.NewLinkAttrs()
	la.Name = hostName
	hostLink := &netlink.Veth{LinkAttrs: la, PeerName:  ctrName}

	if r.Interface.MacAddress != "" {
		addr, _ := net.ParseMAC(r.Interface.MacAddress)
		hostLink.PeerHardwareAddr = addr
		res.Interface.MacAddress = r.Interface.MacAddress
	} else {
		var seed string
		p.Lock()
		if len(p.creationQueue) > 0 {
			seed = p.creationQueue[len(p.creationQueue)-1]
		}
		p.Unlock()

		if seed == "" {
			h := sha256.New()
			h.Write([]byte(r.NetworkID))
			h.Write([]byte(r.EndpointID))
			seed = hex.EncodeToString(h.Sum(nil))
		}

		macFormatStr := opts.MacFormat
		macFormat := macgen.FormatColon
		if macFormatStr == "hyphen" { macFormat = macgen.FormatHyphen } else if macFormatStr == "dot" { macFormat = macgen.FormatDot }
		detMac, err := macgen.Generate(macgen.Options{Seed: seed, Format: macFormat})
		if err == nil {
			addr, _ := net.ParseMAC(detMac)
			hostLink.PeerHardwareAddr = addr
			res.Interface.MacAddress = detMac
		}
	}

	if err := netlink.LinkAdd(hostLink); err != nil { return res, err }
	
	_ = netlink.LinkSetUp(hostLink)
	ctrLink, err := netlink.LinkByName(ctrName)
	if err == nil {
		_ = netlink.LinkSetUp(ctrLink)
		addr, _ := net.ParseMAC(res.Interface.MacAddress)
		_ = netlink.LinkSetHardwareAddr(ctrLink, addr)
	}
	_ = netlink.LinkSetMaster(hostLink, bridge)

	timeout := defaultLeaseTimeout
	if opts.LeaseTimeout != 0 { timeout = opts.LeaseTimeout }
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	
	info, err := udhcpc.GetIP(timeoutCtx, ctrName, &udhcpc.DHCPClientOptions{V6: opts.IPv6})
	if err == nil {
		ip, _ := netlink.ParseAddr(info.IP)
		p.Lock()
		hint := p.joinHints[r.EndpointID]
		res.Interface.Address = info.IP
		hint.IPv4 = ip
		hint.Gateway = info.Gateway
		p.joinHints[r.EndpointID] = hint
		p.Unlock()
	}

	reqLog.WithField("mac", res.Interface.MacAddress).Info("Endpoint created")
	return res, nil
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
	if err != nil { return nil }
	_ = netlink.LinkDel(link)
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
	opts, _ := p.netOptions(ctx, r.NetworkID)
	_, ctrName := vethPairNames(r.EndpointID)
	res := JoinResponse{InterfaceName: InterfaceName{SrcName: ctrName, DstPrefix: "eth"}}

	p.Lock()
	hint, ok := p.joinHints[r.EndpointID]
	if ok { delete(p.joinHints, r.EndpointID) }
	p.Unlock()

	if ok {
		if hint.Gateway != "" { res.Gateway = hint.Gateway }
		if bridge, err := netlink.LinkByName(opts.Bridge); err == nil {
			_ = p.addRoutes(&opts, false, bridge, r, hint, &res)
			if opts.IPv6 { _ = p.addRoutes(&opts, true, bridge, r, hint, &res) }
		}
	}

	m := newDHCPManager(p.docker, r, opts)
	m.LastIP = hint.IPv4
	m.LastIPv6 = hint.IPv6

	go func() {
		ctxBG, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Start(ctxBG); err != nil { return }
		_ = m.setupClient(false)
		if opts.IPv6 { _ = m.setupClient(true) }
		p.Lock()
		p.persistentDHCP[r.EndpointID] = m
		p.Unlock()
		ep := EndpointState{ID: r.EndpointID, SandboxKey: r.SandboxKey, MacAddress: m.ctrLink.Attrs().HardwareAddr.String(), IP: m.LastIP.String()}
		_ = p.cache.SetEndpoint(r.NetworkID, ep)
	}()

	reqLog.Debug("Joined sandbox")
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
