#!/usr/bin/env bash
set -euo pipefail

# Deploy to a node via SSH.
# Usage: ./deploy.sh <role> <host> <ssh-key-path> [image-tag]
#
# Roles: app, worker, db
# Provider-agnostic — just needs SSH access to the target.

ROLE="$1"
HOST="$2"
SSH_KEY="$3"
TAG="${4:-latest}"

case "$ROLE" in
  app)
    COMPOSE_FILE="docker-compose.app.yml"
    HEALTH_SERVICE="api"
    ;;
  worker)
    COMPOSE_FILE="docker-compose.worker.yml"
    HEALTH_SERVICE="worker"
    ;;
  db)
    COMPOSE_FILE="docker-compose.db.yml"
    HEALTH_SERVICE="postgres"
    ;;
  *)      echo "Unknown role: $ROLE (expected: app, worker, db)"; exit 1 ;;
esac

echo "Deploying role=$ROLE tag=$TAG to $HOST..."

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "COMPOSE_FILE=$COMPOSE_FILE" "HEALTH_SERVICE=$HEALTH_SERVICE" "ROLE=$ROLE" "IMAGE_TAG=$TAG" \
  bash << 'REMOTE'
  cd /opt/143
  docker compose -f "$COMPOSE_FILE" pull
  docker compose -f "$COMPOSE_FILE" up -d --remove-orphans

  echo "Waiting for $HEALTH_SERVICE health check..."
  for i in $(seq 1 30); do
    CONTAINER_ID="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE")"
    if [ -z "$CONTAINER_ID" ]; then
      echo "ERROR: could not find container for service $HEALTH_SERVICE"
      exit 1
    fi

    HEALTH_STATUS="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$CONTAINER_ID")"
    if [ "$HEALTH_STATUS" = "healthy" ] || [ "$HEALTH_STATUS" = "running" ]; then
      echo "Health check passed for $HEALTH_SERVICE."
      break
    fi

    if [ "$HEALTH_STATUS" = "unhealthy" ] || [ "$HEALTH_STATUS" = "exited" ] || [ "$HEALTH_STATUS" = "dead" ]; then
      echo "ERROR: $HEALTH_SERVICE entered terminal state: $HEALTH_STATUS"
      exit 1
    fi

    if [ "$i" -eq 30 ]; then
      echo "ERROR: Health check timed out after 60s for $HEALTH_SERVICE (last status: $HEALTH_STATUS)"
      exit 1
    fi
    sleep 2
  done

  # Run migrations after the app node itself is healthy.
  if [ "$ROLE" = "app" ]; then
    docker compose -f "$COMPOSE_FILE" exec -T api /bin/migrate up
  fi

  echo "Deploy complete ($ROLE)."
REMOTE
