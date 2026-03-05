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
	Source   string
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
		       i.created_at, i.updated_at
		FROM issues i
		LEFT JOIN priority_scores ps ON ps.issue_id = i.id
		WHERE i.org_id = @org_id`
	} else {
		query = `
		SELECT id, org_id, external_id, source, source_integration_id, repository_id,
		       title, description, raw_data, status, first_seen_at, last_seen_at,
		       occurrence_count, affected_customer_count, severity, tags, fingerprint,
		       created_at, updated_at
		FROM issues
		WHERE org_id = @org_id`
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
		       created_at, updated_at
		FROM issues
		WHERE id = @id AND org_id = @org_id`

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
		       created_at, updated_at
		FROM issues
		WHERE org_id = @org_id AND id = ANY(@issue_ids)`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"issue_ids": issueIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("query issues by ids: %w", err)
	}

	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Issue])
}

func (s *IssueStore) Upsert(ctx context.Context, issue *models.Issue) error {
	query := `
		INSERT INTO issues (org_id, external_id, source, source_integration_id, repository_id,
		                    title, description, raw_data, status, first_seen_at, last_seen_at,
		                    occurrence_count, affected_customer_count, severity, tags, fingerprint)
		VALUES (@org_id, @external_id, @source, @source_integration_id, @repository_id,
		        @title, @description, @raw_data, @status, @first_seen_at, @last_seen_at,
		        @occurrence_count, @affected_customer_count, @severity, @tags, @fingerprint)
		ON CONFLICT (org_id, fingerprint) DO UPDATE
		SET title = EXCLUDED.title,
		    description = EXCLUDED.description,
		    raw_data = EXCLUDED.raw_data,
		    last_seen_at = GREATEST(issues.last_seen_at, EXCLUDED.last_seen_at),
		    occurrence_count = issues.occurrence_count + EXCLUDED.occurrence_count,
		    affected_customer_count = GREATEST(issues.affected_customer_count, EXCLUDED.affected_customer_count),
		    severity = EXCLUDED.severity,
		    tags = EXCLUDED.tags,
		    updated_at = now()
		RETURNING id, created_at, updated_at`

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
	query := `UPDATE issues SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     issueID,
		"org_id": orgID,
		"status": status,
	})
	return err
}

func (s *IssueStore) CountByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM issues WHERE org_id = @org_id`, pgx.NamedArgs{"org_id": orgID}).Scan(&count)
	return count, err
}
