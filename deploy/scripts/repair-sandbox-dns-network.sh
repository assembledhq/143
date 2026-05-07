#!/usr/bin/env bash
set -euo pipefail

# Repair the production worker sandbox DNS bridge after a bad 143-sandbox
# network was created with Docker bridge ICC disabled. The repair is
# destructive on each worker: it stops the worker compose stack, removes
# containers attached to 143-sandbox, recreates the bridge, restarts the
# stack, and verifies DNS from a fresh runsc sandbox.
#
# Dry-run by default:
#   deploy/scripts/repair-sandbox-dns-network.sh
#
# Apply to every worker listed in FLEET_HOSTS / .env.production.enc:
#   APPLY=true deploy/scripts/repair-sandbox-dns-network.sh
#
# Optional:
#   SSH_KEY=~/.ssh/143-deploy deploy/scripts/repair-sandbox-dns-network.sh
#   FLEET_HOSTS='worker:1.2.3.4,worker:5.6.7.8' APPLY=true ...

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/143-deploy}"
APPLY="${APPLY:-}"

if [ ! -f "$SSH_KEY" ]; then
  echo "ERROR: SSH key not found: $SSH_KEY" >&2
  echo "Set SSH_KEY=/path/to/key and retry." >&2
  exit 1
fi

resolve_fleet_hosts() {
  if [ -n "${FLEET_HOSTS:-}" ]; then
    printf '%s\n' "$FLEET_HOSTS"
    return
  fi

  local enc_file="$PROJECT_DIR/.env.production.enc"
  if [ ! -f "$enc_file" ]; then
    echo "ERROR: FLEET_HOSTS is unset and $enc_file is missing." >&2
    exit 1
  fi

  local decrypted
  decrypted="$(sops --decrypt --input-type dotenv --output-type dotenv "$enc_file")"
  printf '%s\n' "$decrypted" |
    awk -F= '$1 == "FLEET_HOSTS" {print substr($0, index($0, "=") + 1); exit}' |
    sed -e 's/^"//' -e 's/"$//'
}

FLEET="$(resolve_fleet_hosts)"
WORKER_HOSTS=()
while IFS= read -r host; do
  [ -n "$host" ] && WORKER_HOSTS+=("$host")
done < <(printf '%s\n' "$FLEET" | tr ',' '\n' | grep '^worker:' | cut -d: -f2 | sed '/^$/d')

if [ "${#WORKER_HOSTS[@]}" -eq 0 ]; then
  echo "ERROR: no worker hosts found in FLEET_HOSTS." >&2
  exit 1
fi

echo "Worker hosts to repair:"
printf '  %s\n' "${WORKER_HOSTS[@]}"

if [ "$APPLY" != "true" ]; then
  cat <<'DRYRUN'

Dry run only. Re-run with APPLY=true to perform the repair.

For each worker, APPLY=true will:
  1. Run repair-deploy-sudoers.sh worker <host> <ssh-key> via root SSH.
  2. Stop /opt/143/docker-compose.worker.yml.
  3. Remove remaining containers attached to 143-sandbox.
  4. Recreate 143-sandbox without the bad bridge ICC override.
  5. Re-apply sandbox firewall and resolv.conf.
  6. Restart the worker stack and verify nslookup github.com from runsc.
DRYRUN
  exit 0
fi

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

for host in "${WORKER_HOSTS[@]}"; do
  echo ""
  echo "=== Repairing worker $host ==="

  "$SCRIPT_DIR/repair-deploy-sudoers.sh" worker "$host" "$SSH_KEY"

  ssh "${SSH_OPTS[@]}" deploy@"$host" 'bash -s' <<'REMOTE'
set -euo pipefail

cd /opt/143

echo "--- Current 143-sandbox network options ---"
docker network inspect 143-sandbox --format '{{json .Options}}' 2>/dev/null || echo "143-sandbox does not exist"

echo "--- Stopping worker stack ---"
docker compose -f docker-compose.worker.yml down

echo "--- Removing containers still attached to 143-sandbox ---"
attached="$(docker ps -aq --filter network=143-sandbox || true)"
if [ -n "$attached" ]; then
  # shellcheck disable=SC2086
  docker rm -f $attached
else
  echo "No remaining containers attached to 143-sandbox."
fi

echo "--- Recreating 143-sandbox without bridge ICC override ---"
docker network rm 143-sandbox 2>/dev/null || true
docker network create --driver bridge --subnet 172.30.0.0/24 --label managed-by=143 143-sandbox
docker network inspect 143-sandbox --format '{{json .Options}}'

echo "--- Re-applying sandbox firewall and resolv.conf ---"
sudo -n /opt/143/deploy/scripts/sandbox-firewall.sh 143-sandbox
sudo -n /opt/143/deploy/scripts/sandbox-resolv-conf.sh

echo "--- Restarting worker stack ---"
docker compose -f docker-compose.worker.yml up -d

echo "--- Waiting for sandbox-dns health ---"
for _ in $(seq 1 40); do
  dns_id="$(docker compose -f docker-compose.worker.yml ps -q sandbox-dns || true)"
  if [ -n "$dns_id" ]; then
    status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$dns_id" 2>/dev/null || true)"
    if [ "$status" = "healthy" ]; then
      echo "sandbox-dns is healthy."
      break
    fi
  fi
  sleep 3
done

dns_id="$(docker compose -f docker-compose.worker.yml ps -q sandbox-dns || true)"
if [ -z "$dns_id" ]; then
  echo "ERROR: sandbox-dns container not found after restart." >&2
  exit 1
fi
status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$dns_id")"
if [ "$status" != "healthy" ]; then
  echo "ERROR: sandbox-dns is not healthy after restart; status=$status" >&2
  exit 1
fi

echo "--- Verifying DNS from a fresh runsc sandbox ---"
docker run --rm \
  --runtime=runsc \
  --network 143-sandbox \
  --mount type=bind,src=/etc/143/sandbox-resolv.conf,dst=/etc/resolv.conf,ro \
  busybox:latest nslookup github.com

echo "Worker repair complete."
REMOTE
done

echo ""
echo "All worker sandbox DNS repairs completed."
