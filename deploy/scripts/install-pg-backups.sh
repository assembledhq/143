#!/usr/bin/env bash
# Install/refresh automated Postgres backups on a db host.
#
# Writes /etc/cron.d/143-pg-backup, which runs:
#   - pg-backup.sh   every 6 hours  (pg_dump custom-format, verified, 7d retention)
#   - restore-test.sh weekly        (restores the newest dump into a throwaway
#                                    Postgres to prove it is recoverable)
#
# Idempotent: the cron file is only rewritten when its desired content
# changes, so re-running on every provision/deploy is a no-op. cron picks up
# /etc/cron.d changes automatically — no service restart needed.
#
# Runs as root. provision.sh invokes it over its root SSH session, and the
# standalone `make provision-db-backups` path (deploy/scripts/provision-db-backups.sh)
# does the same. It never runs as the deploy user, so it needs no sudoers grant.
#
# Offsite sync (true disaster recovery) is opt-in — see deploy/scripts/pg-backup.sh.
#
# Usage (on the db host, as root):
#   install-pg-backups.sh
# Env overrides:
#   BACKUP_DIR             (default /backups/postgres)
#   BACKUP_RETENTION_DAYS  (default 7 — ~25 GB at 900 MB/dump every 6h; long-term
#                           history belongs in the offsite sync, not local disk)
#   SCRIPTS_DIR            (default /opt/143/deploy/scripts)
#   BACKUP_CRON            (default "0 */6 * * *")
#   RESTORE_TEST_CRON      (default "0 5 * * 0")
#   CRON_FILE / PG_BACKUP_LOG / RESTORE_TEST_LOG — overridable for tests

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/backups/postgres}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-7}"
SCRIPTS_DIR="${SCRIPTS_DIR:-/opt/143/deploy/scripts}"
BACKUP_CRON="${BACKUP_CRON:-0 */6 * * *}"
RESTORE_TEST_CRON="${RESTORE_TEST_CRON:-0 5 * * 0}"

CRON_FILE="${CRON_FILE:-/etc/cron.d/143-pg-backup}"
PG_BACKUP_LOG="${PG_BACKUP_LOG:-/var/log/pg-backup.log}"
RESTORE_TEST_LOG="${RESTORE_TEST_LOG:-/var/log/restore-test.log}"

# The backup scripts must already be on the host (provision.sh / the wrapper
# copy them to SCRIPTS_DIR before invoking this installer).
for s in pg-backup.sh restore-test.sh; do
  if [ ! -f "$SCRIPTS_DIR/$s" ]; then
    echo "ERROR: $SCRIPTS_DIR/$s not found — copy the deploy scripts to this host first." >&2
    exit 1
  fi
  chmod +x "$SCRIPTS_DIR/$s"
done

mkdir -p "$BACKUP_DIR"

# Per-job env (BACKUP_DIR / retention) is set in the cron.d file so the jobs
# honor the same values configured here. cron.d entries take a user field.
DESIRED="$(cat <<EOF
# Managed by deploy/scripts/install-pg-backups.sh — do not edit by hand.
# Automated Postgres backups for the 143 db node.
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
BACKUP_DIR=$BACKUP_DIR
BACKUP_RETENTION_DAYS=$BACKUP_RETENTION_DAYS

$BACKUP_CRON root $SCRIPTS_DIR/pg-backup.sh >> $PG_BACKUP_LOG 2>&1
$RESTORE_TEST_CRON root $SCRIPTS_DIR/restore-test.sh >> $RESTORE_TEST_LOG 2>&1
EOF
)"

if [ -f "$CRON_FILE" ] && [ "$(cat "$CRON_FILE")" = "$DESIRED" ]; then
  echo "pg-backups: $CRON_FILE already up to date; nothing to do."
else
  # Atomic install. Temp name carries dots/leading dot so cron's run-parts
  # naming rules ignore it until the mv completes.
  TMP="$(mktemp "$(dirname "$CRON_FILE")/.143-pg-backup.XXXXXX")"
  trap 'rm -f "$TMP"' EXIT
  printf '%s\n' "$DESIRED" > "$TMP"
  chmod 0644 "$TMP"
  # Already root-owned when run as root on the db host (root created the temp);
  # tolerate failure so the script is testable unprivileged.
  chown root:root "$TMP" 2>/dev/null || true
  mv "$TMP" "$CRON_FILE"
  trap - EXIT
  echo "pg-backups: installed $CRON_FILE (backup '$BACKUP_CRON', restore-test '$RESTORE_TEST_CRON')."
fi

# Pre-create the log files so the first cron run can append.
touch "$PG_BACKUP_LOG" "$RESTORE_TEST_LOG"
chmod 0640 "$PG_BACKUP_LOG" "$RESTORE_TEST_LOG"

echo "pg-backups: dumps -> $BACKUP_DIR (retention ${BACKUP_RETENTION_DAYS}d)."
if [ -f /opt/143/backup-sync.env ]; then
  echo "pg-backups: offsite sync configured (/opt/143/backup-sync.env)."
else
  echo "pg-backups: NOTE no offsite sync — dumps are local-only (not disaster-safe). See deploy/scripts/pg-backup.sh to enable."
fi
