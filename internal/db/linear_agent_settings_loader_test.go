package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLinearAgentSettingsView_LoadDefaultWorkRepositoryIDFallsBackToFirstActiveRepo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	firstRepoID := uuid.New()
	secondRepoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT id, name, release_channel, settings, created_at, updated_at").
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "release_channel", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", "stable", json.RawMessage(`{}`), now, now))
	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id, full_name").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoColumns).
			AddRow(newRepoRow(firstRepoID, orgID, integrationID, now)...).
			AddRow(newRepoRow(secondRepoID, orgID, integrationID, now)...))

	view := LinearAgentSettingsView{
		Orgs:  NewOrganizationStore(mock),
		Repos: NewRepositoryStore(mock),
	}

	got, err := view.LoadDefaultWorkRepositoryID(context.Background(), orgID)

	require.NoError(t, err, "shared default loader should fall back to repositories without error")
	require.NotNil(t, got, "shared default loader should choose a repository when none is configured")
	require.Equal(t, firstRepoID, *got, "shared default loader should choose the first active repository returned by ListByOrg")
	require.NoError(t, mock.ExpectationsWereMet(), "all default repository lookup expectations should be met")
}
