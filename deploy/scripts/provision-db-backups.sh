#!/usr/bin/env bash
# Install/refresh automated Postgres backups on the db host.
#
# Copies the current backup scripts to the host and runs the installer over
# root SSH (the same transport provision.sh uses). The installer is
# idempotent, so this is safe to run any time. Invoked automatically at the
# end of provision-db and exposed for standalone runs as
# `make provision-db-backups`.
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
ssh "${SSH_OPTS[@]}" root@"$HOST" \
  "chmod +x /opt/143/deploy/scripts/pg-backup.sh /opt/143/deploy/scripts/restore-test.sh /opt/143/deploy/scripts/install-pg-backups.sh && /opt/143/deploy/scripts/install-pg-backups.sh"
echo "--- DB backups configured on $HOST ---"
