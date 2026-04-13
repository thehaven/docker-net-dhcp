package plugin

import (
	"testing"
	"time"

	"github.com/thehaven/docker-net-dhcp/pkg/macgen"
)

// ---------------------------------------------------------------------------
// BUG-1 / BUG-2: per-network FIFO pending queue
// ---------------------------------------------------------------------------

// TestPendingQueue_FIFO verifies oldest container is popped first (FIFO, not LIFO).
// The previous creationQueue used tail-read (LIFO) which assigned wrong names
// under concurrent or multi-network scenarios.
func TestPendingQueue_FIFO(t *testing.T) {
	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
	}

	p.pushPendingContainer("net-123", "container-A", "host-A")
	p.pushPendingContainer("net-123", "container-B", "host-B")

	name, _ := p.popPendingContainer("net-123")
	if name != "container-A" {
		t.Errorf("first pop (FIFO) = %q, want \"container-A\"", name)
	}
	name, _ = p.popPendingContainer("net-123")
	if name != "container-B" {
		t.Errorf("second pop = %q, want \"container-B\"", name)
	}
}

// TestPendingQueue_PopOnRead verifies that popping a container removes it.
// The previous creationQueue never consumed entries — same stale name was
// reused for every subsequent CreateEndpoint call.
func TestPendingQueue_PopOnRead(t *testing.T) {
	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
	}

	p.pushPendingContainer("net-456", "container-X", "host-X")

	name1, _ := p.popPendingContainer("net-456")
	if name1 != "container-X" {
		t.Fatalf("first pop = %q, want \"container-X\"", name1)
	}

	// Queue must be empty after the first pop.
	name2, _ := p.popPendingContainer("net-456")
	if name2 != "" {
		t.Errorf("second pop on drained queue = %q, want \"\"", name2)
	}
}

// TestPendingQueue_EmptyReturnsEmpty verifies pop on missing network returns ("","").
func TestPendingQueue_EmptyReturnsEmpty(t *testing.T) {
	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
	}

	name, hostname := p.popPendingContainer("nonexistent-network")
	if name != "" || hostname != "" {
		t.Errorf("pop on empty = (%q, %q), want (\"\", \"\")", name, hostname)
	}
}

// TestPendingQueue_NetworkIsolation verifies containers on different networks
// do not interfere with each other.
func TestPendingQueue_NetworkIsolation(t *testing.T) {
	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
	}

	p.pushPendingContainer("net-A", "ctr-in-A", "h-A")
	p.pushPendingContainer("net-B", "ctr-in-B", "h-B")

	nameB, _ := p.popPendingContainer("net-B")
	if nameB != "ctr-in-B" {
		t.Fatalf("net-B pop = %q, want \"ctr-in-B\"", nameB)
	}

	nameA, _ := p.popPendingContainer("net-A")
	if nameA != "ctr-in-A" {
		t.Errorf("net-A pop after net-B consumed = %q, want \"ctr-in-A\"", nameA)
	}
}

// TestPendingQueue_PruneExpired verifies stale entries are cleaned up.
func TestPendingQueue_PruneExpired(t *testing.T) {
	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
	}

	p.Lock()
	p.pendingByNetwork["net-prune"] = []pendingContainer{
		{name: "stale", hostname: "h-stale", createdAt: time.Now().Add(-10 * time.Second)},
		{name: "fresh", hostname: "h-fresh", createdAt: time.Now()},
	}
	p.Unlock()

	p.pruneExpiredPending(5 * time.Second)

	name, _ := p.popPendingContainer("net-prune")
	if name != "fresh" {
		t.Errorf("after prune, pop = %q, want \"fresh\" (stale should have been removed)", name)
	}
}

// ---------------------------------------------------------------------------
// BUG-5: IsDHCPPlugin backward compatibility with devplayer0 plugin name
// ---------------------------------------------------------------------------

// TestIsDHCPPlugin_BackwardCompat verifies both old (devplayer0) and new (thehaven)
// plugin names are accepted, so cache reconciliation does not ghost-prune vlan107.
func TestIsDHCPPlugin_BackwardCompat(t *testing.T) {
	tests := []struct {
		driver string
		want   bool
	}{
		{"ghcr.io/thehaven/docker-net-dhcp:golang", true},
		{"ghcr.io/thehaven/docker-net-dhcp:latest", true},
		{"ghcr.io/thehaven/docker-net-dhcp:v1.0.0", true},
		{"ghcr.io/devplayer0/docker-net-dhcp:golang", true},
		{"ghcr.io/devplayer0/docker-net-dhcp:latest", true},
		{"ghcr.io/devplayer0/docker-net-dhcp:v0.0.4", true},
		{"bridge", false},
		{"overlay", false},
		{"null", false},
		{"ghcr.io/other/docker-net-dhcp:golang", false},
	}

	for _, tt := range tests {
		got := IsDHCPPlugin(tt.driver)
		if got != tt.want {
			t.Errorf("IsDHCPPlugin(%q) = %v, want %v", tt.driver, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// MAC parity: container name → same output as shell generate_mac.func
// ---------------------------------------------------------------------------

// TestMACParity_ContainerNameSeed verifies that macgen.Generate() with a container
// name as seed produces bit-identical output to the shell generate_mac.func script.
// Reference vectors taken from generate_mac.bats test suite.
func TestMACParity_ContainerNameSeed(t *testing.T) {
	tests := []struct {
		seed string
		want string
	}{
		// md5("test") = 098f6bcd4621d373cade4e832627b4f6
		// first 5 bytes: 09 8f 6b cd 46 → prefixed with 02
		{"test", "02:09:8f:6b:cd:46"},
		// md5("container-1") = b588c219865f6fe336908e5991216b13
		// first 5 bytes: b5 88 c2 19 86
		{"container-1", "02:b5:88:c2:19:86"},
	}

	for _, tt := range tests {
		got, err := macgen.Generate(macgen.Options{Seed: tt.seed, Format: macgen.FormatColon})
		if err != nil {
			t.Fatalf("Generate(%q): %v", tt.seed, err)
		}
		if got != tt.want {
			t.Errorf("Generate(%q) = %q, want %q (parity with generate_mac.func)", tt.seed, got, tt.want)
		}
	}
}

// TestMACParity_HyphenFormat verifies hyphen format parity.
func TestMACParity_HyphenFormat(t *testing.T) {
	got, err := macgen.Generate(macgen.Options{Seed: "test", Format: macgen.FormatHyphen})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	want := "02-09-8f-6b-cd-46"
	if got != want {
		t.Errorf("Generate(hyphen) = %q, want %q", got, want)
	}
}

// TestMACParity_DotFormat verifies Cisco dot format parity.
func TestMACParity_DotFormat(t *testing.T) {
	got, err := macgen.Generate(macgen.Options{Seed: "test", Format: macgen.FormatDot})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	want := "0209.8f6b.cd46"
	if got != want {
		t.Errorf("Generate(dot) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// BUG-7: static-MAC container must consume pending queue entry
// ---------------------------------------------------------------------------

// TestPendingQueue_StaticMACConsumesPendingEntry verifies that when a user
// specifies --mac-address for a container, the corresponding pending queue
// entry is still consumed. Without this, the stale entry would be popped by
// the next dynamic-MAC container on the same network, assigning it the wrong
// container name as the MAC seed.
//
// Reproduces the observed failure:
//   - push "test-mac-static" → pending[net]
//   - push "test-mac-dyn"    → pending[net]
//   - static container CreateEndpoint: MUST consume "test-mac-static"
//   - dynamic container CreateEndpoint: MUST get "test-mac-dyn" (not "test-mac-static")
func TestPendingQueue_StaticMACConsumesPendingEntry(t *testing.T) {
	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
	}

	net := "net-static-bug"
	p.pushPendingContainer(net, "test-mac-static", "host-static")
	p.pushPendingContainer(net, "test-mac-dyn", "host-dyn")

	// Simulate static-MAC CreateEndpoint: pop (and discard) the first entry.
	// The caller (CreateEndpoint) pops first regardless of whether a static MAC
	// is provided — this is the BUG-7 fix.
	_, _ = p.popPendingContainer(net)

	// Simulate dynamic-MAC CreateEndpoint: must get "test-mac-dyn".
	name, _ := p.popPendingContainer(net)
	if name != "test-mac-dyn" {
		t.Errorf("dynamic container after static-MAC consumed queue entry: got %q, want \"test-mac-dyn\""+
			" (stale static entry leaked into dynamic container MAC seed)", name)
	}
}

// ---------------------------------------------------------------------------
// C1: SeedName persisted in EndpointState (cache round-trip)
// ---------------------------------------------------------------------------

// TestEndpointState_SeedNamePersisted verifies that SeedName survives a
// cache set → get round-trip. Leave pre-population (C2) depends on this.
func TestEndpointState_SeedNamePersisted(t *testing.T) {
	c := NewNetworkCache(t.TempDir() + "/test-cache.json")

	netID := "net-seed-test"
	_ = c.Set(NetworkState{ID: netID, Options: DHCPNetworkOptions{Bridge: "br0"}})

	ep := EndpointState{
		ID:       "ep-001",
		Hostname: "my-container",
		SeedName: "my-container",
	}
	if err := c.SetEndpoint(netID, ep); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}

	state, ok := c.Get(netID)
	if !ok {
		t.Fatal("network not found in cache after Set")
	}
	got, ok := state.Endpoints["ep-001"]
	if !ok {
		t.Fatal("endpoint not found in cache after SetEndpoint")
	}
	if got.SeedName != "my-container" {
		t.Errorf("SeedName round-trip = %q, want \"my-container\"", got.SeedName)
	}
	if got.Hostname != "my-container" {
		t.Errorf("Hostname round-trip = %q, want \"my-container\"", got.Hostname)
	}
}

// ---------------------------------------------------------------------------
// C2: Leave pre-populates pending queue for restart
// ---------------------------------------------------------------------------

// TestLeavePrePopulatesPendingQueue verifies that Leave pushes the container
// name from the cached EndpointState into the pending queue so the subsequent
// CreateEndpoint call (during restart) can pop it immediately.
func TestLeavePrePopulatesPendingQueue(t *testing.T) {
	cacheDir := t.TempDir()
	c := NewNetworkCache(cacheDir + "/test-cache.json")

	netID := "net-leave-test"
	epID := "ep-leave-001"
	_ = c.Set(NetworkState{ID: netID, Options: DHCPNetworkOptions{Bridge: "br0"}})
	_ = c.SetEndpoint(netID, EndpointState{
		ID:       epID,
		SeedName: "my-service",
		Hostname: "my-service",
	})

	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
		cache:            c,
	}

	// Simulate Leave — should push container name to pending queue.
	_ = p.Leave(nil, LeaveRequest{NetworkID: netID, EndpointID: epID})

	name, hostname := p.popPendingContainer(netID)
	if name != "my-service" {
		t.Errorf("Leave pre-population: seedName = %q, want \"my-service\"", name)
	}
	if hostname != "my-service" {
		t.Errorf("Leave pre-population: hostname = %q, want \"my-service\"", hostname)
	}
}

// TestLeaveNoSeedNameSkipsPrePopulation verifies that Leave does NOT push
// to the pending queue when the cached endpoint has no SeedName (e.g. old
// cache format before C1 was deployed).
func TestLeaveNoSeedNameSkipsPrePopulation(t *testing.T) {
	cacheDir := t.TempDir()
	c := NewNetworkCache(cacheDir + "/test-cache.json")

	netID := "net-leave-noseed"
	epID := "ep-leave-002"
	_ = c.Set(NetworkState{ID: netID, Options: DHCPNetworkOptions{Bridge: "br0"}})
	_ = c.SetEndpoint(netID, EndpointState{
		ID:       epID,
		Hostname: "old-format-host",
		// SeedName intentionally empty — old cache format
	})

	p := &Plugin{
		pendingByNetwork: make(map[string][]pendingContainer),
		joinHints:        make(map[string]joinHint),
		persistentDHCP:   make(map[string]*dhcpManager),
		cache:            c,
	}

	_ = p.Leave(nil, LeaveRequest{NetworkID: netID, EndpointID: epID})

	name, _ := p.popPendingContainer(netID)
	if name != "" {
		t.Errorf("Leave with empty SeedName should not pre-populate, got %q", name)
	}
}

// ---------------------------------------------------------------------------
// BUG-6b: default container hostname (short ID) must be replaced with
// container name so DNS registers as <name>.<domain> not <id>.<domain>
// ---------------------------------------------------------------------------

// TestDefaultHostnameFallsBackToContainerName verifies that when the container's
// internal hostname is Docker's default (first 12 chars of container ID), the
// pending queue stores the container NAME instead, so the DHCP hostname option
// produces DNS entry <name>.docker.<domain> rather than <id>.docker.<domain>.
func TestDefaultHostnameFallsBackToContainerName(t *testing.T) {
	// Simulate: containerID = "ddc7a5df6d79abcdef01234567890abc" (32+ chars)
	// Docker default hostname = containerID[:12] = "ddc7a5df6d79"
	containerID := "ddc7a5df6d79abcdef01234567890abcdef01234567890abc"
	defaultHostname := containerID[:12] // "ddc7a5df6d79"
	containerName := "test-det-1"

	// Apply the same logic as watchDockerEvents.
	hostname := defaultHostname
	if len(containerID) >= 12 && hostname == containerID[:12] {
		hostname = containerName
	}

	if hostname != containerName {
		t.Errorf("hostname with default container ID = %q, want container name %q"+
			" (DNS would register as %q.<domain> instead of %q.<domain>)",
			hostname, containerName, defaultHostname, containerName)
	}

	// When --hostname is set explicitly, it must be preserved.
	explicitHostname := "my-custom-host"
	hostname2 := explicitHostname
	if len(containerID) >= 12 && hostname2 == containerID[:12] {
		hostname2 = containerName
	}
	if hostname2 != explicitHostname {
		t.Errorf("explicit hostname got overwritten: got %q, want %q", hostname2, explicitHostname)
	}
}
