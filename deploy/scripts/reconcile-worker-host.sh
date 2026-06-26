#!/usr/bin/env bash
set -euo pipefail

# Canonical idempotent worker host invariant reconciliation.
# Safe to run repeatedly as root from cloud-init, provision.sh, deploy.sh,
# or a manual repair command.

SANDBOX_NETWORK="${1:-143-sandbox}"
SANDBOX_SUBNET="172.30.0.0/24"
STATIC_EGRESS_NETWORK="143-sandbox-static-egress"
STATIC_EGRESS_SUBNET="172.31.0.0/24"
STATIC_EGRESS_RESOLV_CONF="/etc/143/sandbox-static-egress-resolv.conf"
STATIC_EGRESS_DNS_IP="172.31.0.2"
STATIC_EGRESS_CAPABILITY_FILE="/etc/143/static-egress-capable"
STATIC_EGRESS_ENV_FILE="${STATIC_EGRESS_ENV_FILE:-/opt/143/.env}"
STATIC_EGRESS_SECRETS_FILE="${STATIC_EGRESS_SECRETS_FILE:-/opt/143/static-egress-worker.env}"
WORKER_COMPOSE_FILE="${WORKER_COMPOSE_FILE:-/opt/143/docker-compose.worker.yml}"
DEPLOY_SCRIPT_DIR="${DEPLOY_SCRIPT_DIR:-/opt/143/deploy/scripts}"
DEFAULT_NETWORK="${2:-143_default}"
DEPLOY_MODE_FILE="${DEPLOY_MODE_FILE:-/opt/143/.deploy-mode}"
DEPLOY_MODE_FILE_MAX_AGE_SECONDS="${DEPLOY_MODE_FILE_MAX_AGE_SECONDS:-600}"

resolve_deploy_mode() {
  local mode="${DEPLOY_MODE:-}"
  local file_mode file_ts now max_age age

  if [ -z "$mode" ] && [ -r "$DEPLOY_MODE_FILE" ]; then
    read -r file_mode file_ts < "$DEPLOY_MODE_FILE" || true
    if [ -n "${file_mode:-}" ]; then
      case "${file_ts:-}" in
        ""|*[!0-9]*)
          ;;
        *)
          now="$(date +%s)"
          max_age="$DEPLOY_MODE_FILE_MAX_AGE_SECONDS"
          case "$max_age" in
            ""|*[!0-9]*) max_age=600 ;;
          esac
          age=$((now - file_ts))
          if [ "$age" -ge 0 ] && [ "$age" -le "$max_age" ]; then
            mode="$file_mode"
          fi
          ;;
      esac
    fi
  fi

  case "$mode" in
    ""|routine) printf '%s\n' routine ;;
    maintenance) printf '%s\n' maintenance ;;
    *)
      echo "ERROR: invalid DEPLOY_MODE for worker reconciliation: $mode" >&2
      return 1
      ;;
  esac
}

load_static_egress_env_key() {
  local key="$1"
  local file="$2"
  local value

  value="$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)"
  if [ -n "$value" ]; then
    printf -v "$key" '%s' "$value"
    export "$key"
  fi
}

load_static_egress_env() {
  local key

  for key in \
    STATIC_EGRESS_PUBLIC_IP \
    STATIC_EGRESS_GATEWAY_PUBLIC_IP \
    STATIC_EGRESS_GATEWAY_PUBLIC_KEY \
    STATIC_EGRESS_WORKER_PRIVATE_KEY \
    STATIC_EGRESS_WORKER_WG_ADDRESS \
    STATIC_EGRESS_PROBE_IMAGE; do
    if [ -z "${!key:-}" ]; then
      load_static_egress_env_key "$key" "$STATIC_EGRESS_ENV_FILE"
    fi
    if [ -z "${!key:-}" ]; then
      load_static_egress_env_key "$key" "$STATIC_EGRESS_SECRETS_FILE"
    fi
  done
}

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "ERROR: reconcile-worker-host.sh must run as root." >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is required before reconciling worker host invariants." >&2
  exit 1
fi

DEPLOY_MODE="$(resolve_deploy_mode)"
export DEPLOY_MODE

ensure_bridge() {
  local network="$1"
  local subnet="$2"
  local existing_subnet

  existing_subnet=$(docker network inspect "$network" \
    -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null || true)
  if [ -z "$existing_subnet" ]; then
    docker network create --driver bridge \
      --subnet "$subnet" \
      --label managed-by=143 "$network"
  elif [ "$existing_subnet" != "$subnet" ]; then
    echo "ERROR: $network network has subnet '$existing_subnet'; expected $subnet." >&2
    echo "  This worker was provisioned before the pinned-subnet change. To upgrade:" >&2
    echo "    1. docker compose -f /opt/143/docker-compose.worker.yml down" >&2
    echo "    2. docker network rm $network" >&2
    echo "    3. Re-run deploy (or provision-worker) for this host." >&2
    echo "  Step 1 will drain in-flight coding turns; plan for a maintenance window." >&2
    exit 1
  fi
}

# sandbox-dns is pinned to fixed addresses (172.30.0.2 on the sandbox bridge,
# 172.31.0.2 on the static-egress bridge) so the resolv.conf files can hard-code
# the resolver. Because the IPs never change, a single leaked libnetwork
# endpoint still holding one of them — left behind by a daemon hiccup or an
# ungraceful blue/green drain — makes every later recreate fail with
# "failed to set up container networking: Address already in use" and wedges
# ALL future deploys on the host until the endpoint is cleared by hand. This
# helper finds whichever endpoint currently owns a pinned IP and force-detaches
# it so the recreate can reclaim the address.
endpoint_owner_for_ip() {
  local network="$1"
  local ip="$2"

  docker network inspect "$network" \
    -f '{{range .Containers}}{{.Name}} {{.IPv4Address}}{{"\n"}}{{end}}' 2>/dev/null \
    | awk -v want="$ip/" 'index($2, want) == 1 {print $1}' | head -n1
}

disconnect_endpoint_for_ip() {
  local network="$1"
  local ip="$2"
  local endpoint

  endpoint="$(endpoint_owner_for_ip "$network" "$ip")"
  if [ -n "$endpoint" ]; then
    echo "Detaching stale endpoint '$endpoint' holding $ip on $network..." >&2
    docker network disconnect -f "$network" "$endpoint" >/dev/null 2>&1 || true
  fi
}

sandbox_dns_running_with_pinned_ips() {
  local status health sandbox_owner static_owner

  status="$(docker inspect 143-sandbox-dns-1 --format '{{.State.Status}}' 2>/dev/null || true)"
  if [ "$status" != "running" ]; then
    return 1
  fi

  health="$(docker inspect 143-sandbox-dns-1 --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' 2>/dev/null || true)"
  if [ "$health" != "healthy" ]; then
    return 1
  fi

  sandbox_owner="$(endpoint_owner_for_ip "$SANDBOX_NETWORK" 172.30.0.2)"
  static_owner="$(endpoint_owner_for_ip "$STATIC_EGRESS_NETWORK" "$STATIC_EGRESS_DNS_IP")"
  if [ "$sandbox_owner" != "143-sandbox-dns-1" ] || [ "$static_owner" != "143-sandbox-dns-1" ]; then
    return 1
  fi
  return 0
}

wait_for_sandbox_dns_pinned_ips() {
  local timeout="${1:-30}"
  local waited=0

  case "$timeout" in
    ""|*[!0-9]*) timeout=30 ;;
  esac

  while [ "$waited" -le "$timeout" ]; do
    if sandbox_dns_running_with_pinned_ips; then
      return 0
    fi
    if [ "$waited" -eq "$timeout" ]; then
      break
    fi
    sleep 2
    waited=$((waited + 2))
    if [ "$waited" -gt "$timeout" ]; then
      waited="$timeout"
    fi
  done
  return 1
}

warn_if_sandbox_dns_image_drift() {
  local running_image local_image

  running_image="$(docker inspect 143-sandbox-dns-1 --format '{{.Image}}' 2>/dev/null || true)"
  local_image="$(docker image inspect 143-sandbox-dns:local --format '{{.Id}}' 2>/dev/null || true)"
  if [ -n "$running_image" ] && [ -n "$local_image" ] && [ "$running_image" != "$local_image" ]; then
    echo "WARNING: routine deploy leaves healthy sandbox-dns in place even though 143-sandbox-dns:local points at a different image." >&2
    echo "  Run DEPLOY_MODE=maintenance after reviewing active runtimes to activate support-service changes." >&2
  fi
}

clear_sandbox_dns_endpoints() {
  # Remove the prior container (if any) and drop leaked endpoints by name and by
  # pinned IP. The by-IP pass catches ghost endpoints whose owning container was
  # removed uncleanly and no longer matches the container name.
  docker rm -f 143-sandbox-dns-1 >/dev/null 2>&1 || true
  docker network disconnect -f "$SANDBOX_NETWORK" 143-sandbox-dns-1 >/dev/null 2>&1 || true
  docker network disconnect -f "$STATIC_EGRESS_NETWORK" 143-sandbox-dns-1 >/dev/null 2>&1 || true
  disconnect_endpoint_for_ip "$SANDBOX_NETWORK" 172.30.0.2
  disconnect_endpoint_for_ip "$STATIC_EGRESS_NETWORK" "$STATIC_EGRESS_DNS_IP"
}

# Per-turn sandbox run containers (143-worker-run-*) should be removed when a
# coding turn ends; leftovers accumulate stale libnetwork endpoints on the
# sandbox bridge and feed the leaked-endpoint failure handled above. Sweep only
# NON-running containers so in-flight coding turns are never touched.
sweep_stopped_worker_run_containers() {
  local stale
  stale="$(docker ps -a \
    --filter 'name=143-worker-run-' \
    --filter 'status=exited' \
    --filter 'status=created' \
    --filter 'status=dead' \
    --format '{{.ID}}' 2>/dev/null || true)"
  if [ -n "$stale" ]; then
    echo "Removing stale (non-running) worker-run sandbox containers..." >&2
    printf '%s\n' "$stale" | xargs -r docker rm -f >/dev/null 2>&1 || true
  fi
}

ensure_static_egress_dns() {
  local compose_dir
  local compose_file

  if [ ! -f "$WORKER_COMPOSE_FILE" ]; then
    echo "ERROR: $WORKER_COMPOSE_FILE is required before static egress verification." >&2
    exit 1
  fi
  compose_dir="$(dirname "$WORKER_COMPOSE_FILE")"
  compose_file="$(basename "$WORKER_COMPOSE_FILE")"
  if [ ! -f "$compose_dir/Dockerfile.dnsmasq" ]; then
    echo "ERROR: $compose_dir/Dockerfile.dnsmasq is required before static egress verification." >&2
    exit 1
  fi

  # Fresh SSH provisioning verifies static egress before the full worker
  # compose stack is started, so make the pinned sandbox DNS IP available
  # without starting the worker service early. Routine reconciliation must not
  # force a rebuild: recreating sandbox-dns briefly frees the pinned resolver
  # IPs, and an active worker can race in with a new sandbox container that
  # claims them before DNS starts.
  (
    cd "$compose_dir"
    if [ "${DEPLOY_MODE:-routine}" = "routine" ]; then
      if sandbox_dns_running_with_pinned_ips; then
        warn_if_sandbox_dns_image_drift
        echo "sandbox-dns healthy on pinned IPs; routine deploy leaves it in place."
        exit 0
      fi

      if ! docker image inspect 143-sandbox-dns:local >/dev/null 2>&1; then
        docker compose -f "$compose_file" build sandbox-dns
      fi

      if docker compose -f "$compose_file" up -d --no-deps --no-recreate sandbox-dns \
        && wait_for_sandbox_dns_pinned_ips "${SANDBOX_DNS_ROUTINE_WAIT_SECONDS:-30}"; then
        warn_if_sandbox_dns_image_drift
        echo "sandbox-dns verified on pinned IPs without recreation."
        exit 0
      fi

      echo "ERROR: routine worker reconciliation could not verify healthy sandbox-dns without recreating it." >&2
      echo "  Routine deploys do not recreate sandbox-dns because recreating the pinned-IP sidecar can race active sandboxes." >&2
      echo "  Run DEPLOY_MODE=maintenance after reviewing active runtimes, or inspect docker compose/network state on the worker." >&2
      exit 1
    fi

    if ! docker image inspect 143-sandbox-dns:local >/dev/null 2>&1; then
      docker compose -f "$compose_file" build sandbox-dns
    fi

    if docker compose -f "$compose_file" up -d --no-deps sandbox-dns; then
      exit 0
    fi

    echo "sandbox-dns start failed; clearing leaked sandbox-dns network endpoints and retrying..." >&2
    for attempt in 1 2 3; do
      clear_sandbox_dns_endpoints
      sweep_stopped_worker_run_containers
      if docker compose -f "$compose_file" up -d --no-deps sandbox-dns; then
        exit 0
      fi
      if [ "$attempt" != "3" ]; then
        sleep 1
      fi
    done
    exit 1
  )
}

# Ensure the shared sandbox bridges exist with pinned subnets. The subnets let
# sandbox-dns claim stable addresses in docker-compose.worker.yml. Leave bridge
# ICC at Docker's default so gVisor sandboxes can reach sandbox-dns.
ensure_bridge "$SANDBOX_NETWORK" "$SANDBOX_SUBNET"
ensure_bridge "$STATIC_EGRESS_NETWORK" "$STATIC_EGRESS_SUBNET"

load_static_egress_env

# Worker blue/green generations run as separate compose projects but must share
# the same default bridge so the worker can resolve support services such as
# chrome by container DNS name. Compose treats this network as external.
if ! docker network inspect "$DEFAULT_NETWORK" >/dev/null 2>&1; then
  docker network create --driver bridge \
    --label managed-by=143 "$DEFAULT_NETWORK"
fi

# Reclaim sandbox-bridge endpoints leaked by ended coding turns before they
# accumulate and starve the pinned sandbox-dns addresses.
sweep_stopped_worker_run_containers

# Install iptables-persistent so the egress block survives reboots. This is
# best-effort because some minimal images prompt or temporarily lack apt locks;
# sandbox-firewall.sh still applies the live rules below.
apt-get install -y --no-install-recommends iptables-persistent >/dev/null 2>&1 || true

if [ -x "$DEPLOY_SCRIPT_DIR/sandbox-firewall.sh" ]; then
  "$DEPLOY_SCRIPT_DIR/sandbox-firewall.sh" "$SANDBOX_NETWORK"
  "$DEPLOY_SCRIPT_DIR/sandbox-firewall.sh" "$STATIC_EGRESS_NETWORK"
else
  echo "ERROR: $DEPLOY_SCRIPT_DIR/sandbox-firewall.sh is missing or not executable." >&2
  exit 1
fi

if [ -x "$DEPLOY_SCRIPT_DIR/sandbox-resolv-conf.sh" ]; then
  "$DEPLOY_SCRIPT_DIR/sandbox-resolv-conf.sh" /etc/143/sandbox-resolv.conf 172.30.0.2
  "$DEPLOY_SCRIPT_DIR/sandbox-resolv-conf.sh" "$STATIC_EGRESS_RESOLV_CONF" "$STATIC_EGRESS_DNS_IP"
else
  echo "ERROR: $DEPLOY_SCRIPT_DIR/sandbox-resolv-conf.sh is missing or not executable." >&2
  exit 1
fi

if [ -n "${STATIC_EGRESS_PUBLIC_IP:-}" ]; then
  if [ ! -x "$DEPLOY_SCRIPT_DIR/install-static-egress-worker.sh" ]; then
    echo "ERROR: static egress is configured but $DEPLOY_SCRIPT_DIR/install-static-egress-worker.sh is missing or not executable." >&2
    exit 1
  fi
  : "${STATIC_EGRESS_GATEWAY_PUBLIC_IP:?STATIC_EGRESS_GATEWAY_PUBLIC_IP is required when STATIC_EGRESS_PUBLIC_IP is configured}"
  : "${STATIC_EGRESS_GATEWAY_PUBLIC_KEY:?STATIC_EGRESS_GATEWAY_PUBLIC_KEY is required when STATIC_EGRESS_PUBLIC_IP is configured}"
  : "${STATIC_EGRESS_WORKER_PRIVATE_KEY:?STATIC_EGRESS_WORKER_PRIVATE_KEY is required when STATIC_EGRESS_PUBLIC_IP is configured}"
  : "${STATIC_EGRESS_WORKER_WG_ADDRESS:?STATIC_EGRESS_WORKER_WG_ADDRESS is required when STATIC_EGRESS_PUBLIC_IP is configured}"
  ensure_static_egress_dns
  "$DEPLOY_SCRIPT_DIR/install-static-egress-worker.sh"
else
  rm -f "$STATIC_EGRESS_CAPABILITY_FILE"
fi

# /run is tmpfs on systemd hosts, so recreate the sandbox auth socket parent
# on boot via tmpfiles and force the current runtime state now. The explicit
# chown/chmod repairs drift such as Docker auto-creating the bind mount source
# as root:root 0755 before reconciliation ran.
cat > /etc/tmpfiles.d/143-sandbox-auth.conf <<'TMPFILES'
d /var/run/143 0755 root root -
d /var/run/143/sandbox-auth 0750 1000 1000 -
TMPFILES
systemd-tmpfiles --create /etc/tmpfiles.d/143-sandbox-auth.conf
mkdir -p /var/run/143/sandbox-auth
chown 1000:1000 /var/run/143/sandbox-auth
chmod 0750 /var/run/143/sandbox-auth

# Worker-local preview dependency cache L1, bind-mounted into the worker
# container. Docker auto-creates a missing bind source as root:root, and hosts
# provisioned before the cache moved host-side never ran the provision-time
# chown, leaving the orchestrator (uid 1000) unable to create its staging dir.
# Reconciling here heals existing hosts on every deploy.
mkdir -p /var/cache/143/preview-dependency-cache
chown 1000:1000 /var/cache/143/preview-dependency-cache
chmod 0750 /var/cache/143/preview-dependency-cache

cat > /etc/sysctl.d/99-worker.conf <<SYSCTL
vm.swappiness = 1
SYSCTL
sysctl -p /etc/sysctl.d/99-worker.conf

echo "Worker host reconciliation complete ($SANDBOX_NETWORK, $STATIC_EGRESS_NETWORK)."
