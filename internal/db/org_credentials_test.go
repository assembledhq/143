package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func TestOrgCredentialStore_UpdateStatusByIDReturnsNoRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	credID := uuid.New()
	store := NewOrgCredentialStore(mock, nil)

	mock.ExpectExec(`UPDATE org_credentials SET status = @status, last_verified_at = now\(\), updated_at = now\(\) WHERE id = @id AND org_id = @org_id`).
		WithArgs(string(models.CredentialStatusInvalid), credID, orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.UpdateStatusByID(context.Background(), orgID, credID, models.CredentialStatusInvalid)

	require.ErrorIs(t, err, pgx.ErrNoRows, "UpdateStatusByID should report not found when the scoped update touches zero rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestOrgCredentialStore_UpsertWithLabel_NonCodingProvider(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery(`INSERT INTO org_credentials`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	store := NewOrgCredentialStore(mock, nil)
	_, err = store.UpsertWithLabel(context.Background(), orgID, nil, "github-oauth", models.GitHubOAuthConfig{
		ClientID:    "client",
		AccessToken: "gho_test",
	})
	require.NoError(t, err, "UpsertWithLabel should not return an error for non-coding providers")
	require.NoError(t, mock.ExpectationsWereMet(), "no MAX(priority) query should be issued for non-coding providers")
}

func TestOrgCredentialStore_UpdateLinearConfigIfRefreshTokenMatches(t *testing.T) {
	t.Parallel()

	t.Run("updates when refresh token matches", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		credentialID := uuid.New()
		now := time.Now().UTC()
		priorJSON, err := json.Marshal(models.LinearConfig{
			AccessToken:  "lin_at_old",
			RefreshToken: "lin_rt_old",
			ExpiresAt:    now.Add(time.Minute),
		})
		require.NoError(t, err, "prior config should marshal")

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT .* FROM org_credentials .* FOR UPDATE`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(credColumns).
				AddRow(credentialID, orgID, string(models.ProviderLinear), "", crypto.DevEncrypt(priorJSON), "active", nil, nil, nil, now, now))
		mock.ExpectExec(`UPDATE org_credentials SET config = .* WHERE id = .* AND org_id = .*`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit()

		store := NewOrgCredentialStore(mock, nil)
		merged := models.LinearConfig{
			AccessToken:  "lin_at_new",
			RefreshToken: "lin_rt_new",
			ExpiresAt:    now.Add(2 * time.Hour),
		}
		current, updated, err := store.UpdateLinearConfigIfRefreshTokenMatches(context.Background(), orgID, "lin_rt_old", merged)
		require.NoError(t, err, "matching refresh token should update")
		require.True(t, updated, "matching refresh token should report updated")
		require.Equal(t, merged, current, "matching refresh token should return the persisted config")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("skips update when refresh token changed", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		credentialID := uuid.New()
		now := time.Now().UTC()
		reconnected := models.LinearConfig{
			AccessToken:   "lin_at_reconnected",
			RefreshToken:  "lin_rt_reconnected",
			ExpiresAt:     now.Add(2 * time.Hour),
			WorkspaceID:   "wks-new",
			WorkspaceName: "Reconnected Workspace",
		}
		currentJSON, err := json.Marshal(reconnected)
		require.NoError(t, err, "current config should marshal")

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT .* FROM org_credentials .* FOR UPDATE`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(credColumns).
				AddRow(credentialID, orgID, string(models.ProviderLinear), "", crypto.DevEncrypt(currentJSON), "active", nil, nil, nil, now, now))
		mock.ExpectCommit()

		store := NewOrgCredentialStore(mock, nil)
		current, updated, err := store.UpdateLinearConfigIfRefreshTokenMatches(context.Background(), orgID, "lin_rt_old", models.LinearConfig{
			AccessToken:  "lin_at_from_old_chain",
			RefreshToken: "lin_rt_from_old_chain",
			ExpiresAt:    now.Add(2 * time.Hour),
		})
		require.NoError(t, err, "changed refresh token should not error")
		require.False(t, updated, "changed refresh token should report no update")
		require.Equal(t, reconnected, current, "changed refresh token should return the current row for race recovery")
		require.NoError(t, mock.ExpectationsWereMet(), "no UPDATE should be issued when the refresh token changed")
	})
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

// TestOrgCredentialStore_Get_FiltersLabelEmpty asserts the contract that
// Get returns only the singleton label=” row. Providers that mix an
// API-key row (label=”) with labeled subscription rows (label!=”)
// depend on this filter so resolveProviderConfig doesn't accidentally
// return a subscription row when an API key is expected. If this test
// ever breaks, audit every Get caller in the repo before relaxing the
// filter.
func TestOrgCredentialStore_Get_FiltersLabelEmpty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// The SQL must include `label = ''` — this regex enforces it.
	mock.ExpectQuery(`SELECT .* FROM org_credentials .* label = ''`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(credColumns).
			AddRow(uuid.New(), uuid.New(), "anthropic", "", crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-test"}`)), "active", nil, nil, nil, time.Now(), time.Now()))

	store := NewOrgCredentialStore(mock, nil)
	cred, err := store.Get(context.Background(), uuid.New(), models.ProviderAnthropic)
	require.NoError(t, err)
	require.NotNil(t, cred)
	require.Empty(t, cred.Label, "Get must return only the singleton label='' row")
	require.NoError(t, mock.ExpectationsWereMet(), "Get query must filter on label = ''")
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

func TestOrgCredentialStore_GetAllIntegrations(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sentryID := uuid.New()
	linearID := uuid.New()
	now := time.Now().UTC()
	sentryData := crypto.DevEncrypt([]byte(`{"access_token":"sentry-token","org_slug":"acme"}`))
	linearData := crypto.DevEncrypt([]byte(`{"access_token":"linear-token"}`))

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT .* FROM org_credentials .* org_id = @org_id .* provider = ANY\(@providers\) .* label = '' .* status != 'disabled'`).
		WithArgs(orgID, []string{"sentry", "linear", "notion"}).
		WillReturnRows(pgxmock.NewRows(credColumns).
			AddRow(sentryID, orgID, "sentry", "", sentryData, "active", nil, nil, nil, now, now).
			AddRow(linearID, orgID, "linear", "", linearData, "active", nil, nil, nil, now, now))

	store := NewOrgCredentialStore(mock, nil)
	creds, err := store.GetAllIntegrations(context.Background(), orgID, []models.ProviderName{
		models.ProviderSentry,
		models.ProviderLinear,
		models.ProviderNotion,
	})
	require.NoError(t, err, "GetAllIntegrations should not return an error")
	require.Equal(t, map[models.ProviderName]*models.DecryptedCredential{
		models.ProviderSentry: {
			ID:        sentryID,
			OrgID:     orgID,
			Provider:  models.ProviderSentry,
			Config:    models.SentryConfig{AccessToken: "sentry-token", OrgSlug: "acme"},
			Status:    models.CredentialStatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		},
		models.ProviderLinear: {
			ID:        linearID,
			OrgID:     orgID,
			Provider:  models.ProviderLinear,
			Config:    models.LinearConfig{AccessToken: "linear-token"},
			Status:    models.CredentialStatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, creds, "GetAllIntegrations should return credentials keyed by provider")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestOrgCredentialStore_GetAllIntegrations_EmptyProviders(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	store := NewOrgCredentialStore(mock, nil)
	creds, err := store.GetAllIntegrations(context.Background(), uuid.New(), nil)
	require.NoError(t, err, "GetAllIntegrations should not error when no providers are requested")
	require.Equal(t, map[models.ProviderName]*models.DecryptedCredential{}, creds, "GetAllIntegrations should return an empty credential map")
	require.NoError(t, mock.ExpectationsWereMet(), "empty provider batch should not issue database queries")
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
	require.Equal(t, models.CredentialStatusActive, anthropicSummary.Status, "anthropic should be active")
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

func TestOrgCredentialStore_ListSummaries_FiltersLabelEmpty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	store := NewOrgCredentialStore(mock, nil)

	mock.ExpectQuery(`(?s)SELECT .* FROM org_credentials.*label = ''`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(credColumns))

	summaries, err := store.ListSummaries(context.Background(), uuid.New())
	require.NoError(t, err, "ListSummaries should not return an error")

	var anthropicSummary *models.CredentialSummary
	for i := range summaries {
		if summaries[i].Provider == models.ProviderAnthropic {
			anthropicSummary = &summaries[i]
			break
		}
	}
	require.NotNil(t, anthropicSummary, "summaries should include Anthropic")
	require.False(t, anthropicSummary.Configured, "labeled subscription rows must not make Anthropic API key appear configured")
	require.NoError(t, mock.ExpectationsWereMet(), "ListSummaries query must filter to label = ''")
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

func TestOrgCredentialStore_Disable_FiltersLabelEmpty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	store := NewOrgCredentialStore(mock, nil)

	mock.ExpectExec(`(?s)UPDATE org_credentials.*status = 'disabled'.*label = ''`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Disable(context.Background(), uuid.New(), models.ProviderAnthropic)
	require.NoError(t, err, "Disable should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "Disable query must filter to label = ''")
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

func TestOrgCredentialStore_HasActiveLabeled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupMock  func(mock pgxmock.PgxPoolIface)
		wantExists bool
		expectErr  bool
	}{
		{
			name: "returns true when labeled row exists",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`(?s)SELECT EXISTS.*label != ''.*status = 'active'`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
			},
			wantExists: true,
		},
		{
			name: "returns false when no labeled row exists",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`(?s)SELECT EXISTS.*label != ''.*status = 'active'`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
			},
			wantExists: false,
		},
		{
			name: "db error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`(?s)SELECT EXISTS.*label != ''.*status = 'active'`).
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

			exists, err := store.HasActiveLabeled(context.Background(), uuid.New(), models.ProviderAnthropic)
			if tt.expectErr {
				require.Error(t, err, "HasActiveLabeled should return an error")
				return
			}
			require.NoError(t, err, "HasActiveLabeled should not return an error")
			require.Equal(t, tt.wantExists, exists, "HasActiveLabeled should return expected existence")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestOrgCredentialStore_DisableLabeled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "disables labeled rows",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec(`(?s)UPDATE org_credentials.*status = 'disabled'.*label != ''`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 2))
			},
		},
		{
			name: "db error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec(`(?s)UPDATE org_credentials.*status = 'disabled'.*label != ''`).
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

			err = store.DisableLabeled(context.Background(), uuid.New(), models.ProviderAnthropic)
			if tt.expectErr {
				require.Error(t, err, "DisableLabeled should return an error")
				return
			}
			require.NoError(t, err, "DisableLabeled should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
