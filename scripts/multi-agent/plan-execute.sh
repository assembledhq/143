#!/usr/bin/env bash
#
# plan-execute.sh — Read a PLAN.md, create worktrees, and dispatch tasks to agents.
#
# Usage:
#   ./scripts/multi-agent/plan-execute.sh [path/to/PLAN.md]
#
# This script:
#   1. Parses PLAN.md to extract tasks with their agent assignments
#   2. Creates a git worktree per task for isolation
#   3. Launches each agent in its worktree with the task description
#   4. Reports status as agents complete
#
# Requirements:
#   - git (with worktree support)
#   - claude (Claude Code CLI) — for claude-code tasks
#   - codex (Codex CLI) — for codex tasks
#   - jq (for JSON parsing, optional)

set -euo pipefail

PLAN_FILE="${1:-PLAN.md}"
WORKTREE_DIR=".worktrees"
BASE_BRANCH="$(git branch --show-current)"
PLAN_NAME=""
PIDS=()
TASK_BRANCHES=()

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

log_info()  { echo -e "${BLUE}[info]${NC} $*"; }
log_ok()    { echo -e "${GREEN}[ok]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[warn]${NC} $*"; }
log_error() { echo -e "${RED}[error]${NC} $*"; }
log_agent() { echo -e "${CYAN}[$1]${NC} $2"; }

# --- Validation ---

if [ ! -f "$PLAN_FILE" ]; then
    log_error "Plan file not found: $PLAN_FILE"
    echo ""
    echo "Create a PLAN.md using the template:"
    echo "  cp scripts/multi-agent/PLAN.template.md PLAN.md"
    echo "  \$EDITOR PLAN.md"
    exit 1
fi

if ! command -v git &> /dev/null; then
    log_error "git is required"
    exit 1
fi

# --- Parse PLAN.md ---

# Extract plan name from the title line: "# Plan: <name>"
PLAN_NAME=$(grep -m1 '^# Plan:' "$PLAN_FILE" | sed 's/^# Plan: *//' | tr '[:upper:]' '[:lower:]' | tr ' ' '-' | tr -cd 'a-z0-9-')

if [ -z "$PLAN_NAME" ]; then
    PLAN_NAME="unnamed-plan"
    log_warn "No plan name found in $PLAN_FILE, using '$PLAN_NAME'"
fi

log_info "Plan: $PLAN_NAME"
log_info "Base branch: $BASE_BRANCH"
echo ""

# Parse tasks from PLAN.md
# We look for ### Task N: <description> followed by - **Agent**: <agent>
parse_tasks() {
    local current_task=""
    local current_agent=""
    local current_desc=""
    local current_files=""
    local current_notes=""
    local in_notes=false
    local in_acceptance=false
    local acceptance=""

    while IFS= read -r line; do
        # New task header
        if [[ "$line" =~ ^###[[:space:]]+Task[[:space:]]+([0-9]+):[[:space:]]+(.*) ]]; then
            # Emit previous task if exists
            if [ -n "$current_task" ] && [ -n "$current_agent" ]; then
                echo "TASK|$current_task|$current_agent|$current_desc|$current_files|$acceptance|$current_notes"
            fi
            current_task="${BASH_REMATCH[1]}"
            current_desc="${BASH_REMATCH[2]}"
            current_agent=""
            current_files=""
            current_notes=""
            acceptance=""
            in_notes=false
            in_acceptance=false
        fi

        # Agent assignment
        if [[ "$line" =~ ^-[[:space:]]+\*\*Agent\*\*:[[:space:]]+(.*) ]]; then
            current_agent=$(echo "${BASH_REMATCH[1]}" | tr -d '`' | xargs)
        fi

        # Files
        if [[ "$line" =~ ^-[[:space:]]+\*\*Files\*\*:[[:space:]]+(.*) ]]; then
            current_files="${BASH_REMATCH[1]}"
        fi

        # Acceptance criteria
        if [[ "$line" =~ ^-[[:space:]]+\*\*Acceptance[[:space:]]+criteria\*\*: ]]; then
            in_acceptance=true
            in_notes=false
            continue
        fi

        if $in_acceptance && [[ "$line" =~ ^[[:space:]]*-[[:space:]]+\[.\][[:space:]]+(.*) ]]; then
            if [ -n "$acceptance" ]; then
                acceptance="$acceptance; ${BASH_REMATCH[1]}"
            else
                acceptance="${BASH_REMATCH[1]}"
            fi
        fi

        # Notes
        if [[ "$line" =~ ^-[[:space:]]+\*\*Notes\*\*:[[:space:]]+(.*) ]]; then
            current_notes="${BASH_REMATCH[1]}"
            in_notes=true
            in_acceptance=false
        fi

        # End of task section (next heading or constraints)
        if [[ "$line" =~ ^##[[:space:]] ]] && [[ ! "$line" =~ ^### ]]; then
            in_notes=false
            in_acceptance=false
        fi

    done < "$PLAN_FILE"

    # Emit last task
    if [ -n "$current_task" ] && [ -n "$current_agent" ]; then
        echo "TASK|$current_task|$current_agent|$current_desc|$current_files|$acceptance|$current_notes"
    fi
}

# --- Create worktrees and dispatch ---

dispatch_task() {
    local task_num="$1"
    local agent="$2"
    local description="$3"
    local files="$4"
    local acceptance="$5"
    local notes="$6"

    local branch="plan/${PLAN_NAME}/${agent}/task-${task_num}"
    local worktree_path="${WORKTREE_DIR}/${agent}-task-${task_num}"

    # Create branch from current HEAD
    if git show-ref --verify --quiet "refs/heads/$branch" 2>/dev/null; then
        log_warn "Branch $branch already exists, reusing"
    else
        git branch "$branch" HEAD
    fi

    # Create worktree
    if [ -d "$worktree_path" ]; then
        log_warn "Worktree $worktree_path already exists, removing"
        git worktree remove "$worktree_path" --force 2>/dev/null || true
    fi

    git worktree add "$worktree_path" "$branch" --quiet
    log_ok "Created worktree: $worktree_path (branch: $branch)"

    TASK_BRANCHES+=("$branch")

    # Build the prompt for the agent
    local prompt="You are working on a specific task from a multi-agent plan.

## Your Task
Task ${task_num}: ${description}

## Files to modify
${files}

## Acceptance Criteria
${acceptance}

## Additional Notes
${notes}

## Important
- Only modify files listed above. Do not touch other files.
- Run tests after making changes.
- Commit your changes with a clear message referencing Task ${task_num}.
- Do not push to remote — the orchestrator handles that."

    # Dispatch to the right agent
    case "$agent" in
        claude-code|claude)
            if ! command -v claude &> /dev/null; then
                log_error "Claude Code CLI not found. Install: https://docs.anthropic.com/en/docs/claude-code"
                return 1
            fi
            log_agent "claude" "Starting Task $task_num: $description"
            (
                cd "$worktree_path"
                claude --print "$prompt" > "../${agent}-task-${task_num}.log" 2>&1
            ) &
            PIDS+=($!)
            ;;

        codex)
            if ! command -v codex &> /dev/null; then
                log_error "Codex CLI not found. Install: https://github.com/openai/codex"
                return 1
            fi
            log_agent "codex" "Starting Task $task_num: $description"
            (
                cd "$worktree_path"
                codex --quiet "$prompt" > "../${agent}-task-${task_num}.log" 2>&1
            ) &
            PIDS+=($!)
            ;;

        any)
            # Default to claude-code for "any" agent tasks
            if command -v claude &> /dev/null; then
                log_agent "claude" "Starting Task $task_num (assigned: any → claude): $description"
                (
                    cd "$worktree_path"
                    claude --print "$prompt" > "../${agent}-task-${task_num}.log" 2>&1
                ) &
                PIDS+=($!)
            elif command -v codex &> /dev/null; then
                log_agent "codex" "Starting Task $task_num (assigned: any → codex): $description"
                (
                    cd "$worktree_path"
                    codex --quiet "$prompt" > "../${agent}-task-${task_num}.log" 2>&1
                ) &
                PIDS+=($!)
            else
                log_error "No agent CLI found. Install claude or codex."
                return 1
            fi
            ;;

        *)
            log_warn "Unknown agent '$agent' for Task $task_num, skipping"
            return 0
            ;;
    esac
}

# --- Main ---

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Multi-Agent Plan Execution"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# Parse and display tasks
TASKS=$(parse_tasks)
TASK_COUNT=$(echo "$TASKS" | grep -c "^TASK|" || true)

if [ "$TASK_COUNT" -eq 0 ]; then
    log_error "No tasks found in $PLAN_FILE"
    echo "Make sure tasks follow the format:"
    echo "  ### Task 1: <description>"
    echo "  - **Agent**: claude-code"
    exit 1
fi

log_info "Found $TASK_COUNT task(s)"
echo ""

# Display task summary
echo "┌──────────┬────────────┬──────────────────────────────────────────┐"
printf "│ %-8s │ %-10s │ %-40s │\n" "Task" "Agent" "Description"
echo "├──────────┼────────────┼──────────────────────────────────────────┤"
while IFS='|' read -r _ num agent desc _files _acc _notes; do
    printf "│ %-8s │ %-10s │ %-40.40s │\n" "Task $num" "$agent" "$desc"
done <<< "$TASKS"
echo "└──────────┴────────────┴──────────────────────────────────────────┘"
echo ""

# Confirm before executing
read -r -p "Execute plan? [y/N] " confirm
if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
    log_info "Aborted."
    exit 0
fi

echo ""
mkdir -p "$WORKTREE_DIR"

# Dispatch all tasks
while IFS='|' read -r _ num agent desc files acceptance notes; do
    dispatch_task "$num" "$agent" "$desc" "$files" "$acceptance" "$notes"
done <<< "$TASKS"

echo ""
log_info "Waiting for ${#PIDS[@]} agent(s) to complete..."
echo ""

# Wait for all agents
FAILED=0
for pid in "${PIDS[@]}"; do
    if ! wait "$pid"; then
        FAILED=$((FAILED + 1))
    fi
done

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Results"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

if [ "$FAILED" -gt 0 ]; then
    log_warn "$FAILED task(s) failed. Check logs in $WORKTREE_DIR/"
else
    log_ok "All tasks completed successfully"
fi

echo ""
log_info "Branches created:"
for branch in "${TASK_BRANCHES[@]}"; do
    echo "  - $branch"
done

echo ""
log_info "Next steps:"
echo "  1. Review each branch:"
for branch in "${TASK_BRANCHES[@]}"; do
    echo "     git diff ${BASE_BRANCH}..${branch}"
done
echo "  2. Merge into your feature branch:"
echo "     git checkout $BASE_BRANCH"
for branch in "${TASK_BRANCHES[@]}"; do
    echo "     git merge $branch"
done
echo "  3. Clean up worktrees:"
echo "     git worktree prune"
echo ""
