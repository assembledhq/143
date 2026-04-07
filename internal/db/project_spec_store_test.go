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

var specTestCols = []string{
	"id", "project_id", "org_id", "title", "content", "spec_type",
	"sort_order", "version", "created_by", "created_at", "updated_at",
}

func newSpecRow(id, projectID, orgID uuid.UUID, title, content string, now time.Time) []any {
	return []any{
		id, projectID, orgID, title, content, "prd",
		0, 1, (*uuid.UUID)(nil), now, now,
	}
}

func TestProjectSpecStore_Create(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectSpecStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	spec := &models.ProjectSpec{
		ProjectID: uuid.New(),
		OrgID:     uuid.New(),
		Title:     "Requirements",
		Content:   "## Overview\nBuild feature X",
		SpecType:  "prd",
	}

	mock.ExpectQuery("INSERT INTO project_specs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "version", "created_at", "updated_at"}).
				AddRow(generatedID, 1, now, now),
		)

	err = store.Create(context.Background(), spec)
	require.NoError(t, err)
	require.Equal(t, generatedID, spec.ID)
	require.Equal(t, 1, spec.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecStore_ListByProject(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectSpecStore(mock)
	orgID := uuid.New()
	projectID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(specTestCols).
				AddRow(newSpecRow(uuid.New(), projectID, orgID, "Spec A", "content A", now)...).
				AddRow(newSpecRow(uuid.New(), projectID, orgID, "Spec B", "content B", now)...),
		)

	specs, err := store.ListByProject(context.Background(), orgID, projectID)
	require.NoError(t, err)
	require.Len(t, specs, 2)
	require.Equal(t, "Spec A", specs[0].Title)
	require.Equal(t, "Spec B", specs[1].Title)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecStore_GetByID(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectSpecStore(mock)
	orgID := uuid.New()
	specID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(specTestCols).
				AddRow(newSpecRow(specID, uuid.New(), orgID, "My Spec", "spec content", now)...),
		)

	spec, err := store.GetByID(context.Background(), orgID, specID)
	require.NoError(t, err)
	require.Equal(t, specID, spec.ID)
	require.Equal(t, "My Spec", spec.Title)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecStore_Update(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectSpecStore(mock)
	now := time.Now()

	spec := &models.ProjectSpec{
		ID:       uuid.New(),
		OrgID:    uuid.New(),
		Title:    "Updated Spec",
		Content:  "new content",
		SpecType: "prd",
	}

	mock.ExpectQuery("UPDATE project_specs SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"version", "updated_at"}).
				AddRow(2, now),
		)

	err = store.Update(context.Background(), spec)
	require.NoError(t, err)
	require.Equal(t, 2, spec.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecStore_Delete(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewProjectSpecStore(mock)

	mock.ExpectExec("DELETE FROM project_specs WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.Delete(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
