## Context

The repository’s `Makefile` defaults to `ghcr.io/thehaven/docker-net-dhcp`, while the host has `ghcr.io/devplayer0/docker-net-dhcp:golang` installed and `vlan107` uses that legacy driver. A second empty `thehaven-vlan107` network exists with the current driver. The plugin configuration bind-mounts `/var/run/docker/netns`, but the directory was absent during Docker startup; the plugin failed to enable and Docker 29.5.2 subsequently panicked while restoring the remote driver.

The repository instructions require Go 1.26.1 and `CGO_ENABLED=0`, but the Dockerfile uses unpinned `golang:alpine` and both GitHub workflows use Go 1.24. The project is a Go Docker network plugin with a plugin rootfs assembled through the Makefile and multi-architecture publication scripts. Existing compatibility code intentionally recognises both plugin identities.

## Goals / Non-Goals

**Goals:**

- Produce plugin artefacts under `ghcr.io/thehaven/docker-net-dhcp` with a pinned, tested toolchain.
- Make the `/var/run/docker/netns` mount requirement explicit, validated, and deployable across reboots.
- Keep legacy driver/network state recognisable during a controlled migration.
- Prevent build, deployment, or documentation steps from silently selecting the old identity.
- Add automated checks for image identity, toolchain, CGO, mount metadata, and migration safety.

**Non-Goals:**

- Deleting the installed devplayer0 plugin.
- Recreating or changing `vlan107`.
- Moving production containers to `thehaven-vlan107`.
- Making Docker Engine resilient to its observed nil-pointer panic.
- Changing DHCP allocation, deterministic MAC, or endpoint lifecycle algorithms unrelated to the incident.

## Decisions

### 1. Pin the build toolchain at every build boundary

Use Go 1.26.1 for the Dockerfile builder and CI workflows. Set `CGO_ENABLED=0` explicitly for the plugin binary. Add build metadata checks that fail if the configured Go version or CGO mode drifts.

**Rationale:** The repository documents Go 1.26.1 as a production requirement and reports Go 1.24 binaries crash-looping inside the Alpine plugin container. Unpinned `golang:alpine` is not reproducible.

**Alternative rejected:** Keeping `golang:alpine` and relying on the current tag; a moving tag can silently change Go and Alpine versions.

### 2. Centralise the image identity

Retain `PLUGIN_NAME ?= ghcr.io/thehaven/docker-net-dhcp` as the single default and propagate it through local Make targets, CI, release scripts, examples, and deployment instructions. Legacy references may remain only in explicit compatibility detection and cleanup/migration documentation.

**Rationale:** The host currently has both identities represented in network state. A single authoritative default prevents future accidental legacy deployments.

**Alternative rejected:** A repository-wide blind replacement; compatibility parsing and migration diagnostics need to retain the legacy value deliberately.

### 3. Treat runtime netns preparation as a host integration contract

Document and package a preparation unit or equivalent installation hook that creates `/var/run/docker/netns` before Docker starts. Keep the plugin config mount unchanged and add validation/tests proving source and destination match. The plugin repository may provide the unit/template or a documented integration artefact, but it must not attempt to create host directories from inside the plugin container.

**Rationale:** The plugin cannot fix a missing host path after Docker begins plugin restoration. Host boot ordering is the correct boundary.

**Alternative rejected:** Removing the mount and relying only on PID namespace fallback; the mount remains part of the normal fast path and is required by existing deployments.

### 4. Preserve compatibility but do not mutate networks

Keep `IsDHCPPlugin` recognition for both `ghcr.io/thehaven/docker-net-dhcp:*` and `ghcr.io/devplayer0/docker-net-dhcp:*` so existing state is not ghost-pruned. Add read-only tooling/tests to identify the driver attached to a network and produce migration instructions. Do not add code that deletes/recreates networks or silently changes drivers.

**Rationale:** Existing `vlan107` has active production endpoints. The plugin can remain compatible while a separate operator-controlled migration is planned.

**Alternative rejected:** Automatically replacing the old network on plugin enablement; this risks a service-wide outage.

### 5. Make deployment identity and version verifiable

Add an artefact verification step that checks the image/reference, plugin config, binary architecture, Go version/build metadata where available, and `CGO_ENABLED=0` build assumptions before rootfs installation or publication. CI must run tests and `go vet` before packaging.

**Rationale:** The failure mode is partly provenance drift: source says The Haven, host says devplayer0, and CI uses the wrong Go version.

### 6. Update operator documentation as part of the change

Rewrite `AGENTS.md`, README deployment examples, and changelog/deployment notes so normal deployment uses The Haven, boot preparation is explicit, and legacy network migration is a separate maintenance operation. Include rollback and verification commands without prescribing destructive network deletion.

## Risks / Trade-offs

- [Risk] Pinning Go/Alpine may require availability of the exact builder image. → Mitigation: validate the pinned image in CI and document the local Go 1.26.1 fallback.
- [Risk] Existing operators may follow old cached instructions. → Mitigation: remove stale legacy deployment steps and add a prominent migration warning.
- [Risk] Compatibility code could mask an incomplete migration. → Mitigation: expose exact driver identity and add explicit read-only mismatch checks.
- [Risk] Runtime directory permissions may differ across hosts. → Mitigation: make the preparation mode configurable, test the default, and verify with Docker/plugin startup in a controlled environment.
- [Risk] Multi-arch publication scripts may not preserve the intended image name. → Mitigation: add end-to-end dry-run/assertion tests around generated references.

## Migration Plan

1. Land build and documentation changes without touching the live Docker daemon.
2. Build and test a The Haven plugin artefact using Go 1.26.1 and `CGO_ENABLED=0`.
3. Install the runtime netns preparation unit and verify it on a reboot or isolated Docker restart.
4. Perform a read-only host inventory: installed plugins, network drivers, containers attached to `vlan107`, and the empty/current `thehaven-vlan107` network.
5. During a maintenance window, install/enable the The Haven plugin according to the updater proposal while preserving `vlan107`.
6. Migrate networks and containers only through a separately approved operational change.
7. Roll back code/artefacts by restoring the previous plugin reference and binary, without deleting network state; if runtime startup fails, disable the new plugin and use the documented legacy compatibility procedure.

## Open Questions

- Should the runtime preparation unit live in this repository, the docker-updater repository, or the Gentoo ebuild/package integration?
- Which exact Go 1.26.1 + Alpine image digest is available and acceptable for CI?
- Should the The Haven plugin use `:golang`, `:release`, or immutable version/SHA tags for production deployment?
- What operator-owned procedure will migrate `vlan107` without downtime or loss of endpoint/MAC state?
- Should CI publish a compatibility alias for the old image, or must all legacy publication stop immediately?
