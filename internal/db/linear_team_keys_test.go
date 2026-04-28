package db

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestLinearTeamKeyStore_ListByOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expected    []LinearTeamKey
		expectedErr string
	}{
		{
			name: "returns team keys",
			setup: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now().UTC()
				mock.ExpectQuery("SELECT org_id, integration_id, workspace_id, team_id, team_key, team_name, refreshed_at").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"org_id", "integration_id", "workspace_id", "team_id", "team_key", "team_name", "refreshed_at"}).
						AddRow(orgID, uuid.MustParse("11111111-1111-1111-1111-111111111111"), "workspace-1", "team-1", "ACS", "Core", now))
			},
			expected: []LinearTeamKey{{
				IntegrationID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
				WorkspaceID:   "workspace-1",
				TeamID:        "team-1",
				TeamKey:       "ACS",
				TeamName:      "Core",
			}},
		},
		{
			name: "wraps query errors",
			setup: func(mock pgxmock.PgxPoolIface, _ uuid.UUID) {
				mock.ExpectQuery("SELECT org_id, integration_id, workspace_id, team_id, team_key, team_name, refreshed_at").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(errors.New("db unavailable"))
			},
			expectedErr: "query linear team keys",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			orgID := uuid.New()
			tt.setup(mock, orgID)
			got, err := NewLinearTeamKeyStore(mock).ListByOrg(context.Background(), orgID)
			if tt.expectedErr != "" {
				require.Error(t, err, "ListByOrg should return the expected error")
				require.Contains(t, err.Error(), tt.expectedErr, "ListByOrg should wrap query errors")
			} else {
				require.NoError(t, err, "ListByOrg should succeed")
				require.Len(t, got, len(tt.expected), "ListByOrg should return expected row count")
				require.Equal(t, orgID, got[0].OrgID, "ListByOrg should decode org id")
				require.Equal(t, tt.expected[0].TeamKey, got[0].TeamKey, "ListByOrg should decode team key")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestLinearTeamKeyStore_ReplaceForIntegration(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	store := NewLinearTeamKeyStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM linear_team_keys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectExec("INSERT INTO linear_team_keys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = store.ReplaceForIntegration(context.Background(), orgID, integrationID, "workspace-1", []LinearTeamKey{{TeamID: "team-1", TeamKey: "ACS", TeamName: "Core"}})
	require.NoError(t, err, "ReplaceForIntegration should wrap delete and insert in a transaction")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestLinearTeamKeyStore_ReplaceForIntegrationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(mock pgxmock.PgxPoolIface)
		expectedErr string
	}{
		{
			name: "begin error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectBegin().WillReturnError(errors.New("begin failed"))
			},
			expectedErr: "begin linear team keys tx",
		},
		{
			name: "insert error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectBegin()
				mock.ExpectExec("DELETE FROM linear_team_keys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("DELETE", 1))
				mock.ExpectExec("INSERT INTO linear_team_keys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("insert failed"))
				mock.ExpectRollback()
			},
			expectedErr: "insert linear team key",
		},
		{
			name: "commit error",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectBegin()
				mock.ExpectExec("DELETE FROM linear_team_keys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("DELETE", 1))
				mock.ExpectExec("INSERT INTO linear_team_keys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			},
			expectedErr: "commit linear team keys",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setup(mock)
			err = NewLinearTeamKeyStore(mock).ReplaceForIntegration(context.Background(), uuid.New(), uuid.New(), "workspace-1", []LinearTeamKey{{TeamID: "team-1", TeamKey: "ACS", TeamName: "Core"}})
			require.Error(t, err, "ReplaceForIntegration should return the expected error")
			require.Contains(t, err.Error(), tt.expectedErr, "ReplaceForIntegration should wrap errors with context")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
