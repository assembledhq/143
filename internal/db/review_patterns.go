package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ReviewPatternStore struct {
	db DBTX
}

func NewReviewPatternStore(db DBTX) *ReviewPatternStore {
	return &ReviewPatternStore{db: db}
}

func (s *ReviewPatternStore) Create(ctx context.Context, p *models.ReviewPattern) error {
	query := `
		INSERT INTO review_patterns (org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, active)
		VALUES (@org_id, @repo, @rule, @category, @source_comment_ids, @occurrence_count, @status, @manually_curated, true)
		RETURNING id, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":             p.OrgID,
		"repo":               p.Repo,
		"rule":               p.Rule,
		"category":           p.Category,
		"source_comment_ids": p.SourceCommentIDs,
		"occurrence_count":   p.OccurrenceCount,
		"status":             p.Status,
		"manually_curated":   p.ManuallyCurated,
	})
	return row.Scan(&p.ID, &p.CreatedAt)
}

func (s *ReviewPatternStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.ReviewPattern, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, created_at
		FROM review_patterns
		WHERE id = @id AND org_id = @org_id AND active = true`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.ReviewPattern{}, fmt.Errorf("query review pattern: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ReviewPattern])
}

type ReviewPatternFilters struct {
	Status string
	Limit  int
	Cursor string
}

func (s *ReviewPatternStore) ListByRepo(ctx context.Context, orgID uuid.UUID, repo string, filters ReviewPatternFilters) ([]models.ReviewPattern, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, created_at
		FROM review_patterns
		WHERE org_id = @org_id AND repo = @repo AND active = true`

	args := pgx.NamedArgs{"org_id": orgID, "repo": repo}

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

	query += ` ORDER BY occurrence_count DESC, created_at DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query review patterns: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ReviewPattern])
}

func (s *ReviewPatternStore) ListActiveByRepo(ctx context.Context, orgID uuid.UUID, repo string) ([]models.ReviewPattern, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, created_at
		FROM review_patterns
		WHERE org_id = @org_id AND repo = @repo AND active = true AND status = 'active'
		ORDER BY category, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"repo":   repo,
	})
	if err != nil {
		return nil, fmt.Errorf("query active review patterns: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ReviewPattern])
}

// FindMatchingRule finds an active pattern with a matching rule (case-insensitive) for dedup.
func (s *ReviewPatternStore) FindMatchingRule(ctx context.Context, orgID uuid.UUID, repo, normalizedRule string) (models.ReviewPattern, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, created_at
		FROM review_patterns
		WHERE org_id = @org_id AND repo = @repo AND active = true
		  AND lower(rule) = @normalized_rule`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"repo":            repo,
		"normalized_rule": normalizedRule,
	})
	if err != nil {
		return models.ReviewPattern{}, fmt.Errorf("query matching review pattern: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ReviewPattern])
}

// UpdatePattern implements insert-only versioning: deactivates the current row
// and inserts a new one with updated values.
func (s *ReviewPatternStore) UpdatePattern(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) error {
	// 1. Inactivate the current row and get its values.
	inactivateQuery := `
		UPDATE review_patterns SET active = false
		WHERE id = @id AND org_id = @org_id AND active = true
		RETURNING org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated`

	var existing models.ReviewPattern
	err := s.db.QueryRow(ctx, inactivateQuery, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	}).Scan(
		&existing.OrgID, &existing.Repo, &existing.Rule, &existing.Category,
		&existing.SourceCommentIDs, &existing.OccurrenceCount, &existing.Status,
		&existing.ManuallyCurated,
	)
	if err != nil {
		return fmt.Errorf("inactivate review pattern: %w", err)
	}

	// 2. Apply updates.
	newRule := existing.Rule
	if rule != nil {
		newRule = *rule
		existing.ManuallyCurated = true
	}
	newStatus := existing.Status
	if status != nil {
		newStatus = *status
	}

	// 3. Insert new active row.
	insertQuery := `
		INSERT INTO review_patterns (org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, active)
		VALUES (@org_id, @repo, @rule, @category, @source_comment_ids, @occurrence_count, @status, @manually_curated, true)`

	_, err = s.db.Exec(ctx, insertQuery, pgx.NamedArgs{
		"org_id":             existing.OrgID,
		"repo":               existing.Repo,
		"rule":               newRule,
		"category":           existing.Category,
		"source_comment_ids": existing.SourceCommentIDs,
		"occurrence_count":   existing.OccurrenceCount,
		"status":             newStatus,
		"manually_curated":   existing.ManuallyCurated,
	})
	return err
}

// IncrementOccurrence deactivates the current pattern and inserts a new active row
// with incremented occurrence_count and appended source comment ID.
func (s *ReviewPatternStore) IncrementOccurrence(ctx context.Context, orgID, patternID, commentID uuid.UUID) error {
	inactivateQuery := `
		UPDATE review_patterns SET active = false
		WHERE id = @id AND org_id = @org_id AND active = true
		RETURNING org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated`

	var existing models.ReviewPattern
	err := s.db.QueryRow(ctx, inactivateQuery, pgx.NamedArgs{
		"id":     patternID,
		"org_id": orgID,
	}).Scan(
		&existing.OrgID, &existing.Repo, &existing.Rule, &existing.Category,
		&existing.SourceCommentIDs, &existing.OccurrenceCount, &existing.Status,
		&existing.ManuallyCurated,
	)
	if err != nil {
		return fmt.Errorf("inactivate review pattern for increment: %w", err)
	}

	newCount := existing.OccurrenceCount + 1
	newSourceIDs := append(existing.SourceCommentIDs, commentID)

	// Auto-promote to active at 2+ occurrences.
	newStatus := existing.Status
	if newCount >= 2 && newStatus == "candidate" {
		newStatus = "active"
	}

	insertQuery := `
		INSERT INTO review_patterns (org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, active)
		VALUES (@org_id, @repo, @rule, @category, @source_comment_ids, @occurrence_count, @status, @manually_curated, true)`

	_, err = s.db.Exec(ctx, insertQuery, pgx.NamedArgs{
		"org_id":             existing.OrgID,
		"repo":               existing.Repo,
		"rule":               existing.Rule,
		"category":           existing.Category,
		"source_comment_ids": newSourceIDs,
		"occurrence_count":   newCount,
		"status":             newStatus,
		"manually_curated":   existing.ManuallyCurated,
	})
	return err
}
