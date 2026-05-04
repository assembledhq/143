#!/bin/sh
# preview-start.sh — dogfood preview server entrypoint.
#
# Runs inside the 143 preview sandbox for this repo. Ordered as:
#   1. build binaries once (go run would rebuild twice for migrate+server)
#   2. run migrations
#   3. seed the database
#   4. generate + cache SESSION_SECRET at $SCRATCH_DIR/session_secret so
#      a server restart inside the same sandbox keeps the reviewer signed in
#   5. exec the server
#
# Everything lives under $TMPDIR (= /var/tmp in the sandbox), not /tmp:
# the sandbox mounts /tmp as a 256 MiB noexec tmpfs but /var/tmp as a
# 512 MiB exec-allowed tmpfs (see internal/services/agent/providers/docker.go).
# Writing the GOCACHE + binaries to /tmp ran the small tmpfs out of space
# while the Go build was copying its final a.out, and noexec would have
# blocked exec'ing the resulting binary anyway. SESSION_SECRET still lives
# on a tmpfs, so a sandbox recycle still resets it and the reviewer simply
# re-signs-in with the public demo credentials.
#
# -u catches typos in required env vars (DATABASE_URL, PREVIEW_ORIGIN)
# instead of silently substituting empty strings and failing downstream.
set -eu

SCRATCH_DIR="${TMPDIR:-/var/tmp}/143-preview"
SECRET_FILE="${SCRATCH_DIR}/session_secret"
MIGRATE_BIN="${SCRATCH_DIR}/143-migrate"
SERVER_BIN="${SCRATCH_DIR}/143-server"

mkdir -p "$SCRATCH_DIR"

# Persist the Go build cache across in-sandbox server restarts so rebuilds
# after a code edit reuse object files instead of recompiling the world.
# A full sandbox recycle wipes the tmpfs and pays the cold-build cost once.
GOCACHE="${SCRATCH_DIR}/gocache"
export GOCACHE
mkdir -p "$GOCACHE"

echo '[143-preview] building binaries...'
# Merge stderr into stdout so `go build` compile errors are captured by the
# preview executor's output tail and surfaced in the launch error. Without
# this, the executor discards stderr (see docker_preview.go) and the user
# only sees the "building binaries..." line followed by "exited with code 1".
go build -o "$MIGRATE_BIN" ./cmd/migrate 2>&1
go build -o "$SERVER_BIN" ./cmd/server 2>&1

echo '[143-preview] running migrations...'
"$MIGRATE_BIN" up

echo '[143-preview] seeding database...'
psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f .143/seed.sql

chmod 700 "$SCRATCH_DIR"
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
exec "$SERVER_BIN"
