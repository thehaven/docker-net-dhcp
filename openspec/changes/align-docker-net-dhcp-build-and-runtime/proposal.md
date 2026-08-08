## Why

The host boot incident exposed a gap between this repository’s current The Haven build identity and the deployed runtime: the source defaults and CI publish `ghcr.io/thehaven/docker-net-dhcp:golang`, while production still uses the legacy devplayer0 plugin and `vlan107` remains bound to it. The plugin also relies on `/var/run/docker/netns`, a runtime directory that Docker did not create before plugin startup, and the repository’s CI/toolchain instructions are inconsistent. This change makes the artefact, runtime contract, deployment documentation, and migration safeguards internally consistent without mutating production networks automatically.

## What Changes

- Pin the plugin build toolchain to Go 1.26.1 with `CGO_ENABLED=0` for Alpine compatibility.
- Align Dockerfile and GitHub Actions build/release workflows with the required toolchain and verify the built binary.
- Make the The Haven image identity authoritative for local and CI artefacts.
- Remove stale deployment instructions that direct operators to copy binaries into the legacy plugin.
- Add an explicit runtime netns preparation/deployment contract for `/var/run/docker/netns`.
- Add migration-safe diagnostics for networks using the legacy driver identity.
- Preserve compatibility parsing for existing legacy network state while preventing accidental ghost-pruning.
- Add tests and documentation for plugin identity, mount requirements, boot recovery, and controlled network migration.
- **BREAKING**: New deployment instructions no longer treat `ghcr.io/devplayer0/docker-net-dhcp:golang` as the default plugin to enable or update.
- **BREAKING**: The project will not automatically recreate or migrate `vlan107`.

## Capabilities

### New Capabilities

- `reproducible-plugin-build`: Reproducible, pinned, Alpine-compatible plugin builds and publication under the The Haven image identity.
- `runtime-netns-contract`: Explicit preparation and validation contract for the Docker network namespace bind mount required by the plugin.
- `safe-driver-migration`: Read-only detection and documented, controlled migration handling for legacy and current Docker network driver identities.

### Modified Capabilities

<!-- No existing OpenSpec capability specifications exist in this repository. -->

## Impact

- `Makefile`, `Dockerfile`, `.github/workflows/build.yaml`, `.github/workflows/release.yaml`.
- `AGENTS.md`, README, CHANGELOG, deployment/migration documentation, and test fixtures.
- `config.json` and plugin packaging metadata where runtime mount validation is needed.
- CI toolchain, published GHCR artefacts, local plugin creation, and operator deployment workflow.
- Existing Docker networks and containers remain untouched by this repository change; host migration is an operational follow-up.
