#!/usr/bin/env bash
# Remove only resources proved to belong to this disposable acceptance run.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/prepare-review-acceptance-runtime.sh"

daemon_pid_matches() {
  local daemon_pid=$1
  [[ $daemon_pid =~ ^[1-9][0-9]*$ ]] || return 1
  sudo -n python3 - "$daemon_pid" "$REVIEW_ACCEPTANCE_RUNTIME_STATE/daemon.json" <<'PY'
import sys
from pathlib import Path
try:
    args = (Path('/proc') / sys.argv[1] / 'cmdline').read_bytes().split(b'\0')
except FileNotFoundError:
    raise SystemExit(1)
expected = [b'dockerd', b'--config-file', sys.argv[2].encode()]
raise SystemExit(0 if args[:3] == expected and all(not a for a in args[3:]) else 1)
PY
}

main() {
  guard_host
  validate_paths
  [[ ${1:-} != --validate ]] || return 0
  [[ -z ${1:-} ]] || fail 'usage: cleanup-review-acceptance-runtime.sh [--validate]'
  [[ -e $REVIEW_ACCEPTANCE_RUNTIME_STATE ]] || return 0
  verify_state
  clear_docker_client_environment
  local state=$REVIEW_ACCEPTANCE_RUNTIME_STATE artifacts=$REVIEW_ACCEPTANCE_ARTIFACT_DIR failed=0
  mkdir -p "$artifacts"
  exec > >(tee -a "$artifacts/runtime-cleanup.log") 2>&1
  local daemon_pid=''
  if [[ -f $state/launch.pid && ! -L $state/launch.pid ]]; then daemon_pid=$(cat "$state/launch.pid"); fi
  if [[ -n $daemon_pid ]] && daemon_pid_matches "$daemon_pid"; then
    # The endpoint is the verified owned daemon; still select only the owned
    # network and the probe's explicit state label rather than globally pruning.
    local -a ids=() network_ids=() probe_ids=()
    if timeout 10s docker info >/dev/null 2>&1; then
      local listed_ids
      if timeout 10s docker network inspect "$REVIEW_ACCEPTANCE_NETWORK" >/dev/null 2>&1; then
        if listed_ids=$(timeout 10s docker ps -aq --filter "network=$REVIEW_ACCEPTANCE_NETWORK"); then
          if [[ -n $listed_ids ]]; then mapfile -t network_ids <<< "$listed_ids"; fi
        else printf 'owned network container listing failed\n'; failed=1; fi
      fi
      if listed_ids=$(timeout 10s docker ps -aq --filter "label=io.143.review-acceptance-state=$state"); then
        if [[ -n $listed_ids ]]; then mapfile -t probe_ids <<< "$listed_ids"; fi
      else printf 'owned probe container listing failed\n'; failed=1; fi
      ids=("${network_ids[@]}" "${probe_ids[@]}")
      if (( ${#ids[@]} )); then mapfile -t ids < <(printf '%s\n' "${ids[@]}" | sort -u); fi
      if (( ${#ids[@]} )); then
        if ! timeout 30s docker rm -f "${ids[@]}"; then printf 'owned container removal failed\n'; failed=1; fi
      fi
      if timeout 10s docker network inspect "$REVIEW_ACCEPTANCE_NETWORK" >/dev/null 2>&1; then
        if ! timeout 15s docker network rm "$REVIEW_ACCEPTANCE_NETWORK"; then printf 'owned network removal failed\n'; failed=1; fi
      fi
      if ! timeout 10s docker ps -a --no-trunc > "$artifacts/containers-after-cleanup.txt"; then failed=1; fi
    else
      printf 'owned daemon is not answering; proceeding to verified daemon shutdown\n'
      failed=1
    fi
    if ! sudo -n kill -TERM "$daemon_pid"; then printf 'owned daemon TERM failed\n'; failed=1; fi
    for _ in {1..20}; do
      if ! daemon_pid_matches "$daemon_pid"; then break; fi
      sleep 1
    done
    if daemon_pid_matches "$daemon_pid"; then
      printf 'owned daemon did not stop in 20 seconds; issuing KILL\n'
      if ! sudo -n kill -KILL "$daemon_pid"; then failed=1; fi
      failed=1
    fi
  elif [[ -n $daemon_pid && -e /proc/$daemon_pid ]]; then
    printf 'refusing to signal a PID whose command does not match the owned daemon\n'
    failed=1
  fi

  if [[ -f $state/loop-device && ! -L $state/loop-device ]]; then
    local loop
    loop=$(cat "$state/loop-device")
    if [[ ! $loop =~ ^/dev/loop[0-9]+$ ]]; then fail 'invalid owned loop device'; fi
    # Before unmount or detach, match the actual kernel loop backing to the
    # canonical owned file. A stale/reused loop number is never authority.
    if sudo -n losetup --list --json --output NAME,BACK-FILE "$loop" > "$state/loop-current.json"; then
      if ! python3 - "$state/loop-current.json" "$state/backing.xfs" "$loop" <<'PY'
import json, sys
from pathlib import Path
records = json.loads(Path(sys.argv[1]).read_text())['loopdevices']
expected = Path(sys.argv[2])
if len(records) != 1 or records[0]['name'] != sys.argv[3] or expected.is_symlink() or records[0]['back-file'] != str(expected):
    raise SystemExit('loop backing does not match the owned acceptance file')
PY
      then fail 'refusing cleanup of an unowned loop device'; fi
      if mountpoint -q "$state/mount"; then
        local mounted_source
        mounted_source=$(findmnt -n -o SOURCE --target "$state/mount")
        [[ $mounted_source == "$loop" ]] || fail 'mounted source does not match the owned loop device'
        if ! sudo -n umount "$state/mount"; then printf 'owned XFS unmount failed\n'; failed=1; fi
      fi
      if ! mountpoint -q "$state/mount"; then
        if ! sudo -n losetup -d "$loop"; then printf 'owned loop detach failed\n'; failed=1; fi
      fi
    else
      printf 'recorded loop observation failed; checking backing associations before removal\n'
    fi
  fi
  local attachments
  if [[ -L $state/backing.xfs ]]; then
    printf 'preserving symlinked backing; owned allocation cannot be established\n'
    failed=1
  elif [[ -e $state/backing.xfs ]]; then
    if attachments=$(sudo -n losetup -j "$state/backing.xfs"); then
      if ! mountpoint -q "$state/mount" && [[ -z $attachments ]]; then
        if [[ -f $state/backing.xfs ]]; then rm "$state/backing.xfs"; fi
      else
        printf 'preserving still-mounted or attached XFS backing\n'
        failed=1
      fi
    else
      printf 'cannot establish loop detach state; preserving XFS backing\n'
      failed=1
    fi
  fi
  df -B1 "$RUNNER_TEMP" > "$artifacts/host-capacity-after-cleanup.txt"
  printf 'runtime cleanup result: %s\n' "$failed"
  return "$failed"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
