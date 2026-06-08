#!/usr/bin/env bash
set -euo pipefail

# Provisions the static egress gateway data plane. The VM's public IPv4 is the
# customer allowlist address. Tailscale enrollment, when desired, should use
# deploy/scripts/install-tailscale.sh separately for management only.

WG_INTERFACE="${STATIC_EGRESS_WG_INTERFACE:-wg0}"
WG_PORT="${STATIC_EGRESS_GATEWAY_WG_PORT:-51820}"
WG_PRIVATE_KEY="${STATIC_EGRESS_GATEWAY_PRIVATE_KEY:?STATIC_EGRESS_GATEWAY_PRIVATE_KEY is required}"
WG_ADDRESS="${STATIC_EGRESS_GATEWAY_WG_ADDRESS:-10.143.0.1/24}"
WG_TUNNEL_CIDR="${STATIC_EGRESS_GATEWAY_WG_CIDR:-10.143.0.0/24}"
WG_PEERS="${STATIC_EGRESS_WORKER_PEERS:?STATIC_EGRESS_WORKER_PEERS is required as publicKey@allowedIP,...}"
PUBLIC_INTERFACE="${STATIC_EGRESS_PUBLIC_INTERFACE:-$(ip route show default | awk '{print $5; exit}')}"

BLOCKED_DESTS=(
  "169.254.0.0/16"
  "100.64.0.0/10"
  "10.0.0.0/8"
  "172.16.0.0/12"
  "192.168.0.0/16"
)

apt-get update >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends wireguard iptables iproute2 iptables-persistent >/dev/null

install -d -m 700 /etc/wireguard
umask 077
cat > "/etc/wireguard/${WG_INTERFACE}.conf" <<WGCONF
[Interface]
PrivateKey = ${WG_PRIVATE_KEY}
Address = ${WG_ADDRESS}
ListenPort = ${WG_PORT}
WGCONF

IFS=',' read -r -a peers <<< "$WG_PEERS"
for peer in "${peers[@]}"; do
  if [[ "$peer" != *@* ]]; then
    echo "Invalid STATIC_EGRESS_WORKER_PEERS entry '${peer}'; expected publicKey@allowedIP" >&2
    exit 1
  fi
  public_key="${peer%@*}"
  allowed_ip="${peer##*@}"
  if [[ -z "$public_key" || -z "$allowed_ip" ]]; then
    echo "Invalid STATIC_EGRESS_WORKER_PEERS entry '${peer}'; expected publicKey@allowedIP" >&2
    exit 1
  fi
  cat >> "/etc/wireguard/${WG_INTERFACE}.conf" <<WGCONF

[Peer]
PublicKey = ${public_key}
AllowedIPs = ${allowed_ip}
WGCONF
done

cat > /etc/sysctl.d/99-static-egress-gateway.conf <<SYSCTL
net.ipv4.ip_forward = 1
SYSCTL
sysctl -p /etc/sysctl.d/99-static-egress-gateway.conf

systemctl enable "wg-quick@${WG_INTERFACE}" >/dev/null
systemctl restart "wg-quick@${WG_INTERFACE}"

iptables -N STATIC_EGRESS_GUARD 2>/dev/null || true
iptables -F STATIC_EGRESS_GUARD
iptables -C FORWARD -i "$WG_INTERFACE" -j STATIC_EGRESS_GUARD 2>/dev/null ||
  iptables -I FORWARD -i "$WG_INTERFACE" -j STATIC_EGRESS_GUARD

for dest in "${BLOCKED_DESTS[@]}"; do
  iptables -A STATIC_EGRESS_GUARD -d "$dest" -j DROP
done
iptables -A STATIC_EGRESS_GUARD -j ACCEPT

iptables -t nat -C POSTROUTING -s "$WG_TUNNEL_CIDR" -o "$PUBLIC_INTERFACE" -j MASQUERADE 2>/dev/null ||
  iptables -t nat -A POSTROUTING -s "$WG_TUNNEL_CIDR" -o "$PUBLIC_INTERFACE" -j MASQUERADE

netfilter-persistent save >/dev/null

echo "Provisioned static egress gateway on ${WG_INTERFACE}; outbound traffic SNATs through ${PUBLIC_INTERFACE}."
