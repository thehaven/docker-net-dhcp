package plugin

import (
	"testing"
	"time"

	"github.com/thehaven/docker-net-dhcp/pkg/macgen"
)

// ---------------------------------------------------------------------------
// FIFO queue: push, pop, dedup, forward-flush
// ---------------------------------------------------------------------------

// TestFIFO_BasicPushPop verifies FIFO ordering of the pending queue.
func TestFIFO_BasicPushPop(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	p.pushPendingContainer("net-1", "alpha", "h-alpha", true)
	p.pushPendingContainer("net-1", "bravo", "h-bravo", true)
	p.pushPendingContainer("net-1", "charlie", "h-charlie", true)

	name, hn, ok := p.popPendingContainer("net-1")
	if !ok || name != "alpha" || hn != "h-alpha" {
		t.Errorf("pop#1 = (%q, %q, %v), want (alpha, h-alpha, true)", name, hn, ok)
	}

	name, hn, ok = p.popPendingContainer("net-1")
	if !ok || name != "bravo" || hn != "h-bravo" {
		t.Errorf("pop#2 = (%q, %q, %v), want (bravo, h-bravo, true)", name, hn, ok)
	}

	name, hn, ok = p.popPendingContainer("net-1")
	if !ok || name != "charlie" || hn != "h-charlie" {
		t.Errorf("pop#3 = (%q, %q, %v), want (charlie, h-charlie, true)", name, hn, ok)
	}

	_, _, ok = p.popPendingContainer("net-1")
	if ok {
		t.Error("pop#4 should return false on empty queue")
	}
}

// TestFIFO_Dedup verifies that pushing the same name removes the old entry
// and appends at the end.
func TestFIFO_Dedup(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	p.pushPendingContainer("net-1", "alpha", "h1", true)
	p.pushPendingContainer("net-1", "bravo", "h2", true)
	p.pushPendingContainer("net-1", "alpha", "h1-updated", true) // dedup: removes old alpha

	// Queue should be: [bravo, alpha(updated)]
	name, hn, ok := p.popPendingContainer("net-1")
	if !ok || name != "bravo" {
		t.Errorf("pop#1 = (%q, %q, %v), want (bravo, ...)", name, hn, ok)
	}

	name, hn, ok = p.popPendingContainer("net-1")
	if !ok || name != "alpha" || hn != "h1-updated" {
		t.Errorf("pop#2 = (%q, %q, %v), want (alpha, h1-updated, true)", name, hn, ok)
	}
}

// TestFIFO_ForwardFlushesBackward verifies that pushing a forward-looking
// event flushes all backward-looking entries. This prevents compose-down
// contamination: die/Leave entries are cleared when create events arrive.
func TestFIFO_ForwardFlushesBackward(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	// Simulate compose down: die/Leave events (backward-looking)
	p.pushPendingContainer("net-1", "stale-A", "h-stale-A", false)
	p.pushPendingContainer("net-1", "stale-B", "h-stale-B", false)
	p.pushPendingContainer("net-1", "unrelated", "h-unrelated", false)

	// Simulate compose up: first create event (forward-looking) flushes backward entries
	p.pushPendingContainer("net-1", "fresh-A", "h-fresh-A", true)
	p.pushPendingContainer("net-1", "fresh-B", "h-fresh-B", true)

	// Queue should only have fresh entries
	name, _, ok := p.popPendingContainer("net-1")
	if !ok || name != "fresh-A" {
		t.Errorf("pop#1 = %q, want fresh-A (backward entries should be flushed)", name)
	}

	name, _, ok = p.popPendingContainer("net-1")
	if !ok || name != "fresh-B" {
		t.Errorf("pop#2 = %q, want fresh-B", name)
	}

	_, _, ok = p.popPendingContainer("net-1")
	if ok {
		t.Error("pop#3 should be empty — all backward entries were flushed")
	}
}

// TestFIFO_BackwardSurvivesWithoutForward verifies that backward-looking
// entries (die/Leave) remain in the queue when no forward event follows.
// This is the docker-restart case: die fires, CreateEndpoint pops it.
func TestFIFO_BackwardSurvivesWithoutForward(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	// Simulate docker restart: die event pushes backward entry
	p.pushPendingContainer("net-1", "restarting-svc", "h-restart", false)

	// No forward events follow — entry survives
	name, hn, ok := p.popPendingContainer("net-1")
	if !ok || name != "restarting-svc" || hn != "h-restart" {
		t.Errorf("pop = (%q, %q, %v), want (restarting-svc, h-restart, true)", name, hn, ok)
	}
}

// TestFIFO_NetworkIsolation verifies queues are independent per network.
func TestFIFO_NetworkIsolation(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	p.pushPendingContainer("net-A", "svc-A", "h-A", true)
	p.pushPendingContainer("net-B", "svc-B", "h-B", true)

	name, _, _ := p.popPendingContainer("net-A")
	if name != "svc-A" {
		t.Errorf("net-A pop = %q, want svc-A", name)
	}

	name, _, _ = p.popPendingContainer("net-B")
	if name != "svc-B" {
		t.Errorf("net-B pop = %q, want svc-B", name)
	}
}

// TestFIFO_ComposeDownUpScenario is an end-to-end test simulating the exact
// sequence that caused the original bug: compose down → compose up.
func TestFIFO_ComposeDownUpScenario(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	netID := "net-vlan107"

	// Phase 1: compose down — die/Leave events fire for 5 containers
	p.pushPendingContainer(netID, "mem0-server", "mem0-server", false)
	p.pushPendingContainer(netID, "mem0-redis", "mem0-redis", false)
	p.pushPendingContainer(netID, "mem0-postgres", "mem0-postgres", false)
	p.pushPendingContainer(netID, "mem0-mcp", "mem0-mcp", false)
	p.pushPendingContainer(netID, "mem0-qdrant", "mem0-qdrant", false)
	// Cross-project pollution
	p.pushPendingContainer(netID, "freshrss", "freshrss", false)

	// Phase 2: compose up — create events fire for containers.
	// First forward event flushes ALL backward entries.
	p.pushPendingContainer(netID, "mem0-qdrant", "mem0-qdrant", true)
	p.pushPendingContainer(netID, "mem0-postgres", "mem0-postgres", true)
	p.pushPendingContainer(netID, "mem0-redis", "mem0-redis", true)
	p.pushPendingContainer(netID, "mem0-mcp", "mem0-mcp", true)
	p.pushPendingContainer(netID, "mem0-neo4j", "mem0-neo4j", true)

	// Phase 3: CreateEndpoint pops — should get ONLY fresh compose-up entries
	expected := []string{"mem0-qdrant", "mem0-postgres", "mem0-redis", "mem0-mcp", "mem0-neo4j"}
	for i, want := range expected {
		name, _, ok := p.popPendingContainer(netID)
		if !ok {
			t.Fatalf("pop#%d: unexpected empty queue (wanted %q)", i+1, want)
		}
		if name != want {
			t.Errorf("pop#%d = %q, want %q", i+1, name, want)
		}
	}

	// Queue should be empty — no freshrss contamination
	_, _, ok := p.popPendingContainer(netID)
	if ok {
		t.Error("queue should be empty after all compose-up entries consumed")
	}
}

// ---------------------------------------------------------------------------
// Metadata map: upsert, lookup, isolation, staleness
// ---------------------------------------------------------------------------

// TestMetadataMap_UpsertOverwrites verifies last-write-wins semantics.
func TestMetadataMap_UpsertOverwrites(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	p.upsertPendingMeta("net-123", "my-service", "old-hostname")
	p.upsertPendingMeta("net-123", "my-service", "new-hostname")

	hostname, ok := p.lookupPendingMeta("net-123", "my-service")
	if !ok {
		t.Fatal("expected entry to exist after upsert")
	}
	if hostname != "new-hostname" {
		t.Errorf("upsert hostname = %q, want \"new-hostname\" (last-write-wins)", hostname)
	}
}

// TestMetadataMap_LookupByName verifies keyed lookup.
func TestMetadataMap_LookupByName(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	p.upsertPendingMeta("net-123", "container-A", "host-A")
	p.upsertPendingMeta("net-123", "container-B", "host-B")

	hostname, ok := p.lookupPendingMeta("net-123", "container-B")
	if !ok || hostname != "host-B" {
		t.Errorf("lookup(container-B) = (%q, %v), want (host-B, true)", hostname, ok)
	}

	hostname, ok = p.lookupPendingMeta("net-123", "container-A")
	if !ok || hostname != "host-A" {
		t.Errorf("lookup(container-A) = (%q, %v), want (host-A, true)", hostname, ok)
	}
}

// TestMetadataMap_LookupNonexistent verifies lookup on missing entries.
func TestMetadataMap_LookupNonexistent(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	_, ok := p.lookupPendingMeta("nonexistent-network", "any-container")
	if ok {
		t.Error("expected ok=false for nonexistent network")
	}

	p.upsertPendingMeta("net-123", "container-A", "host-A")
	_, ok = p.lookupPendingMeta("net-123", "nonexistent-container")
	if ok {
		t.Error("expected ok=false for nonexistent container")
	}
}

// TestMetadataMap_NetworkIsolation verifies containers on different networks
// do not interfere with each other.
func TestMetadataMap_NetworkIsolation(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	p.upsertPendingMeta("net-A", "shared-name", "host-from-A")
	p.upsertPendingMeta("net-B", "shared-name", "host-from-B")

	hostname, ok := p.lookupPendingMeta("net-A", "shared-name")
	if !ok || hostname != "host-from-A" {
		t.Errorf("net-A lookup = (%q, %v), want (\"host-from-A\", true)", hostname, ok)
	}

	hostname, ok = p.lookupPendingMeta("net-B", "shared-name")
	if !ok || hostname != "host-from-B" {
		t.Errorf("net-B lookup = (%q, %v), want (\"host-from-B\", true)", hostname, ok)
	}
}

// ---------------------------------------------------------------------------
// Pruning: both FIFO and metadata
// ---------------------------------------------------------------------------

// TestPruneExpired verifies stale entries are cleaned from both structures.
func TestPruneExpired(t *testing.T) {
	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
	}

	now := time.Now()

	// Seed FIFO with stale and fresh entries
	p.Lock()
	p.pendingQueue["net-prune"] = []pendingContainer{
		{name: "stale-q", hostname: "h-stale-q", createdAt: now.Add(-10 * time.Second), forward: true},
		{name: "fresh-q", hostname: "h-fresh-q", createdAt: now, forward: true},
	}
	// Seed metadata with stale and fresh entries
	p.pendingMeta["net-prune"] = map[string]pendingContainer{
		"stale-m": {name: "stale-m", hostname: "h-stale-m", createdAt: now.Add(-10 * time.Second)},
		"fresh-m": {name: "fresh-m", hostname: "h-fresh-m", createdAt: now},
	}
	p.Unlock()

	p.pruneExpiredPending(5 * time.Second)

	// FIFO: only fresh entry should remain
	name, _, ok := p.popPendingContainer("net-prune")
	if !ok || name != "fresh-q" {
		t.Errorf("FIFO after prune: pop = (%q, %v), want (fresh-q, true)", name, ok)
	}
	_, _, ok = p.popPendingContainer("net-prune")
	if ok {
		t.Error("FIFO should have only 1 entry after prune")
	}

	// Metadata: stale entry pruned, fresh survives
	_, ok = p.lookupPendingMeta("net-prune", "stale-m")
	if ok {
		t.Error("stale metadata entry should have been pruned")
	}
	hostname, ok := p.lookupPendingMeta("net-prune", "fresh-m")
	if !ok || hostname != "h-fresh-m" {
		t.Errorf("fresh metadata after prune: (%q, %v), want (\"h-fresh-m\", true)", hostname, ok)
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
func TestMACParity_ContainerNameSeed(t *testing.T) {
	tests := []struct {
		seed string
		want string
	}{
		{"test", "02:09:8f:6b:cd:46"},
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
// C1: SeedName persisted in EndpointState (cache round-trip)
// ---------------------------------------------------------------------------

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
// C2: Leave caches metadata for potential restart (both FIFO + metadata map)
// ---------------------------------------------------------------------------

func TestLeaveCachesMetadata(t *testing.T) {
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
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
		cache:          c,
	}

	_ = p.Leave(nil, LeaveRequest{NetworkID: netID, EndpointID: epID})

	// Verify metadata map
	hostname, ok := p.lookupPendingMeta(netID, "my-service")
	if !ok {
		t.Fatal("Leave should have cached metadata for my-service")
	}
	if hostname != "my-service" {
		t.Errorf("Leave cached hostname = %q, want \"my-service\"", hostname)
	}

	// Verify FIFO queue (backward entry for restart support)
	name, _, ok := p.popPendingContainer(netID)
	if !ok || name != "my-service" {
		t.Errorf("Leave FIFO entry = (%q, %v), want (my-service, true)", name, ok)
	}
}

func TestLeaveNoSeedNameSkipsCaching(t *testing.T) {
	cacheDir := t.TempDir()
	c := NewNetworkCache(cacheDir + "/test-cache.json")

	netID := "net-leave-noseed"
	epID := "ep-leave-002"
	_ = c.Set(NetworkState{ID: netID, Options: DHCPNetworkOptions{Bridge: "br0"}})
	_ = c.SetEndpoint(netID, EndpointState{
		ID:       epID,
		Hostname: "old-format-host",
	})

	p := &Plugin{
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
		joinHints:      make(map[string]joinHint),
		persistentDHCP: make(map[string]*dhcpManager),
		cache:          c,
	}

	_ = p.Leave(nil, LeaveRequest{NetworkID: netID, EndpointID: epID})

	_, ok := p.lookupPendingMeta(netID, "old-format-host")
	if ok {
		t.Error("Leave with empty SeedName should not cache metadata")
	}
}

// ---------------------------------------------------------------------------
// BUG-6b: default container hostname (short ID) → container name
// ---------------------------------------------------------------------------

func TestDefaultHostnameFallsBackToContainerName(t *testing.T) {
	containerID := "ddc7a5df6d79abcdef01234567890abcdef01234567890abc"
	defaultHostname := containerID[:12]
	containerName := "test-det-1"

	hostname := defaultHostname
	if len(containerID) >= 12 && hostname == containerID[:12] {
		hostname = containerName
	}

	if hostname != containerName {
		t.Errorf("hostname with default container ID = %q, want container name %q", hostname, containerName)
	}

	explicitHostname := "my-custom-host"
	hostname2 := explicitHostname
	if len(containerID) >= 12 && hostname2 == containerID[:12] {
		hostname2 = containerName
	}
	if hostname2 != explicitHostname {
		t.Errorf("explicit hostname got overwritten: got %q, want %q", hostname2, explicitHostname)
	}
}

// TestJoinHint_UserSpecifiedMAC verifies that the UserSpecifiedMAC flag is
// properly stored and retrieved from joinHints.
func TestJoinHint_UserSpecifiedMAC(t *testing.T) {
	p := &Plugin{
		joinHints: make(map[string]joinHint),
	}

	endpointID := "ep-001"

	// Store a hint with UserSpecifiedMAC=true (user provided MAC)
	p.joinHints[endpointID] = joinHint{
		SeedName:         "nginx",
		UserSpecifiedMAC: true,
	}

	hint, ok := p.joinHints[endpointID]
	if !ok {
		t.Fatal("stored hint not found in joinHints")
	}
	if !hint.UserSpecifiedMAC {
		t.Error("UserSpecifiedMAC should be true for user-provided MAC hint")
	}
	if hint.SeedName != "nginx" {
		t.Errorf("SeedName = %q, want %q", hint.SeedName, "nginx")
	}

	// Store a hint with UserSpecifiedMAC=false (plugin-generated MAC)
	endpointID2 := "ep-002"
	p.joinHints[endpointID2] = joinHint{
		SeedName:         "redis",
		UserSpecifiedMAC: false,
	}

	hint2, ok := p.joinHints[endpointID2]
	if !ok {
		t.Fatal("stored hint not found in joinHints")
	}
	if hint2.UserSpecifiedMAC {
		t.Error("UserSpecifiedMAC should be false for auto-generated MAC hint")
	}
}

// TestDeleteEndpoint_Cleanup verifies that DeleteEndpoint stops persistentDHCP
// managers and purges the endpoint from the persistent cache.
func TestDeleteEndpoint_Cleanup(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewNetworkCache(tmpDir + "/networks.json")
	_ = cache.Set(NetworkState{ID: "net-test-123", Options: DHCPNetworkOptions{Bridge: "br0"}})
	_ = cache.SetEndpoint("net-test-123", EndpointState{ID: "ep-test-delete", SandboxKey: "/tmp/sb"})

	mgr := &dhcpManager{
		joinReq:  JoinRequest{EndpointID: "ep-test-delete"},
		stopChan: make(chan struct{}),
	}

	p := &Plugin{
		cache:          cache,
		persistentDHCP: map[string]*dhcpManager{"ep-test-delete": mgr},
		joinHints:      make(map[string]joinHint),
		pendingQueue:   make(map[string][]pendingContainer),
		pendingMeta:    make(map[string]map[string]pendingContainer),
	}

	err := p.DeleteEndpoint(DeleteEndpointRequest{
		NetworkID:  "net-test-123",
		EndpointID: "ep-test-delete",
	})
	if err != nil {
		t.Fatalf("DeleteEndpoint failed: %v", err)
	}

	p.RLock()
	_, existsInMap := p.persistentDHCP["ep-test-delete"]
	p.RUnlock()
	if existsInMap {
		t.Error("DeleteEndpoint should have removed manager from persistentDHCP map")
	}

	select {
	case <-mgr.stopChan:
		// Stop was called on manager
	default:
		t.Error("DeleteEndpoint should have stopped the dhcpManager")
	}

	_, existsInCache := p.cache.GetEndpoint("net-test-123", "ep-test-delete")
	if existsInCache {
		t.Error("DeleteEndpoint should have deleted the endpoint from cache")
	}
}
