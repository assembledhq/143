#!/usr/bin/env bash
# Included by the existing deploy-script CI glob; no Docker daemon/AWS required.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
python3 "$SCRIPT_DIR/pg_backup_policy_test.py"
python3 "$SCRIPT_DIR/pg_backup_runtime_test.py"
python3 "$SCRIPT_DIR/pg_backup_health_test.py"
