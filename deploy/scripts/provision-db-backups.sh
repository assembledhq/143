#!/usr/bin/env bash
# Install/refresh automated Postgres backups on the db host.
#
# Copies the current backup scripts to the host and runs the installer over
# root SSH (the same transport provision.sh uses). The installer is
# idempotent, so this is safe to run any time. Invoked automatically at the
# end of provision-db and exposed for standalone runs as
# `make provision-db-backups`.
#
# Offsite sync: if BACKUP_S3_BUCKET is set in the environment (the Makefile
# target and provision.sh resolve it from .env.production.enc), this also
# writes /opt/143/backup-sync.env so pg-backup.sh ships each verified dump to
# S3. When it is unset, any existing backup-sync.env on the host is left
# untouched and offsite stays as-is.
#
# Required env for offsite (all four, or none):
#   BACKUP_S3_BUCKET, BACKUP_S3_REGION,
#   BACKUP_AWS_ACCESS_KEY_ID, BACKUP_AWS_SECRET_ACCESS_KEY
#
# Usage:
#   provision-db-backups.sh <host> [ssh_key]

set -euo pipefail

HOST="${1:?usage: provision-db-backups.sh <host> [ssh_key]}"
SSH_KEY="${2:-$HOME/.ssh/143-deploy}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SSH_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20)
SCP_OPTS=(-i "$SSH_KEY" -o StrictHostKeyChecking=accept-new)

echo "--- Installing automated DB backups on $HOST ---"
ssh "${SSH_OPTS[@]}" root@"$HOST" "mkdir -p /opt/143/deploy/scripts"
scp "${SCP_OPTS[@]}" \
  "$SCRIPT_DIR/pg-backup.sh" \
  "$SCRIPT_DIR/restore-test.sh" \
  "$SCRIPT_DIR/install-pg-backups.sh" \
  root@"$HOST":/opt/143/deploy/scripts/

# Offsite sync config (optional). Write /opt/143/backup-sync.env from the
# BACKUP_* env vars when a bucket is configured. The AWS creds are streamed in
# over SSH stdin (never on the command line) and the file is locked to deploy.
if [ -n "${BACKUP_S3_BUCKET:-}" ]; then
  : "${BACKUP_S3_REGION:?BACKUP_S3_REGION required when BACKUP_S3_BUCKET is set}"
  : "${BACKUP_AWS_ACCESS_KEY_ID:?BACKUP_AWS_ACCESS_KEY_ID required when BACKUP_S3_BUCKET is set}"
  : "${BACKUP_AWS_SECRET_ACCESS_KEY:?BACKUP_AWS_SECRET_ACCESS_KEY required when BACKUP_S3_BUCKET is set}"
  echo "--- Writing offsite sync config (s3://$BACKUP_S3_BUCKET/postgres/) ---"
  # The official AWS CLI image syncs the local backup dir to S3 with no host
  # package install. `s3 sync` (no --delete) never removes remote objects, so
  # offsite retention is governed by the bucket lifecycle, independent of the
  # shorter local retention.
  SYNC_CMD="docker run --rm -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION -v /backups/postgres:/backups:ro public.ecr.aws/aws-cli/aws-cli:latest s3 sync /backups s3://$BACKUP_S3_BUCKET/postgres/ --only-show-errors"
  printf '# Managed by deploy/scripts/provision-db-backups.sh — do not edit by hand.\n# Offsite sync config sourced by pg-backup.sh after each verified dump.\nexport AWS_ACCESS_KEY_ID=%s\nexport AWS_SECRET_ACCESS_KEY=%s\nexport AWS_DEFAULT_REGION=%s\nexport BACKUP_SYNC_CMD=%s\n' \
    "$BACKUP_AWS_ACCESS_KEY_ID" "$BACKUP_AWS_SECRET_ACCESS_KEY" "$BACKUP_S3_REGION" "'$SYNC_CMD'" \
    | ssh "${SSH_OPTS[@]}" root@"$HOST" 'cat > /opt/143/backup-sync.env && chown deploy:deploy /opt/143/backup-sync.env && chmod 600 /opt/143/backup-sync.env'
else
  echo "--- No BACKUP_S3_BUCKET set; leaving offsite sync config unchanged ---"
fi

ssh "${SSH_OPTS[@]}" root@"$HOST" \
  "chmod +x /opt/143/deploy/scripts/pg-backup.sh /opt/143/deploy/scripts/restore-test.sh /opt/143/deploy/scripts/install-pg-backups.sh && /opt/143/deploy/scripts/install-pg-backups.sh"
echo "--- DB backups configured on $HOST ---"
