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
DEFAULT_NETWORK="${SINGLE_NODE_DEFAULT_NETWORK:-143-single-node}"
COMPOSE_FILE="${SINGLE_NODE_COMPOSE_FILE:-$PROJECT_DIR/docker-compose.single-node.yml}"

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

DEPLOY_SCRIPT_DIR="$SCRIPT_DIR" \
WORKER_COMPOSE_FILE="$COMPOSE_FILE" \
DEPLOY_MODE="${DEPLOY_MODE:-routine}" \
"$SCRIPT_DIR/reconcile-worker-host.sh" 143-sandbox "$DEFAULT_NETWORK"

mkdir -p \
  /var/lib/143/uploads \
  /var/lib/143/snapshots \
  /var/lib/143/session-files-cache \
  /var/lib/143/preview-snapshots \
  /var/lib/143/preview-hmr
chown -R 1000:1000 /var/lib/143
chmod 0750 /var/lib/143

cat <<EOF
Single-node host preparation complete.

Use this value in .env.single-node:
DOCKER_GID=$docker_gid

Then start the stack with:
docker compose --env-file .env.single-node -f docker-compose.single-node.yml up -d
EOF
