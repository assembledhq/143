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

# BatchMode=yes prevents ssh from falling through to interactive password auth
# when the github-actions pubkey isn't in the host's authorized_keys yet — the
# deploy fails immediately with `Permission denied (publickey)` instead of
# looking like a stuck retry. Remediation when this fires:
#   make sync-keys APPLY=true
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

repair_deploy_sudoers() {
  bash "$SCRIPT_DIR/repair-deploy-sudoers.sh" "$ROLE" "$HOST" "$SSH_KEY"
}

# repair-deploy-sudoers.sh can ONLY fix one thing: a missing/incorrect NOPASSWD
# grant for the deploy user. It reaches the host over root SSH, which is disabled
# on the fleet, so it is a dead end for every other failure. Only route to it
# when the remote command actually tripped the sudo policy — otherwise a real
# error inside the invoked script (e.g. a leaked docker network endpoint) gets
# misreported as a sudoers problem and the operator is sent chasing root SSH that
# will never work. These signatures are what `sudo -n` prints when it refuses.
is_sudoers_failure() {
  printf '%s' "$1" | grep -qiE \
    'sudo: (a password is required|sorry, a password is required|no tty present)|not allowed to execute|is not in the sudoers file'
}

warn_log_rotation_skipped() {
  echo "WARNING: docker log rotation was not updated on this deploy; continuing."
  echo "  The service deploy will continue, but local Docker json-file logs may remain unbounded."
  echo "  To repair the host when root SSH is available, run:"
  echo "    make repair-deploy-sudoers ROLE=$ROLE HOST=$HOST SSH_KEY=$SSH_KEY"
}

warn_docker_dns_skipped() {
  echo "WARNING: docker daemon DNS pinning was not updated on this deploy; continuing."
  echo "  The service deploy will continue, but the host may still depend on its inherited"
  echo "  resolv.conf upstream — a single resolver outage will take all container DNS down."
  echo "  To repair the host when root SSH is available, run:"
  echo "    make repair-deploy-sudoers ROLE=$ROLE HOST=$HOST SSH_KEY=$SSH_KEY"
}

# Resolver list pinned into /etc/docker/daemon.json on every deploy. Three
# independent operators / networks: Cloudflare (1.1.1.1), Google (8.8.8.8),
# Quad9 (9.9.9.9). Order is fastest-first; Docker's embedded resolver at
# 127.0.0.11 falls through on SERVFAIL/timeout. Lives in deploy.sh (not the
# helper) so it's auditable in repo diff and trivially overridable for
# testing without touching the script that runs as root.
DOCKER_DNS_RESOLVERS=(1.1.1.1 8.8.8.8 9.9.9.9)

# App deploys must keep the Cloudflare-facing origin bound on 80/443. The
# daemon hardening helpers below can restart Docker when daemon.json changes,
# which recycles Caddy and creates a visible origin outage on a single app
# host. Keep those checks out of routine app deploys unless an operator is
# intentionally running maintenance.
ALLOW_DEPLOY_DOCKER_DAEMON_RESTART="${ALLOW_DEPLOY_DOCKER_DAEMON_RESTART:-0}"

run_worker_host_reconcile() {
  local DEPLOY_MODE_FILE="/opt/143/.deploy-mode"

  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "$(remote_env_assignment DEPLOY_MODE "${DEPLOY_MODE:-routine}")" \
    "$(remote_env_assignment DEPLOY_MODE_FILE "$DEPLOY_MODE_FILE")" \
    'bash -s' <<'REMOTE'
set -euo pipefail

case "${DEPLOY_MODE:-routine}" in
  routine|maintenance) ;;
  *) echo "ERROR: invalid DEPLOY_MODE for worker reconciliation: ${DEPLOY_MODE}" >&2; exit 1 ;;
esac

printf '%s %s\n' "$DEPLOY_MODE" "$(date +%s)" > "$DEPLOY_MODE_FILE"
trap 'rm -f "$DEPLOY_MODE_FILE"' EXIT
sudo -n /opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox
REMOTE
}

remote_shell_quote() {
  local value="$1"
  printf "'"
  printf '%s' "$value" | sed "s/'/'\\\\''/g"
  printf "'"
}

remote_env_assignment() {
  local key="$1" value="$2"
  printf '%s=' "$key"
  remote_shell_quote "$value"
}

apply_static_egress_worker_host_map() {
  if [ "$ROLE" != "worker" ] || [ -z "${STATIC_EGRESS_WORKER_HOSTS:-}" ]; then
    return
  fi

  # STATIC_EGRESS_WORKER_HOSTS is a comma-separated list of worker tunnel
  # identities. Entries are "<host>@<wg-address>@<private-key>".
  # Example:
  # STATIC_EGRESS_WORKER_HOSTS="87.99.158.39@10.143.0.2/32@abc=,54.1.2.3@10.143.0.3/32@def="
  IFS=',' read -ra mappings <<< "$STATIC_EGRESS_WORKER_HOSTS"
  for mapping in "${mappings[@]}"; do
    map_host="${mapping%%@*}"
    rest="${mapping#*@}"
    if [ "$map_host" != "$HOST" ]; then
      continue
    fi
    if [ "$rest" = "$mapping" ] || [[ "$rest" != *@* ]]; then
      echo "ERROR: invalid STATIC_EGRESS_WORKER_HOSTS entry for $HOST; expected host@wg-address@private-key" >&2
      exit 1
    fi
    map_wg_address="${rest%%@*}"
    map_private_key="${rest#*@}"
    if [ -z "$map_wg_address" ] || [ -z "$map_private_key" ]; then
      echo "ERROR: invalid STATIC_EGRESS_WORKER_HOSTS entry for $HOST; wg-address and private-key are required" >&2
      exit 1
    fi
    : "${STATIC_EGRESS_WORKER_WG_ADDRESS:=$map_wg_address}"
    : "${STATIC_EGRESS_WORKER_PRIVATE_KEY:=$map_private_key}"
    return
  done
}

# Canonical worker fingerprint input lists, shared by the staged fingerprint
# gate and the blue/green rollover (including the detached rollover script).
# Both sides MUST hash the same inputs: the gate repairs the persisted
# baseline from these lists, and the rollover compares its own computation
# against that baseline. If they diverge, every routine deploy fails with
# "config changed" even when nothing changed — the gate writes one hash, the
# rollover expects another, and DEPLOY_MODE=maintenance just re-arms the loop.
WORKER_HOST_RUNTIME_FINGERPRINT_FILES='/opt/143/deploy/scripts/reconcile-worker-host.sh /opt/143/deploy/scripts/sandbox-firewall.sh /opt/143/deploy/scripts/sandbox-resolv-conf.sh /opt/143/deploy/scripts/install-static-egress-worker.sh'
WORKER_DOCKER_DAEMON_FINGERPRINT_FILES='/opt/143/deploy/scripts/install-docker-dns.sh /opt/143/deploy/scripts/install-log-rotation.sh'
WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES='/opt/143/Dockerfile.dnsmasq /opt/143/docker-compose.dns-probe.yml'
WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES='chrome gvisor-check sandbox-dns'

run_worker_staged_fingerprint_gate() {
  if [ "$ROLE" != "worker" ]; then
    return 0
  fi
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "$(remote_env_assignment DEPLOY_MODE "${DEPLOY_MODE:-routine}")" \
    "$(remote_env_assignment WORKER_HOST_RUNTIME_FINGERPRINT_FILES "$WORKER_HOST_RUNTIME_FINGERPRINT_FILES")" \
    "$(remote_env_assignment WORKER_DOCKER_DAEMON_FINGERPRINT_FILES "$WORKER_DOCKER_DAEMON_FINGERPRINT_FILES")" \
    "$(remote_env_assignment WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES "$WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES")" \
    "$(remote_env_assignment WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES "$WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES")" \
    'bash -s' <<'REMOTE'
set -euo pipefail

: "${WORKER_HOST_RUNTIME_FINGERPRINT_FILES:?deploy.sh must pass the shared host-runtime fingerprint file list}"
: "${WORKER_DOCKER_DAEMON_FINGERPRINT_FILES:?deploy.sh must pass the shared docker-daemon fingerprint file list}"
: "${WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES:?deploy.sh must pass the shared support-service fingerprint file list}"
: "${WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES:?deploy.sh must pass the shared support-service compose service list}"

fingerprint_candidate_files() {
  local selected=()
  local path
  for path in "$@"; do
    if [ -e "${path}.new" ]; then
      selected+=("$path:${path}.new")
    elif [ -e "$path" ]; then
      selected+=("$path:$path")
    fi
  done
  if [ "${#selected[@]}" -eq 0 ]; then
    echo "none"
    return 0
  fi
  {
    local entry logical_path selected_path file_hash
    for entry in "${selected[@]}"; do
      logical_path="${entry%%:*}"
      selected_path="${entry#*:}"
      file_hash="$(sha256sum "$selected_path" | awk '{print $1}')"
      printf '%s  %s\n' "$file_hash" "$logical_path"
    done
  } | sha256sum | awk '{print $1}'
}

fingerprint_active_files() {
  local existing=()
  local path
  for path in "$@"; do
    if [ -e "$path" ]; then
      existing+=("$path")
    fi
  done
  if [ "${#existing[@]}" -eq 0 ]; then
    echo "none"
    return 0
  fi
  {
    local selected_path file_hash
    for selected_path in "${existing[@]}"; do
      file_hash="$(sha256sum "$selected_path" | awk '{print $1}')"
      printf '%s  %s\n' "$file_hash" "$selected_path"
    done
  } | sha256sum | awk '{print $1}'
}

compose_service_fingerprint() {
  local compose_file="$1"
  shift
  local read_file="$compose_file"
  if [ -e "${compose_file}.new" ]; then
    read_file="${compose_file}.new"
  fi
  if [ ! -e "$read_file" ]; then
    printf 'missing:%s\n' "$compose_file" | sha256sum | awk '{print $1}'
    return 0
  fi
  {
    local svc
    for svc in "$@"; do
      printf -- '--- %s:%s ---\n' "$compose_file" "$svc"
      awk -v svc="$svc" '
        /^  [A-Za-z0-9_.-]+:/ {
          current=$1
          sub(/:$/, "", current)
          in_service=(current == svc)
        }
        in_service { print }
      ' "$read_file"
    done
  } | sha256sum | awk '{print $1}'
}

compose_service_active_fingerprint() {
  local compose_file="$1"
  shift
  if [ ! -e "$compose_file" ]; then
    printf 'missing:%s\n' "$compose_file" | sha256sum | awk '{print $1}'
    return 0
  fi
  {
    local svc
    for svc in "$@"; do
      printf -- '--- %s:%s ---\n' "$compose_file" "$svc"
      awk -v svc="$svc" '
        /^  [A-Za-z0-9_.-]+:/ {
          current=$1
          sub(/:$/, "", current)
          in_service=(current == svc)
        }
        in_service { print }
      ' "$compose_file"
    done
  } | sha256sum | awk '{print $1}'
}

worker_process_config_fingerprint() {
  compose_service_fingerprint /opt/143/docker-compose.worker.yml worker
}

# The shared *_FINGERPRINT_FILES / *_COMPOSE_SERVICES lists are intentionally
# expanded unquoted: they are space-separated lists of paths/services.
worker_support_service_fingerprint() {
  {
    # shellcheck disable=SC2086
    compose_service_fingerprint /opt/143/docker-compose.worker.yml $WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES
    # shellcheck disable=SC2086
    fingerprint_candidate_files $WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES
  } | sha256sum | awk '{print $1}'
}

worker_active_support_service_fingerprint() {
  {
    # shellcheck disable=SC2086
    compose_service_active_fingerprint /opt/143/docker-compose.worker.yml $WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES
    # shellcheck disable=SC2086
    fingerprint_active_files $WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES
  } | sha256sum | awk '{print $1}'
}

worker_host_runtime_fingerprint() {
  # shellcheck disable=SC2086
  fingerprint_candidate_files $WORKER_HOST_RUNTIME_FINGERPRINT_FILES
}

worker_active_host_runtime_fingerprint() {
  # shellcheck disable=SC2086
  fingerprint_active_files $WORKER_HOST_RUNTIME_FINGERPRINT_FILES
}

worker_docker_daemon_fingerprint() {
  # shellcheck disable=SC2086
  fingerprint_candidate_files $WORKER_DOCKER_DAEMON_FINGERPRINT_FILES
}

worker_active_docker_daemon_fingerprint() {
  # shellcheck disable=SC2086
  fingerprint_active_files $WORKER_DOCKER_DAEMON_FINGERPRINT_FILES
}

repair_stale_worker_fingerprint() {
  local label="$1" file="$2" expected="$3" candidate="$4" active="$5"
  if [ "$expected" != "$candidate" ] && [ "$candidate" = "$active" ]; then
    echo "WARNING: stored worker $label fingerprint is stale; repairing baseline because staged candidate matches active files." >&2
    printf '%s\n' "$candidate" > "$file"
    return 0
  fi
  return 1
}

mode="${DEPLOY_MODE:-routine}"
worker_process_fingerprint="$(worker_process_config_fingerprint)"
support_fingerprint="$(worker_support_service_fingerprint)"
host_runtime_fingerprint="$(worker_host_runtime_fingerprint)"
docker_daemon_fingerprint="$(worker_docker_daemon_fingerprint)"
active_support_fingerprint="$(worker_active_support_service_fingerprint)"
active_host_runtime_fingerprint="$(worker_active_host_runtime_fingerprint)"
active_docker_daemon_fingerprint="$(worker_active_docker_daemon_fingerprint)"

worker_process_expected="$worker_process_fingerprint"
support_expected="$support_fingerprint"
host_runtime_expected="$host_runtime_fingerprint"
docker_daemon_expected="$docker_daemon_fingerprint"

[ ! -f /opt/143/.worker-process.fingerprint ] || worker_process_expected="$(cat /opt/143/.worker-process.fingerprint)"
[ ! -f /opt/143/.worker-support-services.v2.fingerprint ] || support_expected="$(cat /opt/143/.worker-support-services.v2.fingerprint)"
[ ! -f /opt/143/.worker-host-runtime.fingerprint ] || host_runtime_expected="$(cat /opt/143/.worker-host-runtime.fingerprint)"
[ ! -f /opt/143/.worker-docker-daemon.fingerprint ] || docker_daemon_expected="$(cat /opt/143/.worker-docker-daemon.fingerprint)"

if [ "$mode" = "routine" ] && [ "$support_expected" != "$support_fingerprint" ]; then
  if repair_stale_worker_fingerprint "support-service" /opt/143/.worker-support-services.v2.fingerprint "$support_expected" "$support_fingerprint" "$active_support_fingerprint"; then
    support_expected="$support_fingerprint"
  else
    echo "ERROR: worker support-service config changed during routine deploy; this can recreate sandbox-dns/chrome while sessions are active." >&2
    echo "  Routine deploys verify support services but do not activate support-service changes." >&2
    echo "  Run DEPLOY_MODE=maintenance after reviewing active runtime impact." >&2
    echo "current=$support_expected candidate=$support_fingerprint" >&2
    exit 1
  fi
fi
if [ "$mode" = "routine" ] && [ "$host_runtime_expected" != "$host_runtime_fingerprint" ]; then
  if repair_stale_worker_fingerprint "host-runtime" /opt/143/.worker-host-runtime.fingerprint "$host_runtime_expected" "$host_runtime_fingerprint" "$active_host_runtime_fingerprint"; then
    host_runtime_expected="$host_runtime_fingerprint"
  else
    echo "ERROR: worker host-runtime config changed during routine deploy; run DEPLOY_MODE=maintenance after reviewing active runtime impact." >&2
    echo "current=$host_runtime_expected candidate=$host_runtime_fingerprint" >&2
    exit 1
  fi
fi
if [ "$mode" = "routine" ] && [ "$docker_daemon_expected" != "$docker_daemon_fingerprint" ]; then
  if repair_stale_worker_fingerprint "docker-daemon" /opt/143/.worker-docker-daemon.fingerprint "$docker_daemon_expected" "$docker_daemon_fingerprint" "$active_docker_daemon_fingerprint"; then
    docker_daemon_expected="$docker_daemon_fingerprint"
  else
    echo "ERROR: worker docker-daemon config changed during routine deploy; run DEPLOY_MODE=maintenance after reviewing active runtime impact." >&2
    echo "current=$docker_daemon_expected candidate=$docker_daemon_fingerprint" >&2
    exit 1
  fi
fi
REMOTE
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

# The encrypted bundle lives in the private secrets checkout, not this
# (public) repo. Resolved worktree-safely; see resolve-secrets-dir.sh.
SECRETS_DIR="$("$SCRIPT_DIR/resolve-secrets-dir.sh" "$PROJECT_DIR")"
ENC_FILE="$SECRETS_DIR/.env.production.enc"
# Having the age key but no bundle means the private secrets checkout is
# missing — deploying anyway would silently reuse stale /opt/143/.env and
# skip the bundle scp. Fail loudly instead of degrading.
if [ -n "${SOPS_AGE_KEY:-}" ] && [ ! -f "$ENC_FILE" ]; then
  echo "ERROR: SOPS_AGE_KEY is set but $ENC_FILE does not exist." >&2
  echo "Clone the private secrets repo next to the main checkout (see docs/secrets/README.md) or set SECRETS_DIR." >&2
  exit 1
fi
if [ -n "${SOPS_AGE_KEY:-}" ] && [ -f "$ENC_FILE" ]; then
  echo "Refreshing secrets from .env.production.enc..."
  DECRYPTED=$(SOPS_AGE_KEY="$SOPS_AGE_KEY" sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE")

  while IFS= read -r line; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    if [ -z "${!key:-}" ]; then
      export "$key=$value"
    fi
  done <<< "$DECRYPTED"

  apply_worker_bucket_overrides "$ROLE" "$HOST"
  apply_static_egress_worker_host_map

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
    : "${DB_BIND_IP:?DB_BIND_IP is required for db role (set it to the db node primary private IP)}"
    printf 'DB_PASSWORD=%s\nDB_BIND_IP=%s\n' "$DB_PASSWORD" "$DB_BIND_IP" \
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
    : "${SANDBOX_HEALTH_CHECK_IMAGE:=busybox:1.36.1}"
    : "${SANDBOX_REQUIRE_DISK_QUOTA:=true}"
    : "${SANDBOX_GC_INTERVAL:=5m}"
    : "${SANDBOX_GC_GRACE:=30m}"
    : "${SANDBOX_GC_HARD_MAX:=24h}"
    : "${STATIC_EGRESS_PROBE_IMAGE:=ghcr.io/assembledhq/143-sandbox:$TAG}"
    # Refresh the shared secrets in /opt/143/.env, then re-append the per-host
    # identity/runtime values from /opt/143/.env.local (NODE_ID,
    # WORKER_PRIVATE_IP, PREVIEW_INTERNAL_BASE_URL, DOCKER_GID) so docker
    # compose can still interpolate them when it parses the compose file.
    # .env.local is owned by provisioning and we abort if it's missing instead
    # of silently coming up with empty/unsafe defaults.
    printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nWORKER_PROCESS_COUNT=%s\nWORKER_MAX_ACTIVE_SANDBOXES=%s\nWORKER_PREVIEW_DRAIN_TIMEOUT=%s\nSANDBOX_CPU_LIMIT=%s\nSANDBOX_MEMORY_LIMIT_MB=%s\nSANDBOX_DISK_LIMIT_GB=%s\nSANDBOX_HEALTH_CHECK_IMAGE=%s\nSANDBOX_REQUIRE_DISK_QUOTA=%s\nSANDBOX_GC_INTERVAL=%s\nSANDBOX_GC_GRACE=%s\nSANDBOX_GC_HARD_MAX=%s\nSTATIC_EGRESS_PUBLIC_IP=%s\nSTATIC_EGRESS_PROBE_IMAGE=%s\n' \
      "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" \
      "${WORKER_PROCESS_COUNT:-}" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "${WORKER_PREVIEW_DRAIN_TIMEOUT:-}" "${SANDBOX_CPU_LIMIT:-}" "${SANDBOX_MEMORY_LIMIT_MB:-}" "${SANDBOX_DISK_LIMIT_GB:-}" \
      "$SANDBOX_HEALTH_CHECK_IMAGE" "$SANDBOX_REQUIRE_DISK_QUOTA" "$SANDBOX_GC_INTERVAL" "$SANDBOX_GC_GRACE" "$SANDBOX_GC_HARD_MAX" \
      "${STATIC_EGRESS_PUBLIC_IP:-}" "$STATIC_EGRESS_PROBE_IMAGE" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" '
          set -euo pipefail
          cat > /opt/143/.env
          chmod 600 /opt/143/.env
          if [ ! -f /opt/143/.env.local ]; then
            echo "ERROR: /opt/143/.env.local is missing on this host." >&2
            echo "       It holds NODE_ID, WORKER_PRIVATE_IP, PREVIEW_INTERNAL_BASE_URL, and DOCKER_GID." >&2
            echo "       Re-run: make provision-worker HOST=<this-host>" >&2
            exit 1
          fi
          if ! grep -q "^DOCKER_GID=" /opt/143/.env.local; then
            DOCKER_GID="$(getent group docker | cut -d: -f3)"
            if [ -z "$DOCKER_GID" ]; then
              echo "ERROR: could not resolve docker group GID on this worker." >&2
              echo "       Re-run: make provision-worker HOST=<this-host>" >&2
              exit 1
            fi
            printf "DOCKER_GID=%s\n" "$DOCKER_GID" >> /opt/143/.env.local
          fi
          cat /opt/143/.env.local >> /opt/143/.env
        '
    printf 'STATIC_EGRESS_GATEWAY_PUBLIC_IP=%s\nSTATIC_EGRESS_GATEWAY_PUBLIC_KEY=%s\nSTATIC_EGRESS_WORKER_PRIVATE_KEY=%s\nSTATIC_EGRESS_WORKER_WG_ADDRESS=%s\n' \
      "${STATIC_EGRESS_GATEWAY_PUBLIC_IP:-}" "${STATIC_EGRESS_GATEWAY_PUBLIC_KEY:-}" "${STATIC_EGRESS_WORKER_PRIVATE_KEY:-}" "${STATIC_EGRESS_WORKER_WG_ADDRESS:-}" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/static-egress-worker.env && chmod 600 /opt/143/static-egress-worker.env'
    scp "${SCP_OPTS[@]}" "$ENC_FILE" deploy@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" "chmod 644 /opt/143/.env.production.enc"
  else
    # App nodes need SOPS_AGE_KEY + the encrypted secrets file so the
    # entrypoint can decrypt GitHub App creds, API keys, etc. at boot.
    : "${DB_PASSWORD:?DB_PASSWORD is required for app role (set it or add to .env.production.enc)}"
    : "${DB_HOST:?DB_HOST is required for app role (set it or add to .env.production.enc)}"
    : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for app role (set it or add to .env.production.enc)}"
    : "${CLOUDFLARE_API_TOKEN:?CLOUDFLARE_API_TOKEN is required for app role (set it or add to .env.production.enc)}"
    : "${DOMAIN:=143.dev}"
    : "${PREVIEW_ORIGIN_TEMPLATE:=https://{id}.preview.143.dev}"
    : "${NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE:=$PREVIEW_ORIGIN_TEMPLATE}"
    printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nDOMAIN=%s\nCLOUDFLARE_API_TOKEN=%s\nPREVIEW_ORIGIN_TEMPLATE=%s\nNEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE=%s\nSTATIC_EGRESS_PUBLIC_IP=%s\n' "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" "$DOMAIN" "$CLOUDFLARE_API_TOKEN" "$PREVIEW_ORIGIN_TEMPLATE" "$NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE" "${STATIC_EGRESS_PUBLIC_IP:-}" \
      | ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat > /opt/143/.env && chmod 600 /opt/143/.env'
    scp "${SCP_OPTS[@]}" "$ENC_FILE" deploy@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" "chmod 644 /opt/143/.env.production.enc"
  fi
  echo "Secrets refreshed."
else
  echo "Skipping secret refresh (no SOPS key or .env.production.enc not found)."
fi

# Sync compose file so the remote always runs the latest version. Worker
# compose can include support-service changes, so stage it until the routine
# worker fingerprint gate allows promotion.
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ]; then
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/$COMPOSE_FILE" deploy@"$HOST":/opt/143/"$COMPOSE_FILE".new
else
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/$COMPOSE_FILE" deploy@"$HOST":/opt/143/
fi
if [ "$ROLE" = "db" ]; then
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" "mkdir -p /opt/143/deploy/postgres"
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/postgres/postgresql.conf" deploy@"$HOST":/opt/143/deploy/postgres/
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/postgres/pg_hba.conf" deploy@"$HOST":/opt/143/deploy/postgres/
fi
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ] || [ "$ROLE" = "logging" ]; then
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.vector.yml" deploy@"$HOST":/opt/143/
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" "mkdir -p /opt/143/deploy /opt/143/deploy/scripts"
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/vector.yaml" deploy@"$HOST":/opt/143/deploy/
fi
# DNS probe is included by app and worker compose files (logging has its
# own stack and doesn't include it). Stage the file so docker compose can
# resolve the include directive.
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ]; then
  if [ "$ROLE" = "worker" ]; then
    scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.dns-probe.yml" deploy@"$HOST":/opt/143/docker-compose.dns-probe.yml.new
  else
    scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.dns-probe.yml" deploy@"$HOST":/opt/143/
  fi
fi
if [ "$ROLE" = "logging" ]; then
  # Older logging hosts may have root-owned vmalert/grafana dirs from a prior
  # provision step; without ownership the deploy user can't unlink the entries
  # in `rm -rf` below. Mirror the worker pattern: try a non-interactive sudo
  # chown (narrowly granted in deploy/scripts/bootstrap.sh), tolerate failure
  # so the rm still runs on hosts where files are already deploy-owned.
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "sudo -n chown -R deploy:deploy /opt/143/deploy/vmalert 2>&1 | sed 's/^/  chown: /' || true; \
     sudo -n chown -R deploy:deploy /opt/143/deploy/grafana 2>&1 | sed 's/^/  chown: /' || true; \
     sudo -n chown -R deploy:deploy /opt/143/deploy/scripts 2>&1 | sed 's/^/  chown: /' || true"
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" "rm -rf /opt/143/deploy/grafana/provisioning /opt/143/deploy/vmalert/rules && mkdir -p /opt/143/deploy/grafana /opt/143/deploy/vmalert"
  scp -r "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/grafana/provisioning" deploy@"$HOST":/opt/143/deploy/grafana/
  scp -r "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/vmalert/rules" deploy@"$HOST":/opt/143/deploy/vmalert/
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/alertmanager_slack_relay.py" \
    deploy@"$HOST":/opt/143/deploy/scripts/alertmanager_slack_relay.py.new
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "mv /opt/143/deploy/scripts/alertmanager_slack_relay.py.new /opt/143/deploy/scripts/alertmanager_slack_relay.py \
     && chmod 644 /opt/143/deploy/scripts/alertmanager_slack_relay.py \
     || { rm -f /opt/143/deploy/scripts/alertmanager_slack_relay.py.new; exit 1; }"
fi
if [ "$ROLE" = "app" ]; then
  # Sync Caddyfile so the remote always has the latest reverse-proxy config.
  # The remote compares the new hash against the currently running copy to
  # decide whether to restart Caddy (see `stage_caddy_config_if_changed` below).
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/Caddyfile" deploy@"$HOST":/opt/143/deploy/Caddyfile.new
  # The app host builds a custom Caddy image locally so wildcard preview certs
  # can use the Cloudflare DNS challenge. Stage the Dockerfile next to the
  # compose file; the remote deploy compares it with the active copy before
  # deciding whether to rebuild/recreate the Cloudflare-facing Caddy origin.
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/Dockerfile.caddy" \
    deploy@"$HOST":/opt/143/Dockerfile.caddy.new
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

  # Sync the sandbox-resolv.conf writer. This is the single source of truth
  # for /etc/143/sandbox-resolv.conf, which gets bind-mounted into every
  # sandbox at /etc/resolv.conf. The earlier chown above normalized
  # ownership for the whole /opt/143/deploy/scripts dir, so re-using it
  # here without another chown is fine. Atomic-rename via .new for the
  # same ETXTBSY-class reasons noted on sandbox-firewall.sh above.
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/sandbox-resolv-conf.sh" \
    deploy@"$HOST":/opt/143/deploy/scripts/sandbox-resolv-conf.sh.new

  # Sync the static egress worker installer before reconciliation. Existing
  # workers created before this feature need the helper present locally so
  # configured static egress can fail closed instead of silently skipping
  # WireGuard/policy-route installation.
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-static-egress-worker.sh" \
    deploy@"$HOST":/opt/143/deploy/scripts/install-static-egress-worker.sh.new

  # Sync the canonical worker host reconciler. It owns the sandbox network,
  # firewall, resolv.conf, sandbox-auth socket dir, and worker sysctl state.
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/reconcile-worker-host.sh" \
    deploy@"$HOST":/opt/143/deploy/scripts/reconcile-worker-host.sh.new

  # Sync Dockerfile.dnsmasq alongside the worker compose file. The
  # sandbox-dns service is built locally on each worker (see
  # docker-compose.worker.yml) and the build context is /opt/143, so the
  # Dockerfile must live next to the compose file before `docker compose
  # up` runs. Atomic-rename via .new for the same ETXTBSY-class reasons
  # noted on sandbox-firewall.sh above.
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/Dockerfile.dnsmasq" \
    deploy@"$HOST":/opt/143/Dockerfile.dnsmasq.new
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-log-rotation.sh" \
    deploy@"$HOST":/opt/143/deploy/scripts/install-log-rotation.sh.new
  scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-docker-dns.sh" \
    deploy@"$HOST":/opt/143/deploy/scripts/install-docker-dns.sh.new
  run_worker_staged_fingerprint_gate
  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "mv /opt/143/$COMPOSE_FILE.new /opt/143/$COMPOSE_FILE \
     && mv /opt/143/docker-compose.dns-probe.yml.new /opt/143/docker-compose.dns-probe.yml \
     && mv /opt/143/deploy/scripts/sandbox-firewall.sh.new /opt/143/deploy/scripts/sandbox-firewall.sh \
     && mv /opt/143/deploy/scripts/sandbox-resolv-conf.sh.new /opt/143/deploy/scripts/sandbox-resolv-conf.sh \
     && chmod +x /opt/143/deploy/scripts/sandbox-resolv-conf.sh \
     && mv /opt/143/deploy/scripts/install-static-egress-worker.sh.new /opt/143/deploy/scripts/install-static-egress-worker.sh \
     && chmod +x /opt/143/deploy/scripts/install-static-egress-worker.sh \
     && mv /opt/143/deploy/scripts/reconcile-worker-host.sh.new /opt/143/deploy/scripts/reconcile-worker-host.sh \
     && chmod +x /opt/143/deploy/scripts/reconcile-worker-host.sh \
     && mv /opt/143/Dockerfile.dnsmasq.new /opt/143/Dockerfile.dnsmasq \
     || { rm -f /opt/143/$COMPOSE_FILE.new /opt/143/docker-compose.dns-probe.yml.new /opt/143/deploy/scripts/sandbox-firewall.sh.new /opt/143/deploy/scripts/sandbox-resolv-conf.sh.new /opt/143/deploy/scripts/install-static-egress-worker.sh.new /opt/143/deploy/scripts/reconcile-worker-host.sh.new /opt/143/Dockerfile.dnsmasq.new; exit 1; }"

  echo "Reconciling worker host invariants..."
  if [ -n "${STATIC_EGRESS_PUBLIC_IP:-}" ]; then
    static_egress_probe_image="${STATIC_EGRESS_PROBE_IMAGE:-ghcr.io/assembledhq/143-sandbox:$TAG}"
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
      "docker pull \"$static_egress_probe_image\""
  fi
  # Capture the reconcile output (while still streaming it live via tee) so we
  # can tell a sudoers refusal apart from a genuine reconcile error. PIPESTATUS
  # gives us the ssh exit status, which propagates the remote script's exit code;
  # tee almost never fails. set +e keeps the failing pipeline from aborting the
  # script before we classify it.
  reconcile_log="$(mktemp)"
  set +e
  run_worker_host_reconcile 2>&1 | tee "$reconcile_log"
  reconcile_rc="${PIPESTATUS[0]}"
  set -e
  if [ "$reconcile_rc" -ne 0 ]; then
    if is_sudoers_failure "$(cat "$reconcile_log")"; then
      echo "reconcile-worker-host.sh blocked by missing deploy sudoers; trying no-teardown deploy sudoers repair..."
      if repair_deploy_sudoers; then
        echo "Retrying worker host reconciliation after sudoers repair..."
        run_worker_host_reconcile
      else
        echo "ERROR: reconcile-worker-host.sh was blocked by sudoers and repair via root SSH did not complete."
        echo "  Run once from a machine with root SSH access:"
        echo "    make repair-deploy-sudoers ROLE=$ROLE HOST=$HOST SSH_KEY=$SSH_KEY"
        echo "  Then re-run the deploy."
        rm -f "$reconcile_log"
        exit 1
      fi
    else
      echo "ERROR: reconcile-worker-host.sh failed on $HOST. This is NOT a sudoers problem"
      echo "  (the deploy sudo grant ran); the underlying reconcile error is printed above."
      echo "  Common cause: a leaked docker network endpoint holding sandbox-dns's pinned IP"
      echo "  (\"Address already in use\"). reconcile-worker-host.sh retries leaked-endpoint cleanup;"
      echo "  if it persists, on the host run: docker network inspect 143-sandbox, then"
      echo "  docker network disconnect -f 143-sandbox <stale-endpoint>, and re-run the deploy."
      echo "  If instead you see an SSH 'Permission denied (publickey)', run: make sync-keys APPLY=true"
      rm -f "$reconcile_log"
      exit 1
    fi
  fi
  rm -f "$reconcile_log"
fi

if [ "$ROLE" = "app" ] && [ "$ALLOW_DEPLOY_DOCKER_DAEMON_RESTART" != "1" ]; then
  echo "Skipping docker log rotation check on app deploy; set ALLOW_DEPLOY_DOCKER_DAEMON_RESTART=1 for explicit maintenance."
else
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
    run_worker_staged_fingerprint_gate
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
fi

if { [ "$ROLE" = "app" ] || { [ "$ROLE" = "worker" ] && [ "${DEPLOY_MODE:-routine}" = "routine" ]; }; } && [ "$ALLOW_DEPLOY_DOCKER_DAEMON_RESTART" != "1" ]; then
  if [ "$ROLE" = "worker" ]; then
    run_worker_staged_fingerprint_gate
    ssh "${SSH_OPTS[@]}" deploy@"$HOST" "rm -f /opt/143/deploy/scripts/install-docker-dns.sh.new"
  fi
  echo "Skipping docker daemon DNS check on $ROLE deploy; set ALLOW_DEPLOY_DOCKER_DAEMON_RESTART=1 and DEPLOY_MODE=maintenance for explicit maintenance."
else
  # --- Docker daemon DNS resolvers (idempotent) ---
  # Pin /etc/docker/daemon.json's `dns` list to multiple independent
  # resolvers so a single upstream DNS outage doesn't take every container's
  # outbound DNS down at once. The 2026-05-07T04:15Z incident hit three
  # workers simultaneously this way (sandboxes couldn't resolve chatgpt.com,
  # workers couldn't resolve github.com) because the embedded resolver at
  # 127.0.0.11 inherits a single host resolv.conf entry by default.
  #
  # Sync + invoke pattern mirrors install-log-rotation.sh above.
  DOCKER_DNS_READY=1

  ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
    "sudo -n chown -R deploy:deploy /opt/143/deploy/scripts 2>&1 | sed 's/^/  chown: /' || true"
  if ! scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-docker-dns.sh" \
      deploy@"$HOST":/opt/143/deploy/scripts/install-docker-dns.sh.new; then
    echo "install-docker-dns.sh sync failed; trying no-teardown deploy sudoers repair..."
    if repair_deploy_sudoers; then
      ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
        "sudo -n chown -R deploy:deploy /opt/143/deploy/scripts 2>&1 | sed 's/^/  chown: /' || true"
      scp -p "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-docker-dns.sh" \
        deploy@"$HOST":/opt/143/deploy/scripts/install-docker-dns.sh.new
    else
      warn_docker_dns_skipped
      DOCKER_DNS_READY=0
    fi
  fi
  if [ "$DOCKER_DNS_READY" -eq 1 ]; then
    if ! ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
      "mv /opt/143/deploy/scripts/install-docker-dns.sh.new /opt/143/deploy/scripts/install-docker-dns.sh \
       && chmod +x /opt/143/deploy/scripts/install-docker-dns.sh \
       || { rm -f /opt/143/deploy/scripts/install-docker-dns.sh.new; exit 1; }"; then
      warn_docker_dns_skipped
      DOCKER_DNS_READY=0
    fi
  fi

  if [ "$DOCKER_DNS_READY" -eq 1 ]; then
    echo "Ensuring docker daemon DNS resolvers (${DOCKER_DNS_RESOLVERS[*]})..."
    run_docker_dns() {
      ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
        "sudo -n /opt/143/deploy/scripts/install-docker-dns.sh ${DOCKER_DNS_RESOLVERS[*]}"
    }
    if ! run_docker_dns; then
      echo "install-docker-dns.sh failed under deploy+sudo; trying no-teardown deploy sudoers repair..."
      if repair_deploy_sudoers; then
        echo "Retrying docker daemon DNS pinning after sudoers repair..."
        if ! run_docker_dns; then
          warn_docker_dns_skipped
        fi
      else
        warn_docker_dns_skipped
      fi
    fi
  fi
fi

ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "$(remote_env_assignment COMPOSE_FILE "$COMPOSE_FILE")" \
  "$(remote_env_assignment HEALTH_SERVICE "$HEALTH_SERVICE")" \
  "$(remote_env_assignment ROLE "$ROLE")" \
  "$(remote_env_assignment IMAGE_TAG "$TAG")" \
  "$(remote_env_assignment WORKER_DEPLOY_DETACH "${WORKER_DEPLOY_DETACH:-}")" \
  "$(remote_env_assignment WORKER_DEPLOY_DRAIN_TIMEOUT_SECONDS "${WORKER_DEPLOY_DRAIN_TIMEOUT_SECONDS:-}")" \
  "$(remote_env_assignment WORKER_BLUE_GREEN_PORT_START "${WORKER_BLUE_GREEN_PORT_START:-}")" \
  "$(remote_env_assignment WORKER_BLUE_GREEN_PORT_END "${WORKER_BLUE_GREEN_PORT_END:-}")" \
  "$(remote_env_assignment WORKER_BLUE_GREEN_PREFLIGHT_ATTEMPTS "${WORKER_BLUE_GREEN_PREFLIGHT_ATTEMPTS:-}")" \
  "$(remote_env_assignment WORKER_BLUE_GREEN_PREFLIGHT_RETRY_DELAY_SECONDS "${WORKER_BLUE_GREEN_PREFLIGHT_RETRY_DELAY_SECONDS:-}")" \
  "$(remote_env_assignment WORKER_BASE_NODE_ID "${WORKER_BASE_NODE_ID:-}")" \
  "$(remote_env_assignment WORKER_DRAIN_TIMEOUT "${WORKER_DRAIN_TIMEOUT:-}")" \
  "$(remote_env_assignment DEPLOY_MODE "${DEPLOY_MODE:-routine}")" \
  "$(remote_env_assignment DEPLOY_REQUESTED_BY "${DEPLOY_REQUESTED_BY:-deploy-script}")" \
  "$(remote_env_assignment DEPLOY_REASON "${DEPLOY_REASON:-routine worker rollout}")" \
  "$(remote_env_assignment FORCE_DEPLOY_WITH_ACTIVE_SESSIONS "${FORCE_DEPLOY_WITH_ACTIVE_SESSIONS:-}")" \
  "$(remote_env_assignment SESSION_EXECUTOR_DOCKER_NETWORK "${SESSION_EXECUTOR_DOCKER_NETWORK:-}")" \
  "$(remote_env_assignment DEPLOY_DOCKER_PRUNE "${DEPLOY_DOCKER_PRUNE:-1}")" \
  "$(remote_env_assignment DOCKER_PRUNE_UNTIL "${DOCKER_PRUNE_UNTIL:-24h}")" \
  "$(remote_env_assignment DEPLOY_DOCKER_VOLUME_PRUNE "${DEPLOY_DOCKER_VOLUME_PRUNE:-0}")" \
  "$(remote_env_assignment WORKER_HOST_RUNTIME_FINGERPRINT_FILES "$WORKER_HOST_RUNTIME_FINGERPRINT_FILES")" \
  "$(remote_env_assignment WORKER_DOCKER_DAEMON_FINGERPRINT_FILES "$WORKER_DOCKER_DAEMON_FINGERPRINT_FILES")" \
  "$(remote_env_assignment WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES "$WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES")" \
  "$(remote_env_assignment WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES "$WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES")" \
  bash << 'REMOTE'
  set -euo pipefail
  cd /opt/143

  # Clean up staged Caddy inputs on any exit path.
  # stage_caddy_*_if_changed normally consumes them (mv or rm), but this
  # guards against a failure between the scp and that call leaving it on disk.
  trap 'rm -f /opt/143/deploy/Caddyfile.new /opt/143/Dockerfile.caddy.new /opt/143/.caddy-env.fingerprint.new /opt/143/.caddy-service.fingerprint.new' EXIT

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
  # the staged file into place. Returns 1 (and removes the staged file) when
  # contents match. Used to avoid restarting Caddy on code-only deploys; Caddy
  # restarts briefly unbind ports 80/443 and surface as 502s through any
  # upstream proxy (Cloudflare, etc.).
  #
  # The promotion MUST overwrite the existing file in place (truncate + rewrite)
  # rather than `mv` it. docker-compose.app.yml bind-mounts the Caddyfile as a
  # single file (./deploy/Caddyfile:/etc/caddy/Caddyfile), and Docker pins a
  # single-file mount to the file's inode at container start. `mv` replaces the
  # path with a NEW inode, so the running container stays bound to the OLD one
  # and never sees the change — `caddy reload` reads the frozen inode and logs
  # "config is unchanged", a silent no-op. Overwriting in place keeps the inode
  # stable so the bind-mounted file (and reload) reflects the new content. See
  # the "Caddy bind-mount drift" incident (June 2026, install routes 404'd for
  # ~10 days). Use a shell redirect, not `mv`: a rename always allocates a new
  # inode. `cat >` is also preferred over `cp`, whose in-place-truncate vs.
  # unlink-and-recreate behavior varies across implementations and flags, while
  # a redirect is unambiguously an in-place truncate+rewrite. That write is
  # therefore NOT atomic, so the guards below refuse an empty staged file and
  # treat a failed write as fatal rather than leaving a corrupt live config.
  stage_caddy_config_if_changed() {
    local new_file="/opt/143/deploy/Caddyfile.new"
    local cur_file="/opt/143/deploy/Caddyfile"
    [ -f "$new_file" ] || return 1
    if [ -f "$cur_file" ] && cmp -s "$new_file" "$cur_file"; then
      rm -f "$new_file"
      return 1
    fi
    # The overwrite below truncates cur_file before writing and is not atomic, so
    # a bad write leaves the LIVE Caddyfile corrupt with no rollback. Refuse an
    # empty staged file (checked before any truncation), and abort the deploy on
    # a write failure instead of reporting a silent "unchanged" — shipping a
    # truncated config would break the next caddy (re)load and can unbind
    # :80/:443 on container recreate. A valid Caddyfile is never empty.
    if [ ! -s "$new_file" ]; then
      echo "ERROR: staged Caddyfile $new_file is empty; refusing to overwrite live config" >&2
      rm -f "$new_file"
      exit 1
    fi
    if ! cat "$new_file" > "$cur_file"; then
      echo "ERROR: failed writing $cur_file from staged Caddyfile (live config may be truncated)" >&2
      rm -f "$new_file"
      exit 1
    fi
    rm -f "$new_file"
    return 0
  }

  # stage_caddy_dockerfile_if_changed — returns 0 only when Dockerfile.caddy
  # changed and promotes the staged copy. Routine code-only deploys must not
  # rebuild Caddy because compose may replace the public 80/443 listener.
  stage_caddy_dockerfile_if_changed() {
    local new_file="/opt/143/Dockerfile.caddy.new"
    local cur_file="/opt/143/Dockerfile.caddy"
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

  promote_staged_app_compose() {
    local new_file="/opt/143/$COMPOSE_FILE.new"
    local cur_file="/opt/143/$COMPOSE_FILE"
    [ -f "$new_file" ] || return 0
    mv "$new_file" "$cur_file"
  }

  caddy_env_from_env_file() {
    local env_file="/opt/143/.env"
    awk -F= '
      BEGIN { domain = "143.dev"; token = "" }
      $1 == "DOMAIN" { domain = substr($0, index($0, "=") + 1) }
      $1 == "CLOUDFLARE_API_TOKEN" { token = substr($0, index($0, "=") + 1) }
      END {
        printf "DOMAIN=%s\n", domain
        printf "CLOUDFLARE_API_TOKEN=%s\n", token
      }
    ' "$env_file"
  }

  caddy_env_from_container() {
    local caddy_id="$1"
    docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$caddy_id" | awk -F= '
      BEGIN { domain = "143.dev"; token = "" }
      $1 == "DOMAIN" { domain = substr($0, index($0, "=") + 1) }
      $1 == "CLOUDFLARE_API_TOKEN" { token = substr($0, index($0, "=") + 1) }
      END {
        printf "DOMAIN=%s\n", domain
        printf "CLOUDFLARE_API_TOKEN=%s\n", token
      }
    '
  }

  caddy_env_fingerprint() {
    caddy_env_from_env_file | sha256sum | awk '{print $1}'
  }

  caddy_container_env_fingerprint() {
    local caddy_id="$1"
    caddy_env_from_container "$caddy_id" | sha256sum | awk '{print $1}'
  }

  caddy_env_fingerprint_changed() {
    local caddy_id="${1:-}"
    local fp_file="/opt/143/.caddy-env.fingerprint"
    local next current
    next="$(caddy_env_fingerprint)"

    if [ -f "$fp_file" ]; then
      current="$(cat "$fp_file")"
    elif [ -n "$caddy_id" ]; then
      current="$(caddy_container_env_fingerprint "$caddy_id")"
      if [ "$current" = "$next" ]; then
        printf '%s\n' "$next" > "$fp_file"
        return 1
      fi
    else
      current=""
    fi

    if [ "$current" != "$next" ]; then
      printf '%s\n' "$next" > "$fp_file.new"
      return 0
    fi
    rm -f "$fp_file.new"
    return 1
  }

  commit_caddy_env_fingerprint() {
    local fp_file="/opt/143/.caddy-env.fingerprint"
    if [ -f "$fp_file.new" ]; then
      mv "$fp_file.new" "$fp_file"
    fi
  }

  caddy_service_config_fingerprint() {
    compose_service_fingerprint /opt/143/docker-compose.app.yml caddy
  }

  caddy_active_service_config_fingerprint() {
    compose_service_active_fingerprint /opt/143/docker-compose.app.yml caddy
  }

  caddy_service_config_changed() {
    local fp_file="/opt/143/.caddy-service.fingerprint"
    local next current
    next="$(caddy_service_config_fingerprint)"

    if [ ! -f "$fp_file" ]; then
      printf '%s\n' "$next" > "$fp_file.new"
      return 0
    fi
    current="$(cat "$fp_file")"

    if [ "$current" != "$next" ]; then
      printf '%s\n' "$next" > "$fp_file.new"
      return 0
    fi
    rm -f "$fp_file.new"
    return 1
  }

  commit_caddy_service_config_fingerprint() {
    local fp_file="/opt/143/.caddy-service.fingerprint"
    if [ -f "$fp_file.new" ]; then
      mv "$fp_file.new" "$fp_file"
    fi
  }

  # reconcile_caddy_service — applies app-edge Caddy changes with the least
  # disruptive path available:
  #   1. Leave Caddy untouched for routine code-only deploys.
  #   2. Recreate Caddy only when Dockerfile.caddy changed, Caddy-specific env
  #      changed, or the container is missing.
  #   3. If only deploy/Caddyfile changed, run `caddy reload` in place so ports
  #      80/443 stay bound.
  reconcile_caddy_service() {
    local caddy_config_changed=0
    if stage_caddy_config_if_changed; then
      caddy_config_changed=1
    fi

    local old_caddy_id new_caddy_id caddy_env_changed=0 caddy_dockerfile_changed="${CADDY_DOCKERFILE_CHANGED:-0}" caddy_service_config_changed="${CADDY_SERVICE_CONFIG_CHANGED:-0}"
    old_caddy_id="$(docker compose -f "$COMPOSE_FILE" ps -q caddy | head -1 || true)"

    if caddy_env_fingerprint_changed "$old_caddy_id"; then
      caddy_env_changed=1
    fi

    if [ -z "$old_caddy_id" ] || [ "$caddy_dockerfile_changed" -eq 1 ] || [ "$caddy_env_changed" -eq 1 ] || [ "$caddy_service_config_changed" -eq 1 ]; then
      echo "Reconciling Caddy service..."
      docker compose -f "$COMPOSE_FILE" up -d --no-deps caddy

      new_caddy_id="$(docker compose -f "$COMPOSE_FILE" ps -q caddy | head -1 || true)"
      if [ -z "$new_caddy_id" ]; then
        echo "ERROR: caddy container not found after docker compose up"
        return 1
      fi

      commit_caddy_env_fingerprint
      commit_caddy_service_config_fingerprint

      if [ -z "$old_caddy_id" ]; then
        echo "Caddy started successfully."
        return 0
      fi

      if [ "$old_caddy_id" != "$new_caddy_id" ]; then
        echo "Caddy container recreated to pick up image/env changes."
        return 0
      fi

      echo "Caddy service reconciled without container replacement."
      return 0
    fi

    if [ "$caddy_config_changed" -eq 1 ]; then
      echo "Caddyfile changed without container recreate — reloading caddy in place..."
      if ! docker exec "$old_caddy_id" caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile; then
        echo "In-place reload failed — forcing container recreate."
        docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate caddy
      fi
    else
      echo "Caddy inputs unchanged — leaving caddy running."
    fi
  }

  resolve_caddy_service_ips() {
    local caddy_id="$1" service="$2"
    docker exec "$caddy_id" sh -c '
      service="$1"
      if command -v getent >/dev/null 2>&1; then
        getent hosts "$service" | while read -r ip _; do
          case "$ip" in
            *:*) ;;
            *) echo "$ip" ;;
          esac
        done
      elif command -v nslookup >/dev/null 2>&1; then
        nslookup "$service" | while read -r first second third _; do
          case "$first" in
            Address:)
              ip="$second"
              ;;
            Address)
              case "$second" in
                *:) ip="$third" ;;
                *) continue ;;
              esac
              ;;
            *)
              continue
              ;;
          esac
          case "$ip" in
            ""|*:*) ;;
            *) echo "$ip" ;;
          esac
        done
      else
        exit 127
      fi
    ' sh "$service" | sort -u
  }

  # wait_caddy_upstream_discovery SERVICE CONTAINER_ID - Docker health means the
  # container is ready, but Caddy still needs to be able to reach it from the
  # proxy container's network namespace. Keep the old app/frontend container
  # serving until the same service-name DNS path Caddy uses includes the new
  # upstream and Caddy can probe it directly.
  wait_caddy_upstream_discovery() {
    local service="$1" new_container="$2" port
    case "$service" in
      api) port="8080" ;;
      frontend) port="3000" ;;
      *) return 0 ;;
    esac

    local caddy_id
    caddy_id="$(docker compose -f "$COMPOSE_FILE" ps -q caddy | head -1 || true)"
    if [ -z "$caddy_id" ]; then
      echo "ERROR: caddy container not found; refusing to drain old $service containers"
      return 1
    fi

    local new_ip
    new_ip="$(docker inspect --format '{{range .NetworkSettings.Networks}}{{if .IPAddress}}{{println .IPAddress}}{{end}}{{end}}' "$new_container" | head -1)"
    if [ -z "$new_ip" ]; then
      echo "ERROR: could not determine IP for new $service container ${new_container:0:12}"
      return 1
    fi

    local attempts="${CADDY_UPSTREAM_DISCOVERY_ATTEMPTS:-10}"
    local interval="${CADDY_UPSTREAM_DISCOVERY_INTERVAL_SECONDS:-1}"
    local dynamic_refresh="${CADDY_DYNAMIC_REFRESH_SECONDS:-2}"
    local url="http://$new_ip:$port/healthz"
    local dns_seen=0

    echo "Waiting for Caddy to reach healthy $service upstream at $new_ip:$port..."
    for i in $(seq 1 "$attempts"); do
      local service_ips
      service_ips="$(resolve_caddy_service_ips "$caddy_id" "$service" || true)"
      if printf '%s\n' "$service_ips" | grep -Fxq "$new_ip"; then
        if [ "$dns_seen" -eq 0 ]; then
          echo "Caddy service DNS resolves $service to new upstream $new_ip; waiting ${dynamic_refresh}s for dynamic upstream refresh..."
          sleep "$dynamic_refresh"
          dns_seen=1
        fi
        if docker exec "$caddy_id" sh -c 'if command -v wget >/dev/null 2>&1; then wget -qO- -T 2 "$1" >/dev/null; elif command -v curl >/dev/null 2>&1; then curl -fsS --max-time 2 "$1" >/dev/null; else exit 127; fi' sh "$url"; then
          echo "Caddy discovered and reached new $service upstream on attempt $i."
          return 0
        fi
      fi
      if [ "$i" -lt "$attempts" ]; then
        sleep "$interval"
      fi
    done

    echo "ERROR: Caddy could not reach new $service upstream at $new_ip:$port after $attempts attempt(s)"
    return 1
  }

  preview_rpc_auth_preflight() {
    local cid="$1"
    # The app-side probe signs with the candidate's secret and validates against
    # every active preview worker — the only place a worker with a divergent
    # secret (stale bundle / incomplete rotation) is caught. Fail the deploy on
    # any unverified *active* worker rather than silently tolerating it; set
    # PREVIEW_RPC_AUTH_STRICT=0 to fall back to best-effort in an emergency.
    local strict_flag="--fail-on-skipped"
    if [ "${PREVIEW_RPC_AUTH_STRICT:-1}" != "1" ]; then
      strict_flag=""
      echo "PREVIEW_RPC_AUTH_STRICT=0: preview RPC auth-check will not fail on unverified active workers."
    fi
    echo "Running preview RPC auth compatibility check from candidate api container ${cid:0:12}..."
    # shellcheck disable=SC2086 # $strict_flag is an optional single flag or empty
    docker exec "$cid" /docker-entrypoint.sh /bin/worker-deployctl preview-auth-check --json $strict_flag
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

    if ! wait_container_healthy "$new_container" 180 "$service"; then
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
      if [ "$service" = "api" ]; then
        preview_rpc_auth_preflight "$new_container"
      fi
      wait_caddy_upstream_discovery "$service" "$new_container"
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

  resolve_worker_drain_timeout_seconds() {
    local timeout="${WORKER_DEPLOY_DRAIN_TIMEOUT_SECONDS:-}"
    if [ -z "$timeout" ]; then
      case "${WORKER_DRAIN_TIMEOUT:-}" in
        ''|*[!0-9]*) timeout="14400" ;;
        *) timeout="$WORKER_DRAIN_TIMEOUT" ;;
      esac
    fi
    echo "$timeout"
  }

  # drain_worker_service SERVICE — legacy blocking worker drain helper. Kept
  # for manual recovery paths; routine worker deploys use blue/green
  # generations below so the deploy completes after the new generation is
  # healthy while old preview owners drain in the background.
  drain_worker_service() {
    local service="$1"
    local timeout
    timeout="$(resolve_worker_drain_timeout_seconds)"
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

  drain_worker_containers_blocking() {
    local containers="${1:-}"
    local timeout waited cid running_count
    timeout="$(resolve_worker_drain_timeout_seconds)"
    waited=0

    if [ -z "$containers" ]; then
      echo "ERROR: no worker containers available to drain." >&2
      return 1
    fi

    echo "Requesting blocking drain for existing worker containers (timeout ${timeout}s)..."
    for cid in $containers; do
      if docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | grep -q true; then
        docker kill --signal=TERM "$cid" >/dev/null
      fi
    done

    while true; do
      running_count=0
      for cid in $containers; do
        if docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | grep -q true; then
          running_count=$((running_count + 1))
        fi
      done
      if [ "$running_count" -eq 0 ]; then
        echo "Existing worker containers drained successfully."
        return 0
      fi
      if [ "$waited" -ge "$timeout" ]; then
        echo "ERROR: worker container drain timed out after ${timeout}s (${running_count} still running)" >&2
        return 1
      fi
      sleep 5
      waited=$((waited + 5))
    done
  }

  read_worker_env_value() {
    local key="$1"
    awk -F= -v key="$key" '$1 == key {sub(/^[^=]*=/, ""); print; exit}' /opt/143/.env
  }

  load_worker_endpoint_check_env() {
    DB_HOST="${DB_HOST:-$(read_worker_env_value DB_HOST)}"
    DB_PASSWORD="${DB_PASSWORD:-$(read_worker_env_value DB_PASSWORD)}"
    export DB_HOST DB_PASSWORD

    if [ -z "${DB_HOST:-}" ] || [ -z "${DB_PASSWORD:-}" ]; then
      echo "ERROR: DB_HOST and DB_PASSWORD are required to verify preview runtime endpoint reuse safety." >&2
      echo "Run make deploy-worker-preflight to verify worker blue/green readiness before a routine deploy." >&2
      return 1
    fi
  }

  sanitize_compose_project() {
    local raw="$1" sanitized
    sanitized="$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9_-]+/-/g; s/^-+//; s/-+$//')"
    if [ -z "$sanitized" ]; then
      sanitized="worker"
    fi
    printf '%.63s' "$sanitized"
  }

  list_running_worker_containers() {
    docker ps --filter "label=com.docker.compose.service=worker" --format '{{.ID}}'
  }

  worker_container_node_id() {
    local cid="$1"
    docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$cid" 2>/dev/null \
      | awk -F= '$1=="NODE_ID"{print $2; exit}'
  }

  first_running_worker_node_id() {
    local containers="${1:-}" cid node_id
    for cid in $containers; do
      if ! docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | grep -q true; then
        continue
      fi
      node_id="$(worker_container_node_id "$cid")"
      if [ -n "$node_id" ]; then
        echo "$node_id"
        return 0
      fi
    done
    return 1
  }

  run_worker_deployctl() {
    docker compose -f "$COMPOSE_FILE" run --rm -T --no-deps \
      -e "IMAGE_TAG=${IMAGE_TAG:-}" \
      "$HEALTH_SERVICE" /bin/worker-deployctl "$@" < /dev/null
  }

  wait_worker_db_heartbeat() {
    local node_id="$1" timeout="${2:-120}" deadline
    deadline=$((SECONDS + timeout))
    echo "Waiting for worker node $node_id to register a fresh DB heartbeat (timeout ${timeout}s)..."
    while [ "$SECONDS" -lt "$deadline" ]; do
      if run_worker_deployctl status --node-id "$node_id" --require-fresh --json; then
        return 0
      fi
      sleep 2
    done
    echo "ERROR: worker node $node_id did not publish a fresh DB heartbeat before the rollout deadline." >&2
    return 1
  }

  worker_port_in_use() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
      if ss -ltnH "sport = :$port" 2>/dev/null | grep -q .; then
        return 0
      fi
    fi
    docker ps --format '{{.Ports}}' | grep -Eq "(^|, |:)${port}->8080/tcp"
  }

  worker_runtime_endpoint_in_use() {
    local worker_private_ip="$1" port="$2" endpoint query count
    endpoint="http://${worker_private_ip}:${port}"

    if [ -z "${DB_HOST:-}" ] || [ -z "${DB_PASSWORD:-}" ]; then
      echo "ERROR: DB_HOST and DB_PASSWORD are required to verify preview runtime endpoint reuse safety." >&2
      return 0
    fi

    query="WITH endpoint_blockers AS (
  SELECT 1 FROM preview_runtimes WHERE endpoint_url = :'endpoint' AND status IN ('starting', 'ready', 'draining') AND lease_expires_at > now()
  UNION ALL
  SELECT 1 FROM nodes WHERE metadata->>'preview_internal_base_url' = :'endpoint' AND status IN ('active', 'draining')
)
SELECT COUNT(*) FROM endpoint_blockers;"
    if ! count="$(printf '%s\n' "$query" | docker run -i --rm --network host -e PGPASSWORD="$DB_PASSWORD" postgres:16-alpine \
      psql -h "$DB_HOST" -U onefortythree -d onefortythree \
      -v ON_ERROR_STOP=1 \
      -v endpoint="$endpoint" \
      -tA)"; then
      echo "ERROR: could not verify preview runtime/node endpoint reuse safety for ${endpoint}; refusing to reuse it." >&2
      return 0
    fi

    count="$(printf '%s' "$count" | tr -d '[:space:]')"
    [ "${count:-0}" -gt 0 ]
  }

  worker_blue_green_extra_ports_configured() {
    local start="${WORKER_BLUE_GREEN_PORT_START:-8080}"
    local end="${WORKER_BLUE_GREEN_PORT_END:-$start}"

    [ "$start" != "8080" ] || [ "$end" != "$start" ]
  }

  worker_host_capacity_preflight() {
    local min_mem="${WORKER_BLUE_GREEN_MIN_FREE_MEMORY_MB:-512}"
    local min_cpu="${WORKER_BLUE_GREEN_MIN_IDLE_CPU_MILLIS:-0}"
    local attempts="${WORKER_BLUE_GREEN_PREFLIGHT_ATTEMPTS:-3}"
    local retry_delay="${WORKER_BLUE_GREEN_PREFLIGHT_RETRY_DELAY_SECONDS:-2}"
    local free_mem idle_cpu idle1 total1 idle2 total2 delta cpu_count
    local attempt

    if ! [[ "$attempts" =~ ^[0-9]+$ ]] || [ "$attempts" -lt 1 ]; then
      attempts=1
    fi
    if ! [[ "$retry_delay" =~ ^[0-9]+$ ]]; then
      retry_delay=2
    fi
    cpu_count="$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
    if ! [[ "$cpu_count" =~ ^[0-9]+$ ]] || [ "$cpu_count" -lt 1 ]; then
      cpu_count=1
    fi

    for ((attempt = 1; attempt <= attempts; attempt++)); do
      free_mem="$(awk '/MemAvailable:/ {printf "%d", $2 / 1024}' /proc/meminfo 2>/dev/null || echo 0)"
      read -r idle1 total1 < <(awk '/^cpu / {idle=$5; total=0; for (i=2; i<=NF; i++) total+=$i; print idle, total; exit}' /proc/stat)
      sleep 1
      read -r idle2 total2 < <(awk '/^cpu / {idle=$5; total=0; for (i=2; i<=NF; i++) total+=$i; print idle, total; exit}' /proc/stat)
      idle1="${idle1:-0}"
      total1="${total1:-0}"
      idle2="${idle2:-0}"
      total2="${total2:-0}"
      delta=$((total2 - total1))
      if [ "$delta" -le 0 ]; then
        idle_cpu=0
      else
        idle_cpu=$((((idle2 - idle1) * 1000 * cpu_count) / delta))
      fi

      if [ "$free_mem" -ge "$min_mem" ] && [ "$idle_cpu" -ge "$min_cpu" ]; then
        WORKER_BLUE_GREEN_FREE_MEMORY_MB="$free_mem"
        WORKER_BLUE_GREEN_IDLE_CPU_MILLIS="$idle_cpu"
        return 0
      fi

      if [ "$attempt" -lt "$attempts" ]; then
        echo "Worker capacity preflight attempt ${attempt}/${attempts} below threshold: free=${free_mem}MB min=${min_mem}MB idle=${idle_cpu}m min=${min_cpu}m; retrying..." >&2
        sleep "$retry_delay"
      fi
    done

    if [ "$free_mem" -lt "$min_mem" ]; then
      echo "ERROR: insufficient free memory for worker blue/green overlap: free=${free_mem}MB min=${min_mem}MB" >&2
      return 1
    fi
    if [ "$idle_cpu" -lt "$min_cpu" ]; then
      echo "ERROR: insufficient idle CPU for worker blue/green overlap: idle=${idle_cpu}m min=${min_cpu}m" >&2
      return 1
    fi
  }

  fingerprint_files() {
    local existing=()
    local path
    for path in "$@"; do
      if [ -e "$path" ]; then
        existing+=("$path")
      fi
    done
    if [ "${#existing[@]}" -eq 0 ]; then
      echo "none"
      return 0
    fi
    sha256sum "${existing[@]}" | sha256sum | awk '{print $1}'
  }

  compose_service_fingerprint() {
    local compose_file="$1"
    shift
    local read_file="$compose_file"
    if [ -e "${compose_file}.new" ]; then
      read_file="${compose_file}.new"
    fi
    if [ ! -e "$read_file" ]; then
      printf 'missing:%s\n' "$compose_file" | sha256sum | awk '{print $1}'
      return 0
    fi
    {
      local svc
      for svc in "$@"; do
        printf -- '--- %s:%s ---\n' "$compose_file" "$svc"
        awk -v svc="$svc" '
          /^  [A-Za-z0-9_.-]+:/ {
            current=$1
            sub(/:$/, "", current)
            in_service=(current == svc)
          }
          in_service { print }
        ' "$read_file"
      done
    } | sha256sum | awk '{print $1}'
  }

  compose_service_active_fingerprint() {
    local compose_file="$1"
    shift
    if [ ! -e "$compose_file" ]; then
      printf 'missing:%s\n' "$compose_file" | sha256sum | awk '{print $1}'
      return 0
    fi
    {
      local svc
      for svc in "$@"; do
        printf -- '--- %s:%s ---\n' "$compose_file" "$svc"
        awk -v svc="$svc" '
          /^  [A-Za-z0-9_.-]+:/ {
            current=$1
            sub(/:$/, "", current)
            in_service=(current == svc)
          }
          in_service { print }
        ' "$compose_file"
      done
    } | sha256sum | awk '{print $1}'
  }

  worker_process_config_fingerprint() {
    compose_service_fingerprint /opt/143/docker-compose.worker.yml worker
  }

  # These fingerprints must match the staged fingerprint gate's computation
  # exactly, so both read the shared *_FINGERPRINT_FILES / *_COMPOSE_SERVICES
  # lists (passed via SSH env here, baked into the detached rollover script).
  # The lists are space-separated, hence the intentional unquoted expansion.
  worker_support_service_fingerprint() {
    {
      # shellcheck disable=SC2086
      compose_service_fingerprint /opt/143/docker-compose.worker.yml $WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES
      # shellcheck disable=SC2086
      fingerprint_files $WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES
    } | sha256sum | awk '{print $1}'
  }

  worker_host_runtime_fingerprint() {
    # shellcheck disable=SC2086
    fingerprint_files $WORKER_HOST_RUNTIME_FINGERPRINT_FILES
  }

  worker_docker_daemon_fingerprint() {
    # shellcheck disable=SC2086
    fingerprint_files $WORKER_DOCKER_DAEMON_FINGERPRINT_FILES
  }

  ensure_routine_worker_fingerprints_compatible() {
    local mode="${DEPLOY_MODE:-routine}"
    local worker_process_file="/opt/143/.worker-process.fingerprint"
    # v2 keeps the semantic support-service hash separate from the legacy
    # broad hash that included the entire worker compose file.
    local support_file="/opt/143/.worker-support-services.v2.fingerprint"
    local host_runtime_file="/opt/143/.worker-host-runtime.fingerprint"
    local docker_daemon_file="/opt/143/.worker-docker-daemon.fingerprint"

    WORKER_PROCESS_FINGERPRINT="$(worker_process_config_fingerprint)"
    WORKER_SUPPORT_SERVICE_FINGERPRINT="$(worker_support_service_fingerprint)"
    WORKER_HOST_RUNTIME_FINGERPRINT="$(worker_host_runtime_fingerprint)"
    WORKER_DOCKER_DAEMON_FINGERPRINT="$(worker_docker_daemon_fingerprint)"

    WORKER_PROCESS_EXPECTED_FINGERPRINT="$WORKER_PROCESS_FINGERPRINT"
    WORKER_SUPPORT_SERVICE_EXPECTED_FINGERPRINT="$WORKER_SUPPORT_SERVICE_FINGERPRINT"
    WORKER_HOST_RUNTIME_EXPECTED_FINGERPRINT="$WORKER_HOST_RUNTIME_FINGERPRINT"
    WORKER_DOCKER_DAEMON_EXPECTED_FINGERPRINT="$WORKER_DOCKER_DAEMON_FINGERPRINT"

    if [ -f "$worker_process_file" ]; then
      WORKER_PROCESS_EXPECTED_FINGERPRINT="$(cat "$worker_process_file")"
    fi
    if [ -f "$support_file" ]; then
      WORKER_SUPPORT_SERVICE_EXPECTED_FINGERPRINT="$(cat "$support_file")"
    fi
    if [ -f "$host_runtime_file" ]; then
      WORKER_HOST_RUNTIME_EXPECTED_FINGERPRINT="$(cat "$host_runtime_file")"
    fi
    if [ -f "$docker_daemon_file" ]; then
      WORKER_DOCKER_DAEMON_EXPECTED_FINGERPRINT="$(cat "$docker_daemon_file")"
    fi

    if [ "$mode" = "routine" ] && [ "$WORKER_SUPPORT_SERVICE_EXPECTED_FINGERPRINT" != "$WORKER_SUPPORT_SERVICE_FINGERPRINT" ]; then
      echo "ERROR: worker support-service config changed during routine deploy; this can recreate sandbox-dns/chrome while sessions are active." >&2
      echo "  Routine deploys verify support services but do not activate support-service changes." >&2
      echo "  Run DEPLOY_MODE=maintenance after reviewing active runtime impact." >&2
      echo "current=$WORKER_SUPPORT_SERVICE_EXPECTED_FINGERPRINT candidate=$WORKER_SUPPORT_SERVICE_FINGERPRINT" >&2
      return 1
    fi
    if [ "$mode" = "routine" ] && [ "$WORKER_HOST_RUNTIME_EXPECTED_FINGERPRINT" != "$WORKER_HOST_RUNTIME_FINGERPRINT" ]; then
      echo "ERROR: worker host-runtime config changed during routine deploy; run DEPLOY_MODE=maintenance after reviewing active runtime impact." >&2
      echo "current=$WORKER_HOST_RUNTIME_EXPECTED_FINGERPRINT candidate=$WORKER_HOST_RUNTIME_FINGERPRINT" >&2
      return 1
    fi
    if [ "$mode" = "routine" ] && [ "$WORKER_DOCKER_DAEMON_EXPECTED_FINGERPRINT" != "$WORKER_DOCKER_DAEMON_FINGERPRINT" ]; then
      echo "ERROR: worker docker-daemon config changed during routine deploy; run DEPLOY_MODE=maintenance after reviewing active runtime impact." >&2
      echo "current=$WORKER_DOCKER_DAEMON_EXPECTED_FINGERPRINT candidate=$WORKER_DOCKER_DAEMON_FINGERPRINT" >&2
      return 1
    fi
  }

  worker_expected_schema_version() {
    local version
    version="$(find /opt/143/migrations -maxdepth 1 -name '*.up.sql' -printf '%f\n' 2>/dev/null \
      | sed -E 's/^([0-9]+).*/\1/' \
      | sort -n \
      | tail -1)"
    echo "${version:-0}"
  }

  protect_active_executor_images() {
    local node_id="$1" deploy_id="$2"
    # worker-deployctl retain-images records active executor image refs before pruning.
    run_worker_deployctl retain-images \
      --node-id "$node_id" \
      --deploy-id "$deploy_id" \
      --reason "active executor image retention before worker deploy prune" \
      --retain-for "${WORKER_ACTIVE_EXECUTOR_IMAGE_RETAIN_FOR:-24h}" \
      --json || true
  }

  find_free_worker_port() {
    local worker_private_ip="$1"
    local endpoint_reuse_mode="${2:-strict}"
    local start="${WORKER_BLUE_GREEN_PORT_START:-8080}"
    local end="${WORKER_BLUE_GREEN_PORT_END:-$start}"
    local port

    if [ -z "$worker_private_ip" ]; then
      echo "ERROR: worker private IP is required to verify preview runtime endpoint reuse safety." >&2
      return 1
    fi
    if [ "$endpoint_reuse_mode" != "strict" ] && [ "$endpoint_reuse_mode" != "after-blocking-drain" ]; then
      echo "ERROR: invalid worker endpoint reuse mode: $endpoint_reuse_mode" >&2
      return 1
    fi
    if [[ "$start" == *[!0-9]* ]] || [[ "$end" == *[!0-9]* ]]; then
      echo "ERROR: WORKER_BLUE_GREEN_PORT_START and WORKER_BLUE_GREEN_PORT_END must be numeric." >&2
      return 1
    fi
    if [ "$start" -gt "$end" ]; then
      echo "ERROR: WORKER_BLUE_GREEN_PORT_START ($start) must be <= WORKER_BLUE_GREEN_PORT_END ($end)." >&2
      return 1
    fi
    if [ "$start" != "$end" ]; then
      echo "Worker blue/green port range ${start}-${end} is enabled; app-to-worker network must allow every configured worker blue/green port." >&2
    fi

    for port in $(seq "$start" "$end"); do
      if worker_port_in_use "$port"; then
        continue
      fi
      if [ "$endpoint_reuse_mode" = "after-blocking-drain" ]; then
        echo "Worker host port ${port} is free after blocking drain; reusing the drained endpoint." >&2
        echo "$port"
        return 0
      fi
      if ! worker_runtime_endpoint_in_use "$worker_private_ip" "$port"; then
        echo "$port"
        return 0
      fi
    done
    echo "ERROR: no reusable worker host port in ${start}-${end}; Docker or active preview_runtimes still own every endpoint" >&2
    return 1
  }

  start_worker_generation() {
    local node_id="$1" host_port="$2" base_url="$3" project="$4"
    local cid

    echo "Starting worker generation node_id=$node_id port=$host_port project=$project..."
    NODE_ID="$node_id" \
      WORKER_HOST_PORT="$host_port" \
      PREVIEW_INTERNAL_BASE_URL="$base_url" \
      IMAGE_TAG="$IMAGE_TAG" \
      docker compose -p "$project" -f "$COMPOSE_FILE" up -d --no-deps "$HEALTH_SERVICE"

    cid="$(NODE_ID="$node_id" WORKER_HOST_PORT="$host_port" PREVIEW_INTERNAL_BASE_URL="$base_url" IMAGE_TAG="$IMAGE_TAG" docker compose -p "$project" -f "$COMPOSE_FILE" ps -q "$HEALTH_SERVICE" | head -1)"
    if [ -z "$cid" ]; then
      echo "ERROR: could not find new worker generation container"
      return 1
    fi

    if ! wait_container_healthy "$cid" 180 "$HEALTH_SERVICE"; then
      echo "Rolling back failed worker generation ${cid:0:12}..."
      docker stop "$cid" >/dev/null 2>&1 || true
      docker rm "$cid" >/dev/null 2>&1 || true
      return 1
    fi
    STARTED_WORKER_CID="$cid"
  }

  drain_old_worker_containers() {
    local new_cid="$1"
    local old_containers="${2:-}"
    local deploy_id="${3:-worker-rollout-$(date -u +%Y%m%d%H%M%S)}"
    if [ -z "$old_containers" ]; then
      echo "No old worker containers to drain."
      return 0
    fi

    mkdir -p /var/log/143
    for cid in $old_containers; do
      local node_id
      if [ "$cid" = "$new_cid" ]; then
        continue
      fi
      if ! docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | grep -q true; then
        continue
      fi
      node_id="$(worker_container_node_id "$cid")"
      if [ -z "$node_id" ]; then
        echo "WARNING: old worker container ${cid:0:12} has no NODE_ID; leaving it untouched for operator review." >&2
        continue
      fi
      echo "Marking old worker node $node_id (${cid:0:12}) as draining; retirement will wait for owned runtimes."
      run_worker_deployctl mark-draining \
        --node-id "$node_id" \
        --intent planned_rollout \
        --deploy-id "$deploy_id" \
        --reason "${DEPLOY_REASON:-routine worker rollout}" \
        --requested-by "${DEPLOY_REQUESTED_BY:-deploy-script}" \
        --build-sha "${IMAGE_TAG:-}" \
        --json
      local monitor_log monitor_lock
      monitor_log="/var/log/143/drain-worker-${cid:0:12}.log"
      monitor_lock="/var/log/143/drain-worker-${cid:0:12}.lock"
      nohup bash -c '
        set -euo pipefail
        monitor_lock="$1"
        node_id="$2"
        cid="$3"
        compose_file="$4"
        deploy_id="$5"
        build_sha="$6"
        requested_by="$7"
        reason="$8"
        health_service="$9"
        exec 9>"$monitor_lock"
        if ! flock -xn 9; then
          echo "drain monitor already running for old worker container ${cid:0:12}; skipping duplicate."
          exit 0
        fi
        run_ctl() {
          docker compose -f "$compose_file" run --rm -T --no-deps \
            -e "IMAGE_TAG=$build_sha" \
            "$health_service" /bin/worker-deployctl "$@" < /dev/null
        }
        while true; do
          if ! docker inspect --format "{{.State.Running}}" "$cid" 2>/dev/null | grep -q true; then
            echo "old worker container ${cid:0:12} is no longer running; drain monitor exiting."
            exit 0
          fi
          run_ctl expire-budget \
            --node-id "$node_id" \
            --deploy-id "$deploy_id" \
            --reason "$reason" \
            --requested-by "$requested_by" \
            --build-sha "$build_sha" \
            --json || true
          if run_ctl retire-ready --node-id "$node_id" --json; then
            echo "Worker node $node_id is retire-ready; stopping container ${cid:0:12}."
            docker stop -t 60 "$cid"
            exit 0
          fi
          sleep 30
        done
      ' _ "$monitor_lock" "$node_id" "$cid" "$COMPOSE_FILE" "$deploy_id" "${IMAGE_TAG:-}" "${DEPLOY_REQUESTED_BY:-deploy-script}" "${DEPLOY_REASON:-routine worker rollout}" "$HEALTH_SERVICE" >"$monitor_log" 2>&1 &
    done
  }

  deploy_worker_blue_green() {
    local old_containers base_node_id worker_private_ip generation node_id host_port base_url project new_cid deploy_id preflight_node_id deploy_mode

    old_containers="$(list_running_worker_containers || true)"
    preflight_node_id="$(first_running_worker_node_id "$old_containers" || true)"
    base_node_id="${WORKER_BASE_NODE_ID:-$(read_worker_env_value NODE_ID)}"
    worker_private_ip="$(read_worker_env_value WORKER_PRIVATE_IP)"
    deploy_mode="${DEPLOY_MODE:-routine}"
    if [ -z "$base_node_id" ] || [ -z "$worker_private_ip" ]; then
      echo "ERROR: NODE_ID and WORKER_PRIVATE_IP must be present in /opt/143/.env for worker blue/green deploy." >&2
      return 1
    fi
    ensure_routine_worker_fingerprints_compatible

    if [ "$deploy_mode" = "maintenance" ]; then
      if [ -n "$old_containers" ]; then
        drain_worker_containers_blocking "$old_containers"
      else
        echo "No existing worker containers found; maintenance deploy will start a fresh generation."
      fi
    else
      load_worker_endpoint_check_env
      worker_host_capacity_preflight
    fi

    generation="$(date -u +%Y%m%d%H%M%S)-${IMAGE_TAG:0:12}"
    node_id="${base_node_id}-g${generation}"
    deploy_id="worker-${generation}"
    if [ "$deploy_mode" = "maintenance" ]; then
      if ! host_port="$(find_free_worker_port "$worker_private_ip" "after-blocking-drain")"; then
        echo "ERROR: no reusable worker host port after maintenance drain." >&2
        return 1
      fi
    else
      if ! host_port="$(find_free_worker_port "$worker_private_ip")"; then
        echo "ERROR: no free worker generation port; routine blue/green deploy refuses blocking drain fallback." >&2
        echo "Configure WORKER_BLUE_GREEN_PORT_START/END or run an explicit maintenance deploy." >&2
        return 1
      fi
    fi
    base_url="http://${worker_private_ip}:${host_port}"
    project="$(sanitize_compose_project "143-${node_id}")"

    if [ -n "$preflight_node_id" ]; then
      run_worker_deployctl impact --node-id "$preflight_node_id" --json
      run_worker_deployctl preflight \
        --mode "${DEPLOY_MODE:-routine}" \
        --node-id "$preflight_node_id" \
        --candidate-port "$host_port" \
        --build-sha "${IMAGE_TAG:-}" \
        --expected-schema-version "$(worker_expected_schema_version)" \
        --worker-process-fingerprint "${WORKER_PROCESS_FINGERPRINT:-}" \
        --expected-worker-process-fingerprint "${WORKER_PROCESS_EXPECTED_FINGERPRINT:-${WORKER_PROCESS_FINGERPRINT:-}}" \
        --support-services-fingerprint "${WORKER_SUPPORT_SERVICE_FINGERPRINT:-}" \
        --expected-support-services-fingerprint "${WORKER_SUPPORT_SERVICE_EXPECTED_FINGERPRINT:-${WORKER_SUPPORT_SERVICE_FINGERPRINT:-}}" \
        --host-runtime-fingerprint "${WORKER_HOST_RUNTIME_FINGERPRINT:-}" \
        --expected-host-runtime-fingerprint "${WORKER_HOST_RUNTIME_EXPECTED_FINGERPRINT:-${WORKER_HOST_RUNTIME_FINGERPRINT:-}}" \
        --docker-daemon-fingerprint "${WORKER_DOCKER_DAEMON_FINGERPRINT:-}" \
        --expected-docker-daemon-fingerprint "${WORKER_DOCKER_DAEMON_EXPECTED_FINGERPRINT:-${WORKER_DOCKER_DAEMON_FINGERPRINT:-}}" \
        --free-memory-mb "${WORKER_BLUE_GREEN_FREE_MEMORY_MB:-0}" \
        --min-free-memory-mb "${WORKER_BLUE_GREEN_MIN_FREE_MEMORY_MB:-512}" \
        --idle-cpu-millis "${WORKER_BLUE_GREEN_IDLE_CPU_MILLIS:-0}" \
        --min-idle-cpu-millis "${WORKER_BLUE_GREEN_MIN_IDLE_CPU_MILLIS:-0}" \
        --include-impact \
        --json
    else
      echo "No existing worker node id found for preflight; continuing first-generation deploy after local port checks."
    fi

    STARTED_WORKER_CID=""
    start_worker_generation "$node_id" "$host_port" "$base_url" "$project"
    new_cid="$STARTED_WORKER_CID"
    if ! wait_worker_db_heartbeat "$node_id" "${WORKER_BLUE_GREEN_DB_HEARTBEAT_TIMEOUT_SECONDS:-120}"; then
      echo "Rolling back worker generation ${new_cid:0:12} after DB heartbeat readiness failure..."
      docker stop "$new_cid" >/dev/null 2>&1 || true
      docker rm "$new_cid" >/dev/null 2>&1 || true
      return 1
    fi
    if ! run_worker_deployctl preview-auth-check --node-id "$node_id" --json; then
      echo "Rolling back worker generation ${new_cid:0:12} after preview RPC auth compatibility failure..."
      docker stop "$new_cid" >/dev/null 2>&1 || true
      docker rm "$new_cid" >/dev/null 2>&1 || true
      return 1
    fi
    if [ -n "$preflight_node_id" ]; then
      protect_active_executor_images "$preflight_node_id" "$deploy_id"
    fi
    drain_old_worker_containers "$new_cid" "$old_containers" "$deploy_id"
    printf '%s\n' "${WORKER_PROCESS_FINGERPRINT:-}" > /opt/143/.worker-process.fingerprint
    printf '%s\n' "${WORKER_SUPPORT_SERVICE_FINGERPRINT:-}" > /opt/143/.worker-support-services.v2.fingerprint
    printf '%s\n' "${WORKER_HOST_RUNTIME_FINGERPRINT:-}" > /opt/143/.worker-host-runtime.fingerprint
    printf '%s\n' "${WORKER_DOCKER_DAEMON_FINGERPRINT:-}" > /opt/143/.worker-docker-daemon.fingerprint
    echo "Worker generation ${new_cid:0:12} is healthy; old workers are admission-draining until owned runtimes retire."
  }

  dump_diagnostics() {
    local cid="${1:-}" service="${2:-$HEALTH_SERVICE}"
    echo "--- Last 50 lines of $service logs ---"
    docker compose -f "$COMPOSE_FILE" logs --tail=50 "$service" 2>&1 || true
    if [ -n "$cid" ]; then
      echo "--- Last 50 lines of container ${cid:0:12} logs ---"
      docker logs --tail=50 "$cid" 2>&1 || true
      echo "--- Docker health check log ---"
      docker inspect --format '{{if .State.Health}}{{range .State.Health.Log}}--- {{.Start}} ---
{{.Output}}
{{end}}{{else}}(no health check configured){{end}}' "$cid" 2>&1 || true
    fi
  }

  # prune_docker_deploy_artifacts ROLE — reclaim Docker cache after a
  # successful rollout. Runs only after the new service is healthy so the
  # freshly pulled image is protected by a running container. Detached worker
  # deploys embed and call this helper inside the detached rollover script for
  # the same reason.
  prune_docker_deploy_artifacts() {
    local role="${1:-}"
    if [ "${DEPLOY_DOCKER_PRUNE:-1}" = "0" ]; then
      echo "Docker deploy prune skipped (DEPLOY_DOCKER_PRUNE=0)."
      return 0
    fi
    case "$role" in
      app|worker)
        ;;
      *)
        echo "Docker deploy prune skipped for role=$role."
        return 0
        ;;
    esac

    local prune_until="${DOCKER_PRUNE_UNTIL:-24h}"
    echo "Pruning unused Docker artifacts older than $prune_until..."
    docker container prune -f --filter "until=$prune_until" || echo "WARNING: docker container prune failed; continuing."
    docker image prune -af --filter "until=$prune_until" || echo "WARNING: docker image prune failed; continuing."
    docker builder prune -af --filter "until=$prune_until" || echo "WARNING: docker builder prune failed; continuing."
    if [ "$role" = "worker" ] && [ -n "${IMAGE_TAG:-}" ]; then
      # worker-deployctl release-retained-images clears expired DB retention records.
      run_worker_deployctl release-retained-images --json || true
      local sandbox_image="ghcr.io/assembledhq/143-sandbox:$IMAGE_TAG"
      if ! docker image inspect "$sandbox_image" >/dev/null 2>&1; then
        echo "Re-pulling required sandbox image after prune: $sandbox_image"
        docker pull "$sandbox_image"
      fi
    fi
    if [ "$role" = "worker" ] && [ "${DEPLOY_DOCKER_VOLUME_PRUNE:-0}" = "1" ]; then
      docker volume prune -f || echo "WARNING: docker volume prune failed; continuing."
    fi
  }

  run_worker_session_deploy_guardrail() {
    if [ "$ROLE" != "worker" ]; then
      return 0
    fi
    echo "Checking active long-running sessions before worker deploy..."
    docker compose -f "$COMPOSE_FILE" run --rm -T --no-deps \
      -e "FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=${FORCE_DEPLOY_WITH_ACTIVE_SESSIONS:-}" \
      "$HEALTH_SERVICE" /bin/deploy-guardrail worker-sessions < /dev/null
  }

  # wait_container_healthy CONTAINER_ID TIMEOUT — poll until a specific container
  # passes its health check, or fail after TIMEOUT seconds.
  wait_container_healthy() {
    local cid="$1" timeout="${2:-120}" service="${3:-$HEALTH_SERVICE}"
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
        dump_diagnostics "$cid" "$service"
        return 1
      fi
    fi

    for i in $(seq 1 $((timeout / 2))); do
      local state
      state="$(docker inspect --format '{{.State.Status}}' "$cid" 2>/dev/null || echo "missing")"
      if [ "$state" = "exited" ] || [ "$state" = "dead" ] || [ "$state" = "missing" ]; then
        echo "ERROR: container entered terminal state: $state"
        dump_diagnostics "$cid" "$service"
        return 1
      fi

      HEALTH_STATUS="$(docker inspect --format '{{.State.Health.Status}}' "$cid")"
      if [ "$HEALTH_STATUS" = "healthy" ]; then
        echo "Health check passed."
        return 0
      fi

      if [ "$i" -eq $((timeout / 2)) ]; then
        echo "ERROR: Health check timed out after ${timeout}s (last status: $HEALTH_STATUS)"
        dump_diagnostics "$cid" "$service"
        return 1
      fi
      sleep 2
    done
  }

  wait_vector_healthy() {
    local cid="$1"
    local timeout="${VECTOR_HEALTH_TIMEOUT:-90}"
    local waited=0
    local state health

    echo "Waiting for Vector log collector health check (timeout ${timeout}s)..."
    while [ "$waited" -le "$timeout" ]; do
      state="$(docker inspect --format '{{.State.Status}}' "$cid" 2>/dev/null || echo "missing")"
      health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid" 2>/dev/null || echo "missing")"

      if [ "$state" = "running" ] && { [ "$health" = "healthy" ] || [ "$health" = "none" ]; }; then
        echo "Vector is healthy (state: $state, health: $health)."
        return 0
      fi

      if [ "$state" = "exited" ] || [ "$state" = "dead" ]; then
        echo "ERROR: Vector is not running (state: $state, health: $health)"
        docker compose -f "$COMPOSE_FILE" logs --tail=50 vector 2>&1 || true
        return 1
      fi

      if [ "$health" = "unhealthy" ]; then
        echo "ERROR: Vector is not healthy (state: $state, health: $health)"
        docker compose -f "$COMPOSE_FILE" logs --tail=50 vector 2>&1 || true
        docker inspect --format '{{if .State.Health}}{{range .State.Health.Log}}--- {{.Start}} ---
{{.Output}}
{{end}}{{end}}' "$cid" 2>&1 || true
        return 1
      fi

      if [ "$waited" -ge "$timeout" ]; then
        echo "ERROR: Vector is not healthy after ${timeout}s (state: $state, health: $health)"
        docker compose -f "$COMPOSE_FILE" logs --tail=50 vector 2>&1 || true
        docker inspect --format '{{if .State.Health}}{{range .State.Health.Log}}--- {{.Start}} ---
{{.Output}}
{{end}}{{end}}' "$cid" 2>&1 || true
        return 1
      fi

      sleep 2
      waited=$((waited + 2))
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
    if [ ! -f "$DAEMON_JSON" ] || ! grep -q "ignore-cgroups" "$DAEMON_JSON" || ! grep -Eq -- '--host-uds(=|[[:space:]]+)open' "$DAEMON_JSON"; then
      if [ "${DEPLOY_MODE:-routine}" = "routine" ]; then
        echo "ERROR: routine worker deploy would restart Docker to patch runsc; rerun with DEPLOY_MODE=maintenance after reviewing active runtimes." >&2
        exit 1
      fi
      echo "Patching runsc runtime with --ignore-cgroups --host-uds=open..."
      sudo runsc install -- --ignore-cgroups --host-uds=open
      sudo systemctl restart docker
      echo "Docker restarted with updated gVisor config."
    fi
  fi

  # --ignore-buildable: skip services whose image is built locally (sandbox-dns
  # has both build: and image: 143-sandbox-dns:local in docker-compose.worker.yml,
  # which pull would otherwise treat as a registry reference and fail on).
  if [ "$ROLE" = "app" ]; then
    CADDY_SERVICE_CONFIG_CHANGED=0
    if caddy_service_config_changed; then
      CADDY_SERVICE_CONFIG_CHANGED=1
    fi
    promote_staged_app_compose
  fi

  docker compose -f "$COMPOSE_FILE" pull --ignore-buildable

  # The sandbox image is referenced via SANDBOX_IMAGE env var, not as a compose
  # service, so `docker compose pull` doesn't fetch it. Pull it explicitly —
  # ContainerCreate doesn't auto-pull, so the worker would fail on first launch.
  if [ "$ROLE" = "worker" ]; then
    docker pull "ghcr.io/assembledhq/143-sandbox:$IMAGE_TAG"
    if [ "${DEPLOY_MODE:-routine}" = "routine" ]; then
      if ! docker image inspect 143-sandbox-dns:local >/dev/null 2>&1; then
        echo "sandbox-dns image missing; building local support image for routine worker deploy..."
        docker compose -f "$COMPOSE_FILE" build sandbox-dns
      else
        echo "Skipping sandbox-dns build for routine worker deploy; use DEPLOY_MODE=maintenance to activate support-service changes."
      fi
    else
      # Maintenance deploys intentionally rebuild locally-built support images
      # before recreating support services below.
      docker compose -f "$COMPOSE_FILE" build sandbox-dns
    fi
  elif [ "$ROLE" = "app" ]; then
    CADDY_DOCKERFILE_CHANGED=0
    if stage_caddy_dockerfile_if_changed; then
      CADDY_DOCKERFILE_CHANGED=1
      # Caddy is built locally (Dockerfile.caddy), so neither `docker compose
      # pull` nor an in-place `caddy reload` would pick up Dockerfile/base-image
      # changes. Build only when the Dockerfile changed; rebuilding on every
      # app deploy can make compose replace the single Cloudflare-facing origin.
      echo "Dockerfile.caddy changed — building custom Caddy image..."
      docker compose -f "$COMPOSE_FILE" build caddy
    else
      echo "Dockerfile.caddy unchanged — skipping Caddy image build."
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
    if [ "${DEPLOY_MODE:-routine}" = "routine" ]; then
      echo "Skipping supporting-service recreation for routine worker deploy; use DEPLOY_MODE=maintenance for host/runtime dependency changes."
    else
      echo "Updating supporting services for ${DEPLOY_MODE:-maintenance} worker deploy..."
      recreate_other_services "$HEALTH_SERVICE"
    fi
  fi

  # Rolling deploy for both api and frontend on the app role. Order matters:
  # api first so the new code and any new DB columns are live before the
  # frontend that references them starts serving. --no-recreate keeps old
  # containers as-is during the health-check window.
  if [ "$ROLE" = "app" ]; then
    rolling_deploy_service api
    rolling_deploy_service frontend

    reconcile_caddy_service

  elif [ "$ROLE" = "worker" ]; then
    run_worker_session_deploy_guardrail

    # Worker deploys use per-generation node IDs and host ports. The new
    # generation becomes active first; old generations are marked draining by
    # their own SIGTERM handlers and keep serving owned previews until they
    # stop naturally or hit the preview drain timeout.
    #
    # Worker drain can take up to the in-process job drain plus preview drain
    # budget, capped by docker stop_grace_period. Holding an SSH session —
    # and therefore a CI runner minute — open that long is wasteful, so CI
    # sets WORKER_DEPLOY_DETACH=1 to spawn the rollover as a backgrounded
    # host-side process and return immediately. Manual deploys leave it unset
    # to keep the synchronous "did it work?" feedback loop.
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
$(declare -f resolve_worker_drain_timeout_seconds drain_worker_service drain_worker_containers_blocking read_worker_env_value load_worker_endpoint_check_env sanitize_compose_project list_running_worker_containers worker_container_node_id first_running_worker_node_id run_worker_deployctl wait_worker_db_heartbeat worker_port_in_use worker_runtime_endpoint_in_use worker_blue_green_extra_ports_configured worker_host_capacity_preflight fingerprint_files compose_service_fingerprint worker_process_config_fingerprint worker_support_service_fingerprint worker_host_runtime_fingerprint worker_docker_daemon_fingerprint ensure_routine_worker_fingerprints_compatible worker_expected_schema_version protect_active_executor_images find_free_worker_port start_worker_generation drain_old_worker_containers deploy_worker_blue_green wait_container_healthy dump_diagnostics prune_docker_deploy_artifacts)
COMPOSE_FILE='$COMPOSE_FILE'
HEALTH_SERVICE='$HEALTH_SERVICE'
STATUS_FILE='$status_file'
IMAGE_TAG='$IMAGE_TAG'
DEPLOY_MODE='${DEPLOY_MODE:-routine}'
DEPLOY_REQUESTED_BY='${DEPLOY_REQUESTED_BY:-deploy-script}'
DEPLOY_REASON='${DEPLOY_REASON:-routine worker rollout}'
DB_HOST=$(printf '%q' "$(read_worker_env_value DB_HOST)")
DB_PASSWORD=$(printf '%q' "$(read_worker_env_value DB_PASSWORD)")
DEPLOY_DOCKER_PRUNE='${DEPLOY_DOCKER_PRUNE:-1}'
DOCKER_PRUNE_UNTIL='${DOCKER_PRUNE_UNTIL:-24h}'
DEPLOY_DOCKER_VOLUME_PRUNE='${DEPLOY_DOCKER_VOLUME_PRUNE:-0}'
WORKER_DEPLOY_DRAIN_TIMEOUT_SECONDS='${WORKER_DEPLOY_DRAIN_TIMEOUT_SECONDS:-}'
WORKER_BLUE_GREEN_PORT_START='${WORKER_BLUE_GREEN_PORT_START:-}'
WORKER_BLUE_GREEN_PORT_END='${WORKER_BLUE_GREEN_PORT_END:-}'
WORKER_BLUE_GREEN_PREFLIGHT_ATTEMPTS='${WORKER_BLUE_GREEN_PREFLIGHT_ATTEMPTS:-}'
WORKER_BLUE_GREEN_PREFLIGHT_RETRY_DELAY_SECONDS='${WORKER_BLUE_GREEN_PREFLIGHT_RETRY_DELAY_SECONDS:-}'
WORKER_BASE_NODE_ID='${WORKER_BASE_NODE_ID:-}'
WORKER_DRAIN_TIMEOUT='${WORKER_DRAIN_TIMEOUT:-}'
WORKER_HOST_RUNTIME_FINGERPRINT_FILES='$WORKER_HOST_RUNTIME_FINGERPRINT_FILES'
WORKER_DOCKER_DAEMON_FINGERPRINT_FILES='$WORKER_DOCKER_DAEMON_FINGERPRINT_FILES'
WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES='$WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES'
WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES='$WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES'

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
echo "[\$(date -u -Iseconds)] starting detached worker blue/green deploy (tag=$IMAGE_TAG)"
deploy_worker_blue_green
echo "[\$(date -u -Iseconds)] blue/green deploy succeeded"
echo "ok" > "\$STATUS_FILE"
prune_docker_deploy_artifacts worker
EOS
      chmod 700 "$rollover_script"

      # setsid: new session, detached from the SSH controlling tty so the
      #   child survives session end (no SIGHUP).
      # flock: serialize against any prior detached deploy on this host so
      #   back-to-back commits can't race on docker.
      # </dev/null + redirect: nothing tied back to the SSH stdio so SSH can
      #   close cleanly.
      setsid bash -c "
        flock -xo /tmp/143-deploy-worker.lock '$rollover_script' >>'$log_file' 2>&1
        rm -f '$rollover_script'
      " </dev/null >/dev/null 2>&1 &
      disown
      echo "Detached worker rollover launched."
      echo "  log:    $log_file"
      echo "  status: $status_file (poll for 'ok' / 'fail')"
      echo "  follow: ssh deploy@<host> tail -f $log_file"
    else
      deploy_worker_blue_green
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
    if ! wait_vector_healthy "$VECTOR_ID"; then
      exit 1
    fi
  fi

  if [ "$ROLE" != "worker" ] || [ -z "${WORKER_DEPLOY_DETACH:-}" ]; then
    prune_docker_deploy_artifacts "$ROLE"
  fi

  echo "Deploy complete ($ROLE)."
REMOTE
