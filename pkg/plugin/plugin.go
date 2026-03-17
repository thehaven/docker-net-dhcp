package plugin

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
	docker "github.com/docker/docker/client"
	"github.com/gorilla/handlers"
	"github.com/mitchellh/mapstructure"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"

	"github.com/thehaven/docker-net-dhcp/pkg/util"
)

// DriverName is the name of the Docker Network Driver
const DriverName string = "net-dhcp"

const defaultLeaseTimeout = 30 * time.Second

// Both the canonical (thehaven) and legacy (devplayer0) plugin names are accepted
// so that cache reconciliation does not ghost-prune networks created under the
// old name (e.g. vlan107 using ghcr.io/devplayer0/docker-net-dhcp:golang).
var (
	driverRegexpNew = regexp.MustCompile(`^ghcr\.io/thehaven/docker-net-dhcp:.+$`)
	driverRegexpOld = regexp.MustCompile(`^ghcr\.io/devplayer0/docker-net-dhcp:.+$`)
)

// IsDHCPPlugin checks if a Docker network driver is an instance of this plugin.
func IsDHCPPlugin(driver string) bool {
	return driverRegexpNew.MatchString(driver) || driverRegexpOld.MatchString(driver)
}

// DHCPNetworkOptions contains options for the DHCP network driver
type DHCPNetworkOptions struct {
	Bridge          string
	IPv6            bool
	LeaseTimeout    time.Duration `mapstructure:"lease_timeout"`
	IgnoreConflicts bool          `mapstructure:"ignore_conflicts"`
	SkipRoutes      bool          `mapstructure:"skip_routes"`
	MacFormat       string        `mapstructure:"mac_format"`
}

func decodeOpts(input interface{}) (DHCPNetworkOptions, error) {
	var opts DHCPNetworkOptions
	optsDecoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           &opts,
		ErrorUnused:      true,
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
		),
	})
	if err != nil {
		return opts, fmt.Errorf("failed to create options decoder: %w", err)
	}
	if err := optsDecoder.Decode(input); err != nil {
		return opts, err
	}
	return opts, nil
}

type joinHint struct {
	IPv4     *netlink.Addr
	IPv6     *netlink.Addr
	Gateway  string
	Hostname string // container hostname, propagated to DHCP Option 12 / Option 81
}

// pendingContainer holds transient info about a container that has been
// created but not yet assigned an endpoint. Used to correlate container
// names with CreateEndpoint calls for deterministic MAC seeding and
// DHCP hostname registration.
type pendingContainer struct {
	name      string
	hostname  string
	createdAt time.Time
}

// Plugin is the DHCP network plugin
type Plugin struct {
	awaitTimeout time.Duration

	docker *docker.Client
	server http.Server

	sync.RWMutex
	joinHints      map[string]joinHint
	persistentDHCP map[string]*dhcpManager

	cache *NetworkCache

	// Per-network FIFO queue of recently created containers awaiting endpoint
	// assignment. Keyed by Docker network UUID. Populated by watchDockerEvents,
	// consumed (popped) by CreateEndpoint. This eliminates the previous global
	// LIFO creationQueue that caused incorrect MAC seed assignment.
	pendingByNetwork map[string][]pendingContainer
}

// NewPlugin creates a new Plugin
func NewPlugin(awaitTimeout time.Duration) (*Plugin, error) {
	client, err := docker.NewClientWithOpts(
		docker.WithAPIVersionNegotiation(),
		docker.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	p := &Plugin{
		awaitTimeout:     awaitTimeout,
		docker:           client,
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
		cache:            NewNetworkCache("/var/lib/docker-net-dhcp/networks.json"),
		pendingByNetwork: make(map[string][]pendingContainer),
	}

	// Immediate reconciliation to populate cache before recovery runs.
	p.cache.Reconcile(context.Background(), client, 0)

	go p.cache.ReconcileLoop(context.Background(), client, 5*time.Minute)
	go p.Recover(context.Background())
	go p.watchDockerEvents(context.Background())
	go p.scavengerLoop(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("/health", p.apiHealth)
	mux.HandleFunc("/NetworkDriver.GetCapabilities", p.apiGetCapabilities)

	mux.HandleFunc("/NetworkDriver.CreateNetwork", p.apiCreateNetwork)
	mux.HandleFunc("/NetworkDriver.DeleteNetwork", p.apiDeleteNetwork)

	mux.HandleFunc("/NetworkDriver.CreateEndpoint", p.apiCreateEndpoint)
	mux.HandleFunc("/NetworkDriver.EndpointOperInfo", p.apiEndpointOperInfo)
	mux.HandleFunc("/NetworkDriver.DeleteEndpoint", p.apiDeleteEndpoint)

	mux.HandleFunc("/NetworkDriver.Join", p.apiJoin)
	mux.HandleFunc("/NetworkDriver.Leave", p.apiLeave)

	p.server = http.Server{
		Handler: handlers.CustomLoggingHandler(nil, mux, util.WriteAccessLog),
	}

	return p, nil
}

// Listen starts the plugin server
func (p *Plugin) Listen(bindSock string) error {
	l, err := net.Listen("unix", bindSock)
	if err != nil {
		return err
	}
	return p.server.Serve(l)
}

// Close stops the plugin server
func (p *Plugin) Close() error {
	if err := p.docker.Close(); err != nil {
		return fmt.Errorf("failed to close docker client: %w", err)
	}
	if err := p.server.Close(); err != nil {
		return fmt.Errorf("failed to close http server: %w", err)
	}
	return nil
}

func (p *Plugin) apiHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// ---------------------------------------------------------------------------
// Pending container queue — per-network FIFO
// ---------------------------------------------------------------------------

// pushPendingContainer enqueues a container into the FIFO pending list for
// the given Docker network UUID.
func (p *Plugin) pushPendingContainer(networkID, name, hostname string) {
	p.Lock()
	defer p.Unlock()
	p.pendingByNetwork[networkID] = append(p.pendingByNetwork[networkID], pendingContainer{
		name:      name,
		hostname:  hostname,
		createdAt: time.Now(),
	})
}

// popPendingContainer removes and returns the oldest pending container for
// the given network UUID. Returns ("", "") when the queue is empty.
func (p *Plugin) popPendingContainer(networkID string) (name, hostname string) {
	p.Lock()
	defer p.Unlock()
	queue := p.pendingByNetwork[networkID]
	if len(queue) == 0 {
		return "", ""
	}
	pc := queue[0]
	p.pendingByNetwork[networkID] = queue[1:]
	return pc.name, pc.hostname
}

// pruneExpiredPending removes pending container entries older than maxAge.
func (p *Plugin) pruneExpiredPending(maxAge time.Duration) {
	p.Lock()
	defer p.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for netID, queue := range p.pendingByNetwork {
		fresh := queue[:0]
		for _, pc := range queue {
			if pc.createdAt.After(cutoff) {
				fresh = append(fresh, pc)
			}
		}
		if len(fresh) == 0 {
			delete(p.pendingByNetwork, netID)
		} else {
			p.pendingByNetwork[netID] = fresh
		}
	}
}

// ---------------------------------------------------------------------------
// Docker event listener
// ---------------------------------------------------------------------------

// watchDockerEvents listens for container create events and pre-populates the
// per-network pending queue so that CreateEndpoint can resolve the container
// name for deterministic MAC generation.
func (p *Plugin) watchDockerEvents(ctx context.Context) {
	log.Info("Starting Docker event listener for container name pre-caching...")
	msgChan, errChan := p.docker.Events(ctx, events.ListOptions{})

	for {
		select {
		case msg := <-msgChan:
			if msg.Type == "container" && msg.Action == "create" {
				name := strings.TrimPrefix(msg.Actor.Attributes["name"], "/")

				inspectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				ctr, err := p.docker.ContainerInspect(inspectCtx, msg.Actor.ID)
				cancel()
				if err != nil {
					log.WithError(err).WithField("name", name).Debug("Could not inspect created container for seed caching")
					continue
				}

				hostname := ctr.Config.Hostname
				// Docker defaults the internal hostname to the short container ID
				// (first 12 hex chars) when the user does not set --hostname.
				// Using that as the DHCP hostname would register the container ID in
				// DNS instead of the container name.  Fall back to the container name
				// so `docker run --name foo …` produces DNS entry foo.<domain>.
				if len(ctr.ID) >= 12 && hostname == ctr.ID[:12] {
					hostname = name
				}

				// Docker only allows a single --network at container creation time.
				// Additional networks are connected via `docker network connect`
				// which fires a separate "network connect" event (not handled here
				// since CreateEndpoint for multi-network requires a separate pending
				// entry — out of scope for now). The primary network is in
				// HostConfig.NetworkMode.
				netName := string(ctr.HostConfig.NetworkMode)
				if netName != "" && netName != "default" && netName != "bridge" &&
					netName != "host" && netName != "none" {
					p.registerPendingForNetwork(ctx, netName, name, hostname)
				}
			}
		case err := <-errChan:
			if err != nil && ctx.Err() == nil {
				log.WithError(err).Error("Docker event listener error, restarting in 5s...")
				time.Sleep(5 * time.Second)
				go p.watchDockerEvents(ctx)
				return
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

// registerPendingForNetwork resolves a network name-or-ID to its UUID, verifies
// it is a DHCP plugin network, and pushes the container into the pending queue.
// Returns true on success.
func (p *Plugin) registerPendingForNetwork(ctx context.Context, netNameOrID, containerName, hostname string) bool {
	netCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	n, err := p.docker.NetworkInspect(netCtx, netNameOrID, network.InspectOptions{})
	cancel()
	if err != nil {
		log.WithError(err).WithField("network", netNameOrID).Debug("Could not resolve network for pending container")
		return false
	}
	if !IsDHCPPlugin(n.Driver) {
		return false
	}
	p.pushPendingContainer(n.ID, containerName, hostname)
	log.WithFields(log.Fields{
		"container": containerName,
		"hostname":  hostname,
		"network":   netNameOrID,
		"networkID": n.ID[:12],
	}).Debug("Queued pending container for MAC seed lookup")
	return true
}

// scavengerLoop periodically removes stale entries from the pending queue.
// Entries older than 2 minutes are guaranteed to be uncollected (CreateEndpoint
// runs within milliseconds of container create), so pruning them is safe.
func (p *Plugin) scavengerLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.pruneExpiredPending(2 * time.Minute)
		case <-ctx.Done():
			return
		}
	}
}
