#!/usr/bin/env bash
# Install/update Docker daemon DNS resolvers in /etc/docker/daemon.json.
#
# Why: every container on these hosts (worker, sandbox-dns sidecar, app
# services) reaches public hostnames through Docker's embedded resolver at
# 127.0.0.11, which forwards to whatever the daemon's `dns` setting is. With
# no explicit setting Docker copies the host's /etc/resolv.conf, so a single
# upstream resolver outage takes the whole fleet's outbound DNS down at
# once — that's exactly the failure mode that produced the
# 2026-05-07T04:15–04:22Z incident (workers couldn't resolve github.com,
# sandboxes couldn't resolve chatgpt.com). Pinning multiple independent
# resolvers makes the embedded resolver fall through on the next provider
# without involving the host's resolv.conf at all.
#
# Idempotent: merges the `dns` key into the existing daemon.json (preserving
# log-driver / log-opts / runtimes blocks), writes atomically, and restarts
# docker only when the on-disk content actually changes — so steady-state
# deploys cost nothing.
#
# Runs as root (invoked via `sudo` from deploy.sh / provision.sh). Listed in
# /etc/sudoers.d/99-deploy so the deploy user can call it with no password
# and no broader privileges.
#
# Usage:
#   install-docker-dns.sh <resolver> [<resolver>...]
# Example:
#   install-docker-dns.sh 1.1.1.1 8.8.8.8 9.9.9.9

set -euo pipefail

if [ "$#" -lt 1 ]; then
  echo "Usage: install-docker-dns.sh <resolver> [<resolver>...]" >&2
  exit 1
fi

# jq is the merge engine. Every cloud-init installs it; pin in
# deploy_config_test.go ensures that stays true. Fail loudly with an
# operator-actionable message if it's somehow missing — `set -e` on the
# subsequent jq call would otherwise abort with a less helpful "command not
# found".
if ! command -v jq >/dev/null 2>&1; then
  echo "install-docker-dns: jq not installed — required for daemon.json merge. Install with: apt-get install -y jq" >&2
  exit 1
fi

# Validate every arg is a literal IPv4/IPv6 address. Docker accepts hostnames
# in the `dns` field too, but using one would create a chicken-and-egg
# dependency: the embedded resolver would need a working upstream just to
# bootstrap its own upstream list. Reject anything that isn't a literal so
# operators don't accidentally regress the very property this script exists
# to enforce.
is_ip_literal() {
  local arg="$1"
  # IPv4: four dotted decimal octets, each 0-255.
  if [[ "$arg" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    local IFS=.
    local -a parts=($arg)
    for p in "${parts[@]}"; do
      if [ "$p" -lt 0 ] || [ "$p" -gt 255 ]; then
        return 1
      fi
    done
    return 0
  fi
  # IPv6: at least one colon, only hex digits + colons (loose, but any
  # non-literal — e.g. a hostname — would fail this and be rejected).
  if [[ "$arg" =~ ^[0-9a-fA-F:]+$ && "$arg" == *:* ]]; then
    return 0
  fi
  return 1
}
for arg in "$@"; do
  if ! is_ip_literal "$arg"; then
    echo "install-docker-dns: invalid resolver '$arg' (must be a literal IPv4/IPv6 address)" >&2
    exit 1
  fi
done

DAEMON_JSON="/etc/docker/daemon.json"
CURRENT='{}'
if [ -s "$DAEMON_JSON" ]; then
  # Reject malformed daemon.json explicitly rather than letting jq abort
  # mid-pipeline under `set -e` with a stack trace. An operator-edited file
  # we can't parse is not safe to overwrite — refuse and surface the path.
  if ! jq -e . "$DAEMON_JSON" >/dev/null 2>&1; then
    echo "install-docker-dns: $DAEMON_JSON is not valid JSON; refusing to overwrite operator state. Fix manually and re-run." >&2
    exit 1
  fi
  CURRENT="$(cat "$DAEMON_JSON")"
fi

# Build the desired and normalized-current JSON with the same `jq -S` (sort
# keys, default 2-space indent). Using identical formatters on both sides
# avoids cosmetic diffs (key ordering, whitespace) forcing a needless docker
# restart on every deploy.
DESIRED="$(printf '%s' "$CURRENT" | jq -S --args '. + {dns: $ARGS.positional}' -- "$@")"
CURRENT_NORM="$(printf '%s' "$CURRENT" | jq -S .)"

if [ "$CURRENT_NORM" = "$DESIRED" ]; then
  echo "install-docker-dns: daemon.json already configured (dns=$*); skipping docker restart."
  exit 0
fi

# Loud, structured log line: a config change here triggers `systemctl restart
# docker`, which kills every running container on the host. Operators
# watching deploy output should see *why* this deploy is doing a full-stack
# restart — otherwise the next 30s of dropped connections looks unexplained.
echo "install-docker-dns: config drift detected; updating $DAEMON_JSON (dns=$*) — docker daemon will be restarted, all containers on this host will recycle."
mkdir -p /etc/docker
# Atomic rename: a SIGKILL between truncate and write under a plain `>`
# would leave a zero-byte daemon.json that docker rejects on next start.
# Writing to .new and renaming guarantees the file is either old-good or
# new-good, never partial.
TMP="$(mktemp /etc/docker/daemon.json.new.XXXXXX)"
trap 'rm -f "$TMP"' EXIT
printf '%s\n' "$DESIRED" > "$TMP"
# Preserve the existing file's mode if present so an operator who tightened
# permissions (e.g. 0640) doesn't get them silently widened back to 0644.
if [ -e "$DAEMON_JSON" ]; then
  chmod --reference="$DAEMON_JSON" "$TMP"
else
  chmod 0644 "$TMP"
fi
mv "$TMP" "$DAEMON_JSON"
trap - EXIT

# `dns` only takes effect for newly created containers, so existing services
# keep using the old resolver list until next recreate. Restart docker so
# the daemon picks up the new config; the next compose up / rolling deploy
# then recreates containers with the new resolver list. Skipping this would
# leave the running fleet exposed until an unrelated deploy cycles each
# container.
systemctl restart docker
echo "install-docker-dns: docker restarted with new dns config."
