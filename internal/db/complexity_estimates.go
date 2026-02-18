package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ComplexityEstimateStore struct {
	db DBTX
}

func NewComplexityEstimateStore(db DBTX) *ComplexityEstimateStore {
	return &ComplexityEstimateStore{db: db}
}

func (s *ComplexityEstimateStore) Upsert(ctx context.Context, est *models.ComplexityEstimate) error {
	query := `
		INSERT INTO complexity_estimates (issue_id, org_id, tier, label, confidence,
		            issue_type, reasoning, estimated_files, estimated_tokens,
		            model_used, computed_at)
		VALUES (@issue_id, @org_id, @tier, @label, @confidence,
		        @issue_type, @reasoning, @estimated_files, @estimated_tokens,
		        @model_used, @computed_at)
		ON CONFLICT (issue_id) DO UPDATE
		SET tier = EXCLUDED.tier,
		    label = EXCLUDED.label,
		    confidence = EXCLUDED.confidence,
		    issue_type = EXCLUDED.issue_type,
		    reasoning = EXCLUDED.reasoning,
		    estimated_files = EXCLUDED.estimated_files,
		    estimated_tokens = EXCLUDED.estimated_tokens,
		    model_used = EXCLUDED.model_used,
		    computed_at = EXCLUDED.computed_at
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"issue_id":         est.IssueID,
		"org_id":           est.OrgID,
		"tier":             est.Tier,
		"label":            est.Label,
		"confidence":       est.Confidence,
		"issue_type":       est.IssueType,
		"reasoning":        est.Reasoning,
		"estimated_files":  est.EstimatedFiles,
		"estimated_tokens": est.EstimatedTokens,
		"model_used":       est.ModelUsed,
		"computed_at":      est.ComputedAt,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&est.ID, &est.CreatedAt)
}

func (s *ComplexityEstimateStore) GetByIssueID(ctx context.Context, orgID, issueID uuid.UUID) (models.ComplexityEstimate, error) {
	query := `
		SELECT id, issue_id, org_id, tier, label, confidence, issue_type,
		       reasoning, estimated_files, estimated_tokens, model_used,
		       computed_at, created_at
		FROM complexity_estimates
		WHERE issue_id = @issue_id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"issue_id": issueID,
		"org_id":   orgID,
	})
	if err != nil {
		return models.ComplexityEstimate{}, fmt.Errorf("query complexity estimate: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ComplexityEstimate])
}

func (s *ComplexityEstimateStore) ListByOrg(ctx context.Context, orgID uuid.UUID, maxTier *int, limit int) ([]models.ComplexityEstimate, error) {
	query := `
		SELECT id, issue_id, org_id, tier, label, confidence, issue_type,
		       reasoning, estimated_files, estimated_tokens, model_used,
		       computed_at, created_at
		FROM complexity_estimates
		WHERE org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if maxTier != nil {
		query += ` AND tier <= @max_tier`
		args["max_tier"] = *maxTier
	}

	query += ` ORDER BY tier ASC, computed_at DESC`

	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query complexity estimates: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ComplexityEstimate])
}
