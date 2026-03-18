#!/usr/bin/env bash
#
# multi-review.sh — Kick off parallel PR reviews from multiple coding agents.
#
# Usage:
#   ./scripts/multi-agent/multi-review.sh <pr-number> [agents...]
#
# Examples:
#   ./scripts/multi-agent/multi-review.sh 42                    # Review with all available agents
#   ./scripts/multi-agent/multi-review.sh 42 claude codex       # Review with specific agents
#   ./scripts/multi-agent/multi-review.sh 42 claude             # Review with Claude only
#
# This script:
#   1. Fetches the PR diff from GitHub
#   2. Launches reviews from each agent in parallel
#   3. Posts each review as a PR comment
#   4. Posts a summary comment linking all reviews
#
# Requirements:
#   - gh (GitHub CLI, authenticated)
#   - claude (Claude Code CLI) — for Claude reviews
#   - codex (Codex CLI) — for Codex reviews

set -euo pipefail

PR_NUMBER="${1:-}"
shift || true
AGENTS=("${@}")
REVIEW_DIR=$(mktemp -d)
PIDS=()
AGENT_NAMES=()

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info()  { echo -e "${BLUE}[info]${NC} $*"; }
log_ok()    { echo -e "${GREEN}[ok]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[warn]${NC} $*"; }
log_error() { echo -e "${RED}[error]${NC} $*"; }

# --- Validation ---

if [ -z "$PR_NUMBER" ]; then
    echo "Usage: $0 <pr-number> [agents...]"
    echo ""
    echo "Examples:"
    echo "  $0 42                  # All available agents"
    echo "  $0 42 claude codex     # Specific agents"
    exit 1
fi

if ! command -v gh &> /dev/null; then
    log_error "GitHub CLI (gh) is required. Install: https://cli.github.com"
    exit 1
fi

# Auto-detect available agents if none specified
if [ ${#AGENTS[@]} -eq 0 ]; then
    command -v claude &> /dev/null && AGENTS+=("claude")
    command -v codex &> /dev/null && AGENTS+=("codex")

    if [ ${#AGENTS[@]} -eq 0 ]; then
        log_error "No coding agents found. Install claude or codex CLI."
        exit 1
    fi
fi

# --- Fetch PR info ---

log_info "Fetching PR #${PR_NUMBER}..."

PR_TITLE=$(gh pr view "$PR_NUMBER" --json title -q '.title')
PR_BODY=$(gh pr view "$PR_NUMBER" --json body -q '.body')
PR_DIFF=$(gh pr diff "$PR_NUMBER")
PR_FILES=$(gh pr view "$PR_NUMBER" --json files -q '.files[].path' | head -50)
DIFF_LINES=$(echo "$PR_DIFF" | wc -l)

log_info "PR: $PR_TITLE"
log_info "Diff: $DIFF_LINES lines across $(echo "$PR_FILES" | wc -l) files"
echo ""

# Check if the diff is too small for multi-agent review
if [ "$DIFF_LINES" -lt 50 ]; then
    log_warn "Small diff ($DIFF_LINES lines). Consider using a single agent review instead."
    read -r -p "Continue with multi-agent review? [y/N] " confirm
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        exit 0
    fi
fi

# Save diff for agents
echo "$PR_DIFF" > "$REVIEW_DIR/pr-diff.patch"

# --- Build review prompts ---

build_review_prompt() {
    local agent="$1"
    local focus=""

    case "$agent" in
        claude)
            focus="architecture, security vulnerabilities, error handling, design pattern consistency, and performance implications"
            ;;
        codex)
            focus="correctness, edge cases, test coverage, code clarity, and potential bugs"
            ;;
        *)
            focus="code quality, correctness, and best practices"
            ;;
    esac

    cat <<PROMPT
You are reviewing Pull Request #${PR_NUMBER}: "${PR_TITLE}"

## PR Description
${PR_BODY}

## Files Changed
${PR_FILES}

## Diff
${PR_DIFF}

## Your Review Focus
Focus on: ${focus}

## Review Format
Provide your review in this exact format:

### Summary
One paragraph summarizing the changes and your overall assessment.

### Issues
List any bugs, security issues, or correctness problems. For each issue:
- **File**: \`path/to/file.ext\`
- **Line**: approximate line number
- **Severity**: critical | warning | nit
- **Issue**: description of the problem
- **Suggestion**: how to fix it

If no issues found, write "No issues found."

### Suggestions
Optional improvements that aren't blocking:
- description of suggestion

### Verdict
One of: APPROVE | REQUEST_CHANGES | COMMENT
PROMPT
}

# --- Dispatch reviews ---

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Multi-Agent PR Review"
echo " PR #${PR_NUMBER}: ${PR_TITLE}"
echo " Agents: ${AGENTS[*]}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

for agent in "${AGENTS[@]}"; do
    prompt=$(build_review_prompt "$agent")
    output_file="$REVIEW_DIR/review-${agent}.md"

    case "$agent" in
        claude)
            if ! command -v claude &> /dev/null; then
                log_warn "Claude Code CLI not found, skipping"
                continue
            fi
            log_info "Starting Claude Code review..."
            (
                echo "$prompt" | claude --print > "$output_file" 2>/dev/null
                echo "DONE" > "$REVIEW_DIR/.${agent}-done"
            ) &
            PIDS+=($!)
            AGENT_NAMES+=("claude")
            ;;

        codex)
            if ! command -v codex &> /dev/null; then
                log_warn "Codex CLI not found, skipping"
                continue
            fi
            log_info "Starting Codex review..."
            (
                echo "$prompt" | codex --quiet > "$output_file" 2>/dev/null
                echo "DONE" > "$REVIEW_DIR/.${agent}-done"
            ) &
            PIDS+=($!)
            AGENT_NAMES+=("codex")
            ;;

        *)
            log_warn "Unknown agent '$agent', skipping"
            ;;
    esac
done

if [ ${#PIDS[@]} -eq 0 ]; then
    log_error "No agents were started"
    exit 1
fi

log_info "Waiting for ${#PIDS[@]} review(s)..."
echo ""

# Wait for all reviews
for pid in "${PIDS[@]}"; do
    wait "$pid" || true
done

# --- Post reviews as PR comments ---

post_review_comment() {
    local agent="$1"
    local review_file="$REVIEW_DIR/review-${agent}.md"
    local agent_label=""

    case "$agent" in
        claude) agent_label="Claude Code" ;;
        codex)  agent_label="Codex" ;;
        *)      agent_label="$agent" ;;
    esac

    if [ ! -f "$review_file" ] || [ ! -s "$review_file" ]; then
        log_warn "No review output from $agent_label"
        return 1
    fi

    local review_content
    review_content=$(cat "$review_file")

    local comment_body
    comment_body=$(cat <<EOF
## Review: ${agent_label}

${review_content}

---
*Automated review by ${agent_label} via multi-agent orchestration*
EOF
)

    gh pr comment "$PR_NUMBER" --body "$comment_body"
    log_ok "Posted ${agent_label} review to PR #${PR_NUMBER}"
}

echo ""
log_info "Posting reviews..."

POSTED=0
for agent in "${AGENT_NAMES[@]}"; do
    if post_review_comment "$agent"; then
        POSTED=$((POSTED + 1))
    fi
done

# --- Post summary comment ---

if [ "$POSTED" -gt 1 ]; then
    summary="## Multi-Agent Review Summary\n\n"
    summary+="This PR was reviewed by **${POSTED} agents**: ${AGENT_NAMES[*]}\n\n"
    summary+="Each agent focused on different aspects of the code. See individual review comments above.\n\n"
    summary+="---\n*Generated by multi-agent review orchestration*"

    gh pr comment "$PR_NUMBER" --body "$(echo -e "$summary")"
    log_ok "Posted summary comment"
fi

echo ""
log_ok "Review complete. $POSTED review(s) posted to PR #${PR_NUMBER}"

# Cleanup
rm -rf "$REVIEW_DIR"
