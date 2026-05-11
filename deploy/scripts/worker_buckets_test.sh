#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/scripts/worker_buckets.sh
source "$SCRIPT_DIR/worker_buckets.sh"

assert_eq() {
  local expected="$1"
  local actual="$2"
  local message="$3"
  if [ "$expected" != "$actual" ]; then
    echo "assertion failed: $message" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    exit 1
  fi
}

reset_worker_env() {
  unset WORKER_PROCESS_COUNT WORKER_MAX_ACTIVE_SANDBOXES SANDBOX_CPU_LIMIT SANDBOX_MEMORY_LIMIT_MB SANDBOX_DISK_LIMIT_GB WORKER_BUCKET_MAP WORKER_DEFAULT_BUCKET
}

test_bucket_map_uses_bucket_then_host_with_colon_separator() {
  reset_worker_env
  WORKER_BUCKET_MAP="hcloud-ccx23:87.99.158.39,hcloud-cpx31:10.0.0.5"
  apply_worker_bucket_overrides worker "87.99.158.39"
  assert_eq "4" "${WORKER_PROCESS_COUNT:-}" "bucket map should match bucket:host entries"
  assert_eq "4" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "bucket map should set the matching machine's sandbox cap"
}

test_bucket_map_does_not_override_other_hosts() {
  reset_worker_env
  WORKER_BUCKET_MAP="hcloud-cpx31:87.99.158.39"
  apply_worker_bucket_overrides worker "87.99.158.40"
  assert_eq "2" "${WORKER_PROCESS_COUNT:-}" "unmapped hosts should keep the builtin default bucket"
  assert_eq "2" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "unmapped hosts should keep the builtin default sandbox cap"
}

test_bucket_map_preserves_explicit_worker_process_count() {
  reset_worker_env
  WORKER_BUCKET_MAP="hcloud-ccx23:87.99.158.39"
  WORKER_PROCESS_COUNT="9"
  apply_worker_bucket_overrides worker "87.99.158.39"
  assert_eq "9" "${WORKER_PROCESS_COUNT:-}" "explicit WORKER_PROCESS_COUNT should win over bucket defaults"
  assert_eq "4" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "sandbox cap should still follow the machine bucket when not explicitly set"
}

test_bucket_map_preserves_explicit_worker_max_active_sandboxes() {
  reset_worker_env
  WORKER_BUCKET_MAP="hcloud-ccx23:87.99.158.39"
  WORKER_MAX_ACTIVE_SANDBOXES="6"
  apply_worker_bucket_overrides worker "87.99.158.39"
  assert_eq "4" "${WORKER_PROCESS_COUNT:-}" "worker process count should still follow the machine bucket"
  assert_eq "6" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "explicit WORKER_MAX_ACTIVE_SANDBOXES should win over bucket defaults"
}

test_bucket_map_sets_sandbox_defaults() {
  reset_worker_env
  WORKER_BUCKET_MAP="hcloud-cpx31:87.99.158.39"
  apply_worker_bucket_overrides worker "87.99.158.39"
  assert_eq "2" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "worker bucket overrides should set default live sandbox capacity"
  assert_eq "2" "${SANDBOX_CPU_LIMIT:-}" "worker bucket overrides should set default sandbox CPU"
  assert_eq "3072" "${SANDBOX_MEMORY_LIMIT_MB:-}" "worker bucket overrides should set default sandbox memory"
  assert_eq "10" "${SANDBOX_DISK_LIMIT_GB:-}" "worker bucket overrides should set default sandbox disk"
}

test_bucket_map_ignores_non_worker_roles() {
  reset_worker_env
  WORKER_BUCKET_MAP="hcloud-cpx31:87.99.158.39"
  apply_worker_bucket_overrides app "87.99.158.39"
  assert_eq "" "${WORKER_PROCESS_COUNT:-}" "non-worker roles should not receive worker overrides"
  assert_eq "" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "non-worker roles should not receive live sandbox capacity overrides"
}

test_bucket_map_uses_default_bucket_when_set() {
  reset_worker_env
  WORKER_DEFAULT_BUCKET="hcloud-ccx23"
  apply_worker_bucket_overrides worker "87.99.158.40"
  assert_eq "4" "${WORKER_PROCESS_COUNT:-}" "default bucket should still work for unmapped workers"
  assert_eq "4" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "default bucket should set live sandbox capacity for unmapped workers"
}

test_bucket_map_uses_builtin_fallback_without_mapping() {
  reset_worker_env
  apply_worker_bucket_overrides worker "87.99.158.40"
  assert_eq "2" "${WORKER_PROCESS_COUNT:-}" "builtin default bucket should apply when no mapping is configured"
  assert_eq "2" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "builtin default bucket should set live sandbox capacity when no mapping is configured"
}

test_builtin_bucket_counts_follow_reserved_ram_rule() {
  local cases=(
    "hcloud-cpx11:1"
    "hcloud-cpx21:1"
    "hcloud-cpx31:2"
    "hcloud-cpx41:4"
    "hcloud-cpx51:9"
    "hcloud-ccx13:2"
    "hcloud-ccx23:4"
    "hcloud-ccx33:8"
    "hcloud-ccx43:16"
    "hcloud-ccx53:32"
    "hcloud-ccx63:48"
    "ec2-t3.xlarge:4"
    "ec2-c6i.2xlarge:4"
    "ec2-c6i.4xlarge:9"
  )

  local case bucket expected
  for case in "${cases[@]}"; do
    reset_worker_env
    bucket="${case%%:*}"
    expected="${case#*:}"
    WORKER_DEFAULT_BUCKET="$bucket"
    apply_worker_bucket_overrides worker "87.99.158.40"
    assert_eq "$expected" "${WORKER_PROCESS_COUNT:-}" "$bucket should follow the reserved RAM sizing rule"
    assert_eq "$expected" "${WORKER_MAX_ACTIVE_SANDBOXES:-}" "$bucket live sandbox cap should follow the reserved RAM sizing rule"
  done
}

main() {
  test_bucket_map_uses_bucket_then_host_with_colon_separator
  test_bucket_map_does_not_override_other_hosts
  test_bucket_map_preserves_explicit_worker_process_count
  test_bucket_map_sets_sandbox_defaults
  test_bucket_map_ignores_non_worker_roles
  test_bucket_map_uses_default_bucket_when_set
  test_bucket_map_uses_builtin_fallback_without_mapping
  test_bucket_map_preserves_explicit_worker_max_active_sandboxes
  test_builtin_bucket_counts_follow_reserved_ram_rule
}

main "$@"
