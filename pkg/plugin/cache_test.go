package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/network"
)

type mockDockerClient struct {
	networks []network.Inspect
	delay    time.Duration
}

func (m *mockDockerClient) NetworkList(ctx context.Context, options network.ListOptions) ([]network.Inspect, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.networks, nil
}

func TestNetworkCache(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "docker-net-dhcp-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cacheFile := filepath.Join(tmpDir, "networks.json")
	cache := NewNetworkCache(cacheFile)

	// Test Set and Get
	err = cache.Set(NetworkState{
		ID: "test-net-1",
		Options: DHCPNetworkOptions{
			Bridge: "br0",
			IPv6:   true,
		},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	state, ok := cache.Get("test-net-1")
	if !ok {
		t.Fatalf("Get() missing state")
	}
	if state.Options.Bridge != "br0" {
		t.Errorf("Get() state.Options.Bridge = %v, want br0", state.Options.Bridge)
	}

	// Test Persist and Reload
	cache2 := NewNetworkCache(cacheFile)
	state2, ok := cache2.Get("test-net-1")
	if !ok {
		t.Fatalf("Reloaded cache missing state")
	}
	if state2.Options.Bridge != "br0" {
		t.Errorf("Reloaded cache state.Options.Bridge = %v, want br0", state2.Options.Bridge)
	}

	// Test Delete
	err = cache.Delete("test-net-1")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, ok = cache.Get("test-net-1")
	if ok {
		t.Fatalf("Get() found state after Delete()")
	}

	// Test Reconcile (Ghost Network)
	cache.Set(NetworkState{ID: "ghost-net", Options: DHCPNetworkOptions{Bridge: "br1"}})
	cache.Set(NetworkState{ID: "real-net", Options: DHCPNetworkOptions{Bridge: "br2"}})

	mockDocker := &mockDockerClient{
		networks: []network.Inspect{
			{ID: "real-net", Driver: "ghcr.io/thehaven/docker-net-dhcp:latest"},
		},
		delay: 0,
	}

	cache.Reconcile(context.Background(), mockDocker, 0)

	_, ok = cache.Get("ghost-net")
	if ok {
		t.Errorf("Reconcile() failed to remove ghost network")
	}

	_, ok = cache.Get("real-net")
	if !ok {
		t.Errorf("Reconcile() incorrectly removed real network")
	}
}
