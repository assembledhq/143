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
case "$*" in
  *pg-backup-config.py\ install*) cat > "${FAKE_CONFIG_INPUT:?}" ;;
esac
EOF
cat > "$TEST_ROOT/bin/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FAKE_SCP_LOG:?}"
exit 0
EOF
cat > "$TEST_ROOT/bin/sops" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' 'FLEET_HOSTS=db:example.invalid' 'BACKUP_ENABLED=false' 'RESTORE_TEST_ENABLED=false'
EOF
chmod +x "$TEST_ROOT/bin/ssh" "$TEST_ROOT/bin/scp" "$TEST_ROOT/bin/sops"
export FAKE_SSH_LOG="$TEST_ROOT/ssh.log"
export FAKE_SCP_LOG="$TEST_ROOT/scp.log"
export FAKE_CONFIG_INPUT="$TEST_ROOT/storage.json"
export PATH="$TEST_ROOT/bin:$PATH"
export BACKUP_S3_BUCKET=''
export BACKUP_S3_REGION=''
export BACKUP_AWS_ACCESS_KEY_ID=''
export BACKUP_AWS_SECRET_ACCESS_KEY=''
export BACKUP_ENABLED=''
export RESTORE_TEST_ENABLED=''

for setting in BACKUP_ENABLED RESTORE_TEST_ENABLED; do
  : > "$FAKE_SSH_LOG"
  if env "$setting=false; unexpected" bash "$SCRIPT_DIR/provision-db-backups.sh" example.invalid >/dev/null 2>&1; then
    fail "invalid $setting must fail"
  fi
  [ ! -s "$FAKE_SSH_LOG" ] || fail "invalid $setting must fail before any SSH action"
done

# Partial storage configuration must fail before touching scripts or cron.
for setting in BACKUP_S3_BUCKET BACKUP_S3_REGION BACKUP_AWS_ACCESS_KEY_ID BACKUP_AWS_SECRET_ACCESS_KEY; do
  : > "$FAKE_SSH_LOG"
  : > "$FAKE_SCP_LOG"
  if env "$setting=partial" bash "$SCRIPT_DIR/provision-db-backups.sh" example.invalid >/dev/null 2>&1; then
    fail "partial storage configuration must fail"
  fi
  [ ! -s "$FAKE_SSH_LOG" ] && [ ! -s "$FAKE_SCP_LOG" ] || fail "partial config must not reach a host"
done

BACKUP_S3_BUCKET=test-backups BACKUP_S3_REGION=us-east-1 \
  BACKUP_AWS_ACCESS_KEY_ID=test-key BACKUP_AWS_SECRET_ACCESS_KEY='literal-$not-shell' \
  bash "$SCRIPT_DIR/provision-db-backups.sh" example.invalid >/dev/null
python3 - "$FAKE_CONFIG_INPUT" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    actual = json.load(f)
assert actual == dict(bucket='test-backups', region='us-east-1', access_key_id='test-key', secret_access_key='literal-$not-shell')
PY
if grep -q 'literal-' "$FAKE_SSH_LOG"; then fail 'credentials must not become shell command arguments'; fi
for helper in pg-backup.sh restore-test.sh restore-test-body.sh pg-backup-policy.py pg-backup-config.py pg_backup_state.py pg_backup_runtime.py pg_backup_health.py install-pg-backups.sh; do
  grep -Fq "$helper" "$FAKE_SCP_LOG" || fail "missing installed helper $helper"
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
