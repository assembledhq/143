#!/bin/sh
# preview-build.sh — compile the dogfood preview binaries.
#
# Wired as the `build` command of the server service in .143/config.json, this
# runs in the preview's dedicated build phase: after the Go build cache is
# restored, before any service starts, and OFF the readiness-probe hot path.
# That matters because a cold `go build` of ./cmd/server downloads ~70 modules
# and compiles the world — several minutes — which blows past the server's
# readiness timeout when it runs at start time. The build phase has its own
# 30-minute budget (defaultServiceBuildTimeout) and its cache is flushed even on
# failure (flushBuildCachesBeforeCleanup), so the first cold build still warms
# every subsequent launch instead of failing the whole preview.
#
# The compiled binaries land under $SCRATCH_DIR on the container rootfs, which
# persists into the start phase (same sandbox, same $HOME). preview-start.sh
# execs them directly and falls back to invoking this script if they're missing.
#
# Go cache locations: in the build phase the platform pins GOCACHE
# ($HOME/.cache/go-build), GOMODCACHE ($HOME/go/pkg/mod), and GOTMPDIR
# ($HOME/.cache/go-build-tmp) — exactly the paths it archives as the home build
# cache (see ResolvePreviewBuildCacheHomePaths). The `:=` defaults below only
# apply when this script is invoked directly (e.g. the preview-start.sh
# fallback), keeping that path pointed at the same restored caches. GOTMPDIR
# must stay off the 512 MiB /var/tmp tmpfs (RAM-backed, counts against the
# memory cgroup) or a large compile overflows it with "no space left on device".
#
# -u catches typos in required env vars instead of silently substituting empty
# strings.
set -eu

HOME_DIR="${HOME:-/home/sandbox}"
SCRATCH_DIR="${HOME_DIR}/.cache/143-preview"
MIGRATE_BIN="${SCRATCH_DIR}/143-migrate"
SERVER_BIN="${SCRATCH_DIR}/143-server"

mkdir -p "$SCRATCH_DIR"
chmod 700 "$SCRATCH_DIR"

: "${GOCACHE:=${HOME_DIR}/.cache/go-build}"
: "${GOMODCACHE:=${HOME_DIR}/go/pkg/mod}"
: "${GOTMPDIR:=${HOME_DIR}/.cache/go-build-tmp}"
export GOCACHE GOMODCACHE GOTMPDIR
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR"

echo '[143-preview] building binaries...'
# Merge stderr into stdout so `go build` compile errors are captured by the
# preview executor's output tail and surfaced in the launch error. Without this,
# the executor discards stderr (see docker_preview.go) and the only visible line
# is "building binaries..." followed by a bare non-zero exit.
go build -o "$MIGRATE_BIN" ./cmd/migrate 2>&1
go build -o "$SERVER_BIN" ./cmd/server 2>&1
