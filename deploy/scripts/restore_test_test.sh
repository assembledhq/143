#!/usr/bin/env bash
# Exercise cleanup ownership and exit handling with a fake Docker CLI. These
# tests check commands and shell behavior; they do not replace a Docker drill.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

FAKE_BIN="$TEST_ROOT/bin"
mkdir -p "$FAKE_BIN"
export FAKE_CONTAINER_ID=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

cat > "$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$FAKE_CASE_DIR/calls"

case "$1" in
  create)
    shift
    [ "$1" = --cidfile ] || exit 99
    cidfile=$2
    shift 2
    [ "$1" = --name ] || exit 99
    case "$2" in 143-restore-test.*) ;; *) exit 99 ;; esac
    printf '%s\n' "$2" > "$FAKE_CASE_DIR/container-name"
    shift 2
    # In particular, no live data volume or backup directory can be mounted.
    [ "$*" = '-e POSTGRES_USER=test-user -e POSTGRES_PASSWORD=test -e POSTGRES_DB=test-db postgres:18' ] || exit 99
    case "$FAKE_CASE" in
      create-failure|name-collision) exit 125 ;;
      create-without-receipt) : > "$cidfile"; exit 137 ;;
      missing-id) exit 0 ;;
      invalid-id) printf '143-postgres-1' > "$cidfile"; exit 0 ;;
    esac
    # Docker's cidfile does not have a trailing newline.
    printf '%s' "$FAKE_CONTAINER_ID" > "$cidfile"
    case "$FAKE_CASE" in
      create-receipt-failure) exit 126 ;;
      create-term) kill -TERM "$PPID" ;;
    esac
    printf '%s\n' "$FAKE_CONTAINER_ID"
    ;;
  start)
    [ "$#" = 2 ] && [ "$2" = "$FAKE_CONTAINER_ID" ] || exit 99
    if [ "$FAKE_CASE" = start-failure ]; then exit 23; fi
    ;;
  exec)
    shift
    if [ "$1" = -i ]; then shift; fi
    [ "$1" = "$FAKE_CONTAINER_ID" ] || exit 99
    shift
    case "$1" in
      pg_isready)
        if [ "$FAKE_CASE" = readiness-failure ]; then exit 1; fi
        if [ "$FAKE_CASE" = readiness-term ]; then kill -TERM "$PPID"; fi
        ;;
      pg_restore)
        cat > "$FAKE_CASE_DIR/restored-input"
        case "$FAKE_CASE" in
          restore-failure|restore-and-cleanup-failure) exit 42 ;;
          restore-int) kill -INT "$PPID" ;;
          restore-hup) kill -HUP "$PPID" ;;
        esac
        ;;
      psql)
        case "$*" in
          *information_schema.tables*)
            if [ "$FAKE_CASE" = few-tables ]; then echo 2; else echo 10; fi
            ;;
          *pg_stat_user_tables*)
            if [ "$FAKE_CASE" = empty-tables ]; then echo 0; else echo 7; fi
            ;;
          *) exit 99 ;;
        esac
        ;;
      *) exit 99 ;;
    esac
    ;;
  rm)
    [ "$*" = "rm -f -v $FAKE_CONTAINER_ID" ] || exit 99
    case "$FAKE_CASE" in
      cleanup-failure|restore-and-cleanup-failure) exit 55 ;;
    esac
    ;;
  *) exit 99 ;;
esac
EOF
cat > "$FAKE_BIN/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$FAKE_BIN/docker" "$FAKE_BIN/sleep"

case_count=0
while read -r name expected_status expected_cleanup expected_start expected_restore; do
  case_dir="$TEST_ROOT/$name"
  mkdir -p "$case_dir/backups" "$case_dir/tmp"
  printf 'backup bytes to preserve\n' > "$case_dir/backups/test.dump"
  : > "$case_dir/calls"
  status=0
  env PATH="$FAKE_BIN:$PATH" \
    TMPDIR="$case_dir/tmp" \
    BACKUP_DIR="$case_dir/backups" \
    POSTGRES_USER=test-user POSTGRES_DB=test-db POSTGRES_IMAGE=postgres:18 \
    MIN_TABLE_COUNT=5 FAKE_CASE="$name" FAKE_CASE_DIR="$case_dir" \
    bash "$SCRIPT_DIR/restore-test.sh" < /dev/null > "$case_dir/output" 2>&1 || status=$?
  if [ "$status" != "$expected_status" ]; then
    cat "$case_dir/output" >&2
    fail "$name: expected status $expected_status, got $status"
  fi

  grep -Fq "in $(cat "$case_dir/container-name")..." "$case_dir/output" || fail "$name: log must identify the unique container even when creation fails"

  cleanup_calls=$(grep -c '^rm ' "$case_dir/calls" || true)
  [ "$cleanup_calls" = "$expected_cleanup" ] || fail "$name: incorrect cleanup count"
  if [ "$expected_cleanup" = 1 ]; then
    grep -Fxq "rm -f -v $FAKE_CONTAINER_ID" "$case_dir/calls" || fail "$name: cleanup must target only the created ID and its anonymous volumes"
  fi
  start_calls=$(grep -c '^start ' "$case_dir/calls" || true)
  [ "$start_calls" = "$expected_start" ] || fail "$name: must not start an unowned container"
  restore_calls=$(grep -c ' pg_restore ' "$case_dir/calls" || true)
  [ "$restore_calls" = "$expected_restore" ] || fail "$name: unexpected restore attempt"
  if [ "$expected_restore" = 1 ]; then
    cmp "$case_dir/backups/test.dump" "$case_dir/restored-input" || fail "$name: restore should receive all backup bytes"
  fi
  [ "$(cat "$case_dir/backups/test.dump")" = 'backup bytes to preserve' ] || fail "$name: must not alter the backup"
  [ -z "$(ls -A "$case_dir/tmp")" ] || fail "$name: temporary state must be cleaned up"
  case "$name" in
    cleanup-failure|restore-and-cleanup-failure)
      grep -Fq "ERROR: Cleanup failed for restore-test container $FAKE_CONTAINER_ID" "$case_dir/output" || fail "$name: cleanup failure must identify the remaining container"
      ;;
    invalid-id)
      grep -Fq 'refusing cleanup' "$case_dir/output" || fail "$name: invalid ID must fail closed"
      ;;
    missing-id)
      grep -Fq 'Docker returned an invalid restore-test container ID' "$case_dir/output" || fail "$name: missing ID must report the ownership failure"
      ;;
    readiness-failure)
      [ "$(grep -c ' pg_isready ' "$case_dir/calls")" = 30 ] || fail "$name: readiness must be bounded"
      ;;
    success)
      grep -Fq 'Restore test PASSED' "$case_dir/output" || fail "$name: completed restore should report success"
      ;;
  esac
  case_count=$((case_count + 1))
done <<'CASES'
success 0 1 1 1
create-failure 125 0 0 0
name-collision 125 0 0 0
create-without-receipt 137 0 0 0
create-receipt-failure 126 1 0 0
missing-id 1 0 0 0
invalid-id 1 0 0 0
start-failure 23 1 1 0
readiness-failure 1 1 1 0
restore-failure 42 1 1 1
few-tables 1 1 1 1
empty-tables 1 1 1 1
cleanup-failure 1 1 1 1
restore-and-cleanup-failure 42 1 1 1
create-term 143 1 0 0
readiness-term 143 1 1 0
restore-int 130 1 1 1
restore-hup 129 1 1 1
CASES

[ "$case_count" = 18 ] || fail "all 18 cases must run; stdin consumption must not truncate the suite"
echo "PASS: restore_test_test.sh ($case_count cases)"
