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
capture_file="${SCP_CAPTURE_FILE:?}"
{
  printf 'ARGS:'
  printf ' %q' "$@"
  printf '\n'
} >>"$capture_file"
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

{
  printf 'ARGS:'
  printf ' %q' "$@"
  printf '\n'
} >>"$capture_file"

stdin_file="${SSH_STDIN_DIR:?}/call-${call_number}.stdin"
cat >"$stdin_file"
printf 'STDIN:%s\n' "$stdin_file" >>"$capture_file"

exit 0
EOF
chmod +x "$STUB_DIR/ssh"

HOME_DIR="$TMP_DIR/home"
mkdir -p "$HOME_DIR/.config/sops/age"
printf 'AGE-SECRET-KEY-test\n' >"$HOME_DIR/.config/sops/age/keys.txt"

# deploy.sh resolves the encrypted bundle from SECRETS_DIR (the private
# secrets checkout); stage a stub so the sops-stub decrypt path runs.
SECRETS_DIR="$TMP_DIR/secrets"
mkdir -p "$SECRETS_DIR"
printf 'sops-stub\n' >"$SECRETS_DIR/.env.production.enc"
export SECRETS_DIR

CAPTURE_FILE="$TMP_DIR/deploy.capture"
: >"$CAPTURE_FILE"
STDIN_DIR="$TMP_DIR/stdin"
mkdir -p "$STDIN_DIR"
COUNTER_FILE="$TMP_DIR/deploy.counter"
printf '1\n' >"$COUNTER_FILE"
OUTPUT_FILE="$TMP_DIR/deploy.out"

(
  export HOME="$HOME_DIR"
  export PATH="$STUB_DIR:$PATH"
  export SOPS_STUB_OUTPUT=$'REDIS_PASSWORD=redis-secret\nREDIS_PRIVATE_IP=10.0.0.50'
  export SSH_CAPTURE_FILE="$CAPTURE_FILE"
  export SSH_STDIN_DIR="$STDIN_DIR"
  export SSH_COUNTER_FILE="$COUNTER_FILE"
  export SCP_CAPTURE_FILE="$CAPTURE_FILE"
  export REDIS_PASSWORD="redis-secret"
  export REDIS_PRIVATE_IP="10.0.0.50"

  bash "$ROOT_DIR/deploy/scripts/deploy.sh" redis 127.0.0.1 "$TMP_DIR/fake-key" >"$OUTPUT_FILE" 2>&1
)

grep -R -q "REDIS_PASSWORD=redis-secret" "$STDIN_DIR"
grep -R -q "REDIS_PRIVATE_IP=10.0.0.50" "$STDIN_DIR"
grep -q "docker-compose.redis.yml" "$CAPTURE_FILE"

printf 'redis deploy test passed\n'
