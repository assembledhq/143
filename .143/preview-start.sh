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
# Scratch lives under $HOME on the container's writable rootfs, NOT in
# $TMPDIR. The sandbox mounts /tmp (256 MiB) and /var/tmp (512 MiB) as
# tmpfs (see internal/services/agent/providers/docker.go), and the Go
# build's GOCACHE + $WORK + the migrate/server output binaries together
# blow past 512 MiB while compiling deps like aws-sdk-go-v2/service/s3.
# Moving them off tmpfs also frees that footprint from the container's
# memory cgroup (tmpfs pages count against the memory limit per the
# Tmpfs comment in providers/docker.go), giving the running server more
# heap headroom.
#
# The rootfs is wiped automatically when the container is removed — both
# on the happy path (Destroy) and the crash path (orphan reconciler), so
# no explicit cleanup is needed here. Size is bounded by
# SANDBOX_DISK_LIMIT_GB (default 10 GB) on hosts whose Docker storage
# driver supports project quotas (overlay2 + XFS with pquota); on hosts
# without that, providers/docker.go silently disables the quota and the
# only ceiling is host disk. Prod is provisioned with the supported
# driver; dev hosts without it should treat the cap as advisory.
#
# Cleanup invariant: this path must stay on a non-bind-mounted directory.
# Today $HOME and the repo workdir are both rootfs-only (the only host
# binds are /etc/resolv.conf and the auth-socket dir; see Mounts in
# providers/docker.go). If a future change bind-mounts $HOME from the
# host, move SCRATCH_DIR to a path that's still on the rootfs — otherwise
# build artifacts will outlive the sandbox.
#
# GOTMPDIR is set explicitly so `go build`'s intermediate $WORK files
# don't fall back to the ambient GOTMPDIR=/var/tmp set by docker.go,
# which would put the largest churn back on the small tmpfs.
#
# -u catches typos in required env vars (DATABASE_URL, PREVIEW_ORIGIN)
# instead of silently substituting empty strings and failing downstream.
set -eu

# Fall back to /home/sandbox if HOME is somehow unset; the sandbox image
# bakes that as the user's home so it's the right default. The fallback
# matches HomeDir in internal/services/agent/adapter.go.
SCRATCH_DIR="${HOME:-/home/sandbox}/.cache/143-preview"
SECRET_FILE="${SCRATCH_DIR}/session_secret"
MIGRATE_BIN="${SCRATCH_DIR}/143-migrate"
SERVER_BIN="${SCRATCH_DIR}/143-server"

mkdir -p "$SCRATCH_DIR"
# Lock down the scratch dir up-front so anything dropped here later
# (notably SECRET_FILE) inherits a sandbox-only parent. SECRET_FILE
# itself is also chmod 600 below; this is defense in depth, not the
# primary control.
chmod 700 "$SCRATCH_DIR"

# Persist the Go build cache and intermediate $WORK dir across in-sandbox
# server restarts so rebuilds after a code edit reuse object files
# instead of recompiling the world. A full sandbox recycle wipes the
# rootfs and pays the cold-build cost once.
GOCACHE="${SCRATCH_DIR}/gocache"
GOTMPDIR="${SCRATCH_DIR}/gotmp"
export GOCACHE GOTMPDIR
mkdir -p "$GOCACHE" "$GOTMPDIR"

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
