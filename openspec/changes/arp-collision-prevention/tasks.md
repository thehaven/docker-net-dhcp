# Implementation Tasks: ARP Collision Prevention

- [ ] **1. udhcpc-handler ARP Conflict Detection**
  - [ ] Add L2 ARP probing logic to `cmd/udhcpc-handler/main.go` using socket/arping checks.
  - [ ] Implement exit code 1 behavior when an ARP reply is detected for an offered IP.
  - [ ] Emit structured `collision` event JSON to stdout before exiting on conflict.

- [ ] **2. Plugin DHCP Manager Collision Handling**
  - [ ] Update `pkg/plugin/dhcp_manager.go` to handle `collision` events in `processEvents`.
  - [ ] Add retry backoff logic when `udhcpc` reports IP collisions.

- [ ] **3. Phase 1 & 2 MAC Alignment**
  - [ ] Update `pkg/udhcpc/client.go` `DHCPClientOptions` to support explicit MAC address overrides.
  - [ ] Update `pkg/plugin/network.go` `CreateEndpoint` to pass deterministic MAC hints into `GetIP()`.

- [ ] **4. Testing & Verification**
  - [ ] Add unit tests in `pkg/udhcpc/client_test.go` verifying `DHCPDECLINE` exit codes.
  - [ ] Add end-to-end ARP collision simulation tests in `tests/`.
  - [ ] Verify `go vet ./...` and `go test -race ./...` pass cleanly.
