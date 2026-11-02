#!/usr/bin/env bash
# Probe the configured sink from the running collector, including host-local
# environment overrides. Local Vector API health does not prove sink access.
set -euo pipefail

if [ "${1:-}" = --compose ]; then
  : "${2:?compose file required}"
  probe_name="143-vector-sink-probe-$$-$RANDOM-$RANDOM"
  cleanup_probe() {
    local status=$?
    trap - EXIT INT TERM
    # Killing the Compose CLI does not guarantee container removal. Keep the
    # candidate named and remove it explicitly, with a separate hard deadline.
    if ! timeout --kill-after=2s 5s docker rm -f "$probe_name" >/dev/null 2>&1; then
      echo 'ERROR: temporary Vector sink probe cleanup failed or timed out; inspect named 143-vector-sink-probe containers.' >&2
      status=1
    fi
    exit "$status"
  }
  trap cleanup_probe EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  probe=(docker compose -f "$2" run --name "$probe_name" -T --no-deps --entrypoint sh vector)
else
  cid="${1:-}"
  if [ -z "$cid" ]; then
    echo 'ERROR: Vector container not found; cannot validate log sink connectivity.' >&2
    exit 1
  fi
  probe=(docker exec "$cid" sh)
fi

echo 'Checking configured log sink connectivity from Vector (10s deadline, 2s kill grace)...'
if timeout --kill-after=2s 10s "${probe[@]}" -ec '
  if [ -z "${VICTORIALOGS_HOST:-}" ]; then
    echo "ERROR: Vector container has no configured VICTORIALOGS_HOST." >&2
    exit 1
  fi
  # wget is included in the pinned Alpine Vector image and used by its local
  # healthcheck. Suppress its URL/error output to avoid disclosing routing.
  if ! wget -q -T 5 -O /dev/null "http://${VICTORIALOGS_HOST}:9428/health" >/dev/null 2>&1; then
    echo "ERROR: configured log sink is unreachable from Vector." >&2
    exit 1
  fi
' < /dev/null; then
  echo 'Configured log sink is reachable from Vector (reachability only; ingestion not verified).'
else
  echo 'ERROR: Vector sink connectivity validation failed or exceeded its deadline; local collector health is insufficient.' >&2
  exit 1
fi
