#!/usr/bin/env bash
# sandbox-firewall.sh — apply host-level egress rules for the sandbox network.
#
# Blocks sandbox container traffic to cloud metadata (169.254/16) and
# RFC1918 (10/8, 172.16/12, 192.168/16). A prompt-injected agent could
# otherwise pivot to internal infra (DB, monitoring, metadata API) that
# happens to be reachable from the worker host.
#
# Idempotent: drops any prior rules tagged with our comment before re-adding.
# Safe to call on every deploy and on boot.
#
# Requires: docker, iptables, netfilter-persistent (for reboot survival).
#
# Usage: sudo deploy/scripts/sandbox-firewall.sh [network-name]
#        Default network: 143-sandbox.

set -euo pipefail

NETWORK="${1:-143-sandbox}"
COMMENT_TAG="143-sandbox-egress"
BLOCKED_DESTS=(
  "169.254.0.0/16"   # cloud metadata (all major providers)
  "10.0.0.0/8"       # RFC1918
  "172.16.0.0/12"    # RFC1918 (includes Docker default pools)
  "192.168.0.0/16"   # RFC1918
)

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker not installed" >&2
  exit 1
fi

if ! command -v iptables >/dev/null 2>&1; then
  echo "ERROR: iptables not installed" >&2
  exit 1
fi

# Read the actual subnet Docker assigned to the sandbox network. If the
# network doesn't exist yet, bail — the caller (deploy/provision) must
# create it first.
SUBNET=$(docker network inspect "$NETWORK" \
  -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null || true)

if [ -z "$SUBNET" ]; then
  echo "ERROR: docker network '$NETWORK' not found or has no subnet. Create it first." >&2
  exit 1
fi

# DOCKER-USER is appended to FORWARD by Docker on install; create it if
# the host is pre-Docker-17.06 or had it flushed.
iptables -N DOCKER-USER 2>/dev/null || true

# Drop any rules we previously inserted. Detect by comment tag so we can
# change the source subnet or destination list across versions without
# leaking stale rules.
while :; do
  LINE=$(iptables -L DOCKER-USER --line-numbers -n 2>/dev/null \
    | awk -v tag="$COMMENT_TAG" '$0 ~ tag {print $1; exit}')
  [ -z "$LINE" ] && break
  iptables -D DOCKER-USER "$LINE"
done

# Insert fresh rules at the top of DOCKER-USER so Docker's default
# RETURN/ACCEPT at the bottom can't shadow them.
for dest in "${BLOCKED_DESTS[@]}"; do
  iptables -I DOCKER-USER -s "$SUBNET" -d "$dest" \
    -m comment --comment "$COMMENT_TAG" -j DROP
done

# Persist across reboots if iptables-persistent is installed.
if command -v netfilter-persistent >/dev/null 2>&1; then
  netfilter-persistent save >/dev/null
fi

echo "Applied sandbox egress block ($NETWORK, $SUBNET):"
iptables -L DOCKER-USER -n --line-numbers | grep "$COMMENT_TAG" || true
