#!/usr/bin/env bash

# Shared worker bucket defaults for deploy/provision scripts.
# Why these process counts?
# - Default sandbox memory is 3072 MB (3 GB cgroup limit). Tmpfs at /tmp +
#   /var/tmp uses ~768 MB of that worst-case, leaving ~2.25 GB for the agent.
# - We reserve host RAM before sizing sandboxes: max(2 GB, 10% of node RAM).
# - We set WORKER_PROCESS_COUNT and WORKER_MAX_ACTIVE_SANDBOXES to roughly:
#   max(1, min(vCPU, floor((RAM_GB - reserve_GB) / 3))).
# - WORKER_PROCESS_COUNT controls job loops; WORKER_MAX_ACTIVE_SANDBOXES caps
#   real live containers, including preview-held sandboxes on this node.
# - That targets high utilization at default limits while leaving room for the
#   worker process, Docker/gVisor overhead, kernel memory, and page cache.
# - If you lower SANDBOX_MEMORY_LIMIT_MB further, raise counts; if you raise it,
#   lower counts.

apply_worker_bucket_overrides() {
  local role="$1"
  local host="$2"
  if [ "$role" != "worker" ]; then
    return
  fi

  # WORKER_BUCKET_MAP is a single env var mapping provider SKU to host/IP:
  #   WORKER_BUCKET_MAP="hcloud-cpx31:10.0.0.4,hcloud-ccx23:10.0.0.5"
  local bucket
  bucket="${WORKER_DEFAULT_BUCKET:-hcloud-cpx31}"
  if [ -n "${WORKER_BUCKET_MAP:-}" ]; then
    IFS=',' read -ra mappings <<< "$WORKER_BUCKET_MAP"
    for mapping in "${mappings[@]}"; do
      map_bucket="${mapping%%:*}"
      map_host="${mapping#*:}"
      if [ "$map_host" = "$host" ]; then
        bucket="$map_bucket"
        break
      fi
    done
  fi

  case "$bucket" in
    # Hetzner CPX (shared CPU) buckets
    hcloud-cpx11) : "${WORKER_PROCESS_COUNT:=1}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=1}" ;;  # 2 vCPU / 2 GB  (RAM-bound)
    hcloud-cpx21) : "${WORKER_PROCESS_COUNT:=1}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=1}" ;;  # 3 vCPU / 4 GB  (RAM-bound)
    hcloud-cpx31) : "${WORKER_PROCESS_COUNT:=2}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=2}" ;;  # 4 vCPU / 8 GB
    hcloud-cpx41) : "${WORKER_PROCESS_COUNT:=4}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=4}" ;;  # 8 vCPU / 16 GB
    hcloud-cpx51) : "${WORKER_PROCESS_COUNT:=9}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=9}" ;;  # 16 vCPU / 32 GB
    # Hetzner CCX (dedicated CPU) buckets
    hcloud-ccx13) : "${WORKER_PROCESS_COUNT:=2}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=2}" ;;   # 2 vCPU / 8 GB  (CPU-bound)
    hcloud-ccx23) : "${WORKER_PROCESS_COUNT:=4}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=4}" ;;   # 4 vCPU / 16 GB
    hcloud-ccx33) : "${WORKER_PROCESS_COUNT:=8}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=8}" ;;   # 8 vCPU / 32 GB  (CPU-bound)
    hcloud-ccx43) : "${WORKER_PROCESS_COUNT:=16}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=16}" ;; # 16 vCPU / 64 GB (CPU-bound)
    hcloud-ccx53) : "${WORKER_PROCESS_COUNT:=32}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=32}" ;; # 32 vCPU / 128 GB (CPU-bound)
    hcloud-ccx63) : "${WORKER_PROCESS_COUNT:=48}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=48}" ;; # 48 vCPU / 192 GB (CPU-bound)
    # AWS EC2 buckets
    ec2-t3.xlarge) : "${WORKER_PROCESS_COUNT:=4}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=4}" ;;   # 4 vCPU / 16 GB
    ec2-c6i.2xlarge) : "${WORKER_PROCESS_COUNT:=4}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=4}" ;; # 8 vCPU / 16 GB
    ec2-c6i.4xlarge) : "${WORKER_PROCESS_COUNT:=9}"; : "${WORKER_MAX_ACTIVE_SANDBOXES:=9}" ;; # 16 vCPU / 32 GB
    *)
      : "${WORKER_PROCESS_COUNT:=4}" # default baseline
      : "${WORKER_MAX_ACTIVE_SANDBOXES:=4}"
      ;;
  esac

  : "${SANDBOX_CPU_LIMIT:=2}"
  : "${SANDBOX_MEMORY_LIMIT_MB:=3072}"
  : "${SANDBOX_DISK_LIMIT_GB:=10}"
}
