#!/usr/bin/env bash
set -euo pipefail

# Sync authorized public keys from $SECRETS_DIR/deploy/authorized_keys/*.pub to remote servers.
# Usage: ./sync-keys.sh [--apply] <ssh-key-path> <host-or-role:host> [host...]
#
# Replaces ~/.ssh/authorized_keys on each target with the keys from the private
# secrets repo. Fleet nodes are deploy@ with <ssh-key-path>. egress:<host>
# entries use EGRESS_SSH_KEY (falling back to <ssh-key-path>) and probe root
# then ubuntu unless EGRESS_SSH_USER is set.
#
# Dry-run by default (shows diff without changing anything). Pass --apply to
# actually push changes.

APPLY=false
if [ "${1:-}" = "--apply" ]; then
  APPLY=true
  shift
fi

if [ "$#" -lt 2 ]; then
  echo "Usage: $0 [--apply] <ssh-key-path> <host-or-role:host> [host...]"
  exit 1
fi

SSH_KEY="$1"
shift
HOST_SPECS=("$@")

if [ ${#HOST_SPECS[@]} -eq 0 ]; then
  echo "Usage: $0 [--apply] <ssh-key-path> <host-or-role:host> [host...]"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
SECRETS_DIR="$("$SCRIPT_DIR/resolve-secrets-dir.sh" "$PROJECT_DIR")"
KEYS_DIR="$SECRETS_DIR/deploy/authorized_keys"

# Collect all .pub files
PUB_FILES=("$KEYS_DIR"/*.pub)
if [ ! -e "${PUB_FILES[0]}" ]; then
  echo "ERROR: No .pub files found in $KEYS_DIR"
  echo "Add public key files to deploy/authorized_keys/ in the private 143-infra repo, or set SECRETS_DIR."
  exit 1
fi

# 143-egress.pub is the AWS/gateway bootstrap public key. It must be present so
# replace-style sync cannot lock out EGRESS_SSH_KEY, but it must not be installed
# on fleet deploy@ hosts — that pem is not a fleet login key.
EGRESS_ONLY_PUB="143-egress.pub"

collect_keys() {
  local include_egress_only="$1"
  local keys="" f base
  for f in "${PUB_FILES[@]}"; do
    base="$(basename "$f")"
    if [ "$include_egress_only" != "true" ] && [ "$base" = "$EGRESS_ONLY_PUB" ]; then
      continue
    fi
    keys+="$(cat "$f")"$'\n'
  done
  printf '%s' "$keys" | sort
}

FLEET_AUTHORIZED_KEYS="$(collect_keys false)"
EGRESS_AUTHORIZED_KEYS="$(collect_keys true)"

if [ -z "$FLEET_AUTHORIZED_KEYS" ]; then
  echo "ERROR: no fleet public keys found in $KEYS_DIR (refusing to sync an empty authorized_keys file)."
  echo "Add at least one non-$EGRESS_ONLY_PUB key, or rename $EGRESS_ONLY_PUB if it is meant for fleet nodes."
  exit 1
fi

KEY_COUNT=$(printf '%s' "$EGRESS_AUTHORIZED_KEYS" | grep -c . || true)
FLEET_KEY_COUNT=$(printf '%s' "$FLEET_AUTHORIZED_KEYS" | grep -c . || true)
echo "Found $KEY_COUNT key(s) in $KEYS_DIR"
if [ "$FLEET_KEY_COUNT" -lt "$KEY_COUNT" ]; then
  echo "$EGRESS_ONLY_PUB is gateway-only and will not be installed on fleet nodes"
fi
echo "Using SSH_KEY=$SSH_KEY"
if [ -n "${EGRESS_SSH_KEY:-}" ] && [ "${EGRESS_SSH_KEY}" != "$SSH_KEY" ]; then
  echo "Using EGRESS_SSH_KEY=$EGRESS_SSH_KEY for egress:<host> entries"
fi

if [ "$APPLY" = false ]; then
  echo ""
  echo "DRY RUN — showing what would change (pass --apply to execute)"
  echo ""
fi

key_id() {
  awk '{print $1" "$2}'
}

# Return 0 if we cannot verify, or if the private key's public half is in AUTHORIZED_KEYS.
# Return 1 if we can verify and the key is missing — replacing authorized_keys would lock us out.
egress_bootstrap_key_present() {
  local key_path="$1"
  local pub pub_id
  pub="$(ssh-keygen -y -f "$key_path" -P "" 2>/dev/null || true)"
  if [ -z "$pub" ]; then
    return 0
  fi
  pub_id="$(printf '%s\n' "$pub" | key_id)"
  printf '%s\n' "$AUTHORIZED_KEYS" | key_id | grep -qxF "$pub_id"
}

resolve_egress_ssh_user() {
  local key="$1"
  local host="$2"
  local user
  if [ -n "${EGRESS_SSH_USER:-}" ]; then
    printf '%s' "$EGRESS_SSH_USER"
    return 0
  fi
  local opts=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o IdentitiesOnly=yes -i "$key")
  for user in root ubuntu; do
    if ssh "${opts[@]}" "$user@$host" true >/dev/null 2>&1; then
      printf '%s' "$user"
      return 0
    fi
  done
  return 1
}

FAILED=0
for SPEC in "${HOST_SPECS[@]}"; do
  SPEC="${SPEC#"${SPEC%%[![:space:]]*}"}"
  SPEC="${SPEC%"${SPEC##*[![:space:]]}"}"
  [ -n "$SPEC" ] || continue

  ROLE=""
  HOST="$SPEC"
  if [[ "$SPEC" == *:* ]]; then
    ROLE="${SPEC%%:*}"
    HOST="${SPEC#*:}"
  fi
  if [ -z "$HOST" ]; then
    echo "ERROR: invalid host spec '$SPEC' (expected host or role:host)."
    FAILED=1
    continue
  fi

  if [ "$ROLE" = "egress" ]; then
    AUTHORIZED_KEYS="$EGRESS_AUTHORIZED_KEYS"
  else
    AUTHORIZED_KEYS="$FLEET_AUTHORIZED_KEYS"
  fi

  ssh_user="deploy"
  ssh_key="$SSH_KEY"
  if [ "$ROLE" = "egress" ]; then
    ssh_key="${EGRESS_SSH_KEY:-$SSH_KEY}"
    if ! ssh_user="$(resolve_egress_ssh_user "$ssh_key" "$HOST")"; then
      echo "--- $HOST (egress) ---"
      echo "ERROR: could not SSH to egress gateway $HOST as root or ubuntu with $ssh_key."
      echo "Set EGRESS_SSH_USER=<user> and/or EGRESS_SSH_KEY=<path>."
      FAILED=1
      continue
    fi
  fi

  if [ -n "$ROLE" ]; then
    echo "--- ${ssh_user}@$HOST ($ROLE) ---"
  else
    echo "--- ${ssh_user}@$HOST ---"
  fi

  if [ "$ROLE" = "egress" ] && ! egress_bootstrap_key_present "$ssh_key"; then
    echo "ERROR: EGRESS_SSH_KEY public key is not in $KEYS_DIR; refusing to replace authorized_keys (would lock out the gateway bootstrap key)."
    echo "Add the AWS/gateway public key as a .pub file in 143-infra/deploy/authorized_keys/."
    FAILED=1
    continue
  fi

  SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o IdentitiesOnly=yes -i "$ssh_key")

  if ! REMOTE_KEYS="$(ssh "${SSH_OPTS[@]}" "${ssh_user}@$HOST" 'cat ~/.ssh/authorized_keys 2>/dev/null | sort')"; then
    echo "ERROR: SSH failed for ${ssh_user}@$HOST."
    FAILED=1
    continue
  fi

  if [ "$AUTHORIZED_KEYS" = "$REMOTE_KEYS" ]; then
    echo "  No changes."
    continue
  fi

  # Show diff: removed lines in red with -, added lines in green with +
  while IFS= read -r line; do
    case "$line" in
      "< "*)  printf '  \033[31m- %s\033[0m\n' "${line#< }" ;;
      "> "*)  printf '  \033[32m+ %s\033[0m\n' "${line#> }" ;;
      ---*)   ;;
      *)      ;;
    esac
  done < <(diff <(printf '%s\n' "$REMOTE_KEYS") <(printf '%s\n' "$AUTHORIZED_KEYS") || true)

  if [ "$APPLY" = true ]; then
    if ! printf '%s\n' "$AUTHORIZED_KEYS" | ssh "${SSH_OPTS[@]}" "${ssh_user}@$HOST" \
      'mkdir -p ~/.ssh && TMP=$(mktemp) && cat > "$TMP" && mv "$TMP" ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && chmod 700 ~/.ssh'; then
      echo "ERROR: failed to apply keys on ${ssh_user}@$HOST."
      FAILED=1
    else
      echo "  Applied."
    fi
  fi
done

if [ "$FAILED" -ne 0 ]; then
  echo "ERROR: One or more hosts failed."
  exit 1
fi

if [ "$APPLY" = true ]; then
  echo "All keys synced."
else
  echo ""
  echo "Dry run complete. Run with --apply to push changes."
fi
