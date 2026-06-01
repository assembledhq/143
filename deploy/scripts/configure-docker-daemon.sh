#!/usr/bin/env bash
# Configure Docker daemon host-wide settings in one atomic pass.
#
# Fresh provisioning needs several daemon.json hardening settings before the
# first compose up: bounded json-file logs and a pinned multi-resolver DNS list.
# Applying those with separate helpers can restart Docker multiple times on a
# fresh host, tripping systemd's start-rate limit after bootstrap/gVisor also
# touches the daemon. This helper merges the full desired daemon state and
# restarts Docker at most once when the normalized file changes.

set -euo pipefail

DAEMON_JSON="${DAEMON_JSON:-/etc/docker/daemon.json}"
LOG_MAX_SIZE=""
LOG_MAX_FILE=""
DNS_RESOLVERS=()

usage() {
  echo "Usage: $0 --log-max-size <size> --log-max-file <count> --dns <resolver> [<resolver>...]" >&2
}

is_ip_literal() {
  local arg="$1"
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
  if [[ "$arg" =~ ^[0-9a-fA-F:]+$ && "$arg" == *:* ]]; then
    return 0
  fi
  return 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --log-max-size)
      LOG_MAX_SIZE="${2:?--log-max-size requires a value}"
      shift 2
      ;;
    --log-max-file)
      LOG_MAX_FILE="${2:?--log-max-file requires a value}"
      shift 2
      ;;
    --dns)
      shift
      while [ "$#" -gt 0 ] && [[ "$1" != --* ]]; do
        DNS_RESOLVERS+=("$1")
        shift
      done
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "configure-docker-daemon: unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [ -z "$LOG_MAX_SIZE" ] || [ -z "$LOG_MAX_FILE" ] || [ "${#DNS_RESOLVERS[@]}" -eq 0 ]; then
  usage
  exit 1
fi

for resolver in "${DNS_RESOLVERS[@]}"; do
  if ! is_ip_literal "$resolver"; then
    echo "configure-docker-daemon: invalid resolver '$resolver' (must be a literal IPv4/IPv6 address)" >&2
    exit 1
  fi
done

if ! command -v jq >/dev/null 2>&1; then
  echo "configure-docker-daemon: jq not installed — required for daemon.json merge. Install with: apt-get install -y jq" >&2
  exit 1
fi

CURRENT='{}'
if [ -s "$DAEMON_JSON" ]; then
  if ! jq -e . "$DAEMON_JSON" >/dev/null 2>&1; then
    echo "configure-docker-daemon: $DAEMON_JSON is not valid JSON; refusing to overwrite operator state. Fix manually and re-run." >&2
    exit 1
  fi
  CURRENT="$(cat "$DAEMON_JSON")"
fi

DESIRED="$(printf '%s' "$CURRENT" | jq -S \
  --arg max_size "$LOG_MAX_SIZE" \
  --arg max_file "$LOG_MAX_FILE" \
  --args '
    . + {"log-driver": "json-file"}
    | .["log-opts"] = ((.["log-opts"] // {}) + {"max-size": $max_size, "max-file": $max_file})
    | . + {dns: $ARGS.positional}
  ' -- "${DNS_RESOLVERS[@]}")"
CURRENT_NORM="$(printf '%s' "$CURRENT" | jq -S .)"

if [ "$CURRENT_NORM" = "$DESIRED" ]; then
  echo "configure-docker-daemon: daemon.json already configured (log max-size=$LOG_MAX_SIZE max-file=$LOG_MAX_FILE; dns=${DNS_RESOLVERS[*]}); skipping docker restart."
  exit 0
fi

echo "configure-docker-daemon: updating $DAEMON_JSON (log max-size=$LOG_MAX_SIZE max-file=$LOG_MAX_FILE; dns=${DNS_RESOLVERS[*]}) — docker daemon will be restarted once, all containers on this host will recycle."
mkdir -p "$(dirname "$DAEMON_JSON")"
TMP="$(mktemp "${DAEMON_JSON}.new.XXXXXX")"
trap 'rm -f "$TMP"' EXIT
printf '%s\n' "$DESIRED" > "$TMP"
if [ -e "$DAEMON_JSON" ]; then
  EXISTING_MODE="$(stat -c %a "$DAEMON_JSON" 2>/dev/null || stat -f %Lp "$DAEMON_JSON")"
  chmod "$EXISTING_MODE" "$TMP"
else
  chmod 0644 "$TMP"
fi
mv "$TMP" "$DAEMON_JSON"
trap - EXIT

if [ "${SKIP_DOCKER_RESTART:-0}" = "1" ]; then
  echo "configure-docker-daemon: SKIP_DOCKER_RESTART=1; not restarting docker."
  exit 0
fi

restart_docker() {
  systemctl reset-failed docker.service docker.socket >/dev/null 2>&1 || true
  if systemctl restart docker; then
    return 0
  fi
  systemctl reset-failed docker.service docker.socket >/dev/null 2>&1 || true
  systemctl start docker
}

if ! restart_docker; then
  echo "configure-docker-daemon: docker restart failed after daemon.json update." >&2
  systemctl status docker --no-pager >&2 || true
  journalctl -u docker --no-pager -n 100 >&2 || true
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "configure-docker-daemon: docker did not become usable after restart." >&2
  systemctl status docker --no-pager >&2 || true
  journalctl -u docker --no-pager -n 100 >&2 || true
  exit 1
fi

echo "configure-docker-daemon: docker restarted with daemon hardening config."
