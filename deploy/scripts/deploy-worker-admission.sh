#!/usr/bin/env bash
set -euo pipefail

# Advisory observations only: no pulls, Docker runs, configuration, or markers.
# Capacity is checked again by the locked guardian and at each pull/start.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/scripts/worker-disk-capacity.sh
source "$SCRIPT_DIR/worker-disk-capacity.sh"

worker_host_admission() {
  local root="${1:-/opt/143}" logs="${2:-/var/log/143}" legacy="${3:-/tmp/143-deploy-worker.lock}" lock
  # Open existing locks read-only. This probe must not create a lock file.
  if [ -e "$root/.worker-rollout.lock" ]; then
    exec 9<"$root/.worker-rollout.lock" || return 1
    flock -xn 9 || { echo "ERROR: worker rollout is active." >&2; return 1; }
  fi
  if [ -e "$legacy" ]; then
    exec 7<"$legacy" || return 1
    flock -xn 7 || { echo "ERROR: legacy worker rollout is active." >&2; return 1; }
  fi
  for lock in "$logs"/drain-worker-*.lock; do
    [ -e "$lock" ] || continue
    if ! (exec 6<"$lock" && flock -xn 6); then
      echo "ERROR: legacy worker drain monitor is active." >&2
      return 1
    fi
  done
  if [ -e "$root/.worker-rollout" ] || [ -L "$root/.worker-rollout" ]; then
    echo "ERROR: unresolved worker rollout intent; operator reconciliation required." >&2
    return 1
  fi
  if worker_disk_capacity_observe pre-pull; then return 0; fi
  worker_disk_capacity_json >&2 || { echo "ERROR: capacity observation could not be serialized." >&2; }
  return 1
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  host="$1" key="$2"
  environment=""
  for setting in WORKER_BLUE_GREEN_MIN_FREE_DISK_MB WORKER_BLUE_GREEN_MIN_FREE_INODES WORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB WORKER_BLUE_GREEN_PULL_INODE_BUDGET; do
    if [ -n "${!setting+x}" ]; then
      printf -v value '%q' "${!setting}"
      environment+="$setting=$value "
    fi
  done
  {
    printf 'set -euo pipefail\n'
    declare -f worker_disk_capacity_observe worker_disk_capacity_json worker_host_admission
    printf 'worker_host_admission\n'
  } | ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 \
    -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -i "$key" deploy@"$host" "${environment}bash -s"
fi
