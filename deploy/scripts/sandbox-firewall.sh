#!/usr/bin/env bash
# sandbox-firewall.sh — apply host-level egress rules for the sandbox network.
#
# Blocks sandbox container traffic to cloud metadata (169.254/16) and
# RFC1918 (10/8, 172.16/12, 192.168/16). A prompt-injected agent could
# otherwise pivot to internal infra (DB, monitoring, metadata API) that
# happens to be reachable from the worker host.
#
# Carves out intra-bridge traffic via a RETURN rule so sandboxes can still
# reach preview-infrastructure containers (postgres, etc.) and the
# sandbox-dns resolver, all of which sit on the same 143-sandbox bridge.
#
# Verification after first deploy on a new worker (the RETURN rule only
# helps if intra-bridge traffic actually traverses DOCKER-USER — some
# Docker versions enforce enable_icc=false in an earlier chain):
#
#   docker exec -it <sandbox-container> sh -c 'nc -zv 172.30.0.2 53 && \
#       nc -zv preview-db-<handle> 5432'
#
# If either connection refuses, the fix is to remove enable_icc=false from
# the network and rely on these iptables rules alone for sandbox isolation.
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

# Allow intra-bridge traffic so sandbox containers can reach the preview-
# infrastructure containers (postgres, etc.) and the sandbox-dns resolver,
# all of which sit on the same 143-sandbox bridge. The DROP rules above
# block 172.16/12, which the bridge subnet itself is part of, so without
# this carve-out every intra-bridge request gets dropped.
#
# RETURN — not ACCEPT — exits DOCKER-USER and returns to the FORWARD
# chain so the rest of FORWARD still applies. The bridge is created with
# enable_icc=false to isolate sandbox-from-sandbox; this rule operates
# below that layer and does not undo it.
#
# Inserted last so iptables -I lands it at position 1, ahead of the DROPs.
iptables -I DOCKER-USER -s "$SUBNET" -d "$SUBNET" \
  -m comment --comment "$COMMENT_TAG" -j RETURN

# Persist across reboots if iptables-persistent is installed.
if command -v netfilter-persistent >/dev/null 2>&1; then
  netfilter-persistent save >/dev/null
fi

echo "Applied sandbox egress block ($NETWORK, $SUBNET):"
iptables -L DOCKER-USER -n --line-numbers | grep "$COMMENT_TAG" || true
