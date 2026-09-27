#!/usr/bin/env bash
# Install/refresh automated Postgres backups on the db host.
#
# Copies the current backup scripts to the host and runs the installer over
# root SSH (the same transport provision.sh uses). The installer is
# idempotent; run only within an approved installation window. Invoked at the
# end of provision-db and exposed for standalone runs as
# `make provision-db-backups`.
#
# Offsite sync: if BACKUP_S3_BUCKET is set in the environment (the Makefile
# target and provision.sh resolve it from .env.production.enc), this also
# writes /opt/143/backup-storage.json so pg-backup.sh ships each verified dump to
# S3. When it is unset, any existing backup-storage.json on the host is left
# untouched and offsite stays as-is.
#
# Required env for offsite (all four, or none):
#   BACKUP_S3_BUCKET, BACKUP_S3_REGION,
#   BACKUP_AWS_ACCESS_KEY_ID, BACKUP_AWS_SECRET_ACCESS_KEY
# Optional schedule controls (also exported by provision.sh from private config):
#   BACKUP_ENABLED, RESTORE_TEST_ENABLED (true/false; omitted preserves host state)
#
# Usage:
#   provision-db-backups.sh <host> [ssh_key]

set -euo pipefail

HOST="${1:?usage: provision-db-backups.sh <host> [ssh_key]}"
SSH_KEY="${2:-$HOME/.ssh/143-deploy}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Validate before any SSH/copy, including before changing an offsite config.
for setting in BACKUP_ENABLED RESTORE_TEST_ENABLED; do
  case "${!setting:-}" in
    true|false|'') ;;
    *) echo "ERROR: $setting must be true or false" >&2; exit 1 ;;
  esac
done

# Validate all storage fields before copying anything to a host. JSON escaping
# preserves literal credential bytes; no credential becomes shell program text.
STORAGE_JSON=""
STORAGE_CONFIGURED=false
for setting in BACKUP_S3_BUCKET BACKUP_S3_REGION BACKUP_AWS_ACCESS_KEY_ID BACKUP_AWS_SECRET_ACCESS_KEY; do
  if [ -n "${!setting:-}" ]; then STORAGE_CONFIGURED=true; fi
done
if [ "$STORAGE_CONFIGURED" = true ]; then
  STORAGE_JSON="$(python3 "$SCRIPT_DIR/pg-backup-config.py" generate)"
fi

SSH_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20)
SCP_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=accept-new)

echo "--- Installing automated DB backups on $HOST ---"
ssh "${SSH_OPTS[@]}" root@"$HOST" "mkdir -p /opt/143/deploy/scripts"
scp "${SCP_OPTS[@]}" \
  "$SCRIPT_DIR/pg-backup.sh" \
  "$SCRIPT_DIR/restore-test.sh" \
  "$SCRIPT_DIR/restore-test-body.sh" \
  "$SCRIPT_DIR/pg-backup-policy.py" \
  "$SCRIPT_DIR/pg-backup-config.py" \
  "$SCRIPT_DIR/pg_backup_state.py" \
  "$SCRIPT_DIR/pg_backup_runtime.py" \
  "$SCRIPT_DIR/pg_backup_health.py" \
  "$SCRIPT_DIR/install-pg-backups.sh" \
  root@"$HOST":/opt/143/deploy/scripts/

# This data-only configuration replaces executable BACKUP_SYNC_CMD fragments.
# Keep legacy shell config untouched for rollback; the new job never sources it.
if [ -n "$STORAGE_JSON" ]; then
  printf '%s\n' "$STORAGE_JSON" | ssh "${SSH_OPTS[@]}" root@"$HOST" \
    'python3 /opt/143/deploy/scripts/pg-backup-config.py install'
else
  echo "--- No BACKUP_S3_BUCKET set; leaving JSON storage config unchanged ---"
fi

ssh "${SSH_OPTS[@]}" root@"$HOST" \
  "chmod +x /opt/143/deploy/scripts/pg-backup.sh /opt/143/deploy/scripts/restore-test.sh /opt/143/deploy/scripts/install-pg-backups.sh && BACKUP_ENABLED='${BACKUP_ENABLED:-}' RESTORE_TEST_ENABLED='${RESTORE_TEST_ENABLED:-}' /opt/143/deploy/scripts/install-pg-backups.sh"
echo "--- DB backups configured on $HOST ---"
