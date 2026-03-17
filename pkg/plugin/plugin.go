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

	mux := http.NewServeMux()
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

func (p *Plugin) watchDockerEvents(ctx context.Context) {
	log.Info("Starting Docker event listener...")
	
	msgChan, errChan := p.docker.Events(ctx, events.ListOptions{})

	for {
		select {
		case msg := <-msgChan:
			if msg.Type == "container" && msg.Action == "create" {
				name := strings.TrimPrefix(msg.Actor.Attributes["name"], "/")
				log.WithField("name", name).Debug("Container created, adding to queue")
				
				p.Lock()
				p.creationQueue = append(p.creationQueue, name)
				// Keep queue small
				if len(p.creationQueue) > 10 {
					p.creationQueue = p.creationQueue[1:]
				}
				p.Unlock()
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
