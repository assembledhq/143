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
    printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\n' "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
    scp "${SCP_OPTS[@]}" "$ENC_FILE" deploy@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" "chmod 644 /opt/143/.env.production.enc"
  else
    # Both app and worker nodes need SOPS_AGE_KEY + the encrypted secrets file
    # so the entrypoint can decrypt GitHub App creds, API keys, etc. at boot.
    : "${DB_PASSWORD:?DB_PASSWORD is required for app role (set it or add to .env.production.enc)}"
    : "${DB_HOST:?DB_HOST is required for app role (set it or add to .env.production.enc)}"
    : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for app role (set it or add to .env.production.enc)}"
    printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\n' "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
    scp "${SCP_OPTS[@]}" "$ENC_FILE" deploy@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" "chmod 644 /opt/143/.env.production.enc"
  fi
  echo "Secrets refreshed."
else
  echo "Skipping secret refresh (no SOPS key or .env.production.enc not found)."
fi

# Sync compose file so the remote always runs the latest version
scp "${SCP_OPTS[@]}" "$PROJECT_DIR/$COMPOSE_FILE" deploy@"$HOST":/opt/143/
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ]; then
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.vector.yml" deploy@"$HOST":/opt/143/
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" "mkdir -p /opt/143/deploy"
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/vector.yaml" deploy@"$HOST":/opt/143/deploy/
fi

ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "COMPOSE_FILE=$COMPOSE_FILE" "HEALTH_SERVICE=$HEALTH_SERVICE" "ROLE=$ROLE" "IMAGE_TAG=$TAG" \
  bash << 'REMOTE'
  cd /opt/143

  recreate_other_services() {
    local skip="$1"
    local others
    others="$(docker compose -f "$COMPOSE_FILE" config --services | grep -v "^${skip}$" || true)"
    if [ -n "$others" ]; then
      echo "$others" | xargs docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps --remove-orphans
    fi
  }

  dump_diagnostics() {
    local cid="${1:-}"
    echo "--- Last 50 lines of $HEALTH_SERVICE logs ---"
    docker compose -f "$COMPOSE_FILE" logs --tail=50 "$HEALTH_SERVICE" 2>&1 || true
    if [ -n "$cid" ]; then
      echo "--- Docker health check log ---"
      docker inspect --format '{{if .State.Health}}{{range .State.Health.Log}}--- {{.Start}} ---
{{.Output}}
{{end}}{{else}}(no health check configured){{end}}' "$cid" 2>&1 || true
    fi
  }

  # wait_container_healthy CONTAINER_ID TIMEOUT — poll until a specific container
  # passes its health check, or fail after TIMEOUT seconds.
  wait_container_healthy() {
    local cid="$1" timeout="${2:-120}"
    echo "Waiting for container $cid health check (timeout ${timeout}s)..."

    # If the container has no HEALTHCHECK, treat "running" as healthy.
    local has_healthcheck
    has_healthcheck="$(docker inspect --format '{{if .State.Health}}yes{{else}}no{{end}}' "$cid")"
    if [ "$has_healthcheck" = "no" ]; then
      local state
      state="$(docker inspect --format '{{.State.Status}}' "$cid")"
      if [ "$state" = "running" ]; then
        echo "No health check configured; container is running."
        return 0
      else
        echo "ERROR: container is $state (no health check configured)"
        dump_diagnostics "$cid"
        return 1
      fi
    fi

    for i in $(seq 1 $((timeout / 2))); do
      HEALTH_STATUS="$(docker inspect --format '{{.State.Health.Status}}' "$cid")"
      if [ "$HEALTH_STATUS" = "healthy" ]; then
        echo "Health check passed."
        return 0
      fi

      if [ "$HEALTH_STATUS" = "unhealthy" ] || [ "$HEALTH_STATUS" = "exited" ] || [ "$HEALTH_STATUS" = "dead" ]; then
        echo "ERROR: container entered terminal state: $HEALTH_STATUS"
        dump_diagnostics "$cid"
        return 1
      fi

      if [ "$i" -eq $((timeout / 2)) ]; then
        echo "ERROR: Health check timed out after ${timeout}s (last status: $HEALTH_STATUS)"
        dump_diagnostics "$cid"
        return 1
      fi
      sleep 2
    done
  }

  # Ensure gVisor runtime is configured with --ignore-cgroups so Docker
  # handles cgroup management (prevents EOF errors from cgroup conflicts).
  if [ "$ROLE" = "worker" ] && command -v runsc &>/dev/null; then
    DAEMON_JSON="/etc/docker/daemon.json"
    if [ -f "$DAEMON_JSON" ] && ! grep -q "ignore-cgroups" "$DAEMON_JSON"; then
      echo "Patching runsc runtime to use --ignore-cgroups..."
      sudo runsc install -- --ignore-cgroups
      sudo systemctl restart docker
      echo "Docker restarted with updated gVisor config."
    fi
  fi

  docker compose -f "$COMPOSE_FILE" pull

  # The sandbox image is referenced via SANDBOX_IMAGE env var, not as a compose
  # service, so `docker compose pull` doesn't fetch it. Pull it explicitly —
  # ContainerCreate doesn't auto-pull, so the worker would fail on first launch.
  if [ "$ROLE" = "worker" ]; then
    docker pull "ghcr.io/assembledhq/143-sandbox:$IMAGE_TAG"
    # Ensure the shared sandbox egress network exists (idempotent). Older hosts
    # provisioned before this was added won't have it, and session creation
    # will fail until it does.
    docker network inspect 143-sandbox >/dev/null 2>&1 || \
      docker network create --driver bridge --label managed-by=143 143-sandbox
  fi

  # Run migrations BEFORE restarting the app so the DB schema is ready when
  # the new code starts serving traffic. Uses `docker compose run` on the new
  # image (already pulled) to execute the migration binary without replacing
  # the running container. This prevents 500s from code referencing columns
  # that the old schema doesn't have yet.
  if [ "$ROLE" = "app" ]; then
    echo "Running database migrations..."
    docker compose -f "$COMPOSE_FILE" run --rm -T --no-deps api /bin/migrate up < /dev/null
  fi

  # Recreate non-health-service containers (vector, caddy, frontend, etc.)
  # BEFORE the rolling deploy. These services don't depend on the health
  # service version, and updating them first ensures config fixes (e.g.
  # vector.yaml, Caddyfile) are applied even if the health service roll fails.
  if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ]; then
    echo "Updating supporting services..."
    recreate_other_services "$HEALTH_SERVICE"
  fi

  # Rolling deploy for the app service:
  #   1. Scale up a new container alongside the old one (both share the network
  #      so the new container can reach Postgres during startup)
  #   2. Wait for the new container's health check to pass
  #   3. Stop the old container
  # NOTE: --no-recreate keeps the old container as-is. If you change compose
  # config (env vars, ports, etc.) alongside a code deploy, the old container
  # will still run the stale config during the health-check window.
  if [ "$ROLE" = "app" ]; then
    OLD_CONTAINER="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | head -1 || true)"

    echo "Starting new $HEALTH_SERVICE container..."
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --scale "$HEALTH_SERVICE=2" --no-recreate "$HEALTH_SERVICE"

    # Identify the new container
    if [ -n "$OLD_CONTAINER" ]; then
      NEW_CONTAINER="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | grep -v "$OLD_CONTAINER" | head -1)"
    else
      NEW_CONTAINER="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | head -1)"
    fi
    if [ -z "$NEW_CONTAINER" ]; then
      echo "ERROR: could not identify new container"
      exit 1
    fi

    if ! wait_container_healthy "$NEW_CONTAINER" 120; then
      echo "Rolling back — removing failed container..."
      docker stop "$NEW_CONTAINER" >/dev/null 2>&1 || true
      docker rm "$NEW_CONTAINER" >/dev/null 2>&1 || true
      # Ensure the old container is still serving after rollback.
      if [ -n "$OLD_CONTAINER" ]; then
        OLD_STATUS="$(docker inspect --format '{{.State.Status}}' "$OLD_CONTAINER" 2>/dev/null || echo "missing")"
        if [ "$OLD_STATUS" != "running" ]; then
          echo "WARNING: old container is $OLD_STATUS — restarting service..."
          docker compose -f "$COMPOSE_FILE" up -d --no-deps "$HEALTH_SERVICE"
        fi
      else
        docker compose -f "$COMPOSE_FILE" up -d --no-deps "$HEALTH_SERVICE"
      fi
      exit 1
    fi

    # New container is healthy — remove the old one.
    if [ -n "$OLD_CONTAINER" ]; then
      echo "Removing old container..."
      docker stop "$OLD_CONTAINER" >/dev/null 2>&1 || true
      docker rm "$OLD_CONTAINER" >/dev/null 2>&1 || true
    fi
    # Reconcile Compose state back to scale=1 now that only one container remains.
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --scale "$HEALTH_SERVICE=1" "$HEALTH_SERVICE"
    echo "$HEALTH_SERVICE rolled over successfully."

  elif [ "$ROLE" = "worker" ]; then
    # Workers poll for jobs, so running two simultaneously would double the
    # effective concurrency limit. Instead, stop-then-start: brief downtime is
    # acceptable since workers process async jobs (no user-facing HTTP traffic).
    echo "Stopping old $HEALTH_SERVICE container..."
    docker compose -f "$COMPOSE_FILE" stop "$HEALTH_SERVICE"
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$HEALTH_SERVICE"

    CONTAINER_ID="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | head -1)"
    if [ -n "$CONTAINER_ID" ]; then
      if ! wait_container_healthy "$CONTAINER_ID" 120; then
        echo "ERROR: new worker failed health check"
        exit 1
      fi
    fi
    echo "$HEALTH_SERVICE restarted successfully."

  else
    # Non-rolling roles (db, logging) — just recreate everything.
    docker compose -f "$COMPOSE_FILE" up -d --force-recreate --remove-orphans

    CONTAINER_ID="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | head -1)"
    if [ -n "$CONTAINER_ID" ]; then
      wait_container_healthy "$CONTAINER_ID" 120
    fi
  fi

  # Verify Vector is running on app/worker nodes
  if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ]; then
    echo "Checking Vector log collector..."
    VECTOR_ID="$(docker compose -f "$COMPOSE_FILE" ps -q vector)"
    if [ -z "$VECTOR_ID" ]; then
      echo "ERROR: Vector container not found — logs will not be collected"
      exit 1
    fi
    VECTOR_STATUS="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$VECTOR_ID")"
    if [ "$VECTOR_STATUS" = "exited" ] || [ "$VECTOR_STATUS" = "dead" ]; then
      echo "ERROR: Vector is not running (status: $VECTOR_STATUS)"
      docker compose -f "$COMPOSE_FILE" logs --tail=20 vector 2>&1 || true
      exit 1
    fi
    echo "Vector is running (status: $VECTOR_STATUS)."
  fi

  echo "Deploy complete ($ROLE)."
REMOTE
