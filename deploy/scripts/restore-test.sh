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

TEST_CONTAINER="143-restore-test-$(date +%s)"
trap 'docker rm -f "$TEST_CONTAINER" 2>/dev/null' EXIT
echo "$(date -Iseconds) Testing restore of $BACKUP..."

# Start a temporary Postgres for the test
docker run -d --name "$TEST_CONTAINER" \
  -e POSTGRES_USER="$DB_USER" \
  -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB="$DB_NAME" \
  "$POSTGRES_IMAGE"

# Wait for Postgres to be ready
for i in $(seq 1 30); do
  if docker exec "$TEST_CONTAINER" pg_isready -U "$DB_USER" > /dev/null 2>&1; then
    break
  fi
  sleep 1
done

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
