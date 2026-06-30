#!/usr/bin/env bash
set -euo pipefail

# Sync authorized public keys from $SECRETS_DIR/deploy/authorized_keys/*.pub to remote servers.
# Usage: ./sync-keys.sh [--apply] <ssh-key-path> <host1> [host2] ...
#
# Replaces /home/deploy/.ssh/authorized_keys on each target host with the keys
# from the private secrets repo. By default runs in dry-run mode (shows diff
# without changing anything). Pass --apply to actually push changes.

APPLY=false
if [ "${1:-}" = "--apply" ]; then
  APPLY=true
  shift
fi

if [ "$#" -lt 2 ]; then
  echo "Usage: $0 [--apply] <ssh-key-path> <host1> [host2] ..."
  exit 1
fi

SSH_KEY="$1"
shift
HOSTS=("$@")

if [ ${#HOSTS[@]} -eq 0 ]; then
  echo "Usage: $0 [--apply] <ssh-key-path> <host1> [host2] ..."
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

# Collect keys with guaranteed trailing newline per entry, sorted for stable diff
AUTHORIZED_KEYS=""
for f in "${PUB_FILES[@]}"; do
  AUTHORIZED_KEYS+="$(cat "$f")"$'\n'
done
AUTHORIZED_KEYS="$(printf '%s' "$AUTHORIZED_KEYS" | sort)"

KEY_COUNT=$(printf '%s' "$AUTHORIZED_KEYS" | grep -c . || true)
echo "Found $KEY_COUNT key(s) in $KEYS_DIR"
echo "Using SSH_KEY=$SSH_KEY"

if [ "$APPLY" = false ]; then
  echo ""
  echo "DRY RUN — showing what would change (pass --apply to execute)"
  echo ""
fi

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$SSH_KEY")

FAILED=0
for HOST in "${HOSTS[@]}"; do
  echo "--- deploy@$HOST ---"

  # Fetch current keys from remote
  REMOTE_KEYS="$(ssh "${SSH_OPTS[@]}" deploy@"$HOST" 'cat ~/.ssh/authorized_keys 2>/dev/null | sort' || true)"

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
    printf '%s\n' "$AUTHORIZED_KEYS" | ssh "${SSH_OPTS[@]}" deploy@"$HOST" \
      'mkdir -p ~/.ssh && TMP=$(mktemp) && cat > "$TMP" && mv "$TMP" ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && chmod 700 ~/.ssh'
    if [ $? -ne 0 ]; then
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
