#!/usr/bin/env bash
set -euo pipefail

# Wrapper around provision-egress-gateway.sh that reads gateway static egress
# config from .env.production.enc. This keeps make provision-worker from
# needing operator-exported STATIC_EGRESS_* values after secrets are edited.

HOST="${1:-${HOST:-}}"
SSH_KEY="${2:-${SSH_KEY:-}}"
SSH_USER="${SSH_USER:-root}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ENC_FILE="${STATIC_EGRESS_ENV_FILE:-$PROJECT_DIR/.env.production.enc}"
SSH_OPTS=(-i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)

load_env_key() {
  local key="$1"
  local file="$2"
  local value

  value="$(grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)"
  if [ -n "$value" ] && [ -z "${!key:-}" ]; then
    export "$key=$value"
  fi
}

dotenv_get() {
  local key="$1"
  local file="$2"
  grep -E "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- || true
}

trim_spaces() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

resolve_egress_host_from_fleet() {
  local file="$1"
  local fleet_hosts entry role host resolved_host

  fleet_hosts="$(dotenv_get FLEET_HOSTS "$file")"
  [ -n "$fleet_hosts" ] || return 0

  IFS=',' read -r -a fleet_entries <<< "$fleet_hosts"
  for entry in "${fleet_entries[@]}"; do
    entry="$(trim_spaces "$entry")"
    [ -n "$entry" ] || continue
    role="${entry%%:*}"
    host="${entry#*:}"
    if [ "$role" = "egress" ] && [ -n "$host" ] && [ "$host" != "$entry" ]; then
      if [ -n "${resolved_host:-}" ] && [ "$resolved_host" != "$host" ]; then
        echo "ERROR: FLEET_HOSTS contains multiple egress:<host> entries." >&2
        return 1
      fi
      resolved_host="$host"
    fi
  done

  if [ -n "${resolved_host:-}" ] && [ -z "$HOST" ]; then
    HOST="$resolved_host"
  fi
}

load_static_egress_env() {
  local tmp_env
  tmp_env="$(mktemp)"
  trap 'rm -f "$tmp_env"' RETURN
  sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE" > "$tmp_env"
  resolve_egress_host_from_fleet "$tmp_env"

  for key in \
    STATIC_EGRESS_PUBLIC_IP \
    STATIC_EGRESS_GATEWAY_PRIVATE_KEY \
    STATIC_EGRESS_WORKER_PEERS \
    STATIC_EGRESS_WG_INTERFACE \
    STATIC_EGRESS_GATEWAY_WG_PORT \
    STATIC_EGRESS_GATEWAY_WG_ADDRESS \
    STATIC_EGRESS_GATEWAY_WG_CIDR \
    STATIC_EGRESS_PUBLIC_INTERFACE \
    TS_AUTH_KEY_EGRESS \
    TS_TAG_EGRESS \
    TS_AUTH_KEY \
    TS_TAG \
    TS_HOSTNAME; do
    load_env_key "$key" "$tmp_env"
  done
}

remote_target() {
  printf '%s@%s' "$SSH_USER" "$HOST"
}

remote_sudo_prefix() {
  if [ "$SSH_USER" = "root" ]; then
    printf ''
  else
    printf 'sudo '
  fi
}

default_tailscale_hostname() {
  local host_tag
  host_tag="${HOST//[^A-Za-z0-9-]/-}"
  printf '143-egress-%s' "$host_tag"
}

configure_tailscale_if_requested() {
  : "${TS_AUTH_KEY:=${TS_AUTH_KEY_EGRESS:-}}"
  if [ -z "${TS_AUTH_KEY:-}" ]; then
    return
  fi
  : "${TS_TAG:=${TS_TAG_EGRESS:-}}"
  : "${TS_HOSTNAME:=$(default_tailscale_hostname)}"

  local remote sudo_prefix
  remote="$(remote_target)"
  sudo_prefix="$(remote_sudo_prefix)"

  scp "${SSH_OPTS[@]}" "$SCRIPT_DIR/install-tailscale.sh" "$remote":/tmp/install-tailscale.sh
  printf '%s\n%s\n%s\n%s\n%s\n' "$TS_AUTH_KEY" "$TS_HOSTNAME" "${TS_TAG:-}" "" "false" |
    ssh "${SSH_OPTS[@]}" "$remote" "${sudo_prefix}bash -c 'set -euo pipefail
      read -r TS_AUTH_KEY
      read -r TS_HOSTNAME
      read -r TS_TAG
      read -r TS_ADVERTISE_ROUTES
      read -r TS_ACCEPT_ROUTES
      install -d -m 755 /opt/143/deploy/scripts
      install -m 755 /tmp/install-tailscale.sh /opt/143/deploy/scripts/install-tailscale.sh
      export TS_AUTH_KEY TS_HOSTNAME TS_TAG TS_ADVERTISE_ROUTES TS_ACCEPT_ROUTES
      /opt/143/deploy/scripts/install-tailscale.sh
    '"
}

if [ ! -f "$ENC_FILE" ]; then
  echo "Skipping static egress gateway provisioning; $ENC_FILE does not exist."
  exit 0
fi

if ! command -v sops >/dev/null 2>&1; then
  echo "ERROR: sops is required to load static egress gateway config from $ENC_FILE." >&2
  exit 1
fi

load_static_egress_env

if [ -z "${STATIC_EGRESS_PUBLIC_IP:-}" ]; then
  echo "Skipping static egress gateway provisioning; STATIC_EGRESS_PUBLIC_IP is empty."
  exit 0
fi

if [ -z "$HOST" ]; then
  echo "ERROR: add egress:<host> to FLEET_HOSTS in .env.production.enc, or pass HOST=<ip>." >&2
  exit 1
fi
if [ -z "$SSH_KEY" ]; then
  echo "ERROR: SSH_KEY is required." >&2
  exit 1
fi

: "${STATIC_EGRESS_GATEWAY_PRIVATE_KEY:?STATIC_EGRESS_GATEWAY_PRIVATE_KEY is required; run deploy/scripts/sync-static-egress-secrets.sh --apply}"
: "${STATIC_EGRESS_WORKER_PEERS:?STATIC_EGRESS_WORKER_PEERS is required; run deploy/scripts/sync-static-egress-secrets.sh --apply}"

REMOTE="$(remote_target)"
SUDO_PREFIX="$(remote_sudo_prefix)"

configure_tailscale_if_requested

scp "${SSH_OPTS[@]}" "$SCRIPT_DIR/provision-egress-gateway.sh" "$REMOTE":/tmp/provision-egress-gateway.sh
{
  printf 'STATIC_EGRESS_GATEWAY_PRIVATE_KEY=%s\n' "$STATIC_EGRESS_GATEWAY_PRIVATE_KEY"
  printf 'STATIC_EGRESS_WORKER_PEERS=%s\n' "$STATIC_EGRESS_WORKER_PEERS"
  printf 'STATIC_EGRESS_WG_INTERFACE=%s\n' "${STATIC_EGRESS_WG_INTERFACE:-}"
  printf 'STATIC_EGRESS_GATEWAY_WG_PORT=%s\n' "${STATIC_EGRESS_GATEWAY_WG_PORT:-}"
  printf 'STATIC_EGRESS_GATEWAY_WG_ADDRESS=%s\n' "${STATIC_EGRESS_GATEWAY_WG_ADDRESS:-}"
  printf 'STATIC_EGRESS_GATEWAY_WG_CIDR=%s\n' "${STATIC_EGRESS_GATEWAY_WG_CIDR:-}"
  printf 'STATIC_EGRESS_PUBLIC_INTERFACE=%s\n' "${STATIC_EGRESS_PUBLIC_INTERFACE:-}"
} | ssh "${SSH_OPTS[@]}" "$REMOTE" "${SUDO_PREFIX}sh -c 'install -d -m 700 /opt/143 && cat > /opt/143/static-egress-gateway.env && chmod 600 /opt/143/static-egress-gateway.env'"
ssh "${SSH_OPTS[@]}" "$REMOTE" "${SUDO_PREFIX}sh -c 'install -m 755 /tmp/provision-egress-gateway.sh /opt/143/provision-egress-gateway.sh && set -a && . /opt/143/static-egress-gateway.env && set +a && /opt/143/provision-egress-gateway.sh'"
