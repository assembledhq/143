#!/usr/bin/env bash
# Keep failed browser probes useful without publishing their raw output or paths.
set -uo pipefail
umask 077

if [[ "$#" -ne 1 ]]; then
  printf 'Preview Chrome probe failed: reason=invalid_arguments\n' >&2
  exit 2
fi
if [[ ! -e "$1" ]]; then
  printf 'Preview Chrome probe failed: reason=missing_executable\n' >&2
  exit 127
fi
if [[ ! -f "$1" || ! -x "$1" ]]; then
  printf 'Preview Chrome probe failed: reason=not_executable\n' >&2
  exit 126
fi

if ! probe_dir="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/preview-chrome-probe.XXXXXX" 2>/dev/null)"; then
  printf 'Preview Chrome probe failed: reason=temporary_directory_unavailable\n' >&2
  exit 1
fi
cleanup() {
  local status="$?"
  if ! rm -rf -- "$probe_dir" >/dev/null 2>&1; then
    printf 'Preview Chrome probe cleanup unavailable\n' >&2 || :
  fi
  return "$status"
}
trap cleanup EXIT

started="$SECONDS"
probe_status=0
# Group redirection also captures Bash's own signal-termination diagnostics.
{
  timeout --kill-after=2s 5s "$1" --version || probe_status="$?"
} 2>/dev/null >"$probe_dir/stdout" 2>"$probe_dir/stderr" || probe_status="$?"
elapsed="$((SECONDS - started))"

byte_count() {
  local count
  if count="$(wc -c 2>/dev/null < "$1")"; then
    count="${count//[[:space:]]/}"
    if [[ "$count" =~ ^[0-9]+$ ]]; then
      printf '%s' "$count"
      return
    fi
  fi
  printf 'unavailable'
}
failure() {
  local status="$1" reason="$2"
  printf 'Preview Chrome probe failed: reason=%s exit_status=%s elapsed_seconds=%s stdout_bytes=%s stderr_bytes=%s\n' \
    "$reason" "$status" "$elapsed" "$(byte_count "$probe_dir/stdout")" "$(byte_count "$probe_dir/stderr")" >&2 || :
  exit "$status"
}
if [[ "$probe_status" -ne 0 ]]; then
  failure "$probe_status" probe_exit
fi

# Read to EOF without command substitution: Bash would otherwise warn on NUL
# bytes, and prefix-only validation could publish arbitrary successful output.
version=''
if IFS= read -r -d '' version 2>/dev/null < "$probe_dir/stdout"; then
  failure 1 invalid_identity
fi
if [[ "$version" =~ ^Google\ Chrome\ ([0-9]+(\.[0-9]+){3})[[:space:]]*$ ]]; then
  printf 'Google Chrome %s\n' "${BASH_REMATCH[1]}"
else
  failure 1 invalid_identity
fi
