package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ReviewCommentStore struct {
	db DBTX
}

func NewReviewCommentStore(db DBTX) *ReviewCommentStore {
	return &ReviewCommentStore{db: db}
}

func (s *ReviewCommentStore) Create(ctx context.Context, c *models.ReviewComment) error {
	query := `
		INSERT INTO review_comments (pull_request_id, org_id, github_comment_id, reviewer, body, diff_path, diff_position, filter_status)
		VALUES (@pull_request_id, @org_id, @github_comment_id, @reviewer, @body, @diff_path, @diff_position, @filter_status)
		ON CONFLICT (pull_request_id, github_comment_id) DO NOTHING
		RETURNING id, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"pull_request_id":   c.PullRequestID,
		"org_id":            c.OrgID,
		"github_comment_id": c.GitHubCommentID,
		"reviewer":          c.Reviewer,
		"body":              c.Body,
		"diff_path":         c.DiffPath,
		"diff_position":     c.DiffPosition,
		"filter_status":     c.FilterStatus,
	})
	return row.Scan(&c.ID, &c.CreatedAt)
}

func (s *ReviewCommentStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.ReviewComment, error) {
	query := `
		SELECT id, pull_request_id, org_id, github_comment_id, reviewer, body,
		       diff_path, diff_position, filter_status, category, actionable,
		       generalizable, generalized_rule, summary, applied, created_at
		FROM review_comments
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.ReviewComment{}, fmt.Errorf("query review comment: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ReviewComment])
}

type ReviewCommentFilters struct {
	PullRequestID *uuid.UUID
	FilterStatus  string
	Limit         int
	Cursor        string
}

func (s *ReviewCommentStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters ReviewCommentFilters) ([]models.ReviewComment, error) {
	query := `
		SELECT id, pull_request_id, org_id, github_comment_id, reviewer, body,
		       diff_path, diff_position, filter_status, category, actionable,
		       generalizable, generalized_rule, summary, applied, created_at
		FROM review_comments
		WHERE org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.PullRequestID != nil {
		query += ` AND pull_request_id = @pull_request_id`
		args["pull_request_id"] = *filters.PullRequestID
	}
	if filters.FilterStatus != "" {
		query += ` AND filter_status = @filter_status`
		args["filter_status"] = filters.FilterStatus
	}
	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND id < @cursor_id`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY created_at DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query review comments: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ReviewComment])
}

func (s *ReviewCommentStore) ListByPullRequest(ctx context.Context, orgID, prID uuid.UUID) ([]models.ReviewComment, error) {
	query := `
		SELECT id, pull_request_id, org_id, github_comment_id, reviewer, body,
		       diff_path, diff_position, filter_status, category, actionable,
		       generalizable, generalized_rule, summary, applied, created_at
		FROM review_comments
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id
		ORDER BY created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": prID,
	})
	if err != nil {
		return nil, fmt.Errorf("query review comments by PR: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ReviewComment])
}

func (s *ReviewCommentStore) ListActionableByPullRequest(ctx context.Context, orgID, prID uuid.UUID) ([]models.ReviewComment, error) {
	query := `
		SELECT id, pull_request_id, org_id, github_comment_id, reviewer, body,
		       diff_path, diff_position, filter_status, category, actionable,
		       generalizable, generalized_rule, summary, applied, created_at
		FROM review_comments
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id
		  AND filter_status = 'accepted' AND actionable = true
		ORDER BY created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": prID,
	})
	if err != nil {
		return nil, fmt.Errorf("query actionable review comments: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ReviewComment])
}

func (s *ReviewCommentStore) UpdateClassification(ctx context.Context, orgID, id uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error {
	query := `
		UPDATE review_comments
		SET filter_status = @filter_status, category = @category, actionable = @actionable,
		    generalizable = @generalizable, generalized_rule = @generalized_rule, summary = @summary
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":               id,
		"org_id":           orgID,
		"filter_status":    filterStatus,
		"category":         category,
		"actionable":       actionable,
		"generalizable":    generalizable,
		"generalized_rule": generalizedRule,
		"summary":          summary,
	})
	return err
}

func (s *ReviewCommentStore) MarkApplied(ctx context.Context, orgID, id uuid.UUID) error {
	query := `UPDATE review_comments SET applied = true WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	return err
}

func (s *ReviewCommentStore) CountPendingByPR(ctx context.Context, orgID, prID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM review_comments WHERE org_id = @org_id AND pull_request_id = @pull_request_id AND filter_status = 'pending'`,
		pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID},
	).Scan(&count)
	return count, err
}
