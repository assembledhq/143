package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type IssueStore struct {
	db DBTX
}

func NewIssueStore(db DBTX) *IssueStore {
	return &IssueStore{db: db}
}

type IssueFilters struct {
	Status   string
	Source   models.IssueSource
	Severity string
	Sort     string
	Limit    int
	Cursor   string // issue ID for cursor-based pagination
}

func (s *IssueStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters IssueFilters) ([]models.Issue, error) {
	var query string
	if filters.Sort == "priority" {
		query = `
		SELECT i.id, i.org_id, i.external_id, i.source, i.source_integration_id, i.repository_id,
		       i.title, i.description, i.raw_data, i.status, i.first_seen_at, i.last_seen_at,
		       i.occurrence_count, i.affected_customer_count, i.severity, i.tags, i.fingerprint,
		       i.created_at, i.updated_at, i.deleted_at
		FROM issues i
		LEFT JOIN priority_scores ps ON ps.issue_id = i.id
		WHERE i.org_id = @org_id AND i.deleted_at IS NULL`
	} else {
		query = `
		SELECT id, org_id, external_id, source, source_integration_id, repository_id,
		       title, description, raw_data, status, first_seen_at, last_seen_at,
		       occurrence_count, affected_customer_count, severity, tags, fingerprint,
		       created_at, updated_at, deleted_at
		FROM issues
		WHERE org_id = @org_id AND deleted_at IS NULL`
	}

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
	}
	if filters.Source != "" {
		query += ` AND source = @source`
		args["source"] = filters.Source
	}
	if filters.Severity != "" {
		query += ` AND severity = @severity`
		args["severity"] = filters.Severity
	}
	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND id < @cursor_id`
			args["cursor_id"] = cursorID
		}
	}

	if filters.Sort == "priority" {
		query += ` ORDER BY ps.score DESC NULLS LAST`
	} else {
		query += ` ORDER BY last_seen_at DESC`
	}

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query issues: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Issue])
}

func (s *IssueStore) GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
	query := `
		SELECT id, org_id, external_id, source, source_integration_id, repository_id,
		       title, description, raw_data, status, first_seen_at, last_seen_at,
		       occurrence_count, affected_customer_count, severity, tags, fingerprint,
		       created_at, updated_at, deleted_at
		FROM issues
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     issueID,
		"org_id": orgID,
	})
	if err != nil {
		return models.Issue{}, fmt.Errorf("query issue: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Issue])
}

func (s *IssueStore) ListByIDs(ctx context.Context, orgID uuid.UUID, issueIDs []uuid.UUID) ([]models.Issue, error) {
	if len(issueIDs) == 0 {
		return []models.Issue{}, nil
	}

	query := `
		SELECT id, org_id, external_id, source, source_integration_id, repository_id,
		       title, description, raw_data, status, first_seen_at, last_seen_at,
		       occurrence_count, affected_customer_count, severity, tags, fingerprint,
		       created_at, updated_at, deleted_at
		FROM issues
		WHERE org_id = @org_id AND id = ANY(@issue_ids) AND deleted_at IS NULL`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"issue_ids": issueIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("query issues by ids: %w", err)
	}

	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Issue])
}

// GetByOrgAndExternalID looks up a single issue by its external (provider)
// identifier. Returns pgx.ErrNoRows when no row matches. Useful for the Linear
// linker which holds Linear issue IDs (UUIDs from Linear) but needs to resolve
// them to local issues row IDs.
func (s *IssueStore) GetByOrgAndExternalID(ctx context.Context, orgID uuid.UUID, source models.IssueSource, externalID string) (models.Issue, error) {
	query := `
		SELECT id, org_id, external_id, source, source_integration_id, repository_id,
		       title, description, raw_data, status, first_seen_at, last_seen_at,
		       occurrence_count, affected_customer_count, severity, tags, fingerprint,
		       created_at, updated_at, deleted_at
		FROM issues
		WHERE org_id = @org_id AND source = @source AND external_id = @external_id AND deleted_at IS NULL
		LIMIT 1`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":      orgID,
		"source":      source,
		"external_id": externalID,
	})
	if err != nil {
		return models.Issue{}, fmt.Errorf("query issue by external id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Issue])
}

// UpdateRepositoryID re-associates an issue with a repository. Used by the
// Linear linker's webhook re-validation path when a Linear issue's repo
// association changes after the link was created.
func (s *IssueStore) UpdateRepositoryID(ctx context.Context, orgID, issueID uuid.UUID, repoID *uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE issues SET repository_id = @repository_id, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`,
		pgx.NamedArgs{
			"id":            issueID,
			"org_id":        orgID,
			"repository_id": repoID,
		})
	return err
}

func (s *IssueStore) Upsert(ctx context.Context, issue *models.Issue) error {
	conflictTarget := "(org_id, source, external_id)"
	if issue.Source == models.IssueSourcePMAgent {
		conflictTarget = "(org_id, fingerprint)"
	}
	query := fmt.Sprintf(`
		INSERT INTO issues (org_id, external_id, source, source_integration_id, repository_id,
		                    title, description, raw_data, status, first_seen_at, last_seen_at,
		                    occurrence_count, affected_customer_count, severity, tags, fingerprint)
		VALUES (@org_id, @external_id, @source, @source_integration_id, @repository_id,
		        @title, @description, @raw_data, @status, @first_seen_at, @last_seen_at,
		        @occurrence_count, @affected_customer_count, @severity, @tags, @fingerprint)
		ON CONFLICT %s DO UPDATE
		SET title = EXCLUDED.title,
		    description = EXCLUDED.description,
		    raw_data = EXCLUDED.raw_data,
		    source_integration_id = COALESCE(EXCLUDED.source_integration_id, issues.source_integration_id),
		    repository_id = COALESCE(EXCLUDED.repository_id, issues.repository_id),
		    last_seen_at = GREATEST(issues.last_seen_at, EXCLUDED.last_seen_at),
		    occurrence_count = issues.occurrence_count + EXCLUDED.occurrence_count,
		    affected_customer_count = GREATEST(issues.affected_customer_count, EXCLUDED.affected_customer_count),
		    severity = EXCLUDED.severity,
		    tags = EXCLUDED.tags,
		    fingerprint = EXCLUDED.fingerprint,
		    updated_at = now()
		WHERE issues.deleted_at IS NULL
		RETURNING id, created_at, updated_at`, conflictTarget)

	args := pgx.NamedArgs{
		"org_id":                  issue.OrgID,
		"external_id":             issue.ExternalID,
		"source":                  issue.Source,
		"source_integration_id":   issue.SourceIntegrationID,
		"repository_id":           issue.RepositoryID,
		"title":                   issue.Title,
		"description":             issue.Description,
		"raw_data":                issue.RawData,
		"status":                  issue.Status,
		"first_seen_at":           issue.FirstSeenAt,
		"last_seen_at":            issue.LastSeenAt,
		"occurrence_count":        issue.OccurrenceCount,
		"affected_customer_count": issue.AffectedCustomerCount,
		"severity":                issue.Severity,
		"tags":                    issue.Tags,
		"fingerprint":             issue.Fingerprint,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&issue.ID, &issue.CreatedAt, &issue.UpdatedAt)
}

func (s *IssueStore) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error {
	query := `UPDATE issues SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     issueID,
		"org_id": orgID,
		"status": status,
	})
	return err
}

func (s *IssueStore) CountByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM issues WHERE org_id = @org_id AND deleted_at IS NULL`, pgx.NamedArgs{"org_id": orgID}).Scan(&count)
	return count, err
}

// SoftDelete marks an issue as deleted without removing the row.
// Uses db.Exec directly (not a transaction) since this is a single atomic UPDATE.
func (s *IssueStore) SoftDelete(ctx context.Context, orgID, issueID uuid.UUID) error {
	query := `UPDATE issues SET deleted_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     issueID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("issue not found or already deleted")
	}
	return nil
}
