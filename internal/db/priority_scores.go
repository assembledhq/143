package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type PriorityScoreStore struct {
	db DBTX
}

func NewPriorityScoreStore(db DBTX) *PriorityScoreStore {
	return &PriorityScoreStore{db: db}
}

func (s *PriorityScoreStore) Upsert(ctx context.Context, score *models.PriorityScore) error {
	query := `
		INSERT INTO priority_scores (issue_id, org_id, score, customer_impact_score,
		            severity_score, recency_score, revenue_risk_score, direction_alignment,
		            factors, eligible_for_agent, computed_at)
		VALUES (@issue_id, @org_id, @score, @customer_impact_score,
		        @severity_score, @recency_score, @revenue_risk_score, @direction_alignment,
		        @factors, @eligible_for_agent, @computed_at)
		ON CONFLICT (issue_id) DO UPDATE
		SET score = EXCLUDED.score,
		    customer_impact_score = EXCLUDED.customer_impact_score,
		    severity_score = EXCLUDED.severity_score,
		    recency_score = EXCLUDED.recency_score,
		    revenue_risk_score = EXCLUDED.revenue_risk_score,
		    direction_alignment = EXCLUDED.direction_alignment,
		    factors = EXCLUDED.factors,
		    eligible_for_agent = EXCLUDED.eligible_for_agent,
		    computed_at = EXCLUDED.computed_at
		RETURNING id`

	args := pgx.NamedArgs{
		"issue_id":              score.IssueID,
		"org_id":                score.OrgID,
		"score":                 score.Score,
		"customer_impact_score": score.CustomerImpactScore,
		"severity_score":        score.SeverityScore,
		"recency_score":         score.RecencyScore,
		"revenue_risk_score":    score.RevenueRiskScore,
		"direction_alignment":   score.DirectionAlignment,
		"factors":               score.Factors,
		"eligible_for_agent":    score.EligibleForAgent,
		"computed_at":           score.ComputedAt,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&score.ID)
}

func (s *PriorityScoreStore) GetByIssueID(ctx context.Context, orgID, issueID uuid.UUID) (models.PriorityScore, error) {
	query := `
		SELECT id, issue_id, org_id, score, customer_impact_score, severity_score,
		       recency_score, revenue_risk_score, direction_alignment, factors,
		       eligible_for_agent, computed_at
		FROM priority_scores
		WHERE issue_id = @issue_id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"issue_id": issueID,
		"org_id":   orgID,
	})
	if err != nil {
		return models.PriorityScore{}, fmt.Errorf("query priority score: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PriorityScore])
}

func (s *PriorityScoreStore) ListByOrg(ctx context.Context, orgID uuid.UUID, onlyEligible bool, limit int) ([]models.PriorityScore, error) {
	query := `
		SELECT id, issue_id, org_id, score, customer_impact_score, severity_score,
		       recency_score, revenue_risk_score, direction_alignment, factors,
		       eligible_for_agent, computed_at
		FROM priority_scores
		WHERE org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if onlyEligible {
		query += ` AND eligible_for_agent = true`
	}

	query += ` ORDER BY score DESC`

	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query priority scores: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PriorityScore])
}

func (s *PriorityScoreStore) DeleteByIssueID(ctx context.Context, orgID, issueID uuid.UUID) error {
	query := `DELETE FROM priority_scores WHERE issue_id = @issue_id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"issue_id": issueID,
		"org_id":   orgID,
	})
	return err
}
