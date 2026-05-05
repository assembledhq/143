#!/usr/bin/env bash
# Install/update Docker log rotation in /etc/docker/daemon.json.
#
# Docker's default json-file driver has no size limit, so a chatty
# container will fill the disk on its own. This script merges
# log-driver + log-opts into the existing daemon.json (preserving any
# other keys, notably gVisor's runtimes block on workers), writes the
# result atomically, and restarts docker only when the content actually
# changed — so steady-state deploys cost nothing.
#
# Runs as root (invoked via `sudo` from deploy.sh / bootstrap.sh). Listed
# in /etc/sudoers.d/99-deploy so the deploy user can call it with no
# password and no broader privileges.
#
# Usage:
#   install-log-rotation.sh <max-size> <max-file>
# Example:
#   install-log-rotation.sh 100m 5

set -euo pipefail

MAX_SIZE="${1:?max-size required (e.g. 100m)}"
MAX_FILE="${2:?max-file required (e.g. 5)}"

DAEMON_JSON="/etc/docker/daemon.json"
CURRENT='{}'
[ -s "$DAEMON_JSON" ] && CURRENT="$(cat "$DAEMON_JSON")"

# Single python script with a MODE switch so DESIRED and CURRENT_NORM use
# identical pretty-print parameters — anything else risks a false diff
# (different key ordering, indent, etc.) that would force a needless
# docker restart on every deploy.
PYCODE='
import json, os
cur = json.loads(os.environ["CUR"])
if os.environ["MODE"] == "merge":
    opts = dict(cur.get("log-opts", {}))
    opts["max-size"] = os.environ["MS"]
    opts["max-file"] = os.environ["MF"]
    cur["log-driver"] = "json-file"
    cur["log-opts"] = opts
print(json.dumps(cur, sort_keys=True, indent=2))
'
DESIRED="$(MODE=merge MS="$MAX_SIZE" MF="$MAX_FILE" CUR="$CURRENT" python3 -c "$PYCODE")"
CURRENT_NORM="$(MODE=normalize CUR="$CURRENT" python3 -c "$PYCODE")"

if [ "$CURRENT_NORM" = "$DESIRED" ]; then
  echo "log-rotation: daemon.json already configured (max-size=$MAX_SIZE, max-file=$MAX_FILE); skipping docker restart."
  exit 0
fi

echo "log-rotation: updating $DAEMON_JSON (max-size=$MAX_SIZE, max-file=$MAX_FILE)..."
mkdir -p /etc/docker
# Atomic rename: a SIGKILL between truncate and write under a plain `>`
# would leave a zero-byte daemon.json that docker rejects on next start.
# Writing to .new and renaming guarantees the file is either old-good or
# new-good, never partial.
TMP="$(mktemp /etc/docker/daemon.json.new.XXXXXX)"
trap 'rm -f "$TMP"' EXIT
printf '%s\n' "$DESIRED" > "$TMP"
chmod 0644 "$TMP"
mv "$TMP" "$DAEMON_JSON"
trap - EXIT

# log-driver / log-opts only take effect for newly created containers, so
# existing services keep their old (unbounded) logs until next recreate.
# Restart docker so the daemon picks up the new config; the next compose
# up / rolling deploy then recreates containers with the cap. Skipping
# this would leave the running fleet exposed until an unrelated deploy
# happens to cycle each container.
systemctl restart docker
echo "log-rotation: docker restarted with new log-driver config."
