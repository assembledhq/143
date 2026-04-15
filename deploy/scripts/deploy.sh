#!/usr/bin/env bash
set -euo pipefail

# Deploy to a node via SSH.
# Usage: ./deploy.sh <role> <host> <ssh-key-path> [image-tag]
#
# Roles: app, worker, db, logging
# Provider-agnostic — just needs SSH access to the target.

ROLE="$1"
HOST="$2"
SSH_KEY="$3"
TAG="${4:-latest}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

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
  logging)
    COMPOSE_FILE="docker-compose.logging.yml"
    HEALTH_SERVICE="grafana"
    ;;
  *)      echo "Unknown role: $ROLE (expected: app, worker, db, logging)"; exit 1 ;;
esac

echo "Deploying role=$ROLE tag=$TAG to $HOST..."

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

# --- Refresh secrets from .env.production.enc ---
if [ -z "${SOPS_AGE_KEY:-}" ]; then
  AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
  if [ -f "$AGE_KEY_FILE" ]; then
    SOPS_AGE_KEY=$(grep "^AGE-SECRET-KEY-" "$AGE_KEY_FILE" | head -1)
    export SOPS_AGE_KEY
  else
    echo "WARNING: No SOPS_AGE_KEY set and no keyfile at $AGE_KEY_FILE — skipping secret refresh"
  fi
fi

ENC_FILE="$PROJECT_DIR/.env.production.enc"
if [ -n "${SOPS_AGE_KEY:-}" ] && [ -f "$ENC_FILE" ]; then
  echo "Refreshing secrets from .env.production.enc..."
  DECRYPTED=$(SOPS_AGE_KEY="$SOPS_AGE_KEY" sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE")

  while IFS= read -r line; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    if [ -z "${!key+x}" ]; then
      export "$key=$value"
    fi
  done <<< "$DECRYPTED"

  if [ "$ROLE" = "logging" ]; then
    : "${GRAFANA_ADMIN_PASSWORD:?GRAFANA_ADMIN_PASSWORD is required for logging role (set it or add to .env.production.enc)}"
    : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for logging role (set it or add to .env.production.enc)}"
    printf 'GRAFANA_ADMIN_PASSWORD=%s\nVICTORIALOGS_HOST=%s\n' "$GRAFANA_ADMIN_PASSWORD" "$VICTORIALOGS_HOST" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
  elif [ "$ROLE" = "db" ]; then
    : "${DB_PASSWORD:?DB_PASSWORD is required for db role (set it or add to .env.production.enc)}"
    printf 'DB_PASSWORD=%s\n' "$DB_PASSWORD" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
  elif [ "$ROLE" = "worker" ]; then
    : "${DB_PASSWORD:?DB_PASSWORD is required for worker role (set it or add to .env.production.enc)}"
    : "${DB_HOST:?DB_HOST is required for worker role (set it or add to .env.production.enc)}"
    : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for worker role (set it or add to .env.production.enc)}"
    printf 'DB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\n' "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
  else
    # Both app and worker nodes need SOPS_AGE_KEY + the encrypted secrets file
    # so the entrypoint can decrypt GitHub App creds, API keys, etc. at boot.
    : "${DB_PASSWORD:?DB_PASSWORD is required for app role (set it or add to .env.production.enc)}"
    : "${DB_HOST:?DB_HOST is required for app role (set it or add to .env.production.enc)}"
    : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for app role (set it or add to .env.production.enc)}"
    printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\n' "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
    scp "${SCP_OPTS[@]}" "$ENC_FILE" deploy@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" "chmod 600 /opt/143/.env.production.enc"
  fi
  echo "Secrets refreshed."
else
  echo "Skipping secret refresh (no SOPS key or .env.production.enc not found)."
fi

# Sync compose file so the remote always runs the latest version
scp "${SCP_OPTS[@]}" "$PROJECT_DIR/$COMPOSE_FILE" deploy@"$HOST":/opt/143/
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ]; then
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.vector.yml" deploy@"$HOST":/opt/143/
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/vector.yaml" deploy@"$HOST":/opt/143/deploy/
fi

ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "COMPOSE_FILE=$COMPOSE_FILE" "HEALTH_SERVICE=$HEALTH_SERVICE" "ROLE=$ROLE" "IMAGE_TAG=$TAG" \
  bash << 'REMOTE'
  cd /opt/143

  dump_diagnostics() {
    echo "--- Last 50 lines of $HEALTH_SERVICE logs ---"
    docker compose -f "$COMPOSE_FILE" logs --tail=50 "$HEALTH_SERVICE" 2>&1 || true
    echo "--- Docker health check log ---"
    docker inspect --format '{{range .State.Health.Log}}--- {{.Start}} ---
{{.Output}}
{{end}}' "$CONTAINER_ID" 2>&1 || true
  }

  docker compose -f "$COMPOSE_FILE" pull

  # Run migrations BEFORE restarting the app so the DB schema is ready when
  # the new code starts serving traffic. Uses `docker compose run` on the new
  # image (already pulled) to execute the migration binary without replacing
  # the running container. This prevents 500s from code referencing columns
  # that the old schema doesn't have yet.
  if [ "$ROLE" = "app" ]; then
    echo "Running database migrations..."
    docker compose -f "$COMPOSE_FILE" run --rm -T --no-deps api /bin/migrate up
  fi

  docker compose -f "$COMPOSE_FILE" up -d --remove-orphans

  echo "Waiting for $HEALTH_SERVICE health check..."
  for i in $(seq 1 60); do
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
      dump_diagnostics
      exit 1
    fi

    if [ "$i" -eq 60 ]; then
      echo "ERROR: Health check timed out after 120s for $HEALTH_SERVICE (last status: $HEALTH_STATUS)"
      dump_diagnostics
      exit 1
    fi
    sleep 2
  done

  echo "Deploy complete ($ROLE)."
REMOTE
