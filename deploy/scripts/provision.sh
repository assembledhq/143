#!/usr/bin/env bash
set -euo pipefail

# Provision a node by running bootstrap.sh + copying config files via SSH.
# Usage: ./provision.sh <role> <host> <ssh-key-path> [--reprovision]
#
# Roles: app, worker, db, logging, redis
# This is the SSH-based alternative to cloud-init for already-running servers.
#
# Pass --reprovision to tear down existing containers and volumes before reprovisioning.
# Without --reprovision, the script will abort if services are already running.
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
REPROVISION="${4:-}"
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
ENC_FILE="$PROJECT_DIR/.env.production.enc"
if [ -f "$ENC_FILE" ]; then
  echo "Reading secrets from .env.production.enc..."
  DECRYPTED=$(SOPS_AGE_KEY="$SOPS_AGE_KEY" sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE")

  # Source decrypted values, but don't overwrite existing env vars
  while IFS= read -r line; do
    # Skip empty lines and comments
    [[ -z "$line" || "$line" == \#* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    # Only set if not already in environment
    if [ -z "${!key+x}" ]; then
      export "$key=$value"
    fi
  done <<< "$DECRYPTED"
else
  echo "WARNING: .env.production.enc not found at $ENC_FILE"
  echo "Falling back to environment variables."
fi

apply_worker_bucket_overrides "$ROLE" "$HOST"
if [ "$ROLE" = "worker" ]; then
  : "${SANDBOX_HEALTH_CHECK_IMAGE:=busybox:1.36.1}"
  : "${SANDBOX_REQUIRE_DISK_QUOTA:=true}"
  : "${SANDBOX_GC_INTERVAL:=5m}"
  : "${SANDBOX_GC_GRACE:=30m}"
  : "${SANDBOX_GC_HARD_MAX:=24h}"
fi

# Validate required secrets are available (from env or .env.production.enc)
if [ "$ROLE" != "logging" ] && [ "$ROLE" != "redis" ]; then
  : "${DB_PASSWORD:?DB_PASSWORD is required (set it or add to .env.production.enc)}"
  : "${GHCR_TOKEN:?GHCR_TOKEN is required (set it or add to .env.production.enc)}"
fi
if [ "$ROLE" != "db" ] && [ "$ROLE" != "logging" ] && [ "$ROLE" != "redis" ]; then
  : "${DB_HOST:?DB_HOST is required for $ROLE role (set it or add to .env.production.enc)}"
fi
if [ "$ROLE" = "logging" ]; then
  : "${GRAFANA_ADMIN_PASSWORD:?GRAFANA_ADMIN_PASSWORD is required for logging role (set it or add to .env.production.enc)}"
  GRAFANA_ALERTS_WARNING_WEBHOOK_URL="${GRAFANA_ALERTS_WARNING_WEBHOOK_URL:-$DISABLED_WARNING_WEBHOOK_URL}"
  GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL="${GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL:-$DISABLED_CRITICAL_WEBHOOK_URL}"
fi
if [ "$ROLE" = "db" ]; then
  : "${DB_BIND_IP:?DB_BIND_IP is required for db role (set it to the db node primary private IP)}"
fi
if [ "$ROLE" = "redis" ]; then
  : "${REDIS_PASSWORD:?REDIS_PASSWORD is required for redis role (set it or add to .env.production.enc)}"
  : "${REDIS_PRIVATE_IP:?REDIS_PRIVATE_IP is required for redis role (Redis node private IP)}"
fi
if [ "$ROLE" != "db" ] && [ "$ROLE" != "redis" ]; then
  : "${VICTORIALOGS_HOST:?VICTORIALOGS_HOST is required for $ROLE role (logging server private IP)}"
fi

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

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
      # skipping docker/bridge/veth/loopback. A naive "first private IPv4"
      # filter would silently return 172.17.0.1 (docker0) on hosts where the
      # bridge enumerates before the NIC, and `ip route get 1.1.1.1` returns
      # the *public* IP because the default route goes through the public NIC.
      # We collect candidates (no awk `exit`) so multi-homed hosts surface as
      # an error rather than silently picking whichever NIC enumerates first.
      WORKER_PRIVATE_IP_CANDIDATES="$(ssh "${SSH_OPTS[@]}" root@"$HOST" \
        'ip -4 -o addr show | awk "\$2 !~ /^(docker|br-|veth|virbr|lo)/ && /inet (10\\.|172\\.(1[6-9]|2[0-9]|3[0-1])\\.|192\\.168\\.)/ { split(\$4, a, \"/\"); print a[1] }"')"
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

# Check if already provisioned
RUNNING=$(ssh "${SSH_OPTS[@]}" root@"$HOST" "su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE ps -q 2>/dev/null'" 2>/dev/null || true)
if [ -n "$RUNNING" ]; then
  if [ "$REPROVISION" != "--reprovision" ]; then
    echo "ERROR: $ROLE node at $HOST is already provisioned and running."
    echo ""
    echo "To tear down and reprovision, run:"
    echo "  make provision-$ROLE HOST=$HOST SSH_KEY=$SSH_KEY REPROVISION=true"
    exit 1
  fi

  echo "=== Reprovisioning $ROLE node at $HOST (tearing down existing) ==="
  ssh "${SSH_OPTS[@]}" root@"$HOST" "su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE down -v'"
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
ssh "${SSH_OPTS[@]}" root@"$HOST" "chown -R deploy:deploy /opt/143 && chmod +x /opt/143/deploy/scripts/install-log-rotation.sh /opt/143/deploy/scripts/install-docker-dns.sh /opt/143/deploy/scripts/install-tailscale.sh"

# Step 2a: Cap docker container log files (max-size/max-file in
# /etc/docker/daemon.json) BEFORE step 5 starts services. Closes the
# provision-to-first-deploy window where new containers would log
# unboundedly. db gets a larger cap because postgres logs every
# connection / slow query / lock wait, and the db host has no Vector log
# shipping — the local docker log is the only copy of that trail.
case "$ROLE" in
  db) LOG_MAX_SIZE="500m" ;;
  *)  LOG_MAX_SIZE="100m" ;;
esac
ssh "${SSH_OPTS[@]}" root@"$HOST" "/opt/143/deploy/scripts/install-log-rotation.sh $LOG_MAX_SIZE 5"

# Step 2a (continued): Pin Docker daemon DNS to multiple independent
# resolvers. Without this, the embedded resolver at 127.0.0.11 inherits the
# host's resolv.conf — usually a single provider DNS — so one upstream
# outage takes the whole fleet's container DNS down at once. The
# 2026-05-07T04:15Z incident hit three workers simultaneously this way.
# Order is fastest first; Docker's embedded resolver falls through to the
# next on a SERVFAIL/timeout. Cloudflare + Google + Quad9 are independent
# operators and networks.
ssh "${SSH_OPTS[@]}" root@"$HOST" "/opt/143/deploy/scripts/install-docker-dns.sh 1.1.1.1 8.8.8.8 9.9.9.9"

# Optional Tailscale enrollment. This runs before worker identity resolution
# so a new west-region worker can use WORKER_PRIVATE_IP_SOURCE=tailscale and
# publish its 100.64.0.0/10 address as the internal preview endpoint.
configure_tailscale_if_requested
resolve_worker_identity

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
  printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nREDIS_TOPOLOGY=%s\nREDIS_PRIVATE_IP=%s\nREDIS_PASSWORD=%s\nGITHUB_APP_CLIENT_ID=%s\nGITHUB_APP_CLIENT_SECRET=%s\nWORKER_PROCESS_COUNT=%s\nWORKER_MAX_ACTIVE_SANDBOXES=%s\nSANDBOX_CPU_LIMIT=%s\nSANDBOX_MEMORY_LIMIT_MB=%s\nSANDBOX_DISK_LIMIT_GB=%s\nSANDBOX_HEALTH_CHECK_IMAGE=%s\nSANDBOX_REQUIRE_DISK_QUOTA=%s\nSANDBOX_GC_INTERVAL=%s\nSANDBOX_GC_GRACE=%s\nSANDBOX_GC_HARD_MAX=%s\n' \
    "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" "${REDIS_TOPOLOGY:-standalone}" "${REDIS_PRIVATE_IP:-}" "${REDIS_PASSWORD:-}" "${GITHUB_APP_CLIENT_ID:-}" "${GITHUB_APP_CLIENT_SECRET:-}" \
    "${WORKER_PROCESS_COUNT:-}" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "${SANDBOX_CPU_LIMIT:-}" "${SANDBOX_MEMORY_LIMIT_MB:-}" "${SANDBOX_DISK_LIMIT_GB:-}" \
    "$SANDBOX_HEALTH_CHECK_IMAGE" "$SANDBOX_REQUIRE_DISK_QUOTA" "$SANDBOX_GC_INTERVAL" "$SANDBOX_GC_GRACE" "$SANDBOX_GC_HARD_MAX" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'

  # Per-host identity (NODE_ID, WORKER_PRIVATE_IP, PREVIEW_INTERNAL_BASE_URL)
  # lives in .env.local and survives every deploy — the secret refresh in
  # deploy.sh only rewrites /opt/143/.env, then re-appends .env.local.
  printf 'NODE_ID=%s\nWORKER_PRIVATE_IP=%s\nPREVIEW_INTERNAL_BASE_URL=%s\n' \
    "$NODE_ID" "$WORKER_PRIVATE_IP" "$PREVIEW_INTERNAL_BASE_URL" \
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
  printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\nVICTORIALOGS_HOST=%s\nSERVER_ROLE=%s\nREDIS_TOPOLOGY=%s\nREDIS_PRIVATE_IP=%s\nREDIS_PASSWORD=%s\nCLOUDFLARE_API_TOKEN=%s\nPREVIEW_ORIGIN_TEMPLATE=%s\nNEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE=%s\n' "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" "$VICTORIALOGS_HOST" "$ROLE" "${REDIS_TOPOLOGY:-standalone}" "${REDIS_PRIVATE_IP:-}" "${REDIS_PASSWORD:-}" "$CLOUDFLARE_API_TOKEN" "$PREVIEW_ORIGIN_TEMPLATE" "$NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE" \
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
      # Ensure the shared sandbox bridge exists with a pinned subnet.
      #
      # The subnet is hard-coded to 172.30.0.0/24 so sandbox-dns can be
      # given the stable static IP 172.30.0.2 in docker-compose.worker.yml,
      # which /etc/143/sandbox-resolv.conf below points at. Without a pinned
      # subnet Docker auto-assigns from its default pool and the static IP
      # mapping breaks.
      #
      # Leave Docker's bridge ICC setting at its default. On some Docker /
      # gVisor combinations, disabling bridge ICC blocks sandbox traffic to
      # the sandbox-dns sidecar before DOCKER-USER can carve it out, which
      # breaks all agent DNS resolution.
      # docker inspect returns "" on a missing network (with exit 1 swallowed
      # by `|| true`) and the subnet string on an existing one. Distinguishing
      # the two via a single call keeps us from spawning two `su - deploy`
      # login shells per provision.
      EXISTING_SANDBOX_SUBNET=$(su - deploy -c 'docker network inspect 143-sandbox -f "{{range .IPAM.Config}}{{.Subnet}}{{end}}" 2>/dev/null' || true)
      if [ -z "$EXISTING_SANDBOX_SUBNET" ]; then
        su - deploy -c 'docker network create --driver bridge --subnet 172.30.0.0/24 --label managed-by=143 143-sandbox'
      elif [ "$EXISTING_SANDBOX_SUBNET" != "172.30.0.0/24" ]; then
        echo "ERROR: 143-sandbox network has subnet '$EXISTING_SANDBOX_SUBNET'; expected 172.30.0.0/24." >&2
        echo "  This worker was provisioned before the pinned-subnet change. To upgrade:" >&2
        echo "    1. docker compose -f /opt/143/docker-compose.worker.yml down" >&2
        echo "    2. docker network rm 143-sandbox" >&2
        echo "    3. Re-run provision-worker for this host." >&2
        echo "  Step 1 will drain in-flight coding turns; plan for a maintenance window." >&2
        exit 1
      fi
      # Install iptables-persistent so the egress block survives reboots.
      apt-get install -y --no-install-recommends iptables-persistent >/dev/null 2>&1 || true
      # Apply sandbox egress firewall. Script is idempotent and reads the
      # network's current subnet, so safe to re-run on every provision.
      if [ -x /opt/143/deploy/scripts/sandbox-firewall.sh ]; then
        /opt/143/deploy/scripts/sandbox-firewall.sh 143-sandbox
      fi
      # Provision /etc/143/sandbox-resolv.conf via the shared writer so the
      # provisioning path and routine deploys agree byte-for-byte on its
      # contents. See deploy/scripts/sandbox-resolv-conf.sh for the full
      # rationale (gVisor + Docker embedded DNS + sandbox-dns sidecar). The
      # file was scp'd to /opt/143 in Step 2 and runs as root here.
      /opt/143/deploy/scripts/sandbox-resolv-conf.sh
      # Provision /var/run/143/sandbox-auth/ for the per-session GitHub
      # credential sockets. The worker container bind-mounts this path
      # in (see docker-compose.worker.yml); the orchestrator running as
      # appuser uid 1000 opens one Unix-domain socket per session here,
      # and the docker daemon bind-mounts the per-session subdir into
      # the sandbox container at /run/143-auth/. SANDBOX_AUTH_SOCKET_DIR
      # points the server at this path.
      #
      # /run is tmpfs on systemd hosts (and /var/run is a symlink to it),
      # so the directory disappears on every reboot. We register it with
      # systemd-tmpfiles so it's recreated at boot — and via --create
      # below, immediately on first provision. Mode 0750 satisfies the
      # orchestrator's startup assertion (assertParentDirPerms in
      # internal/services/sandboxauth/server.go); owner 1000:1000 matches
      # the worker container's appuser so MkdirAll on per-session subdirs
      # succeeds.
      cat > /etc/tmpfiles.d/143-sandbox-auth.conf <<'TMPFILES'
d /var/run/143 0755 root root -
d /var/run/143/sandbox-auth 0750 1000 1000 -
TMPFILES
      systemd-tmpfiles --create /etc/tmpfiles.d/143-sandbox-auth.conf
      # Belt-and-suspenders: if the directory already existed (e.g. Docker
      # auto-created the bind-mount source as root:root 0755 before this
      # provision step ran, or an older provision left it 0755), tmpfiles
      # --create's adjustment isn't always reliable across systemd
      # versions. Force the desired ownership and mode explicitly so the
      # orchestrator's assertParentDirPerms check passes on first boot.
      mkdir -p /var/run/143/sandbox-auth
      chown 1000:1000 /var/run/143/sandbox-auth
      chmod 0750 /var/run/143/sandbox-auth
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
