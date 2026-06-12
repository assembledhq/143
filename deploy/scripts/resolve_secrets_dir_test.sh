#!/usr/bin/env bash
# Tests for resolve-secrets-dir.sh: the explicit override, the
# main-checkout sibling resolved through a linked worktree, and the
# non-git fallback. Run directly: bash deploy/scripts/resolve_secrets_dir_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RESOLVER="$SCRIPT_DIR/resolve-secrets-dir.sh"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# Normalize a path whose final component may not exist (the 143-infra
# sibling usually doesn't in tests) by canonicalizing its parent.
norm() {
  printf '%s/%s\n' "$(cd "$(dirname "$1")" && pwd -P)" "$(basename "$1")"
}

# Tier 1: explicit SECRETS_DIR wins, verbatim.
got="$(SECRETS_DIR=/custom/secrets "$RESOLVER" "$TMP_DIR")"
[ "$got" = "/custom/secrets" ] || fail "override: got '$got', want '/custom/secrets'"

# Tier 2: from inside a linked worktree, resolve to a sibling of the MAIN
# checkout (worktrees share the main repo's git dir).
MAIN_REPO="$TMP_DIR/main-repo"
git init -q -b main "$MAIN_REPO"
git -C "$MAIN_REPO" -c user.email=t@t.invalid -c user.name=t \
  commit -q --allow-empty -m init
git -C "$MAIN_REPO" worktree add -q "$TMP_DIR/linked-wt" -b wt-branch

got="$(env -u SECRETS_DIR "$RESOLVER" "$TMP_DIR/linked-wt")"
want="$MAIN_REPO/../143-infra"
[ "$(norm "$got")" = "$(norm "$want")" ] \
  || fail "worktree: got '$got', want sibling of main checkout '$want'"

# Tier 2, main checkout itself: same answer.
got="$(env -u SECRETS_DIR "$RESOLVER" "$MAIN_REPO")"
[ "$(norm "$got")" = "$(norm "$want")" ] \
  || fail "main checkout: got '$got', want '$want'"

# Tier 3: a non-git project dir falls back to its own sibling.
# GIT_CEILING_DIRECTORIES stops git from discovering any repo above TMP_DIR.
mkdir "$TMP_DIR/plain"
got="$(env -u SECRETS_DIR GIT_CEILING_DIRECTORIES="$TMP_DIR" "$RESOLVER" "$TMP_DIR/plain")"
[ "$got" = "$TMP_DIR/plain/../143-infra" ] \
  || fail "non-git fallback: got '$got', want '$TMP_DIR/plain/../143-infra'"

echo "PASS: resolve-secrets-dir.sh resolution tiers"
