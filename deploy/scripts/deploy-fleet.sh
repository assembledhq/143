#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes in the fleet.
# Usage: ./deploy-fleet.sh <ssh-key-path> [image-tag]
#
# Fleet hosts are read from (in priority order):
#   1. FLEET_HOSTS env var             — comma-separated "role:IP" pairs
#   2. .env.production.enc (FLEET_HOSTS) — encrypted, decrypted via SOPS
#
# FLEET_HOSTS format:  app:10.0.0.2,worker:10.0.0.4,worker:10.0.0.5

SSH_KEY="$1"
TAG="${2:-latest}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Read FLEET_HOSTS from env var, or decrypt from SOPS.
if [ -z "${FLEET_HOSTS:-}" ]; then
  ENC_FILE="$PROJECT_DIR/.env.production.enc"
  if [ -f "$ENC_FILE" ]; then
    echo "Decrypting fleet hosts from .env.production.enc..."
    FLEET_HOSTS="$(sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE" \
      | grep '^FLEET_HOSTS=' | cut -d= -f2- || true)"
  fi
fi

if [ -z "${FLEET_HOSTS:-}" ]; then
  echo "ERROR: FLEET_HOSTS is not set."
  echo "Set FLEET_HOSTS env var or add it to .env.production.enc"
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

echo "Fleet deployment complete."
