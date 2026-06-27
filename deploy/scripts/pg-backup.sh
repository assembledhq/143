#!/usr/bin/env bash
set -euo pipefail

# Automated pg_dump backup with verification and retention.
# Installed as a cron job by deploy/scripts/install-pg-backups.sh; runs every
# 6 hours as root on the db host.
#
# The postgres container authenticates with scram-sha-256 even for local
# connections (see deploy/postgres/pg_hba.conf), so pg_dump needs the
# password. It is read from $DB_PASSWORD if exported, otherwise from
# /opt/143/.env (written by provision.sh).

BACKUP_DIR="${BACKUP_DIR:-/backups/postgres}"
# Local retention defaults to 7 days. At ~900 MB/dump every 6h that is ~25 GB;
# 30 days would be ~108 GB and risk filling the disk. Long-term history is the
# job of the offsite sync, not the local disk.
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-7}"
CONTAINER_NAME="${POSTGRES_CONTAINER:-143-postgres-1}"
DB_USER="${POSTGRES_USER:-onefortythree}"
DB_NAME="${POSTGRES_DB:-onefortythree}"
ENV_FILE="${ENV_FILE:-/opt/143/.env}"

# Resolve the DB password (needed for the in-container pg_dump connection).
DB_PASSWORD="${DB_PASSWORD:-}"
if [ -z "$DB_PASSWORD" ] && [ -f "$ENV_FILE" ]; then
  DB_PASSWORD="$(grep -E '^DB_PASSWORD=' "$ENV_FILE" | cut -d= -f2- || true)"
fi
if [ -z "$DB_PASSWORD" ]; then
  echo "ERROR: DB_PASSWORD not set and not found in $ENV_FILE" >&2
  exit 1
fi

mkdir -p "$BACKUP_DIR"

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_FILE="$BACKUP_DIR/$DB_NAME-$TIMESTAMP.dump"

# Custom format: compressed, supports selective restore
docker exec -e PGPASSWORD="$DB_PASSWORD" "$CONTAINER_NAME" \
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

# Optional offsite sync (true disaster recovery). Without it, dumps live only
# on this host's disk — a disk/VPS loss takes the backups with it. To enable,
# drop a BACKUP_SYNC_CMD into /opt/143/backup-sync.env, e.g.:
#   BACKUP_SYNC_CMD='rclone sync /backups/postgres s3:143-backups/postgres/'
SYNC_ENV="${BACKUP_SYNC_ENV:-/opt/143/backup-sync.env}"
# shellcheck disable=SC1090
[ -f "$SYNC_ENV" ] && . "$SYNC_ENV"
if [ -n "${BACKUP_SYNC_CMD:-}" ]; then
  echo "$(date -Iseconds) Running offsite sync..."
  if eval "$BACKUP_SYNC_CMD"; then
    echo "$(date -Iseconds) Offsite sync complete"
  else
    echo "ERROR: offsite sync failed (BACKUP_SYNC_CMD)" >&2
    exit 1
  fi
fi
