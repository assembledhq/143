#!/usr/bin/env bash
set -euo pipefail

# Automated pg_dump backup with verification and retention.
# Usage: run via cron every 6 hours.
#   0 */6 * * * /opt/143/deploy/scripts/pg-backup.sh >> /var/log/pg-backup.log 2>&1

BACKUP_DIR="${BACKUP_DIR:-/backups/postgres}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
CONTAINER_NAME="${POSTGRES_CONTAINER:-143-postgres-1}"
DB_USER="${POSTGRES_USER:-onefortythree}"
DB_NAME="${POSTGRES_DB:-onefortythree}"

mkdir -p "$BACKUP_DIR"

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_FILE="$BACKUP_DIR/$DB_NAME-$TIMESTAMP.dump"

# Custom format: compressed, supports selective restore
docker exec "$CONTAINER_NAME" \
  pg_dump -U "$DB_USER" -Fc "$DB_NAME" > "$BACKUP_FILE"

# Verify the backup is valid (runs pg_restore inside the container
# so we don't require postgresql-client on the host)
docker exec -i "$CONTAINER_NAME" \
  pg_restore --list < "$BACKUP_FILE" > /dev/null 2>&1 || {
  echo "ERROR: Backup verification failed for $BACKUP_FILE" >&2
  rm -f "$BACKUP_FILE"
  exit 1
}

BACKUP_SIZE=$(du -h "$BACKUP_FILE" | cut -f1)
echo "$(date -Iseconds) Backup complete: $BACKUP_FILE ($BACKUP_SIZE)"

# Clean up old backups
find "$BACKUP_DIR" -name "*.dump" -mtime +"$RETENTION_DAYS" -delete
echo "$(date -Iseconds) Cleaned backups older than $RETENTION_DAYS days"
