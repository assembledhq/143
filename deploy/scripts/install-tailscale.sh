#!/usr/bin/env bash
set -euo pipefail

# Idempotently enroll a host into the production tailnet.
# Required:
#   TS_AUTH_KEY  - Tailscale auth key, preferably tagged and pre-approved.
#
# Optional:
#   TS_HOSTNAME  - stable node name shown in the Tailscale admin console.
#   TS_TAG       - tag advertised by this node, e.g. tag:prod-worker.
#   TS_ADVERTISE_ROUTES - comma-separated private routes to advertise, e.g. 10.0.0.3/32.
#   TS_ACCEPT_ROUTES - set to "true" on clients that should use advertised routes.

: "${TS_AUTH_KEY:?TS_AUTH_KEY is required to enroll this host in Tailscale}"

TS_HOSTNAME="${TS_HOSTNAME:-$(hostname)}"
TS_TAG="${TS_TAG:-}"

if ! command -v tailscale >/dev/null 2>&1; then
  curl -fsSL https://tailscale.com/install.sh | sh
fi

args=(
  "--auth-key=$TS_AUTH_KEY"
  "--hostname=$TS_HOSTNAME"
  "--accept-dns=false"
)

if [ -n "$TS_TAG" ]; then
  args+=("--advertise-tags=$TS_TAG")
fi

if [ -n "${TS_ADVERTISE_ROUTES:-}" ]; then
  cat >/etc/sysctl.d/99-tailscale-subnet-router.conf <<'EOF'
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
EOF
  sysctl -p /etc/sysctl.d/99-tailscale-subnet-router.conf
  args+=("--advertise-routes=$TS_ADVERTISE_ROUTES")
fi

if [ "${TS_ACCEPT_ROUTES:-false}" = "true" ]; then
  args+=("--accept-routes=true")
fi

tailscale up "${args[@]}"
tailscale ip -4
