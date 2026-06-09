#!/usr/bin/env bash
set -euo pipefail

# Installs the worker-side WireGuard tunnel and policy routing for the
# opt-in static egress sandbox bridge. This script intentionally handles only
# packets sourced from STATIC_EGRESS_SUBNET; ordinary host and sandbox traffic
# keeps the existing route.

WG_INTERFACE="${STATIC_EGRESS_WG_INTERFACE:-wg-egress}"
LEGACY_WG_INTERFACES="wg-static-egress wgstegress"
WG_PRIVATE_KEY="${STATIC_EGRESS_WORKER_PRIVATE_KEY:?STATIC_EGRESS_WORKER_PRIVATE_KEY is required}"
WG_ADDRESS="${STATIC_EGRESS_WORKER_WG_ADDRESS:?STATIC_EGRESS_WORKER_WG_ADDRESS is required, e.g. 10.143.0.2/32}"
WG_PEER_PUBLIC_KEY="${STATIC_EGRESS_GATEWAY_PUBLIC_KEY:?STATIC_EGRESS_GATEWAY_PUBLIC_KEY is required}"
WG_ENDPOINT="${STATIC_EGRESS_GATEWAY_PUBLIC_IP:?STATIC_EGRESS_GATEWAY_PUBLIC_IP is required}:${STATIC_EGRESS_GATEWAY_WG_PORT:-51820}"
PUBLIC_IP="${STATIC_EGRESS_PUBLIC_IP:?STATIC_EGRESS_PUBLIC_IP is required}"
STATIC_EGRESS_NETWORK="${STATIC_EGRESS_NETWORK:-143-sandbox-static-egress}"
STATIC_EGRESS_SUBNET="${STATIC_EGRESS_SUBNET:-172.31.0.0/24}"
STATIC_EGRESS_DNS_IP="${STATIC_EGRESS_DNS_IP:-172.31.0.2}"
TABLE_ID="${STATIC_EGRESS_ROUTE_TABLE:-143}"
FWMARK="${STATIC_EGRESS_FWMARK:-0x143}"
CAPABILITY_FILE="/etc/143/static-egress-capable"
PROBE_URL="${STATIC_EGRESS_PROBE_URL:-https://api.ipify.org}"
PROBE_IMAGE="${STATIC_EGRESS_PROBE_IMAGE:-ghcr.io/assembledhq/143-sandbox:latest}"
PROBE_TIMEOUT="${STATIC_EGRESS_PROBE_TIMEOUT_SECONDS:-10}"

if [ "${#WG_INTERFACE}" -gt 15 ]; then
  echo "ERROR: STATIC_EGRESS_WG_INTERFACE '$WG_INTERFACE' is too long; Linux interface names must be 15 characters or fewer." >&2
  exit 1
fi
case "$WG_INTERFACE" in
  ""|.|..|*/*)
    echo "ERROR: STATIC_EGRESS_WG_INTERFACE '$WG_INTERFACE' is not a valid WireGuard interface name." >&2
    exit 1
    ;;
esac

apt-get update >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends wireguard iproute2 iptables iptables-persistent >/dev/null

rm -f "$CAPABILITY_FILE"

install -d -m 700 /etc/wireguard
umask 077
cat > "/etc/wireguard/${WG_INTERFACE}.conf" <<WGCONF
[Interface]
PrivateKey = ${WG_PRIVATE_KEY}
Address = ${WG_ADDRESS}
Table = off
PostUp = ip rule del fwmark ${FWMARK} table ${TABLE_ID} 2>/dev/null || true; ip rule add fwmark ${FWMARK} table ${TABLE_ID}; ip route replace default dev %i table ${TABLE_ID}
PostDown = ip rule del fwmark ${FWMARK} table ${TABLE_ID} 2>/dev/null || true; ip route flush table ${TABLE_ID} 2>/dev/null || true

[Peer]
PublicKey = ${WG_PEER_PUBLIC_KEY}
AllowedIPs = 0.0.0.0/0
Endpoint = ${WG_ENDPOINT}
PersistentKeepalive = 25
WGCONF

for legacy_interface in $LEGACY_WG_INTERFACES; do
  if [ "$WG_INTERFACE" != "$legacy_interface" ]; then
    systemctl disable --now "wg-quick@${legacy_interface}" >/dev/null 2>&1 || true
    rm -f "/etc/wireguard/${legacy_interface}.conf"
  fi
done
systemctl enable "wg-quick@${WG_INTERFACE}" >/dev/null
systemctl restart "wg-quick@${WG_INTERFACE}"

iptables -t mangle -N STATIC_EGRESS_MARK 2>/dev/null || true
iptables -t mangle -F STATIC_EGRESS_MARK
iptables -t mangle -C PREROUTING -s "$STATIC_EGRESS_SUBNET" -j STATIC_EGRESS_MARK 2>/dev/null ||
  iptables -t mangle -A PREROUTING -s "$STATIC_EGRESS_SUBNET" -j STATIC_EGRESS_MARK
iptables -t mangle -A STATIC_EGRESS_MARK -j MARK --set-mark "$FWMARK"

# Hide bridge subnet reuse from the gateway. Gateway ACLs can still restrict
# which worker WireGuard peers are accepted.
iptables -t nat -C POSTROUTING -s "$STATIC_EGRESS_SUBNET" -o "$WG_INTERFACE" -j MASQUERADE 2>/dev/null ||
  iptables -t nat -A POSTROUTING -s "$STATIC_EGRESS_SUBNET" -o "$WG_INTERFACE" -j MASQUERADE

if ! command -v netfilter-persistent >/dev/null 2>&1; then
  echo "ERROR: netfilter-persistent is required to persist static egress firewall rules." >&2
  exit 1
fi
netfilter-persistent save >/dev/null

ip link show "$WG_INTERFACE" >/dev/null
ip rule show | grep -F "fwmark $FWMARK" | grep -F "lookup $TABLE_ID" >/dev/null
ip route show table "$TABLE_ID" | grep -F "dev $WG_INTERFACE" >/dev/null

if [ "${STATIC_EGRESS_SKIP_PROBES:-false}" != "true" ]; then
  if ! docker image inspect "$PROBE_IMAGE" >/dev/null 2>&1; then
    docker pull "$PROBE_IMAGE" >/dev/null
  fi
  if ! observed_ip="$(docker run --rm --network "$STATIC_EGRESS_NETWORK" \
    --name "143-static-egress-probe-$$" \
    --dns "$STATIC_EGRESS_DNS_IP" \
    --pull never \
    --cap-drop ALL \
    --security-opt no-new-privileges \
    "$PROBE_IMAGE" \
    /bin/sh -c 'url="$2"; host="${url#*://}"; host="${host%%/*}"; host="${host%%:*}"; getent hosts "$host" >/dev/null; curl -fsS --max-time "$1" "$2" | tr -d "[:space:]"' \
    sh "$PROBE_TIMEOUT" "$PROBE_URL")"; then
    echo "ERROR: static egress probe container failed on network '$STATIC_EGRESS_NETWORK' using image '$PROBE_IMAGE'." >&2
    echo "       Ensure the probe image is present locally or set STATIC_EGRESS_PROBE_IMAGE." >&2
    exit 1
  fi
  if [ "$observed_ip" != "$PUBLIC_IP" ]; then
    echo "ERROR: static egress probe returned '$observed_ip', expected '$PUBLIC_IP'." >&2
    exit 1
  fi
fi

install -d -m 755 "$(dirname "$CAPABILITY_FILE")"
tmp_capability="${CAPABILITY_FILE}.tmp"
cat > "$tmp_capability" <<CAPABILITY
public_ip=${PUBLIC_IP}
network=143-sandbox-static-egress
CAPABILITY
chmod 644 "$tmp_capability"
mv -f "$tmp_capability" "$CAPABILITY_FILE"

echo "Installed static egress worker routing for ${STATIC_EGRESS_SUBNET} via ${WG_INTERFACE}."
