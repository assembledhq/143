#!/usr/bin/env bash
set -euo pipefail

# Re-apply demo migrations/seed and prune volatile state on the demo host.
# Usage: deploy/scripts/demo-reset.sh <host> <ssh-key-path>

HOST="${1:?host is required}"
SSH_KEY="${2:?ssh key path is required}"
REMOTE_DIR="/opt/143-demo"

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'bash -s' "$REMOTE_DIR" <<'REMOTE'
set -euo pipefail
remote_dir="$1"
cd "$remote_dir"

docker compose --env-file .env.demo -f docker-compose.demo.yml up -d postgres redis
docker compose --env-file .env.demo -f docker-compose.demo.yml up --force-recreate --exit-code-from migrate migrate
docker compose --env-file .env.demo -f docker-compose.demo.yml up --force-recreate --exit-code-from demo-seed demo-seed
docker compose --env-file .env.demo -f docker-compose.demo.yml run --rm demo-seed /bin/demo-seed prune --max-age 24h
docker compose --env-file .env.demo -f docker-compose.demo.yml up -d api frontend caddy
REMOTE
