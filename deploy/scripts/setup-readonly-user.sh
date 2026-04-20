#!/usr/bin/env bash
set -euo pipefail

# Apply the readonly Postgres role to the prod DB. Idempotent — safe to rerun
# (reruns rotate the readonly password to match DB_READONLY_PASSWORD).
#
# Driven by `make setup-readonly-user`, which resolves DB_HOST from
# FLEET_HOSTS and DB_PASSWORD / DB_READONLY_PASSWORD from .env.production.enc.
#
# Required env:
#   DB_HOST                (IP or hostname of the db fleet node)
#   DB_PASSWORD            (admin password for the onefortythree role)
#   DB_READONLY_PASSWORD   (password to set on the readonly role)
# Optional env:
#   SSH_KEY                (default ~/.ssh/143-deploy)
#   POSTGRES_CONTAINER     (default 143-postgres-1)
#   DB_NAME / DB_ADMIN_USER (defaults: onefortythree / onefortythree)

: "${DB_HOST:?DB_HOST required}"
: "${DB_PASSWORD:?DB_PASSWORD required (admin password)}"
: "${DB_READONLY_PASSWORD:?DB_READONLY_PASSWORD required}"

SSH_KEY="${SSH_KEY:-$HOME/.ssh/143-deploy}"
CONTAINER="${POSTGRES_CONTAINER:-143-postgres-1}"
DB_NAME="${DB_NAME:-onefortythree}"
DB_ADMIN_USER="${DB_ADMIN_USER:-onefortythree}"

SQL_FILE="$(cd "$(dirname "$0")" && pwd)/setup-readonly-user.sql"
if [[ ! -f "$SQL_FILE" ]]; then
  echo "ERROR: SQL file not found: $SQL_FILE" >&2
  exit 1
fi

echo "Applying readonly role on $DB_HOST (container: $CONTAINER)"

# Stream the SQL file over SSH to psql running inside the postgres container.
# PGPASSWORD and the psql -v variable land in the remote process's arg list
# for the short command lifetime; acceptable since that host is already
# trusted with the DB admin credentials.
ssh -i "$SSH_KEY" -o LogLevel=ERROR "deploy@$DB_HOST" \
  "docker exec -i -e PGPASSWORD='$DB_PASSWORD' '$CONTAINER' \
     psql -U '$DB_ADMIN_USER' -d '$DB_NAME' \
       -v ON_ERROR_STOP=1 \
       -v ropass='$DB_READONLY_PASSWORD' \
       -v dbname='$DB_NAME' \
       -v owner='$DB_ADMIN_USER'" \
  < "$SQL_FILE"

echo "Readonly role applied. Test with: make db-query Q='SELECT current_user'"
