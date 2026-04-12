#!/usr/bin/env bash
set -euo pipefail

# Deploy to a single node via SSH.
# Usage: ./deploy.sh <host> <ssh-key-path> [image-tag]
#
# Provider-agnostic — just needs SSH access to the target.

HOST="$1"
SSH_KEY="$2"
TAG="${3:-latest}"

echo "Deploying tag=$TAG to $HOST..."

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

ssh "${SSH_OPTS[@]}" deploy@"$HOST" << 'REMOTE'
  cd /opt/143
  docker compose -f docker-compose.prod.yml pull
  docker compose -f docker-compose.prod.yml up -d --remove-orphans

  # Wait for Postgres and API to be healthy before running migrations
  echo "Waiting for API health check..."
  for i in $(seq 1 30); do
    if curl -sf http://localhost:8080/healthz > /dev/null 2>&1; then
      echo "Health check passed."
      break
    fi
    if [ "$i" -eq 30 ]; then
      echo "ERROR: Health check timed out after 60s"
      exit 1
    fi
    sleep 2
  done

  docker compose -f docker-compose.prod.yml exec -T api /bin/migrate up
  echo "Deploy complete."
REMOTE
