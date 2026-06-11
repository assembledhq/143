#!/usr/bin/env bash
set -euo pipefail

# Deploy to all nodes in the fleet.
# Usage: ./deploy-fleet.sh <ssh-key-path> [image-tag] [roles]
#
# Routine fleet deploys intentionally default to app+worker only. Deploying
# db/redis/logging recreates stateful or operator-facing services and should be
# an explicit maintenance action:
#   ./deploy/scripts/deploy-fleet.sh <ssh-key> [tag] all
#   ./deploy/scripts/deploy-fleet.sh <ssh-key> [tag] app,worker,redis
#   DEPLOY_JOBS=1 ./deploy/scripts/deploy-fleet.sh <ssh-key> [tag] app,worker
#
# Fleet hosts are read from (in priority order):
#   1. FLEET_HOSTS env var             — comma-separated "role:IP" pairs
#   2. .env.production.enc (FLEET_HOSTS) — encrypted, decrypted via SOPS
#
# FLEET_HOSTS format:  app:10.0.0.2,worker:10.0.0.4,db:10.0.0.3,logging:10.0.0.6,redis:10.0.0.5,egress:10.0.0.7

SSH_KEY="$1"
TAG="${2:-latest}"
REQUESTED_ROLES="${3:-app,worker}"
DEPLOY_JOBS="${DEPLOY_JOBS:-4}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

valid_role() {
  case "$1" in
    app|worker|db|logging|redis|all) return 0 ;;
    *) return 1 ;;
  esac
}

validate_requested_roles() {
  if [ "$REQUESTED_ROLES" = "all" ]; then
    return 0
  fi
  local selected
  IFS=',' read -ra selected <<< "$REQUESTED_ROLES"
  for r in "${selected[@]}"; do
    if [ "$r" = "all" ]; then
      echo "ERROR: role 'all' cannot be combined with other roles. Use ROLES=all or list concrete roles." >&2
      exit 1
    fi
    if ! valid_role "$r"; then
      echo "ERROR: unknown deploy role '$r' in roles '$REQUESTED_ROLES'." >&2
      echo "Expected one of: app, worker, db, logging, redis, all." >&2
      exit 1
    fi
  done
}

should_deploy_role() {
  local role="$1"
  if [ "$REQUESTED_ROLES" = "all" ]; then
    return 0
  fi
  local selected
  IFS=',' read -ra selected <<< "$REQUESTED_ROLES"
  for r in "${selected[@]}"; do
    if [ "$r" = "$role" ]; then
      return 0
    fi
  done
  return 1
}

validate_requested_roles
if ! [[ "$DEPLOY_JOBS" =~ ^[1-9][0-9]*$ ]]; then
  echo "ERROR: DEPLOY_JOBS must be a positive integer, got '$DEPLOY_JOBS'." >&2
  exit 1
fi

# Read FLEET_HOSTS from env var, or decrypt from SOPS.
if [ -z "${FLEET_HOSTS:-}" ]; then
  ENC_FILE="$PROJECT_DIR/.env.production.enc"
  if [ -f "$ENC_FILE" ]; then
    echo "Decrypting fleet hosts from .env.production.enc..."
    FLEET_HOSTS="$(sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE" \
      | grep '^FLEET_HOSTS=' | cut -d= -f2- || true)"
  fi
fi

if [ -z "${FLEET_HOSTS:-}" ]; then
  echo "ERROR: FLEET_HOSTS is not set."
  echo "Set FLEET_HOSTS env var or add it to .env.production.enc"
  exit 1
fi

echo "Reading fleet from FLEET_HOSTS..."
echo "Deploying roles: $REQUESTED_ROLES"
echo "Deploying up to $DEPLOY_JOBS node(s) concurrently. Set DEPLOY_JOBS=1 for serial deploys."
# DEPLOY_FLEET_LOG_DIR pins per-host logs to a stable path so CI can upload
# them as an artifact; ephemeral runners lose /tmp the moment the job ends.
if [ -n "${DEPLOY_FLEET_LOG_DIR:-}" ]; then
  LOG_DIR="$DEPLOY_FLEET_LOG_DIR"
  mkdir -p "$LOG_DIR"
else
  LOG_DIR="$(mktemp -d /tmp/deploy-fleet.XXXXXX)"
fi
HOSTS=()
HOST_GROUP_FILES=()
TARGET_COUNT=0
IFS=',' read -ra ENTRIES <<< "$FLEET_HOSTS"
for entry in "${ENTRIES[@]}"; do
  ROLE="${entry%%:*}"
  IP="${entry#*:}"
  if [ "$ROLE" = "egress" ]; then
    echo "Skipping egress@$IP (static egress gateways are managed by make provision-egress)."
    continue
  fi
  if ! should_deploy_role "$ROLE"; then
    echo "Skipping $ROLE@$IP (not in requested roles: $REQUESTED_ROLES)."
    continue
  fi
  group_file=""
  for i in "${!HOSTS[@]}"; do
    if [ "${HOSTS[$i]}" = "$IP" ]; then
      group_file="${HOST_GROUP_FILES[$i]}"
      break
    fi
  done
  if [ -z "$group_file" ]; then
    safe_host="${IP//[^A-Za-z0-9_.-]/_}"
    group_file="$LOG_DIR/host-$safe_host.targets"
    HOSTS+=("$IP")
    HOST_GROUP_FILES+=("$group_file")
  fi
  printf '%s:%s\n' "$ROLE" "$IP" >> "$group_file"
  TARGET_COUNT=$((TARGET_COUNT + 1))
done

if [ "$TARGET_COUNT" -eq 0 ]; then
  echo "ERROR: No fleet hosts matched requested roles: $REQUESTED_ROLES." >&2
  exit 1
fi

deploy_one() {
  local target="$1"
  local role="${target%%:*}"
  local ip="${target#*:}"
  local safe_name="${role}-${ip//[^A-Za-z0-9_.-]/_}"
  local log="$LOG_DIR/$safe_name.log"

  echo "--- Deploying $role to $ip (log: $log) ---"
  if "$SCRIPT_DIR/deploy.sh" "$role" "$ip" "$SSH_KEY" "$TAG" >"$log" 2>&1; then
    echo "--- OK: $role@$ip ---"
  else
    echo "--- FAILED: $role@$ip (log tail printed after all deploys finish) ---" >&2
    touch "$log.failed"
    return 1
  fi
}

# Print the tail of every failed host's log after the parallel fan-out
# finishes. Dumping from the parent (not deploy_one) keeps concurrent
# failures from interleaving their logs. On GitHub Actions each log is
# wrapped in a collapsible ::group:: and also appended to the job summary.
DEPLOY_FAIL_LOG_LINES="${DEPLOY_FAIL_LOG_LINES:-200}"
dump_failed_logs() {
  local marker failed_log name
  for marker in "$LOG_DIR"/*.log.failed; do
    [ -e "$marker" ] || continue
    failed_log="${marker%.failed}"
    name="$(basename "$failed_log" .log)"
    echo ""
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
      echo "::group::FAILED $name — last $DEPLOY_FAIL_LOG_LINES log lines"
    else
      echo "===== FAILED $name — last $DEPLOY_FAIL_LOG_LINES log lines (full log: $failed_log) ====="
    fi
    tail -n "$DEPLOY_FAIL_LOG_LINES" "$failed_log"
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
      echo "::endgroup::"
    fi
  done
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
      echo "## Deploy failures"
      for marker in "$LOG_DIR"/*.log.failed; do
        [ -e "$marker" ] || continue
        failed_log="${marker%.failed}"
        name="$(basename "$failed_log" .log)"
        echo "<details><summary><code>$name</code> — last $DEPLOY_FAIL_LOG_LINES log lines</summary>"
        echo ""
        echo '```'
        tail -n "$DEPLOY_FAIL_LOG_LINES" "$failed_log"
        echo '```'
        echo "</details>"
      done
    } >> "$GITHUB_STEP_SUMMARY"
  fi
}

deploy_group() {
  local group_file="$1"
  local target
  while IFS= read -r target; do
    [ -n "$target" ] || continue
    deploy_one "$target" || return 1
  done < "$group_file"
}

export -f deploy_one
export -f deploy_group
export SCRIPT_DIR SSH_KEY TAG LOG_DIR

echo "Deploying ${#HOST_GROUP_FILES[@]} node(s), $DEPLOY_JOBS at a time (logs: $LOG_DIR)."
if printf '%s\n' "${HOST_GROUP_FILES[@]}" | xargs -n1 -P "$DEPLOY_JOBS" bash -c 'deploy_group "$1"' deploy-group; then
  echo "Fleet deployment complete."
else
  dump_failed_logs
  echo "FAILED: one or more deploys failed; see logs in $LOG_DIR." >&2
  exit 1
fi
