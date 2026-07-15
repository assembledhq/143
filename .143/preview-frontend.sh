#!/bin/sh
# preview-frontend.sh — dogfood preview frontend entrypoint (RUN phase only).
#
# The production build is produced in the platform build phase by
# .143/preview-build-frontend.sh, so the launch hot path here just starts the
# prebuilt Next standalone server. Building at readiness time (as this script
# used to) put the multi-GB `next build` peak in the same sandbox memory cgroup
# as the running server + DB seed + readiness probe and OOM-killed the sandbox
# (exit 137).
#
# The preview serves a production build instead of the Next dev server. The dev
# server's HMR endpoint streams framework internals through the preview gateway
# and can leave App Router pages partially hydrated with raw Flight payload
# visible in the document. Production mode removes HMR from the browser path and
# matches the way reviewers inspect dogfood previews.
#
# -u catches typos in required env vars (HOST, NODE_OPTIONS) the same way
# preview-start.sh does for DATABASE_URL.
set -eu

# Restart safety: a service restart can land in a sandbox whose build output is
# gone (e.g. a fresh sandbox restored from a snapshot, which never tars
# .next/standalone). Rebuild before serving so a restart never 404s. On the
# normal launch path the build phase already produced this, so the check is a
# cheap no-op.
if [ ! -f frontend/.next/standalone/frontend/server.js ]; then
  echo '[143-preview] standalone build missing; building now (restart fallback)...'
  sh .143/preview-build-frontend.sh
fi

cd frontend

echo '[143-preview] starting next production server...'
# The frontend config emits Next's standalone server for production builds.
# Run that server directly: `next start` warns and is not the supported path
# when output is set to standalone. The preview worker proxies to the sandbox
# container IP, so the server must bind all interfaces, not just loopback.
export HOSTNAME=0.0.0.0
export PORT="${PORT:-3000}"
exec node .next/standalone/frontend/server.js
