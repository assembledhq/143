# Static Egress Gateway

> Status: Implemented baseline | Last reviewed: 2026-05-19

143 keeps Tailscale as the private control-plane overlay for app, worker, db, and Redis routing. Static customer allowlisting uses a separate opt-in data plane so worker host traffic and ordinary sandbox traffic do not inherit a global exit node.

## Runtime Contract

- Organizations opt in with `settings.sandbox_network.static_egress_enabled`.
- When disabled, new and hydrated sandboxes use the default `143-sandbox` bridge.
- When enabled, new and hydrated sandboxes use `143-sandbox-static-egress` plus the static-egress resolver file.
- Workers advertise `static_egress_capable=true` only when platform config enables static egress, the customer-facing public IP is configured, and host reconciliation has written `/etc/143/static-egress-capable` after installing and probing the WireGuard policy route.
- Preview worker selection requires `static_egress_capable=true` for opted-in organizations.
- Preview infrastructure containers join the sandbox container's selected bridge, so static-egress previews keep same-network access to managed preview dependencies.
- Live preview reuse checks the existing container network. If the current container does not match the requested egress mode, preview startup fails closed with a restart-required message instead of silently using the wrong route.

## Deploy Contract

- Worker reconciliation creates both bridges with pinned, non-overlapping subnets:
  - `143-sandbox` -> `172.30.0.0/24`
  - `143-sandbox-static-egress` -> `172.31.0.0/24`
- Firewall rule comments are network-specific, so reconciling one bridge cannot delete the other bridge's metadata/RFC1918 guardrails.
- A single sandbox dnsmasq sidecar attaches to both sandbox bridges. Each bridge has its own fixed resolver IP and resolver file.
- Worker-side static egress uses raw WireGuard plus policy routing for traffic sourced from the static egress bridge subnet.
- `STATIC_EGRESS_ENABLED=true` fails closed during worker reconciliation if the WireGuard helper or required gateway/worker tunnel settings are missing.
- The egress gateway SNATs accepted WireGuard traffic to its stable public IPv4 and independently blocks metadata and RFC1918 destinations.

## Non-goals

- Static egress v1 is for public endpoints protected by source-IP allowlists.
- Customer-private VPC connectivity remains a separate future connector design.
- The customer-facing allowlist address is always the gateway public IPv4, never a Tailscale `100.64.0.0/10` address.
