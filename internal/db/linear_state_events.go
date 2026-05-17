package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinearStateEventKind names a state-sync trigger so the fire-once unique
// constraint on (session_id, issue_id, event_kind) collapses replays.
type LinearStateEventKind string

const (
	LinearStateEventLinked   LinearStateEventKind = "linked"
	LinearStateEventStarted  LinearStateEventKind = "started"
	LinearStateEventPROpened LinearStateEventKind = "pr_opened"
	LinearStateEventPRMerged LinearStateEventKind = "pr_merged"
	LinearStateEventEnded    LinearStateEventKind = "ended"
	LinearStateEventCanceled LinearStateEventKind = "canceled"
)

// LinearStateSkipReason is the controlled vocabulary stored in
// session_issue_link_state_events.skipped_reason — the audit trail for "why
// didn't Linear update?". Keep in lockstep with the operator debug surface
// so support and dogfooding don't have to guess.
type LinearStateSkipReason string

const (
	// LinearStateSkipAlreadyPastTarget covers transitions skipped because the
	// issue is in or past a terminal state (completed/canceled).
	LinearStateSkipAlreadyPastTarget LinearStateSkipReason = "already_past_target"
	// LinearStateSkipAlreadyInTargetState covers the common no-op case where
	// the issue is already in the desired target state (e.g. already "In
	// Progress" when we open a PR). Distinct from already_past_target so the
	// audit log surfaces "this is fine, nothing to do" vs "we refused".
	LinearStateSkipAlreadyInTargetState    LinearStateSkipReason = "already_in_target_state"
	LinearStateSkipUserRecentEdit          LinearStateSkipReason = "user_recent_edit"
	LinearStateSkipLinearGitHubIntegration LinearStateSkipReason = "linear_github_integration_active"
	LinearStateSkipDisabledByUser          LinearStateSkipReason = "disabled_by_user"
	LinearStateSkipDebounced               LinearStateSkipReason = "debounced"
	LinearStateSkipPrivateSession          LinearStateSkipReason = "private_session"
	LinearStateSkipNotPrimary              LinearStateSkipReason = "not_primary"
	LinearStateSkipPerTeamDisabled         LinearStateSkipReason = "per_team_disabled"
	// LinearStateSkipWorkspaceMismatch is recorded when the workspace_slug
	// persisted on the link no longer matches the workspace_slug returned by
	// FetchIssue at transition time (integration reconnected to a different
	// workspace, or Linear renamed the workspace slug). Distinct from
	// already_past_target so operators chasing a workspace-drift incident
	// don't have to guess from a generic skip.
	LinearStateSkipWorkspaceMismatch LinearStateSkipReason = "workspace_mismatch"
	// LinearStateSkipNoTargetState is recorded when WorkflowStateForType
	// returns no candidate (or one with an empty ID) — a permanent,
	// operator-fixable condition where the target Linear team has no state
	// of the required type (e.g. no `started` state for a PR-open).
	LinearStateSkipNoTargetState LinearStateSkipReason = "no_target_state"
)

// LinearStateEventStore writes the append-only audit/fire-once log of state
// transition decisions on Linear-linked sessions.
type LinearStateEventStore struct {
	db DBTX
}

func NewLinearStateEventStore(db DBTX) *LinearStateEventStore {
	return &LinearStateEventStore{db: db}
}

// LinearStateEventInput is the row to insert. OrgID is supplied separately
// to Insert as an explicit parameter — the lint-stores guardrail requires
// every exported store method to take org scope independently of carrier
// structs that live outside the models package.
type LinearStateEventInput struct {
	SessionID      uuid.UUID
	IssueID        uuid.UUID
	EventKind      LinearStateEventKind
	TransitionFrom string
	TransitionTo   string
	SkippedReason  LinearStateSkipReason
}

// ErrLinearStateEventExists is returned when an event with the same
// (session_id, issue_id, event_kind) already exists. Treat as a no-op:
// the event already fired, replays are idempotent.
var ErrLinearStateEventExists = errors.New("linear state event already exists")

// Insert records a state transition or skip decision. Returns
// ErrLinearStateEventExists when the unique constraint blocks a duplicate.
func (s *LinearStateEventStore) Insert(ctx context.Context, orgID uuid.UUID, in LinearStateEventInput) error {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO session_issue_link_state_events (
			org_id, session_id, issue_id, event_kind,
			transition_from, transition_to, skipped_reason
		)
		VALUES (
			@org_id, @session_id, @issue_id, @event_kind,
			NULLIF(@transition_from, ''), NULLIF(@transition_to, ''),
			NULLIF(@skipped_reason, '')
		)
		ON CONFLICT (session_id, issue_id, event_kind) DO NOTHING`,
		pgx.NamedArgs{
			"org_id":          orgID,
			"session_id":      in.SessionID,
			"issue_id":        in.IssueID,
			"event_kind":      string(in.EventKind),
			"transition_from": in.TransitionFrom,
			"transition_to":   in.TransitionTo,
			"skipped_reason":  string(in.SkippedReason),
		})
	if err != nil {
		return fmt.Errorf("insert linear state event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinearStateEventExists
	}
	return nil
}

// LinearStateEventSummary is a row from the audit log used by the operator
// debug surface on session detail.
type LinearStateEventSummary struct {
	EventKind      string `db:"event_kind" json:"event_kind"`
	TransitionFrom string `db:"transition_from" json:"transition_from,omitempty"`
	TransitionTo   string `db:"transition_to" json:"transition_to,omitempty"`
	SkippedReason  string `db:"skipped_reason" json:"skipped_reason,omitempty"`
	CreatedAt      string `db:"created_at" json:"created_at"`
}

// ListBySession returns the most recent N state events for the operator
// debug pane on session detail.
func (s *LinearStateEventStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, limit int) ([]LinearStateEventSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.Query(ctx, `
		SELECT event_kind,
		       COALESCE(transition_from, '') AS transition_from,
		       COALESCE(transition_to, '') AS transition_to,
		       COALESCE(skipped_reason, '') AS skipped_reason,
		       to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SSOF') AS created_at
		FROM session_issue_link_state_events
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY created_at DESC
		LIMIT @limit`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
			"limit":      limit,
		})
	if err != nil {
		return nil, fmt.Errorf("query linear state events: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[LinearStateEventSummary])
}
