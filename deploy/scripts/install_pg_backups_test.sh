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
grep -q '^BACKUP_RETENTION_DAYS=7$' "$CRON_FILE" || fail "default retention not 7"
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
grep -q '^BACKUP_RETENTION_DAYS=14$' "$CRON_FILE" || fail "custom retention not applied"

# 4. Missing backup script is a hard error.
rm -f "$SCRIPTS/restore-test.sh"
if run_installer >/dev/null 2>&1; then
  fail "expected non-zero exit when restore-test.sh is missing"
fi

echo "PASS: install_pg_backups_test.sh"
