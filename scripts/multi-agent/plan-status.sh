#!/usr/bin/env bash
#
# plan-status.sh — Show the status of all tasks in a multi-agent plan.
#
# Usage:
#   ./scripts/multi-agent/plan-status.sh [plan-name]
#
# Shows:
#   - Which branches exist for the plan
#   - Commit status on each branch (ahead/behind base)
#   - Whether worktrees are still active
#   - CI status if available

set -euo pipefail

PLAN_NAME="${1:-}"
BASE_BRANCH="$(git branch --show-current)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
DIM='\033[2m'
NC='\033[0m'

# --- Find plan branches ---

if [ -n "$PLAN_NAME" ]; then
    BRANCHES=$(git branch --list "plan/${PLAN_NAME}/*" | sed 's/^[* ]*//')
else
    # Show all plan branches
    BRANCHES=$(git branch --list "plan/*" | sed 's/^[* ]*//')
fi

if [ -z "$BRANCHES" ]; then
    echo "No plan branches found."
    echo ""
    echo "Run plan-execute.sh first to create plan branches:"
    echo "  ./scripts/multi-agent/plan-execute.sh PLAN.md"
    exit 0
fi

# Group by plan name
declare -A PLANS

while IFS= read -r branch; do
    # Extract plan name: plan/<name>/<agent>/task-<n>
    plan=$(echo "$branch" | cut -d'/' -f2)
    PLANS["$plan"]+="$branch"$'\n'
done <<< "$BRANCHES"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Multi-Agent Plan Status"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

for plan in "${!PLANS[@]}"; do
    echo ""
    echo -e "${CYAN}Plan: ${plan}${NC}"
    echo "─────────────────────────────────────────────"

    while IFS= read -r branch; do
        [ -z "$branch" ] && continue

        # Extract agent and task
        agent=$(echo "$branch" | cut -d'/' -f3)
        task=$(echo "$branch" | cut -d'/' -f4)

        # Count commits ahead of base
        ahead=$(git rev-list --count "${BASE_BRANCH}..${branch}" 2>/dev/null || echo "?")
        behind=$(git rev-list --count "${branch}..${BASE_BRANCH}" 2>/dev/null || echo "?")

        # Check for worktree
        worktree_path=".worktrees/${agent}-${task}"
        has_worktree="no"
        [ -d "$worktree_path" ] && has_worktree="yes"

        # Status indicator
        if [ "$ahead" = "0" ]; then
            status_icon="${DIM}○${NC}"
            status_text="no changes"
        elif [ "$ahead" != "?" ]; then
            status_icon="${GREEN}●${NC}"
            status_text="${ahead} commit(s) ahead"
        else
            status_icon="${YELLOW}?${NC}"
            status_text="unknown"
        fi

        printf "  %b %-12s %-10s  %s" "$status_icon" "$agent" "$task" "$status_text"

        if [ "$has_worktree" = "yes" ]; then
            printf "  ${DIM}[worktree active]${NC}"
        fi

        echo ""

    done <<< "${PLANS[$plan]}"

    echo ""
    echo -e "  ${DIM}Base: ${BASE_BRANCH}${NC}"
done

echo ""

# --- Worktree status ---

ACTIVE_WORKTREES=$(git worktree list --porcelain 2>/dev/null | grep "^worktree" | grep -c ".worktrees" || true)

if [ "$ACTIVE_WORKTREES" -gt 0 ]; then
    echo -e "${BLUE}[info]${NC} $ACTIVE_WORKTREES active worktree(s)"
    echo "  Clean up with: git worktree prune"
fi

echo ""
