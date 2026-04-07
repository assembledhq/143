package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var attachmentTestCols = []string{
	"id", "project_id", "org_id", "file_name", "file_url", "file_type",
	"thumbnail_url", "file_size", "category", "caption", "sort_order",
	"uploaded_by", "created_at", "updated_at",
}

func newAttachmentRow(id, projectID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, projectID, orgID, "screenshot.png", "https://cdn.example.com/screenshot.png", "image/png",
		(*string)(nil), (*int)(nil), "design", (*string)(nil), 0,
		(*uuid.UUID)(nil), now, now,
	}
}

func TestProjectAttachmentStore_Create(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectAttachmentStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	a := &models.ProjectAttachment{
		ProjectID: uuid.New(),
		OrgID:     uuid.New(),
		FileName:  "screenshot.png",
		FileURL:   "https://cdn.example.com/screenshot.png",
		FileType:  "image/png",
		Category:  "design",
	}

	mock.ExpectQuery("INSERT INTO project_attachments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, generatedID, a.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentStore_ListByProject(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectAttachmentStore(mock)
	orgID := uuid.New()
	projectID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(attachmentTestCols).
				AddRow(newAttachmentRow(uuid.New(), projectID, orgID, now)...).
				AddRow(newAttachmentRow(uuid.New(), projectID, orgID, now)...),
		)

	attachments, err := store.ListByProject(context.Background(), orgID, projectID)
	require.NoError(t, err)
	require.Len(t, attachments, 2)
	require.Equal(t, "screenshot.png", attachments[0].FileName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentStore_GetByID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectAttachmentStore(mock)
	orgID := uuid.New()
	attachID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(attachmentTestCols).
				AddRow(newAttachmentRow(attachID, uuid.New(), orgID, now)...),
		)

	a, err := store.GetByID(context.Background(), orgID, attachID)
	require.NoError(t, err)
	require.Equal(t, attachID, a.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentStore_Update(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectAttachmentStore(mock)

	a := &models.ProjectAttachment{
		ID:       uuid.New(),
		OrgID:    uuid.New(),
		FileName: "updated.png",
		Category: "mockup",
	}

	mock.ExpectExec("UPDATE project_attachments SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Update(context.Background(), a)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentStore_Delete(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectAttachmentStore(mock)

	mock.ExpectExec("DELETE FROM project_attachments WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.Delete(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
