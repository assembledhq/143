#!/usr/bin/env bash
set -euo pipefail

# Installs the worker-side WireGuard tunnel and policy routing for the
# opt-in static egress sandbox bridge. This script intentionally handles only
# packets sourced from STATIC_EGRESS_SUBNET; ordinary host and sandbox traffic
# keeps the existing route.

WG_INTERFACE="${STATIC_EGRESS_WG_INTERFACE:-wg-static-egress}"
WG_PRIVATE_KEY="${STATIC_EGRESS_WORKER_PRIVATE_KEY:?STATIC_EGRESS_WORKER_PRIVATE_KEY is required}"
WG_ADDRESS="${STATIC_EGRESS_WORKER_WG_ADDRESS:?STATIC_EGRESS_WORKER_WG_ADDRESS is required, e.g. 10.143.0.2/32}"
WG_PEER_PUBLIC_KEY="${STATIC_EGRESS_GATEWAY_PUBLIC_KEY:?STATIC_EGRESS_GATEWAY_PUBLIC_KEY is required}"
WG_ENDPOINT="${STATIC_EGRESS_GATEWAY_PUBLIC_IP:?STATIC_EGRESS_GATEWAY_PUBLIC_IP is required}:${STATIC_EGRESS_GATEWAY_WG_PORT:-51820}"
PUBLIC_IP="${STATIC_EGRESS_PUBLIC_IP:?STATIC_EGRESS_PUBLIC_IP is required}"
STATIC_EGRESS_SUBNET="${STATIC_EGRESS_SUBNET:-172.31.0.0/24}"
TABLE_ID="${STATIC_EGRESS_ROUTE_TABLE:-143}"
FWMARK="${STATIC_EGRESS_FWMARK:-0x143}"
CAPABILITY_FILE="${STATIC_EGRESS_CAPABILITY_FILE:-/etc/143/static-egress-capable}"
PROBE_URL="${STATIC_EGRESS_PROBE_URL:-https://api.ipify.org}"

apt-get update >/dev/null
apt-get install -y --no-install-recommends wireguard iproute2 iptables curl >/dev/null

install -d -m 700 /etc/wireguard
umask 077
cat > "/etc/wireguard/${WG_INTERFACE}.conf" <<WGCONF
[Interface]
PrivateKey = ${WG_PRIVATE_KEY}
Address = ${WG_ADDRESS}
Table = off

[Peer]
PublicKey = ${WG_PEER_PUBLIC_KEY}
AllowedIPs = 0.0.0.0/0
Endpoint = ${WG_ENDPOINT}
PersistentKeepalive = 25
WGCONF

systemctl enable --now "wg-quick@${WG_INTERFACE}"

iptables -t mangle -N STATIC_EGRESS_MARK 2>/dev/null || true
iptables -t mangle -F STATIC_EGRESS_MARK
iptables -t mangle -C PREROUTING -s "$STATIC_EGRESS_SUBNET" -j STATIC_EGRESS_MARK 2>/dev/null ||
  iptables -t mangle -A PREROUTING -s "$STATIC_EGRESS_SUBNET" -j STATIC_EGRESS_MARK
iptables -t mangle -A STATIC_EGRESS_MARK -j MARK --set-mark "$FWMARK"

ip rule replace fwmark "$FWMARK" table "$TABLE_ID"
ip route replace default dev "$WG_INTERFACE" table "$TABLE_ID"

# Hide bridge subnet reuse from the gateway. Gateway ACLs can still restrict
# which worker WireGuard peers are accepted.
iptables -t nat -C POSTROUTING -s "$STATIC_EGRESS_SUBNET" -o "$WG_INTERFACE" -j MASQUERADE 2>/dev/null ||
  iptables -t nat -A POSTROUTING -s "$STATIC_EGRESS_SUBNET" -o "$WG_INTERFACE" -j MASQUERADE

if command -v netfilter-persistent >/dev/null 2>&1; then
  netfilter-persistent save >/dev/null
fi

ip link show "$WG_INTERFACE" >/dev/null
ip rule show | grep -F "fwmark $FWMARK" | grep -F "lookup $TABLE_ID" >/dev/null
ip route show table "$TABLE_ID" | grep -F "dev $WG_INTERFACE" >/dev/null

if [ "${STATIC_EGRESS_SKIP_PROBES:-false}" != "true" ]; then
  observed_ip="$(curl --interface "$WG_INTERFACE" -fsS --max-time 10 "$PROBE_URL" | tr -d '[:space:]')"
  if [ "$observed_ip" != "$PUBLIC_IP" ]; then
    echo "ERROR: static egress probe returned '$observed_ip', expected '$PUBLIC_IP'." >&2
    exit 1
  fi
fi

install -d -m 755 "$(dirname "$CAPABILITY_FILE")"
tmp_capability="${CAPABILITY_FILE}.tmp"
cat > "$tmp_capability" <<CAPABILITY
public_ip=${PUBLIC_IP}
network=${STATIC_EGRESS_NETWORK:-143-sandbox-static-egress}
CAPABILITY
chmod 644 "$tmp_capability"
mv -f "$tmp_capability" "$CAPABILITY_FILE"

echo "Installed static egress worker routing for ${STATIC_EGRESS_SUBNET} via ${WG_INTERFACE}."
