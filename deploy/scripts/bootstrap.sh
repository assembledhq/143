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

# Detached worker rollovers (WORKER_DEPLOY_DETACH=1) write progress + status
# files here. /var/log is root-owned so the deploy user can't mkdir it
# itself; provision once with deploy ownership.
mkdir -p /var/log/143
chown deploy:deploy /var/log/143

# Docker (idempotent)
command -v docker &>/dev/null || (curl -fsSL https://get.docker.com | sh)

# Add deploy to docker group (must be after Docker install creates the group)
usermod -aG docker deploy

# Narrow NOPASSWD sudo for the deploy user. Each entry mirrors a sudo call
# site in deploy/scripts/deploy.sh — keep in sync when adding new ones.
# Defence-in-depth, not a hard isolation boundary: deploy is also in the
# docker group (root-equivalent via `docker run -v /:/host`) and owns the
# sandbox-firewall.sh that root executes. Listing commands explicitly still
# blocks the obvious blunders (`sudo bash`, `sudo cat /etc/shadow`) and
# makes the privilege grant auditable.
cat > /etc/sudoers.d/99-deploy <<'SUDOERS'
Cmnd_Alias DEPLOY_CMDS = \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/scripts, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/vmalert, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/grafana, \
    /usr/bin/runsc install -- --ignore-cgroups --host-uds=open, \
    /usr/bin/systemctl restart docker, \
    /usr/bin/apt-get install -y --no-install-recommends iptables-persistent, \
    /opt/143/deploy/scripts/sandbox-firewall.sh 143-sandbox, \
    /opt/143/deploy/scripts/sandbox-resolv-conf.sh, \
    /opt/143/deploy/scripts/install-log-rotation.sh *, \
    /opt/143/deploy/scripts/install-docker-dns.sh *

deploy ALL=(root) NOPASSWD: DEPLOY_CMDS
SUDOERS
chmod 440 /etc/sudoers.d/99-deploy
visudo -cf /etc/sudoers.d/99-deploy

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
