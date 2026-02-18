package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var integrationColumns = []string{
	"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at",
}

func TestIntegrationStore_Create_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	integration := &models.Integration{
		OrgID:    uuid.New(),
		Provider: "github",
		Config:   json.RawMessage(`{"token":"abc"}`),
		Status:   "active",
	}

	// 4 named args: org_id, provider, config, status
	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), integration)
	require.NoError(t, err)
	assert.Equal(t, generatedID, integration.ID)
	assert.Equal(t, now, integration.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_Create_NilConfig(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	integration := &models.Integration{
		OrgID:    uuid.New(),
		Provider: "sentry",
		Config:   nil, // nil config should be defaulted to {}
		Status:   "active",
	}

	// 4 named args: org_id, provider, config, status
	mock.ExpectQuery("INSERT INTO integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), integration)
	require.NoError(t, err)
	assert.Equal(t, generatedID, integration.ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_GetByOrgAndProvider_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	// 2 named args: org_id, provider
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(integrationColumns).
				AddRow(integrationID, orgID, "github", json.RawMessage(`{}`), "active", nil, now),
		)

	integration, err := store.GetByOrgAndProvider(context.Background(), orgID, "github")
	require.NoError(t, err)
	assert.Equal(t, integrationID, integration.ID)
	assert.Equal(t, orgID, integration.OrgID)
	assert.Equal(t, "github", integration.Provider)
	assert.Equal(t, "active", integration.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_GetByOrgAndProvider_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()

	// 2 named args: org_id, provider
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(integrationColumns))

	_, err = store.GetByOrgAndProvider(context.Background(), orgID, "github")
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_GetByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	integrationID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	// 1 named arg: id
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(integrationColumns).
				AddRow(integrationID, orgID, "sentry", json.RawMessage(`{}`), "active", nil, now),
		)

	integration, err := store.GetByID(context.Background(), integrationID)
	require.NoError(t, err)
	assert.Equal(t, integrationID, integration.ID)
	assert.Equal(t, "sentry", integration.Provider)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_GetByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	integrationID := uuid.New()

	// 1 named arg: id
	mock.ExpectQuery("SELECT .+ FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(integrationColumns))

	_, err = store.GetByID(context.Background(), integrationID)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_ListByOrg_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Len(t, integrations, 2)
	assert.Equal(t, id1, integrations[0].ID)
	assert.Equal(t, id2, integrations[1].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_UpdateLastSyncedAt_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	syncedAt := time.Now()

	mock.ExpectExec("UPDATE integrations SET last_synced_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateLastSyncedAt(context.Background(), orgID, id, syncedAt)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIntegrationStore_UpdateStatus_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIntegrationStore(mock)
	orgID := uuid.New()
	integrationID := uuid.New()

	// 3 named args: id, org_id, status
	mock.ExpectExec("UPDATE integrations SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, integrationID, "inactive")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
