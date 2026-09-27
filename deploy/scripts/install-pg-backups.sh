#!/usr/bin/env bash
# Install/refresh automated Postgres backups on a db host.
#
# Writes /etc/cron.d/143-pg-backup, which runs:
#   - pg-backup.sh   every 6 hours  (pg_dump custom-format, verified, receipt-qualified retention)
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
# Offsite JSON configuration is required by the policy; full restoration is a
# separate gate. See docs/self-hosting/database-backup-controls.md.
#
# Usage (on the db host, as root):
#   install-pg-backups.sh
# Env overrides:
#   BACKUP_DIR             (default /backups/postgres)
#   Retention is two receipt-qualified copies, not an age window.
#   SCRIPTS_DIR            (default /opt/143/deploy/scripts)
#   BACKUP_CRON            (default "0 */6 * * *")
#   RESTORE_TEST_CRON      (default "0 5 * * 0")
#   BACKUP_ENABLED / RESTORE_TEST_ENABLED (true/false; otherwise preserve the
#                           installed value, defaulting to true on first install)
#   CRON_FILE / PG_BACKUP_LOG / RESTORE_TEST_LOG — overridable for tests

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/backups/postgres}"
SCRIPTS_DIR="${SCRIPTS_DIR:-/opt/143/deploy/scripts}"
BACKUP_CRON="${BACKUP_CRON:-0 */6 * * *}"
RESTORE_TEST_CRON="${RESTORE_TEST_CRON:-0 5 * * 0}"

CRON_FILE="${CRON_FILE:-/etc/cron.d/143-pg-backup}"
PG_BACKUP_LOG="${PG_BACKUP_LOG:-/var/log/pg-backup.log}"
RESTORE_TEST_LOG="${RESTORE_TEST_LOG:-/var/log/restore-test.log}"

# Read only the literal boolean fields; never source a cron file as shell.
# An omitted setting must not undo an operator's previously installed hold.
resolve_enabled() {
  local name="$1" value="$2"
  if [ -z "$value" ] && [ -f "$CRON_FILE" ]; then
    # Cron accepts whitespace around names/equals; reject noncanonical forms
    # rather than overlook a hand-written hold and silently enable the job.
    if ! awk -v key="$name" '
      $0 ~ "^[[:space:]]*" key "[[:space:]]*=" && $0 !~ "^" key "=" { exit 1 }
    ' "$CRON_FILE"; then
      echo "ERROR: installed $name has noncanonical spacing; supply an explicit true or false" >&2
      return 1
    fi
    value="$(awk -F= -v key="$name" '$1 == key { print substr($0, length(key) + 2) }' "$CRON_FILE")"
    if [ -z "$value" ] && grep -q "^$name=" "$CRON_FILE"; then
      echo "ERROR: installed $name is empty; supply an explicit true or false" >&2
      return 1
    fi
  fi
  value="${value:-true}"
  case "$value" in
    true|false) printf '%s\n' "$value" ;;
    *) echo "ERROR: $name must be true or false" >&2; return 1 ;;
  esac
}
BACKUP_ENABLED="$(resolve_enabled BACKUP_ENABLED "${BACKUP_ENABLED:-}")"
RESTORE_TEST_ENABLED="$(resolve_enabled RESTORE_TEST_ENABLED "${RESTORE_TEST_ENABLED:-}")"

# The backup scripts must already be on the host (provision.sh / the wrapper
# copy them to SCRIPTS_DIR before invoking this installer).
command -v python3 >/dev/null || { echo "ERROR: Python 3 is required" >&2; exit 1; }
for s in pg-backup.sh restore-test.sh restore-test-body.sh pg-backup-policy.py pg-backup-config.py pg_backup_state.py pg_backup_runtime.py pg_backup_health.py; do
  if [ ! -f "$SCRIPTS_DIR/$s" ]; then
    echo "ERROR: $SCRIPTS_DIR/$s not found — copy the deploy scripts to this host first." >&2
    exit 1
  fi
  chmod +x "$SCRIPTS_DIR/$s"
done

mkdir -p "$BACKUP_DIR"

BACKUP_LINE="$BACKUP_CRON root $SCRIPTS_DIR/pg-backup.sh >> $PG_BACKUP_LOG 2>&1"
RESTORE_LINE="$RESTORE_TEST_CRON root $SCRIPTS_DIR/restore-test.sh >> $RESTORE_TEST_LOG 2>&1"
if [ "$BACKUP_ENABLED" = false ]; then
  BACKUP_LINE="# DISABLED: $BACKUP_LINE"
fi
if [ "$RESTORE_TEST_ENABLED" = false ]; then
  RESTORE_LINE="# DISABLED: $RESTORE_LINE"
fi

# Per-job env (BACKUP_DIR / holds) is set in the cron.d file so the jobs
# honor the same values configured here. cron.d entries take a user field.
DESIRED="$(cat <<EOF
# Managed by deploy/scripts/install-pg-backups.sh — do not edit by hand.
# Automated Postgres backups for the 143 db node.
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
BACKUP_DIR=$BACKUP_DIR
BACKUP_ENABLED=$BACKUP_ENABLED
RESTORE_TEST_ENABLED=$RESTORE_TEST_ENABLED

$BACKUP_LINE
$RESTORE_LINE
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

echo "pg-backups: dumps -> $BACKUP_DIR (two receipt-qualified retained copies)."
if [ "$BACKUP_ENABLED" = false ]; then
  echo "WARNING: database backups are HELD; record the recovery-point gap and an owner to resume them." >&2
fi
if [ "$RESTORE_TEST_ENABLED" = false ]; then
  echo "WARNING: restore testing is DEFERRED, not successful; assign a replacement drill." >&2
fi
if [ -f /opt/143/backup-storage.json ]; then
  echo "pg-backups: offsite sync configured (/opt/143/backup-storage.json)."
else
  echo "WARNING: no JSON offsite configuration; backup admission will fail closed." >&2
fi

echo "WARNING: M1a requires attended runs; cron alone does not authorize admission or retention. See docs/self-hosting/database-backup-controls.md." >&2
