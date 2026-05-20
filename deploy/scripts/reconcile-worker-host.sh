#!/usr/bin/env bash
set -euo pipefail

# Canonical idempotent worker host invariant reconciliation.
# Safe to run repeatedly as root from cloud-init, provision.sh, deploy.sh,
# or a manual repair command.

SANDBOX_NETWORK="${1:-143-sandbox}"
SANDBOX_SUBNET="172.30.0.0/24"
STATIC_EGRESS_NETWORK="143-sandbox-static-egress"
STATIC_EGRESS_SUBNET="172.31.0.0/24"
STATIC_EGRESS_RESOLV_CONF="/etc/143/sandbox-static-egress-resolv.conf"
STATIC_EGRESS_DNS_IP="172.31.0.2"
STATIC_EGRESS_ENABLED="${STATIC_EGRESS_ENABLED:-false}"
STATIC_EGRESS_CAPABILITY_FILE="/etc/143/static-egress-capable"

if [ "${EUID:-$(id -u)}" -ne 0 ]; then
  echo "ERROR: reconcile-worker-host.sh must run as root." >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is required before reconciling worker host invariants." >&2
  exit 1
fi

ensure_bridge() {
  local network="$1"
  local subnet="$2"
  local existing_subnet

  existing_subnet=$(docker network inspect "$network" \
    -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null || true)
  if [ -z "$existing_subnet" ]; then
    docker network create --driver bridge \
      --subnet "$subnet" \
      --label managed-by=143 "$network"
  elif [ "$existing_subnet" != "$subnet" ]; then
    echo "ERROR: $network network has subnet '$existing_subnet'; expected $subnet." >&2
    echo "  This worker was provisioned before the pinned-subnet change. To upgrade:" >&2
    echo "    1. docker compose -f /opt/143/docker-compose.worker.yml down" >&2
    echo "    2. docker network rm $network" >&2
    echo "    3. Re-run deploy (or provision-worker) for this host." >&2
    echo "  Step 1 will drain in-flight coding turns; plan for a maintenance window." >&2
    exit 1
  fi
}

# Ensure the shared sandbox bridges exist with pinned subnets. The subnets let
# sandbox-dns claim stable addresses in docker-compose.worker.yml. Leave bridge
# ICC at Docker's default so gVisor sandboxes can reach sandbox-dns.
ensure_bridge "$SANDBOX_NETWORK" "$SANDBOX_SUBNET"
ensure_bridge "$STATIC_EGRESS_NETWORK" "$STATIC_EGRESS_SUBNET"

# Install iptables-persistent so the egress block survives reboots. This is
# best-effort because some minimal images prompt or temporarily lack apt locks;
# sandbox-firewall.sh still applies the live rules below.
apt-get install -y --no-install-recommends iptables-persistent >/dev/null 2>&1 || true

if [ -x /opt/143/deploy/scripts/sandbox-firewall.sh ]; then
  /opt/143/deploy/scripts/sandbox-firewall.sh "$SANDBOX_NETWORK"
  /opt/143/deploy/scripts/sandbox-firewall.sh "$STATIC_EGRESS_NETWORK"
else
  echo "ERROR: /opt/143/deploy/scripts/sandbox-firewall.sh is missing or not executable." >&2
  exit 1
fi

if [ -x /opt/143/deploy/scripts/sandbox-resolv-conf.sh ]; then
  /opt/143/deploy/scripts/sandbox-resolv-conf.sh /etc/143/sandbox-resolv.conf 172.30.0.2
  /opt/143/deploy/scripts/sandbox-resolv-conf.sh "$STATIC_EGRESS_RESOLV_CONF" "$STATIC_EGRESS_DNS_IP"
else
  echo "ERROR: /opt/143/deploy/scripts/sandbox-resolv-conf.sh is missing or not executable." >&2
  exit 1
fi

if [ "$STATIC_EGRESS_ENABLED" = "true" ]; then
  if [ ! -x /opt/143/deploy/scripts/install-static-egress-worker.sh ]; then
    echo "ERROR: static egress is enabled but /opt/143/deploy/scripts/install-static-egress-worker.sh is missing or not executable." >&2
    exit 1
  fi
  : "${STATIC_EGRESS_PUBLIC_IP:?STATIC_EGRESS_PUBLIC_IP is required when STATIC_EGRESS_ENABLED=true}"
  : "${STATIC_EGRESS_GATEWAY_PUBLIC_IP:?STATIC_EGRESS_GATEWAY_PUBLIC_IP is required when STATIC_EGRESS_ENABLED=true}"
  : "${STATIC_EGRESS_GATEWAY_PUBLIC_KEY:?STATIC_EGRESS_GATEWAY_PUBLIC_KEY is required when STATIC_EGRESS_ENABLED=true}"
  : "${STATIC_EGRESS_WORKER_PRIVATE_KEY:?STATIC_EGRESS_WORKER_PRIVATE_KEY is required when STATIC_EGRESS_ENABLED=true}"
  : "${STATIC_EGRESS_WORKER_WG_ADDRESS:?STATIC_EGRESS_WORKER_WG_ADDRESS is required when STATIC_EGRESS_ENABLED=true}"
  /opt/143/deploy/scripts/install-static-egress-worker.sh
else
  rm -f "$STATIC_EGRESS_CAPABILITY_FILE"
fi

# /run is tmpfs on systemd hosts, so recreate the sandbox auth socket parent
# on boot via tmpfiles and force the current runtime state now. The explicit
# chown/chmod repairs drift such as Docker auto-creating the bind mount source
# as root:root 0755 before reconciliation ran.
cat > /etc/tmpfiles.d/143-sandbox-auth.conf <<'TMPFILES'
d /var/run/143 0755 root root -
d /var/run/143/sandbox-auth 0750 1000 1000 -
TMPFILES
systemd-tmpfiles --create /etc/tmpfiles.d/143-sandbox-auth.conf
mkdir -p /var/run/143/sandbox-auth
chown 1000:1000 /var/run/143/sandbox-auth
chmod 0750 /var/run/143/sandbox-auth

cat > /etc/sysctl.d/99-worker.conf <<SYSCTL
vm.swappiness = 1
SYSCTL
sysctl -p /etc/sysctl.d/99-worker.conf

echo "Worker host reconciliation complete ($SANDBOX_NETWORK, $STATIC_EGRESS_NETWORK)."
