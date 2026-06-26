#!/bin/sh
# preview-start.sh — dogfood preview server entrypoint.
#
# Runs inside the 143 preview sandbox for this repo. Ordered as:
#   1. ensure binaries exist (normally already built by the build phase —
#      .143/preview-build.sh, wired as the server service's `build` command)
#   2. run migrations
#   3. seed the database
#   4. generate + cache SESSION_SECRET at $SCRATCH_DIR/session_secret so
#      a server restart inside the same sandbox keeps the reviewer signed in
#   5. exec the server
#
# Compilation deliberately happens in the dedicated build phase, not here: a
# cold `go build` of ./cmd/server takes minutes, and at start time that runs
# against the readiness probe's clock and times out the whole preview. The build
# phase (see .143/preview-build.sh) has a 30-minute budget and persists the Go
# cache even on failure, so it warms subsequent launches. The binaries it
# produces live under $SCRATCH_DIR on the container rootfs, which persists into
# this start phase (same sandbox, same $HOME).
#
# Scratch lives under $HOME on the container's writable rootfs, NOT in $TMPDIR.
# The sandbox mounts /tmp (256 MiB) and /var/tmp (512 MiB) as tmpfs (see
# internal/services/agent/providers/docker.go); keeping artifacts off tmpfs also
# frees that footprint from the container's memory cgroup (tmpfs pages count
# against the memory limit), giving the running server more heap headroom.
#
# The rootfs is wiped automatically when the container is removed — both on the
# happy path (Destroy) and the crash path (orphan reconciler), so no explicit
# cleanup is needed here. Size is bounded by SANDBOX_DISK_LIMIT_GB (default
# 10 GB) on hosts whose Docker storage driver supports project quotas (overlay2
# + XFS with pquota); on hosts without that, providers/docker.go silently
# disables the quota and the only ceiling is host disk. Prod is provisioned with
# the supported driver; dev hosts without it should treat the cap as advisory.
#
# Cleanup invariant: this path must stay on a non-bind-mounted directory. Today
# $HOME and the repo workdir are both rootfs-only (the only host binds are
# /etc/resolv.conf and the auth-socket dir; see Mounts in providers/docker.go).
# If a future change bind-mounts $HOME from the host, move SCRATCH_DIR to a path
# that's still on the rootfs — otherwise build artifacts will outlive the sandbox.
#
# -u catches typos in required env vars (DATABASE_URL, PREVIEW_ORIGIN) instead of
# silently substituting empty strings and failing downstream.
set -eu

# Fall back to /home/sandbox if HOME is somehow unset; the sandbox image bakes
# that as the user's home so it's the right default. The fallback matches HomeDir
# in internal/services/agent/adapter.go.
SCRATCH_DIR="${HOME:-/home/sandbox}/.cache/143-preview"
SECRET_FILE="${SCRATCH_DIR}/session_secret"
MIGRATE_BIN="${SCRATCH_DIR}/143-migrate"
SERVER_BIN="${SCRATCH_DIR}/143-server"

mkdir -p "$SCRATCH_DIR"
# Lock down the scratch dir up-front so anything dropped here later (notably
# SECRET_FILE) inherits a sandbox-only parent. SECRET_FILE itself is also
# chmod 600 below; this is defense in depth, not the primary control.
chmod 700 "$SCRATCH_DIR"

# Normally the build phase already produced these. Fall back to building now if
# they're missing — a platform without build-phase support, or a manual run.
if [ ! -x "$MIGRATE_BIN" ] || [ ! -x "$SERVER_BIN" ]; then
    echo '[143-preview] binaries missing; building inline...'
    sh "$(dirname "$0")/preview-build.sh"
fi

echo '[143-preview] running migrations...'
"$MIGRATE_BIN" up

echo '[143-preview] seeding database...'
for seed_file in .143/seed/*.sql; do
    if [ ! -f "$seed_file" ]; then
        echo '[143-preview] no seed SQL fragments found in .143/seed'
        exit 1
    fi
    psql -v ON_ERROR_STOP=1 "$DATABASE_URL" -f "$seed_file"
done

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
