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
var codingAuthColumns = []string{"id", "org_id", "provider", "label", "config", "status", "priority", "last_verified_at", "last_used_at", "created_by", "created_at", "updated_at"}

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

func TestOrgCredentialStore_ListCodingAuths(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	codexSub := crypto.DevEncrypt([]byte(`{"access_token":"tok","refresh_token":"ref","expires_at":"2030-01-01T00:00:00Z","account_type":"plus"}`))
	claudeKey := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-test"}`))
	geminiKey := crypto.DevEncrypt([]byte(`{"api_key":"AIza-test","model":"gemini-2.5-pro"}`))

	mock.ExpectQuery(`(?s)SELECT .* FROM org_credentials.*priority`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows(codingAuthColumns).
			AddRow(uuid.New(), orgID, "openai_chatgpt", "Team seat A", codexSub, "active", 1, &now, &now, nil, now, now).
			AddRow(uuid.New(), orgID, "anthropic", "Claude backup", claudeKey, "active", 2, nil, nil, nil, now, now).
			AddRow(uuid.New(), orgID, "gemini", "", geminiKey, "active", 3, nil, nil, nil, now, now))

	store := NewOrgCredentialStore(mock, nil)
	rows, err := store.ListCodingAuths(context.Background(), orgID)
	require.NoError(t, err, "ListCodingAuths should not return an error")
	require.Len(t, rows, 3, "ListCodingAuths should return every coding auth row")
	require.Equal(t, models.AgentTypeCodex, rows[0].Agent, "ListCodingAuths should classify Codex subscription rows")
	require.True(t, rows[0].IsDefault, "ListCodingAuths should mark the first runnable row as default")
	require.Equal(t, models.CodingAuthTypeAPIKey, rows[1].AuthType, "ListCodingAuths should classify API key rows")
	require.Equal(t, models.CodingAuthStatusNeverVerified, rows[1].Status, "ListCodingAuths should derive Never verified when last_verified_at is nil")
	require.Equal(t, models.AgentTypeGeminiCLI, rows[2].Agent, "ListCodingAuths should classify Gemini API key rows")
	require.Equal(t, "Gemini CLI API key", rows[2].Label, "ListCodingAuths should synthesize a default label when none is provided")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestOrgCredentialStore_ReorderCodingAuths(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE org_credentials SET priority = .*updated_at = now\(\) WHERE id = .* AND org_id = .*`).
		WithArgs(1, firstID, orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE org_credentials SET priority = .*updated_at = now\(\) WHERE id = .* AND org_id = .*`).
		WithArgs(2, secondID, orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	store := NewOrgCredentialStore(mock, nil)
	err = store.ReorderCodingAuths(context.Background(), orgID, []uuid.UUID{firstID, secondID})
	require.NoError(t, err, "ReorderCodingAuths should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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

func TestOrgCredentialStore_ClaimNextRoundRobin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns active credential with oldest last_used_at",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				configData := crypto.DevEncrypt([]byte(`{"access_token":"abc","refresh_token":"def","account_id":"acct","id_token":"tok","expires_at":"2030-01-01T00:00:00Z"}`))
				mock.ExpectQuery(`(?s)WITH next AS.*ORDER BY priority, last_used_at NULLS FIRST, created_at.*FOR UPDATE.*UPDATE org_credentials.*RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns).
						AddRow(uuid.New(), uuid.New(), "openai_chatgpt", "work", configData, "active", nil, nil, nil, time.Now(), time.Now()))
			},
		},
		{
			name: "no active credentials",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`(?s)WITH next AS.*ORDER BY priority, last_used_at NULLS FIRST, created_at.*FOR UPDATE.*UPDATE org_credentials.*RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns))
			},
			expectErr: true,
		},
		{
			name: "db error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`(?s)WITH next AS.*ORDER BY priority, last_used_at NULLS FIRST, created_at.*FOR UPDATE.*UPDATE org_credentials.*RETURNING`).
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

			cred, err := store.ClaimNextRoundRobin(context.Background(), uuid.New(), models.ProviderOpenAIChatGPT)
			if tt.expectErr {
				require.Error(t, err, "ClaimNextRoundRobin should return an error")
				return
			}
			require.NoError(t, err, "ClaimNextRoundRobin should not return an error")
			require.NotNil(t, cred, "ClaimNextRoundRobin should return a credential")
			require.Equal(t, models.ProviderOpenAIChatGPT, cred.Provider, "credential should have correct provider")
			require.Equal(t, "active", cred.Status, "returned credential should be active")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestOrgCredentialStore_ClaimNextLabeledRoundRobin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns labeled active credential",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				configData := crypto.DevEncrypt([]byte(`{"subscription":{"access_token":"a","refresh_token":"r","expires_at":"2030-01-01T00:00:00Z"}}`))
				mock.ExpectQuery(`(?s)WITH next AS.*label != ''.*ORDER BY priority, last_used_at NULLS FIRST, created_at.*FOR UPDATE.*UPDATE org_credentials.*RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns).
						AddRow(uuid.New(), uuid.New(), "anthropic", "team-a", configData, "active", nil, nil, nil, time.Now(), time.Now()))
			},
		},
		{
			name: "no labeled active credentials",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`(?s)WITH next AS.*label != ''.*ORDER BY priority, last_used_at NULLS FIRST, created_at.*FOR UPDATE.*UPDATE org_credentials.*RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(credColumns))
			},
			expectErr: true,
		},
		{
			name: "db error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`(?s)WITH next AS.*label != ''.*ORDER BY priority, last_used_at NULLS FIRST, created_at.*FOR UPDATE.*UPDATE org_credentials.*RETURNING`).
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

			cred, err := store.ClaimNextLabeledRoundRobin(context.Background(), uuid.New(), models.ProviderAnthropic)
			if tt.expectErr {
				require.Error(t, err, "ClaimNextLabeledRoundRobin should return an error")
				return
			}
			require.NoError(t, err, "ClaimNextLabeledRoundRobin should not return an error")
			require.NotNil(t, cred, "ClaimNextLabeledRoundRobin should return a credential")
			require.NotEmpty(t, cred.Label, "claimed credential should have a non-empty label")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
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
