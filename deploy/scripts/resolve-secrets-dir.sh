#!/usr/bin/env bash
# Prints the private secrets checkout path (see docs/secrets/README.md).
#
# Resolution order:
#   1. $SECRETS_DIR if set (explicit override; also how the Makefile and CI
#      pass their value through to the deploy scripts).
#   2. Sibling of the MAIN checkout, located via the shared git dir. Linked
#      worktrees (Claude Code, Codex, Conductor) all share the main repo's
#      .git, so this resolves to the same 143-infra checkout no matter which
#      worktree a script runs from.
#   3. Sibling of the given project dir (non-git contexts, e.g. a bare copy).
set -euo pipefail

project_dir="${1:?usage: resolve-secrets-dir.sh <project_dir>}"

if [ -n "${SECRETS_DIR:-}" ]; then
  printf '%s\n' "$SECRETS_DIR"
  exit 0
fi

git_common_dir="$(git -C "$project_dir" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
if [ -n "$git_common_dir" ]; then
  printf '%s\n' "$(dirname "$git_common_dir")/../143-infra"
else
  printf '%s\n' "$project_dir/../143-infra"
fi
