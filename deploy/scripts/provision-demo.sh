#!/usr/bin/env bash
set -euo pipefail

# Provision a one-host public demo node.
# Usage: deploy/scripts/provision-demo.sh <host> <ssh-key-path>

HOST="${1:?host is required}"
SSH_KEY="${2:?ssh key path is required}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
REMOTE_DIR="/opt/143-demo"

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

echo "Provisioning demo host $HOST..."

ssh "${SSH_OPTS[@]}" root@"$HOST" 'bash -s' <<'REMOTE'
set -euo pipefail

if command -v apt-get >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl gnupg openssl ufw
  install -m 0755 -d /etc/apt/keyrings
  if [ ! -f /etc/apt/keyrings/docker.asc ]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc || \
      curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
  fi
  . /etc/os-release
  repo_os="$ID"
  repo_codename="${VERSION_CODENAME:-}"
  if [ "$repo_os" != "ubuntu" ] && [ "$repo_os" != "debian" ]; then
    echo "ERROR: demo provisioning supports Ubuntu/Debian hosts; found $ID" >&2
    exit 1
  fi
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${repo_os} ${repo_codename} stable" > /etc/apt/sources.list.d/docker.list
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
else
  echo "ERROR: apt-get not found; demo provisioning expects Ubuntu/Debian." >&2
  exit 1
fi

systemctl enable --now docker

if ! id deploy >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash deploy
fi
mkdir -p /home/deploy/.ssh
if [ -f /root/.ssh/authorized_keys ]; then
  cp /root/.ssh/authorized_keys /home/deploy/.ssh/authorized_keys
fi
chown -R deploy:deploy /home/deploy/.ssh
chmod 700 /home/deploy/.ssh
if [ -f /home/deploy/.ssh/authorized_keys ]; then
  chmod 600 /home/deploy/.ssh/authorized_keys
fi
usermod -aG docker deploy

mkdir -p /opt/143-demo/deploy
chown -R deploy:deploy /opt/143-demo
chmod 755 /opt/143-demo

if command -v ufw >/dev/null 2>&1; then
  ufw allow OpenSSH
  ufw allow 80/tcp
  ufw allow 443/tcp
  ufw --force enable
fi
REMOTE

tmp_dir="$(ssh "${SSH_OPTS[@]}" root@"$HOST" 'mktemp -d /tmp/143-demo-provision.XXXXXX')"
scp "${SCP_OPTS[@]}" \
  "$PROJECT_DIR/docker-compose.demo.yml" \
  "$PROJECT_DIR/.env.demo.example" \
  root@"$HOST":"$tmp_dir/"
ssh "${SSH_OPTS[@]}" root@"$HOST" "mkdir -p $tmp_dir/deploy"
scp "${SCP_OPTS[@]}" "$PROJECT_DIR/deploy/Caddyfile.demo" root@"$HOST":"$tmp_dir/deploy/Caddyfile.demo"

ssh "${SSH_OPTS[@]}" root@"$HOST" 'bash -s' "$tmp_dir" "$REMOTE_DIR" <<'REMOTE'
set -euo pipefail
tmp_dir="$1"
remote_dir="$2"

install -o deploy -g deploy -m 0644 "$tmp_dir/docker-compose.demo.yml" "$remote_dir/docker-compose.demo.yml"
install -o deploy -g deploy -m 0644 "$tmp_dir/deploy/Caddyfile.demo" "$remote_dir/deploy/Caddyfile.demo"
install -o deploy -g deploy -m 0644 "$tmp_dir/.env.demo.example" "$remote_dir/.env.demo.example"

if [ ! -f "$remote_dir/.env.demo" ]; then
  db_password="$(openssl rand -hex 32)"
  session_secret="$(openssl rand -hex 32)"
  csrf_key="$(openssl rand -hex 32)"
  encryption_master_key="$(openssl rand -hex 32)"
  github_webhook_secret="$(openssl rand -hex 32)"
  cat > "$remote_dir/.env.demo" <<EOF
IMAGE_REGISTRY=ghcr.io/assembledhq
IMAGE_TAG=latest
DOMAIN=demo.143.dev
BASE_URL=https://demo.143.dev
FRONTEND_URL=https://demo.143.dev
CORS_ALLOWED_ORIGINS=https://demo.143.dev
NEXT_PUBLIC_DEMO_URL=https://demo.143.dev
DB_PASSWORD=$db_password
SESSION_SECRET=$session_secret
CSRF_SIGNING_KEY=$csrf_key
ENCRYPTION_MASTER_KEY=$encryption_master_key
GITHUB_WEBHOOK_SECRET=$github_webhook_secret
DEMO_MODE=true
DEMO_READ_ONLY=true
DEMO_ENTRY_EMAIL=preview-viewer@143.dev
DATABASE_MAX_CONNS=10
DATABASE_MAX_CONN_IDLE_TIME=5m
EOF
  chown deploy:deploy "$remote_dir/.env.demo"
  chmod 600 "$remote_dir/.env.demo"
fi

cat > /etc/cron.d/143-demo-prune <<'EOF'
17 3 * * * deploy cd /opt/143-demo && docker compose --env-file .env.demo -f docker-compose.demo.yml run --rm demo-seed /bin/demo-seed prune --max-age 24h >> /var/log/143-demo-prune.log 2>&1
EOF
chmod 0644 /etc/cron.d/143-demo-prune

rm -rf "$tmp_dir"
REMOTE

echo "Demo host provisioned. Edit /opt/143-demo/.env.demo if DOMAIN or image settings need changes."
