#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

STUB_DIR="$TMP_DIR/stubs"
mkdir -p "$STUB_DIR"

cat >"$STUB_DIR/sops" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${SOPS_STUB_OUTPUT:-}"
EOF
chmod +x "$STUB_DIR/sops"

cat >"$STUB_DIR/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit 0
EOF
chmod +x "$STUB_DIR/scp"

cat >"$STUB_DIR/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

capture_file="${SSH_CAPTURE_FILE:?}"
counter_file="${SSH_COUNTER_FILE:?}"
call_number="$(cat "$counter_file")"
next_call_number=$((call_number + 1))
printf '%s\n' "$next_call_number" >"$counter_file"

printf 'ARGS:'
printf ' %q' "$@"
printf '\n' >>"$capture_file"

stdin_file="${SSH_STDIN_DIR:?}/call-${call_number}.stdin"
cat >"$stdin_file"
printf 'STDIN:%s\n' "$stdin_file" >>"$capture_file"

if printf '%s\n' "$@" | grep -q "docker compose -f"; then
  exit 0
fi

exit 0
EOF
chmod +x "$STUB_DIR/ssh"

run_case() {
  local script_path="$1"
  local expected_warning_url="$2"
  local expected_critical_url="$3"

  local home_dir="$TMP_DIR/home"
  mkdir -p "$home_dir/.config/sops/age"
  printf 'AGE-SECRET-KEY-test\n' >"$home_dir/.config/sops/age/keys.txt"

  # deploy.sh resolves the encrypted bundle from SECRETS_DIR (the private
  # secrets checkout); stage a stub so the sops-stub decrypt path runs.
  local secrets_dir="$TMP_DIR/secrets"
  mkdir -p "$secrets_dir"
  printf 'sops-stub\n' >"$secrets_dir/.env.production.enc"

  local capture_file="$TMP_DIR/$(basename "$script_path").capture"
  : >"$capture_file"
  local stdin_dir="$TMP_DIR/$(basename "$script_path").stdin"
  mkdir -p "$stdin_dir"
  local counter_file="$TMP_DIR/$(basename "$script_path").counter"
  printf '1\n' >"$counter_file"

  local output_file="$TMP_DIR/$(basename "$script_path").out"

  (
    export HOME="$home_dir"
    export SECRETS_DIR="$secrets_dir"
    export PATH="$STUB_DIR:$PATH"
    export SOPS_STUB_OUTPUT=$'GRAFANA_ADMIN_PASSWORD=admin-secret\nVICTORIALOGS_HOST=10.0.0.9'
    export SSH_CAPTURE_FILE="$capture_file"
    export SSH_STDIN_DIR="$stdin_dir"
    export SSH_COUNTER_FILE="$counter_file"
    export GRAFANA_ADMIN_PASSWORD="admin-secret"
    export VICTORIALOGS_HOST="10.0.0.9"
    unset GRAFANA_ALERTS_WARNING_WEBHOOK_URL
    unset GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL

    bash "$script_path" logging 127.0.0.1 "$TMP_DIR/fake-key" >"$output_file" 2>&1
  )

  grep -R -q "GRAFANA_ALERTS_WARNING_WEBHOOK_URL=${expected_warning_url}" "$stdin_dir"
  grep -R -q "GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL=${expected_critical_url}" "$stdin_dir"
}

run_case \
  "$ROOT_DIR/deploy/scripts/deploy.sh" \
  "http://localhost:65535/disabled-warning" \
  "http://localhost:65535/disabled-critical"

grep -q 'GRAFANA_ALERTS_WARNING_WEBHOOK_URL="${GRAFANA_ALERTS_WARNING_WEBHOOK_URL:-\$DISABLED_WARNING_WEBHOOK_URL}"' \
  "$ROOT_DIR/deploy/scripts/provision.sh"
grep -q 'GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL="${GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL:-\$DISABLED_CRITICAL_WEBHOOK_URL}"' \
  "$ROOT_DIR/deploy/scripts/provision.sh"

printf 'logging webhook fallback test passed\n'
