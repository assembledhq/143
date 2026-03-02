package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PMDecisionLogStore struct {
	db DBTX
}

func NewPMDecisionLogStore(db DBTX) *PMDecisionLogStore {
	return &PMDecisionLogStore{db: db}
}

func (s *PMDecisionLogStore) Create(ctx context.Context, entry *models.PMDecisionLogEntry) error {
	query := `
		INSERT INTO pm_decision_log (org_id, plan_id, issue_id, decision, reasoning)
		VALUES (@org_id, @plan_id, @issue_id, @decision, @reasoning)
		RETURNING id, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":    entry.OrgID,
		"plan_id":   entry.PlanID,
		"issue_id":  entry.IssueID,
		"decision":  entry.Decision,
		"reasoning": entry.Reasoning,
	})
	return row.Scan(&entry.ID, &entry.CreatedAt)
}

func (s *PMDecisionLogStore) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]models.PMDecisionLogEntry, error) {
	query := `
		SELECT id, org_id, plan_id, issue_id, decision, reasoning, outcome, created_at
		FROM pm_decision_log
		WHERE org_id = @org_id
		ORDER BY created_at DESC`

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query pm decision log: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PMDecisionLogEntry])
}

func (s *PMDecisionLogStore) UpdateOutcome(ctx context.Context, orgID, planID, issueID uuid.UUID, outcome models.PMDecisionOutcome) error {
	query := `
		UPDATE pm_decision_log
		SET outcome = @outcome
		WHERE org_id = @org_id AND plan_id = @plan_id AND issue_id = @issue_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"plan_id":  planID,
		"issue_id": issueID,
		"outcome":  outcome,
	})
	return err
}
