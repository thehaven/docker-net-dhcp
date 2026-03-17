package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/docker/docker/api/types/network"
	log "github.com/sirupsen/logrus"
)

// EndpointState represents the cached state of an active endpoint.
type EndpointState struct {
	ID         string `json:"id"`
	SandboxKey string `json:"sandbox_key"`
	MacAddress string `json:"mac_address"`
	IP         string `json:"ip"`
	IPv6       string `json:"ipv6,omitempty"`
	Gateway    string `json:"gateway"`
}

// ContainerMetadata stores stable seeds for MAC generation
type ContainerMetadata struct {
	Name      string    `json:"name"`
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"created_at"`
}

// NetworkState represents the cached configuration of a Docker network and its active endpoints.
type NetworkState struct {
	ID        string                   `json:"id"`
	Options   DHCPNetworkOptions       `json:"options"`
	Endpoints map[string]EndpointState `json:"endpoints,omitempty"`
	// Mapping of EndpointID to ContainerID for deterministic seeding
	ContainerMap map[string]string `json:"container_map,omitempty"`
	// Mapping of EndpointID to Metadata (Name/Hostname) for deterministic seeding
	MetadataMap map[string]ContainerMetadata `json:"metadata_map,omitempty"`
}

// NetworkCache manages the local, persistent state of network configurations.
type NetworkCache struct {
	sync.RWMutex
	filePath string
	networks map[string]NetworkState
}

// NewNetworkCache creates a new NetworkCache, initializing it from the backing file if present.
func NewNetworkCache(filePath string) *NetworkCache {
	c := &NetworkCache{
		filePath: filePath,
		networks: make(map[string]NetworkState),
	}
	c.load()
	return c
}

// load reads the cache from disk.
func (c *NetworkCache) load() {
	c.Lock()
	defer c.Unlock()

	data, err := os.ReadFile(c.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("Failed to read network cache file")
		}
		return
	}

	if err := json.Unmarshal(data, &c.networks); err != nil {
		log.WithError(err).Warn("Failed to parse network cache file")
	}
}

// save atomically writes the cache to disk.
func (c *NetworkCache) save() error {
	data, err := json.MarshalIndent(c.networks, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal network cache: %w", err)
	}

	dir := filepath.Dir(c.filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	tmpFile := c.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write temporary cache file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile, c.filePath); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to commit cache file: %w", err)
	}

	return nil
}

// Get retrieves a network state from the cache.
func (c *NetworkCache) Get(id string) (NetworkState, bool) {
	c.RLock()
	defer c.RUnlock()
	state, ok := c.networks[id]
	if ok {
		if state.Endpoints == nil {
			state.Endpoints = make(map[string]EndpointState)
		}
		if state.MetadataMap == nil {
			state.MetadataMap = make(map[string]ContainerMetadata)
		}
	}
	return state, ok
}

// GetMetadata retrieves container metadata for an endpoint from the cache.
func (c *NetworkCache) GetMetadata(networkID, endpointID string) (ContainerMetadata, bool) {
	c.RLock()
	defer c.RUnlock()
	net, ok := c.networks[networkID]
	if !ok || net.MetadataMap == nil {
		return ContainerMetadata{}, false
	}
	meta, found := net.MetadataMap[endpointID]
	return meta, found
}

// SetMetadata adds or updates a container's metadata mapping in the cache and persists it.
func (c *NetworkCache) SetMetadata(networkID, endpointID string, meta ContainerMetadata) {
	c.Lock()
	defer c.Unlock()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now()
	}
	net, ok := c.networks[networkID]
	if !ok {
		// Create a placeholder network entry if it doesn't exist (e.g. 'global')
		net = NetworkState{
			ID:        networkID,
			Endpoints: make(map[string]EndpointState),
		}
	}
	if net.MetadataMap == nil {
		net.MetadataMap = make(map[string]ContainerMetadata)
	}
	net.MetadataMap[endpointID] = meta
	c.networks[networkID] = net
	_ = c.save()
}

// PruneMetadata removes metadata entries older than the specified duration.
func (c *NetworkCache) PruneMetadata(maxAge time.Duration) {
	c.Lock()
	defer c.Unlock()

	now := time.Now()
	changed := false

	for netID, net := range c.networks {
		if net.MetadataMap == nil {
			continue
		}
		for epID, meta := range net.MetadataMap {
			if now.Sub(meta.CreatedAt) > maxAge {
				log.WithFields(log.Fields{
					"network":  netID,
					"endpoint": epID,
					"age":      now.Sub(meta.CreatedAt),
				}).Debug("Pruning expired metadata")
				delete(net.MetadataMap, epID)
				changed = true
			}
		}
		if changed {
			c.networks[netID] = net
		}
	}

	if changed {
		_ = c.save()
	}
}

// GetAll returns all cached networks.
func (c *NetworkCache) GetAll() []NetworkState {
	c.RLock()
	defer c.RUnlock()
	nets := make([]NetworkState, 0, len(c.networks))
	for _, n := range c.networks {
		nets = append(nets, n)
	}
	return nets
}

// Set adds or updates a network state in the cache and persists to disk.
func (c *NetworkCache) Set(state NetworkState) error {
	c.Lock()
	defer c.Unlock()
	if state.Endpoints == nil {
		if existing, ok := c.networks[state.ID]; ok {
			state.Endpoints = existing.Endpoints
		} else {
			state.Endpoints = make(map[string]EndpointState)
		}
	}
	if state.MetadataMap == nil {
		if existing, ok := c.networks[state.ID]; ok {
			state.MetadataMap = existing.MetadataMap
		} else {
			state.MetadataMap = make(map[string]ContainerMetadata)
		}
	}
	c.networks[state.ID] = state
	return c.save()
}

// SetEndpoint adds or updates an endpoint state within a network.
func (c *NetworkCache) SetEndpoint(networkID string, ep EndpointState) error {
	c.Lock()
	defer c.Unlock()
	net, ok := c.networks[networkID]
	if !ok {
		return fmt.Errorf("network %s not found in cache", networkID)
	}
	if net.Endpoints == nil {
		net.Endpoints = make(map[string]EndpointState)
	}
	net.Endpoints[ep.ID] = ep
	c.networks[networkID] = net
	return c.save()
}

// DeleteEndpoint removes an endpoint state from a network.
func (c *NetworkCache) DeleteEndpoint(networkID string, endpointID string) error {
	c.Lock()
	defer c.Unlock()
	net, ok := c.networks[networkID]
	if !ok {
		return nil
	}
	if net.Endpoints != nil {
		delete(net.Endpoints, endpointID)
	}
	if net.MetadataMap != nil {
		delete(net.MetadataMap, endpointID)
	}
	c.networks[networkID] = net
	return c.save()
}

// Delete removes a network state from the cache and persists to disk.
func (c *NetworkCache) Delete(id string) error {
	c.Lock()
	defer c.Unlock()
	delete(c.networks, id)
	return c.save()
}

// Reconcile synchronizes the local cache with the Docker daemon's actual networks.
func (c *NetworkCache) Reconcile(ctx context.Context, dockerClient interface {
	NetworkList(ctx context.Context, options network.ListOptions) ([]network.Inspect, error)
}, initialDelay time.Duration) {
	if initialDelay > 0 {
		select {
		case <-time.After(initialDelay):
		case <-ctx.Done():
			return
		}
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	log.Info("Starting network cache reconciliation...")
	networks, err := dockerClient.NetworkList(ctxTimeout, network.ListOptions{})
	if err != nil {
		log.WithError(err).Warn("Failed to list Docker networks for reconciliation")
		return
	}

	dockerNets := make(map[string]network.Inspect)
	for _, n := range networks {
		if IsDHCPPlugin(n.Driver) {
			dockerNets[n.ID] = n
		}
	}

	c.Lock()
	defer c.Unlock()

	changed := false
	for id := range c.networks {
		if id == "global" { continue }
		if _, ok := dockerNets[id]; !ok {
			log.WithField("network", id).Info("Removing ghost network from cache")
			delete(c.networks, id)
			changed = true
		}
	}

	for id, n := range dockerNets {
		existing, ok := c.networks[id]
		if !ok {
			log.WithField("network", id).Info("Found existing Docker network, populating cache")
			opts, err := decodeOpts(n.Options)
			if err != nil {
				log.WithError(err).WithField("network", id).Warn("Failed to decode existing network options")
				continue
			}
			existing = NetworkState{
				ID:        id,
				Options:   opts,
				Endpoints: make(map[string]EndpointState),
			}
			changed = true
		}

		newMap := make(map[string]string)
		for ctrID, epInfo := range n.Containers {
			newMap[epInfo.EndpointID] = ctrID
		}
		existing.ContainerMap = newMap
		c.networks[id] = existing
	}

	if changed {
		_ = c.save()
	}
}

func (c *NetworkCache) ReconcileLoop(ctx context.Context, dockerClient interface {
	NetworkList(ctx context.Context, options network.ListOptions) ([]network.Inspect, error)
}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.Reconcile(ctx, dockerClient, 0)
		case <-ctx.Done():
			return
		}
	}
}
