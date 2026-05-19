package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinearAgentSession bridges a Linear AgentSession (the conversation surface
// in Linear's UI) to a 143 sessions row. The dispatcher upserts one row per
// (org, linear_agent_session_id) on `created` webhook delivery; the worker
// fills in session_id once the 143 session exists. We never delete from this
// table — closed AgentSessions stay as the audit trail of "this issue was
// handed to the agent".
type LinearAgentSession struct {
	ID                    uuid.UUID                      `db:"id" json:"id"`
	OrgID                 uuid.UUID                      `db:"org_id" json:"org_id"`
	IntegrationID         uuid.UUID                      `db:"integration_id" json:"integration_id"`
	LinearAgentSessionID  string                         `db:"linear_agent_session_id" json:"linear_agent_session_id"`
	LinearIssueID         string                         `db:"linear_issue_id" json:"linear_issue_id"`
	LinearIssueIdentifier string                         `db:"linear_issue_identifier" json:"linear_issue_identifier,omitempty"`
	LinearAppUserID       string                         `db:"linear_app_user_id" json:"linear_app_user_id,omitempty"`
	LinearCreatorUserID   string                         `db:"linear_creator_user_id" json:"linear_creator_user_id,omitempty"`
	SessionID             *uuid.UUID                     `db:"session_id" json:"session_id,omitempty"`
	State                 models.LinearAgentSessionState `db:"state" json:"state"`
	LastEventReceivedAt   *time.Time                     `db:"last_event_received_at" json:"last_event_received_at,omitempty"`
	CreatedAt             time.Time                      `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time                      `db:"updated_at" json:"updated_at"`
}

// LinearAgentSessionStore reads and writes the linear_agent_sessions table.
// Sole idempotency anchor for the inbound agent flow: re-deliveries of the
// same AgentSessionEvent collide on UNIQUE (org_id, linear_agent_session_id)
// and ON CONFLICT DO NOTHING preserves the row, returning the existing
// session_id so the worker can recover from a retry.
type LinearAgentSessionStore struct {
	db DBTX
}

func NewLinearAgentSessionStore(db DBTX) *LinearAgentSessionStore {
	return &LinearAgentSessionStore{db: db}
}

// UpsertOnCreatedInput is the dispatcher's view of an AgentSessionEvent
// `created` webhook. Mirrors the subset of the Linear payload the
// dispatcher needs to record before handing off to the worker.
type UpsertOnCreatedInput struct {
	OrgID                 uuid.UUID
	IntegrationID         uuid.UUID
	LinearAgentSessionID  string
	LinearIssueID         string
	LinearIssueIdentifier string
	LinearAppUserID       string
	LinearCreatorUserID   string
}

// UpsertOnCreated inserts a fresh row when this AgentSessionID has never been
// seen, or returns the existing row (untouched) on a re-delivery. The bool
// return distinguishes "we just inserted" (true → kick off the worker) from
// "we already had this" (false → re-enqueue is safe but redundant; the worker
// dedupe key still protects double-processing).
//
// last_event_received_at is bumped on every call so the operator surface can
// answer "when did Linear last touch this AgentSession?" without scanning
// the activity log.
//
// orgID is passed alongside the input struct (which also carries it) so the
// store-lint can statically verify the org scope without recursing through
// model carriers — keeps the lint cheap and the store boundary obvious.
func (s *LinearAgentSessionStore) UpsertOnCreated(ctx context.Context, orgID uuid.UUID, in UpsertOnCreatedInput) (*LinearAgentSession, bool, error) {
	if in.OrgID == uuid.Nil {
		in.OrgID = orgID
	}
	if in.OrgID != orgID {
		return nil, false, errors.New("org_id mismatch between argument and input struct")
	}
	if err := in.validate(); err != nil {
		return nil, false, err
	}

	var (
		row     LinearAgentSession
		created bool
	)
	err := s.db.QueryRow(ctx, `
		INSERT INTO linear_agent_sessions (
			org_id, integration_id, linear_agent_session_id,
			linear_issue_id, linear_issue_identifier,
			linear_app_user_id, linear_creator_user_id,
			state, last_event_received_at
		) VALUES (
			@org_id, @integration_id, @linear_agent_session_id,
			@linear_issue_id, @linear_issue_identifier,
			@linear_app_user_id, @linear_creator_user_id,
			'pending', now()
		)
		ON CONFLICT (org_id, linear_agent_session_id) DO UPDATE
		SET last_event_received_at = now(),
		    updated_at             = now()
		RETURNING
			id, org_id, integration_id, linear_agent_session_id,
			linear_issue_id, COALESCE(linear_issue_identifier, '') AS linear_issue_identifier,
			COALESCE(linear_app_user_id, '') AS linear_app_user_id,
			COALESCE(linear_creator_user_id, '') AS linear_creator_user_id,
			session_id, state, last_event_received_at,
			created_at, updated_at,
			(xmax = 0) AS inserted`,
		pgx.NamedArgs{
			"org_id":                  in.OrgID,
			"integration_id":          in.IntegrationID,
			"linear_agent_session_id": in.LinearAgentSessionID,
			"linear_issue_id":         in.LinearIssueID,
			"linear_issue_identifier": nullableString(in.LinearIssueIdentifier),
			"linear_app_user_id":      nullableString(in.LinearAppUserID),
			"linear_creator_user_id":  nullableString(in.LinearCreatorUserID),
		}).Scan(
		&row.ID,
		&row.OrgID,
		&row.IntegrationID,
		&row.LinearAgentSessionID,
		&row.LinearIssueID,
		&row.LinearIssueIdentifier,
		&row.LinearAppUserID,
		&row.LinearCreatorUserID,
		&row.SessionID,
		&row.State,
		&row.LastEventReceivedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
		&created,
	)
	if err != nil {
		return nil, false, fmt.Errorf("upsert linear_agent_sessions: %w", err)
	}
	return &row, created, nil
}

// Lookup returns the row for an AgentSession by Linear's id. Used by the
// `prompted` event handler to find the linked 143 session and decide turn-
// append vs revision. Returns ErrLinearAgentSessionNotFound when no row
// exists — not a system error, just "we never saw a `created` event for
// this id."
func (s *LinearAgentSessionStore) Lookup(ctx context.Context, orgID uuid.UUID, linearAgentSessionID string) (*LinearAgentSession, error) {
	var row LinearAgentSession
	err := s.db.QueryRow(ctx, `
		SELECT id, org_id, integration_id, linear_agent_session_id,
		       linear_issue_id, COALESCE(linear_issue_identifier, '') AS linear_issue_identifier,
		       COALESCE(linear_app_user_id, '') AS linear_app_user_id,
		       COALESCE(linear_creator_user_id, '') AS linear_creator_user_id,
		       session_id, state, last_event_received_at,
		       created_at, updated_at
		FROM linear_agent_sessions
		WHERE org_id = @org_id
		  AND linear_agent_session_id = @linear_agent_session_id`,
		pgx.NamedArgs{
			"org_id":                  orgID,
			"linear_agent_session_id": linearAgentSessionID,
		}).Scan(
		&row.ID,
		&row.OrgID,
		&row.IntegrationID,
		&row.LinearAgentSessionID,
		&row.LinearIssueID,
		&row.LinearIssueIdentifier,
		&row.LinearAppUserID,
		&row.LinearCreatorUserID,
		&row.SessionID,
		&row.State,
		&row.LastEventReceivedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLinearAgentSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query linear_agent_sessions: %w", err)
	}
	return &row, nil
}

// LookupBySessionID is the reverse direction: given a 143 session, find the
// Linear AgentSession that triggered it (if any). Used by the milestone
// fan-out in HandleMilestone to decide whether to emit AgentActivities.
func (s *LinearAgentSessionStore) LookupBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (*LinearAgentSession, error) {
	var row LinearAgentSession
	err := s.db.QueryRow(ctx, `
		SELECT id, org_id, integration_id, linear_agent_session_id,
		       linear_issue_id, COALESCE(linear_issue_identifier, '') AS linear_issue_identifier,
		       COALESCE(linear_app_user_id, '') AS linear_app_user_id,
		       COALESCE(linear_creator_user_id, '') AS linear_creator_user_id,
		       session_id, state, last_event_received_at,
		       created_at, updated_at
		FROM linear_agent_sessions
		WHERE org_id = @org_id
		  AND session_id = @session_id`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
		}).Scan(
		&row.ID,
		&row.OrgID,
		&row.IntegrationID,
		&row.LinearAgentSessionID,
		&row.LinearIssueID,
		&row.LinearIssueIdentifier,
		&row.LinearAppUserID,
		&row.LinearCreatorUserID,
		&row.SessionID,
		&row.State,
		&row.LastEventReceivedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLinearAgentSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query linear_agent_sessions by session_id: %w", err)
	}
	return &row, nil
}

// GetByID returns the row with the given primary key, scoped to org. Used
// by the debug surface to render a single agent session by its uuid.
// Returns ErrLinearAgentSessionNotFound when no row exists.
func (s *LinearAgentSessionStore) GetByID(ctx context.Context, orgID, agentSessionRowID uuid.UUID) (*LinearAgentSession, error) {
	var row LinearAgentSession
	err := s.db.QueryRow(ctx, `
		SELECT id, org_id, integration_id, linear_agent_session_id,
		       linear_issue_id, COALESCE(linear_issue_identifier, '') AS linear_issue_identifier,
		       COALESCE(linear_app_user_id, '') AS linear_app_user_id,
		       COALESCE(linear_creator_user_id, '') AS linear_creator_user_id,
		       session_id, state, last_event_received_at,
		       created_at, updated_at
		FROM linear_agent_sessions
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"id":     agentSessionRowID,
			"org_id": orgID,
		}).Scan(
		&row.ID,
		&row.OrgID,
		&row.IntegrationID,
		&row.LinearAgentSessionID,
		&row.LinearIssueID,
		&row.LinearIssueIdentifier,
		&row.LinearAppUserID,
		&row.LinearCreatorUserID,
		&row.SessionID,
		&row.State,
		&row.LastEventReceivedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLinearAgentSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query linear_agent_sessions by id: %w", err)
	}
	return &row, nil
}

// AttachSession links a freshly-created 143 session to the AgentSession row.
// Idempotent: if session_id is already set to the same value, this is a
// no-op; if set to a different value, returns ErrLinearAgentSessionMismatch
// to surface the bug rather than silently overwriting.
func (s *LinearAgentSessionStore) AttachSession(ctx context.Context, orgID uuid.UUID, agentSessionRowID, sessionID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE linear_agent_sessions
		SET session_id = @session_id,
		    state      = CASE WHEN state = 'pending' THEN 'in_progress' ELSE state END,
		    updated_at = now()
		WHERE id = @id
		  AND org_id = @org_id
		  AND (session_id IS NULL OR session_id = @session_id)`,
		pgx.NamedArgs{
			"id":         agentSessionRowID,
			"org_id":     orgID,
			"session_id": sessionID,
		})
	if err != nil {
		return fmt.Errorf("attach session to linear_agent_sessions: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinearAgentSessionMismatch
	}
	return nil
}

// SetState flips the cached state. Linear is the source of truth for the
// AgentSession's effective state (it computes it from the activity stream),
// but caching here lets the dispatcher fast-path lookups answer "should we
// route a `prompted` to a turn-append or a revision?" without round-tripping
// to Linear.
func (s *LinearAgentSessionStore) SetState(ctx context.Context, orgID, agentSessionRowID uuid.UUID, state models.LinearAgentSessionState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE linear_agent_sessions
		SET state = @state, updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"id":     agentSessionRowID,
			"org_id": orgID,
			"state":  string(state),
		})
	if err != nil {
		return fmt.Errorf("update linear_agent_sessions state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinearAgentSessionNotFound
	}
	return nil
}

// ListByOrg returns the most-recently-updated agent sessions for one org,
// bounded by limit. The operator debug surface uses this rather than the
// cross-org ListPendingForRecovery path so a busy multi-tenant deploy
// doesn't pay to scan rows for unrelated orgs.
//
// Uses idx_linear_agent_sessions_org_state_recent for the org-scoped scan;
// the index includes `state` so a future filter ("only show in_progress
// sessions") stays cheap.
func (s *LinearAgentSessionStore) ListByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]LinearAgentSession, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, integration_id, linear_agent_session_id,
		       linear_issue_id, COALESCE(linear_issue_identifier, '') AS linear_issue_identifier,
		       COALESCE(linear_app_user_id, '') AS linear_app_user_id,
		       COALESCE(linear_creator_user_id, '') AS linear_creator_user_id,
		       session_id, state, last_event_received_at,
		       created_at, updated_at
		FROM linear_agent_sessions
		WHERE org_id = @org_id
		ORDER BY updated_at DESC
		LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list linear_agent_sessions by org: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[LinearAgentSession])
}

// ListPendingForRecovery returns rows whose state is pending or in_progress
// and that haven't been touched in the given window. Used by the recovery
// sweeper to find AgentSessions whose worker job died mid-flight (e.g. a
// pod was killed between `created` arrival and 143 session creation).
//
// The query is bounded by limit; the caller paginates with successive calls
// until fewer than limit rows come back.
//
// lint:allow-no-orgid reason="cross-org sweeper for stuck agent sessions; runs as system worker"
func (s *LinearAgentSessionStore) ListPendingForRecovery(ctx context.Context, olderThan time.Duration, limit int) ([]LinearAgentSession, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, integration_id, linear_agent_session_id,
		       linear_issue_id, COALESCE(linear_issue_identifier, '') AS linear_issue_identifier,
		       COALESCE(linear_app_user_id, '') AS linear_app_user_id,
		       COALESCE(linear_creator_user_id, '') AS linear_creator_user_id,
		       session_id, state, last_event_received_at,
		       created_at, updated_at
		FROM linear_agent_sessions
		WHERE state IN ('pending', 'in_progress')
		  AND updated_at < now() - make_interval(secs => @older_than_secs)
		ORDER BY updated_at ASC
		LIMIT @limit`,
		pgx.NamedArgs{
			"older_than_secs": olderThan.Seconds(),
			"limit":           limit,
		})
	if err != nil {
		return nil, fmt.Errorf("list pending linear_agent_sessions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[LinearAgentSession])
}

// validate enforces the dispatcher's preconditions before we reach for the
// DB. Shifts the failure mode from "obscure SQL constraint violation" to
// "validation error in the dispatcher" which is much easier for an operator
// to triage from a webhook-delivery audit row.
func (in UpsertOnCreatedInput) validate() error {
	if in.OrgID == uuid.Nil {
		return errors.New("org_id is required")
	}
	if in.IntegrationID == uuid.Nil {
		return errors.New("integration_id is required")
	}
	if in.LinearAgentSessionID == "" {
		return errors.New("linear_agent_session_id is required")
	}
	if in.LinearIssueID == "" {
		return errors.New("linear_issue_id is required")
	}
	return nil
}

// nullableString turns "" into nil so we can write SQL NULL rather than an
// empty string for sparse identifier fields. Keeps reports like "agent
// sessions missing the issue identifier" trivially expressible as
// `WHERE linear_issue_identifier IS NULL`.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ErrLinearAgentSessionNotFound is returned when a Lookup or AttachSession
// targets a row that doesn't exist. Sentinel so callers can distinguish
// "we lost the row" (operator surfaces a recovery hint) from a system
// error (logs + retry).
var ErrLinearAgentSessionNotFound = errors.New("linear agent session not found")

// ErrLinearAgentSessionMismatch is returned when AttachSession is called with
// a session_id that conflicts with an already-attached session. Indicates a
// bug — the dispatcher should never produce two distinct 143 sessions for
// the same Linear AgentSession.
var ErrLinearAgentSessionMismatch = errors.New("linear agent session already attached to a different session")
