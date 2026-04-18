package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var prTemplateColumns = []string{
	"id", "repository_id", "org_id", "template_content", "template_path",
	"fetched_at", "created_at", "updated_at",
}

func TestPRTemplateStore_GetByRepositoryID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPRTemplateStore(mock)
	repoID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM repository_pr_templates WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTemplateColumns).
				AddRow(uuid.New(), repoID, orgID, "## Template", ".github/pull_request_template.md", now, now, now),
		)

	tpl, err := store.GetByRepositoryID(context.Background(), orgID, repoID)
	require.NoError(t, err)
	require.NotNil(t, tpl)
	require.Equal(t, "## Template", tpl.TemplateContent)
	require.Equal(t, ".github/pull_request_template.md", tpl.TemplatePath)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPRTemplateStore_GetByRepositoryID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPRTemplateStore(mock)
	repoID := uuid.New()
	orgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM repository_pr_templates WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTemplateColumns))

	_, err = store.GetByRepositoryID(context.Background(), orgID, repoID)
	require.Error(t, err, "should return error when no template found")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPRTemplateStore_Upsert_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPRTemplateStore(mock)
	repoID := uuid.New()
	orgID := uuid.New()

	mock.ExpectExec("INSERT INTO repository_pr_templates").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = store.Upsert(context.Background(), repoID, orgID, "## Template", ".github/pull_request_template.md")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
