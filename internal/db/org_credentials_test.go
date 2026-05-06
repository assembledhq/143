package db

import (
	"context"
	"encoding/json"
	"errors"
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
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(1))
				mock.ExpectQuery("INSERT INTO org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
		},
		{
			name: "upserts openai config",
			cfg:  models.OpenAIConfig{APIKey: "sk-test", APIType: "chat"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(1))
				mock.ExpectQuery("INSERT INTO org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
		},
		{
			name: "db error",
			cfg:  models.AnthropicConfig{APIKey: "sk-ant-test"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(1))
				mock.ExpectQuery("INSERT INTO org_credentials").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
				mock.ExpectRollback()
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

func TestOrgCredentialStore_InsertPendingAuth(t *testing.T) {
	t.Parallel()

	t.Run("assigns next priority when inserting a fresh pending auth", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		newID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(3))
		mock.ExpectQuery(`INSERT INTO org_credentials`).
			WithArgs(orgID, "openai_chatgpt", "team-a", pgxmock.AnyArg(), pgxmock.AnyArg(), 3).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(newID))
		mock.ExpectCommit()

		store := NewOrgCredentialStore(mock, nil)
		id, err := store.InsertPendingAuth(context.Background(), orgID, nil, "team-a", models.OpenAIChatGPTConfig{DeviceAuthID: "dev-1"})
		require.NoError(t, err, "InsertPendingAuth should not return an error")
		require.NotNil(t, id, "InsertPendingAuth should return the new row id")
		require.Equal(t, newID, *id, "InsertPendingAuth should return the inserted id")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("bumps priority when resurrecting a disabled row", func(t *testing.T) {
		t.Parallel()

		// The ON CONFLICT clause includes a CASE that uses EXCLUDED.priority
		// when the existing row's status is 'disabled'. This test verifies the
		// SQL still passes the new priority through so the row appears at the
		// bottom of the stack on resurrection rather than reusing a stale slot.
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		existingID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(5))
		mock.ExpectQuery(`(?s)INSERT INTO org_credentials.*EXCLUDED\.priority`).
			WithArgs(orgID, "openai_chatgpt", "team-a", pgxmock.AnyArg(), pgxmock.AnyArg(), 5).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(existingID))
		mock.ExpectCommit()

		store := NewOrgCredentialStore(mock, nil)
		id, err := store.InsertPendingAuth(context.Background(), orgID, nil, "team-a", models.OpenAIChatGPTConfig{DeviceAuthID: "dev-2"})
		require.NoError(t, err, "InsertPendingAuth should not return an error on resurrection")
		require.NotNil(t, id, "InsertPendingAuth should return the resurrected row id")
		require.Equal(t, existingID, *id, "InsertPendingAuth should return the existing id on resurrection")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns ErrCredentialLabelTaken when label conflicts with active row", func(t *testing.T) {
		t.Parallel()

		// When the existing row is 'active' or 'invalid', the ON CONFLICT
		// WHERE clause filters it out, so RETURNING produces zero rows
		// (pgx.ErrNoRows). The handler then looks up the existing status to
		// surface a typed *ErrCredentialLabelTaken so the API can render a
		// status-aware message.
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(2))
		mock.ExpectQuery(`INSERT INTO org_credentials`).
			WithArgs(orgID, "openai_chatgpt", "team-a", pgxmock.AnyArg(), pgxmock.AnyArg(), 2).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT status FROM org_credentials`).
			WithArgs(orgID, "openai_chatgpt", "team-a").
			WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		id, err := store.InsertPendingAuth(context.Background(), orgID, nil, "team-a", models.OpenAIChatGPTConfig{DeviceAuthID: "dev-3"})
		require.Nil(t, id, "InsertPendingAuth should not return an id when the label is taken")
		require.Error(t, err, "InsertPendingAuth should return an error when the label is taken")
		var taken *ErrCredentialLabelTaken
		require.ErrorAs(t, err, &taken, "InsertPendingAuth should return ErrCredentialLabelTaken")
		require.Equal(t, "team-a", taken.Label, "ErrCredentialLabelTaken should carry the label")
		require.Equal(t, "active", taken.ExistingStatus, "ErrCredentialLabelTaken should carry the actual existing status")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns ErrCredentialLabelTaken with unknown status when the lookup fails", func(t *testing.T) {
		t.Parallel()

		// If the status lookup itself fails (e.g. the row vanished between
		// the failed insert and the SELECT), surface a generic
		// ExistingStatus="unknown" rather than swallow the conflict.
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(2))
		mock.ExpectQuery(`INSERT INTO org_credentials`).
			WithArgs(orgID, "openai_chatgpt", "team-a", pgxmock.AnyArg(), pgxmock.AnyArg(), 2).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT status FROM org_credentials`).
			WithArgs(orgID, "openai_chatgpt", "team-a").
			WillReturnError(errors.New("connection lost"))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		_, err = store.InsertPendingAuth(context.Background(), orgID, nil, "team-a", models.OpenAIChatGPTConfig{DeviceAuthID: "dev-4"})
		require.Error(t, err, "InsertPendingAuth should return an error when status lookup fails")
		var taken *ErrCredentialLabelTaken
		require.ErrorAs(t, err, &taken, "InsertPendingAuth should still return ErrCredentialLabelTaken")
		require.Equal(t, "unknown", taken.ExistingStatus, "ErrCredentialLabelTaken should fall back to 'unknown' status")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps non-conflict insert errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(2))
		mock.ExpectQuery(`INSERT INTO org_credentials`).
			WithArgs(orgID, "openai_chatgpt", "team-a", pgxmock.AnyArg(), pgxmock.AnyArg(), 2).
			WillReturnError(errors.New("disk full"))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		_, err = store.InsertPendingAuth(context.Background(), orgID, nil, "team-a", models.OpenAIChatGPTConfig{DeviceAuthID: "dev-5"})
		require.Error(t, err, "InsertPendingAuth should surface non-conflict insert errors")
		require.NotErrorIs(t, err, &ErrCredentialLabelTaken{}, "non-conflict errors should not be reported as label-taken")
		require.Contains(t, err.Error(), "insert pending", "InsertPendingAuth should wrap the insert failure")
	})
}

// TestOrgCredentialStore_WithCodingAuthPriorityErrors covers the error paths
// in the priority-locking helper: missing transaction support, advisory lock
// failure, and priority-query failure all bubble up so the caller can
// distinguish them from the surrounding work.
func TestOrgCredentialStore_WithCodingAuthPriorityErrors(t *testing.T) {
	t.Parallel()

	t.Run("rejects non-transactional stores", func(t *testing.T) {
		t.Parallel()

		// Plain DBTX without Begin satisfies the store but not TxStarter.
		// Use a direct UpsertWithLabel call for a coding provider so the
		// helper is exercised. noTxDB is defined in session_store_test.go.
		store := NewOrgCredentialStore(noTxDB{}, nil)
		_, err := store.UpsertWithLabel(context.Background(), uuid.New(), nil, "team-a", models.AnthropicConfig{APIKey: "sk-ant"})
		require.Error(t, err, "UpsertWithLabel should reject non-transactional stores for coding providers")
		require.Contains(t, err.Error(), "does not support transactions")
	})

	t.Run("surfaces Begin failures", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectBegin().WillReturnError(errors.New("connection reset"))

		store := NewOrgCredentialStore(mock, nil)
		_, err = store.UpsertWithLabel(context.Background(), uuid.New(), nil, "team-a", models.AnthropicConfig{APIKey: "sk-ant"})
		require.Error(t, err, "UpsertWithLabel should surface Begin failures")
		require.Contains(t, err.Error(), "begin priority transaction")
	})
}

// TestOrgCredentialStore_CreateCodingAuthErrors covers the failure branches
// inside the CreateCodingAuth INSERT closure so the diff-coverage gate sees
// these lines exercised.
func TestOrgCredentialStore_CreateCodingAuthErrors(t *testing.T) {
	t.Parallel()

	t.Run("wraps insert query failures", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"next_priority"}).AddRow(1))
		mock.ExpectQuery(`INSERT INTO org_credentials`).
			WithArgs(orgID, "openai", "Codex backup", pgxmock.AnyArg(), 1, &userID).
			WillReturnError(errors.New("constraint violation"))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		_, err = store.CreateCodingAuth(context.Background(), orgID, &userID, models.CreateCodingAuthInput{
			Agent:    models.AgentTypeCodex,
			AuthType: models.CodingAuthTypeAPIKey,
			Label:    "Codex backup",
			APIKey:   "sk-test",
		})
		require.Error(t, err, "CreateCodingAuth should surface query failures")
		require.Contains(t, err.Error(), "create coding auth")
	})

	t.Run("wraps row collection failures", func(t *testing.T) {
		t.Parallel()

		// Returning zero rows from RETURNING tricks pgx.CollectOneRow into
		// returning ErrNoRows, exercising the cerr branch.
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"next_priority"}).AddRow(1))
		mock.ExpectQuery(`INSERT INTO org_credentials`).
			WithArgs(orgID, "openai", "Codex backup", pgxmock.AnyArg(), 1, &userID).
			WillReturnRows(pgxmock.NewRows(codingAuthColumns))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		_, err = store.CreateCodingAuth(context.Background(), orgID, &userID, models.CreateCodingAuthInput{
			Agent:    models.AgentTypeCodex,
			AuthType: models.CodingAuthTypeAPIKey,
			Label:    "Codex backup",
			APIKey:   "sk-test",
		})
		require.Error(t, err, "CreateCodingAuth should surface CollectOneRow failures")
		require.Contains(t, err.Error(), "create coding auth")
	})

	t.Run("surfaces advisory lock failures", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("lock timeout"))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		_, err = store.UpsertWithLabel(context.Background(), uuid.New(), nil, "team-a", models.AnthropicConfig{APIKey: "sk-ant"})
		require.Error(t, err, "UpsertWithLabel should surface advisory lock failures")
		require.Contains(t, err.Error(), "acquire priority lock")
	})

	t.Run("surfaces priority query failures", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("planner exploded"))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		_, err = store.UpsertWithLabel(context.Background(), uuid.New(), nil, "team-a", models.AnthropicConfig{APIKey: "sk-ant"})
		require.Error(t, err, "UpsertWithLabel should surface priority query failures")
		require.Contains(t, err.Error(), "get next coding auth priority")
	})
}

// TestOrgCredentialStore_UpsertWithLabel_NonCodingProvider verifies the
// priority gating: priority is meaningful only for the coding-agent fallback
// stack, so non-coding providers (GitHub/Sentry/Linear/Notion) must skip the
// MAX(priority) lookup entirely. The mock fails loudly if an unexpected
// SELECT fires.
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
	ampKey := crypto.DevEncrypt([]byte(`{"api_key":"sgamp_test_token"}`))
	piKey := crypto.DevEncrypt([]byte(`{"api_key":"pi-provider-key"}`))

	mock.ExpectQuery(`(?s)SELECT .* FROM org_credentials.*priority`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows(codingAuthColumns).
			AddRow(uuid.New(), orgID, "openai_chatgpt", "Team seat A", codexSub, "active", 1, &now, &now, nil, now, now).
			AddRow(uuid.New(), orgID, "anthropic", "Claude backup", claudeKey, "active", 2, nil, nil, nil, now, now).
			AddRow(uuid.New(), orgID, "gemini", "", geminiKey, "active", 3, nil, nil, nil, now, now).
			AddRow(uuid.New(), orgID, "amp", "", ampKey, "active", 4, nil, nil, nil, now, now).
			AddRow(uuid.New(), orgID, "pi", "", piKey, "active", 5, nil, nil, nil, now, now))

	store := NewOrgCredentialStore(mock, nil)
	rows, err := store.ListCodingAuths(context.Background(), orgID)
	require.NoError(t, err, "ListCodingAuths should not return an error")
	require.Len(t, rows, 5, "ListCodingAuths should return every coding auth row")
	require.Equal(t, models.AgentTypeCodex, rows[0].Agent, "ListCodingAuths should classify Codex subscription rows")
	require.True(t, rows[0].IsDefault, "ListCodingAuths should mark the first runnable row as default")
	require.Equal(t, models.CodingAuthTypeAPIKey, rows[1].AuthType, "ListCodingAuths should classify API key rows")
	require.Equal(t, models.CodingAuthStatusHealthy, rows[1].Status, "ListCodingAuths should treat active rows as healthy regardless of last_verified_at")
	require.Equal(t, models.AgentTypeGeminiCLI, rows[2].Agent, "ListCodingAuths should classify Gemini API key rows")
	require.Equal(t, "Gemini CLI API key", rows[2].Label, "ListCodingAuths should synthesize a default label when none is provided")
	require.Equal(t, models.AgentTypeAmp, rows[3].Agent, "ListCodingAuths should classify Amp API key rows")
	require.Equal(t, "Amp API key", rows[3].Label, "ListCodingAuths should synthesize an Amp label when none is provided")
	require.Equal(t, models.AgentTypePi, rows[4].Agent, "ListCodingAuths should classify Pi API key rows")
	require.Equal(t, "Pi API key", rows[4].Label, "ListCodingAuths should synthesize a Pi label when none is provided")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestOrgCredentialStore_ListCodingAuths_ErrorAndFilteringCases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now().UTC()

	t.Run("surfaces query errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectQuery(`(?s)SELECT .* FROM org_credentials.*priority`).
			WithArgs(orgID).
			WillReturnError(fmt.Errorf("connection refused"))

		store := NewOrgCredentialStore(mock, nil)
		rows, err := store.ListCodingAuths(context.Background(), orgID)
		require.Error(t, err, "ListCodingAuths should return query failures")
		require.Nil(t, rows, "ListCodingAuths should not return rows on query failure")
	})

	t.Run("filters unsupported rows and assigns first runnable default", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		openRouterKey := crypto.DevEncrypt([]byte(`{"api_key":"sk-or-test"}`))
		invalidClaude := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-test"}`))
		geminiKey := crypto.DevEncrypt([]byte(`{"api_key":"AIza-test"}`))

		mock.ExpectQuery(`(?s)SELECT .* FROM org_credentials.*priority`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows(codingAuthColumns).
				AddRow(uuid.New(), orgID, "openrouter", "Unsupported", openRouterKey, "active", 1, nil, nil, nil, now, now).
				AddRow(uuid.New(), orgID, "anthropic", "Needs reauth", invalidClaude, "invalid", 2, &now, nil, nil, now, now).
				AddRow(uuid.New(), orgID, "gemini", "Gemini healthy", geminiKey, "active", 3, &now, nil, nil, now, now))

		store := NewOrgCredentialStore(mock, nil)
		rows, err := store.ListCodingAuths(context.Background(), orgID)
		require.NoError(t, err, "ListCodingAuths should not return an error")
		require.Len(t, rows, 2, "ListCodingAuths should filter out unsupported provider rows")
		require.False(t, rows[0].IsDefault, "ListCodingAuths should not mark non-runnable rows as default")
		require.True(t, rows[1].IsDefault, "ListCodingAuths should mark the first runnable row as default")
	})
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

func TestOrgCredentialStore_ReorderCodingAuths_ErrorCases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	rowID := uuid.New()

	t.Run("returns error when db does not support transactions", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		store := NewOrgCredentialStore(mock, nil)
		err = store.ReorderCodingAuths(context.Background(), orgID, []uuid.UUID{rowID})
		require.Error(t, err, "ReorderCodingAuths should reject non-transactional stores")
	})

	t.Run("rolls back when an updated row is missing", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE org_credentials SET priority = .*updated_at = now\(\) WHERE id = .* AND org_id = .*`).
			WithArgs(1, rowID, orgID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		mock.ExpectRollback()

		store := NewOrgCredentialStore(mock, nil)
		err = store.ReorderCodingAuths(context.Background(), orgID, []uuid.UUID{rowID})
		require.Error(t, err, "ReorderCodingAuths should fail when a row is not found")
		require.Contains(t, err.Error(), "not found", "ReorderCodingAuths should explain missing rows")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
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

func TestOrgCredentialStore_CodingAuthCRUDAndHelpers(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	rowID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()

	t.Run("gets a credential by id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		configData := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-test"}`))
		mock.ExpectQuery(`SELECT .* FROM org_credentials`).
			WithArgs(rowID, orgID).
			WillReturnRows(pgxmock.NewRows(credColumns).
				AddRow(rowID, orgID, "anthropic", "", configData, "active", nil, nil, nil, now, now))

		store := NewOrgCredentialStore(mock, nil)
		cred, err := store.GetByID(context.Background(), orgID, rowID)
		require.NoError(t, err, "GetByID should not return an error")
		require.Equal(t, rowID, cred.ID, "GetByID should return the requested row")
	})

	t.Run("gets a credential by provider and label", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		configData := crypto.DevEncrypt([]byte(`{"subscription":{"access_token":"a","refresh_token":"r","expires_at":"2030-01-01T00:00:00Z"}}`))
		mock.ExpectQuery(`SELECT .* FROM org_credentials`).
			WithArgs(orgID, "anthropic", "team-a").
			WillReturnRows(pgxmock.NewRows(credColumns).
				AddRow(rowID, orgID, "anthropic", "team-a", configData, "active", nil, nil, nil, now, now))

		store := NewOrgCredentialStore(mock, nil)
		cred, err := store.GetByProviderAndLabel(context.Background(), orgID, models.ProviderAnthropic, "team-a")
		require.NoError(t, err, "GetByProviderAndLabel should not return an error")
		require.Equal(t, "team-a", cred.Label, "GetByProviderAndLabel should return the requested label")
	})

	t.Run("lists provider rows in priority order", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		first := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-first"}`))
		second := crypto.DevEncrypt([]byte(`{"api_key":"sk-ant-second"}`))
		mock.ExpectQuery(`(?s)SELECT .* FROM org_credentials.*ORDER BY priority, created_at`).
			WithArgs(orgID, "anthropic").
			WillReturnRows(pgxmock.NewRows(codingAuthColumns).
				AddRow(uuid.New(), orgID, "anthropic", "first", first, "active", 1, nil, nil, nil, now, now).
				AddRow(uuid.New(), orgID, "anthropic", "second", second, "active", 2, nil, nil, nil, now, now))

		store := NewOrgCredentialStore(mock, nil)
		creds, err := store.ListByProvider(context.Background(), orgID, models.ProviderAnthropic)
		require.NoError(t, err, "ListByProvider should not return an error")
		require.Len(t, creds, 2, "ListByProvider should return every active provider row")
		require.Equal(t, "first", creds[0].Label, "ListByProvider should preserve priority ordering")
	})

	t.Run("creates a coding auth", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		configData := crypto.DevEncrypt([]byte(`{"api_key":"sk-test-123","api_type":"responses"}`))
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery(`(?s)SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).
			WithArgs(orgID).
			WillReturnRows(pgxmock.NewRows([]string{"next_priority"}).AddRow(4))
		mock.ExpectQuery(`INSERT INTO org_credentials`).
			WithArgs(orgID, "openai", "Codex backup", pgxmock.AnyArg(), 4, &userID).
			WillReturnRows(pgxmock.NewRows(codingAuthColumns).
				AddRow(rowID, orgID, "openai", "Codex backup", configData, "active", 4, nil, nil, &userID, now, now))
		mock.ExpectCommit()

		store := NewOrgCredentialStore(mock, nil)
		row, err := store.CreateCodingAuth(context.Background(), orgID, &userID, models.CreateCodingAuthInput{
			Agent:    models.AgentTypeCodex,
			AuthType: models.CodingAuthTypeAPIKey,
			Label:    "Codex backup",
			APIKey:   "sk-test-123",
		})
		require.NoError(t, err, "CreateCodingAuth should not return an error")
		require.Equal(t, models.AgentTypeCodex, row.Agent, "CreateCodingAuth should classify the created row")
		require.Equal(t, 4, row.Priority, "CreateCodingAuth should append at the end of the stack")
	})

	t.Run("updates a coding auth label", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		configData := crypto.DevEncrypt([]byte(`{"api_key":"AIza-test","model":"gemini-2.5-pro"}`))
		label := "Renamed"
		mock.ExpectQuery(`(?s)UPDATE org_credentials.*SET label = .*RETURNING`).
			WithArgs(label, rowID, orgID).
			WillReturnRows(pgxmock.NewRows(codingAuthColumns).
				AddRow(rowID, orgID, "gemini", label, configData, "active", 2, nil, nil, nil, now, now))

		store := NewOrgCredentialStore(mock, nil)
		row, err := store.UpdateCodingAuth(context.Background(), orgID, rowID, models.UpdateCodingAuthInput{Label: &label})
		require.NoError(t, err, "UpdateCodingAuth should not return an error")
		require.Equal(t, label, row.Label, "UpdateCodingAuth should return the updated label")
	})

	t.Run("rejects empty update payloads", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		store := NewOrgCredentialStore(mock, nil)
		row, err := store.UpdateCodingAuth(context.Background(), orgID, rowID, models.UpdateCodingAuthInput{})
		require.Error(t, err, "UpdateCodingAuth should reject empty payloads")
		require.Nil(t, row, "UpdateCodingAuth should not return a row for empty payloads")
	})

	t.Run("disables coding auth by id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectExec(`UPDATE org_credentials SET status = 'disabled', updated_at = now\(\) WHERE id = .* AND org_id = .*`).
			WithArgs(rowID, orgID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		store := NewOrgCredentialStore(mock, nil)
		err = store.DisableCodingAuth(context.Background(), orgID, rowID)
		require.NoError(t, err, "DisableCodingAuth should not return an error")
	})

	t.Run("deletes coding auth by id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectExec(`DELETE FROM org_credentials WHERE id = .* AND org_id = .*`).
			WithArgs(rowID, orgID).
			WillReturnResult(pgxmock.NewResult("DELETE", 1))

		store := NewOrgCredentialStore(mock, nil)
		err = store.DeleteCodingAuth(context.Background(), orgID, rowID)
		require.NoError(t, err, "DeleteCodingAuth should not return an error")
	})

	t.Run("surfaces delete coding auth errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectExec(`DELETE FROM org_credentials WHERE id = .* AND org_id = .*`).
			WithArgs(rowID, orgID).
			WillReturnError(errors.New("delete failed"))

		store := NewOrgCredentialStore(mock, nil)
		err = store.DeleteCodingAuth(context.Background(), orgID, rowID)
		require.Error(t, err, "DeleteCodingAuth should return database errors")
		require.Contains(t, err.Error(), "delete coding auth", "DeleteCodingAuth should wrap database failures")
	})

	t.Run("disables row by id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectExec(`UPDATE org_credentials SET status = 'disabled', updated_at = now\(\) WHERE id = .* AND org_id = .*`).
			WithArgs(rowID, orgID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		store := NewOrgCredentialStore(mock, nil)
		err = store.DisableByID(context.Background(), orgID, rowID)
		require.NoError(t, err, "DisableByID should not return an error")
	})

	t.Run("checks provider ownership by id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectQuery(`SELECT EXISTS`).
			WithArgs(rowID, orgID, "anthropic").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

		store := NewOrgCredentialStore(mock, nil)
		exists, err := store.ExistsForProviderByID(context.Background(), orgID, rowID, models.ProviderAnthropic)
		require.NoError(t, err, "ExistsForProviderByID should not return an error")
		require.True(t, exists, "ExistsForProviderByID should report matching ownership")
	})

	t.Run("updates status by id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "creating mock pool should not error")
		defer mock.Close()

		mock.ExpectExec(`UPDATE org_credentials SET status = .*last_verified_at = now\(\), updated_at = now\(\) WHERE id = .* AND org_id = .*`).
			WithArgs("invalid", rowID, orgID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		store := NewOrgCredentialStore(mock, nil)
		err = store.UpdateStatusByID(context.Background(), orgID, rowID, "invalid")
		require.NoError(t, err, "UpdateStatusByID should not return an error")
	})

	t.Run("maps coding auth helper functions", func(t *testing.T) {
		t.Parallel()

		verified := now
		cred := models.DecryptedCredential{
			ID:             rowID,
			OrgID:          orgID,
			Provider:       models.ProviderOpenAIChatGPT,
			Priority:       1,
			Config:         models.OpenAIChatGPTConfig{AccessToken: "tok", RefreshToken: "ref", AccountType: "plus"},
			Status:         "active",
			LastVerifiedAt: &verified,
			CreatedAt:      now,
			UpdatedAt:      now,
		}

		summary, ok := buildCodingAuthSummary(cred)
		require.True(t, ok, "buildCodingAuthSummary should support Codex subscription rows")
		require.Equal(t, models.AgentTypeCodex, summary.Agent, "buildCodingAuthSummary should infer the agent from provider")
		require.Equal(t, models.CodingAuthTypeSubscription, summary.AuthType, "buildCodingAuthSummary should infer subscription auth type")
		require.Equal(t, "Codex subscription", summary.Label, "buildCodingAuthSummary should synthesize a fallback label")
		require.Equal(t, "plus", summary.UsageNote, "buildCodingAuthSummary should surface subscription account type")
		require.Equal(t, models.CodingAuthStatusHealthy, summary.Status, "buildCodingAuthSummary should map active rows to healthy")

		require.Equal(t, models.CodingAuthStatusInvalid, inferCodingAuthStatus(models.DecryptedCredential{Status: "invalid"}), "inferCodingAuthStatus should map invalid rows")
		require.Equal(t, models.CodingAuthStatusNeedsReauth, inferCodingAuthStatus(models.DecryptedCredential{Status: "pending_auth"}), "inferCodingAuthStatus should map pending auth rows")
		require.Equal(t, models.CodingAuthStatusHealthy, inferCodingAuthStatus(models.DecryptedCredential{Status: "active"}), "inferCodingAuthStatus should map active rows to healthy even without last_verified_at")
		require.Equal(t, models.CodingAuthStatusNeedsReauth, inferCodingAuthStatus(models.DecryptedCredential{Status: "other"}), "inferCodingAuthStatus should map unknown rows to needs reauth")
		require.Equal(t, models.AgentType(""), inferCodingAuthAgent(models.DecryptedCredential{Provider: models.ProviderOpenRouter}), "inferCodingAuthAgent should reject unsupported providers")
		require.Equal(t, models.CodingAuthType(""), inferCodingAuthType(models.DecryptedCredential{Config: models.OpenRouterConfig{APIKey: "sk-or"}}), "inferCodingAuthType should reject unsupported provider configs")
		require.Equal(t, "Claude Code subscription", fallbackLabel(models.AgentTypeClaudeCode, models.CodingAuthTypeSubscription), "fallbackLabel should synthesize Claude subscription labels")
		require.Equal(t, "Claude Code API key", fallbackLabel(models.AgentTypeClaudeCode, models.CodingAuthTypeAPIKey), "fallbackLabel should synthesize Claude API key labels")
		require.Equal(t, "Gemini CLI API key", fallbackLabel(models.AgentTypeGeminiCLI, models.CodingAuthTypeAPIKey), "fallbackLabel should synthesize Gemini labels")
		require.Equal(t, "Amp API key", fallbackLabel(models.AgentTypeAmp, models.CodingAuthTypeAPIKey), "fallbackLabel should synthesize Amp labels")
		require.Equal(t, "Pi API key", fallbackLabel(models.AgentTypePi, models.CodingAuthTypeAPIKey), "fallbackLabel should synthesize Pi labels")
		require.Equal(t, "fallback", defaultString("", "fallback"), "defaultString should return the fallback when empty")
		require.Equal(t, "value", defaultString("value", "fallback"), "defaultString should preserve non-empty values")
	})

	t.Run("maps create input to provider configs", func(t *testing.T) {
		t.Parallel()

		cfg, provider, err := providerConfigForCodingAuthInput(models.CreateCodingAuthInput{
			Agent:    models.AgentTypeCodex,
			AuthType: models.CodingAuthTypeAPIKey,
			APIKey:   "sk-openai",
		})
		require.NoError(t, err, "providerConfigForCodingAuthInput should support Codex")
		require.Equal(t, models.ProviderOpenAI, provider, "providerConfigForCodingAuthInput should map Codex to OpenAI")
		require.Equal(t, "responses", cfg.(models.OpenAIConfig).APIType, "providerConfigForCodingAuthInput should default Codex API type to responses")

		cfg, provider, err = providerConfigForCodingAuthInput(models.CreateCodingAuthInput{
			Agent:    models.AgentTypeClaudeCode,
			AuthType: models.CodingAuthTypeAPIKey,
			APIKey:   "sk-ant",
			BaseURL:  "https://anthropic.example",
		})
		require.NoError(t, err, "providerConfigForCodingAuthInput should support Claude Code")
		require.Equal(t, models.ProviderAnthropic, provider, "providerConfigForCodingAuthInput should map Claude Code to Anthropic")
		require.Equal(t, "https://anthropic.example", cfg.(models.AnthropicConfig).BaseURL, "providerConfigForCodingAuthInput should preserve Claude base URLs")

		cfg, provider, err = providerConfigForCodingAuthInput(models.CreateCodingAuthInput{
			Agent:    models.AgentTypeGeminiCLI,
			AuthType: models.CodingAuthTypeAPIKey,
			APIKey:   "AIza-test",
		})
		require.NoError(t, err, "providerConfigForCodingAuthInput should support Gemini")
		require.Equal(t, models.ProviderGemini, provider, "providerConfigForCodingAuthInput should map Gemini to Gemini provider")
		require.Equal(t, models.GeminiCLIModelGemini25Pro, cfg.(models.GeminiConfig).Model, "providerConfigForCodingAuthInput should default Gemini models")

		cfg, provider, err = providerConfigForCodingAuthInput(models.CreateCodingAuthInput{
			Agent:    models.AgentTypeAmp,
			AuthType: models.CodingAuthTypeAPIKey,
			APIKey:   "sgamp_test_token",
		})
		require.NoError(t, err, "providerConfigForCodingAuthInput should support Amp")
		require.Equal(t, models.ProviderAmp, provider, "providerConfigForCodingAuthInput should map Amp to the Amp provider")
		require.Equal(t, "sgamp_test_token", cfg.(models.AmpConfig).APIKey, "providerConfigForCodingAuthInput should preserve the Amp API key")

		cfg, provider, err = providerConfigForCodingAuthInput(models.CreateCodingAuthInput{
			Agent:    models.AgentTypePi,
			AuthType: models.CodingAuthTypeAPIKey,
			APIKey:   "pi-provider-key",
		})
		require.NoError(t, err, "providerConfigForCodingAuthInput should support Pi")
		require.Equal(t, models.ProviderPi, provider, "providerConfigForCodingAuthInput should map Pi to the Pi provider")
		require.Equal(t, "pi-provider-key", cfg.(models.PiConfig).APIKey, "providerConfigForCodingAuthInput should preserve the Pi API key")
	})
}
