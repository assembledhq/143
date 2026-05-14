#!/usr/bin/env bash
set -euo pipefail

# Idempotently enroll a host into the production tailnet.
# Required:
#   TS_AUTH_KEY  - Tailscale auth key, preferably tagged and pre-approved.
#
# Optional:
#   TS_HOSTNAME  - stable node name shown in the Tailscale admin console.
#   TS_TAG       - tag advertised by this node, e.g. tag:prod-worker.

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

tailscale up "${args[@]}"
tailscale ip -4
