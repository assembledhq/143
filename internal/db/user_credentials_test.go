package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var userCredentialTestColumns = []string{
	"id", "user_id", "org_id", "provider", "config", "status", "last_verified_at", "created_at", "updated_at",
}

func newMockUserCredentialStore(t *testing.T) (*UserCredentialStore, pgxmock.PgxPoolIface) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	store := NewUserCredentialStore(mock, nil)
	return store, mock
}

func encryptedUserConfig(t *testing.T, store *UserCredentialStore, cfg models.ProviderConfig) []byte {
	t.Helper()

	data, err := store.encrypt(mustJSON(t, cfg))
	require.NoError(t, err, "test config should encrypt")
	return data
}

func userCredentialRow(t *testing.T, store *UserCredentialStore, orgID, userID, id uuid.UUID, provider models.ProviderName, cfg models.ProviderConfig) []any {
	t.Helper()

	now := time.Now().UTC()
	return []any{
		id,
		userID,
		orgID,
		string(provider),
		encryptedUserConfig(t, store, cfg),
		"active",
		nil,
		now,
		now,
	}
}

func mustJSON(t *testing.T, cfg models.ProviderConfig) []byte {
	t.Helper()

	data, err := json.Marshal(cfg)
	require.NoError(t, err, "provider config should marshal")
	return data
}

func TestUserCredentialStoreUpsertAndQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(t *testing.T, mock pgxmock.PgxPoolIface, store *UserCredentialStore, orgID, userID, id uuid.UUID)
		call     func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error)
		wantRows int
	}{
		{
			name: "upsert",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *UserCredentialStore, orgID, userID, id uuid.UUID) {
				mock.ExpectQuery("INSERT INTO user_credentials").
					WithArgs(codingAnyArgs(4)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error) {
				err := store.Upsert(ctx, userID, orgID, models.GitHubAppUserConfig{AccessToken: "ghu_token_123456"})
				return nil, err
			},
		},
		{
			name: "get for user",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *UserCredentialStore, orgID, userID, id uuid.UUID) {
				mock.ExpectQuery("FROM user_credentials").
					WithArgs(codingAnyArgs(3)...).
					WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).AddRow(userCredentialRow(t, store, orgID, userID, id, models.ProviderGitHubAppUser, models.GitHubAppUserConfig{AccessToken: "ghu_token_123456"})...))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error) {
				got, err := store.GetForUser(ctx, orgID, userID, models.ProviderGitHubAppUser)
				if err != nil {
					return nil, err
				}
				return []models.DecryptedUserCredential{*got}, nil
			},
			wantRows: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, mock := newMockUserCredentialStore(t)
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			id := uuid.New()
			tt.setup(t, mock, store, orgID, userID, id)

			got, err := tt.call(context.Background(), store, orgID, userID)

			require.NoError(t, err, "user credential store call should not return an error")
			require.Len(t, got, tt.wantRows, "user credential store call should return the expected rows")
			if tt.wantRows > 0 {
				require.Equal(t, orgID, got[0].OrgID, "decrypted credential should preserve org id")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserCredentialStoreDisable(t *testing.T) {
	t.Parallel()

	store, mock := newMockUserCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectExec("UPDATE user_credentials").
		WithArgs(codingAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err := store.Disable(context.Background(), orgID, userID, models.ProviderGitHubAppUser)

	require.NoError(t, err, "Disable should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
