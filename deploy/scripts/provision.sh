#!/usr/bin/env bash
set -euo pipefail

# Provision a node by running bootstrap.sh + copying config files via SSH.
# Usage: ./provision.sh <role> <host> <ssh-key-path>
#
# Roles: app, worker, db
# This is the SSH-based alternative to cloud-init for already-running servers.
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
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Validate role
case "$ROLE" in
  app)    COMPOSE_FILE="docker-compose.app.yml" ;;
  worker) COMPOSE_FILE="docker-compose.worker.yml" ;;
  db)     COMPOSE_FILE="docker-compose.db.yml" ;;
  *)      echo "Unknown role: $ROLE (expected: app, worker, db)"; exit 1 ;;
esac

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
  while IFS='=' read -r key value; do
    # Skip empty lines and comments
    [[ -z "$key" || "$key" == \#* ]] && continue
    # Only set if not already in environment
    if [ -z "${!key+x}" ]; then
      export "$key=$value"
    fi
  done <<< "$DECRYPTED"
else
  echo "WARNING: .env.production.enc not found at $ENC_FILE"
  echo "Falling back to environment variables."
fi

# Validate required secrets are available (from env or .env.production.enc)
: "${DB_PASSWORD:?DB_PASSWORD is required (set it or add to .env.production.enc)}"
: "${GHCR_TOKEN:?GHCR_TOKEN is required (set it or add to .env.production.enc)}"
if [ "$ROLE" != "db" ]; then
  : "${DB_HOST:?DB_HOST is required for $ROLE role (set it or add to .env.production.enc)}"
fi

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

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
    cat > /etc/sysctl.d/99-postgres.conf <<SYSCTL
vm.overcommit_memory = 2
vm.overcommit_ratio = 80
vm.swappiness = 1
SYSCTL
    sysctl -p /etc/sysctl.d/99-postgres.conf
    echo "Bootstrap complete (db)."
BOOTSTRAP_DB
else
  ssh "${SSH_OPTS[@]}" root@"$HOST" 'bash -s -- '"$ROLE" < "$SCRIPT_DIR/bootstrap.sh"
fi

# Step 2: Copy compose file and deploy configs
echo "--- Step 2/5: Copying config files ---"
scp "${SCP_OPTS[@]}" "$PROJECT_DIR/$COMPOSE_FILE" root@"$HOST":/opt/143/
scp "${SCP_OPTS[@]}" -r "$PROJECT_DIR/deploy" root@"$HOST":/opt/143/
ssh "${SSH_OPTS[@]}" root@"$HOST" "chown -R deploy:deploy /opt/143"

# Step 3: Write .env with secrets
# Uses printf + pipe to avoid nested heredoc quoting issues with special chars.
echo "--- Step 3/5: Writing secrets ---"
if [ "$ROLE" = "db" ]; then
  printf 'DB_PASSWORD=%s\n' "$DB_PASSWORD" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
elif [ "$ROLE" = "worker" ]; then
  # Workers only get the secrets they need — no age key or encrypted bundle.
  # A worker compromise cannot decrypt the full production secret set.
  printf 'DB_PASSWORD=%s\nDB_HOST=%s\n' "$DB_PASSWORD" "$DB_HOST" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
else
  # App nodes get the full secret set for SOPS decryption
  printf 'SOPS_AGE_KEY=%s\nDB_PASSWORD=%s\nDB_HOST=%s\n' "$SOPS_AGE_KEY" "$DB_PASSWORD" "$DB_HOST" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/.env && chown deploy:deploy /opt/143/.env && chmod 600 /opt/143/.env'
  # Copy encrypted production env (baked into the Docker image too, but
  # having it on disk lets you re-decrypt without rebuilding)
  if [ -f "$ENC_FILE" ]; then
    scp "${SCP_OPTS[@]}" "$ENC_FILE" root@"$HOST":/opt/143/
    ssh "${SSH_OPTS[@]}" root@"$HOST" "chown deploy:deploy /opt/143/.env.production.enc && chmod 600 /opt/143/.env.production.enc"
  fi
fi

# Step 4: GHCR login + pull images
echo "--- Step 4/5: Pulling images ---"
ssh "${SSH_OPTS[@]}" root@"$HOST" << PULL
  su - deploy -c 'echo "${GHCR_TOKEN}" | docker login ghcr.io -u deploy --password-stdin'
PULL

case "$ROLE" in
  app)
    ssh "${SSH_OPTS[@]}" root@"$HOST" << 'PULL_APP'
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-server:latest'
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-frontend:latest'
PULL_APP
    ;;
  worker)
    ssh "${SSH_OPTS[@]}" root@"$HOST" << 'PULL_WORKER'
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-server:latest'
      su - deploy -c 'docker pull ghcr.io/assembledhq/143-sandbox:latest'
PULL_WORKER
    ;;
  db)
    # Postgres image is pulled automatically by compose
    ;;
esac

# Step 5: Start services
echo "--- Step 5/5: Starting services ---"
COMPOSE_FILE_REMOTE="$COMPOSE_FILE"
ssh "${SSH_OPTS[@]}" root@"$HOST" << START
  su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE_REMOTE up -d'
START

# For app nodes: wait for health + run migrations (single SSH session)
if [ "$ROLE" = "app" ]; then
  echo "Waiting for API health check..."
  ssh "${SSH_OPTS[@]}" root@"$HOST" << HEALTHCHECK
    for i in \$(seq 1 30); do
      if curl -sf http://localhost:8080/healthz > /dev/null 2>&1; then
        echo "Health check passed."
        break
      fi
      if [ "\$i" -eq 30 ]; then
        echo "ERROR: Health check timed out after 60s"
        exit 1
      fi
      sleep 2
    done
    su - deploy -c 'cd /opt/143 && docker compose -f $COMPOSE_FILE_REMOTE exec -T api /bin/migrate up'
HEALTHCHECK
fi

echo ""
echo "=== $ROLE node provisioned at $HOST ==="
