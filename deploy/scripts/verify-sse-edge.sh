#!/usr/bin/env bash
set -euo pipefail

: "${BASE_URL:?set BASE_URL to the production-equivalent HTTPS edge origin}"
: "${ORG_ID:?set ORG_ID to an authorized test organization UUID}"
: "${SESSION_COOKIE:?set SESSION_COOKIE to an authorized session_token value}"

headers_file="${TMPDIR:-/home/sandbox}/143-live-edge-headers.$$"
body_file="${TMPDIR:-/home/sandbox}/143-live-edge-body.$$"
trap 'rm -f "$headers_file" "$body_file"' EXIT

curl --http2 --no-buffer --silent --show-error --max-time 30 \
  --cookie "session_token=${SESSION_COOKIE}" \
  --dump-header "$headers_file" \
  --output "$body_file" \
  "${BASE_URL%/}/api/v1/events/stream?org_id=${ORG_ID}" || status=$?

if [[ "${status:-0}" -ne 0 && "${status:-0}" -ne 28 ]]; then
  echo "SSE edge request failed with curl status ${status}" >&2
  exit "${status}"
fi

grep -Eiq '^content-type: text/event-stream' "$headers_file"
grep -Eiq '^cache-control: .*no-transform' "$headers_file"
grep -Eiq '^x-accel-buffering: no' "$headers_file"
grep -q 'event: live.ready' "$body_file"
grep -q 'event: live.heartbeat' "$body_file"

echo "SSE edge verification passed over HTTP/2 with flushed ready and heartbeat frames"
