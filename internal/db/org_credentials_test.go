package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

var credColumns = []string{"id", "org_id", "provider", "label", "config", "status", "last_verified_at", "last_used_at", "created_by", "created_at", "updated_at"}

func TestOrgCredentialStore_Upsert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       models.ProviderConfig
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "upserts anthropic config",
			cfg:  models.AnthropicConfig{APIKey: "sk-ant-test", BaseURL: ""},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			},
		},
		{
			name: "upserts openai config",
			cfg:  models.OpenAIConfig{APIKey: "sk-test", APIType: "chat"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			},
		},
		{
			name: "db error",
			cfg:  models.AnthropicConfig{APIKey: "sk-ant-test"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "creating mock pool should not error")
			defer mock.Close()

			store := NewOrgCredentialStore(mock, nil)
			tt.setupMock(mock)

			orgID := uuid.New()
			err = store.Upsert(context.Background(), orgID, tt.cfg)
			if tt.expectErr {
				require.Error(t, err, "Upsert should return an error")
				return
			}
			require.NoError(t, err, "Upsert should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestOrgCredentialStore_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		provider  models.ProviderName
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name:     "gets anthropic credential",
			provider: models.ProviderAnthropic,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				configData := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-test","base_url":""}`))
				mock.ExpectQuery("SELECT .* FROM org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns).
						AddRow(uuid.New(), uuid.New(), "anthropic", "", configData, "active", nil, nil, nil, time.Now(), time.Now()))
			},
		},
		{
			name:     "not found",
			provider: models.ProviderAnthropic,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .* FROM org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "creating mock pool should not error")
			defer mock.Close()

			store := NewOrgCredentialStore(mock, nil)
			tt.setupMock(mock)

			cred, err := store.Get(context.Background(), uuid.New(), tt.provider)
			if tt.expectErr {
				require.Error(t, err, "Get should return an error")
				return
			}
			require.NoError(t, err, "Get should not return an error")
			require.NotNil(t, cred, "Get should return a credential")
			require.Equal(t, tt.provider, cred.Provider, "credential should have correct provider")
			require.NotNil(t, cred.Config, "credential should have a config")
		})
	}
}

func TestOrgCredentialStore_GetAllLLM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name: "returns LLM credentials",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				anthropicData := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-test"}`))
				openaiData := crypto.DevEncrypt([]byte(`{"api_key":"sk-test","api_type":"chat"}`))
				mock.ExpectQuery("SELECT .* FROM org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns).
						AddRow(uuid.New(), uuid.New(), "anthropic", "", anthropicData, "active", nil, nil, nil, time.Now(), time.Now()).
						AddRow(uuid.New(), uuid.New(), "openai", "", openaiData, "active", nil, nil, nil, time.Now(), time.Now()))
			},
			expected: 2,
		},
		{
			name: "returns empty when no LLM credentials",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .* FROM org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns))
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "creating mock pool should not error")
			defer mock.Close()

			store := NewOrgCredentialStore(mock, nil)
			tt.setupMock(mock)

			creds, err := store.GetAllLLM(context.Background(), uuid.New())
			if tt.expectErr {
				require.Error(t, err, "GetAllLLM should return an error")
				return
			}
			require.NoError(t, err, "GetAllLLM should not return an error")
			require.Len(t, creds, tt.expected, "GetAllLLM should return expected number of credentials")
		})
	}
}

func TestOrgCredentialStore_ListSummaries(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	store := NewOrgCredentialStore(mock, nil)

	anthropicData := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-api03-longkeyhere"}`))
	mock.ExpectQuery("SELECT .* FROM org_credentials").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(credColumns).
			AddRow(uuid.New(), uuid.New(), "anthropic", "", anthropicData, "active", nil, nil, nil, time.Now(), time.Now()))

	summaries, err := store.ListSummaries(context.Background(), uuid.New())
	require.NoError(t, err, "ListSummaries should not return an error")
	require.Len(t, summaries, len(models.AllProviders), "ListSummaries should return a summary for every known provider")

	// Find the anthropic summary.
	var anthropicSummary *models.CredentialSummary
	for i, s := range summaries {
		if s.Provider == models.ProviderAnthropic {
			anthropicSummary = &summaries[i]
			break
		}
	}
	require.NotNil(t, anthropicSummary, "should have anthropic summary")
	require.True(t, anthropicSummary.Configured, "anthropic should be configured")
	require.Equal(t, "active", anthropicSummary.Status, "anthropic should be active")
	require.NotEmpty(t, anthropicSummary.MaskedKey, "anthropic should have masked key")
	require.NotContains(t, anthropicSummary.MaskedKey, "sk-ant-api03-longkeyhere", "masked key should not contain full key")

	// Find an unconfigured provider.
	var openaiSummary *models.CredentialSummary
	for i, s := range summaries {
		if s.Provider == models.ProviderOpenAI {
			openaiSummary = &summaries[i]
			break
		}
	}
	require.NotNil(t, openaiSummary, "should have openai summary")
	require.False(t, openaiSummary.Configured, "openai should not be configured")
}

func TestOrgCredentialStore_Disable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "disables credential",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "db error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "creating mock pool should not error")
			defer mock.Close()

			store := NewOrgCredentialStore(mock, nil)
			tt.setupMock(mock)

			err = store.Disable(context.Background(), uuid.New(), models.ProviderAnthropic)
			if tt.expectErr {
				require.Error(t, err, "Disable should return an error")
				return
			}
			require.NoError(t, err, "Disable should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestOrgCredentialStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	store := NewOrgCredentialStore(mock, nil)

	mock.ExpectExec("UPDATE org_credentials").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), uuid.New(), models.ProviderAnthropic, "active")
	require.NoError(t, err, "UpdateStatus should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
