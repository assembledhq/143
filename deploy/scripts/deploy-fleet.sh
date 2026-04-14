#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes in the fleet.
# Usage: ./deploy-fleet.sh <ssh-key-path> [image-tag]
#
# Fleet hosts are read from (in priority order):
#   1. deploy/fleet-hosts.txt          — plain text, one "role IP" per line
#   2. FLEET_HOSTS env var             — comma-separated "role:IP" pairs
#   3. .env.production.enc (FLEET_HOSTS) — encrypted, decrypted via SOPS
#
# FLEET_HOSTS format:  app:10.0.0.2,worker:10.0.0.4,worker:10.0.0.5

SSH_KEY="$1"
TAG="${2:-latest}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
HOSTS_FILE="$PROJECT_DIR/deploy/fleet-hosts.txt"

# If fleet-hosts.txt exists, use it directly.
if [ -f "$HOSTS_FILE" ]; then
  echo "Reading fleet from $HOSTS_FILE..."
  while IFS=' ' read -r ROLE IP; do
    [[ -z "$ROLE" || "$ROLE" == \#* ]] && continue
    echo "--- Deploying $ROLE to $IP ---"
    "$SCRIPT_DIR/deploy.sh" "$ROLE" "$IP" "$SSH_KEY" "$TAG"
    echo "$ROLE@$IP deployed."
  done < "$HOSTS_FILE"
else
  # Fall back to FLEET_HOSTS env var (may already be set, or decrypt from SOPS).
  if [ -z "${FLEET_HOSTS:-}" ]; then
    ENC_FILE="$PROJECT_DIR/.env.production.enc"
    if [ -f "$ENC_FILE" ]; then
      echo "Decrypting fleet hosts from .env.production.enc..."
      FLEET_HOSTS="$(sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE" \
        | grep '^FLEET_HOSTS=' | cut -d= -f2-)"
    fi
  fi

  if [ -z "${FLEET_HOSTS:-}" ]; then
    echo "ERROR: No fleet-hosts.txt found and FLEET_HOSTS is not set."
    echo "Either create deploy/fleet-hosts.txt or add FLEET_HOSTS to .env.production.enc"
    exit 1
  fi

  echo "Reading fleet from FLEET_HOSTS..."
  IFS=',' read -ra ENTRIES <<< "$FLEET_HOSTS"
  for entry in "${ENTRIES[@]}"; do
    ROLE="${entry%%:*}"
    IP="${entry#*:}"
    echo "--- Deploying $ROLE to $IP ---"
    "$SCRIPT_DIR/deploy.sh" "$ROLE" "$IP" "$SSH_KEY" "$TAG"
    echo "$ROLE@$IP deployed."
  done
fi

echo "Fleet deployment complete."
