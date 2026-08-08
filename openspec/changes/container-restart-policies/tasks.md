# Implementation Tasks: Container Infrastructure Restart Policy Standardization

- [x] **1. Core Infrastructure Restart Policies (`--restart always`)**
  - [x] Update `coredns` start script to `--restart always`.
  - [x] Update `haproxy` start script to `--restart always`.
  - [x] Update `postgres` start script to `--restart always`.
  - [x] Update `redis` start script to `--restart always`.
  - [x] Update `vault` start script to `--restart always`.
  - [x] Update `cert-agent` start script to `--restart always`.
  - [x] Update `librenms` database start script to `--restart always`.

- [x] **2. Ephemeral & Batch Task Policies (`--restart on-failure:3`)**
  - [x] Update `certbot` start script to `--restart on-failure:3`.

- [x] **3. Runtime Policy Update**
  - [x] Update live container restart policies via `docker update --restart`.
  - [x] Verify live container restart policies using `docker inspect`.
