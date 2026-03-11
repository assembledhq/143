#!/usr/bin/env bash
set -euo pipefail

# If SOPS_AGE_KEY is set, decrypt .env.production.enc and export the values.
# This lets Render (or any deploy target) store a single secret — the age
# private key — while all other secrets live in the encrypted file in git.
if [ -n "${SOPS_AGE_KEY:-}" ] && [ -f .env.production.enc ]; then
  export SOPS_AGE_KEY_FILE=/tmp/age-key.txt
  echo "$SOPS_AGE_KEY" > "$SOPS_AGE_KEY_FILE"
  chmod 600 "$SOPS_AGE_KEY_FILE"

  # sops exec-env decrypts the file and sets each key=value pair as an
  # environment variable, then runs the given command.  This avoids writing
  # plaintext secrets to disk and sidesteps shell-quoting issues with values
  # that contain spaces or newlines (e.g. RSA private keys).
  #
  # However, sops exec-env OVERRIDES any existing env vars with the same name.
  # Platform-provided vars (e.g. Render's DATABASE_URL from `fromDatabase`)
  # must take precedence over stale values in the encrypted file.  We extract
  # the plaintext keys from the encrypted file, save any that are already set,
  # and re-export them after sops exec-env runs.
  #
  # exec-env doesn't support --input-type, so we symlink to a .env extension
  # that sops auto-detects as dotenv format.
  RESTORE_CMDS=""
  while IFS='=' read -r key _; do
    # Skip sops metadata, empty lines, and comments.
    [ -z "$key" ] && continue
    [[ "$key" == sops_* ]] && continue
    [[ "$key" == \#* ]] && continue
    # If this key is already set in the environment, build an export command
    # to restore it after sops exec-env overrides it.
    if [ -n "${!key+x}" ]; then
      RESTORE_CMDS="${RESTORE_CMDS}export $(printf '%s=%q; ' "$key" "${!key}")"
    fi
  done < .env.production.enc

  ln -sf "$(realpath .env.production.enc)" /tmp/.env.production.sops.env
  CMD=$(printf '%q ' "$@")
  exec sops exec-env /tmp/.env.production.sops.env \
    "${RESTORE_CMDS}unset SOPS_AGE_KEY SOPS_AGE_KEY_FILE && rm -f '$SOPS_AGE_KEY_FILE' /tmp/.env.production.sops.env && exec $CMD"
fi

exec "$@"
