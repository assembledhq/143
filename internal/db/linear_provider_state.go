package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinearProviderState is the JSONB blob persisted in
// session_issue_link_provider_state.state for the Linear provider.
//
// Keeping every Linear-specific persisted field in this struct (rather than as
// columns on session_issue_links or its own polluting columns on the side
// table) lets future trackers grow their own provider without schema churn.
type LinearProviderState struct {
	// Identifier is the human Linear key (e.g. "ACS-1234"). The canonical
	// issues.external_id remains Linear's UUID because API writes need it;
	// linked-issue views and PR title prefixes read this field instead.
	Identifier string `json:"identifier,omitempty"`
	// AttachmentID is the Linear attachmentCreate id we persist as the durable
	// handle for this session's attachment on the issue. Subsequent milestones
	// use attachmentUpdate, not attachmentCreate, so this is the dedupe anchor.
	AttachmentID string `json:"attachment_id,omitempty"`
	// AttachmentURL is the human-visible Linear deep-link to the session.
	AttachmentURL string `json:"attachment_url,omitempty"`
	// CommentID is the single live comment we update in place across
	// milestones. Posting separate comments for "started", "PR opened", and
	// "PR merged" would create adoption-breaking notification volume; design
	// 62 §"One live comment, not three" pins this to one rolling comment.
	CommentID string `json:"comment_id,omitempty"`
	// PriorStateID captures the Linear workflow state we transitioned away
	// from, persisted before the transition fires. Used by future "restore
	// to prior state" operations and the audit surface.
	PriorStateID string `json:"prior_state_id,omitempty"`
	// LastKnownStateName is a debug-only cache of the issue's last observed
	// workflow state name. Surface in the operator debug pane.
	LastKnownStateName string `json:"last_known_state_name,omitempty"`
	// LastKnownStateType is one of triage/backlog/unstarted/started/completed/canceled.
	LastKnownStateType string `json:"last_known_state_type,omitempty"`
	// TeamID is the Linear team id of the linked issue. Cached so transitions
	// don't have to round-trip back to GetTask just to discover the team.
	TeamID string `json:"team_id,omitempty"`
	// WorkspaceSlug is the Linear org URL key (e.g. "acs"). Persisted so
	// the LinkedIssueCard can render `linear.app/<slug>/issue/<KEY>` deep
	// links instead of the universal `/issue/<KEY>` redirect — the
	// universal form only resolves correctly when the user is logged into
	// the right workspace.
	WorkspaceSlug string `json:"workspace_slug,omitempty"`
	// LinkAuditReason is set when the link was inserted via the null-repo
	// carve-out, so we can quantify how often the carve-out triggers.
	LinkAuditReason string `json:"link_audit_reason,omitempty"`
	// CoexistsWithGitHubIntegration is true when we observed Linear's
	// native GitHub integration on this issue. Suppresses our merge-time
	// writes to avoid double cycle/sprint membership and double transitions.
	//
	// Pointer (not bare bool) so Merge can distinguish "patch left this
	// alone" from "patch said false" — without the pointer, every partial
	// Merge call would silently reset coexistence to false the moment after
	// it was detected, defeating the suppression guard entirely.
	CoexistsWithGitHubIntegration *bool `json:"coexists_with_github_integration,omitempty"`
	// IssueRepoStale is true when a Linear webhook reported that the linked
	// issue's repo association changed and now mismatches the session's repo.
	// Surfaces in the LinkedIssueCard with a one-click "remove or repair"
	// affordance.
	//
	// Pointer for the same reason as CoexistsWithGitHubIntegration — the
	// repair path needs to clear the flag back to false explicitly, which
	// requires distinguishing "leave alone" from "set to false".
	IssueRepoStale *bool `json:"issue_repo_stale,omitempty"`
	// LastWriteOutcome captures the last attachment/comment/state write so
	// the operator debug surface can answer "why did/didn't Linear update?".
	LastWriteOutcome string `json:"last_write_outcome,omitempty"`
	// LastSkippedReason captures the last suppress decision (debounced,
	// user_recent_edit, linear_github_integration_active, etc.).
	LastSkippedReason string `json:"last_skipped_reason,omitempty"`
	// PrimarySnapshot is the JSON-encoded LinearTurnContext captured at link
	// time so the agent's pre-turn-0 boot can hydrate without a live Linear
	// read. Stored as RawMessage to keep this package free of any
	// services/linear import; consumers re-decode into LinearTurnContext.
	PrimarySnapshot json.RawMessage `json:"primary_snapshot,omitempty"`
}

// LinearAttachmentMetadata is the stable schema we send in attachment
// metadata. Locked from day one so PMs can build Linear custom views like
// "issues with a 143 attachment whose outcome = merged".
type LinearAttachmentMetadata struct {
	Service   string `json:"service"`
	SessionID string `json:"session_id"`
	Primary   bool   `json:"primary"`
	Outcome   string `json:"outcome"`
}

// LinearAttachmentOutcome is the controlled vocabulary stored in
// LinearAttachmentMetadata.Outcome. Keep this list in lockstep with the
// subtitle table in design 62 §"The attachment is the durable handle".
type LinearAttachmentOutcome string

const (
	LinearAttachmentOutcomeRunning   LinearAttachmentOutcome = "running"
	LinearAttachmentOutcomePROpen    LinearAttachmentOutcome = "pr_open"
	LinearAttachmentOutcomeMerged    LinearAttachmentOutcome = "merged"
	LinearAttachmentOutcomeEndedNoPR LinearAttachmentOutcome = "ended_no_pr"
	LinearAttachmentOutcomeFailed    LinearAttachmentOutcome = "failed"
)

const linearProviderName = "linear"

// LinearProviderStateStore reads and writes the per-link provider-state row
// for Linear. Provider-agnostic by table design but provider-typed at this
// boundary so callers don't have to round-trip jsonb.
type LinearProviderStateStore struct {
	db DBTX
}

func NewLinearProviderStateStore(db DBTX) *LinearProviderStateStore {
	return &LinearProviderStateStore{db: db}
}

// Get returns the linear-typed state for a link. Returns a zero-value state
// (not an error) when no row exists yet.
func (s *LinearProviderStateStore) Get(ctx context.Context, orgID, linkID uuid.UUID) (LinearProviderState, error) {
	var raw json.RawMessage
	err := s.db.QueryRow(ctx, `
		SELECT state FROM session_issue_link_provider_state
		WHERE link_id = @link_id AND org_id = @org_id AND provider = @provider`,
		pgx.NamedArgs{
			"link_id":  linkID,
			"org_id":   orgID,
			"provider": linearProviderName,
		}).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return LinearProviderState{}, nil
	}
	if err != nil {
		return LinearProviderState{}, fmt.Errorf("query linear provider state: %w", err)
	}
	var state LinearProviderState
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &state); err != nil {
			return LinearProviderState{}, fmt.Errorf("decode linear provider state: %w", err)
		}
	}
	return state, nil
}

// Upsert merges new values into the existing state row. Callers that need
// strictly read-modify-write semantics should wrap calls in a Begin/Commit;
// the design doc lets late writers win because per-link write coalescing
// already collapses near-simultaneous events.
func (s *LinearProviderStateStore) Upsert(ctx context.Context, orgID, linkID uuid.UUID, state LinearProviderState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode linear provider state: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO session_issue_link_provider_state (link_id, org_id, provider, state, updated_at)
		VALUES (@link_id, @org_id, @provider, @state, now())
		ON CONFLICT (link_id) DO UPDATE
		SET state = EXCLUDED.state, updated_at = now()`,
		pgx.NamedArgs{
			"link_id":  linkID,
			"org_id":   orgID,
			"provider": linearProviderName,
			"state":    raw,
		})
	if err != nil {
		return fmt.Errorf("upsert linear provider state: %w", err)
	}
	return nil
}

// Merge applies a partial update on top of the current row. Empty string
// fields in patch leave the existing values intact; this avoids accidentally
// clearing AttachmentID just because we wanted to update LastWriteOutcome.
func (s *LinearProviderStateStore) Merge(ctx context.Context, orgID, linkID uuid.UUID, patch LinearProviderState) error {
	current, err := s.Get(ctx, orgID, linkID)
	if err != nil {
		return err
	}
	merged := MergeLinearProviderState(current, patch)
	return s.Upsert(ctx, orgID, linkID, merged)
}

// MergeLinearProviderState applies a partial patch to current state.
// Exported pure function so the merge semantics — particularly the
// pointer-typed bool fields — are unit-testable without a DB.
//
// Empty string fields in patch leave the existing values intact (no
// accidental clears); pointer-typed bool fields nil = leave alone, non-nil
// = overwrite. Without these semantics, partial-update callers (e.g.
// recording a skip reason) would clobber sticky flags like
// CoexistsWithGitHubIntegration back to false on every call.
func MergeLinearProviderState(current, patch LinearProviderState) LinearProviderState {
	if patch.Identifier != "" {
		current.Identifier = patch.Identifier
	}
	if patch.AttachmentID != "" {
		current.AttachmentID = patch.AttachmentID
	}
	if patch.AttachmentURL != "" {
		current.AttachmentURL = patch.AttachmentURL
	}
	if patch.CommentID != "" {
		current.CommentID = patch.CommentID
	}
	if patch.PriorStateID != "" {
		current.PriorStateID = patch.PriorStateID
	}
	if patch.LastKnownStateName != "" {
		current.LastKnownStateName = patch.LastKnownStateName
	}
	if patch.LastKnownStateType != "" {
		current.LastKnownStateType = patch.LastKnownStateType
	}
	if patch.TeamID != "" {
		current.TeamID = patch.TeamID
	}
	if patch.WorkspaceSlug != "" {
		current.WorkspaceSlug = patch.WorkspaceSlug
	}
	if patch.LinkAuditReason != "" {
		current.LinkAuditReason = patch.LinkAuditReason
	}
	if patch.LastWriteOutcome != "" {
		current.LastWriteOutcome = patch.LastWriteOutcome
	}
	if patch.LastSkippedReason != "" {
		current.LastSkippedReason = patch.LastSkippedReason
	}
	if patch.CoexistsWithGitHubIntegration != nil {
		current.CoexistsWithGitHubIntegration = patch.CoexistsWithGitHubIntegration
	}
	if patch.IssueRepoStale != nil {
		current.IssueRepoStale = patch.IssueRepoStale
	}
	if len(patch.PrimarySnapshot) > 0 {
		current.PrimarySnapshot = patch.PrimarySnapshot
	}
	return current
}

// BoolPtr is a small helper for constructing the pointer-typed bool fields
// on LinearProviderState. Reduces visual noise at call sites that need to
// pass `&true` / `&false`.
func BoolPtr(b bool) *bool { return &b }
