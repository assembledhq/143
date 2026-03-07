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

type PMDecisionFilters struct {
	Limit  int
	Cursor string
}

// ListDecisionViews returns enriched decisions joined with issue and project data.
func (s *PMDecisionLogStore) ListDecisionViews(ctx context.Context, orgID uuid.UUID, filters PMDecisionFilters) ([]models.PMDecisionView, error) {
	query := `
		SELECT d.id, d.plan_id, d.issue_id,
		       i.title AS issue_title,
		       i.project_id,
		       p.title AS project_title,
		       d.decision, d.reasoning, d.outcome, d.created_at
		FROM pm_decision_log d
		LEFT JOIN issues i ON i.id = d.issue_id AND i.org_id = d.org_id
		LEFT JOIN projects p ON p.id = i.project_id AND p.org_id = d.org_id
		WHERE d.org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND d.created_at <= (SELECT created_at FROM pm_decision_log WHERE id = @cursor_id AND org_id = @org_id)
				AND d.id < @cursor_id`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY d.created_at DESC, d.id DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query pm decision views: %w", err)
	}
	defer rows.Close()

	var results []models.PMDecisionView
	for rows.Next() {
		var v models.PMDecisionView
		if err := rows.Scan(
			&v.ID, &v.PlanID, &v.IssueID,
			&v.IssueTitle,
			&v.ProjectID, &v.ProjectTitle,
			&v.Decision, &v.Reasoning, &v.Outcome, &v.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pm decision view: %w", err)
		}
		results = append(results, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pm decision views: %w", err)
	}
	return results, nil
}

// GetDecisionSummary returns aggregate stats for delegated decisions.
func (s *PMDecisionLogStore) GetDecisionSummary(ctx context.Context, orgID uuid.UUID) (models.PMDecisionSummary, error) {
	query := `
		SELECT
			count(*) FILTER (WHERE decision = 'delegate') AS total_delegated,
			count(*) FILTER (WHERE decision = 'delegate' AND outcome = 'succeeded') AS succeeded,
			count(*) FILTER (WHERE decision = 'delegate' AND outcome = 'failed') AS failed,
			count(*) FILTER (WHERE decision = 'delegate' AND (outcome = 'still_open' OR outcome IS NULL OR outcome = '')) AS still_open
		FROM pm_decision_log
		WHERE org_id = @org_id`

	var summary models.PMDecisionSummary
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID}).Scan(
		&summary.TotalDelegated, &summary.Succeeded, &summary.Failed, &summary.StillOpen,
	)
	if err != nil {
		return models.PMDecisionSummary{}, fmt.Errorf("query decision summary: %w", err)
	}
	return summary, nil
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
