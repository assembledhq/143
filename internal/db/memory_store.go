package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type MemoryStore struct {
	db TxStarter
}

func NewMemoryStore(db TxStarter) *MemoryStore {
	return &MemoryStore{db: db}
}

func (s *MemoryStore) Create(ctx context.Context, m *models.Memory) error {
	query := `
		INSERT INTO memories (org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns)
		VALUES (@org_id, @repo, @rule, @category, @source_comment_ids, @occurrence_count, @status, @manually_curated, true, @scope, @source, @last_used_at, @times_reinforced, @file_patterns)
		RETURNING id, created_at`

	scope := m.Scope
	if scope == "" {
		scope = "repo"
	}
	source := m.Source
	if source == "" {
		source = "review"
	}

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":             m.OrgID,
		"repo":               m.Repo,
		"rule":               m.Rule,
		"category":           m.Category,
		"source_comment_ids": m.SourceCommentIDs,
		"occurrence_count":   m.OccurrenceCount,
		"status":             m.Status,
		"manually_curated":   m.ManuallyCurated,
		"scope":              scope,
		"source":             source,
		"last_used_at":       m.LastUsedAt,
		"times_reinforced":   m.TimesReinforced,
		"file_patterns":      m.FilePatterns,
	})
	return row.Scan(&m.ID, &m.CreatedAt)
}

func (s *MemoryStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.Memory, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns, created_at
		FROM memories
		WHERE id = @id AND org_id = @org_id AND active = true`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.Memory{}, fmt.Errorf("query memory: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Memory])
}

type MemoryFilters struct {
	Status string
	Limit  int
	Cursor string
}

func (s *MemoryStore) ListByRepo(ctx context.Context, orgID uuid.UUID, repo string, filters MemoryFilters) ([]models.Memory, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns, created_at
		FROM memories
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
		return nil, fmt.Errorf("query memories: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Memory])
}

func (s *MemoryStore) ListActiveByRepo(ctx context.Context, orgID uuid.UUID, repo string) ([]models.Memory, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns, created_at
		FROM memories
		WHERE org_id = @org_id AND repo = @repo AND active = true AND status = 'active'
		ORDER BY category, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"repo":   repo,
	})
	if err != nil {
		return nil, fmt.Errorf("query active memories: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Memory])
}

// FindMatchingRule finds an active memory with a matching rule (case-insensitive) for dedup.
func (s *MemoryStore) FindMatchingRule(ctx context.Context, orgID uuid.UUID, repo, normalizedRule string) (models.Memory, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns, created_at
		FROM memories
		WHERE org_id = @org_id AND repo = @repo AND active = true
		  AND lower(rule) = @normalized_rule`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"repo":            repo,
		"normalized_rule": normalizedRule,
	})
	if err != nil {
		return models.Memory{}, fmt.Errorf("query matching memory: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Memory])
}

// UpdateMemory implements insert-only versioning: deactivates the current row
// and inserts a new one with updated values. Wrapped in a transaction to ensure
// the old row is never deactivated without a replacement being inserted.
func (s *MemoryStore) UpdateMemory(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) error {
	_, err := s.UpdateMemoryAndGet(ctx, orgID, id, rule, status)
	return err
}

// UpdateMemoryAndGet performs insert-only versioning and returns the newly inserted active row.
func (s *MemoryStore) UpdateMemoryAndGet(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) (models.Memory, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.Memory{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Inactivate the current row and get its values.
	inactivateQuery := `
		UPDATE memories SET active = false
		WHERE id = @id AND org_id = @org_id AND active = true
		RETURNING org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, scope, source, last_used_at, times_reinforced, file_patterns`

	var existing models.Memory
	err = tx.QueryRow(ctx, inactivateQuery, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	}).Scan(
		&existing.OrgID, &existing.Repo, &existing.Rule, &existing.Category,
		&existing.SourceCommentIDs, &existing.OccurrenceCount, &existing.Status,
		&existing.ManuallyCurated, &existing.Scope, &existing.Source,
		&existing.LastUsedAt, &existing.TimesReinforced, &existing.FilePatterns,
	)
	if err != nil {
		return models.Memory{}, fmt.Errorf("inactivate memory: %w", err)
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
		INSERT INTO memories (org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns)
		VALUES (@org_id, @repo, @rule, @category, @source_comment_ids, @occurrence_count, @status, @manually_curated, true, @scope, @source, @last_used_at, @times_reinforced, @file_patterns)
		RETURNING id, org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns, created_at`

	rows, err := tx.Query(ctx, insertQuery, pgx.NamedArgs{
		"org_id":             existing.OrgID,
		"repo":               existing.Repo,
		"rule":               newRule,
		"category":           existing.Category,
		"source_comment_ids": existing.SourceCommentIDs,
		"occurrence_count":   existing.OccurrenceCount,
		"status":             newStatus,
		"manually_curated":   existing.ManuallyCurated,
		"scope":              existing.Scope,
		"source":             existing.Source,
		"last_used_at":       existing.LastUsedAt,
		"times_reinforced":   existing.TimesReinforced,
		"file_patterns":      existing.FilePatterns,
	})
	if err != nil {
		return models.Memory{}, fmt.Errorf("insert new memory version: %w", err)
	}

	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Memory])
	if err != nil {
		return models.Memory{}, fmt.Errorf("scan new memory version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.Memory{}, fmt.Errorf("commit transaction: %w", err)
	}

	return updated, nil
}

// IncrementOccurrence deactivates the current memory and inserts a new active row
// with incremented occurrence_count and appended source comment ID. Wrapped in a
// transaction to ensure atomicity of the insert-only versioning operation.
func (s *MemoryStore) IncrementOccurrence(ctx context.Context, orgID, memoryID, commentID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inactivateQuery := `
		UPDATE memories SET active = false
		WHERE id = @id AND org_id = @org_id AND active = true
		RETURNING org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, scope, source, last_used_at, times_reinforced, file_patterns`

	var existing models.Memory
	err = tx.QueryRow(ctx, inactivateQuery, pgx.NamedArgs{
		"id":     memoryID,
		"org_id": orgID,
	}).Scan(
		&existing.OrgID, &existing.Repo, &existing.Rule, &existing.Category,
		&existing.SourceCommentIDs, &existing.OccurrenceCount, &existing.Status,
		&existing.ManuallyCurated, &existing.Scope, &existing.Source,
		&existing.LastUsedAt, &existing.TimesReinforced, &existing.FilePatterns,
	)
	if err != nil {
		return fmt.Errorf("inactivate memory for increment: %w", err)
	}

	newCount := existing.OccurrenceCount + 1
	newSourceIDs := append(existing.SourceCommentIDs, commentID)

	// Auto-promote to active at 2+ occurrences.
	newStatus := existing.Status
	if newCount >= 2 && newStatus == "candidate" {
		newStatus = "active"
	}

	insertQuery := `
		INSERT INTO memories (org_id, repo, rule, category, source_comment_ids, occurrence_count, status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns)
		VALUES (@org_id, @repo, @rule, @category, @source_comment_ids, @occurrence_count, @status, @manually_curated, true, @scope, @source, now(), @times_reinforced, @file_patterns)`

	_, err = tx.Exec(ctx, insertQuery, pgx.NamedArgs{
		"org_id":             existing.OrgID,
		"repo":               existing.Repo,
		"rule":               existing.Rule,
		"category":           existing.Category,
		"source_comment_ids": newSourceIDs,
		"occurrence_count":   newCount,
		"status":             newStatus,
		"manually_curated":   existing.ManuallyCurated,
		"scope":              existing.Scope,
		"source":             existing.Source,
		"times_reinforced":   existing.TimesReinforced + 1,
		"file_patterns":      existing.FilePatterns,
	})
	if err != nil {
		return fmt.Errorf("insert incremented memory: %w", err)
	}

	return tx.Commit(ctx)
}

// ListForContext returns all active memories relevant to a given repo context.
// This includes repo-scoped memories for the specific repo AND org-scoped
// memories that apply across all repos. Used by the memory scoring service to
// build agent context.
func (s *MemoryStore) ListForContext(ctx context.Context, orgID uuid.UUID, repo string) ([]models.Memory, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns, created_at
		FROM memories
		WHERE org_id = @org_id AND active = true AND status = 'active'
		  AND (
		    (scope = 'repo' AND repo = @repo)
		    OR scope = 'org'
		  )
		ORDER BY times_reinforced DESC, created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"repo":   repo,
	})
	if err != nil {
		return nil, fmt.Errorf("query memories for context: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Memory])
}

// ListActiveByOrg returns all active org-scoped memories for an organization.
func (s *MemoryStore) ListActiveByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Memory, error) {
	query := `
		SELECT id, org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, active, scope, source, last_used_at, times_reinforced, file_patterns, created_at
		FROM memories
		WHERE org_id = @org_id AND active = true AND status = 'active' AND scope = 'org'
		ORDER BY category, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query org-scoped memories: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Memory])
}

// ReinforceBatch updates last_used_at and increments times_reinforced for
// multiple memories in two bulk queries (deactivate + insert). Used when a PR
// is approved after memories were injected into the agent context. Uses
// insert-only versioning: deactivates current rows and inserts new ones with
// updated counters. Memories that were deactivated between selection and
// reinforcement are silently skipped (the CTE's WHERE clause filters them out).
func (s *MemoryStore) ReinforceBatch(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error {
	if len(memoryIDs) == 0 {
		return nil
	}

	query := `
		WITH deactivated AS (
			UPDATE memories SET active = false
			WHERE id = ANY(@ids) AND org_id = @org_id AND active = true
			RETURNING org_id, repo, rule, category, source_comment_ids,
			          occurrence_count, status, manually_curated, scope,
			          source, times_reinforced, file_patterns
		)
		INSERT INTO memories (
			org_id, repo, rule, category, source_comment_ids, occurrence_count,
			status, manually_curated, active, scope, source, last_used_at,
			times_reinforced, file_patterns
		)
		SELECT org_id, repo, rule, category, source_comment_ids, occurrence_count,
		       status, manually_curated, true, scope, source, now(),
		       times_reinforced + 1, file_patterns
		FROM deactivated`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"ids":    memoryIDs,
		"org_id": orgID,
	})
	if err != nil {
		return fmt.Errorf("reinforce memories batch: %w", err)
	}

	return nil
}
