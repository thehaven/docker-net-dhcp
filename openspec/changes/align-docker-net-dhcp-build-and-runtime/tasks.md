## 1. Build identity and toolchain

- [ ] 1.1 Pin the Dockerfile builder to a verified Go 1.26.1 Alpine-compatible image and set `CGO_ENABLED=0` explicitly.
- [ ] 1.2 Update build and release workflows from Go 1.24 to Go 1.26.1 and add a toolchain/CGO verification step.
- [ ] 1.3 Add Makefile verification targets that fail on wrong plugin repository, wrong Go version, or CGO-enabled production builds.
- [ ] 1.4 Audit README, CHANGELOG, scripts, examples, and deployment instructions; retain legacy references only in labelled compatibility/migration/cleanup contexts.

## 2. Runtime netns contract

- [ ] 2.1 Validate `config.json` and `plugin/config.json` source/destination/propagation for `/var/run/docker/netns`.
- [ ] 2.2 Add a host-side runtime preparation unit/template or packaging artefact that creates and validates `/var/run/docker/netns` before Docker/plugin startup.
- [ ] 2.3 Add tests for absent directory, existing directory, conflicting file, permission failure, and idempotent preparation.
- [ ] 2.4 Document systemd/OpenRC ordering and the relationship between runtime preparation, Docker, plugin enablement, and PID fallback.
- [ ] 2.5 Verify plugin runtime fallback tests still cover inaccessible sandbox-key paths and PID namespace recovery.

## 3. Migration-safe identity handling

- [ ] 3.1 Keep and test exact compatibility matching for both The Haven and devplayer0 plugin references without broad substring matching.
- [ ] 3.2 Add a read-only network inspection command/script or documented procedure reporting driver identity, endpoint count, and migration risk.
- [ ] 3.3 Add tests proving inspection and cache reconciliation never emit network/container deletion, recreation, attach, or detach operations.
- [ ] 3.4 Update `AGENTS.md` deployment steps to use the current identity and remove stale instructions that copy binaries into the legacy plugin.
- [ ] 3.5 Document controlled migration and rollback for `vlan107`; explicitly preserve existing production endpoint state during normal plugin deployment.

## 4. Verification and release gates

- [ ] 4.1 Ensure CI runs `go vet ./...` and `go test -race ./...` before rootfs assembly/publication.
- [ ] 4.2 Add artefact checks for expected config, entrypoint, architecture, Go build metadata, and CGO-disabled assumptions.
- [ ] 4.3 Run local Makefile build/test/verify targets with Go 1.26.1 and record outputs.
- [ ] 4.4 Run multi-architecture packaging in a dry-run or isolated registry path and verify all generated references use `ghcr.io/thehaven/docker-net-dhcp`.
- [ ] 4.5 Perform a read-only live-host validation against installed plugins and networks; do not delete or recreate `vlan107` or its containers.
