package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SessionTurnIssueSnapshotStore struct {
	db DBTX
}

func NewSessionTurnIssueSnapshotStore(db DBTX) *SessionTurnIssueSnapshotStore {
	return &SessionTurnIssueSnapshotStore{db: db}
}

const sessionTurnIssueSnapshotColumns = `id, org_id, session_id, turn_number, linked_issues, created_at`

func (s *SessionTurnIssueSnapshotStore) Create(ctx context.Context, snapshot *models.SessionTurnIssueSnapshot) error {
	linkedIssues, err := json.Marshal(snapshot.LinkedIssues)
	if err != nil {
		return fmt.Errorf("marshal linked issues: %w", err)
	}
	query := `
		INSERT INTO session_turn_issue_snapshots (org_id, session_id, turn_number, linked_issues)
		VALUES (@org_id, @session_id, @turn_number, @linked_issues)
		RETURNING id, created_at`

	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":        snapshot.OrgID,
		"session_id":    snapshot.SessionID,
		"turn_number":   snapshot.TurnNumber,
		"linked_issues": linkedIssues,
	}).Scan(&snapshot.ID, &snapshot.CreatedAt); err != nil {
		return fmt.Errorf("insert session turn issue snapshot: %w", err)
	}
	snapshot.RawLinkedIssues = linkedIssues
	return nil
}

func (s *SessionTurnIssueSnapshotStore) GetByID(ctx context.Context, orgID, snapshotID uuid.UUID) (models.SessionTurnIssueSnapshot, error) {
	query := `
		SELECT ` + sessionTurnIssueSnapshotColumns + `
		FROM session_turn_issue_snapshots
		WHERE org_id = @org_id AND id = @id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"id":     snapshotID,
	})
	if err != nil {
		return models.SessionTurnIssueSnapshot{}, fmt.Errorf("query session turn issue snapshot: %w", err)
	}
	snapshot, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionTurnIssueSnapshot])
	if err != nil {
		return models.SessionTurnIssueSnapshot{}, err
	}
	if len(snapshot.RawLinkedIssues) > 0 {
		if err := json.Unmarshal(snapshot.RawLinkedIssues, &snapshot.LinkedIssues); err != nil {
			return models.SessionTurnIssueSnapshot{}, fmt.Errorf("decode linked issues: %w", err)
		}
	}
	return snapshot, nil
}

func (s *SessionTurnIssueSnapshotStore) GetByTurn(ctx context.Context, orgID, sessionID uuid.UUID, turnNumber int) (models.SessionTurnIssueSnapshot, error) {
	query := `
		SELECT ` + sessionTurnIssueSnapshotColumns + `
		FROM session_turn_issue_snapshots
		WHERE org_id = @org_id AND session_id = @session_id AND turn_number = @turn_number`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":      orgID,
		"session_id":  sessionID,
		"turn_number": turnNumber,
	})
	if err != nil {
		return models.SessionTurnIssueSnapshot{}, fmt.Errorf("query session turn issue snapshot by turn: %w", err)
	}
	snapshot, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionTurnIssueSnapshot])
	if err != nil {
		return models.SessionTurnIssueSnapshot{}, err
	}
	if len(snapshot.RawLinkedIssues) > 0 {
		if err := json.Unmarshal(snapshot.RawLinkedIssues, &snapshot.LinkedIssues); err != nil {
			return models.SessionTurnIssueSnapshot{}, fmt.Errorf("decode linked issues: %w", err)
		}
	}
	return snapshot, nil
}
