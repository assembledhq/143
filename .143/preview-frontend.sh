#!/bin/sh
# preview-frontend.sh — dogfood preview frontend entrypoint.
#
# Symmetric with preview-start.sh on the server side: a fresh sandbox
# restored from snapshot has frontend/package.json + package-lock.json but
# no frontend/node_modules (gitignored, never tarred into the snapshot).
# Without an install step here, `npm run dev` shells out to `next` via
# node_modules/.bin/next, sh can't find it, and the readiness probe sees
# "exited with code 127". This script makes the install part of the launch
# instead of expecting a side-channel to populate node_modules.
#
# -u catches typos in required env vars (HOST, NODE_OPTIONS) the same way
# preview-start.sh does for DATABASE_URL.
set -eu

cd frontend

# Skip the install on a hot restart inside the same sandbox: once the deps
# are present, `npm ci` would still wipe and reinstall (~30s) for nothing.
# A full sandbox recycle re-enters this branch and pays the cold-install
# cost once, the same way preview-start.sh's gocache works.
if [ ! -d node_modules ]; then
    echo '[143-preview] installing frontend deps (npm ci)...'
    npm ci --no-audit --no-fund
fi

echo '[143-preview] starting next dev server...'
exec npm run dev
