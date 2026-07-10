#!/bin/sh
# preview-build-frontend.sh — dogfood preview frontend BUILD phase.
#
# Runs the Next production build during the platform-managed build phase, not
# at service start. Previously the build lived in preview-frontend.sh (the
# service command) and ran `next build` at readiness time — inside the same
# sandbox memory cgroup as the Go server, the DB seed, and the readiness probe.
# `next build` peaks at several GB, so with everything sharing one cap the
# sandbox OOM-killed (exit 137) before the frontend ever became ready. Building
# here isolates that peak from the run phase, and the launch hot path becomes
# "just start the prebuilt standalone server" (see preview-frontend.sh).
#
# -u catches typos in required env vars, matching preview-frontend.sh.
set -eu

# The install phase (preview.install in .143/config.json) normally populates
# frontend/node_modules before any build runs. Run the install here too as a
# fallback: the lock-hash marker inside node_modules makes this a no-op when the
# platform phase (or a cache restore) already completed, but it keeps a service
# restart in a fresh sandbox from failing with "next: not found" (exit 127).
sh .143/preview-install-frontend.sh

cd frontend

echo '[143-preview] building next production bundle...'
npm run build

echo '[143-preview] staging next standalone static assets...'
# Next's standalone output includes the minimal server and traced Node
# dependencies, but it does not copy generated CSS/JS chunks or public assets.
# The production Dockerfile performs these copies into /app; the preview runs
# the generated server from .next/standalone/frontend, so stage them there.
rm -rf .next/standalone/frontend/.next/static .next/standalone/frontend/public
mkdir -p .next/standalone/frontend/.next
cp -R .next/static .next/standalone/frontend/.next/static
cp -R public .next/standalone/frontend/public

echo '[143-preview] next standalone bundle ready'
