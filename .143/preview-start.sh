#!/bin/sh
# preview-start.sh — dogfood preview server entrypoint.
#
# Runs inside the 143 preview sandbox for this repo. Ordered as:
#   1. build binaries once (go run would rebuild twice for migrate+server)
#   2. run migrations
#   3. seed the database
#   4. generate + cache SESSION_SECRET at /tmp/143-preview/session_secret so
#      a server restart inside the same sandbox keeps the reviewer signed in
#   5. exec the server
#
# SESSION_SECRET intentionally lives in /tmp/ (not committed): a full
# sandbox recycle generates a fresh secret, at which point the reviewer
# simply re-signs-in with the public demo credentials.
#
# -u catches typos in required env vars (DATABASE_URL, PREVIEW_ORIGIN)
# instead of silently substituting empty strings and failing downstream.
set -eu

SECRET_DIR=/tmp/143-preview
SECRET_FILE="${SECRET_DIR}/session_secret"

# Persist the Go build cache across in-sandbox server restarts so rebuilds
# after a code edit reuse object files instead of recompiling the world.
# A full sandbox recycle wipes /tmp and pays the cold-build cost once.
GOCACHE="${SECRET_DIR}/gocache"
export GOCACHE
mkdir -p "$GOCACHE"

echo '[143-preview] building binaries...'
# Merge stderr into stdout so `go build` compile errors are captured by the
# preview executor's output tail and surfaced in the launch error. Without
# this, the executor discards stderr (see docker_preview.go) and the user
# only sees the "building binaries..." line followed by "exited with code 1".
go build -o /tmp/143-migrate ./cmd/migrate 2>&1
go build -o /tmp/143-server ./cmd/server 2>&1

echo '[143-preview] running migrations...'
/tmp/143-migrate up

echo '[143-preview] seeding database...'
psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f .143/seed.sql

mkdir -p "$SECRET_DIR"
chmod 700 "$SECRET_DIR"
if [ ! -s "$SECRET_FILE" ]; then
    # tr strips the trailing newline base64 appends — SESSION_SECRET is
    # consumed as an opaque byte string and a stray \n causes subtle
    # value-mismatch bugs if anything byte-compares it.
    head -c 32 /dev/urandom | base64 | tr -d '\n' > "$SECRET_FILE"
    chmod 600 "$SECRET_FILE"
fi
SESSION_SECRET="$(cat "$SECRET_FILE")"
export SESSION_SECRET

echo '[143-preview] starting server...'
BASE_URL="${PREVIEW_ORIGIN:-http://localhost:8080}" \
FRONTEND_URL="${PREVIEW_ORIGIN:-http://localhost:8080}" \
exec /tmp/143-server
