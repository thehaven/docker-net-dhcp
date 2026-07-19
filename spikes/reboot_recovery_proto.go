package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// EndpointState represents the cached state of an active endpoint.
type EndpointState struct {
	ID         string `json:"id"`
	SandboxKey string `json:"sandbox_key"`
	MacAddress string `json:"mac_address"`
	IP         string `json:"ip"`
	Gateway    string `json:"gateway"`
	Hostname   string `json:"hostname,omitempty"`
	SeedName   string `json:"seed_name,omitempty"`
}

// NetworkState represents the cached configuration of a network.
type NetworkState struct {
	ID        string                   `json:"id"`
	Endpoints map[string]EndpointState `json:"endpoints"`
}

// CreateEndpointRequest represents the incoming API call.
type CreateEndpointRequest struct {
	NetworkID  string
	EndpointID string
	MacAddress string
}

func main() {
	fmt.Println("=== Reboot Recovery Prototype (Cache & Post-Join) ===")

	// Mocking the persistent cache JSON
	mockCacheJSON := `{
		"vlan107": {
			"id": "vlan107",
			"endpoints": {
				"old-endpoint-id-12345": {
					"id": "old-endpoint-id-12345",
					"sandbox_key": "/var/run/docker/netns/abc",
					"mac_address": "02:42:ac:11:00:02",
					"ip": "192.168.107.50/24",
					"gateway": "192.168.107.253",
					"hostname": "test-container-1",
					"seed_name": "test-container-1"
				}
			}
		}
	}`

	var cache map[string]NetworkState
	err := json.Unmarshal([]byte(mockCacheJSON), &cache)
	if err != nil {
		fmt.Printf("Error loading cache: %v\n", err)
		return
	}

	// 1. Simulating CreateEndpoint on Reboot/Restart
	// Docker passes the same MAC address that was assigned before reboot.
	// But it generates a NEW EndpointID.
	req := CreateEndpointRequest{
		NetworkID:  "vlan107",
		EndpointID: "new-endpoint-id-99999",
		MacAddress: "02:42:ac:11:00:02", // Preserved by Docker
	}

	var resolvedSeed, resolvedHostname string
	state, found := cache[req.NetworkID]
	if found {
		// Proactive cache lookup by MAC address
		for _, ep := range state.Endpoints {
			if ep.MacAddress == req.MacAddress && ep.SeedName != "" {
				resolvedSeed = ep.SeedName
				resolvedHostname = ep.Hostname
				break
			}
		}
	}

	fmt.Printf("[CreateEndpoint] Incoming Request - EndpointID: %s, MAC: %s\n", req.EndpointID, req.MacAddress)
	if resolvedSeed != "" {
		fmt.Printf("[CreateEndpoint] SUCCESS: Resolved container name '%s' (hostname: '%s') from cache via MAC!\n", resolvedSeed, resolvedHostname)
	} else {
		fmt.Println("[CreateEndpoint] FAIL: Could not resolve container name via MAC.")
	}

	// 2. Simulating Post-Join Correction
	// If the lookup failed (or to confirm FIFO), we run a background check.
	fmt.Println("\n[Post-Join] Simulating background ContainerList correction...")
	
	// Mock container list returned by Docker API (once endpoint is committed)
	type MockContainerNetwork struct {
		EndpointID string
	}
	type MockContainer struct {
		Name     string
		Hostname string
		Networks map[string]MockContainerNetwork
	}

	mockContainers := []MockContainer{
		{
			Name:     "test-container-1",
			Hostname: "test-container-1",
			Networks: map[string]MockContainerNetwork{
				"vlan107": {EndpointID: "new-endpoint-id-99999"}, // Mapping now visible!
			},
		},
	}

	// The background correction routine
	var actualName, actualHostname string
	for _, c := range mockContainers {
		for _, netInfo := range c.Networks {
			if netInfo.EndpointID == req.EndpointID {
				actualName = c.Name
				actualHostname = c.Hostname
				break
			}
		}
	}

	if actualName != "" {
		fmt.Printf("[Post-Join] SUCCESS: Found container '%s' associated with EndpointID '%s'!\n", actualName, req.EndpointID)
		if actualName != resolvedSeed {
			fmt.Printf("[Post-Join] ACTION: Correcting MAC seed from '%s' to '%s'\n", resolvedSeed, actualName)
			resolvedSeed = actualName
			resolvedHostname = actualHostname
		} else {
			fmt.Println("[Post-Join] INFO: Cache mapping is already correct.")
		}
	} else {
		fmt.Println("[Post-Join] FAIL: Container not found for endpoint.")
	}

	time.Sleep(100 * time.Millisecond)
}
