#!/bin/sh
# preview-frontend.sh — dogfood preview frontend entrypoint.
#
# Symmetric with preview-start.sh on the server side: a fresh sandbox
# restored from snapshot has frontend/package.json + package-lock.json but
# no frontend/node_modules (gitignored, never tarred into the snapshot).
# Without an install step here, the npm scripts shell out to `next` via
# node_modules/.bin/next, sh can't find it, and the readiness probe sees
# "exited with code 127". This script makes the install part of the launch
# instead of expecting a side-channel to populate node_modules.
#
# The preview serves a production build instead of the Next dev server. The
# dev server's HMR endpoint streams framework internals through the preview
# gateway and can leave App Router pages partially hydrated with raw Flight
# payload visible in the document. Production mode removes HMR from the
# browser path and matches the way reviewers inspect dogfood previews.
#
# -u catches typos in required env vars (HOST, NODE_OPTIONS) the same way
# preview-start.sh does for DATABASE_URL.
set -eu

# Install normally happens in the platform-managed preview.install phase (see
# .143/config.json), where the dependency cache can restore node_modules
# before any command runs. Run it here too as a fallback for service restarts
# inside a live sandbox; the lock-hash marker inside node_modules makes this
# a no-op when the platform phase (or a cache restore) already completed.
sh .143/preview-install-frontend.sh

cd frontend

echo '[143-preview] building next production bundle...'
npm run build

echo '[143-preview] staging next standalone static assets...'
# Next's standalone output includes the minimal server and traced Node
# dependencies, but it does not copy generated CSS/JS chunks or public assets.
# The production Dockerfile performs these copies into /app; this script runs
# the generated server from .next/standalone/frontend, so stage them there.
rm -rf .next/standalone/frontend/.next/static .next/standalone/frontend/public
mkdir -p .next/standalone/frontend/.next
cp -R .next/static .next/standalone/frontend/.next/static
cp -R public .next/standalone/frontend/public

echo '[143-preview] starting next production server...'
# The frontend config emits Next's standalone server for production builds.
# Run that server directly: `next start` warns and is not the supported path
# when output is set to standalone. The preview worker proxies to the sandbox
# container IP, so the server must bind all interfaces, not just loopback.
export HOSTNAME=0.0.0.0
export PORT="${PORT:-3000}"
exec node .next/standalone/frontend/server.js
