#!/usr/bin/env bash
set -euo pipefail

# Deploy the public demo stack to an already-provisioned demo host.
# Usage: deploy/scripts/deploy-demo.sh <host> <ssh-key-path> [image-tag]

HOST="${1:?host is required}"
SSH_KEY="${2:?ssh key path is required}"
TAG="${3:-latest}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
REMOTE_DIR="/opt/143-demo"

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

echo "Deploying demo host $HOST with tag $TAG..."

tmp_dir="$(ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'mktemp -d /tmp/143-demo-deploy.XXXXXX')"
scp "${SCP_OPTS[@]}" \
  "$PROJECT_DIR/docker-compose.demo.yml" \
  "$PROJECT_DIR/.env.demo.example" \
  deploy@"$HOST":"$tmp_dir/"
ssh "${SSH_OPTS[@]}" deploy@"$HOST" "mkdir -p $tmp_dir/deploy"
scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/Caddyfile.demo" deploy@"$HOST":"$tmp_dir/deploy/Caddyfile.demo"

ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'bash -s' "$tmp_dir" "$REMOTE_DIR" "$TAG" <<'REMOTE'
set -euo pipefail
tmp_dir="$1"
remote_dir="$2"
tag="$3"

cd "$remote_dir"
install -m 0644 "$tmp_dir/docker-compose.demo.yml" docker-compose.demo.yml
install -m 0644 "$tmp_dir/deploy/Caddyfile.demo" deploy/Caddyfile.demo
install -m 0644 "$tmp_dir/.env.demo.example" .env.demo.example
rm -rf "$tmp_dir"

if [ ! -f .env.demo ]; then
  echo "ERROR: .env.demo is missing. Run make provision-demo first." >&2
  exit 1
fi

update_env() {
  local key="$1" value="$2"
  if grep -q "^${key}=" .env.demo; then
    sed -i "s#^${key}=.*#${key}=${value}#" .env.demo
  else
    printf '%s=%s\n' "$key" "$value" >> .env.demo
  fi
}

update_env IMAGE_TAG "$tag"

docker compose --env-file .env.demo -f docker-compose.demo.yml pull
docker compose --env-file .env.demo -f docker-compose.demo.yml up -d postgres redis
docker compose --env-file .env.demo -f docker-compose.demo.yml up --force-recreate --exit-code-from migrate migrate
docker compose --env-file .env.demo -f docker-compose.demo.yml up --force-recreate --exit-code-from demo-seed demo-seed
docker compose --env-file .env.demo -f docker-compose.demo.yml up -d --remove-orphans api frontend caddy
docker compose --env-file .env.demo -f docker-compose.demo.yml ps
REMOTE

echo "Demo deploy complete."
