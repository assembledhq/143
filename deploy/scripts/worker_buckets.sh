#!/usr/bin/env bash

# Shared worker bucket defaults for deploy/provision scripts.
# Why these process counts?
# - Default sandbox memory is 4096 MB, so memory is usually the hard ceiling.
# - We set WORKER_PROCESS_COUNT to roughly min(vCPU, floor(RAM_GB / 4)).
# - That is intentionally "full utilization" at default limits, not conservative.
# - If you lower SANDBOX_MEMORY_LIMIT_MB, raise counts; if you raise memory, lower counts.

apply_worker_bucket_overrides() {
  local role="$1"
  local host="$2"
  if [ "$role" != "worker" ]; then
    return
  fi

  # WORKER_BUCKET_MAP is a single env var mapping host/IP to a provider SKU:
  #   WORKER_BUCKET_MAP="10.0.0.4=hcloud-cpx31,10.0.0.5=hcloud-ccx23"
  local bucket
  bucket="${WORKER_DEFAULT_BUCKET:-hcloud-cpx31}"
  if [ -n "${WORKER_BUCKET_MAP:-}" ]; then
    IFS=',' read -ra mappings <<< "$WORKER_BUCKET_MAP"
    for mapping in "${mappings[@]}"; do
      map_host="${mapping%%=*}"
      map_bucket="${mapping#*=}"
      if [ "$map_host" = "$host" ]; then
        bucket="$map_bucket"
        break
      fi
    done
  fi

  case "$bucket" in
    # Hetzner CPX (shared CPU) buckets
    hcloud-cpx11) : "${WORKER_PROCESS_COUNT:=1}" ;; # 2 vCPU / 2 GB
    hcloud-cpx21) : "${WORKER_PROCESS_COUNT:=1}" ;; # 3 vCPU / 4 GB
    hcloud-cpx31) : "${WORKER_PROCESS_COUNT:=2}" ;; # 4 vCPU / 8 GB
    hcloud-cpx41) : "${WORKER_PROCESS_COUNT:=4}" ;; # 8 vCPU / 16 GB
    hcloud-cpx51) : "${WORKER_PROCESS_COUNT:=8}" ;; # 16 vCPU / 32 GB
    # Hetzner CCX (dedicated CPU) buckets
    hcloud-ccx13) : "${WORKER_PROCESS_COUNT:=2}" ;;  # 2 vCPU / 8 GB
    hcloud-ccx23) : "${WORKER_PROCESS_COUNT:=4}" ;;  # 4 vCPU / 16 GB
    hcloud-ccx33) : "${WORKER_PROCESS_COUNT:=8}" ;;  # 8 vCPU / 32 GB
    hcloud-ccx43) : "${WORKER_PROCESS_COUNT:=16}" ;; # 16 vCPU / 64 GB
    hcloud-ccx53) : "${WORKER_PROCESS_COUNT:=32}" ;; # 32 vCPU / 128 GB
    hcloud-ccx63) : "${WORKER_PROCESS_COUNT:=48}" ;; # 48 vCPU / 192 GB
    # AWS EC2 buckets
    ec2-t3.xlarge) : "${WORKER_PROCESS_COUNT:=4}" ;;   # 4 vCPU / 16 GB
    ec2-c6i.2xlarge) : "${WORKER_PROCESS_COUNT:=6}" ;; # 8 vCPU / 16 GB
    ec2-c6i.4xlarge) : "${WORKER_PROCESS_COUNT:=10}" ;; # 16 vCPU / 32 GB
    *)
      : "${WORKER_PROCESS_COUNT:=4}" # default baseline
      ;;
  esac

  : "${SANDBOX_CPU_LIMIT:=2}"
  : "${SANDBOX_MEMORY_LIMIT_MB:=4096}"
  : "${SANDBOX_DISK_LIMIT_GB:=10}"
}
