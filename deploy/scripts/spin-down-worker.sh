#!/usr/bin/env bash
set -euo pipefail

# Drain and stop a production worker host.
# Usage:
#   ./deploy/scripts/spin-down-worker.sh <host> [ssh-key] [--clear] [--timeout seconds] [--executor-timeout seconds]
#
# The default path is intentionally conservative: drain worker generations,
# then stop compose-managed support services. Destructive host cleanup only
# runs when --clear is passed.

HOST="${1:-}"
if [ -z "$HOST" ]; then
  echo "Usage: $0 <host> [ssh-key] [--clear] [--timeout seconds] [--executor-timeout seconds]" >&2
  exit 1
fi
shift

SSH_KEY="${SSH_KEY:-$HOME/.ssh/143-deploy}"
if [ "${1:-}" != "" ] && [[ "${1:-}" != --* ]]; then
  SSH_KEY="$1"
  shift
fi

CLEAR_MACHINE=0
WORKER_DRAIN_TIMEOUT_SECONDS="${WORKER_DRAIN_TIMEOUT_SECONDS:-14400}"
EXECUTOR_DRAIN_TIMEOUT_SECONDS="${EXECUTOR_DRAIN_TIMEOUT_SECONDS:-900}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --clear)
      CLEAR_MACHINE=1
      shift
      ;;
    --timeout)
      WORKER_DRAIN_TIMEOUT_SECONDS="${2:?--timeout requires seconds}"
      shift 2
      ;;
    --executor-timeout)
      EXECUTOR_DRAIN_TIMEOUT_SECONDS="${2:?--executor-timeout requires seconds}"
      shift 2
      ;;
    *)
      echo "ERROR: unknown option: $1" >&2
      exit 1
      ;;
  esac
done

if [[ "$WORKER_DRAIN_TIMEOUT_SECONDS" == *[!0-9]* ]]; then
  echo "ERROR: --timeout must be an integer number of seconds." >&2
  exit 1
fi
if [[ "$EXECUTOR_DRAIN_TIMEOUT_SECONDS" == *[!0-9]* ]]; then
  echo "ERROR: --executor-timeout must be an integer number of seconds." >&2
  exit 1
fi
if [ ! -f "$SSH_KEY" ]; then
  echo "ERROR: SSH key not found: $SSH_KEY" >&2
  exit 1
fi

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

echo "Spinning down worker host $HOST..."
echo "  worker drain timeout:   ${WORKER_DRAIN_TIMEOUT_SECONDS}s"
echo "  executor drain timeout: ${EXECUTOR_DRAIN_TIMEOUT_SECONDS}s"
echo "  clear machine:          ${CLEAR_MACHINE}"

ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
  "CLEAR_MACHINE=$CLEAR_MACHINE" \
  "WORKER_DRAIN_TIMEOUT_SECONDS=$WORKER_DRAIN_TIMEOUT_SECONDS" \
  "EXECUTOR_DRAIN_TIMEOUT_SECONDS=$EXECUTOR_DRAIN_TIMEOUT_SECONDS" \
  bash <<'REMOTE'
set -euo pipefail

cd /opt/143

list_worker_containers() {
  docker ps --filter "label=com.docker.compose.service=worker" --format '{{.ID}}'
}

list_session_executor_containers() {
  {
    docker ps -a --filter "label=com.143.role=session-executor" --format '{{.ID}}'
    docker ps -a --filter "name=143-session-executor-" --format '{{.ID}}'
  } | sort -u
}

list_managed_sandbox_containers() {
  {
    docker ps -a \
      --filter "label=com.assembledhq.143.managed=true" \
      --filter "label=com.assembledhq.143.type=sandbox" \
      --format '{{.ID}}'
    docker ps -a --filter "label=143.sandbox=true" --format '{{.ID}}'
  } | sort -u
}

wait_for_stopped() {
  local label="$1" timeout="$2"
  shift 2
  local containers=("$@")
  local waited=0 running_count cid

  if [ "${#containers[@]}" -eq 0 ]; then
    echo "No $label containers found."
    return 0
  fi

  while true; do
    running_count=0
    for cid in "${containers[@]}"; do
      if docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | grep -q true; then
        running_count=$((running_count + 1))
      fi
    done
    if [ "$running_count" -eq 0 ]; then
      echo "$label containers stopped."
      return 0
    fi
    if [ "$waited" -ge "$timeout" ]; then
      echo "ERROR: timed out waiting for $label containers to stop (${running_count} still running)." >&2
      return 1
    fi
    sleep 5
    waited=$((waited + 5))
  done
}

drain_worker_generations() {
  mapfile -t worker_containers < <(list_worker_containers)
  if [ "${#worker_containers[@]}" -eq 0 ]; then
    echo "No running worker generations found."
    return 0
  fi

  echo "Requesting worker drain for ${#worker_containers[@]} generation(s)..."
  for cid in "${worker_containers[@]}"; do
    echo "  SIGTERM worker ${cid:0:12}"
    docker kill --signal=TERM "$cid" >/dev/null || true
  done

  if ! wait_for_stopped "worker" "$WORKER_DRAIN_TIMEOUT_SECONDS" "${worker_containers[@]}"; then
    echo "Forcing remaining worker containers with a hard 30-second deadline..."
    for cid in "${worker_containers[@]}"; do
      if docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | grep -q true; then
        docker stop -t 30 "$cid" >/dev/null || true &
      fi
    done
    wait
  fi
}

drain_session_executors() {
  mapfile -t executor_containers < <(list_session_executor_containers)
  if [ "${#executor_containers[@]}" -eq 0 ]; then
    echo "No durable session executor containers found."
    return 0
  fi

  echo "Stopping ${#executor_containers[@]} durable session executor container(s)..."
  for cid in "${executor_containers[@]}"; do
    docker stop -t "$EXECUTOR_DRAIN_TIMEOUT_SECONDS" "$cid" >/dev/null || true &
  done
  wait
  echo "Session executor containers stopped."
}

stop_compose_services() {
  echo "Stopping worker compose services..."
  docker compose -f docker-compose.worker.yml down --remove-orphans
}

clear_machine() {
  echo "Clearing worker-owned containers and unused Docker artifacts..."

  mapfile -t executor_containers < <(list_session_executor_containers)
  if [ "${#executor_containers[@]}" -gt 0 ]; then
    docker rm -f -v "${executor_containers[@]}" >/dev/null || true
  fi

  mapfile -t sandbox_containers < <(list_managed_sandbox_containers)
  if [ "${#sandbox_containers[@]}" -gt 0 ]; then
    docker rm -f -v "${sandbox_containers[@]}" >/dev/null || true
  fi

  docker container prune -f
  docker volume prune -f
  docker system prune -af
}

drain_worker_generations
drain_session_executors
stop_compose_services

if [ "$CLEAR_MACHINE" = "1" ]; then
  clear_machine
else
  echo "Machine cleanup skipped. Re-run with --clear to remove leftover executors, sandboxes, volumes, and Docker cache."
fi

echo "Worker spin-down complete."
REMOTE
