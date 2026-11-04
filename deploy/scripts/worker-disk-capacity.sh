#!/usr/bin/env bash
set -euo pipefail
# Pure observations shared by fleet admission and attempt-bound worker gates.
# This helper never creates containers, pulls images, or writes host state.
worker_disk_capacity_observe() {
  local phase="$1"
  local reserve_mb="${WORKER_BLUE_GREEN_MIN_FREE_DISK_MB:-10240}" reserve_inodes="${WORKER_BLUE_GREEN_MIN_FREE_INODES:-100000}"
  local pull_mb="${WORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB:-8192}" pull_inodes="${WORKER_BLUE_GREEN_PULL_INODE_BUDGET:-100000}"
  local docker_root bytes_output inodes_output available_kb available_inodes required_kb required_inodes started finished
  WORKER_DISK_STAGE="$phase"
  WORKER_DISK_REASON=unknown
  WORKER_DISK_STARTED_AT= WORKER_DISK_FINISHED_AT=
  WORKER_DISK_AVAILABLE_KIB= WORKER_DISK_AVAILABLE_INODES=
  WORKER_DISK_REQUIRED_KIB= WORKER_DISK_REQUIRED_INODES=
  # Bound decimal widths before Bash arithmetic; neither zero nor overflow can
  # disable the gate. Fresh observations are collected within one 15s window.
  WORKER_DISK_REASON=invalid_thresholds
  if ! [[ "$reserve_mb" =~ ^[1-9][0-9]{0,11}$ && "$reserve_inodes" =~ ^[1-9][0-9]{0,11}$ &&
          "$pull_mb" =~ ^[1-9][0-9]{0,11}$ && "$pull_inodes" =~ ^[1-9][0-9]{0,11}$ ]]; then
    return 1
  fi
  required_kb=$((reserve_mb * 1024))
  required_inodes="$reserve_inodes"
  case "$phase" in
    pre-pull) required_kb=$(((reserve_mb + pull_mb) * 1024)); required_inodes=$((reserve_inodes + pull_inodes)) ;;
    pre-start) ;;
    *) WORKER_DISK_REASON=invalid_stage; return 1 ;;
  esac
  WORKER_DISK_REQUIRED_KIB="$required_kb"
  WORKER_DISK_REQUIRED_INODES="$required_inodes"
  WORKER_DISK_REASON=clock_probe_failed
  if ! started="$(date +%s)" || ! [[ "$started" =~ ^[1-9][0-9]{0,11}$ ]]; then
    return 1
  fi
  WORKER_DISK_STARTED_AT="$started"
  WORKER_DISK_REASON=docker_root_probe_failed
  if ! docker_root="$(timeout --foreground 5s docker info --format '{{.DockerRootDir}}' 2>/dev/null)" ||
     ! [[ "$docker_root" = /* && "$docker_root" != *[[:cntrl:]]* ]] || [ ! -d "$docker_root" ]; then
    return 1
  fi
  WORKER_DISK_REASON=filesystem_probe_failed
  if ! bytes_output="$(LC_ALL=C timeout --foreground 5s df -Pk -- "$docker_root" 2>/dev/null)" ||
     ! inodes_output="$(LC_ALL=C timeout --foreground 5s df -Pi -- "$docker_root" 2>/dev/null)"; then
    return 1
  fi
  WORKER_DISK_REASON=invalid_filesystem_observation
  if ! available_kb="$(printf '%s\n' "$bytes_output" | awk '
      NR==1 {if ($1!="Filesystem" || $2!="1024-blocks" || $4!="Available") exit 1; next}
      NR==2 {if (NF<6 || $2!~/^[1-9][0-9]*$/ || $3!~/^[0-9]+$/ || $4!~/^[1-9][0-9]*$/ || length($2)>15 || length($3)>15 || length($4)>15 || ($3+0)+($4+0)>($2+0)) exit 1; value=$4; next}
      END {if (NR!=2 || value=="") exit 1; print value}')" ||
     ! available_inodes="$(printf '%s\n' "$inodes_output" | awk '
      NR==1 {if ($1!="Filesystem" || $2!="Inodes" || $4!="IFree") exit 1; next}
      NR==2 {if (NF<6 || $2!~/^[1-9][0-9]*$/ || $3!~/^[0-9]+$/ || $4!~/^[1-9][0-9]*$/ || length($2)>15 || length($3)>15 || length($4)>15 || ($3+0)+($4+0)>($2+0)) exit 1; value=$4; next}
      END {if (NR!=2 || value=="") exit 1; print value}')" ||
     ! [[ "$available_kb" =~ ^[1-9][0-9]{0,14}$ && "$available_inodes" =~ ^[1-9][0-9]{0,14}$ ]]; then
    return 1
  fi
  WORKER_DISK_AVAILABLE_KIB="$available_kb"
  WORKER_DISK_AVAILABLE_INODES="$available_inodes"
  WORKER_DISK_REASON=clock_probe_failed
  if ! finished="$(date +%s)" || ! [[ "$finished" =~ ^[1-9][0-9]{0,11}$ ]]; then return 1; fi
  WORKER_DISK_FINISHED_AT="$finished"
  WORKER_DISK_REASON=stale_observation
  if [ "$finished" -lt "$started" ] || [ "$((finished - started))" -gt 15 ]; then return 1; fi
  WORKER_DISK_REASON=insufficient_disk
  if [ "$available_kb" -lt "$required_kb" ]; then return 1; fi
  WORKER_DISK_REASON=insufficient_inodes
  if [ "$available_inodes" -lt "$required_inodes" ]; then return 1; fi
  WORKER_DISK_REASON=accepted
  return 0
}

worker_disk_capacity_json() {
  jq -n --arg stage "$WORKER_DISK_STAGE" --arg reason "$WORKER_DISK_REASON" \
    --arg started "$WORKER_DISK_STARTED_AT" --arg finished "$WORKER_DISK_FINISHED_AT" \
    --arg available_kib "$WORKER_DISK_AVAILABLE_KIB" --arg required_kib "$WORKER_DISK_REQUIRED_KIB" \
    --arg available_inodes "$WORKER_DISK_AVAILABLE_INODES" --arg required_inodes "$WORKER_DISK_REQUIRED_INODES" \
    '{stage:$stage,reason:$reason,started_at:($started|tonumber? // null),finished_at:($finished|tonumber? // null),available_kib:($available_kib|tonumber? // null),required_kib:($required_kib|tonumber? // null),available_inodes:($available_inodes|tonumber? // null),required_inodes:($required_inodes|tonumber? // null)}'
}
