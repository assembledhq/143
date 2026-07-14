package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var organizationColumns = []string{
	"id", "name", "release_channel", "settings", "created_at", "updated_at",
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
		Settings: json.RawMessage(`{"feature_flags":[]}`),
	}

	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
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
							AddRow(orgID, "Test Org", "canary", json.RawMessage(`{}`), now, now),
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
		Settings: json.RawMessage(`{"updated":true}`),
	}

	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).
				AddRow(now),
		)

	err = store.Update(context.Background(), org)
	require.NoError(t, err, "Update should not return an error")
	require.Equal(t, now, org.UpdatedAt, "should set the updated_at timestamp on the organization")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestOrganizationStore_MergeCodingAgentDefaults(t *testing.T) {
	t.Parallel()

	t.Run("merges defaults into the nested agent_config path", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewOrganizationStore(mock)
		orgID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("UPDATE organizations").
			WithArgs("amp", pgxmock.AnyArg(), orgID).
			WillReturnRows(
				pgxmock.NewRows([]string{"updated_at"}).
					AddRow(now),
			)

		err = store.MergeCodingAgentDefaults(context.Background(), orgID, models.AgentTypeAmp, map[string]string{"AMP_MODE": models.AmpModeDeep})
		require.NoError(t, err, "MergeCodingAgentDefaults should not return an error for valid defaults")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("rejects invalid defaults before touching the database", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewOrganizationStore(mock)

		err = store.MergeCodingAgentDefaults(context.Background(), uuid.New(), models.AgentTypeAmp, map[string]string{"AMP_MODE": "turbo"})
		require.Error(t, err, "MergeCodingAgentDefaults should reject invalid agent defaults")
		require.Contains(t, err.Error(), "agent_config.amp.AMP_MODE", "MergeCodingAgentDefaults should surface the validation error")
		require.NoError(t, mock.ExpectationsWereMet(), "validation failures should not hit the database")
	})

	t.Run("returns early when no defaults are supplied", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewOrganizationStore(mock)
		err = store.MergeCodingAgentDefaults(context.Background(), uuid.New(), models.AgentTypeAmp, nil)
		require.NoError(t, err, "MergeCodingAgentDefaults should treat empty defaults as a no-op")
		require.NoError(t, mock.ExpectationsWereMet(), "no-op merges should not hit the database")
	})

	t.Run("surfaces database update errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewOrganizationStore(mock)
		orgID := uuid.New()

		mock.ExpectQuery("UPDATE organizations").
			WithArgs("amp", pgxmock.AnyArg(), orgID).
			WillReturnError(errors.New("db failed"))

		err = store.MergeCodingAgentDefaults(context.Background(), orgID, models.AgentTypeAmp, map[string]string{"AMP_MODE": models.AmpModeDeep})
		require.Error(t, err, "MergeCodingAgentDefaults should return database errors")
		require.Contains(t, err.Error(), "merge coding agent defaults", "MergeCodingAgentDefaults should wrap database failures")
	})
}
