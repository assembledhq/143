package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const pagerDutyProviderName = "pagerduty"

// PagerDutyProviderState is the PagerDuty-specific JSONB payload persisted in
// session_issue_link_provider_state.state. The row is keyed by the
// session_issue_links row, so this state is per session/incident link, not a
// global mirror of the PagerDuty incident.
type PagerDutyProviderState struct {
	IntegrationID     string   `json:"integration_id,omitempty"`
	IncidentID        string   `json:"incident_id,omitempty"`
	IncidentNumber    *int64   `json:"incident_number,omitempty"`
	IncidentURL       string   `json:"incident_url,omitempty"`
	ServiceID         string   `json:"service_id,omitempty"`
	ServiceName       string   `json:"service_name,omitempty"`
	TriggerEventID    string   `json:"trigger_event_id,omitempty"`
	WritebackNoteIDs  []string `json:"writeback_note_ids,omitempty"`
	LastWriteOutcome  string   `json:"last_write_outcome,omitempty"`
	LastSkippedReason string   `json:"last_skipped_reason,omitempty"`
}

type PagerDutyProviderStateStore struct {
	db DBTX
}

func NewPagerDutyProviderStateStore(db DBTX) *PagerDutyProviderStateStore {
	return &PagerDutyProviderStateStore{db: db}
}

func (s *PagerDutyProviderStateStore) GetBySessionIssue(ctx context.Context, orgID, sessionID, issueID uuid.UUID) (PagerDutyProviderState, error) {
	linkID, err := s.lookupLinkID(ctx, s.db, orgID, sessionID, issueID, false)
	if err != nil {
		return PagerDutyProviderState{}, err
	}
	return s.getByLinkID(ctx, s.db, orgID, linkID, false)
}

func (s *PagerDutyProviderStateStore) UpsertBySessionIssue(ctx context.Context, orgID, sessionID, issueID uuid.UUID, state PagerDutyProviderState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode pagerduty provider state: %w", err)
	}
	var linkID uuid.UUID
	err = s.db.QueryRow(ctx, `
		WITH link AS (
			SELECT id
			FROM session_issue_links
			WHERE org_id = @org_id
				AND session_id = @session_id
				AND issue_id = @issue_id
		)
		INSERT INTO session_issue_link_provider_state (link_id, org_id, provider, state, updated_at)
		SELECT id, @org_id, @provider, @state, now()
		FROM link
		ON CONFLICT (link_id) DO UPDATE
		SET provider = EXCLUDED.provider,
			state = EXCLUDED.state,
			updated_at = now()
		WHERE session_issue_link_provider_state.org_id = EXCLUDED.org_id
		RETURNING link_id`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
			"issue_id":   issueID,
			"provider":   pagerDutyProviderName,
			"state":      raw,
		},
	).Scan(&linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidSessionIssueLink
	}
	if err != nil {
		return fmt.Errorf("upsert pagerduty provider state: %w", err)
	}
	return nil
}

func (s *PagerDutyProviderStateStore) AppendWritebackNoteID(ctx context.Context, orgID, sessionID, issueID uuid.UUID, noteID string) error {
	noteID = strings.TrimSpace(noteID)
	if noteID == "" {
		return nil
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		state, err := s.GetBySessionIssue(ctx, orgID, sessionID, issueID)
		if err != nil {
			return err
		}
		if pagerDutyStringSliceContains(state.WritebackNoteIDs, noteID) {
			return nil
		}
		state.WritebackNoteIDs = append(state.WritebackNoteIDs, noteID)
		return s.UpsertBySessionIssue(ctx, orgID, sessionID, issueID, state)
	}

	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin pagerduty provider state note append tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	linkID, err := s.lookupLinkID(ctx, tx, orgID, sessionID, issueID, true)
	if err != nil {
		return err
	}
	state, err := s.getByLinkID(ctx, tx, orgID, linkID, true)
	if err != nil {
		return err
	}
	if !pagerDutyStringSliceContains(state.WritebackNoteIDs, noteID) {
		state.WritebackNoteIDs = append(state.WritebackNoteIDs, noteID)
		if err := s.upsertByLinkID(ctx, tx, orgID, linkID, state); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pagerduty provider state note append tx: %w", err)
	}
	return nil
}

func (s *PagerDutyProviderStateStore) lookupLinkID(ctx context.Context, q DBTX, orgID, sessionID, issueID uuid.UUID, lock bool) (uuid.UUID, error) {
	query := `
		SELECT id
		FROM session_issue_links
		WHERE org_id = @org_id
			AND session_id = @session_id
			AND issue_id = @issue_id`
	if lock {
		query += ` FOR UPDATE`
	}
	var linkID uuid.UUID
	err := q.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
		"issue_id":   issueID,
	}).Scan(&linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInvalidSessionIssueLink
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("lookup pagerduty session issue link: %w", err)
	}
	return linkID, nil
}

func (s *PagerDutyProviderStateStore) getByLinkID(ctx context.Context, q DBTX, orgID, linkID uuid.UUID, lock bool) (PagerDutyProviderState, error) {
	query := `
		SELECT state
		FROM session_issue_link_provider_state
		WHERE link_id = @link_id
			AND org_id = @org_id
			AND provider = @provider`
	if lock {
		query += ` FOR UPDATE`
	}
	var raw json.RawMessage
	err := q.QueryRow(ctx, query, pgx.NamedArgs{
		"link_id":  linkID,
		"org_id":   orgID,
		"provider": pagerDutyProviderName,
	}).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return PagerDutyProviderState{}, nil
	}
	if err != nil {
		return PagerDutyProviderState{}, fmt.Errorf("query pagerduty provider state: %w", err)
	}
	var state PagerDutyProviderState
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &state); err != nil {
			return PagerDutyProviderState{}, fmt.Errorf("decode pagerduty provider state: %w", err)
		}
	}
	return state, nil
}

func (s *PagerDutyProviderStateStore) upsertByLinkID(ctx context.Context, q DBTX, orgID, linkID uuid.UUID, state PagerDutyProviderState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode pagerduty provider state: %w", err)
	}
	tag, err := q.Exec(ctx, `
		INSERT INTO session_issue_link_provider_state (link_id, org_id, provider, state, updated_at)
		VALUES (@link_id, @org_id, @provider, @state, now())
		ON CONFLICT (link_id) DO UPDATE
		SET provider = EXCLUDED.provider,
			state = EXCLUDED.state,
			updated_at = now()
		WHERE session_issue_link_provider_state.org_id = EXCLUDED.org_id`,
		pgx.NamedArgs{
			"link_id":  linkID,
			"org_id":   orgID,
			"provider": pagerDutyProviderName,
			"state":    raw,
		})
	if err != nil {
		return fmt.Errorf("upsert pagerduty provider state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("upsert pagerduty provider state: no row written for link_id %s (org_id mismatch or row deleted concurrently)", linkID)
	}
	return nil
}

func pagerDutyStringSliceContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
