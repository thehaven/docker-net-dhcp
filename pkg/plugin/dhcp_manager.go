package plugin

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	docker "github.com/docker/docker/client"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/thehaven/docker-net-dhcp/pkg/udhcpc"
	"github.com/thehaven/docker-net-dhcp/pkg/util"
)

const pollTime = 100 * time.Millisecond

type dhcpManager struct {
	docker  *docker.Client
	joinReq JoinRequest
	opts    DHCPNetworkOptions

	LastIP   *netlink.Addr
	LastIPv6 *netlink.Addr

	nsPath    string
	hostname  string
	nsHandle  netns.NsHandle
	netHandle *netlink.Handle
	ctrLink   netlink.Link

	stopChan  chan struct{}
	errChan   chan error
	errChanV6 chan error
}

func newDHCPManager(docker *docker.Client, r JoinRequest, opts DHCPNetworkOptions) *dhcpManager {
	return &dhcpManager{
		docker:  docker,
		joinReq: r,
		opts:    opts,
		stopChan: make(chan struct{}),
	}
}

func (m *dhcpManager) logFields(v6 bool) log.Fields {
	return log.Fields{
		"endpoint": m.joinReq.EndpointID[:12],
		"sandbox":  m.joinReq.SandboxKey,
		"is_ipv6":  v6,
	}
}

func (m *dhcpManager) renew(v6 bool, info udhcpc.Info) error {
	lastIP := m.LastIP
	if v6 { lastIP = m.LastIPv6 }
	ip, err := netlink.ParseAddr(info.IP)
	if err != nil { return err }

	if lastIP != nil && !ip.Equal(*lastIP) {
		log.WithFields(m.logFields(v6)).WithField("old", lastIP).WithField("new", ip).Warn("IP changed on renew")
	}
	if v6 { m.LastIPv6 = ip } else { m.LastIP = ip }

	if !v6 && info.Gateway != "" {
		newGw := net.ParseIP(info.Gateway)
		routes, _ := m.netHandle.RouteListFiltered(unix.AF_INET, &netlink.Route{LinkIndex: m.ctrLink.Attrs().Index, Dst: nil}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_DST)
		if len(routes) == 0 {
			_ = m.netHandle.RouteAdd(&netlink.Route{LinkIndex: m.ctrLink.Attrs().Index, Gw: newGw})
		} else if !newGw.Equal(routes[0].Gw) {
			routes[0].Gw = newGw
			_ = m.netHandle.RouteReplace(&routes[0])
		}
	}
	return nil
}

func (m *dhcpManager) setupClient(v6 bool) error {
	client, err := udhcpc.NewDHCPClient(m.ctrLink.Attrs().Name, &udhcpc.DHCPClientOptions{
		Hostname: m.hostname, V6: v6, Namespace: m.nsPath,
	})
	if err != nil { return err }
	events, err := client.Start()
	if err != nil { return err }

	go func() {
		for {
			select {
			case event := <-events:
				if event.Type == "renew" { _ = m.renew(v6, event.Data) }
			case <-m.stopChan:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				client.Finish(ctx)
				return
			}
		}
	}()
	return nil
}

func (m *dhcpManager) Start(ctx context.Context) error {
	// ZERO-API PATH RESOLUTION
	if strings.HasPrefix(m.joinReq.SandboxKey, "/") {
		m.nsPath = m.joinReq.SandboxKey
	} else {
		m.nsPath = fmt.Sprintf("/var/run/docker/netns/%s", m.joinReq.SandboxKey)
	}

	var err error
	m.nsHandle, err = util.AwaitNetNS(ctx, m.nsPath, pollTime)
	if err != nil { return err }

	m.netHandle, err = netlink.NewHandleAt(m.nsHandle)
	if err != nil { return err }

	hostName, _ := vethPairNames(m.joinReq.EndpointID)
	hostLink, err := netlink.LinkByName(hostName)
	if err != nil { return err }
	
	hostVeth, ok := hostLink.(*netlink.Veth)
	if !ok { return util.ErrNotVEth }
	ctrIndex, err := netlink.VethPeerIndex(hostVeth)
	if err != nil { return err }

	m.ctrLink, err = util.AwaitLinkByIndex(ctx, m.netHandle, ctrIndex, pollTime)
	if err != nil { return err }

	// Wait for Docker to rename it from the temp name to ethX (if we return SrcName/DstPrefix correctly)
	// But in CreateEndpoint we used vethPairNames.
	return nil
}

func (m *dhcpManager) Stop() error {
	close(m.stopChan)
	if m.nsHandle != 0 { m.nsHandle.Close() }
	if m.netHandle != nil { m.netHandle.Delete() }
	return nil
}
