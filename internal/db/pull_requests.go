package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type PullRequestStore struct {
	db DBTX
}

func NewPullRequestStore(db DBTX) *PullRequestStore {
	return &PullRequestStore{db: db}
}

type PullRequestFilters struct {
	Status string
	Limit  int
	Cursor string
}

func (s *PullRequestStore) Create(ctx context.Context, pr *models.PullRequest) error {
	query := `
		INSERT INTO pull_requests (session_id, org_id, github_pr_number, github_pr_url, github_repo, title, body, status, review_status)
		VALUES (@session_id, @org_id, @github_pr_number, @github_pr_url, @github_repo, @title, @body, @status, @review_status)
		RETURNING id, created_at, updated_at`

	args := pgx.NamedArgs{
		"session_id":     pr.SessionID,
		"org_id":           pr.OrgID,
		"github_pr_number": pr.GitHubPRNumber,
		"github_pr_url":    pr.GitHubPRURL,
		"github_repo":      pr.GitHubRepo,
		"title":            pr.Title,
		"body":             pr.Body,
		"status":           pr.Status,
		"review_status":    pr.ReviewStatus,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&pr.ID, &pr.CreatedAt, &pr.UpdatedAt)
}

func (s *PullRequestStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PullRequest, error) {
	query := `
		SELECT id, session_id, org_id, github_pr_number, github_pr_url, github_repo,
		       title, body, status, review_status, merged_at, created_at, updated_at
		FROM pull_requests
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequest])
}

func (s *PullRequestStore) GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.PullRequest, error) {
	query := `
		SELECT id, session_id, org_id, github_pr_number, github_pr_url, github_repo,
		       title, body, status, review_status, merged_at, created_at, updated_at
		FROM pull_requests
		WHERE session_id = @session_id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id": sessionID,
		"org_id":       orgID,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request by session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequest])
}

func (s *PullRequestStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status string) error {
	query := `UPDATE pull_requests SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id`
	if status == "merged" {
		query = `UPDATE pull_requests SET status = @status, merged_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id`
	}
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"status": status,
	})
	return err
}

// GetByRepoAndNumber looks up a PR by repo and number without org scoping.
// This is intentionally org-agnostic because it is called from GitHub webhook
// handlers where no org context exists. The returned pr.OrgID is used for
// subsequent org-scoped operations.
func (s *PullRequestStore) GetByRepoAndNumber(ctx context.Context, repo string, number int) (models.PullRequest, error) {
	query := `
		SELECT id, session_id, org_id, github_pr_number, github_pr_url, github_repo,
		       title, body, status, review_status, merged_at, created_at, updated_at
		FROM pull_requests
		WHERE github_repo = @github_repo AND github_pr_number = @github_pr_number`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"github_repo":      repo,
		"github_pr_number": number,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request by repo and number: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequest])
}

func (s *PullRequestStore) UpdateReviewStatus(ctx context.Context, orgID, id uuid.UUID, reviewStatus string) error {
	query := `UPDATE pull_requests SET review_status = @review_status, updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":            id,
		"org_id":        orgID,
		"review_status": reviewStatus,
	})
	return err
}

func (s *PullRequestStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters PullRequestFilters) ([]models.PullRequest, error) {
	query := `
		SELECT id, session_id, org_id, github_pr_number, github_pr_url, github_repo,
		       title, body, status, review_status, merged_at, created_at, updated_at
		FROM pull_requests
		WHERE org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
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
		return nil, fmt.Errorf("query pull requests: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequest])
}
