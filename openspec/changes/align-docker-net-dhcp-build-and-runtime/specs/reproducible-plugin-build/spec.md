## ADDED Requirements

### Requirement: Authoritative The Haven image identity

Local Make targets, CI workflows, release scripts, and normal deployment documentation SHALL use `ghcr.io/thehaven/docker-net-dhcp` as the default image repository. The legacy devplayer0 repository SHALL appear only in explicit compatibility, migration, or cleanup contexts.

#### Scenario: Local plugin build uses current identity
- **WHEN** a developer runs the default local plugin build/create flow
- **THEN** the generated rootfs/plugin reference uses `ghcr.io/thehaven/docker-net-dhcp:golang`

#### Scenario: CI build uses current identity
- **WHEN** the build or release workflow publishes an artefact
- **THEN** every generated tag uses `ghcr.io/thehaven/docker-net-dhcp`

#### Scenario: Legacy reference is found in normal deployment instructions
- **WHEN** repository validation scans normal build/deployment instructions
- **THEN** it fails or reports the stale legacy reference unless the occurrence is marked compatibility/migration/cleanup

### Requirement: Pinned production toolchain

The production plugin binary SHALL be built with Go 1.26.1 and `CGO_ENABLED=0`. The Dockerfile and CI workflows SHALL pin or otherwise verify the required toolchain rather than relying on an unpinned `golang:alpine` tag or Go 1.24.

#### Scenario: Correct toolchain is configured
- **WHEN** CI or a local production build runs
- **THEN** the build uses Go 1.26.1 with CGO disabled

#### Scenario: Toolchain drifts
- **WHEN** the configured builder uses Go 1.24, an unpinned Go image, or CGO-enabled compilation
- **THEN** the verification gate fails before plugin publication or rootfs installation

### Requirement: Build verification before publication

The build pipeline SHALL run `go vet ./...` and race-enabled tests before copying the binary into the plugin rootfs or publishing a multi-architecture plugin. It SHALL verify that the expected plugin config and binary artefact are present.

#### Scenario: Verification passes
- **WHEN** vet, tests, toolchain checks, and config checks pass
- **THEN** the pipeline may assemble and publish the plugin

#### Scenario: Verification fails
- **WHEN** any required static analysis, test, toolchain, or config check fails
- **THEN** the pipeline fails before publication
