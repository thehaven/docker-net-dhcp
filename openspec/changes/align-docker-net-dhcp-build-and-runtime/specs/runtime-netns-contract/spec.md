## ADDED Requirements

### Requirement: Runtime netns mount contract

The plugin configuration SHALL declare a bind mount from `/var/run/docker/netns` to `/var/run/docker/netns` with the required propagation semantics. Deployment integration SHALL provide a host-side preparation step that creates the source directory before Docker/plugin startup.

#### Scenario: Config has matching source and destination
- **WHEN** plugin configuration validation runs
- **THEN** it accepts only a source and destination both equal to `/var/run/docker/netns`

#### Scenario: Config has a mismatched mount
- **WHEN** the source or destination differs from `/var/run/docker/netns`
- **THEN** validation fails before the plugin artefact is deployed

#### Scenario: Host directory is absent before startup
- **WHEN** the host preparation step runs and `/var/run/docker/netns` is absent
- **THEN** it creates and validates the directory before Docker may enable the plugin

#### Scenario: Host directory cannot be prepared
- **WHEN** the source path is blocked by a conflicting non-directory or permissions error
- **THEN** preparation fails non-zero and does not claim the plugin is ready

### Requirement: Runtime fallback remains compatible

The plugin SHALL retain its PID-based namespace fallback for cases where the sandbox-key path cannot be accessed, while treating the host netns mount as the primary configured path.

#### Scenario: Mounted sandbox namespace is available
- **WHEN** the configured sandbox namespace path exists and is accessible
- **THEN** the plugin uses the mounted path

#### Scenario: Mounted sandbox namespace is unavailable
- **WHEN** the sandbox namespace path cannot be accessed but the container PID can be resolved
- **THEN** the plugin attempts the PID-based namespace path and records the fallback outcome
