# Podman Network Isolation for Skiff Containers

## Problem

Skiff containers run untrusted AI coding agents. They currently use `HTTP_PROXY`/`HTTPS_PROXY` environment variables pointing to Gate, but a prompt injection could bypass the proxy by making direct TCP connections. We need network-level enforcement so Skiff containers can ONLY reach Alcove services (Gate, Hail, Ledger, Bridge) and NEVER reach the internet or any external host directly.

## Environment

- Fedora 43, rootless podman 5.8.1
- Network backend: netavark (with aardvark-dns)
- All containers run in the user's rootless namespace

---

## Approach 1: `--internal` Bridge Network (RECOMMENDED)

### How It Works

Podman's `podman network create --internal` flag creates a bridge network where netavark:

1. **Does not add a default route** to containers -- the container's routing table only has a route to the local subnet (e.g., `10.89.2.0/24 dev eth0`), with no `default via ...` entry.
2. **Blocks IP forwarding at the nftables level** in the rootless network namespace -- even if a container has `CAP_NET_ADMIN` and manually adds a default route via the gateway, the traffic is dropped by nftables rules before it can be forwarded to the internet.
3. **Restricts aardvark-dns** to only resolve container names on the same network -- external domain names return `NXDOMAIN`.

### Verified Behavior (tested on this system)

| Test | Result |
|------|--------|
| Container on internal network pings 8.8.8.8 | `Network unreachable` |
| Container on internal network pings host real IP (192.168.x.x) | `Network unreachable` |
| Container on internal network resolves `google.com` | `NXDOMAIN` |
| Container on internal network resolves container names on same network | Works |
| Container on internal network pings another container on same network | Works |
| Container with `CAP_NET_ADMIN` adds default route then pings 8.8.8.8 | Packets dropped (0% received) |
| Container on internal network accesses `host.containers.internal` | `Network unreachable` |
| Container on internal network accesses services on a different podman network | `Network unreachable` (even by IP) |
| Gateway IP (e.g., 10.89.2.1) is pingable | Yes (aardvark-dns runs there) |
| Host services bound to 0.0.0.0 reachable via gateway IP | No (different network namespace in rootless mode) |

### Security Properties

- **No route bypass**: Adding routes requires `CAP_NET_ADMIN` (not granted by default), and even if granted, nftables still blocks forwarding.
- **No DNS leaks**: aardvark-dns only resolves container names on the same internal network; external lookups return `NXDOMAIN`.
- **No host access**: `host.containers.internal` is unreachable. The gateway IP is pingable but runs only aardvark-dns, not host services (rootless podman runs the bridge in a separate network namespace from the host).
- **No cross-network access**: Containers on the internal network cannot reach containers on other podman networks, even by IP.

### Commands

```bash
# Create the internal network (no external routing)
podman network create --internal alcove-internal

# Create the external network (normal bridge with internet)
podman network create alcove-external

# Start Gate on BOTH networks (can reach internet AND internal services)
podman run -d --rm \
  --name gate-$TASK_ID \
  --network alcove-internal,alcove-external \
  localhost/alcove-gate:dev

# Start Skiff on internal network ONLY (no internet access)
podman run -d --rm \
  --name skiff-$TASK_ID \
  --network alcove-internal \
  --env HTTP_PROXY=http://gate-$TASK_ID:8443 \
  --env HTTPS_PROXY=http://gate-$TASK_ID:8443 \
  localhost/alcove-skiff:dev
```

### Connecting Infrastructure Services

NATS (Hail) and PostgreSQL (Ledger) must also be on the internal network for Skiff to reach them. Two options:

**Option A: Start services on both networks** (simplest)
```bash
podman run -d --name alcove-hail \
  --network alcove-internal,alcove-external \
  -p 4222:4222 \
  docker.io/library/nats:latest

# Or connect existing containers:
podman network connect alcove-internal alcove-hail
podman network connect alcove-internal alcove-ledger
```

**Option B: Skiff accesses NATS/PostgreSQL through Gate proxy** (more secure but more complex -- Gate would need to proxy non-HTTP protocols).

### Rootless Compatibility

Fully works with rootless podman. Tested on this system.

### Limitations

- The bridge gateway IP is pingable (aardvark-dns). This is not a security risk since no host services are exposed there in rootless mode.
- Services that Skiff needs to reach directly (NATS, PostgreSQL) must be attached to the internal network. The `podman network connect` command can add running containers to additional networks.
- Podman bridge networking has some performance overhead vs pasta/slirp4netns, but this is negligible for the use case.

### Recommendation

**This is the recommended approach.** It is already implemented in the Alcove codebase (`internal/runtime/podman.go`). It provides defense-in-depth: even if `HTTP_PROXY` is bypassed, the container has no route to the internet. The isolation is enforced at the nftables level by netavark, which the container cannot modify even with `CAP_NET_ADMIN`.

---

## Approach 2: `--network none` + Explicit Connect

### How It Works

Start the container with `--network none` (only loopback interface), then use `podman network connect` to attach it to a specific network.

### Verified Behavior

- `--network none` gives the container only a loopback interface (127.0.0.1).
- **`podman network connect` DOES NOT work on `--network none` containers.** Podman returns: `"" is not supported: invalid network mode`.
- This is a hard limitation. Once a container is started with `--network none`, it cannot be connected to any network.

### Commands

```bash
# This does NOT work:
podman run -d --name test --network none alpine sleep 30
podman network connect alcove-internal test
# Error: "" is not supported: invalid network mode
```

### Rootless Compatibility

Works but useless due to the connect limitation.

### Recommendation

**Not viable.** Cannot add network connectivity after starting with `--network none`.

---

## Approach 3: Podman Pods (Shared Network Namespace)

### How It Works

`podman pod create` creates a pod where all containers share a single network namespace (like Kubernetes pods). Containers within the pod communicate over localhost.

### Verified Behavior

- Containers in a pod share the same network namespace, so they can communicate via `127.0.0.1`.
- The network configuration is set at the **pod level**, not per container.
- If the pod is on an internal network, Gate also loses internet access.
- If the pod is on both internal and external networks, Skiff gains internet access.
- **There is no way to give different containers in the same pod different network access.**

### Commands

```bash
# Pod on internal-only: Gate can't reach internet
podman pod create --name task-pod --network alcove-internal
podman run -d --pod task-pod --name gate alpine sleep 30
podman run -d --pod task-pod --name skiff alpine sleep 30
# Both containers: no internet access

# Pod on both networks: Skiff CAN reach internet (defeats the purpose)
podman pod create --name task-pod --network alcove-internal,alcove-external
# Both containers: full internet access
```

### Rootless Compatibility

Fully works with rootless podman.

### Limitations

- Cannot differentiate network access between containers in the same pod.
- Either both have internet access or neither does.
- This is fundamentally incompatible with the requirement that Gate has internet access but Skiff does not.

### Recommendation

**Not viable for isolation.** The shared network namespace means you cannot restrict one container without restricting the other. This matches Kubernetes behavior -- in k8s, NetworkPolicy is applied per-pod, and the sidecar pattern works because the pod itself has network access while NetworkPolicy restricts which pods can communicate. The Alcove requirement is different: we need per-container isolation within what would be a single pod.

The current Alcove design (separate containers on separate networks, communicating by container name) is the correct approach for podman.

---

## Approach 4: Pasta (passt) Network Mode

### How It Works

Pasta is the modern replacement for slirp4netns in rootless podman. It maps the host's network namespace directly into the container, giving it the same IP, routes, and interfaces as the host.

### Verified Behavior

- Container gets the host's exact routing table (`default via 192.168.6.1`).
- Container has full internet access.
- Pasta has options for port forwarding (`--tcp-ports`, `--udp-ports`) and gateway mapping (`--no-map-gw`) but **no egress filtering**.
- `--no-map-gw` prevents the host gateway from being mapped but does not block outbound traffic.

### Commands

```bash
# Pasta gives full network access:
podman run --rm --network pasta alpine ping -c 1 8.8.8.8
# Works

# No way to restrict egress:
podman run --rm --network 'pasta:--no-map-gw' alpine ping -c 1 8.8.8.8
# Still works
```

### Rootless Compatibility

Fully works (it is the default rootless network mode for single containers in newer podman).

### Recommendation

**Not viable for isolation.** Pasta provides no egress filtering. It is designed to give containers seamless host network access, which is the opposite of what we need.

---

## Approach 5: nftables/iptables Inside the Container

### How It Works

Add firewall rules inside the container's network namespace to block outbound traffic.

### Verified Behavior

- Rootless containers do not have `CAP_NET_ADMIN` by default, so they cannot modify nftables/iptables rules.
- Even if `CAP_NET_ADMIN` is granted, the rules would be inside the container's namespace and the container process could remove them (defeating the purpose).
- The container would need to be restricted from modifying its own firewall rules, which is a chicken-and-egg problem.

### Rootless Compatibility

Partially works (requires `--cap-add NET_ADMIN`) but self-defeating.

### Recommendation

**Not viable.** The container could remove its own firewall rules. This is a trust boundary violation -- the thing being isolated should not control its own isolation.

---

## Approach 6: nftables in the Rootless Network Namespace (Outside Containers)

### How It Works

Apply nftables rules in the rootless user's network namespace (where netavark manages the bridge interfaces) to restrict forwarding. This is outside the container.

### Verified Behavior

- `nft list ruleset` from the host user session returns `Operation not permitted` -- the rootless user cannot see or modify the root nftables rules.
- Netavark manages its own nftables rules in the rootless network namespace. These rules are what enforce the `--internal` flag.
- There is no supported way to add custom nftables rules to the rootless network namespace manually.
- Netavark's `--internal` flag is the intended mechanism for this.

### Rootless Compatibility

Not available. Rootless users cannot manage nftables in the root namespace, and there is no API to inject rules into netavark's managed namespace.

### Recommendation

**Not viable directly**, but this is exactly what `--internal` does under the hood via netavark. Use `--internal` (Approach 1) instead.

---

## Approach 7: DNS Restriction

### How It Works

Prevent DNS resolution for Skiff containers to limit their ability to discover external hosts.

### Options Tested

1. **`--internal` network**: aardvark-dns automatically restricts resolution to container names only. External domains return `NXDOMAIN`. This is automatic with Approach 1.

2. **`--dns` flag**: Override DNS servers per container:
   ```bash
   podman run --dns 127.0.0.1 --network alcove-internal alpine nslookup google.com
   # Fails: no DNS server listening on 127.0.0.1
   ```

3. **`--network create --disable-dns`**: Creates a network without aardvark-dns. Containers have no DNS at all (must use IPs).

### Recommendation

**Not needed as a separate measure.** The `--internal` flag (Approach 1) already restricts DNS to container names only. If additional hardening is desired, `--disable-dns` can be used, but then Skiff must reference Gate by IP address rather than container name.

---

## Approach 8: Multiple Networks with Selective Attachment

### How It Works

Create two networks:
- **Internal network** (`--internal`): no internet access
- **External network**: normal bridge with internet

Attach Gate to both, Skiff to internal only. Infrastructure services (NATS, PostgreSQL) are also on the internal network.

### This is Approach 1

This is the same as Approach 1. It is the recommended approach and is already implemented in the Alcove codebase.

---

## Summary and Recommendation

| Approach | Rootless | Blocks Egress | Per-Container | Bypass-Resistant | Viable |
|----------|----------|---------------|---------------|------------------|--------|
| 1. `--internal` bridge | Yes | Yes | Yes | Yes | **Yes** |
| 2. `--network none` + connect | Yes | N/A | N/A | N/A | No |
| 3. Podman pods | Yes | No* | No | N/A | No |
| 4. Pasta | Yes | No | N/A | N/A | No |
| 5. nftables inside container | Partial | Yes | Yes | No | No |
| 6. nftables outside container | No | Yes | Yes | Yes | No |
| 7. DNS restriction | Yes | Partial | Yes | Yes | Supplement |
| 8. Multi-network (= #1) | Yes | Yes | Yes | Yes | **Yes** |

*Pods block egress only if all containers lose access, which defeats the purpose.

### Recommended Architecture

```
                        alcove-external (bridge, internet access)
                        ┌─────────────────────────────────────────────┐
                        │                                             │
                   ┌────┴────┐                                        │
                   │  Gate   │ (on both networks)                     │
                   └────┬────┘                                        │
                        │                                             │
alcove-internal (bridge, --internal, no internet)                     │
┌───────────────────────┼─────────────────────────────────────────────┘
│                       │
│  ┌─────────┐    ┌─────┴────┐    ┌──────────┐    ┌──────────┐
│  │  Skiff  │    │   Gate   │    │   Hail   │    │  Ledger  │
│  │(internal│────│(internal │    │ (NATS)   │    │(Postgres)│
│  │  only)  │    │  + ext)  │    │          │    │          │
│  └─────────┘    └──────────┘    └──────────┘    └──────────┘
│
│  Skiff can reach: Gate, Hail, Ledger (by container name)
│  Skiff CANNOT reach: internet, host, other networks
│
└─────────────────────────────────────────────────────────────────────
```

### Implementation Status

The dual-network pattern is already implemented in `/home/bmbouter/devel/alcove/internal/runtime/podman.go`:

- `EnsureNetworks()` creates `alcove-internal` (with `--internal`) and `alcove-external`
- `RunTask()` starts Gate on `internalNet + "," + externalNet` and Skiff on `internalNet` only
- `HTTP_PROXY`/`HTTPS_PROXY` are set on Skiff to point to Gate by container name

### Remaining Work

1. ~~**Connect NATS and PostgreSQL to the internal network**~~: Done. The `dev-up` make target starts `alcove-hail` and `alcove-ledger` on `alcove-internal`.

2. **Drop unnecessary capabilities on Skiff**: While `CAP_NET_ADMIN` is not granted by default and even with it the isolation holds, explicitly dropping it with `--cap-drop NET_ADMIN` or `--cap-drop ALL` adds defense-in-depth.

3. **Consider `--dns` override for Skiff**: Optionally set `--dns` to the Gate container's IP so Gate can control all DNS resolution, providing another layer of enforcement.

4. **Read-only filesystem for Skiff**: Consider `--read-only` with targeted tmpfs mounts to prevent the agent from modifying network configuration files.
