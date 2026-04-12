#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes in the fleet.
# Usage: ./deploy-fleet.sh <ssh-key-path> [image-tag]
#
# Reads node definitions from fleet-hosts.txt (format: role IP).
# Example fleet-hosts.txt:
#   db    10.0.0.3
#   app   10.0.0.2
#   worker 10.0.0.4

SSH_KEY="$1"
TAG="${2:-latest}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
HOSTS_FILE="${FLEET_HOSTS:-$PROJECT_DIR/deploy/fleet-hosts.txt}"

echo "Deploying tag=$TAG to fleet..."

while IFS=' ' read -r ROLE IP; do
  [[ -z "$ROLE" || "$ROLE" == \#* ]] && continue
  echo "--- Deploying $ROLE to $IP ---"
  "$SCRIPT_DIR/deploy.sh" "$ROLE" "$IP" "$SSH_KEY" "$TAG"
  echo "$ROLE@$IP deployed."
done < "$HOSTS_FILE"

echo "Fleet deployment complete."
