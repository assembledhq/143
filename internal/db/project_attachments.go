package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ProjectAttachmentStore struct {
	db DBTX
}

func NewProjectAttachmentStore(db DBTX) *ProjectAttachmentStore {
	return &ProjectAttachmentStore{db: db}
}

const attachmentColumns = `id, project_id, org_id, file_name, file_url, file_type,
	thumbnail_url, file_size, category, caption, sort_order, uploaded_by, created_at, updated_at`

func scanAttachment(row pgx.Row) (models.ProjectAttachment, error) {
	var a models.ProjectAttachment
	err := row.Scan(
		&a.ID, &a.ProjectID, &a.OrgID, &a.FileName, &a.FileURL, &a.FileType,
		&a.ThumbnailURL, &a.FileSize, &a.Category, &a.Caption, &a.SortOrder,
		&a.UploadedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	return a, err
}

func scanAttachments(rows pgx.Rows) ([]models.ProjectAttachment, error) {
	var attachments []models.ProjectAttachment
	for rows.Next() {
		var a models.ProjectAttachment
		err := rows.Scan(
			&a.ID, &a.ProjectID, &a.OrgID, &a.FileName, &a.FileURL, &a.FileType,
			&a.ThumbnailURL, &a.FileSize, &a.Category, &a.Caption, &a.SortOrder,
			&a.UploadedBy, &a.CreatedAt, &a.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, a)
	}
	return attachments, rows.Err()
}

func (s *ProjectAttachmentStore) Create(ctx context.Context, a *models.ProjectAttachment) error {
	query := `
		INSERT INTO project_attachments (
			project_id, org_id, file_name, file_url, file_type,
			thumbnail_url, file_size, category, caption, sort_order, uploaded_by
		) VALUES (
			@project_id, @org_id, @file_name, @file_url, @file_type,
			@thumbnail_url, @file_size, @category, @caption, @sort_order, @uploaded_by
		) RETURNING id, created_at, updated_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"project_id":    a.ProjectID,
		"org_id":        a.OrgID,
		"file_name":     a.FileName,
		"file_url":      a.FileURL,
		"file_type":     a.FileType,
		"thumbnail_url": a.ThumbnailURL,
		"file_size":     a.FileSize,
		"category":      a.Category,
		"caption":       a.Caption,
		"sort_order":    a.SortOrder,
		"uploaded_by":   a.UploadedBy,
	})
	return row.Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
}

func (s *ProjectAttachmentStore) ListByProject(ctx context.Context, orgID, projectID uuid.UUID) ([]models.ProjectAttachment, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_attachments
		WHERE project_id = @project_id AND org_id = @org_id
		ORDER BY sort_order ASC, created_at DESC`, attachmentColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"project_id": projectID,
		"org_id":     orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query project attachments: %w", err)
	}
	defer rows.Close()
	return scanAttachments(rows)
}

func (s *ProjectAttachmentStore) GetByID(ctx context.Context, orgID, attachmentID uuid.UUID) (models.ProjectAttachment, error) {
	query := fmt.Sprintf(`SELECT %s FROM project_attachments WHERE id = @id AND org_id = @org_id`, attachmentColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     attachmentID,
		"org_id": orgID,
	})
	return scanAttachment(row)
}

func (s *ProjectAttachmentStore) Update(ctx context.Context, a *models.ProjectAttachment) error {
	query := `UPDATE project_attachments SET
		file_name = @file_name, caption = @caption, category = @category,
		sort_order = @sort_order, updated_at = now()
		WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":         a.ID,
		"org_id":     a.OrgID,
		"file_name":  a.FileName,
		"caption":    a.Caption,
		"category":   a.Category,
		"sort_order": a.SortOrder,
	})
	return err
}

func (s *ProjectAttachmentStore) Delete(ctx context.Context, orgID, attachmentID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM project_attachments WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": attachmentID, "org_id": orgID},
	)
	return err
}
