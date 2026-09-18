#!/usr/bin/env bash
set -euo pipefail

# Tests for sync-keys.sh: SECRETS_DIR key loading, role:host targeting,
# SSH failures, and the egress bootstrap-key lockout guard.
# Run directly: bash deploy/scripts/sync_keys_test.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rc=$?; if [ "$rc" -ne 0 ]; then echo "--- last output ---" >&2; cat "$LAST_OUTPUT" >&2 2>/dev/null || true; echo "--- ssh capture ---" >&2; cat "$CAPTURE_FILE" >&2 2>/dev/null || true; fi; rm -rf "$TMP_DIR"; exit "$rc"' EXIT

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
LAST_OUTPUT="$TMP_DIR/last.out"
printf 'ssh-ed25519 OLD deploy-old\n' >"$REMOTE_KEYS_FILE"
: >"$APPLIED_KEYS_FILE"
: >"$CAPTURE_FILE"
: >"$LAST_OUTPUT"

STUB_DIR="$TMP_DIR/stubs"
mkdir -p "$STUB_DIR"

write_ssh_stub() {
  local mode="$1"
  cat >"$STUB_DIR/ssh" <<EOF
#!/usr/bin/env bash
set -euo pipefail

{
  printf 'ARGS:'
  printf ' %q' "\$@"
  printf '\\n'
} >>"\${SSH_CAPTURE_FILE:?}"

case "\$*" in
  *true)
    if [ "${mode}" = "probe-ubuntu" ]; then
      case "\$*" in
        *root@*) exit 255 ;;
        *ubuntu@*) exit 0 ;;
        *) echo "unexpected probe: \$*" >&2; exit 1 ;;
      esac
    fi
    echo "unexpected ssh true: \$*" >&2
    exit 1
    ;;
  *"cat ~/.ssh/authorized_keys"*)
    if [ "${mode}" = "deny" ]; then
      echo "deploy@host: Permission denied (publickey)." >&2
      exit 255
    fi
    cat "\${SSH_REMOTE_KEYS_FILE:?}"
    ;;
  *"mkdir -p ~/.ssh"*)
    if [ "${mode}" = "apply-fail" ]; then
      echo "deploy@host: Permission denied (publickey)." >&2
      exit 255
    fi
    cat >"\${SSH_APPLIED_KEYS_FILE:?}"
    ;;
  *)
    echo "unexpected ssh invocation: \$*" >&2
    exit 1
    ;;
esac
EOF
  chmod +x "$STUB_DIR/ssh"
}

run_sync() {
  local mode="$1"
  shift
  write_ssh_stub "$mode"
  : >"$CAPTURE_FILE"
  : >"$APPLIED_KEYS_FILE"

  local extra_env=()
  local args=()
  for token in "$@"; do
    if [ ${#args[@]} -eq 0 ] && [[ "$token" == [A-Za-z_][A-Za-z0-9_]*=* ]]; then
      extra_env+=("$token")
    else
      args+=("$token")
    fi
  done

  env PATH="$STUB_DIR:$PATH" \
    SECRETS_DIR="$SECRETS_DIR" \
    SSH_CAPTURE_FILE="$CAPTURE_FILE" \
    SSH_REMOTE_KEYS_FILE="$REMOTE_KEYS_FILE" \
    SSH_APPLIED_KEYS_FILE="$APPLIED_KEYS_FILE" \
    "${extra_env[@]}" \
    "$SCRIPT_DIR/sync-keys.sh" "${args[@]}" >"$LAST_OUTPUT" 2>&1
}

EXPECTED_KEYS="$TMP_DIR/expected_authorized_keys"
{
  printf 'ssh-ed25519 AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA deploy-a\n'
  printf 'ssh-ed25519 BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB deploy-b\n'
} >"$EXPECTED_KEYS"

# 1. Dry-run against a bare host uses deploy@ and does not write keys.
run_sync default "$TMP_DIR/fake-key" 203.0.113.10
grep -Fq "Found 2 key(s) in $KEYS_DIR" "$LAST_OUTPUT" \
  || fail "dry-run should report the private authorized keys directory"
grep -Fq "DRY RUN" "$LAST_OUTPUT" \
  || fail "dry-run should not apply changes"
grep -Fq "deploy@203.0.113.10" "$CAPTURE_FILE" \
  || fail "bare host should SSH as deploy@"
[ ! -s "$APPLIED_KEYS_FILE" ] \
  || fail "dry-run should not write authorized_keys"

# 2. Apply writes sorted keys from the private authorized keys directory.
run_sync default --apply "$TMP_DIR/fake-key" 203.0.113.10
diff -u "$EXPECTED_KEYS" "$APPLIED_KEYS_FILE" \
  || fail "apply should write sorted keys from the private authorized keys directory"
grep -Fq "All keys synced." "$LAST_OUTPUT" \
  || fail "apply should report successful key sync"
grep -Fq "deploy@203.0.113.10" "$CAPTURE_FILE" \
  || fail "ssh should target the requested host"

# 3. worker:<host> still uses deploy@ and the fleet key.
run_sync default --apply "$TMP_DIR/fake-key" worker:203.0.113.10
grep -Fq "deploy@203.0.113.10" "$CAPTURE_FILE" \
  || fail "worker:host should SSH as deploy@"
grep -Fq "(worker)" "$LAST_OUTPUT" \
  || fail "worker:host should label the role"
grep -Fq -- "-i $TMP_DIR/fake-key" "$CAPTURE_FILE" \
  || fail "worker:host should use the fleet SSH key"

# 4. egress:<host> uses ubuntu@ and EGRESS_SSH_KEY.
run_sync default \
  EGRESS_SSH_USER=ubuntu \
  EGRESS_SSH_KEY="$TMP_DIR/egress-fake" \
  --apply "$TMP_DIR/fake-key" egress:203.0.113.99
grep -Fq "ubuntu@203.0.113.99" "$CAPTURE_FILE" \
  || fail "egress:host should SSH as ubuntu@"
grep -Fq "(egress)" "$LAST_OUTPUT" \
  || fail "egress:host should label the role"
grep -Fq -- "-i $TMP_DIR/egress-fake" "$CAPTURE_FILE" \
  || fail "egress:host should use EGRESS_SSH_KEY"
grep -Fq "deploy@203.0.113.99" "$CAPTURE_FILE" \
  && fail "egress:host should not SSH as deploy@"

# 5. Without EGRESS_SSH_USER, probe root then ubuntu.
run_sync probe-ubuntu \
  EGRESS_SSH_KEY="$TMP_DIR/egress-fake" \
  --apply "$TMP_DIR/fake-key" egress:203.0.113.99
grep -Fq "root@203.0.113.99" "$CAPTURE_FILE" \
  || fail "egress probe should try root first"
grep -Fq "ubuntu@203.0.113.99" "$CAPTURE_FILE" \
  || fail "egress probe should fall back to ubuntu"

# 6. SSH auth failure in dry-run is an error, not an empty-key diff.
if run_sync deny "$TMP_DIR/fake-key" 203.0.113.10; then
  fail "dry-run should exit nonzero when SSH fails"
fi
grep -Fq "ERROR: SSH failed for deploy@203.0.113.10." "$LAST_OUTPUT" \
  || fail "dry-run should report the SSH failure"
if grep -Fq "deploy-a" "$LAST_OUTPUT"; then
  fail "failed SSH should not fake an empty authorized_keys diff"
fi
grep -Fq "Dry run complete" "$LAST_OUTPUT" \
  && fail "dry-run should not report success when a host failed"

# 7. Apply SSH failure is captured (does not abort via set -e) and exits 1.
if run_sync apply-fail --apply "$TMP_DIR/fake-key" 203.0.113.10; then
  fail "apply should exit nonzero when SSH apply fails"
fi
grep -Fq "ERROR: failed to apply keys on deploy@203.0.113.10." "$LAST_OUTPUT" \
  || fail "apply should report the failed host instead of aborting silently"
grep -Fq "All keys synced." "$LAST_OUTPUT" \
  && fail "apply should not report success when a host failed"

# 8. Refuses to replace egress authorized_keys if the bootstrap private key
# is missing from the 143-infra set (would lock out EGRESS_SSH_KEY).
ssh-keygen -t ed25519 -f "$TMP_DIR/egress-real" -N "" -C "143-john" -q
if run_sync default \
  EGRESS_SSH_USER=ubuntu \
  EGRESS_SSH_KEY="$TMP_DIR/egress-real" \
  --apply "$TMP_DIR/fake-key" egress:203.0.113.99; then
  fail "apply should refuse when the egress bootstrap pubkey is missing from KEYS_DIR"
fi
grep -Fq "would lock out the gateway bootstrap key" "$LAST_OUTPUT" \
  || fail "apply should explain the egress lockout guard"
[ ! -s "$APPLIED_KEYS_FILE" ] \
  || fail "lockout guard should not write authorized_keys"

# After the bootstrap pubkey is added, apply proceeds.
cp "$TMP_DIR/egress-real.pub" "$KEYS_DIR/143-egress.pub"
{
  printf 'ssh-ed25519 AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA deploy-a\n'
  printf 'ssh-ed25519 BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB deploy-b\n'
  cat "$TMP_DIR/egress-real.pub"
} | sort >"$TMP_DIR/expected_with_egress"
run_sync default \
  EGRESS_SSH_USER=ubuntu \
  EGRESS_SSH_KEY="$TMP_DIR/egress-real" \
  --apply "$TMP_DIR/fake-key" egress:203.0.113.99
diff -u "$TMP_DIR/expected_with_egress" "$APPLIED_KEYS_FILE" \
  || fail "apply should write fleet keys plus the egress bootstrap pubkey"

# 9. 143-egress.pub is gateway-only and must not be installed on fleet nodes.
run_sync default --apply "$TMP_DIR/fake-key" 203.0.113.10
grep -Fq "143-egress.pub is gateway-only" "$LAST_OUTPUT" \
  || fail "should warn that 143-egress.pub is not installed on fleet nodes"
diff -u "$EXPECTED_KEYS" "$APPLIED_KEYS_FILE" \
  || fail "fleet apply should omit 143-egress.pub"
grep -Fq "143-john" "$APPLIED_KEYS_FILE" \
  && fail "fleet authorized_keys should not contain the gateway bootstrap key"

# Contract: provision-egress installs keys via the same script.
grep -Fq 'sync-keys.sh' "$SCRIPT_DIR/provision-egress.sh" \
  || fail "provision-egress.sh should call sync-keys.sh"
grep -Fq 'egress:$HOST' "$SCRIPT_DIR/provision-egress.sh" \
  || fail "provision-egress.sh should pass egress:\$HOST to sync-keys"

echo "PASS: sync-keys reads deploy authorized keys from SECRETS_DIR"
