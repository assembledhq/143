#!/usr/bin/env bash
set -euo pipefail

# Single source of truth for /etc/143/sandbox-resolv.conf — the resolv.conf
# bind-mounted into every sandbox at /etc/resolv.conf.
#
# Why a sandbox-specific resolv.conf at all: gVisor's netstack can't reach
# Docker's embedded resolver at 127.0.0.11, so the auto-injected nameserver
# on user-defined networks doesn't resolve from inside a runsc sandbox. The
# sandbox-dns sidecar (a non-gVisor container at the static IP below) sits
# on the same bridge, forwards queries to 127.0.0.11 from a namespace where
# it's reachable, and answers preview-infrastructure container names like
# preview-db-<handle> — public DNS continues to flow through dnsmasq's
# upstream forwarders.
#
# Why this lives in its own script: provision.sh runs once per host, but the
# file's content can change in a routine PR (e.g. PR #815 swapped 1.1.1.1 →
# 172.30.0.2). If the only writer is provision.sh, every content change
# requires the operator to schedule a fleet-wide reprovision maintenance
# window — and a deploy that lands the new code without that step leaves
# every worker silently misconfigured (sandboxes resolve preview-db names
# against 1.1.1.1, get NXDOMAIN, and previews fail at the migrator). Both
# provision.sh and deploy.sh call this script so the file stays current via
# normal deploys; existing sandboxes pick up the change on their next
# create because the bind-mount reads the host file at lookup time.
#
# Usage: sudo /opt/143/deploy/scripts/sandbox-resolv-conf.sh
#
# Atomic write: tee to a sibling .tmp and rename(2). An in-place truncate-
# then-write would expose sandboxes to an empty resolv.conf for the few
# microseconds between the truncate and the close — under preview churn
# during a deploy, that window is enough to fail a sandbox's first DNS
# lookup. rename(2) swaps the inode atomically.
#
# IP must agree with:
#   - docker-compose.worker.yml      (sandbox-dns ipv4_address)
#   - deploy/scripts/provision.sh    (subnet --subnet 172.30.0.0/24)
# TestSandboxDNSConfigAlignment in deploy/deploy_config_test.go locks the
# alignment in CI; update all three together.

DEST=/etc/143/sandbox-resolv.conf
TMP="${DEST}.tmp"

mkdir -p "$(dirname "$DEST")"

cat > "$TMP" <<RESOLV
nameserver 172.30.0.2
options edns0 trust-ad ndots:0
RESOLV

chmod 644 "$TMP"
mv -f "$TMP" "$DEST"
