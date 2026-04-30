#!/usr/bin/env bash
# Idempotent machine setup for app and worker nodes.
# Usage: ssh root@<vps-ip> 'bash -s -- <role>' < deploy/scripts/bootstrap.sh
#        role: app | worker | logging (default: worker)
set -euo pipefail

ROLE="${1:-worker}"

# Create deploy user (idempotent)
id deploy &>/dev/null || adduser --disabled-password --gecos "" deploy
mkdir -p /home/deploy/.ssh /opt/143
[ -f /root/.ssh/authorized_keys ] && cp /root/.ssh/authorized_keys /home/deploy/.ssh/
chown -R deploy:deploy /home/deploy/.ssh /opt/143
chmod 700 /home/deploy/.ssh

# Docker (idempotent)
command -v docker &>/dev/null || (curl -fsSL https://get.docker.com | sh)

# Add deploy to docker group (must be after Docker install creates the group)
usermod -aG docker deploy

# gVisor (idempotent) — only needed on worker nodes for sandbox isolation
if [ "$ROLE" = "worker" ]; then
  command -v runsc &>/dev/null || {
    curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" \
      > /etc/apt/sources.list.d/gvisor.list
    apt-get update && apt-get install -y runsc
    runsc install -- --ignore-cgroups --host-uds=open
    systemctl restart docker
  }
fi

# Kernel tuning
cat > /etc/sysctl.d/99-worker.conf <<SYSCTL
vm.swappiness = 1
SYSCTL
sysctl -p /etc/sysctl.d/99-worker.conf

echo "Bootstrap complete ($ROLE). Machine is ready for deploy."
