# docker-net-dhcp

`docker-net-dhcp` is a Docker network plugin that allocates IP addresses (IPv4 and optionally IPv6) via an existing DHCP server — for example, your router or a local dnsmasq instance.

Containers appear on your LAN as first-class hosts: they get a real IP from your DHCP server, register a hostname in DNS, and keep the same address across restarts.

---

## Quickstart

Install the plugin and create a network in two commands (replace `br0` with your host bridge):

```bash
docker plugin install ghcr.io/thehaven/docker-net-dhcp:release
docker network create -d ghcr.io/thehaven/docker-net-dhcp:release \
    --ipam-driver null -o bridge=br0 my-dhcp-net
```

Run a container:

```bash
docker run --rm -it --name my-app --network my-dhcp-net alpine ip addr show eth0
```

---

## Installation

```
$ docker plugin install ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64
Plugin "ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64" is requesting the following privileges:
 - network: [host]
 - host pid namespace: [true]
 - mount: [/var/run/docker.sock]
 - capabilities: [CAP_NET_ADMIN CAP_SYS_ADMIN CAP_SYS_PTRACE]
Do you grant the above permissions? [y/N] y
Installed plugin ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64
```

> If you see `invalid rootfs in image configuration`, upgrade your Docker installation.

### Available tags

Tags follow the pattern `<version>-<os>-<arch>`:

| Tag segment | Values |
|---|---|
| Version | `release` (latest release), `x.y.z` (pinned semver), `latest` (HEAD build) |
| OS | `linux` |
| Architecture | `amd64`, `arm64-v8`, `arm-v7`, `386` |

Example: `release-linux-arm64-v8` for the latest release on a 64-bit ARM host.

---

## Setting up a host bridge

`docker-net-dhcp` requires a pre-existing bridge interface on the host that is connected to the network where your DHCP server lives. A minimal setup:

```bash
# Create the bridge and bring it up
sudo ip link add my-bridge type bridge
sudo ip link set my-bridge up

# Attach your uplink (replace eth0 with your interface)
sudo ip link set eth0 up
sudo ip link set eth0 master my-bridge

# Allow forwarding if your firewall drops it by default
sudo iptables -A FORWARD -i my-bridge -j ACCEPT

# Obtain an IP for the host itself via DHCP
sudo dhcpcd my-bridge
```

---

## Creating a network

```bash
docker network create \
    -d ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64 \
    --ipam-driver null \
    -o bridge=my-bridge \
    my-dhcp-net
```

> The `--ipam-driver null` flag is **required**. Without it Docker allocates IPs from a private subnet, which conflicts with your DHCP server.

With IPv6:

```bash
docker network create \
    -d ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64 \
    --ipam-driver null \
    -o bridge=my-bridge \
    -o ipv6=true \
    my-dhcp-net
```

### Network options

| Option | Type | Default | Description |
|---|---|---|---|
| `bridge` | string | **required** | Host bridge interface to attach containers to |
| `ipv6` | bool | `false` | Enable IPv6 DHCP in addition to IPv4 |
| `lease_timeout` | duration | `30s` | How long to wait for the initial DHCP lease before failing the container start |
| `ignore_conflicts` | bool | `false` | Skip the check that prevents two networks sharing the same bridge |
| `skip_routes` | bool | `false` | Do not copy static routes from the host bridge into the container |
| `mac_format` | string | `colon` | MAC address format: `colon` (`02:xx:xx:xx:xx:xx`), `hyphen` (`02-xx-xx-xx-xx-xx`), or `dot` (`02xx.xxxx.xxxx`) |

---

## Running containers

```bash
docker run --rm -it --name my-app --network my-dhcp-net alpine
```

Inside the container the interface will be `eth0`:

```
1: lo: <LOOPBACK,UP,LOWER_UP> ...
    inet 127.0.0.1/8 ...
2: eth0@if42: <BROADCAST,MULTICAST,UP,LOWER_UP> ...
    link/ether 02:ab:cd:ef:12:34 brd ff:ff:ff:ff:ff:ff
    inet 192.168.1.50/24 brd 192.168.1.255 scope global eth0
```

Or with Docker Compose (network pre-created externally):

```yaml
services:
  app:
    image: alpine
    command: ip addr show eth0
    networks:
      dhcp:
        mac_address: 02:00:00:00:00:01
networks:
  dhcp:
    name: my-dhcp-net
    external: true
```

Or letting Compose manage the network:

```yaml
services:
  app:
    image: alpine
    networks:
      - dhcp
networks:
  dhcp:
    driver: ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64
    driver_opts:
      bridge: my-bridge
      ipv6: 'true'
      skip_routes: 'false'
    ipam:
      driver: 'null'
```

### Container options

| Option | Description |
|---|---|
| `--name <name>` | Container name — used as the MAC seed and DHCP hostname (see below) |
| `--hostname <host>` | Override the DHCP hostname independently of the container name |
| `--mac-address <mac>` | Pin a specific MAC address; takes precedence over deterministic generation |

---

## Deterministic MAC addresses

Every container without an explicit `--mac-address` receives a **deterministic MAC** derived from its name via MD5:

```
MAC = 02 : md5(container-name)[0:5]
```

The `02` prefix sets the locally-administered bit, marking it as a non-manufacturer address.

This means:
- The same container name always gets the same MAC address — and therefore the same DHCP-reserved IP.
- You can pre-calculate the MAC offline to create DHCP reservations before starting the container.
- Renaming a container intentionally changes its MAC and IP.

A helper CLI tool is included for offline calculation:

```bash
docker-net-dhcp-macgen my-container-name
# → 02:ab:cd:ef:12:34
```

To override with a fixed MAC (for existing DHCP reservations):

```bash
docker run --network my-dhcp-net --mac-address 02:de:ad:be:ef:01 --name myapp alpine
```

---

## DNS registration

The DHCP client sends the container name as the hostname in the DHCP request (Options 12 and 81). If your DHCP server is configured to update DNS from leases (e.g. dnsmasq with `--dhcp-fqdn`), the container will be reachable by name:

```bash
# Container started as: docker run --name my-app --network my-dhcp-net ...
ping my-app.docker.example.com
```

To use a different hostname from the container name, pass `--hostname`:

```bash
docker run --name my-app --hostname web-server --network my-dhcp-net alpine
```

---

## Persistence and warm recovery

The plugin persists active network and endpoint state to `/var/lib/docker-net-dhcp/networks.json`. On restart it re-reads this file and resumes DHCP lease renewals for all known containers — **no container restart required**.

This makes zero-interruption plugin upgrades possible:

```bash
# Upgrade without stopping containers
sudo docker plugin disable ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64
# (copy new binary into plugin rootfs)
sudo docker plugin enable ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64
```

---

## Debugging

Read the plugin log:

```bash
sudo cat /var/lib/docker/plugins/*/rootfs/var/log/net-dhcp.log
```

Enable trace logging:

```bash
docker plugin set ghcr.io/thehaven/docker-net-dhcp:release-linux-amd64 LOG_LEVEL=trace
```

---

## How it works

`docker-net-dhcp` uses the same `veth`-pair mechanism as Docker's built-in `bridge` driver, but hands off IP allocation to an external DHCP server instead of managing a private subnet.

### Container start sequence

1. Docker requests a new endpoint from the plugin.
2. The plugin creates a `veth` pair and connects the host end to your bridge.
3. A `udhcpc` DHCP client runs on the container end (still in the host network namespace) to obtain an initial IP address. The plugin returns this IP to Docker.
4. Docker moves the container end of the `veth` into the container's network namespace and sets the IP address.
5. The plugin starts a persistent `udhcpc` inside the **container's network namespace** (but in the plugin's PID namespace, so the container cannot see or kill it). This client handles lease renewals for the lifetime of the container.

### Deterministic MAC sequence

1. When a container is created (`docker run`), the plugin's Docker event listener captures the container name immediately.
2. When Docker calls `CreateEndpoint`, the plugin pops the container name from its queue and generates the deterministic MAC via `md5(name)[0:5]` prefixed with `02`.
3. If a user-supplied `--mac-address` is present, it is used as-is and the deterministic MAC is discarded.
