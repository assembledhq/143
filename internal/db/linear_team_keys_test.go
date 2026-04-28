package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLinearTeamKeyStoreReplaceForIntegration_ScopesDeleteByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewLinearTeamKeyStore(mock)
	orgID := uuid.New()
	integrationID := uuid.New()

	mock.ExpectExec(`DELETE FROM linear_team_keys WHERE integration_id = @integration_id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.replaceForIntegrationNoTx(context.Background(), mock, orgID, integrationID, "workspace-1", nil)
	require.NoError(t, err, "replaceForIntegrationNoTx should delete stale team keys for the current org and integration")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
