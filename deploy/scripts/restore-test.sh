#!/usr/bin/env bash
set -euo pipefail

# Automated backup restore verification.
# Run weekly via cron (installed by install-pg-backups.sh) to confirm backups
# are actually restorable.

BACKUP_DIR="${BACKUP_DIR:-/backups/postgres}"
DB_USER="${POSTGRES_USER:-onefortythree}"
DB_NAME="${POSTGRES_DB:-onefortythree}"
# Must match the major version of the production server (docker-compose.db.yml):
# pg_restore from an older server rejects a newer custom-format archive
# ("unsupported version in file header"), which would fail the drill it is
# meant to validate. Override only to test against a different image.
POSTGRES_IMAGE="${POSTGRES_IMAGE:-postgres:18}"
# Minimum number of non-system tables expected after restore.
MIN_TABLE_COUNT="${MIN_TABLE_COUNT:-5}"

BACKUP=$(ls -t "$BACKUP_DIR"/*.dump 2>/dev/null | head -1)
if [ -z "$BACKUP" ]; then
  echo "ERROR: No backup files found in $BACKUP_DIR"
  exit 1
fi

# Keep Docker's ownership receipt in a private directory. Never clean up by
# name: a failed create (for example, a name collision) does not own that name.
TEST_STATE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/143-restore-test.XXXXXXXXXX")
TEST_CONTAINER_NAME="${TEST_STATE_DIR##*/}"
CID_FILE="$TEST_STATE_DIR/container-id"
cleanup() {
  local status=$? container_id
  trap - EXIT
  trap '' HUP INT TERM
  if [ -s "$CID_FILE" ]; then
    if container_id=$(cat "$CID_FILE") && [[ "$container_id" =~ ^[0-9a-f]{64}$ ]]; then
      # -v removes only this disposable container's anonymous volumes. The
      # production volume and backup directory are never mounted here.
      if ! docker rm -f -v "$container_id"; then
        echo "ERROR: Cleanup failed for restore-test container $container_id; manual inspection required" >&2
        if [ "$status" -eq 0 ]; then status=1; fi
      fi
    else
      echo "ERROR: Invalid restore-test container ID in $CID_FILE; refusing cleanup" >&2
      if [ "$status" -eq 0 ]; then status=1; fi
    fi
  fi
  if ! rm -f "$CID_FILE" || ! rmdir "$TEST_STATE_DIR"; then
    echo "ERROR: Failed to remove restore-test state directory $TEST_STATE_DIR" >&2
    if [ "$status" -eq 0 ]; then status=1; fi
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
# Log the unique name before creation so an interrupted Docker CLI that never
# wrote its cidfile still leaves an operator a way to identify that test.
echo "$(date -Iseconds) Testing restore of $BACKUP in $TEST_CONTAINER_NAME..."

# Record the created container before starting it, so startup failures also
# remove its anonymous data volume. Docker writes the cidfile on creation.
docker create --cidfile "$CID_FILE" --name "$TEST_CONTAINER_NAME" \
  -e POSTGRES_USER="$DB_USER" \
  -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB="$DB_NAME" \
  "$POSTGRES_IMAGE"
if ! TEST_CONTAINER=$(cat "$CID_FILE") || ! [[ "$TEST_CONTAINER" =~ ^[0-9a-f]{64}$ ]]; then
  echo "ERROR: Docker returned an invalid restore-test container ID" >&2
  exit 1
fi
docker start "$TEST_CONTAINER"

# Wait for Postgres to be ready
READY=false
for i in $(seq 1 30); do
  if docker exec "$TEST_CONTAINER" pg_isready -U "$DB_USER" > /dev/null 2>&1; then
    READY=true
    break
  fi
  sleep 1
done
if [ "$READY" != true ]; then
  echo "ERROR: Restore-test Postgres did not become ready" >&2
  exit 1
fi

# Restore
docker exec -i "$TEST_CONTAINER" \
  pg_restore -U "$DB_USER" -d "$DB_NAME" --clean --if-exists < "$BACKUP"

# Verify the restore produced a reasonable number of tables with data
TABLE_COUNT=$(docker exec "$TEST_CONTAINER" \
  psql -U "$DB_USER" -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'" 2>/dev/null || echo "0")

echo "Found $TABLE_COUNT public tables after restore."

if [ "$TABLE_COUNT" -lt "$MIN_TABLE_COUNT" ]; then
  echo "FAIL: Expected at least $MIN_TABLE_COUNT tables, found $TABLE_COUNT"
  echo "$(date -Iseconds) Restore test FAILED"
  exit 1
fi

# Verify at least some tables have data (not an empty schema-only restore)
NONEMPTY_COUNT=$(docker exec "$TEST_CONTAINER" \
  psql -U "$DB_USER" -tAc "
    SELECT count(*) FROM (
      SELECT schemaname, relname
      FROM pg_stat_user_tables
      WHERE n_live_tup > 0
    ) t
  " 2>/dev/null || echo "0")

echo "Found $NONEMPTY_COUNT non-empty tables."

if [ "$NONEMPTY_COUNT" -eq 0 ]; then
  echo "FAIL: All tables are empty after restore"
  echo "$(date -Iseconds) Restore test FAILED"
  exit 1
fi

echo "$(date -Iseconds) Restore test PASSED"
