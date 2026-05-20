#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes in the fleet.
# Usage: ./deploy-fleet.sh <ssh-key-path> [image-tag]
#
# Routine fleet deploys intentionally default to app+worker only. Deploying
# db/redis/logging recreates stateful or operator-facing services and should be
# an explicit maintenance action:
#   DEPLOY_FLEET_ROLES=all ./deploy/scripts/deploy-fleet.sh <ssh-key> [tag]
#   DEPLOY_FLEET_ROLES=app,worker,redis ./deploy/scripts/deploy-fleet.sh <ssh-key> [tag]
#
# Fleet hosts are read from (in priority order):
#   1. FLEET_HOSTS env var             — comma-separated "role:IP" pairs
#   2. .env.production.enc (FLEET_HOSTS) — encrypted, decrypted via SOPS
#
# FLEET_HOSTS format:  app:10.0.0.2,worker:10.0.0.4,db:10.0.0.3,logging:10.0.0.6,redis:10.0.0.5

SSH_KEY="$1"
TAG="${2:-latest}"
DEPLOY_FLEET_ROLES="${DEPLOY_FLEET_ROLES:-app,worker}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

should_deploy_role() {
  local role="$1"
  if [ "$DEPLOY_FLEET_ROLES" = "all" ]; then
    return 0
  fi
  local selected
  IFS=',' read -ra selected <<< "$DEPLOY_FLEET_ROLES"
  for r in "${selected[@]}"; do
    if [ "$r" = "$role" ]; then
      return 0
    fi
  done
  return 1
}

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
echo "Deploying roles: $DEPLOY_FLEET_ROLES"
IFS=',' read -ra ENTRIES <<< "$FLEET_HOSTS"
for entry in "${ENTRIES[@]}"; do
  ROLE="${entry%%:*}"
  IP="${entry#*:}"
  if ! should_deploy_role "$ROLE"; then
    echo "Skipping $ROLE@$IP (not in DEPLOY_FLEET_ROLES=$DEPLOY_FLEET_ROLES)."
    continue
  fi
  echo "--- Deploying $ROLE to $IP ---"
  "$SCRIPT_DIR/deploy.sh" "$ROLE" "$IP" "$SSH_KEY" "$TAG"
  echo "$ROLE@$IP deployed."
done

echo "Fleet deployment complete."
