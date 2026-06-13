#!/usr/bin/env bash
set -euo pipefail

# If SOPS_AGE_KEY is set, decrypt .env.production.enc and export the values.
# The deploy target stores a single secret — the age private key — while all
# other secrets live in the encrypted bundle bind-mounted into the workdir
# from the host (staged there by deploy.sh from the private secrets repo).
if [ -n "${SOPS_AGE_KEY:-}" ] && [ -f .env.production.enc ]; then
  export SOPS_AGE_KEY_FILE=/tmp/age-key.txt
  echo "$SOPS_AGE_KEY" > "$SOPS_AGE_KEY_FILE"
  chmod 600 "$SOPS_AGE_KEY_FILE"

  # sops exec-env decrypts the file and sets each key=value pair as an
  # environment variable, then runs the given command.  This avoids writing
  # plaintext secrets to disk and sidesteps shell-quoting issues with values
  # that contain spaces or newlines (e.g. RSA private keys).
  #
  # exec-env doesn't support --input-type, so we symlink to a .env extension
  # that sops auto-detects as dotenv format.
  ln -sf "$(realpath .env.production.enc)" /tmp/.env.production.sops.env
  CMD=$(printf '%q ' "$@")
  exec sops exec-env /tmp/.env.production.sops.env \
    "unset SOPS_AGE_KEY SOPS_AGE_KEY_FILE && rm -f '$SOPS_AGE_KEY_FILE' /tmp/.env.production.sops.env && exec $CMD"
fi

exec "$@"
