package plugin

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"
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

	stopChan chan struct{}
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

	if !v6 && info.Gateway != "" && m.netHandle != nil && m.ctrLink != nil {
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

func (m *dhcpManager) processEvents(v6 bool, events <-chan udhcpc.Event) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				// The udhcpc stdout pipe has closed — the process exited.
				// Return so the caller (setupClient restart loop) can respawn it.
				log.WithFields(m.logFields(v6)).Warn("DHCP client pipe closed (udhcpc exited)")
				return
			}
			switch event.Type {
			case "bound", "renew":
				_ = m.renew(v6, event.Data)
			case "deconfig", "leasefail", "nak":
				log.WithFields(m.logFields(v6)).WithField("event", event.Type).Warn("DHCP failure event received")
			}
		case <-m.stopChan:
			return
		}
	}
}

func (m *dhcpManager) setupClient(v6 bool) error {
	client, err := udhcpc.NewDHCPClient(m.ctrLink.Attrs().Name, &udhcpc.DHCPClientOptions{
		Hostname: m.hostname, V6: v6, Namespace: m.nsPath,
	})
	if err != nil {
		log.WithFields(m.logFields(v6)).WithError(err).Error("Failed to create DHCP client")
		return err
	}
	events, err := client.Start()
	if err != nil {
		log.WithFields(m.logFields(v6)).WithError(err).Error("Failed to start DHCP client")
		return err
	}

	go func(first *udhcpc.DHCPClient, firstEvents <-chan udhcpc.Event) {
		cur, evts := first, firstEvents
		for {
			m.processEvents(v6, evts)

			// Always clean up the current client process.
			finCtx, finCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = cur.Finish(finCtx)
			finCancel()

			// If Stop() was called, exit the loop.
			select {
			case <-m.stopChan:
				return
			default:
			}

			// udhcpc exited without being asked — restart after a short back-off.
			log.WithFields(m.logFields(v6)).Warn("DHCP client exited unexpectedly; restarting in 5 s")

			// Inner retry loop: keep trying until we have a running client or are stopped.
			for {
				select {
				case <-m.stopChan:
					return
				case <-time.After(5 * time.Second):
				}

				newClient, err := udhcpc.NewDHCPClient(m.ctrLink.Attrs().Name, &udhcpc.DHCPClientOptions{
					Hostname: m.hostname, V6: v6, Namespace: m.nsPath,
				})
				if err != nil {
					log.WithFields(m.logFields(v6)).WithError(err).Error("Failed to recreate DHCP client; will retry")
					continue
				}
				newEvents, err := newClient.Start()
				if err != nil {
					log.WithFields(m.logFields(v6)).WithError(err).Error("Failed to start replacement DHCP client; will retry")
					continue
				}
				cur, evts = newClient, newEvents
				break
			}
		}
	}(client, events)
	return nil
}

func (m *dhcpManager) findPID(ctx context.Context) (int, error) {
	n, err := m.docker.NetworkInspect(ctx, m.joinReq.NetworkID, network.InspectOptions{Verbose: true})
	if err != nil {
		return 0, err
	}

	for ctrID, epInfo := range n.Containers {
		if epInfo.EndpointID == m.joinReq.EndpointID {
			ctr, err := m.docker.ContainerInspect(ctx, ctrID)
			if err != nil {
				return 0, err
			}
			return ctr.State.Pid, nil
		}
	}

	return 0, fmt.Errorf("container for endpoint %s not found", m.joinReq.EndpointID)
}

func (m *dhcpManager) Start(ctx context.Context) error {
	// Build the primary namespace path from the SandboxKey.
	if strings.HasPrefix(m.joinReq.SandboxKey, "/") {
		m.nsPath = m.joinReq.SandboxKey
	} else {
		m.nsPath = fmt.Sprintf("/var/run/docker/netns/%s", m.joinReq.SandboxKey)
	}

	// Resolve the namespace handle.  Try the primary path first; if it is not
	// accessible (e.g. /var/run/docker/netns is not bind-mounted into the
	// plugin container), retry the PID-based /proc/<pid>/ns/net path on every
	// poll tick.  The PID lookup itself may fail transiently while Docker is
	// still starting the container, so we keep trying until the context deadline.
	var err error
	m.nsHandle, err = m.awaitNetNS(ctx)
	if err != nil {
		return err
	}

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

	return nil
}

// awaitNetNS polls for the container's network namespace handle using both
// the primary SandboxKey path and a PID-based fallback path.  It retries
// both on every tick so that transient failures (container PID not yet
// assigned, or bind-mount not available) are eventually resolved.
func (m *dhcpManager) awaitNetNS(ctx context.Context) (netns.NsHandle, error) {
	type result struct {
		ns  netns.NsHandle
		err error
	}
	ch := make(chan result, 1)

	go func() {
		for {
			// Try the primary path (SandboxKey).
			if ns, e := netns.GetFromPath(m.nsPath); e == nil {
				ch <- result{ns: ns}
				return
			}

			// Try the PID-based fallback.
			findCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			pid, pidErr := m.findPID(findCtx)
			cancel()
			if pidErr == nil && pid > 0 {
				pidPath := fmt.Sprintf("/proc/%d/ns/net", pid)
				if ns, e := netns.GetFromPath(pidPath); e == nil {
					m.nsPath = pidPath
					log.WithFields(m.logFields(false)).WithField("path", pidPath).Info("Using /proc namespace path (bind-mount fallback)")
					ch <- result{ns: ns}
					return
				}
			}

			select {
			case <-ctx.Done():
				ch <- result{err: ctx.Err()}
				return
			case <-time.After(pollTime):
			}
		}
	}()

	res := <-ch
	if res.err != nil {
		log.WithFields(m.logFields(false)).WithError(res.err).Error("Failed to resolve container network namespace")
	}
	return res.ns, res.err
}

func (m *dhcpManager) Stop() error {
	close(m.stopChan)
	if m.nsHandle != 0 { m.nsHandle.Close() }
	if m.netHandle != nil { m.netHandle.Delete() }
	return nil
}
