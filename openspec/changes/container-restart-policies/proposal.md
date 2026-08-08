# OpenSpec Change Proposal: Container Infrastructure Restart Policy Standardization

## Why

During maintenance events (such as network plugin updates or Docker daemon restarts), Docker containers configured with `--restart unless-stopped` that are stopped or evicted by network driver teardown will remain in an unstarted `STOPPED` state after the daemon boots up. Critical core infrastructure services (such as DNS resolution via `coredns`, database storage via `postgres`/`redis`, and edge routing via `haproxy`) fail to start automatically, causing cascading outages across dependent application containers.

Standardizing container restart policies based on service tier ensures system resilience and reliable self-healing upon boot or daemon recovery.

## What Changes

- Categorize host container deployment scripts in `/storage/docker/` into three distinct architectural tiers:
  1. **Core Infrastructure Tier (`--restart always`):** Critical core dependencies (`coredns`, `haproxy`, `postgres`, `redis`, `vault`, `cert-agent`, `librenms-db`).
  2. **Application Tier (`--restart unless-stopped`):** Long-running microservices (`iplayarr`, `sonarr`, `radarr`, `plex`, `jellyfin`, `transmission`, `freshrss`, `gitlab-ee`).
  3. **Batch / Ephemeral Tier (`--restart on-failure:3`):** Task-based containers (`certbot`, migration scripts).
- Update container creation commands in `/storage/docker/*/start.sh` scripts to reflect the standardized restart policy tiering.

## Capabilities

### New Capabilities

- `resilient-core-restart-policy`: Enforce `--restart always` for core network, identity, and database infrastructure to guarantee boot recovery.
- `ephemeral-task-restart-policy`: Enforce bounded retry limits (`--restart on-failure:3`) for batch and certificate management tasks.

## Impact

- Deployment start scripts in `/storage/docker/` (`coredns/start.sh`, `haproxy/start.sh`, `postgres/start.sh`, `redis/start.sh`, `vault/start.sh`, `certbot/start.sh`).
- Container runtime restart behavior across system reboots and Docker daemon restarts.
