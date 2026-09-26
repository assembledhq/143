#!/usr/bin/env bash
# Tests for install-pg-backups.sh: cron-file rendering, idempotency, the
# missing-script guard, and env overrides. Everything is redirected to a
# tempdir (CRON_FILE / *_LOG / SCRIPTS_DIR / BACKUP_DIR overrides), so this
# runs unprivileged and touches nothing real.
# Run directly: bash deploy/scripts/install_pg_backups_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INSTALLER="$SCRIPT_DIR/install-pg-backups.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  # %b so embedded \n in messages (e.g. dumped cron contents) render.
  printf 'FAIL: %b\n' "$*" >&2
  exit 1
}

# Sandbox layout: fake scripts dir + dump dir + cron/log targets.
SCRIPTS="$TMP_DIR/scripts"
mkdir -p "$SCRIPTS"
: > "$SCRIPTS/pg-backup.sh"
: > "$SCRIPTS/restore-test.sh"
: > "$SCRIPTS/restore-test-body.sh"
: > "$SCRIPTS/pg-backup-policy.py"
: > "$SCRIPTS/pg-backup-config.py"
CRON_FILE="$TMP_DIR/143-pg-backup"

# Extra "KEY=val" args (quoted, so values may contain spaces) are forwarded to
# `env` as overrides — env parses them even though a bare assignment prefix
# from "$@" expansion would not.
run_installer() {
  env \
    CRON_FILE="$CRON_FILE" \
    PG_BACKUP_LOG="$TMP_DIR/pg-backup.log" \
    RESTORE_TEST_LOG="$TMP_DIR/restore-test.log" \
    SCRIPTS_DIR="$SCRIPTS" \
    BACKUP_DIR="$TMP_DIR/backups" \
    "$@" \
    bash "$INSTALLER"
}

# 1. Fresh install renders the expected cron file.
out="$(run_installer)"
[ -f "$CRON_FILE" ] || fail "cron file not created"
grep -q '^0 \*/6 \* \* \* root '"$SCRIPTS"'/pg-backup.sh >> '"$TMP_DIR"'/pg-backup.log 2>&1$' "$CRON_FILE" \
  || fail "backup cron line missing/wrong:\n$(cat "$CRON_FILE")"
grep -q '^0 5 \* \* 0 root '"$SCRIPTS"'/restore-test.sh >> '"$TMP_DIR"'/restore-test.log 2>&1$' "$CRON_FILE" \
  || fail "restore-test cron line missing/wrong:\n$(cat "$CRON_FILE")"
if grep -q '^BACKUP_RETENTION_DAYS=' "$CRON_FILE"; then fail 'obsolete age-based retention must not be installed'; fi
grep -q "^BACKUP_DIR=$TMP_DIR/backups$" "$CRON_FILE" || fail "BACKUP_DIR not in cron env"
[ -d "$TMP_DIR/backups" ] || fail "backup dir not created"
[ -f "$TMP_DIR/pg-backup.log" ] || fail "pg-backup log not pre-created"
case "$out" in *"installed $CRON_FILE"*) ;; *) fail "expected install message, got: $out" ;; esac

# 2. Re-run is a no-op (idempotent): same content, "already up to date".
before="$(cat "$CRON_FILE")"
out="$(run_installer)"
[ "$(cat "$CRON_FILE")" = "$before" ] || fail "cron file changed on idempotent re-run"
case "$out" in *"already up to date"*) ;; *) fail "expected up-to-date message, got: $out" ;; esac

# 3. Env overrides flow into the cron file.
out="$(run_installer BACKUP_CRON='30 */4 * * *' BACKUP_RETENTION_DAYS=14)"
grep -q '^30 \*/4 \* \* \* root ' "$CRON_FILE" || fail "custom BACKUP_CRON not applied"
if grep -q '^BACKUP_RETENTION_DAYS=' "$CRON_FILE"; then fail 'legacy retention override must not enable age pruning'; fi

# 4. Holds disable only the selected schedule and survive an omitted override.
run_installer BACKUP_ENABLED=true RESTORE_TEST_ENABLED=false >/dev/null 2>&1
grep -q '^0 \*/6 .*pg-backup.sh' "$CRON_FILE" || fail "restore hold must retain backup schedule"
grep -q '^# DISABLED: 0 5 .*restore-test.sh' "$CRON_FILE" || fail "restore schedule must be commented out"
grep -q '^RESTORE_TEST_ENABLED=false$' "$CRON_FILE" || fail "restore hold must persist as a literal field"
run_installer >/dev/null 2>&1
grep -q '^# DISABLED: .*restore-test.sh' "$CRON_FILE" || fail "reinstallation must preserve restore hold"
run_installer BACKUP_ENABLED=false >/dev/null 2>&1
grep -q '^# DISABLED: .*pg-backup.sh' "$CRON_FILE" || fail "backup hold must disable backup schedule"
grep -q '^# DISABLED: .*restore-test.sh' "$CRON_FILE" || fail "backup hold must preserve restore hold"

# Invalid or duplicate settings must fail without changing the installed cron.
for invalid in 'yes' 'false; echo unsafe' $'false\ntrue'; do
  before="$(cat "$CRON_FILE")"
  if run_installer RESTORE_TEST_ENABLED="$invalid" >/dev/null 2>&1; then
    fail "invalid restore setting must fail"
  fi
  [ "$(cat "$CRON_FILE")" = "$before" ] || fail "invalid setting changed cron"
done
printf 'RESTORE_TEST_ENABLED=true\n' >> "$CRON_FILE"
before="$(cat "$CRON_FILE")"
if run_installer >/dev/null 2>&1; then fail "duplicate installed setting must fail"; fi
[ "$(cat "$CRON_FILE")" = "$before" ] || fail "duplicate setting changed cron"

# Empty installed settings must not silently turn held work back on.
printf 'RESTORE_TEST_ENABLED=\n' > "$CRON_FILE"
before="$(cat "$CRON_FILE")"
if run_installer >/dev/null 2>&1; then fail "empty installed setting must fail"; fi
[ "$(cat "$CRON_FILE")" = "$before" ] || fail "empty setting changed cron"

# Cron-legal hand edits must never be mistaken for an omitted setting.
for noncanonical in ' RESTORE_TEST_ENABLED=false' 'RESTORE_TEST_ENABLED = false'; do
  printf '%s\n' "$noncanonical" > "$CRON_FILE"
  before="$(cat "$CRON_FILE")"
  if run_installer >/dev/null 2>&1; then fail "spaced installed setting must fail closed"; fi
  [ "$(cat "$CRON_FILE")" = "$before" ] || fail "spaced setting changed cron"
done

# Explicit booleans may repair a bad prior field and resume the schedule.
run_installer BACKUP_ENABLED=true RESTORE_TEST_ENABLED=true >/dev/null
grep -q '^0 \*/6 .*pg-backup.sh' "$CRON_FILE" || fail "explicit backup resume must restore its schedule"
grep -q '^0 5 .*restore-test.sh' "$CRON_FILE" || fail "explicit restore resume must restore its schedule"

# 5. Missing backup script is a hard error.
rm -f "$SCRIPTS/restore-test.sh"
if run_installer >/dev/null 2>&1; then
  fail "expected non-zero exit when restore-test.sh is missing"
fi

echo "PASS: install_pg_backups_test.sh"
