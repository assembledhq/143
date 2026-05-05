#!/usr/bin/env bash
set -euo pipefail

# Refresh the deploy user's narrow NOPASSWD sudoers grant on an already-running
# host without tearing down containers. This is the no-reprovision repair path
# for legacy hosts that predate newer deploy-time sudo call sites.
#
# Usage:
#   repair-deploy-sudoers.sh <role> <host> <ssh-key-path>

if [ "$#" -ne 3 ]; then
  echo "Usage: $0 <role> <host> <ssh-key-path>" >&2
  exit 2
fi

ROLE="$1"
HOST="$2"
SSH_KEY="$3"

case "$ROLE" in
  app|worker|db|logging|redis) ;;
  *)
    echo "Unknown role: $ROLE (expected: app, worker, db, logging, redis)" >&2
    exit 2
    ;;
esac

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

echo "Repairing deploy sudoers for role=$ROLE on $HOST via root SSH..."
ssh "${SSH_OPTS[@]}" root@"$HOST" "ROLE=$ROLE" bash <<'REMOTE'
set -euo pipefail

TMP="$(mktemp /etc/sudoers.d/99-deploy.XXXXXX)"
trap 'rm -f "$TMP"' EXIT

case "$ROLE" in
  app|worker)
    cat > "$TMP" <<'SUDOERS'
Cmnd_Alias DEPLOY_CMDS = \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/scripts, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/vmalert, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/grafana, \
    /usr/bin/runsc install -- --ignore-cgroups --host-uds=open, \
    /usr/bin/systemctl restart docker, \
    /usr/bin/apt-get install -y --no-install-recommends iptables-persistent, \
    /opt/143/deploy/scripts/sandbox-firewall.sh 143-sandbox, \
    /opt/143/deploy/scripts/install-log-rotation.sh *

deploy ALL=(root) NOPASSWD: DEPLOY_CMDS
SUDOERS
    ;;
  logging)
    cat > "$TMP" <<'SUDOERS'
Cmnd_Alias DEPLOY_CMDS = \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/scripts, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/vmalert, \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/grafana, \
    /usr/bin/systemctl restart docker, \
    /opt/143/deploy/scripts/install-log-rotation.sh *

deploy ALL=(root) NOPASSWD: DEPLOY_CMDS
SUDOERS
    ;;
  db|redis)
    cat > "$TMP" <<'SUDOERS'
Cmnd_Alias DEPLOY_CMDS = \
    /usr/bin/chown -R deploy\:deploy /opt/143/deploy/scripts, \
    /usr/bin/systemctl restart docker, \
    /opt/143/deploy/scripts/install-log-rotation.sh *

deploy ALL=(root) NOPASSWD: DEPLOY_CMDS
SUDOERS
    ;;
esac

chmod 440 "$TMP"
visudo -cf "$TMP"
mv "$TMP" /etc/sudoers.d/99-deploy
trap - EXIT

if id deploy >/dev/null 2>&1; then
  for path in \
    /opt/143/deploy/scripts \
    /opt/143/deploy/vmalert \
    /opt/143/deploy/grafana
  do
    [ -e "$path" ] && chown -R deploy:deploy "$path"
  done
fi

echo "Deploy sudoers repaired for $ROLE."
REMOTE
