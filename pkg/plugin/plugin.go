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
	docker "github.com/docker/docker/client"
	"github.com/gorilla/handlers"
	"github.com/mitchellh/mapstructure"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"

	"github.com/devplayer0/docker-net-dhcp/pkg/util"
)

// DriverName is the name of the Docker Network Driver
const DriverName string = "net-dhcp"

const defaultLeaseTimeout = 30 * time.Second

var driverRegexp = regexp.MustCompile(`^ghcr\.io/devplayer0/docker-net-dhcp:.+$`)

// IsDHCPPlugin checks if a Docker network driver is an instance of this plugin
func IsDHCPPlugin(driver string) bool {
	return driverRegexp.MatchString(driver)
}

// DHCPNetworkOptions contains options for the DHCP network driver
type DHCPNetworkOptions struct {
	Bridge          string
	IPv6            bool
	LeaseTimeout    time.Duration `mapstructure:"lease_timeout"`
	IgnoreConflicts bool          `mapstructure:"ignore_conflicts"`
	SkipRoutes      bool          `mapstructure:"skip_routes"`
	MacSeedSource   string        `mapstructure:"mac_seed_source"`
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
	IPv4    *netlink.Addr
	IPv6    *netlink.Addr
	Gateway string
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
	
	// Ordered Queue of recently created container names for deterministic matching
	creationQueue []string
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
		awaitTimeout: awaitTimeout,

		docker: client,

		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),

		cache: NewNetworkCache("/var/lib/docker-net-dhcp/networks.json"),
		
		creationQueue: make([]string, 0),
	}

	// First reconciliation (immediate) to populate cache for recovery
	p.cache.Reconcile(context.Background(), client, 0)

	// Background tasks
	go p.cache.ReconcileLoop(context.Background(), client, 10*time.Second)
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

func (p *Plugin) watchDockerEvents(ctx context.Context) {
	log.Info("Starting Docker event listener for metadata discovery...")
	
	msgChan, errChan := p.docker.Events(ctx, events.ListOptions{})

	for {
		select {
		case msg := <-msgChan:
			if msg.Type == "container" && msg.Action == "create" {
				name := strings.TrimPrefix(msg.Actor.Attributes["name"], "/")
				log.WithField("name", name).Debug("Container created, adding to queue and proactive capture")
				
				p.Lock()
				p.creationQueue = append(p.creationQueue, name)
				if len(p.creationQueue) > 10 {
					p.creationQueue = p.creationQueue[1:]
				}
				p.Unlock()

				inspectCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
				ctr, err := p.docker.ContainerInspect(inspectCtx, msg.Actor.ID)
				cancel()
				if err == nil {
					// Map ContainerID to Name/Hostname immediately in global map
					p.cache.SetMetadata("global", msg.Actor.ID, ContainerMetadata{
						Name:      name,
						Hostname:  ctr.Config.Hostname,
						CreatedAt: time.Now(),
					})
					// Also start watchdog to link to EndpointID later
					go p.endpointWatchdog(ctx, msg.Actor.ID, name)
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

func (p *Plugin) endpointWatchdog(ctx context.Context, containerID, name string) {
	// Poll the container's network settings until an EndpointID appears for our driver
	limit := time.After(10 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			inspectCtx, cancel := context.WithTimeout(ctx, 1 * time.Second)
			ctr, err := p.docker.ContainerInspect(inspectCtx, containerID)
			cancel()
			if err != nil {
				if docker.IsErrNotFound(err) { return }
				continue
			}

			found := false
			for netID, netSettings := range ctr.NetworkSettings.Networks {
				if netSettings.EndpointID != "" {
					p.cache.SetMetadata(netID, netSettings.EndpointID, ContainerMetadata{
						Name:      name,
						Hostname:  ctr.Config.Hostname,
						CreatedAt: time.Now(),
					})
					found = true
				}
			}
			if found { return }
		case <-limit:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (p *Plugin) scavengerLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Prune metadata older than 10 minutes
			p.cache.PruneMetadata(10 * time.Minute)
			
			// Also prune creationQueue
			p.Lock()
			if len(p.creationQueue) > 0 {
				// Creation queue is just for very fresh containers, 
				// we can clear it if it's too old but it's already capped at 10 items.
			}
			p.Unlock()
		case <-ctx.Done():
			return
		}
	}
}
