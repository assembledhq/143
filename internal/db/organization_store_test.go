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

var organizationColumns = []string{
	"id", "name", "slug", "settings", "created_at", "updated_at",
}

func TestOrganizationStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewOrganizationStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	org := &models.Organization{
		Name:     "Test Org",
		Slug:     "test-org",
		Settings: json.RawMessage(`{"feature_flags":[]}`),
	}

	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), org)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, generatedID, org.ID, "should set the generated ID on the organization")
	require.Equal(t, now, org.CreatedAt, "should set the created_at timestamp on the organization")
	require.Equal(t, now, org.UpdatedAt, "should set the updated_at timestamp on the organization")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestOrganizationStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns organization when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(organizationColumns).
							AddRow(orgID, "Test Org", "test-org", json.RawMessage(`{}`), now, now),
					)
			},
		},
		{
			name: "returns error when organization not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(organizationColumns))
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

			store := NewOrganizationStore(mock)
			orgID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, now)

			org, err := store.GetByID(context.Background(), orgID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error when organization is not found")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, orgID, org.ID, "should return the correct organization ID")
			require.Equal(t, "Test Org", org.Name, "should return the correct organization name")
			require.Equal(t, "test-org", org.Slug, "should return the correct organization slug")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestOrganizationStore_GetBySlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns organization when found by slug",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE slug").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(organizationColumns).
							AddRow(orgID, "My Company", "my-company", json.RawMessage(`{}`), now, now),
					)
			},
		},
		{
			name: "returns error when organization not found by slug",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE slug").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(organizationColumns))
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

			store := NewOrganizationStore(mock)
			orgID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, now)

			org, err := store.GetBySlug(context.Background(), "my-company")
			if tt.expectErr {
				require.Error(t, err, "GetBySlug should return an error when organization is not found")
				return
			}
			require.NoError(t, err, "GetBySlug should not return an error")
			require.Equal(t, orgID, org.ID, "should return the correct organization ID")
			require.Equal(t, "My Company", org.Name, "should return the correct organization name")
			require.Equal(t, "my-company", org.Slug, "should return the correct organization slug")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestOrganizationStore_Update(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewOrganizationStore(mock)
	now := time.Now()

	org := &models.Organization{
		ID:       uuid.New(),
		Name:     "Updated Org",
		Slug:     "updated-org",
		Settings: json.RawMessage(`{"updated":true}`),
	}

	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).
				AddRow(now),
		)

	err = store.Update(context.Background(), org)
	require.NoError(t, err, "Update should not return an error")
	require.Equal(t, now, org.UpdatedAt, "should set the updated_at timestamp on the organization")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
