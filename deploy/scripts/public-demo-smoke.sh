#!/usr/bin/env bash
set -euo pipefail

# Smoke-test a public demo URL.
# Usage: deploy/scripts/public-demo-smoke.sh [https://demo.143.dev]

BASE_URL="${1:-https://demo.143.dev}"
BASE_URL="${BASE_URL%/}"
COOKIE_JAR="$(mktemp)"
trap 'rm -f "$COOKIE_JAR"' EXIT

echo "Smoking $BASE_URL..."

providers="$(curl -fsS -c "$COOKIE_JAR" "$BASE_URL/api/v1/auth/providers")"
printf '%s' "$providers" | grep -q '"demo":true' || {
  echo "ERROR: auth providers did not advertise demo mode" >&2
  exit 1
}
if printf '%s' "$providers" | grep -q 'demo_password\|demo_email'; then
  echo "ERROR: auth providers exposed demo credentials" >&2
  exit 1
fi

csrf_token="$(awk '$6 == "csrf_token" { print $7 }' "$COOKIE_JAR" | tail -1)"
if [ -z "$csrf_token" ]; then
  echo "ERROR: providers did not set csrf_token cookie" >&2
  exit 1
fi

demo_login="$(curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
  -H "X-CSRF-Token: $csrf_token" \
  -X POST "$BASE_URL/api/v1/auth/demo")"
printf '%s' "$demo_login" | grep -q '"email":"preview-viewer@143.dev"' || {
  echo "ERROR: direct demo entry did not sign in as preview viewer" >&2
  exit 1
}

me="$(curl -fsS -b "$COOKIE_JAR" "$BASE_URL/api/v1/auth/me")"
printf '%s' "$me" | grep -q '"email":"preview-viewer@143.dev"' || {
  echo "ERROR: /auth/me did not return preview viewer" >&2
  exit 1
}

manifest="$(curl -fsS -b "$COOKIE_JAR" "$BASE_URL/api/v1/demo/manifest")"
printf '%s' "$manifest" | grep -q '"session_id":"00000000-0000-4000-a000-000000000300"' || {
  echo "ERROR: demo manifest did not return the primary seeded session" >&2
  exit 1
}

blocked_body="$(mktemp)"
blocked_status="$(curl -sS -o "$blocked_body" -w "%{http_code}" -b "$COOKIE_JAR" \
  -H "Content-Type: application/json" \
  -H "X-CSRF-Token: $csrf_token" \
  -X POST "$BASE_URL/api/v1/sessions" \
  --data '{}')"
if [ "$blocked_status" != "403" ] || ! grep -q '"code":"DEMO_READ_ONLY"' "$blocked_body"; then
  echo "ERROR: demo write attempt was not blocked with DEMO_READ_ONLY" >&2
  cat "$blocked_body" >&2
  rm -f "$blocked_body"
  exit 1
fi
rm -f "$blocked_body"

echo "Public demo smoke passed."
