#!/usr/bin/env bash
set -euo pipefail

# Deploy to a node via SSH.
# Usage: ./deploy.sh <role> <host> <ssh-key-path> [image-tag]
#
# Roles: app, worker, db, logging, redis
# Provider-agnostic — just needs SSH access to the target.

ROLE="$1"
HOST="$2"
SSH_KEY="$3"
TAG="${4:-latest}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DISABLED_WARNING_WEBHOOK_URL="http://localhost:65535/disabled-warning"
DISABLED_CRITICAL_WEBHOOK_URL="http://localhost:65535/disabled-critical"

# Shared worker bucket defaults and mapping logic.
# shellcheck source=deploy/scripts/worker_buckets.sh
source "$SCRIPT_DIR/worker_buckets.sh"

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
  redis)
    COMPOSE_FILE="docker-compose.redis.yml"
    HEALTH_SERVICE="redis"
    ;;
  *)      echo "Unknown role: $ROLE (expected: app, worker, db, logging, redis)"; exit 1 ;;
esac

echo "Deploying role=$ROLE tag=$TAG to $HOST..."

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

repair_deploy_sudoers() {
  bash "$SCRIPT_DIR/repair-deploy-sudoers.sh" "$ROLE" "$HOST" "$SSH_KEY"
}

warn_log_rotation_skipped() {
  echo "WARNING: docker log rotation was not updated on this deploy; continuing."
  echo "  The service deploy will continue, but local Docker json-file logs may remain unbounded."
  echo "  To repair the host when root SSH is available, run:"
  echo "    make repair-deploy-sudoers ROLE=$ROLE HOST=$HOST SSH_KEY=$SSH_KEY"
}

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

  apply_worker_bucket_overrides "$ROLE" "$HOST"

  if [ "$ROLE" = "logging" ]; then
    : "${GRAFANA_ADMIN_PASSWORD:?GRAFANA_ADMIN_PASSWORD is required for logging role (set it or add to .env.production.enc)}"
    : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for logging role (set it or add to .env.production.enc)}"
    GRAFANA_ALERTS_WARNING_WEBHOOK_URL="${GRAFANA_ALERTS_WARNING_WEBHOOK_URL:-$DISABLED_WARNING_WEBHOOK_URL}"
    GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL="${GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL:-$DISABLED_CRITICAL_WEBHOOK_URL}"
    printf 'GRAFANA_ADMIN_PASSWORD=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nGRAFANA_ALERTS_WARNING_WEBHOOK_URL=%s\nGRAFANA_ALERTS_CRITICAL_WEBHOOK_URL=%s\n' \
      "$GRAFANA_ADMIN_PASSWORD" "$VICTORIALOGS_HOST" "logging" "$GRAFANA_ALERTS_WARNING_WEBHOOK_URL" "$GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
  elif [ "$ROLE" = "db" ]; then
    : "${DB_PASSWORD:?DB_PASSWORD is required for db role (set it or add to .env.production.enc)}"
    printf 'DB_PASSWORD=%s\n' "$DB_PASSWORD" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
  elif [ "$ROLE" = "redis" ]; then
    : "${REDIS_PASSWORD:?REDIS_PASSWORD is required for redis role (set it or add to .env.production.enc)}"
    : "${REDIS_PRIVATE_IP:?REDIS_PRIVATE_IP is required for redis role (set it or add to .env.production.enc)}"
    printf 'REDIS_PASSWORD=%s\nREDIS_PRIVATE_IP=%s\n' "$REDIS_PASSWORD" "$REDIS_PRIVATE_IP" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
  elif [ "$ROLE" = "worker" ]; then
    : "${DB_PASSWORD:?DB_PASSWORD is required for worker role (set it or add to .env.production.enc)}"
    : "${DB_HOST:?DB_HOST is required for worker role (set it or add to .env.production.enc)}"
    : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for worker role (set it or add to .env.production.enc)}"
    # Refresh the shared secrets in /opt/143/.env, then re-append the per-host
    # identity from /opt/143/.env.local (NODE_ID, WORKER_PRIVATE_IP,
    # PREVIEW_INTERNAL_BASE_URL) so docker compose can still interpolate them
    # when it parses the compose file. .env.local is owned by provisioning
    # and we abort if it's missing instead of silently coming up with an
    # empty NODE_ID and WORKER_PRIVATE_IP=0.0.0.0 (would expose worker to
    # the public internet).
    printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nWORKER_PROCESS_COUNT=%s\nSANDBOX_CPU_LIMIT=%s\nSANDBOX_MEMORY_LIMIT_MB=%s\nSANDBOX_DISK_LIMIT_GB=%s\n' \
      "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" \
      "${WORKER_PROCESS_COUNT:-}" "${SANDBOX_CPU_LIMIT:-}" "${SANDBOX_MEMORY_LIMIT_MB:-}" "${SANDBOX_DISK_LIMIT_GB:-}" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" '
          set -euo pipefail
          cat > /opt/143/.env
          chmod 600 /opt/143/.env
          if [ ! -f /opt/143/.env.local ]; then
            echo "ERROR: /opt/143/.env.local is missing on this host." >&2
            echo "       It holds NODE_ID, WORKER_PRIVATE_IP, and PREVIEW_INTERNAL_BASE_URL." >&2
            echo "       Re-run: make provision-worker HOST=<this-host>" >&2
            exit 1
          fi
          cat /opt/143/.env.local >> /opt/143/.env
        '
    scp "${SCP_OPTS[@]}" "$ENC_FILE" deploy@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" "chmod 644 /opt/143/.env.production.enc"
  else
    # App nodes need SOPS_AGE_KEY + the encrypted secrets file so the
    # entrypoint can decrypt GitHub App creds, API keys, etc. at boot.
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
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ] || [ "$ROLE" = "logging" ]; then
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.vector.yml" deploy@"$HOST":/opt/143/
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" "mkdir -p /opt/143/deploy /opt/143/deploy/scripts"
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/vector.yaml" deploy@"$HOST":/opt/143/deploy/
fi
if [ "$ROLE" = "logging" ]; then
  # Older logging hosts may have root-owned vmalert/grafana dirs from a prior
  # provision step; without ownership the deploy user can't unlink the entries
  # in `rm -rf` below. Mirror the worker pattern: try a non-interactive sudo
  # chown (narrowly granted in deploy/scripts/bootstrap.sh), tolerate failure
  # so the rm still runs on hosts where files are already deploy-owned.
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "sudo -n chown -R deploy:deploy /opt/143/deploy/vmalert 2>&1 | sed 's/^/  chown: /' || true; \
     sudo -n chown -R deploy:deploy /opt/143/deploy/grafana 2>&1 | sed 's/^/  chown: /' || true"
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" "rm -rf /opt/143/deploy/grafana/provisioning /opt/143/deploy/vmalert/rules && mkdir -p /opt/143/deploy/grafana /opt/143/deploy/vmalert"
  scp -r "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/grafana/provisioning" deploy@"$HOST":/opt/143/deploy/grafana/
  scp -r "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/vmalert/rules" deploy@"$HOST":/opt/143/deploy/vmalert/
fi
if [ "$ROLE" = "app" ]; then
  # Sync Caddyfile so the remote always has the latest reverse-proxy config.
  # The remote compares the new hash against the currently running copy to
  # decide whether to restart Caddy (see `stage_caddy_config_if_changed` below).
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/Caddyfile" deploy@"$HOST":/opt/143/deploy/Caddyfile.new
fi
if [ "$ROLE" = "worker" ]; then
  # Keep the sandbox firewall script in sync so every deploy can re-apply
  # the egress rules (they read the sandbox network's current subnet).
  # Older workers may have a root-owned copy from cloud-init bootstrap.
  # Try to normalize ownership non-interactively; tolerate failure so the
  # scp below still runs on hosts where files are already deploy-owned.
  # If sudo has no NOPASSWD entry, `sudo -n` exits immediately instead of
  # hanging waiting for a password that CI can't provide.
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "sudo -n chown -R deploy:deploy /opt/143/deploy/scripts 2>&1 | sed 's/^/  chown: /' || true"
  # Stage to a .new path and atomically rename. Writing in place reuses the
  # existing inode, which can yield ETXTBSY ("Text file busy") on the later
  # `sudo sandbox-firewall.sh` exec if anything still holds the old inode
  # open for write (lingering sftp-server FD, ssh ControlMaster, or a
  # concurrent run). rename(2) gives the new contents a fresh inode.
  if ! scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/sandbox-firewall.sh" \
      deploy@"$HOST":/opt/143/deploy/scripts/sandbox-firewall.sh.new; then
    echo "sandbox-firewall.sh sync failed; trying no-teardown deploy sudoers repair..."
    if repair_deploy_sudoers; then
      ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
        "sudo -n chown -R deploy:deploy /opt/143/deploy/scripts 2>&1 | sed 's/^/  chown: /' || true"
      scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/sandbox-firewall.sh" \
        deploy@"$HOST":/opt/143/deploy/scripts/sandbox-firewall.sh.new
    else
      echo "ERROR: scp of sandbox-firewall.sh failed and sudoers repair via root SSH did not complete."
      echo "  Run once from a machine with root SSH access:"
      echo "    make repair-deploy-sudoers ROLE=$ROLE HOST=$HOST SSH_KEY=$SSH_KEY"
      echo "  Then re-run the deploy."
      exit 1
    fi
  fi
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "mv /opt/143/deploy/scripts/sandbox-firewall.sh.new /opt/143/deploy/scripts/sandbox-firewall.sh \
     || { rm -f /opt/143/deploy/scripts/sandbox-firewall.sh.new; exit 1; }"
fi

# --- Docker log rotation (idempotent) ---
# Cap container log file growth so a chatty container can't fill the disk.
# Docker's default json-file driver has no size limit. Logs ship to
# VictoriaLogs (Vector → 30d retention) on app/worker/logging hosts; the
# local file is just a buffer plus a crash-recovery window. db and redis
# hosts have no Vector — the local file is the only copy — so db gets a
# larger cap because postgresql.conf logs every connection, every query
# >500ms, and every lock wait, and the entire trail is local-only.
#
# Sync the helper, then call it via deploy+sudo (matches sandbox-firewall.sh
# pattern). The script is idempotent and only restarts docker when the
# content of /etc/docker/daemon.json actually changes, so steady-state
# deploys cost nothing.
case "$ROLE" in
  db) LOG_MAX_SIZE="500m" ;;  # postgres logs are verbose AND local-only
  *)  LOG_MAX_SIZE="100m" ;;
esac
LOG_MAX_FILE="5"
LOG_ROTATION_READY=1

# Sync install-log-rotation.sh: stage to .new, atomic rename. Same ETXTBSY
# avoidance as sandbox-firewall.sh — a lingering FD on the old inode would
# break the subsequent `sudo install-log-rotation.sh` exec.
ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "sudo -n chown -R deploy:deploy /opt/143/deploy/scripts 2>&1 | sed 's/^/  chown: /' || true"
if ! scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-log-rotation.sh" \
    deploy@"$HOST":/opt/143/deploy/scripts/install-log-rotation.sh.new; then
  echo "install-log-rotation.sh sync failed; trying no-teardown deploy sudoers repair..."
  if repair_deploy_sudoers; then
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
      "sudo -n chown -R deploy:deploy /opt/143/deploy/scripts 2>&1 | sed 's/^/  chown: /' || true"
    scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-log-rotation.sh" \
      deploy@"$HOST":/opt/143/deploy/scripts/install-log-rotation.sh.new
  else
    warn_log_rotation_skipped
    LOG_ROTATION_READY=0
  fi
fi
if [ "$LOG_ROTATION_READY" -eq 1 ]; then
  if ! ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "mv /opt/143/deploy/scripts/install-log-rotation.sh.new /opt/143/deploy/scripts/install-log-rotation.sh \
     && chmod +x /opt/143/deploy/scripts/install-log-rotation.sh \
     || { rm -f /opt/143/deploy/scripts/install-log-rotation.sh.new; exit 1; }"; then
    warn_log_rotation_skipped
    LOG_ROTATION_READY=0
  fi
fi

if [ "$LOG_ROTATION_READY" -eq 1 ]; then
  echo "Ensuring docker log rotation (max-size=$LOG_MAX_SIZE, max-file=$LOG_MAX_FILE)..."
  # `sudo -n` so missing-sudoers fails fast instead of hanging on a password
  # prompt CI can't satisfy. If the repair path also isn't available, keep the
  # deploy moving: log rotation is an operational hardening step, not the app
  # or database rollout itself.
  run_log_rotation() {
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
      "sudo -n /opt/143/deploy/scripts/install-log-rotation.sh $LOG_MAX_SIZE $LOG_MAX_FILE"
  }
  if ! run_log_rotation; then
    echo "install-log-rotation.sh failed under deploy+sudo; trying no-teardown deploy sudoers repair..."
    if repair_deploy_sudoers; then
      echo "Retrying docker log rotation after sudoers repair..."
      if ! run_log_rotation; then
        warn_log_rotation_skipped
      fi
    else
      warn_log_rotation_skipped
    fi
  fi
fi

ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "COMPOSE_FILE=$COMPOSE_FILE" "HEALTH_SERVICE=$HEALTH_SERVICE" "ROLE=$ROLE" "IMAGE_TAG=$TAG" \
  "WORKER_DEPLOY_DETACH=${WORKER_DEPLOY_DETACH:-}" \
  bash << 'REMOTE'
  set -euo pipefail
  cd /opt/143

  # Clean up the staged Caddyfile on any exit path.
  # stage_caddy_config_if_changed normally consumes it (mv or rm), but this
  # guards against a failure between the scp and that call leaving it on disk.
  trap 'rm -f /opt/143/deploy/Caddyfile.new' EXIT

  # recreate_other_services SKIP_SVCS — force-recreate every compose service
  # except the space-separated list in SKIP_SVCS. Used to update out-of-band
  # services (vector, etc.) without touching services that are being rolled.
  recreate_other_services() {
    local skip_list="$1"
    local all filtered=""
    all="$(docker compose -f "$COMPOSE_FILE" config --services)"
    for svc in $all; do
      local match=0
      for skip in $skip_list; do
        if [ "$svc" = "$skip" ]; then match=1; break; fi
      done
      [ "$match" -eq 0 ] && filtered="$filtered $svc"
    done
    # shellcheck disable=SC2086
    if [ -n "$(echo $filtered | tr -d ' ')" ]; then
      echo $filtered | xargs docker compose -f "$COMPOSE_FILE" up -d --force-recreate --no-deps --remove-orphans
    fi
  }

  # stage_caddy_config_if_changed — returns 0 if deploy/Caddyfile.new differs
  # from the currently deployed deploy/Caddyfile, and when it does, promotes
  # the staged file into place (mv). Returns 1 (and removes the staged file)
  # when contents match. Used to avoid restarting Caddy on code-only deploys;
  # Caddy restarts briefly unbind ports 80/443 and surface as 502s through
  # any upstream proxy (Cloudflare, etc.).
  stage_caddy_config_if_changed() {
    local new_file="/opt/143/deploy/Caddyfile.new"
    local cur_file="/opt/143/deploy/Caddyfile"
    [ -f "$new_file" ] || return 1
    if [ ! -f "$cur_file" ]; then
      mv "$new_file" "$cur_file"
      return 0
    fi
    if ! cmp -s "$new_file" "$cur_file"; then
      mv "$new_file" "$cur_file"
      return 0
    fi
    rm -f "$new_file"
    return 1
  }

  # rolling_deploy_service SERVICE — roll a single service with zero-downtime:
  #   1. scale up by 1 alongside the existing container(s)
  #   2. wait for the new container's health check
  #   3. stop & remove the old container(s)
  #   4. reconcile scale back to 1
  # Handles the case where a prior failed roll left >1 container running by
  # scaling to old_count+1 and treating every pre-existing container as old.
  # Requires the service to have a HEALTHCHECK defined; falls back to
  # treating "running" as healthy when no healthcheck is configured.
  rolling_deploy_service() {
    local service="$1"
    local old_containers new_container

    # Capture ALL existing containers so leftovers from a failed prior roll
    # don't get orphaned when we stop "the" old container below.
    old_containers="$(docker compose -f "$COMPOSE_FILE" ps -q "$service" || true)"
    local old_count=0
    if [ -n "$old_containers" ]; then
      old_count="$(printf '%s\n' "$old_containers" | wc -l | tr -d ' ')"
    fi
    local target_scale=$((old_count + 1))

    echo "Starting new $service container (scale=$target_scale, old=$old_count)..."
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --scale "$service=$target_scale" --no-recreate "$service"

    # The new container is the one in all_containers but not in old_containers.
    local all_containers
    all_containers="$(docker compose -f "$COMPOSE_FILE" ps -q "$service")"
    new_container=""
    for c in $all_containers; do
      local is_old=0
      for o in $old_containers; do
        if [ "$c" = "$o" ]; then is_old=1; break; fi
      done
      if [ "$is_old" -eq 0 ]; then new_container="$c"; break; fi
    done
    if [ -z "$new_container" ]; then
      echo "ERROR: could not identify new $service container"
      return 1
    fi

    # Short IDs make the post-mortem easier when a roll misbehaves — you can
    # pick either ID out of the logs and dig with `docker inspect` / `logs`.
    local old_short=""
    for oc in $old_containers; do
      if [ -n "$old_short" ]; then old_short="$old_short,"; fi
      old_short="$old_short${oc:0:12}"
    done
    echo "Rolling $service: new=${new_container:0:12} old=${old_short:-none}"

    if ! wait_container_healthy "$new_container" 180; then
      echo "Rolling back $service — removing failed container..."
      docker stop "$new_container" >/dev/null 2>&1 || true
      docker rm "$new_container" >/dev/null 2>&1 || true
      # Verify at least one old container is still serving. If every old
      # container has died, bring the service back up from compose.
      local any_running=0
      for oc in $old_containers; do
        local s
        s="$(docker inspect --format '{{.State.Status}}' "$oc" 2>/dev/null || echo "missing")"
        if [ "$s" = "running" ]; then any_running=1; break; fi
      done
      if [ "$any_running" -eq 0 ]; then
        echo "WARNING: no old $service containers are running — restarting service..."
        docker compose -f "$COMPOSE_FILE" up -d --no-deps "$service"
      fi
      return 1
    fi

    if [ -n "$old_containers" ]; then
      # Stop each old container with a long timeout so in-flight requests and
      # SSE streams have time to drain. Docker sends SIGTERM and only falls
      # back to SIGKILL once stop_grace_period (compose) / -t (CLI) expires.
      echo "Draining $old_count old $service container(s) (up to 120s each)..."
      for oc in $old_containers; do
        docker stop -t 120 "$oc" >/dev/null 2>&1 || true
        docker rm "$oc" >/dev/null 2>&1 || true
      done
    fi
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --scale "$service=1" "$service"
    echo "$service rolled over successfully."
  }

  # drain_worker_service SERVICE — send SIGTERM to the current worker and wait
  # for it to exit after draining its active jobs. The worker process handles
  # SIGTERM by marking itself draining, stopping new claims, and waiting for
  # in-flight work to finish before exiting.
  drain_worker_service() {
    local service="$1"
    local timeout="${WORKER_DRAIN_TIMEOUT:-7200}"
    local waited=0
    local cid

    cid="$(docker compose -f "$COMPOSE_FILE" ps -q "$service" | head -1 || true)"
    if [ -z "$cid" ]; then
      echo "No running $service container found — nothing to drain."
      return 0
    fi

    echo "Requesting drain for $service container ${cid:0:12}..."
    docker kill --signal=TERM "$cid" >/dev/null

    while docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | grep -q true; do
      if [ "$waited" -ge "$timeout" ]; then
        echo "ERROR: $service drain timed out after ${timeout}s"
        return 1
      fi
      sleep 5
      waited=$((waited + 5))
    done

    echo "$service drained successfully."
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

  # Ensure gVisor runtime is configured with the flags the sandbox depends on:
  #   --ignore-cgroups: Docker handles cgroup management (prevents EOF errors
  #     from cgroup conflicts).
  #   --host-uds=open: allow the in-sandbox 143-tools client to connect() to
  #     the per-session GitHub credential socket bind-mounted from the host.
  #     Default is "none", which makes connect() return ECONNREFUSED even
  #     though the inode is visible inside the sandbox.
  # Re-runs `runsc install` whenever either flag is missing so existing hosts
  # get patched on the next deploy.
  if [ "$ROLE" = "worker" ] && command -v runsc &>/dev/null; then
    DAEMON_JSON="/etc/docker/daemon.json"
    if [ ! -f "$DAEMON_JSON" ] || ! grep -q "ignore-cgroups" "$DAEMON_JSON" || ! grep -q "host-uds" "$DAEMON_JSON"; then
      echo "Patching runsc runtime with --ignore-cgroups --host-uds=open..."
      sudo runsc install -- --ignore-cgroups --host-uds=open
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
    # will fail until it does. enable_icc=false blocks one sandbox from
    # TCP-connecting to another on the same bridge.
    docker network inspect 143-sandbox >/dev/null 2>&1 || \
      docker network create --driver bridge \
        --opt com.docker.network.bridge.enable_icc=false \
        --label managed-by=143 143-sandbox
    # Install iptables-persistent on hosts that predate it (no-op otherwise).
    sudo apt-get install -y --no-install-recommends iptables-persistent >/dev/null 2>&1 || true
    # Re-apply sandbox egress firewall. Script is idempotent — safe to run
    # on every deploy. Ensures rules exist even if someone flushed iptables
    # or the sandbox network was recreated with a new subnet.
    if [ -x /opt/143/deploy/scripts/sandbox-firewall.sh ]; then
      sudo /opt/143/deploy/scripts/sandbox-firewall.sh 143-sandbox
    fi
  fi

  # Run migrations BEFORE restarting the app so the DB schema is ready when
  # the new code starts serving traffic. Uses `docker compose run` on the new
  # image (already pulled) to execute the migration binary without replacing
  # the running container. This prevents 500s from code referencing columns
  # that the old schema doesn't have yet.
  if [ "$ROLE" = "app" ]; then
    echo "Running database migrations..."
    docker compose -f "$COMPOSE_FILE" run --rm -T --no-deps api /bin/migrate up < /dev/null
    echo "Running coding-credentials Anthropic split post-step..."
    docker compose -f "$COMPOSE_FILE" run --rm -T --no-deps api /bin/migrate-coding-credentials-anthropic-split --allow-dual-set < /dev/null
  fi

  # Recreate out-of-band containers (vector, etc.) BEFORE the rolling deploy.
  # Skip services that we roll explicitly (api, frontend) so they don't get
  # force-recreated into downtime. Also skip caddy: we only restart it when
  # the Caddyfile has actually changed (see below), since restarting caddy
  # briefly unbinds ports 80/443 and surfaces as 502s to any proxy in front.
  if [ "$ROLE" = "app" ]; then
    echo "Updating supporting services..."
    recreate_other_services "api frontend caddy"
  elif [ "$ROLE" = "worker" ]; then
    echo "Updating supporting services..."
    recreate_other_services "$HEALTH_SERVICE"
  fi

  # Rolling deploy for both api and frontend on the app role. Order matters:
  # api first so the new code and any new DB columns are live before the
  # frontend that references them starts serving. --no-recreate keeps old
  # containers as-is during the health-check window.
  if [ "$ROLE" = "app" ]; then
    rolling_deploy_service api
    rolling_deploy_service frontend

    # Caddy: only restart when the Caddyfile contents actually changed. This
    # keeps deploys invisible at the edge for the common case (code-only
    # change). When the config changed, we prefer an in-place SIGUSR1 reload
    # over a container restart so ports 80/443 stay bound throughout.
    if stage_caddy_config_if_changed; then
      CADDY_ID="$(docker compose -f "$COMPOSE_FILE" ps -q caddy | head -1 || true)"
      if [ -n "$CADDY_ID" ]; then
        echo "Caddyfile changed — reloading caddy in place..."
        if ! docker exec "$CADDY_ID" caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile; then
          echo "In-place reload failed — falling back to container recreate."
          docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate caddy
        fi
      else
        docker compose -f "$COMPOSE_FILE" up -d --no-deps caddy
      fi
    else
      echo "Caddyfile unchanged — leaving caddy running."
    fi

  elif [ "$ROLE" = "worker" ]; then
    # Workers remain single-replica, but we drain the old replica before
    # replacement so accepted long-running sessions are not interrupted.
    #
    # Worker drain can take up to WORKER_DRAIN_TIMEOUT (default 45m in the
    # process, capped by docker stop_grace_period). Holding an SSH session
    # — and therefore a CI runner minute — open that long is wasteful, so
    # CI sets WORKER_DEPLOY_DETACH=1 to spawn the rollover as a backgrounded
    # host-side process and return immediately. Manual deploys leave it
    # unset to keep the synchronous "did it work?" feedback loop.
    if [ -n "${WORKER_DEPLOY_DETACH:-}" ]; then
      mkdir -p /var/log/143
      sha_short="${IMAGE_TAG:0:7}"
      log_file="/var/log/143/deploy-worker-$(date -u +%Y%m%dT%H%M%SZ)-${sha_short}.log"
      # Predictable status filename (one per SHA) so CI can poll for it
      # deterministically. "ok" on success, "fail: <reason>" otherwise.
      # Cleared inside the flocked block below so a same-SHA redeploy
      # can't wipe a still-running prior deploy's status file.
      status_file="/var/log/143/deploy-worker-${sha_short}.status"
      rollover_script="$(mktemp /tmp/143-rollover-worker-XXXXXX.sh)"
      # Bake the helpers + bound vars into a self-contained script. $(declare
      # -f ...) and "$VAR" expand at heredoc time (remote bash); \$ inside is
      # preserved for runtime.
      cat > "$rollover_script" <<EOS
#!/bin/bash
set -euo pipefail
$(declare -f drain_worker_service wait_container_healthy dump_diagnostics)
COMPOSE_FILE='$COMPOSE_FILE'
HEALTH_SERVICE='$HEALTH_SERVICE'
STATUS_FILE='$status_file'

# Always write a status file so the verify step has a deterministic signal.
# If we exit before the success line writes "ok", the trap leaves "fail".
on_exit() {
  rc=\$?
  if [ ! -s "\$STATUS_FILE" ] || ! grep -q '^ok' "\$STATUS_FILE"; then
    echo "fail: exit \$rc at \$(date -u -Iseconds)" > "\$STATUS_FILE"
  fi
}
trap on_exit EXIT

# Clear any stale status from a previous deploy of this same SHA. Done
# here (inside the flock) rather than from the parent shell so a
# concurrent same-SHA redeploy can't wipe an in-flight deploy's status.
rm -f "\$STATUS_FILE"

cd /opt/143
echo "[\$(date -u -Iseconds)] starting detached worker rollover (tag=$IMAGE_TAG)"
drain_worker_service "\$HEALTH_SERVICE"
docker compose -f "\$COMPOSE_FILE" up -d --no-deps --force-recreate "\$HEALTH_SERVICE"
cid="\$(docker compose -f "\$COMPOSE_FILE" ps -q "\$HEALTH_SERVICE" | head -1)"
if [ -n "\$cid" ]; then
  wait_container_healthy "\$cid" 120 || { echo "[\$(date -u -Iseconds)] HEALTH CHECK FAILED"; exit 1; }
fi
echo "[\$(date -u -Iseconds)] rollover succeeded"
echo "ok" > "\$STATUS_FILE"
EOS
      chmod 700 "$rollover_script"

      # setsid: new session, detached from the SSH controlling tty so the
      #   child survives session end (no SIGHUP).
      # flock: serialize against any prior detached deploy on this host so
      #   back-to-back commits can't race on docker.
      # </dev/null + redirect: nothing tied back to the SSH stdio so SSH can
      #   close cleanly.
      setsid bash -c "
        flock -x /tmp/143-deploy-worker.lock '$rollover_script' >>'$log_file' 2>&1
        rm -f '$rollover_script'
      " </dev/null >/dev/null 2>&1 &
      disown
      echo "Detached worker rollover launched."
      echo "  log:    $log_file"
      echo "  status: $status_file (poll for 'ok' / 'fail')"
      echo "  follow: ssh deploy@<host> tail -f $log_file"
    else
      drain_worker_service "$HEALTH_SERVICE"
      docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$HEALTH_SERVICE"

      CONTAINER_ID="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | head -1)"
      if [ -n "$CONTAINER_ID" ]; then
        if ! wait_container_healthy "$CONTAINER_ID" 120; then
          echo "ERROR: new worker failed health check"
          exit 1
        fi
      fi
      echo "$HEALTH_SERVICE restarted successfully."
    fi

  else
    # Non-rolling roles (db, logging) — just recreate everything.
    docker compose -f "$COMPOSE_FILE" up -d --force-recreate --remove-orphans

    CONTAINER_ID="$(docker compose -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | head -1)"
    if [ -n "$CONTAINER_ID" ]; then
      wait_container_healthy "$CONTAINER_ID" 120
    fi
  fi

  # Verify Vector is running on app/worker/logging nodes
  if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ] || [ "$ROLE" = "logging" ]; then
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
