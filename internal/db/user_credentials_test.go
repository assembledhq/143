package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type recordingCodingCredentialMirror struct {
	userRows []models.UserCredential
}

func (m *recordingCodingCredentialMirror) MirrorOrgCredential(context.Context, models.OrgCredential, models.ProviderConfig) error {
	return nil
}
func (m *recordingCodingCredentialMirror) MirrorOrgCredentialDelete(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (m *recordingCodingCredentialMirror) MirrorOrgCredentialDisable(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (m *recordingCodingCredentialMirror) MirrorUserCredential(_ context.Context, row models.UserCredential, _ models.ProviderConfig) error {
	m.userRows = append(m.userRows, row)
	return nil
}
func (m *recordingCodingCredentialMirror) MirrorUserCredentialDelete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.ProviderName) error {
	return nil
}
func (m *recordingCodingCredentialMirror) MirrorUserCredentialDisable(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.ProviderName) error {
	return nil
}

var userCredentialTestColumns = []string{
	"id", "user_id", "org_id", "provider", "config", "is_team_default", "status", "last_verified_at", "created_at", "updated_at",
}

func newMockUserCredentialStore(t *testing.T) (*UserCredentialStore, pgxmock.PgxPoolIface) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	store := NewUserCredentialStore(mock, nil)
	store.codingMirror = nil
	return store, mock
}

func encryptedUserConfig(t *testing.T, store *UserCredentialStore, cfg models.ProviderConfig) []byte {
	t.Helper()

	data, err := store.encrypt(mustJSON(t, cfg))
	require.NoError(t, err, "test config should encrypt")
	return data
}

func userCredentialRow(t *testing.T, store *UserCredentialStore, orgID, userID, id uuid.UUID, provider models.ProviderName, cfg models.ProviderConfig, teamDefault bool) []any {
	t.Helper()

	now := time.Now().UTC()
	return []any{
		id,
		userID,
		orgID,
		string(provider),
		encryptedUserConfig(t, store, cfg),
		teamDefault,
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

func TestUserCredentialStoreConfiguration(t *testing.T) {
	t.Parallel()

	store, mock := newMockUserCredentialStore(t)
	defer mock.Close()

	called := false
	store.SetMirrorLogger(func(format string, args ...any) {
		called = true
		require.Contains(t, format, "coding_credentials user mirror", "mirror logger should use the expected message prefix")
	})
	store.logMirrorFailure("test", uuid.New(), assertErr("mirror failed"))
	require.True(t, called, "logMirrorFailure should invoke the configured logger when an error is present")

	store.SetCodingMirror(nil)
	require.NotNil(t, store.codingMirror, "nil mirror should install a no-op mirror")
}

func TestUserCredentialStoreUpsertReturnsMirrorError(t *testing.T) {
	t.Parallel()

	store, mock := newMockUserCredentialStore(t)
	defer mock.Close()

	mirrorErr := errors.New("mirror unavailable")
	store.SetCodingMirror(failingCodingCredentialMirror{err: mirrorErr})

	orgID := uuid.New()
	userID := uuid.New()
	credID := uuid.New()
	now := time.Now().UTC()
	cfg := models.OpenAIConfig{APIKey: "sk-openai-123456"}

	mock.ExpectQuery("INSERT INTO user_credentials").
		WithArgs(codingAnyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(credID, now, now))
	mock.ExpectQuery("SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at").
		WithArgs(credID, orgID).
		WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).
			AddRow(userCredentialRow(t, store, orgID, userID, credID, models.ProviderOpenAI, cfg, false)...))

	err := store.Upsert(context.Background(), userID, orgID, cfg, false)

	require.ErrorIs(t, err, mirrorErr, "Upsert should return coding credential mirror failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserCredentialStoreDisableReturnsMirrorError(t *testing.T) {
	t.Parallel()

	store, mock := newMockUserCredentialStore(t)
	defer mock.Close()

	mirrorErr := errors.New("mirror unavailable")
	store.SetCodingMirror(failingCodingCredentialMirror{err: mirrorErr})

	orgID := uuid.New()
	userID := uuid.New()
	credID := uuid.New()

	mock.ExpectQuery("UPDATE user_credentials").
		WithArgs(codingAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(credID))

	err := store.Disable(context.Background(), orgID, userID, models.ProviderOpenAI)

	require.ErrorIs(t, err, mirrorErr, "Disable should return coding credential mirror failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
					WithArgs(codingAnyArgs(5)...).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(id, time.Now(), time.Now()))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error) {
				err := store.Upsert(ctx, userID, orgID, models.OpenAIConfig{APIKey: "sk-openai-123456"}, false)
				return nil, err
			},
		},
		{
			name: "get for user",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *UserCredentialStore, orgID, userID, id uuid.UUID) {
				mock.ExpectQuery("FROM user_credentials").
					WithArgs(codingAnyArgs(3)...).
					WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).AddRow(userCredentialRow(t, store, orgID, userID, id, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-123456"}, false)...))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error) {
				got, err := store.GetForUser(ctx, orgID, userID, models.ProviderOpenAI)
				if err != nil {
					return nil, err
				}
				return []models.DecryptedUserCredential{*got}, nil
			},
			wantRows: 1,
		},
		{
			name: "get team default",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *UserCredentialStore, orgID, userID, id uuid.UUID) {
				mock.ExpectQuery("FROM user_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).AddRow(userCredentialRow(t, store, orgID, userID, id, models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant-123456789"}, true)...))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, _ uuid.UUID) ([]models.DecryptedUserCredential, error) {
				got, err := store.GetTeamDefault(ctx, orgID, models.ProviderAnthropic)
				if err != nil {
					return nil, err
				}
				return []models.DecryptedUserCredential{*got}, nil
			},
			wantRows: 1,
		},
		{
			name: "list by user",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *UserCredentialStore, orgID, userID, id uuid.UUID) {
				mock.ExpectQuery("FROM user_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).AddRow(userCredentialRow(t, store, orgID, userID, id, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-123456"}, false)...))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error) {
				return store.ListByUser(ctx, orgID, userID)
			},
			wantRows: 1,
		},
		{
			name: "list team defaults",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *UserCredentialStore, orgID, userID, id uuid.UUID) {
				mock.ExpectQuery("FROM user_credentials").
					WithArgs(codingAnyArgs(1)...).
					WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).AddRow(userCredentialRow(t, store, orgID, userID, id, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-123456"}, true)...))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, _ uuid.UUID) ([]models.DecryptedUserCredential, error) {
				return store.ListTeamDefaults(ctx, orgID)
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

func TestUserCredentialStoreMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(mock pgxmock.PgxPoolIface, id uuid.UUID)
		call  func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) error
	}{
		{
			name: "disable",
			setup: func(mock pgxmock.PgxPoolIface, id uuid.UUID) {
				mock.ExpectQuery("UPDATE user_credentials").
					WithArgs(codingAnyArgs(3)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) error {
				store.codingMirror = NoopMirror()
				return store.Disable(ctx, orgID, userID, models.ProviderOpenAI)
			},
		},
		{
			name: "clear team default",
			setup: func(mock pgxmock.PgxPoolIface, id uuid.UUID) {
				mock.ExpectQuery("UPDATE user_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, _ uuid.UUID) error {
				return store.ClearTeamDefault(ctx, orgID, models.ProviderOpenAI)
			},
		},
		{
			name: "set team default",
			setup: func(mock pgxmock.PgxPoolIface, id uuid.UUID) {
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE user_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}))
				mock.ExpectQuery("UPDATE user_credentials").
					WithArgs(codingAnyArgs(3)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, userID uuid.UUID) error {
				return store.SetTeamDefault(ctx, orgID, userID, models.ProviderOpenAI)
			},
		},
		{
			name: "remove team default",
			setup: func(mock pgxmock.PgxPoolIface, id uuid.UUID) {
				mock.ExpectQuery("UPDATE user_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
			},
			call: func(ctx context.Context, store *UserCredentialStore, orgID, _ uuid.UUID) error {
				return store.RemoveTeamDefault(ctx, orgID, models.ProviderOpenAI)
			},
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
			tt.setup(mock, id)

			err := tt.call(context.Background(), store, orgID, userID)

			require.NoError(t, err, "user credential mutation should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserCredentialStoreSetTeamDefaultMirrorsClearedDefault(t *testing.T) {
	t.Parallel()

	store, mock := newMockUserCredentialStore(t)
	defer mock.Close()

	mirror := &recordingCodingCredentialMirror{}
	store.SetCodingMirror(mirror)

	ctx := context.Background()
	orgID := uuid.New()
	oldUserID := uuid.New()
	newUserID := uuid.New()
	oldID := uuid.New()
	newID := uuid.New()
	provider := models.ProviderOpenAI
	cfg := models.OpenAIConfig{APIKey: "sk-openai-test"}

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE user_credentials").
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(oldID))
	mock.ExpectQuery("UPDATE user_credentials").
		WithArgs(codingAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(newID))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at").
		WithArgs(oldID, orgID).
		WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).
			AddRow(userCredentialRow(t, store, orgID, oldUserID, oldID, provider, cfg, false)...))
	mock.ExpectQuery("SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at").
		WithArgs(newID, orgID).
		WillReturnRows(pgxmock.NewRows(userCredentialTestColumns).
			AddRow(userCredentialRow(t, store, orgID, newUserID, newID, provider, cfg, true)...))

	err := store.SetTeamDefault(ctx, orgID, newUserID, provider)

	require.NoError(t, err, "SetTeamDefault should succeed")
	require.Len(t, mirror.userRows, 2, "SetTeamDefault should mirror both the cleared and newly-set defaults")
	require.Equal(t, oldID, mirror.userRows[0].ID, "first mirrored row should be the previously-cleared default")
	require.False(t, mirror.userRows[0].IsTeamDefault, "cleared default should be mirrored as personal-scoped again")
	require.Equal(t, newID, mirror.userRows[1].ID, "second mirrored row should be the newly-set default")
	require.True(t, mirror.userRows[1].IsTeamDefault, "new default should be mirrored as org-scoped")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
