#!/bin/sh
# preview-install-frontend.sh — platform-managed install phase for dogfood
# previews.
#
# Declared as preview.install in .143/config.json so the preview dependency
# cache can restore frontend/node_modules (and frontend/.next/cache) before
# this runs — and skip the install entirely when the restored tree satisfies
# verify_paths. The lock-hash marker lives inside node_modules, so a restored
# tree carries it and preview-frontend.sh's fallback install short-circuits
# too.
#
# Keep the marker logic in sync with the fallback block in
# preview-frontend.sh: treating node_modules itself as the success signal
# would let a killed npm ci poison later preview starts.
set -eu

cd frontend

LOCK_HASH="$(sha256sum package-lock.json | awk '{print $1}')"
INSTALL_MARKER="node_modules/.143-npm-ci-lock"

if [ ! -f "$INSTALL_MARKER" ] || [ "$(cat "$INSTALL_MARKER")" != "$LOCK_HASH" ] || [ ! -x node_modules/.bin/next ]; then
    echo '[143-preview] installing frontend deps (npm ci)...'
    rm -rf node_modules
    npm ci --no-audit --no-fund
    printf '%s\n' "$LOCK_HASH" > "$INSTALL_MARKER"
else
    echo '[143-preview] frontend deps already installed for current lockfile'
fi
