#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes listed in a hosts file.
# Usage: ./deploy-fleet.sh [image-tag]
#
# Reads node IPs from /opt/143/fleet-hosts.txt (one IP per line).
# Provider-agnostic — just needs SSH access.

TAG="${1:-latest}"
HOSTS_FILE="${FLEET_HOSTS:-/opt/143/fleet-hosts.txt}"
SERVER_IMAGE="ghcr.io/assembledhq/143-server:$TAG"
SANDBOX_IMAGE="ghcr.io/assembledhq/143-sandbox:$TAG"

echo "Deploying $TAG to fleet..."

while IFS= read -r IP; do
  [[ -z "$IP" || "$IP" == \#* ]] && continue
  echo "--- Deploying to $IP ---"

  ssh -o StrictHostKeyChecking=accept-new deploy@"$IP" << REMOTE
    docker pull $SERVER_IMAGE
    docker pull $SANDBOX_IMAGE
    cd /opt/143
    docker compose -f docker-compose.*.yml up -d --remove-orphans

    # Wait for health check (skip if this is a worker-only node with no API)
    if docker compose -f docker-compose.*.yml ps --format json | grep -q '"api"'; then
      for i in \$(seq 1 30); do
        if curl -sf http://localhost:8080/healthz > /dev/null 2>&1; then
          echo "Health check passed."
          break
        fi
        if [ "\$i" -eq 30 ]; then
          echo "WARNING: Health check timed out after 60s"
        fi
        sleep 2
      done
    else
      echo "No API service on this node, skipping health check."
    fi
REMOTE

  echo "$IP deployed."
done < "$HOSTS_FILE"

echo "Fleet deployment complete."
