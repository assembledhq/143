package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// This file holds request-validation helpers used by automations.go. They were
// extracted so the main handler file stays focused on HTTP-level routing and
// response shaping — mixing field-level validation in kept that file creeping
// past the 800-line mark where handler files in this codebase start to become
// hard to scan.

// automationNameMaxLength and automationGoalMaxLength mirror the
// chk_automations_name_length and chk_automations_goal_length CHECK
// constraints. Keeping them at this layer surfaces a 10MB body as a 400 rather
// than a Postgres constraint violation — the user-legible error.
const (
	automationNameMaxLength = 200
	automationGoalMaxLength = 4000
)

// validExecutionModes mirrors the chk_automations_execution_mode CHECK constraint.
var validExecutionModes = map[string]bool{
	"sequential":       true,
	"parallel":         true,
	"dependency_graph": true,
}

func validateAutomationNameAndGoal(name, goal string) error {
	if len(name) > automationNameMaxLength {
		return fmt.Errorf("name must be at most %d characters", automationNameMaxLength)
	}
	if len(goal) > automationGoalMaxLength {
		return fmt.Errorf("goal must be at most %d characters", automationGoalMaxLength)
	}
	return nil
}

// validateBaseBranch rejects branch names that obviously can't be refs:
// empty/whitespace, path traversal, or embedded whitespace. Intentionally
// conservative — libgit2 has stricter rules but applying them here would
// duplicate logic we'd have to keep in sync with git's rules. The callsite
// (repo checkout) will fail loudly on anything we let through.
func validateBaseBranch(b string) error {
	trimmed := strings.TrimSpace(b)
	if trimmed == "" {
		return fmt.Errorf("base_branch must not be empty")
	}
	if trimmed != b {
		return fmt.Errorf("base_branch must not contain leading/trailing whitespace")
	}
	if strings.ContainsAny(b, " \t\n\r") {
		return fmt.Errorf("base_branch must not contain whitespace")
	}
	if strings.Contains(b, "..") {
		return fmt.Errorf("base_branch must not contain '..'")
	}
	return nil
}

// validateTimezone rejects strings that time.LoadLocation can't parse. Without
// this, a malformed timezone would be silently stored and later fail at
// schedule evaluation time — far from the user's write.
func validateTimezone(tz string) error {
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid timezone %q", tz)
	}
	return nil
}

// resolveRepositoryID parses a repository_id from a request and verifies it
// belongs to orgID and is still active. Returns nil + nil for empty input.
// Errors are user-safe and can be returned directly from handlers; the
// errRepoDisconnected sentinel (defined in repo_active.go) lets handlers
// distinguish disconnected repos so they can return REPO_DISCONNECTED.
func (h *AutomationHandler) resolveRepositoryID(ctx context.Context, orgID uuid.UUID, raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid repository_id")
	}
	if _, err := requireActiveRepo(ctx, h.repoStore, orgID, parsed); err != nil {
		switch {
		case errors.Is(err, errRepoDisconnected):
			return nil, errRepoDisconnected
		case errors.Is(err, errRepoStoreUnconfigured):
			return nil, fmt.Errorf("repository lookup not configured")
		default:
			return nil, fmt.Errorf("repository not found in this org")
		}
	}
	return &parsed, nil
}
