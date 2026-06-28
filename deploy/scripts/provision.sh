#!/usr/bin/env bash
set -euo pipefail

# Provision a node by running bootstrap.sh + copying config files via SSH.
# Usage: ./provision.sh <role> <host> <ssh-key-path> [--reprovision|--tailscale-only]
#
# Roles: app, worker, db, logging, redis
# This is the SSH-based alternative to cloud-init for already-running servers.
#
# Pass --reprovision to tear down existing containers and volumes before reprovisioning.
# Without --reprovision, the script will abort if services are already running.
# Pass --tailscale-only to enroll an already-provisioned host in Tailscale without
# changing Docker containers, volumes, or application env files.
#
# No env vars required by default — the script reads your age key from
# ~/.config/sops/age/keys.txt and all other secrets from .env.production.enc.
# You can override any value by setting it as an env var.
#
# The script will:
#   1. Decrypt .env.production.enc to extract secrets (DB_PASSWORD, DB_HOST, etc.)
#   2. Run bootstrap.sh on the remote machine (installs Docker, gVisor, etc.)
#   3. Copy the appropriate compose file and deploy configs
#   4. Write .env with secrets
#   5. Log in to GHCR, pull images, and start services
#   6. Run migrations (app role only)

ROLE="$1"
HOST="$2"
SSH_KEY="$3"
MODE="${4:-}"
REPROVISION="$MODE"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DISABLED_WARNING_WEBHOOK_URL="http://localhost:65535/disabled-warning"
DISABLED_CRITICAL_WEBHOOK_URL="http://localhost:65535/disabled-critical"

# Shared worker bucket defaults and mapping logic.
# shellcheck source=deploy/scripts/worker_buckets.sh
source "$SCRIPT_DIR/worker_buckets.sh"

# Validate role
case "$ROLE" in
  app)     COMPOSE_FILE="docker-compose.app.yml" ;;
  worker)  COMPOSE_FILE="docker-compose.worker.yml" ;;
  db)      COMPOSE_FILE="docker-compose.db.yml" ;;
  logging) COMPOSE_FILE="docker-compose.logging.yml" ;;
  redis)   COMPOSE_FILE="docker-compose.redis.yml" ;;
  *)       echo "Unknown role: $ROLE (expected: app, worker, db, logging, redis)"; exit 1 ;;
esac

case "$MODE" in
  ""|"--reprovision"|"--tailscale-only") ;;
  *) echo "Unknown mode: $MODE (expected --reprovision or --tailscale-only)"; exit 1 ;;
esac

# Logging nodes use only public runtime images, but they still rely on values
# that commonly live in the encrypted production env.
# Read SOPS_AGE_KEY from the default age keyfile if not already set
if [ -z "${SOPS_AGE_KEY:-}" ]; then
  AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
  if [ -f "$AGE_KEY_FILE" ]; then
    SOPS_AGE_KEY=$(grep "^AGE-SECRET-KEY-" "$AGE_KEY_FILE" | head -1)
    export SOPS_AGE_KEY
    echo "Read SOPS_AGE_KEY from $AGE_KEY_FILE"
  else
    echo "ERROR: No SOPS_AGE_KEY set and no keyfile at $AGE_KEY_FILE"
    echo "Run 'make secrets-setup' first, or export SOPS_AGE_KEY."
    exit 1
  fi
fi

# Decrypt .env.production.enc to extract secrets locally.
# Values set as env vars already will take precedence (eval won't overwrite).
SECRETS_DIR="$("$SCRIPT_DIR/resolve-secrets-dir.sh" "$PROJECT_DIR")"
ENC_FILE="$SECRETS_DIR/.env.production.enc"
if [ -f "$ENC_FILE" ]; then
  echo "Reading secrets from .env.production.enc..."
  DECRYPTED=$(SOPS_AGE_KEY="$SOPS_AGE_KEY" sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE")

  # Source decrypted values, but don't overwrite non-empty env vars.
  while IFS= read -r line; do
    # Skip empty lines and comments
    [[ -z "$line" || "$line" == \#* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    # Make exports empty vars for forwarding, so allow secrets to fill empties.
    if [ -z "${!key:-}" ]; then
      export "$key=$value"
    fi
  done <<< "$DECRYPTED"
else
  echo "WARNING: .env.production.enc not found at $ENC_FILE"
  echo "Falling back to environment variables."
fi

apply_tailscale_worker_host_map() {
  if [ "$ROLE" != "worker" ] || [ -z "${TS_WORKER_HOSTS:-}" ]; then
    return
  fi

  # TS_WORKER_HOSTS is a comma-separated list of worker management hosts that
  # should join the tailnet. Entries may be either "<host>" or
  # "<node-id>:<host>" so the same production secret can also pin NODE_ID.
  # Example: TS_WORKER_HOSTS="worker-usw-1:87.99.158.39,worker-ec2-1:54.1.2.3"
  IFS=',' read -ra mappings <<< "$TS_WORKER_HOSTS"
  for mapping in "${mappings[@]}"; do
    map_node_id=""
    map_host="$mapping"
    if [[ "$mapping" == *:* ]]; then
      map_node_id="${mapping%%:*}"
      map_host="${mapping#*:}"
    fi

    if [ "$map_host" = "$HOST" ]; then
      : "${WORKER_PRIVATE_IP_SOURCE:=tailscale}"
      if [ -n "$map_node_id" ]; then
        : "${NODE_ID:=$map_node_id}"
      fi
      return
    fi
  done
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

apply_tailscale_role_defaults() {
  apply_tailscale_worker_host_map

  case "$ROLE" in
    app)
      : "${TS_AUTH_KEY:=${TS_AUTH_KEY_APP:-}}"
      : "${TS_TAG:=${TS_TAG_APP:-tag:prod-app}}"
      ;;
    db)
      : "${TS_AUTH_KEY:=${TS_AUTH_KEY_DB:-}}"
      : "${TS_TAG:=${TS_TAG_DB:-tag:prod-db}}"
      if [ -n "${TS_AUTH_KEY:-}" ]; then
        : "${DB_BIND_IP:?DB_BIND_IP is required for db Tailscale route advertisement}"
        : "${TS_ADVERTISE_ROUTES:=${DB_BIND_IP}/32}"
      fi
      ;;
    redis)
      : "${TS_AUTH_KEY:=${TS_AUTH_KEY_REDIS:-}}"
      : "${TS_TAG:=${TS_TAG_REDIS:-tag:prod-redis}}"
      if [ -n "${TS_AUTH_KEY:-}" ]; then
        : "${REDIS_PRIVATE_IP:?REDIS_PRIVATE_IP is required for redis Tailscale route advertisement}"
        : "${TS_ADVERTISE_ROUTES:=${REDIS_PRIVATE_IP}/32}"
      fi
      ;;
    worker)
      if [ "${WORKER_PRIVATE_IP_SOURCE:-private}" = "tailscale" ]; then
        : "${TS_AUTH_KEY:=${TS_AUTH_KEY_WORKER:-}}"
        : "${TS_TAG:=${TS_TAG_WORKER:-tag:prod-worker}}"
        TS_ACCEPT_ROUTES=true
      fi
      ;;
  esac

  if [ "$ROLE" = "worker" ] && [ "${WORKER_PRIVATE_IP_SOURCE:-private}" = "tailscale" ]; then
    : "${TS_AUTH_KEY:?TS_AUTH_KEY or TS_AUTH_KEY_WORKER is required for Tailscale worker provisioning}"
  fi
  if [ "$ROLE" = "db" ] && [ -n "${TS_ADVERTISE_ROUTES:-}" ]; then
    : "${TS_AUTH_KEY:?TS_AUTH_KEY or TS_AUTH_KEY_DB is required when TS_ADVERTISE_ROUTES is set}"
  fi
  if [ "$ROLE" = "redis" ] && [ -n "${TS_ADVERTISE_ROUTES:-}" ]; then
    : "${TS_AUTH_KEY:?TS_AUTH_KEY or TS_AUTH_KEY_REDIS is required when TS_ADVERTISE_ROUTES is set}"
  fi
}

apply_tailscale_role_defaults
apply_worker_bucket_overrides "$ROLE" "$HOST"
apply_static_egress_worker_host_map
if [ "$ROLE" = "worker" ]; then
  : "${SANDBOX_HEALTH_CHECK_IMAGE:=busybox:1.36.1}"
  : "${SANDBOX_REQUIRE_DISK_QUOTA:=true}"
  : "${SANDBOX_GC_INTERVAL:=5m}"
  : "${SANDBOX_GC_GRACE:=30m}"
  : "${SANDBOX_GC_HARD_MAX:=24h}"
fi

# Validate required secrets are available (from env or .env.production.enc)
if [ "$MODE" != "--tailscale-only" ] && [ "$ROLE" != "logging" ] && [ "$ROLE" != "redis" ]; then
  : "${DB_PASSWORD:?DB_PASSWORD is required (set it or add to .env.production.enc)}"
  : "${GHCR_TOKEN:?GHCR_TOKEN is required (set it or add to .env.production.enc)}"
fi
if [ "$MODE" != "--tailscale-only" ] && [ "$ROLE" != "db" ] && [ "$ROLE" != "logging" ] && [ "$ROLE" != "redis" ]; then
  : "${DB_HOST:?DB_HOST is required for $ROLE role (set it or add to .env.production.enc)}"
fi
if [ "$MODE" != "--tailscale-only" ] && [ "$ROLE" = "logging" ]; then
  : "${GRAFANA_ADMIN_PASSWORD:?GRAFANA_ADMIN_PASSWORD is required for logging role (set it or add to .env.production.enc)}"
  GRAFANA_ALERTS_WARNING_WEBHOOK_URL="${GRAFANA_ALERTS_WARNING_WEBHOOK_URL:-$DISABLED_WARNING_WEBHOOK_URL}"
  GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL="${GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL:-$DISABLED_CRITICAL_WEBHOOK_URL}"
fi
if [ "$ROLE" = "db" ]; then
  : "${DB_BIND_IP:?DB_BIND_IP is required for db role (set it to the db node primary private IP)}"
fi
if [ "$MODE" != "--tailscale-only" ] && [ "$ROLE" = "redis" ]; then
  : "${REDIS_PASSWORD:?REDIS_PASSWORD is required for redis role (set it or add to .env.production.enc)}"
  : "${REDIS_PRIVATE_IP:?REDIS_PRIVATE_IP is required for redis role (Redis node private IP)}"
fi
if [ "$MODE" != "--tailscale-only" ] && [ "$ROLE" != "db" ] && [ "$ROLE" != "redis" ]; then
  : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for $ROLE role (logging server private IP)}"
fi

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

wait_for_docker_daemon() {
  echo "--- Waiting for Docker daemon ---"
  ssh "${SSH_OPTS[@]}" root@"$HOST" << 'WAIT_DOCKER'
    set -euo pipefail
    if ! systemctl enable --now docker; then
      echo "ERROR: failed to start Docker." >&2
      systemctl status docker --no-pager >&2 || true
      journalctl -u docker --no-pager -n 100 >&2 || true
      exit 1
    fi

    for i in $(seq 1 30); do
      if su - deploy -c 'docker info >/dev/null 2>&1'; then
        echo "Docker daemon is ready."
        exit 0
      fi
      sleep 2
    done

    echo "ERROR: Docker daemon did not become ready for deploy within 60s." >&2
    systemctl status docker --no-pager >&2 || true
    journalctl -u docker --no-pager -n 100 >&2 || true
    exit 1
WAIT_DOCKER
}

configure_tailscale_if_requested() {
  if [ -z "${TS_AUTH_KEY:-}" ]; then
    return
  fi

  local ts_hostname="${TS_HOSTNAME:-143-${ROLE}-${HOST//./-}}"
  local ts_tag="${TS_TAG:-tag:prod-${ROLE}}"
  local ts_advertise_routes="${TS_ADVERTISE_ROUTES:-}"
  local ts_accept_routes="${TS_ACCEPT_ROUTES:-false}"
  echo "--- Configuring Tailscale ($ts_hostname, $ts_tag) ---"
  printf '%s\n%s\n%s\n%s\n%s\n' "$TS_AUTH_KEY" "$ts_hostname" "$ts_tag" "$ts_advertise_routes" "$ts_accept_routes" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" '
        set -euo pipefail
        read -r TS_AUTH_KEY
        read -r TS_HOSTNAME
        read -r TS_TAG
        read -r TS_ADVERTISE_ROUTES
        read -r TS_ACCEPT_ROUTES
        export TS_AUTH_KEY TS_HOSTNAME TS_TAG TS_ADVERTISE_ROUTES TS_ACCEPT_ROUTES
        /opt/143/deploy/scripts/install-tailscale.sh
      '
}

list_worker_reprovision_containers() {
  if [ "$ROLE" != "worker" ]; then
    return
  fi

  ssh "${SSH_OPTS[@]}" root@"$HOST" \
    "command -v docker >/dev/null 2>&1 && docker ps --filter label=com.docker.compose.service=worker --format '{{.ID}}' || true" \
    2>/dev/null || true
}

if [ "$MODE" = "--tailscale-only" ]; then
  : "${TS_AUTH_KEY:?TS_AUTH_KEY or a role-specific TS_AUTH_KEY_* is required for Tailscale enrollment}"

  echo "=== Enrolling $ROLE node at $HOST in Tailscale only ==="
  ssh "${SSH_OPTS[@]}" root@"$HOST" "mkdir -p /opt/143/deploy/scripts"
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/scripts/install-tailscale.sh" root@"$HOST":/opt/143/deploy/scripts/install-tailscale.sh
  ssh "${SSH_OPTS[@]}" root@"$HOST" "chmod +x /opt/143/deploy/scripts/install-tailscale.sh"
  configure_tailscale_if_requested
  echo ""
  echo "=== Tailscale enrollment applied for $ROLE node at $HOST ==="
  exit 0
fi

resolve_worker_identity() {
  if [ "$ROLE" != "worker" ]; then
    return
  fi

  if [ -z "${WORKER_PRIVATE_IP:-}" ]; then
    if [ "${WORKER_PRIVATE_IP_SOURCE:-private}" = "tailscale" ]; then
      echo "Auto-detecting WORKER_PRIVATE_IP from Tailscale (100.64.0.0/10) via SSH..."
      WORKER_PRIVATE_IP_CANDIDATES="$(ssh "${SSH_OPTS[@]}" root@"$HOST" \
        'command -v tailscale >/dev/null 2>&1 && tailscale ip -4 2>/dev/null || true')"
    else
      echo "Auto-detecting WORKER_PRIVATE_IP via SSH..."
      # Enumerate every private IPv4 on a real network interface, deliberately
      # skipping docker/bridge/veth/WireGuard/loopback. A naive "first private IPv4"
      # filter would silently return 172.17.0.1 (docker0) on hosts where the
      # bridge enumerates before the NIC, and `ip route get 1.1.1.1` returns
      # the *public* IP because the default route goes through the public NIC.
      # We collect candidates (no awk `exit`) so multi-homed hosts surface as
      # an error rather than silently picking whichever NIC enumerates first.
      WORKER_PRIVATE_IP_CANDIDATES="$(ssh "${SSH_OPTS[@]}" root@"$HOST" \
        'ip -4 -o addr show | awk "\$2 !~ /^(docker|br-|veth|virbr|lo|wg)/ && /inet (10\\.|172\\.(1[6-9]|2[0-9]|3[0-1])\\.|192\\.168\\.)/ { split(\$4, a, \"/\"); print a[1] }"')"
    fi
    CANDIDATE_COUNT="$(printf '%s\n' "$WORKER_PRIVATE_IP_CANDIDATES" | grep -c . || true)"
    if [ "$CANDIDATE_COUNT" -eq 0 ]; then
      echo "ERROR: could not auto-detect WORKER_PRIVATE_IP on $HOST."
      if [ "${WORKER_PRIVATE_IP_SOURCE:-private}" = "tailscale" ]; then
        echo "       Tailscale discovery was requested, but no tailscale ip -4 address was available."
      fi
      echo "       Set WORKER_PRIVATE_IP=<ip> and re-run."
      exit 1
    elif [ "$CANDIDATE_COUNT" -gt 1 ]; then
      # Multi-homed hosts (e.g. cluster NIC + storage VLAN) need the operator
      # to disambiguate — the worker registers exactly one preview URL, and
      # silently picking the wrong NIC would make app→worker traffic dead.
      echo "ERROR: $HOST has $CANDIDATE_COUNT private IPv4 addresses on real interfaces:"
      printf '%s\n' "$WORKER_PRIVATE_IP_CANDIDATES" | sed 's/^/         /'
      echo "       Pick the one app nodes will reach and re-run with WORKER_PRIVATE_IP=<ip>."
      exit 1
    fi
    WORKER_PRIVATE_IP="$WORKER_PRIVATE_IP_CANDIDATES"
  fi
  # Default NODE_ID to "worker-<dotted-to-dash private IP>" — stable because
  # the private IP is stable across reboots, and unique across the full
  # RFC1918 space (a last-octet-only default would collide whenever the
  # fleet spans more than one /24, e.g. 10.0.0.4 and 10.0.1.4).
  if [ -z "${NODE_ID:-}" ]; then
    NODE_ID="worker-${WORKER_PRIVATE_IP//./-}"
  fi
  if [ -z "${PREVIEW_INTERNAL_BASE_URL:-}" ]; then
    PREVIEW_INTERNAL_BASE_URL="http://${WORKER_PRIVATE_IP}:8080"
  fi
  # Echo resolved values before writing so the operator can eyeball them
  # and Ctrl-C if a stray env var (e.g. NODE_ID leaked from a dev shell)
  # is about to land on a production worker.
  echo "Worker per-host identity for $HOST:"
  echo "  WORKER_PRIVATE_IP         = $WORKER_PRIVATE_IP"
  echo "  NODE_ID                   = $NODE_ID"
  echo "  PREVIEW_INTERNAL_BASE_URL = $PREVIEW_INTERNAL_BASE_URL"
}

resolve_worker_docker_gid() {
  if [ "$ROLE" != "worker" ]; then
    return
  fi

  if [ -z "${DOCKER_GID:-}" ]; then
    DOCKER_GID="$(ssh "${SSH_OPTS[@]}" root@"$HOST" "getent group docker | cut -d: -f3")"
  fi
  if [[ ! "$DOCKER_GID" =~ ^[0-9]+$ ]]; then
    echo "ERROR: could not resolve numeric DOCKER_GID for docker.sock access on $HOST." >&2
    echo "       Expected 'getent group docker | cut -d: -f3' to return a number; got '$DOCKER_GID'." >&2
    exit 1
  fi

  echo "  DOCKER_GID                = $DOCKER_GID"
}

# Check if already provisioned
RUNNING=$(ssh "${SSH_OPTS[@]}" root@"$HOST" "su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE ps -q 2>/dev/null'" 2>/dev/null || true)
if [ "$ROLE" = "worker" ]; then
  WORKER_REPROVISION_CONTAINERS="$(list_worker_reprovision_containers)"
  if [ -n "$WORKER_REPROVISION_CONTAINERS" ]; then
    RUNNING="$(printf '%s\n%s\n' "$RUNNING" "$WORKER_REPROVISION_CONTAINERS" | grep -v '^$' || true)"
  fi
fi
if [ -n "$RUNNING" ]; then
  if [ "$REPROVISION" != "--reprovision" ]; then
    echo "ERROR: $ROLE node at $HOST is already provisioned and running."
    echo ""
    echo "To tear down and reprovision, run:"
    echo "  make provision-$ROLE HOST=$HOST SSH_KEY=$SSH_KEY REPROVISION=true"
    exit 1
  fi

  echo "=== Reprovisioning $ROLE node at $HOST (tearing down existing) ==="
  if [ "$ROLE" = "worker" ]; then
    "$SCRIPT_DIR/spin-down-worker.sh" "$HOST" "$SSH_KEY" \
      --timeout "${WORKER_REPROVISION_DRAIN_TIMEOUT_SECONDS:-14400}" \
      --executor-timeout "${WORKER_REPROVISION_EXECUTOR_TIMEOUT_SECONDS:-900}"
  else
    ssh "${SSH_OPTS[@]}" root@"$HOST" "su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE down -v'"
  fi
fi

echo "=== Provisioning $ROLE node at $HOST ==="

# Step 1: Run bootstrap script
echo "--- Step 1/5: Running bootstrap.sh ---"
if [ "$ROLE" = "db" ]; then
  # DB nodes don't need gVisor — use a trimmed bootstrap
  ssh "${SSH_OPTS[@]}" root@"$HOST" << 'BOOTSTRAP_DB'
    set -euo pipefail
    id deploy &>/dev/null || adduser --disabled-password --gecos "" deploy
    mkdir -p /home/deploy/.ssh /opt/143
    [ -f /root/.ssh/authorized_keys ] && cp /root/.ssh/authorized_keys /home/deploy/.ssh/
    chown -R deploy:deploy /home/deploy/.ssh /opt/143
    chmod 700 /home/deploy/.ssh
    command -v docker &>/dev/null || (curl -fsSL https://get.docker.com | sh)
    usermod -aG docker deploy
    # Narrow NOPASSWD sudo for the deploy user. Keep entries in sync with
    # bootstrap.sh — anything used by deploy/scripts/deploy.sh on a db
    # host has to be listed here. db doesn't need runsc / sandbox-firewall
    # / iptables-persistent, but must allow install-log-rotation.sh so the
    # routine deploy path can cap docker container log files without an
    # extra root SSH hop.
    cat > /etc/sudoers.d/99-deploy <<'SUDOERS'
Cmnd_Alias DEPLOY_CMDS = \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/scripts, \
    /usr/bin/systemctl restart docker, \
    /opt/143/deploy/scripts/install-log-rotation.sh *, \
    /opt/143/deploy/scripts/install-docker-dns.sh *

deploy ALL=(root) NOPASSWD: DEPLOY_CMDS
SUDOERS
    chmod 440 /etc/sudoers.d/99-deploy
    visudo -cf /etc/sudoers.d/99-deploy
    cat > /etc/sysctl.d/99-postgres.conf <<SYSCTL
vm.overcommit_memory = 2
vm.overcommit_ratio = 80
vm.swappiness = 1
SYSCTL
    sysctl -p /etc/sysctl.d/99-postgres.conf
    echo "Bootstrap complete (db)."
BOOTSTRAP_DB
elif [ "$ROLE" = "logging" ]; then
  # Logging nodes just need Docker — no gVisor, no special kernel tuning
  ssh "${SSH_OPTS[@]}" root@"$HOST" << 'BOOTSTRAP_LOGGING'
    set -euo pipefail
    id deploy &>/dev/null || adduser --disabled-password --gecos "" deploy
    mkdir -p /home/deploy/.ssh /opt/143
    [ -f /root/.ssh/authorized_keys ] && cp /root/.ssh/authorized_keys /home/deploy/.ssh/
    chown -R deploy:deploy /home/deploy/.ssh /opt/143
    chmod 700 /home/deploy/.ssh
    command -v docker &>/dev/null || (curl -fsSL https://get.docker.com | sh)
    usermod -aG docker deploy
    # Narrow NOPASSWD sudo for the deploy user. Logging hosts also need
    # the chown grants because deploy.sh sometimes needs to fix root-owned
    # vmalert/grafana dirs left over from prior provisioning. Keep in
    # sync with bootstrap.sh.
    cat > /etc/sudoers.d/99-deploy <<'SUDOERS'
Cmnd_Alias DEPLOY_CMDS = \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/scripts, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/vmalert, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/grafana, \
    /usr/bin/systemctl restart docker, \
    /opt/143/deploy/scripts/install-log-rotation.sh *, \
    /opt/143/deploy/scripts/install-docker-dns.sh *

deploy ALL=(root) NOPASSWD: DEPLOY_CMDS
SUDOERS
    chmod 440 /etc/sudoers.d/99-deploy
    visudo -cf /etc/sudoers.d/99-deploy
    echo "Bootstrap complete (logging)."
BOOTSTRAP_LOGGING
elif [ "$ROLE" = "redis" ]; then
  ssh "${SSH_OPTS[@]}" root@"$HOST" << 'BOOTSTRAP_REDIS'
    set -euo pipefail
    id deploy &>/dev/null || adduser --disabled-password --gecos "" deploy
    mkdir -p /home/deploy/.ssh /opt/143
    [ -f /root/.ssh/authorized_keys ] && cp /root/.ssh/authorized_keys /home/deploy/.ssh/
    chown -R deploy:deploy /home/deploy/.ssh /opt/143
    chmod 700 /home/deploy/.ssh
    command -v docker &>/dev/null || (curl -fsSL https://get.docker.com | sh)
    usermod -aG docker deploy
    # Narrow NOPASSWD sudo for the deploy user. Keep in sync with
    # bootstrap.sh — install-log-rotation.sh is required so deploy.sh
    # can cap docker container log files without an extra root SSH hop.
    cat > /etc/sudoers.d/99-deploy <<'SUDOERS'
Cmnd_Alias DEPLOY_CMDS = \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/scripts, \
    /usr/bin/systemctl restart docker, \
    /opt/143/deploy/scripts/install-log-rotation.sh *, \
    /opt/143/deploy/scripts/install-docker-dns.sh *

deploy ALL=(root) NOPASSWD: DEPLOY_CMDS
SUDOERS
    chmod 440 /etc/sudoers.d/99-deploy
    visudo -cf /etc/sudoers.d/99-deploy
    cat > /etc/sysctl.d/99-redis.conf <<SYSCTL
vm.overcommit_memory = 1
net.core.somaxconn = 512
SYSCTL
    sysctl -p /etc/sysctl.d/99-redis.conf
    echo "Bootstrap complete (redis)."
BOOTSTRAP_REDIS
else
  ssh "${SSH_OPTS[@]}" root@"$HOST" 'bash -s -- '"$ROLE" < "$SCRIPT_DIR/bootstrap.sh"
fi

# Step 2: Copy compose file and deploy configs
echo "--- Step 2/5: Copying config files ---"
scp "${SCP_OPTS[@]}" "$PROJECT_DIR/$COMPOSE_FILE" root@"$HOST":/opt/143/
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ] || [ "$ROLE" = "logging" ]; then
  # Vector collector is included from the main compose file
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.vector.yml" root@"$HOST":/opt/143/
fi
if [ "$ROLE" = "app" ] || [ "$ROLE" = "worker" ]; then
  # DNS probe is included by both compose files; stage it so the include
  # directive resolves on first `docker compose up`.
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/docker-compose.dns-probe.yml" root@"$HOST":/opt/143/
fi
if [ "$ROLE" = "worker" ]; then
  # sandbox-dns is built locally from docker-compose.worker.yml on first
  # `docker compose up`, so fresh worker provisioning must stage its Dockerfile
  # before Step 5 starts services. Routine deploys refresh this separately.
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/Dockerfile.dnsmasq" root@"$HOST":/opt/143/
fi
if [ "$ROLE" = "app" ]; then
  # Caddy is built locally on the app host so the Cloudflare DNS provider
  # module is available for wildcard preview certificates on first boot.
  scp "${SCP_OPTS[@]}" "$PROJECT_DIR/Dockerfile.caddy" root@"$HOST":/opt/143/
fi
scp "${SCP_OPTS[@]}" -r "$PROJECT_DIR/deploy" root@"$HOST":/opt/143/
ssh "${SSH_OPTS[@]}" root@"$HOST" "chown -R deploy:deploy /opt/143 && chmod +x /opt/143/deploy/scripts/configure-docker-daemon.sh /opt/143/deploy/scripts/install-log-rotation.sh /opt/143/deploy/scripts/install-docker-dns.sh /opt/143/deploy/scripts/install-tailscale.sh /opt/143/deploy/scripts/reconcile-worker-host.sh"

# Step 2a: Configure Docker daemon hardening in one pass BEFORE step 5
# starts services. This pins bounded json-file logs and multi-provider DNS
# resolvers while preserving existing daemon keys such as the worker runsc
# runtime. Applying both settings through one helper avoids back-to-back
# Docker restarts on fresh hosts, which can trip systemd's start-rate limit
# after bootstrap/gVisor has already touched the daemon.
#
# db gets a larger log cap because postgres logs every connection / slow
# query / lock wait, and the db host has no Vector log shipping — the local
# docker log is the only copy of that trail.
case "$ROLE" in
  db) LOG_MAX_SIZE="500m" ;;
  *)  LOG_MAX_SIZE="100m" ;;
esac
ssh "${SSH_OPTS[@]}" root@"$HOST" "/opt/143/deploy/scripts/configure-docker-daemon.sh --log-max-size $LOG_MAX_SIZE --log-max-file 5 --dns 1.1.1.1 8.8.8.8 9.9.9.9"
wait_for_docker_daemon

# Optional Tailscale enrollment. This runs before worker identity resolution
# so a new west-region worker can use WORKER_PRIVATE_IP_SOURCE=tailscale and
# publish its 100.64.0.0/10 address as the internal preview endpoint.
configure_tailscale_if_requested
resolve_worker_identity
resolve_worker_docker_gid
if [ "$ROLE" = "worker" ]; then
  ssh "${SSH_OPTS[@]}" root@"$HOST" 'mkdir -p /var/cache/143/preview-dependency-cache && chown 1000:1000 /var/cache/143/preview-dependency-cache && chmod 0750 /var/cache/143/preview-dependency-cache'
fi

# Step 2b: Sync authorized keys from deploy/authorized_keys/*.pub
# Replaces authorized_keys on the host with exactly the keys in the repo.
# Safe here because provisioning just set up the deploy user with the SSH key.
if ls "$PROJECT_DIR/deploy/authorized_keys"/*.pub &>/dev/null; then
  echo "--- Syncing authorized keys ---"
  "$SCRIPT_DIR/sync-keys.sh" --apply "$SSH_KEY" "$HOST"
fi

# Step 3: Write .env with secrets
# Uses printf + pipe to avoid nested heredoc quoting issues with special chars.
echo "--- Step 3/5: Writing secrets ---"
if [ "$ROLE" = "logging" ]; then
  # Logging nodes need the Grafana admin password and the private IP for binding VictoriaLogs
  printf 'GRAFANA_ADMIN_PASSWORD=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nGRAFANA_ALERTS_WARNING_WEBHOOK_URL=%s\nGRAFANA_ALERTS_CRITICAL_WEBHOOK_URL=%s\n' \
    "$GRAFANA_ADMIN_PASSWORD" "$VICTORIALOGS_HOST" "logging" "$GRAFANA_ALERTS_WARNING_WEBHOOK_URL" "$GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
elif [ "$ROLE" = "db" ]; then
  printf 'DB_PASSWORD=%s\nDB_BIND_IP=%s\n' "$DB_PASSWORD" "$DB_BIND_IP" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
elif [ "$ROLE" = "redis" ]; then
  printf 'REDIS_PASSWORD=%s\nREDIS_PRIVATE_IP=%s\n' "$REDIS_PASSWORD" "$REDIS_PRIVATE_IP" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
elif [ "$ROLE" = "worker" ]; then
  # Workers need the age key plus the encrypted production env bundle because
  # the worker compose file bind-mounts .env.production.enc into the container
  # and docker-entrypoint.sh decrypts it at boot. Provision the file before the
  # first `docker compose up` — if the source path is missing, Docker creates a
  # directory at /opt/143/.env.production.enc and later deploy-time scp fails.
  printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nREDIS_TOPOLOGY=%s\nREDIS_PRIVATE_IP=%s\nREDIS_PASSWORD=%s\nGITHUB_APP_CLIENT_ID=%s\nGITHUB_APP_CLIENT_SECRET=%s\nWORKER_PROCESS_COUNT=%s\nWORKER_MAX_ACTIVE_SANDBOXES=%s\nSANDBOX_CPU_LIMIT=%s\nSANDBOX_MEMORY_LIMIT_MB=%s\nSANDBOX_DISK_LIMIT_GB=%s\nSANDBOX_HEALTH_CHECK_IMAGE=%s\nSANDBOX_REQUIRE_DISK_QUOTA=%s\nSANDBOX_GC_INTERVAL=%s\nSANDBOX_GC_GRACE=%s\nSANDBOX_GC_HARD_MAX=%s\nSTATIC_EGRESS_PUBLIC_IP=%s\n' \
    "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" "${REDIS_TOPOLOGY:-standalone}" "${REDIS_PRIVATE_IP:-}" "${REDIS_PASSWORD:-}" "${GITHUB_APP_CLIENT_ID:-}" "${GITHUB_APP_CLIENT_SECRET:-}" \
    "${WORKER_PROCESS_COUNT:-}" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "${SANDBOX_CPU_LIMIT:-}" "${SANDBOX_MEMORY_LIMIT_MB:-}" "${SANDBOX_DISK_LIMIT_GB:-}" \
    "$SANDBOX_HEALTH_CHECK_IMAGE" "$SANDBOX_REQUIRE_DISK_QUOTA" "$SANDBOX_GC_INTERVAL" "$SANDBOX_GC_GRACE" "$SANDBOX_GC_HARD_MAX" \
    "${STATIC_EGRESS_PUBLIC_IP:-}" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'

  printf 'STATIC_EGRESS_GATEWAY_PUBLIC_IP=%s\nSTATIC_EGRESS_GATEWAY_PUBLIC_KEY=%s\nSTATIC_EGRESS_WORKER_PRIVATE_KEY=%s\nSTATIC_EGRESS_WORKER_WG_ADDRESS=%s\n' \
    "${STATIC_EGRESS_GATEWAY_PUBLIC_IP:-}" "${STATIC_EGRESS_GATEWAY_PUBLIC_KEY:-}" "${STATIC_EGRESS_WORKER_PRIVATE_KEY:-}" "${STATIC_EGRESS_WORKER_WG_ADDRESS:-}" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/static-egress-worker.env && chown deploy:deploy /opt/143/static-egress-worker.env && chmod 600 /opt/143/static-egress-worker.env'

  # Per-host identity/runtime values (NODE_ID, WORKER_PRIVATE_IP,
  # PREVIEW_INTERNAL_BASE_URL, DOCKER_GID) live in .env.local and survive
  # every deploy — the secret refresh in deploy.sh only rewrites /opt/143/.env,
  # then re-appends .env.local.
  printf 'NODE_ID=%s\nWORKER_PRIVATE_IP=%s\nPREVIEW_INTERNAL_BASE_URL=%s\nDOCKER_GID=%s\n' \
    "$NODE_ID" "$WORKER_PRIVATE_IP" "$PREVIEW_INTERNAL_BASE_URL" "$DOCKER_GID" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env.local && chown deploy:deploy /opt/143/.env.local && chmod 600 /opt/143/.env.local'

  # Concatenate so docker compose can interpolate ${WORKER_PRIVATE_IP} etc.
  # when parsing docker-compose.worker.yml. deploy.sh repeats this on every
  # deploy.
  ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat /opt/143/.env.local >> /opt/143/.env'

  if [ -f "$ENC_FILE" ]; then
    scp "${SCP_OPTS[@]}" "$ENC_FILE" root@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" root@"$HOST" "chown deploy:deploy /opt/143/.env.production.enc && chmod 644 /opt/143/.env.production.enc"
  fi
else
  # App nodes get the full secret set for SOPS decryption
  : "${CLOUDFLARE_API_TOKEN:?CLOUDFLARE_API_TOKEN is required for app role (set it or add to .env.production.enc)}"
  : "${PREVIEW_ORIGIN_TEMPLATE:=https://{id}.preview.143.dev}"
  : "${NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE:=$PREVIEW_ORIGIN_TEMPLATE}"
  printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nREDIS_TOPOLOGY=%s\nREDIS_PRIVATE_IP=%s\nREDIS_PASSWORD=%s\nCLOUDFLARE_API_TOKEN=%s\nPREVIEW_ORIGIN_TEMPLATE=%s\nNEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE=%s\nSTATIC_EGRESS_PUBLIC_IP=%s\n' "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" "${REDIS_TOPOLOGY:-standalone}" "${REDIS_PRIVATE_IP:-}" "${REDIS_PASSWORD:-}" "$CLOUDFLARE_API_TOKEN" "$PREVIEW_ORIGIN_TEMPLATE" "$NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE" "${STATIC_EGRESS_PUBLIC_IP:-}" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
  # Copy encrypted production env (baked into the Docker image too, but
  # having it on disk lets you re-decrypt without rebuilding)
  if [ -f "$ENC_FILE" ]; then
    scp "${SCP_OPTS[@]}" "$ENC_FILE" root@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" root@"$HOST" "chown deploy:deploy /opt/143/.env.production.enc && chmod 644 /opt/143/.env.production.enc"
  fi
fi

# Step 4: GHCR login + pull images
echo "--- Step 4/5: Pulling images ---"
case "$ROLE" in
  db|logging|redis)
    # Public images (postgres, victorialogs, grafana) are pulled automatically by compose
    ;;
  *)
    ssh "${SSH_OPTS[@]}" root@"$HOST" << PULL
      set -euo pipefail
      su - deploy -c 'echo "${GHCR_TOKEN}" | docker login ghcr.io -u deploy --password-stdin'
PULL
    ;;
esac

case "$ROLE" in
  app)
    ssh "${SSH_OPTS[@]}" root@"$HOST" << 'PULL_APP'
      set -euo pipefail
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-server:latest'
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-frontend:latest'
PULL_APP
    ;;
  worker)
    ssh "${SSH_OPTS[@]}" root@"$HOST" << 'PULL_WORKER'
      set -euo pipefail
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-server:latest'
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-sandbox:latest'
      /opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox
PULL_WORKER
    ;;
  db|logging|redis)
    # Public images are pulled automatically by compose
    ;;
esac

# Step 5: Start services
echo "--- Step 5/5: Starting services ---"
COMPOSE_FILE_REMOTE="$COMPOSE_FILE"
ssh "${SSH_OPTS[@]}" root@"$HOST" << START
  set -euo pipefail
  su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE_REMOTE up -d'
START

# For db nodes: install automated backups (pg_dump every 6h + weekly restore
# test) and the offsite sync config. The wrapper is idempotent, so this is a
# no-op on reprovision. BACKUP_* (if present in .env.production.enc) were
# exported above and drive /opt/143/backup-sync.env.
if [ "$ROLE" = "db" ]; then
  echo "Configuring DB backups..."
  "$SCRIPT_DIR/provision-db-backups.sh" "$HOST" "$SSH_KEY"
fi

# For app nodes: wait for health + run migrations (single SSH session)
if [ "$ROLE" = "app" ]; then
  echo "Waiting for API health check..."
  ssh "${SSH_OPTS[@]}" root@"$HOST" << HEALTHCHECK
    set -euo pipefail
    for i in \$(seq 1 30); do
      if curl -sf http://localhost:80/api/healthz > /dev/null 2>&1; then
        echo "Health check passed."
        break
      fi
      if [ "\$i" -eq 30 ]; then
        echo "ERROR: Health check timed out after 60s"
        exit 1
      fi
      sleep 2
    done
    su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE_REMOTE exec -T api /bin/migrate up' < /dev/null
HEALTHCHECK
fi

echo ""
echo "=== $ROLE node provisioned at $HOST ==="
