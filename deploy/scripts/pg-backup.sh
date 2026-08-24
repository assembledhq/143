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
# Local retention defaults to 7 days. Dump sizes grow with the database, so
# operators must size this window against the DB host's available disk. Long-
# term history is the job of the offsite sync, not the local disk.
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

# Retention must not depend on the new dump succeeding. In particular, a run
# that fails because Postgres is under memory pressure still needs to reclaim
# completed backups that have aged past the local retention window.
find "$BACKUP_DIR" -maxdepth 1 -type f -name "*.dump" -mtime +"$RETENTION_DAYS" -delete
echo "$(date -Iseconds) Cleaned backups older than $RETENTION_DAYS days"

# A normal error or signal removes the in-progress file via the EXIT trap. The
# stale-file sweep handles the one case traps cannot catch (SIGKILL or a host
# crash). Twelve hours is twice the cron interval, so it does not interfere
# with a slow backup from the immediately preceding run.
find "$BACKUP_DIR" -maxdepth 1 -type f -name ".*.dump.partial.*" -mmin +720 -delete

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_FILE="$BACKUP_DIR/$DB_NAME-$TIMESTAMP.dump"
PARTIAL_FILE=""

cleanup_partial_dump() {
  local status=$?
  if [ -n "$PARTIAL_FILE" ] && [ -e "$PARTIAL_FILE" ]; then
    if ! rm -f -- "$PARTIAL_FILE"; then
      echo "ERROR: Failed to remove incomplete backup $PARTIAL_FILE" >&2
    fi
  fi
  return "$status"
}

trap cleanup_partial_dump EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Keep the in-progress file hidden and distinguishable from verified dumps.
# mktemp creates it in the final directory so the promotion below is an atomic
# rename on the same filesystem.
PARTIAL_FILE="$(mktemp "$BACKUP_DIR/.${DB_NAME}-${TIMESTAMP}.dump.partial.XXXXXX")"
chmod 0600 "$PARTIAL_FILE"

# Custom format: compressed, supports selective restore
docker exec -e PGPASSWORD="$DB_PASSWORD" "$CONTAINER_NAME" \
  pg_dump -U "$DB_USER" -Fc "$DB_NAME" > "$PARTIAL_FILE"

# Verify the backup is valid (runs pg_restore inside the container
# so we don't require postgresql-client on the host)
docker exec -i "$CONTAINER_NAME" \
  pg_restore --list < "$PARTIAL_FILE" > /dev/null 2>&1 || {
  echo "ERROR: Backup verification failed for $BACKUP_FILE" >&2
  exit 1
}

# Only verified dumps receive the public .dump filename used by restores and
# offsite sync. Clearing PARTIAL_FILE disarms the EXIT cleanup after promotion.
mv -- "$PARTIAL_FILE" "$BACKUP_FILE"
PARTIAL_FILE=""

BACKUP_SIZE=$(du -h "$BACKUP_FILE" | cut -f1)
echo "$(date -Iseconds) Backup complete: $BACKUP_FILE ($BACKUP_SIZE)"

# Offsite sync (true disaster recovery). Without it, dumps live only on this
# host's disk — a disk/VPS loss takes the backups with it. /opt/143/backup-sync.env
# is written by deploy/scripts/provision-db-backups.sh from the BACKUP_* vars in
# .env.production.enc; it exports AWS creds + a BACKUP_SYNC_CMD that pushes
# $BACKUP_DIR to S3. To wire a different target by hand, set e.g.:
#   BACKUP_SYNC_CMD='rclone sync /backups/postgres s3:143-backups/postgres/'
SYNC_ENV="${BACKUP_SYNC_ENV:-/opt/143/backup-sync.env}"
if [ -f "$SYNC_ENV" ]; then
  # shellcheck disable=SC1090
  . "$SYNC_ENV"
fi
if [ -n "${BACKUP_SYNC_CMD:-}" ]; then
  echo "$(date -Iseconds) Running offsite sync..."
  if eval "$BACKUP_SYNC_CMD"; then
    echo "$(date -Iseconds) Offsite sync complete"
  else
    echo "ERROR: offsite sync failed (BACKUP_SYNC_CMD)" >&2
    exit 1
  fi
fi
