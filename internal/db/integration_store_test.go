package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var integrationColumns = []string{
	"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at",
}

func TestIntegrationStore_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config json.RawMessage
	}{
		{
			name:   "creates integration with config",
			config: json.RawMessage(`{"token":"abc"}`),
		},
		{
			name:   "creates integration with nil config defaulting to empty object",
			config: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewIntegrationStore(mock)
			now := time.Now()
			generatedID := uuid.New()

			integration := &models.Integration{
				OrgID:    uuid.New(),
				Provider: "github",
				Config:   tt.config,
				Status:   "active",
			}

			mock.ExpectQuery("INSERT INTO integrations").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{"id", "created_at"}).
						AddRow(generatedID, now),
				)

			err = store.Create(context.Background(), integration)
			require.NoError(t, err, "Create should not return an error")
			require.Equal(t, generatedID, integration.ID, "should set the generated ID on the integration")
			require.Equal(t, now, integration.CreatedAt, "should set the created_at timestamp on the integration")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestIntegrationStore_GetByOrgAndProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, integrationID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns integration when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM integrations[\\s\\S]*status IN \\('active', 'error'\\)[\\s\\S]*ORDER BY \\(status = 'active'\\) DESC, created_at DESC[\\s\\S]*LIMIT 1").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(integrationColumns).
							AddRow(integrationID, orgID, "github", json.RawMessage(`{}`), "active", nil, now),
					)
			},
		},
		{
			name: "filters out inactive integrations",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM integrations[\\s\\S]*status IN \\('active', 'error'\\)[\\s\\S]*ORDER BY \\(status = 'active'\\) DESC, created_at DESC[\\s\\S]*LIMIT 1").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(integrationColumns).
							AddRow(integrationID, orgID, "github", json.RawMessage(`{}`), "active", nil, now),
					)
			},
		},
		{
			name: "returns error when integration not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM integrations[\\s\\S]*status IN \\('active', 'error'\\)[\\s\\S]*ORDER BY \\(status = 'active'\\) DESC, created_at DESC[\\s\\S]*LIMIT 1").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(integrationColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewIntegrationStore(mock)
			orgID := uuid.New()
			integrationID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, integrationID, now)

			integration, err := store.GetByOrgAndProvider(context.Background(), orgID, "github")
			if tt.expectErr {
				require.Error(t, err, "GetByOrgAndProvider should return an error when integration is not found")
				return
			}
			require.NoError(t, err, "GetByOrgAndProvider should not return an error")
			require.Equal(t, integrationID, integration.ID, "should return the correct integration ID")
			require.Equal(t, orgID, integration.OrgID, "should return the correct org ID")
			require.Equal(t, models.IntegrationProviderGitHub, integration.Provider, "should return the correct provider")
			require.Equal(t, models.IntegrationStatusActive, integration.Status, "should return the correct status")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestIntegrationStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, integrationID, orgID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns integration when found",
			setupMock: func(mock pgxmock.PgxPoolIface, integrationID, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(integrationColumns).
							AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
					)
			},
		},
		{
			name: "returns error when integration not found",
			setupMock: func(mock pgxmock.PgxPoolIface, integrationID, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(integrationColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewIntegrationStore(mock)
			integrationID := uuid.New()
			orgID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, integrationID, orgID, now)

			integration, err := store.GetByID(context.Background(), integrationID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error when integration is not found")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, integrationID, integration.ID, "should return the correct integration ID")
			require.Equal(t, models.IntegrationProviderSentry, integration.Provider, "should return the correct provider")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestIntegrationStore_ListByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(integrationColumns).
				AddRow(id1, orgID, "github", json.RawMessage(`{}`), "active", nil, now).
				AddRow(id2, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
		)

	integrations, err := store.ListByOrg(context.Background(), orgID)
	require.NoError(t, err, "ListByOrg should not return an error")
	require.Len(t, integrations, 2, "should return both integrations for the org")
	require.Equal(t, id1, integrations[0].ID, "should return the first integration ID")
	require.Equal(t, id2, integrations[1].ID, "should return the second integration ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationStore_ListReusableForReconnect(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM integrations .+ status IN \\('active', 'error'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(integrationColumns).
				AddRow(integrationID, orgID, "linear", json.RawMessage(`{}`), "error", nil, now),
		)

	integrations, err := store.ListReusableForReconnect(context.Background(), orgID, "linear")
	require.NoError(t, err, "ListReusableForReconnect should not return an error")
	require.Equal(t, []models.Integration{
		{
			ID:        integrationID,
			OrgID:     orgID,
			Provider:  models.IntegrationProviderLinear,
			Config:    json.RawMessage(`{}`),
			Status:    models.IntegrationStatusError,
			CreatedAt: now,
		},
	}, integrations, "ListReusableForReconnect should return reusable active or errored integrations")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationStore_UpdateLastSyncedAt(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	syncedAt := time.Now()

	mock.ExpectExec("UPDATE integrations SET last_synced_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateLastSyncedAt(context.Background(), orgID, id, syncedAt)
	require.NoError(t, err, "UpdateLastSyncedAt should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	integrationID := uuid.New()

	mock.ExpectExec("UPDATE integrations SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, integrationID, "inactive")
	require.NoError(t, err, "UpdateStatus should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationStore_UpdateStatusAndConfig(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	integrationID := uuid.New()

	mock.ExpectExec("UPDATE integrations SET status = @status, config = @config").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatusAndConfig(context.Background(), orgID, integrationID, "active", json.RawMessage(`{"workspace_id":"wks-1"}`))
	require.NoError(t, err, "UpdateStatusAndConfig should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIntegrationStore_ListOrgsWithActiveIntegrations(t *testing.T) {
	t.Parallel()

	orgID1 := uuid.New()
	orgID2 := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT DISTINCT org_id FROM integrations WHERE status = 'active'").
		WillReturnRows(
			pgxmock.NewRows([]string{"org_id"}).
				AddRow(orgID1).
				AddRow(orgID2),
		)

	store := NewIntegrationStore(mock)
	orgs, err := store.ListOrgsWithActiveIntegrations(context.Background())
	require.NoError(t, err, "ListOrgsWithActiveIntegrations should succeed")
	require.Equal(t, []uuid.UUID{orgID1, orgID2}, orgs, "should return org IDs with active integrations")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
