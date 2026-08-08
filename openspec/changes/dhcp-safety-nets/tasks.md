# Implementation Tasks: Advanced DHCP Safety Nets

- [ ] **1. udhcpc-handler Gratuitous ARP (GARP) Broadcasts (Safety Net 1)**
  - [ ] Implement `sendGARP(ifaceName, ipStr)` in `cmd/udhcpc-handler/main.go`.
  - [ ] Emit 2 GARP Reply frames post-verification on `bound`/`renew`.

- [ ] **2. Environment Parsing & Fallbacks (Safety Net 2)**
  - [ ] Add fallback logic for unpopulated or malformed `$mask` and `$router` environment variables.

- [ ] **3. Multi-Probe ARP Probing (Safety Net 3)**
  - [ ] Upgrade `hasConflict()` in `cmd/udhcpc-handler/main.go` to transmit 3 spaced ARP probes.

- [ ] **4. Collision Backoff Throttling (Safety Net 4)**
  - [ ] Add `collisionCount` tracking and backoff logic to `pkg/plugin/dhcp_manager.go`.

- [ ] **5. IPv6 DAD Probe (Safety Net 5)**
  - [ ] Implement ICMPv6 NS/NA duplicate address check in `cmd/udhcpc-handler/main.go` for IPv6 `bound` events.

- [ ] **6. Testing & Quality Verification**
  - [ ] Add unit test fixtures for environment fallbacks and multi-probe collision logic in `pkg/udhcpc`.
  - [ ] Run `go vet ./...` and `go test -race ./...`.
