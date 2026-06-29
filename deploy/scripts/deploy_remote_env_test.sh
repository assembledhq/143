#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rc=$?; if [ "$rc" -ne 0 ]; then echo "--- deploy output ---" >&2; if [ -n "${OUTPUT_FILE:-}" ]; then cat "$OUTPUT_FILE" >&2 2>/dev/null || true; fi; echo "--- ssh capture ---" >&2; if [ -n "${CAPTURE_FILE:-}" ]; then cat "$CAPTURE_FILE" >&2 2>/dev/null || true; fi; fi; rm -rf "$TMP_DIR"; exit "$rc"' EXIT

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

{
  printf 'ARGS:'
  printf ' %q' "$@"
  printf '\n'
} >>"$capture_file"

# Consume stdin for heredoc callers.
stdin="$(cat)"

remote_start=0
index=1
for arg in "$@"; do
  if [ "$remote_start" -eq 1 ]; then
    break
  fi
  case "$arg" in
    deploy@*) remote_start=$((index + 1)) ;;
  esac
  index=$((index + 1))
done

if [ "$remote_start" -le "$#" ]; then
  remote_args=("${@:$remote_start}")
  if [ "${#remote_args[@]}" -gt 0 ]; then
    remote_command=""
    for arg in "${remote_args[@]}"; do
      if [ -n "$remote_command" ]; then
        remote_command="$remote_command "
      fi
      remote_command="$remote_command$arg"
    done
    printf 'REMOTE:%s\n' "$remote_command" >>"$capture_file"

    last_index=$((${#remote_args[@]} - 1))
    if [ "${remote_args[$last_index]}" = "bash" ]; then
      probe_command="${remote_command% bash} printf remote-command-ok"
      bash -c "$probe_command" >/dev/null
    fi
  fi
fi

printf '%s' "$stdin" >/dev/null
exit 0
EOF
chmod +x "$STUB_DIR/ssh"

HOME_DIR="$TMP_DIR/home"
mkdir -p "$HOME_DIR/.config/sops/age"
printf 'AGE-SECRET-KEY-test\n' >"$HOME_DIR/.config/sops/age/keys.txt"

# deploy.sh resolves the encrypted bundle from SECRETS_DIR and checks it exists
# on disk before decrypting (sops is stubbed). Point it at a temp stub so the
# test is hermetic — without this it needs a real ../143-infra sibling checkout.
SECRETS_DIR="$TMP_DIR/secrets"
mkdir -p "$SECRETS_DIR"
printf 'sops-stub\n' >"$SECRETS_DIR/.env.production.enc"
export SECRETS_DIR

CAPTURE_FILE="$TMP_DIR/deploy.capture"
: >"$CAPTURE_FILE"
OUTPUT_FILE="$TMP_DIR/deploy.out"

(
  export HOME="$HOME_DIR"
  export PATH="$STUB_DIR:$PATH"
  export SOPS_STUB_OUTPUT=$'DB_PASSWORD=db-secret\nDB_HOST=10.0.0.3\nVICTORIALOGS_HOST=10.0.0.9\nCLOUDFLARE_API_TOKEN=cf-secret'
  export SSH_CAPTURE_FILE="$CAPTURE_FILE"
  export DB_PASSWORD="db-secret"
  export DB_HOST="10.0.0.3"
  export VICTORIALOGS_HOST="10.0.0.9"
  export CLOUDFLARE_API_TOKEN="cf-secret"
  unset DEPLOY_REASON

  bash "$ROOT_DIR/deploy/scripts/deploy.sh" app 127.0.0.1 "$TMP_DIR/fake-key" >"$OUTPUT_FILE" 2>&1
)

grep -Fq "DEPLOY_REASON=\\'routine\\ worker\\ rollout\\'" "$CAPTURE_FILE"
grep -q "REMOTE:.*DEPLOY_REASON='routine worker rollout' .* bash" "$CAPTURE_FILE"

printf 'deploy remote env quoting test passed\n'
