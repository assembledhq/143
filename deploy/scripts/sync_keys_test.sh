#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rc=$?; if [ "$rc" -ne 0 ]; then echo "--- dry-run output ---" >&2; cat "$DRY_OUTPUT" >&2 2>/dev/null || true; echo "--- apply output ---" >&2; cat "$APPLY_OUTPUT" >&2 2>/dev/null || true; echo "--- ssh capture ---" >&2; cat "$CAPTURE_FILE" >&2 2>/dev/null || true; fi; rm -rf "$TMP_DIR"; exit "$rc"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

SECRETS_DIR="$TMP_DIR/143-infra"
KEYS_DIR="$SECRETS_DIR/deploy/authorized_keys"
mkdir -p "$KEYS_DIR"
printf 'ssh-ed25519 BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB deploy-b\n' >"$KEYS_DIR/b.pub"
printf 'ssh-ed25519 AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA deploy-a\n' >"$KEYS_DIR/a.pub"

REMOTE_KEYS_FILE="$TMP_DIR/remote_authorized_keys"
APPLIED_KEYS_FILE="$TMP_DIR/applied_authorized_keys"
CAPTURE_FILE="$TMP_DIR/ssh.capture"
DRY_OUTPUT="$TMP_DIR/dry.out"
APPLY_OUTPUT="$TMP_DIR/apply.out"
printf 'ssh-ed25519 OLD deploy-old\n' >"$REMOTE_KEYS_FILE"
: >"$APPLIED_KEYS_FILE"
: >"$CAPTURE_FILE"

STUB_DIR="$TMP_DIR/stubs"
mkdir -p "$STUB_DIR"
cat >"$STUB_DIR/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

{
  printf 'ARGS:'
  printf ' %q' "$@"
  printf '\n'
} >>"${SSH_CAPTURE_FILE:?}"

case "$*" in
  *"cat ~/.ssh/authorized_keys"*)
    cat "${SSH_REMOTE_KEYS_FILE:?}"
    ;;
  *"mkdir -p ~/.ssh"*)
    cat >"${SSH_APPLIED_KEYS_FILE:?}"
    ;;
  *)
    echo "unexpected ssh invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$STUB_DIR/ssh"

(
  export PATH="$STUB_DIR:$PATH"
  export SECRETS_DIR
  export SSH_CAPTURE_FILE="$CAPTURE_FILE"
  export SSH_REMOTE_KEYS_FILE="$REMOTE_KEYS_FILE"
  export SSH_APPLIED_KEYS_FILE="$APPLIED_KEYS_FILE"
  "$SCRIPT_DIR/sync-keys.sh" "$TMP_DIR/fake-key" 203.0.113.10 >"$DRY_OUTPUT" 2>&1
)

grep -Fq "Found 2 key(s) in $KEYS_DIR" "$DRY_OUTPUT" \
  || fail "dry-run should report the private authorized keys directory"
grep -Fq "DRY RUN" "$DRY_OUTPUT" \
  || fail "dry-run should not apply changes"
[ ! -s "$APPLIED_KEYS_FILE" ] \
  || fail "dry-run should not write authorized_keys"

(
  export PATH="$STUB_DIR:$PATH"
  export SECRETS_DIR
  export SSH_CAPTURE_FILE="$CAPTURE_FILE"
  export SSH_REMOTE_KEYS_FILE="$REMOTE_KEYS_FILE"
  export SSH_APPLIED_KEYS_FILE="$APPLIED_KEYS_FILE"
  "$SCRIPT_DIR/sync-keys.sh" --apply "$TMP_DIR/fake-key" 203.0.113.10 >"$APPLY_OUTPUT" 2>&1
)

EXPECTED_KEYS="$TMP_DIR/expected_authorized_keys"
{
  printf 'ssh-ed25519 AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA deploy-a\n'
  printf 'ssh-ed25519 BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB deploy-b\n'
} >"$EXPECTED_KEYS"
diff -u "$EXPECTED_KEYS" "$APPLIED_KEYS_FILE" \
  || fail "apply should write sorted keys from the private authorized keys directory"
grep -Fq "All keys synced." "$APPLY_OUTPUT" \
  || fail "apply should report successful key sync"
grep -Fq "deploy@203.0.113.10" "$CAPTURE_FILE" \
  || fail "ssh should target the requested host"

echo "PASS: sync-keys reads deploy authorized keys from SECRETS_DIR"
