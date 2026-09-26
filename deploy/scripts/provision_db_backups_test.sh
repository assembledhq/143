#!/usr/bin/env bash
# Exercise schedule configuration forwarding without contacting a host or AWS.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/secrets"
cat > "$TEST_ROOT/bin/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FAKE_SSH_LOG:?}"
EOF
cat > "$TEST_ROOT/bin/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit 0
EOF
cat > "$TEST_ROOT/bin/sops" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' 'FLEET_HOSTS=db:example.invalid' 'BACKUP_ENABLED=false' 'RESTORE_TEST_ENABLED=false'
EOF
chmod +x "$TEST_ROOT/bin/ssh" "$TEST_ROOT/bin/scp" "$TEST_ROOT/bin/sops"
export FAKE_SSH_LOG="$TEST_ROOT/ssh.log"
export PATH="$TEST_ROOT/bin:$PATH"
export BACKUP_S3_BUCKET=''
export BACKUP_ENABLED=''
export RESTORE_TEST_ENABLED=''

for setting in BACKUP_ENABLED RESTORE_TEST_ENABLED; do
  : > "$FAKE_SSH_LOG"
  if env "$setting=false; unexpected" bash "$SCRIPT_DIR/provision-db-backups.sh" example.invalid >/dev/null 2>&1; then
    fail "invalid $setting must fail"
  fi
  [ ! -s "$FAKE_SSH_LOG" ] || fail "invalid $setting must fail before any SSH action"
done

: > "$FAKE_SSH_LOG"
BACKUP_ENABLED=true RESTORE_TEST_ENABLED=false bash "$SCRIPT_DIR/provision-db-backups.sh" example.invalid >/dev/null
grep -Fq "BACKUP_ENABLED='true' RESTORE_TEST_ENABLED='false' /opt/143/deploy/scripts/install-pg-backups.sh" "$FAKE_SSH_LOG" || fail "wrapper must forward both switches"

: > "$FAKE_SSH_LOG"
bash "$SCRIPT_DIR/provision-db-backups.sh" example.invalid >/dev/null
grep -Fq "BACKUP_ENABLED='' RESTORE_TEST_ENABLED='' /opt/143/deploy/scripts/install-pg-backups.sh" "$FAKE_SSH_LOG" || fail "omitted switches must allow installed settings to survive"

# The standalone Make path must read booleans from private config and allow an
# explicit operator override. All transports/decryption are fake commands.
for override in '' true; do
  : > "$FAKE_SSH_LOG"
  env BACKUP_ENABLED="$override" make --no-print-directory -C "$PROJECT_DIR" provision-db-backups \
    SECRETS_DIR="$TEST_ROOT/secrets" SSH_KEY="$TEST_ROOT/fake-key" HOST=example.invalid >/dev/null
  expected="${override:-false}"
  grep -Fq "BACKUP_ENABLED='$expected' RESTORE_TEST_ENABLED='false' /opt/143/deploy/scripts/install-pg-backups.sh" "$FAKE_SSH_LOG" || fail "Make must preserve private config and explicit overrides"
done
echo 'PASS: provision_db_backups_test.sh'
