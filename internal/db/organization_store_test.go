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

var organizationColumns = []string{
	"id", "name", "slug", "settings", "created_at", "updated_at",
}

func TestOrganizationStore_Create_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	org := &models.Organization{
		Name:     "Test Org",
		Slug:     "test-org",
		Settings: json.RawMessage(`{"feature_flags":[]}`),
	}

	// 3 named args: name, slug, settings
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), org)
	require.NoError(t, err)
	assert.Equal(t, generatedID, org.ID)
	assert.Equal(t, now, org.CreatedAt)
	assert.Equal(t, now, org.UpdatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationStore_GetByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationStore(mock)
	orgID := uuid.New()
	now := time.Now()

	// 1 named arg: id
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(organizationColumns).
				AddRow(orgID, "Test Org", "test-org", json.RawMessage(`{}`), now, now),
		)

	org, err := store.GetByID(context.Background(), orgID)
	require.NoError(t, err)
	assert.Equal(t, orgID, org.ID)
	assert.Equal(t, "Test Org", org.Name)
	assert.Equal(t, "test-org", org.Slug)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationStore_GetByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationStore(mock)
	orgID := uuid.New()

	// 1 named arg: id
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(organizationColumns))

	_, err = store.GetByID(context.Background(), orgID)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationStore_GetBySlug_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationStore(mock)
	orgID := uuid.New()
	now := time.Now()

	// 1 named arg: slug
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE slug").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(organizationColumns).
				AddRow(orgID, "My Company", "my-company", json.RawMessage(`{}`), now, now),
		)

	org, err := store.GetBySlug(context.Background(), "my-company")
	require.NoError(t, err)
	assert.Equal(t, orgID, org.ID)
	assert.Equal(t, "My Company", org.Name)
	assert.Equal(t, "my-company", org.Slug)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationStore_GetBySlug_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationStore(mock)

	// 1 named arg: slug
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE slug").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(organizationColumns))

	_, err = store.GetBySlug(context.Background(), "nonexistent")
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationStore_Update_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationStore(mock)
	now := time.Now()

	org := &models.Organization{
		ID:       uuid.New(),
		Name:     "Updated Org",
		Slug:     "updated-org",
		Settings: json.RawMessage(`{"updated":true}`),
	}

	// 4 named args: id, name, slug, settings
	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).
				AddRow(now),
		)

	err = store.Update(context.Background(), org)
	require.NoError(t, err)
	assert.Equal(t, now, org.UpdatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}
