#!/usr/bin/env bash
# Tests for pg-backup.sh: failed or invalid dumps never become retained local
# backups, expired backups are pruned before a new dump starts, and verified
# dumps are promoted atomically from a temporary file.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BACKUP_SCRIPT="$SCRIPT_DIR/pg-backup.sh"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

FAKE_BIN="$TEST_ROOT/bin"
mkdir -p "$FAKE_BIN"

cat > "$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case " $* " in
  *" pg_dump "*)
    printf '%s' "${FAKE_DUMP_CONTENT:-test dump}"
    exit "${FAKE_PG_DUMP_EXIT:-0}"
    ;;
  *" pg_restore --list "*)
    cat > "${FAKE_RESTORE_INPUT:?FAKE_RESTORE_INPUT is required}"
    exit "${FAKE_PG_RESTORE_EXIT:-0}"
    ;;
  *)
    printf 'unexpected docker invocation: %s\n' "$*" >&2
    exit 99
    ;;
esac
EOF
chmod +x "$FAKE_BIN/docker"

new_case_dir() {
  local name="$1"
  local case_dir="$TEST_ROOT/$name"
  mkdir -p "$case_dir/backups"
  printf '%s\n' "$case_dir"
}

run_backup() {
  local case_dir="$1"
  shift
  env \
    PATH="$FAKE_BIN:$PATH" \
    BACKUP_DIR="$case_dir/backups" \
    BACKUP_RETENTION_DAYS=7 \
    DB_PASSWORD=test-password \
    BACKUP_SYNC_ENV="$case_dir/missing-sync.env" \
    FAKE_RESTORE_INPUT="$case_dir/restore-input" \
    "$@" \
    bash "$BACKUP_SCRIPT"
}

assert_no_dump_artifacts() {
  local case_dir="$1"
  if find "$case_dir/backups" -maxdepth 1 -type f \( -name '*.dump' -o -name '*.partial.*' \) | grep -q .; then
    find "$case_dir/backups" -maxdepth 1 -type f -print >&2
    fail "failed backup should not leave dump artifacts"
  fi
}

# A pg_dump failure must remove the bytes already streamed to the temporary
# file. Retention must also run before pg_dump, even though the new dump fails.
case_dir="$(new_case_dir pg-dump-failure)"
old_dump="$case_dir/backups/onefortythree-old.dump"
stale_partial="$case_dir/backups/.onefortythree-old.dump.partial.abandoned"
printf 'expired backup' > "$old_dump"
printf 'abandoned partial backup' > "$stale_partial"
touch -t 202001010000 "$old_dump" "$stale_partial"
if run_backup "$case_dir" FAKE_DUMP_CONTENT='partial dump bytes' FAKE_PG_DUMP_EXIT=1 >/dev/null 2>&1; then
  fail "pg_dump failure should return non-zero"
fi
assert_no_dump_artifacts "$case_dir"

# A dump that fails pg_restore verification must likewise disappear and never
# be published under the final .dump name.
case_dir="$(new_case_dir verification-failure)"
if run_backup "$case_dir" FAKE_DUMP_CONTENT='invalid dump bytes' FAKE_PG_RESTORE_EXIT=1 >/dev/null 2>&1; then
  fail "verification failure should return non-zero"
fi
assert_no_dump_artifacts "$case_dir"

# A verified dump is atomically promoted to the final name, with no temporary
# artifact left behind. Verification must receive the complete dump bytes.
case_dir="$(new_case_dir success)"
output="$(run_backup "$case_dir" FAKE_DUMP_CONTENT='verified dump bytes')"
dump_count="$(find "$case_dir/backups" -maxdepth 1 -type f -name '*.dump' | wc -l | tr -d ' ')"
[ "$dump_count" = 1 ] || fail "successful backup should leave exactly one final dump"
if find "$case_dir/backups" -maxdepth 1 -type f -name '*.partial.*' | grep -q .; then
  fail "successful backup should not leave a temporary dump"
fi
final_dump="$(find "$case_dir/backups" -maxdepth 1 -type f -name '*.dump' -print -quit)"
[ "$(cat "$final_dump")" = 'verified dump bytes' ] || fail "final dump should contain the pg_dump output"
[ "$(cat "$case_dir/restore-input")" = 'verified dump bytes' ] || fail "verification should inspect the temporary dump"
case "$output" in
  *"Backup complete: $final_dump"*) ;;
  *) fail "successful backup should report the promoted final path" ;;
esac

echo "PASS: pg_backup_test.sh"
