#!/usr/bin/env bash
set -euo pipefail

case "${BACKUP_ENABLED:-true}" in
  true) ;;
  false) echo "ERROR: database backup is HELD; no backup was made" >&2; exit 75 ;;
  *) echo "ERROR: BACKUP_ENABLED must be true or false" >&2; exit 1 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$SCRIPT_DIR/pg-backup-policy.py" backup "$@"
