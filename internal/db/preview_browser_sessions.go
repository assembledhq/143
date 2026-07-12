package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PreviewBrowserSessionStore struct{ db DBTX }

func NewPreviewBrowserSessionStore(db DBTX) *PreviewBrowserSessionStore {
	return &PreviewBrowserSessionStore{db: db}
}

func (s *PreviewBrowserSessionStore) GetBySession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewBrowserSession, error) {
	rows, err := s.db.Query(ctx, `SELECT id, org_id, session_id, preview_instance_id, context_key, control_state,
control_lease_owner_id, control_lease_expires_at, agent_action_token, agent_action_expires_at, handoff_reason, current_url, viewport, storage_state,
console_cursor, last_observed_at, created_at, updated_at
FROM preview_browser_sessions WHERE org_id = $1 AND session_id = $2`, orgID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query preview browser session: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewBrowserSession])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("collect preview browser session: %w", err)
	}
	return &row, nil
}

func (s *PreviewBrowserSessionStore) Ensure(ctx context.Context, orgID, sessionID, previewID uuid.UUID, contextKey string, viewport models.ViewportSpec) (*models.PreviewBrowserSession, error) {
	viewportJSON, err := json.Marshal(viewport)
	if err != nil {
		return nil, fmt.Errorf("marshal browser viewport: %w", err)
	}
	rows, err := s.db.Query(ctx, `INSERT INTO preview_browser_sessions
(org_id, session_id, preview_instance_id, context_key, viewport)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (org_id, session_id) DO UPDATE SET preview_instance_id = EXCLUDED.preview_instance_id, updated_at = now()
	RETURNING id, org_id, session_id, preview_instance_id, context_key, control_state,
control_lease_owner_id, control_lease_expires_at, agent_action_token, agent_action_expires_at, handoff_reason, current_url, viewport, storage_state,
console_cursor, last_observed_at, created_at, updated_at`, orgID, sessionID, previewID, contextKey, viewportJSON)
	if err != nil {
		return nil, fmt.Errorf("ensure preview browser session: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewBrowserSession])
	if err != nil {
		return nil, fmt.Errorf("collect ensured preview browser session: %w", err)
	}
	return &row, nil
}

func (s *PreviewBrowserSessionStore) GetControl(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewBrowserSession, error) {
	if _, err := s.db.Exec(ctx, `UPDATE preview_browser_sessions SET
control_state = CASE WHEN control_state = 'human_control' AND (control_lease_owner_id IS NULL OR control_lease_expires_at <= now()) THEN 'agent_control' ELSE control_state END,
control_lease_owner_id = CASE WHEN control_state = 'human_control' AND (control_lease_owner_id IS NULL OR control_lease_expires_at <= now()) THEN NULL ELSE control_lease_owner_id END,
control_lease_expires_at = CASE WHEN control_state = 'human_control' AND (control_lease_owner_id IS NULL OR control_lease_expires_at <= now()) THEN NULL ELSE control_lease_expires_at END,
agent_action_token = CASE WHEN agent_action_expires_at <= now() THEN NULL ELSE agent_action_token END,
agent_action_expires_at = CASE WHEN agent_action_expires_at <= now() THEN NULL ELSE agent_action_expires_at END,
updated_at = CASE WHEN (control_state = 'human_control' AND (control_lease_owner_id IS NULL OR control_lease_expires_at <= now())) OR agent_action_expires_at <= now() THEN now() ELSE updated_at END
WHERE org_id = $1 AND session_id = $2`, orgID, sessionID); err != nil {
		return nil, fmt.Errorf("normalize preview browser control: %w", err)
	}
	return s.GetBySession(ctx, orgID, sessionID)
}

func (s *PreviewBrowserSessionStore) RequestHandoff(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (*models.PreviewBrowserSession, error) {
	return s.updateControl(ctx, orgID, sessionID, `UPDATE preview_browser_sessions SET control_state = 'waiting_for_handoff',
control_lease_owner_id = NULL, control_lease_expires_at = NULL, handoff_reason = $3, updated_at = now()
WHERE org_id = $1 AND session_id = $2 AND control_state IN ('agent_control', 'waiting_for_handoff')
AND (agent_action_token IS NULL OR agent_action_expires_at <= now())
RETURNING id, org_id, session_id, preview_instance_id, context_key, control_state, control_lease_owner_id,
control_lease_expires_at, agent_action_token, agent_action_expires_at, handoff_reason, current_url, viewport, storage_state,
console_cursor, last_observed_at, created_at, updated_at`, reason)
}

func (s *PreviewBrowserSessionStore) AcquireHumanControl(ctx context.Context, orgID, sessionID, userID uuid.UUID, duration time.Duration) (*models.PreviewBrowserSession, error) {
	return s.updateControl(ctx, orgID, sessionID, `UPDATE preview_browser_sessions SET control_state = 'human_control',
control_lease_owner_id = $3, control_lease_expires_at = now() + $4::interval, handoff_reason = '', updated_at = now()
WHERE org_id = $1 AND session_id = $2
AND (agent_action_token IS NULL OR agent_action_expires_at <= now())
AND (control_state IN ('agent_control', 'waiting_for_handoff') OR
     (control_state = 'human_control' AND (control_lease_owner_id = $3 OR control_lease_expires_at <= now())))
RETURNING id, org_id, session_id, preview_instance_id, context_key, control_state, control_lease_owner_id,
control_lease_expires_at, agent_action_token, agent_action_expires_at, handoff_reason, current_url, viewport, storage_state,
console_cursor, last_observed_at, created_at, updated_at`, userID, pgInterval(duration))
}

func (s *PreviewBrowserSessionStore) ReturnAgentControl(ctx context.Context, orgID, sessionID, userID uuid.UUID) (*models.PreviewBrowserSession, error) {
	return s.updateControl(ctx, orgID, sessionID, `UPDATE preview_browser_sessions SET control_state = 'agent_control',
control_lease_owner_id = NULL, control_lease_expires_at = NULL, handoff_reason = '', updated_at = now()
WHERE org_id = $1 AND session_id = $2 AND control_state = 'human_control' AND control_lease_owner_id = $3
AND (agent_action_token IS NULL OR agent_action_expires_at <= now())
RETURNING id, org_id, session_id, preview_instance_id, context_key, control_state, control_lease_owner_id,
control_lease_expires_at, agent_action_token, agent_action_expires_at, handoff_reason, current_url, viewport, storage_state,
console_cursor, last_observed_at, created_at, updated_at`, userID)
}

func (s *PreviewBrowserSessionStore) BeginAgentAction(ctx context.Context, orgID, sessionID, token uuid.UUID, duration time.Duration) (bool, error) {
	command, err := s.db.Exec(ctx, `UPDATE preview_browser_sessions SET control_state = 'agent_control',
control_lease_owner_id = NULL, control_lease_expires_at = NULL, handoff_reason = '', agent_action_token = $3,
agent_action_expires_at = now() + $4::interval, updated_at = now()
WHERE org_id = $1 AND session_id = $2
AND (control_state = 'agent_control' OR (control_state = 'human_control' AND (control_lease_owner_id IS NULL OR control_lease_expires_at <= now())))
AND (agent_action_token IS NULL OR agent_action_expires_at <= now())`, orgID, sessionID, token, pgInterval(duration))
	if err != nil {
		return false, fmt.Errorf("begin preview agent action: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func (s *PreviewBrowserSessionStore) EndAgentAction(ctx context.Context, orgID, sessionID, token uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE preview_browser_sessions SET agent_action_token = NULL, agent_action_expires_at = NULL,
updated_at = now() WHERE org_id = $1 AND session_id = $2 AND agent_action_token = $3`, orgID, sessionID, token)
	if err != nil {
		return fmt.Errorf("end preview agent action: %w", err)
	}
	return nil
}

func (s *PreviewBrowserSessionStore) BeginHumanAction(ctx context.Context, orgID, sessionID, userID, token uuid.UUID, duration time.Duration) (bool, error) {
	command, err := s.db.Exec(ctx, `UPDATE preview_browser_sessions SET control_lease_expires_at = now() + interval '5 minutes',
agent_action_token = $4, agent_action_expires_at = now() + $5::interval, updated_at = now()
WHERE org_id = $1 AND session_id = $2 AND control_state = 'human_control'
AND control_lease_owner_id = $3 AND control_lease_expires_at > now()
AND (agent_action_token IS NULL OR agent_action_expires_at <= now())`, orgID, sessionID, userID, token, pgInterval(duration))
	if err != nil {
		return false, fmt.Errorf("begin preview human action: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func (s *PreviewBrowserSessionStore) updateControl(ctx context.Context, orgID, sessionID uuid.UUID, query string, args ...any) (*models.PreviewBrowserSession, error) {
	queryArgs := append([]any{orgID, sessionID}, args...)
	rows, err := s.db.Query(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("update preview browser control: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewBrowserSession])
	if err != nil {
		return nil, fmt.Errorf("collect preview browser control: %w", err)
	}
	return &row, nil
}

func pgInterval(duration time.Duration) string { return fmt.Sprintf("%f seconds", duration.Seconds()) }

func (s *PreviewBrowserSessionStore) SaveState(ctx context.Context, orgID, sessionID uuid.UUID, currentURL string, viewport models.ViewportSpec, storageState json.RawMessage, consoleCursor int64, observedAt time.Time) error {
	viewportJSON, err := json.Marshal(viewport)
	if err != nil {
		return fmt.Errorf("marshal browser viewport: %w", err)
	}
	command, err := s.db.Exec(ctx, `UPDATE preview_browser_sessions SET current_url = $3, viewport = $4,
storage_state = $5, console_cursor = $6, last_observed_at = $7, updated_at = now()
WHERE org_id = $1 AND session_id = $2`, orgID, sessionID, currentURL, viewportJSON, storageState, consoleCursor, observedAt)
	if err != nil {
		return fmt.Errorf("save preview browser state: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("save preview browser state: %w", pgx.ErrNoRows)
	}
	return nil
}
