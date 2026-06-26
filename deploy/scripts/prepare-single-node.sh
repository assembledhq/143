#!/usr/bin/env bash
set -euo pipefail

# Prepare a Linux host for docker-compose.single-node.yml.
#
# This is local, idempotent host setup. It creates the Docker networks,
# sandbox resolver config, sandbox-auth socket directory, preview cache, and
# host-backed application data directory needed by a single-node production
# compose stack.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ENV_FILE="${SINGLE_NODE_ENV_FILE:-$PROJECT_DIR/.env.single-node}"
DEFAULT_NETWORK="${SINGLE_NODE_DEFAULT_NETWORK:-143-single-node}"
COMPOSE_FILE="${SINGLE_NODE_COMPOSE_FILE:-$PROJECT_DIR/docker-compose.single-node.yml}"

read_env_value() {
  local key="$1"
  local file="$2"
  local value

  if [ ! -f "$file" ]; then
    return 1
  fi
  value="$(grep -E "^[[:space:]]*${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d= -f2- || true)"
  if [ -z "$value" ]; then
    return 1
  fi
  value="${value%$'\r'}"
  value="${value#\"}"
  value="${value%\"}"
  value="${value#\'}"
  value="${value%\'}"
  printf '%s\n' "$value"
}

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "ERROR: prepare-single-node.sh must run as root." >&2
  echo "Run: sudo $0" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: Docker is required before preparing a single-node host." >&2
  exit 1
fi

if [ ! -f "$COMPOSE_FILE" ]; then
  echo "ERROR: compose file not found: $COMPOSE_FILE" >&2
  exit 1
fi

docker_gid="$(getent group docker 2>/dev/null | cut -d: -f3 || true)"
if [ -z "$docker_gid" ] && [ -S /var/run/docker.sock ]; then
  docker_gid="$(stat -c '%g' /var/run/docker.sock 2>/dev/null || true)"
fi
if ! [[ "$docker_gid" =~ ^[0-9]+$ ]]; then
  echo "ERROR: could not resolve Docker socket group id." >&2
  echo "Set DOCKER_GID manually in .env.single-node after checking /var/run/docker.sock." >&2
  exit 1
fi

data_dir="${SINGLE_NODE_DATA_DIR:-}"
if [ -z "$data_dir" ]; then
  data_dir="$(read_env_value SINGLE_NODE_DATA_DIR "$ENV_FILE" || true)"
fi
data_dir="${data_dir:-/var/lib/143}"
case "$data_dir" in
  /*) ;;
  *)
    echo "ERROR: SINGLE_NODE_DATA_DIR must be an absolute path, got: $data_dir" >&2
    exit 1
    ;;
esac
if [ "$data_dir" = "/" ]; then
  echo "ERROR: SINGLE_NODE_DATA_DIR must not be /." >&2
  exit 1
fi

DEPLOY_SCRIPT_DIR="$SCRIPT_DIR" \
WORKER_COMPOSE_FILE="$COMPOSE_FILE" \
DEPLOY_MODE="${DEPLOY_MODE:-routine}" \
"$SCRIPT_DIR/reconcile-worker-host.sh" 143-sandbox "$DEFAULT_NETWORK"

mkdir -p \
  "$data_dir/uploads" \
  "$data_dir/snapshots" \
  "$data_dir/session-files-cache" \
  "$data_dir/preview-snapshots" \
  "$data_dir/preview-hmr"
chown -R 1000:1000 "$data_dir"
chmod 0750 "$data_dir"

cat <<EOF
Single-node host preparation complete.

Use this value in .env.single-node:
DOCKER_GID=$docker_gid

Prepared data directory:
SINGLE_NODE_DATA_DIR=$data_dir

Then start the stack with:
docker compose --env-file .env.single-node -f docker-compose.single-node.yml up -d
EOF
