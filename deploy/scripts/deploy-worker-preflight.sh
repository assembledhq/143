#!/usr/bin/env bash
set -euo pipefail

# Read-only worker deploy preflight.
# Usage: ./deploy/scripts/deploy-worker-preflight.sh <host> <ssh-key-path>

HOST="${1:?host is required}"
SSH_KEY="${2:?ssh key path is required}"
WORKER_BLUE_GREEN_PORT_START="${WORKER_BLUE_GREEN_PORT_START:-8080}"
WORKER_BLUE_GREEN_PORT_END="${WORKER_BLUE_GREEN_PORT_END:-8087}"

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

remote_shell_quote() {
  local value="$1"
  printf "'"
  printf '%s' "$value" | sed "s/'/'\\\\''/g"
  printf "'"
}

remote_env_assignment() {
  local key="$1" value="$2"
  printf '%s=' "$key"
  remote_shell_quote "$value"
}

ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "$(remote_env_assignment WORKER_BLUE_GREEN_PORT_START "$WORKER_BLUE_GREEN_PORT_START")" \
  "$(remote_env_assignment WORKER_BLUE_GREEN_PORT_END "$WORKER_BLUE_GREEN_PORT_END")" \
  'bash -s' <<'REMOTE'
set -euo pipefail

cd /opt/143

read_worker_env_value() {
  local key="$1"
  awk -F= -v key="$key" '$1 == key {sub(/^[^=]*=/, ""); print; exit}' /opt/143/.env
}

required_keys=(NODE_ID WORKER_PRIVATE_IP DB_HOST DB_PASSWORD)
missing=0
for key in "${required_keys[@]}"; do
  value="$(read_worker_env_value "$key")"
  if [ -z "$value" ]; then
    echo "ERROR: $key is missing from /opt/143/.env" >&2
    missing=1
    continue
  fi
  printf -v "$key" '%s' "$value"
done
if [ "$missing" -ne 0 ]; then
  echo "ERROR: worker deploy preflight failed because required worker env is incomplete." >&2
  exit 1
fi

start="${WORKER_BLUE_GREEN_PORT_START:-8080}"
end="${WORKER_BLUE_GREEN_PORT_END:-8087}"
if [[ "$start" == *[!0-9]* ]] || [[ "$end" == *[!0-9]* ]]; then
  echo "ERROR: WORKER_BLUE_GREEN_PORT_START and WORKER_BLUE_GREEN_PORT_END must be numeric." >&2
  exit 1
fi
if [ "$start" -gt "$end" ]; then
  echo "ERROR: WORKER_BLUE_GREEN_PORT_START ($start) must be <= WORKER_BLUE_GREEN_PORT_END ($end)." >&2
  exit 1
fi

worker_port_in_use() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    if ss -ltnH "sport = :$port" 2>/dev/null | grep -q .; then
      return 0
    fi
  fi
  docker ps --format '{{.Ports}}' | grep -Eq "(^|, |:)${port}->8080/tcp"
}

worker_runtime_endpoint_in_use() {
  local port="$1" endpoint query count
  endpoint="http://${WORKER_PRIVATE_IP}:${port}"
  query="WITH endpoint_blockers AS (
  SELECT 1 FROM preview_runtimes WHERE endpoint_url = :'endpoint' AND status IN ('starting', 'ready', 'draining') AND lease_expires_at > now()
  UNION ALL
  SELECT 1 FROM nodes WHERE metadata->>'preview_internal_base_url' = :'endpoint' AND status IN ('active', 'draining') AND last_heartbeat_at >= now() - interval '2 minutes'
)
SELECT COUNT(*) FROM endpoint_blockers;"

  if ! count="$(printf '%s\n' "$query" | docker run -i --rm --network host -e PGPASSWORD="$DB_PASSWORD" postgres:16-alpine \
    psql -h "$DB_HOST" -U onefortythree -d onefortythree \
    -v ON_ERROR_STOP=1 \
    -v endpoint="$endpoint" \
    -tA)"; then
    echo "ERROR: could not verify preview runtime/node endpoint reuse safety for ${endpoint}; refusing routine deploy." >&2
    exit 1
  fi

  count="$(printf '%s' "$count" | tr -d '[:space:]')"
  [ "${count:-0}" -gt 0 ]
}

echo "Checking worker blue/green readiness on NODE_ID=$NODE_ID range=${start}-${end}..."
for port in $(seq "$start" "$end"); do
  if worker_port_in_use "$port"; then
    echo "port $port: Docker/listener already owns this worker endpoint"
    continue
  fi
  if worker_runtime_endpoint_in_use "$port"; then
    echo "port $port: active preview_runtimes or fresh node registry rows still own http://${WORKER_PRIVATE_IP}:${port}"
    continue
  fi
  echo "worker deploy preflight ok: safe worker blue/green port $port is available"
  exit 0
done

echo "ERROR: No safe worker blue/green port found in ${start}-${end}." >&2
echo "Expand WORKER_BLUE_GREEN_PORT_START/END and ensure the app-to-worker private network allows the full range." >&2
echo "Use DEPLOY_MODE=maintenance only for disruptive host/runtime/support-service changes." >&2
exit 1
REMOTE
