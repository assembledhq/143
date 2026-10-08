#!/usr/bin/env bash
set -euo pipefail

case "${RESTORE_TEST_ENABLED:-true}" in
  true) ;;
  false) echo "ERROR: restore test is DEFERRED; recovery was not verified" >&2; exit 75 ;;
  *) echo "ERROR: RESTORE_TEST_ENABLED must be true or false" >&2; exit 1 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$SCRIPT_DIR/pg-backup-policy.py" restore "$@"
