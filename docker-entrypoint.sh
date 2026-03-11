#!/usr/bin/env bash
set -euo pipefail

# If SOPS_AGE_KEY is set, decrypt .env.production.enc and export the values.
# This lets Render (or any deploy target) store a single secret — the age
# private key — while all other secrets live in the encrypted file in git.
if [ -n "${SOPS_AGE_KEY:-}" ] && [ -f .env.production.enc ]; then
  export SOPS_AGE_KEY_FILE=/tmp/age-key.txt
  echo "$SOPS_AGE_KEY" > "$SOPS_AGE_KEY_FILE"
  chmod 600 "$SOPS_AGE_KEY_FILE"

  sops --decrypt .env.production.enc > /tmp/.env.production
  set -a
  # shellcheck disable=SC1091
  source /tmp/.env.production
  set +a

  # Clean up plaintext secrets from disk
  rm -f /tmp/.env.production "$SOPS_AGE_KEY_FILE"
  unset SOPS_AGE_KEY SOPS_AGE_KEY_FILE
fi

exec "$@"
