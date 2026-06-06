package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndHashAPIToken(t *testing.T) {
	t.Parallel()

	token, err := GenerateAPIToken()
	require.NoError(t, err, "GenerateAPIToken should create a token")
	require.Contains(t, token, "143_sk_", "GenerateAPIToken should use the public API token prefix")

	hash := HashAPIToken(token)
	require.Contains(t, hash, "sha256:", "HashAPIToken should identify the hash algorithm")
	require.Equal(t, hash, HashAPIToken(token), "HashAPIToken should be deterministic")
}

func TestAPITokenPrefix(t *testing.T) {
	t.Parallel()

	require.Equal(t, "143_sk_abcd", APITokenPrefix("143_sk_abcdefghijklmnopqrstuvwxyz"), "APITokenPrefix should expose only the short public key id")
	require.Equal(t, "short", APITokenPrefix("short"), "APITokenPrefix should tolerate malformed short tokens")
}

func TestAPIClientStore_List(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	clientID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`SELECT .* FROM api_clients WHERE org_id = @org_id`).
		WithArgs(pgx.NamedArgs{"org_id": orgID}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "name", "description", "status", "created_by_user_id", "disabled_by_user_id", "disabled_at", "created_at", "updated_at",
		}).AddRow(clientID, orgID, "production-ci", nil, models.APIClientStatusEnabled, &userID, nil, nil, now, now))

	clients, err := NewAPIClientStore(mock).List(context.Background(), orgID)
	require.NoError(t, err, "List should query API clients by org")
	require.Equal(t, []models.APIClient{{
		ID:              clientID,
		OrgID:           orgID,
		Name:            "production-ci",
		Status:          models.APIClientStatusEnabled,
		CreatedByUserID: &userID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}}, clients, "List should return API clients scoped to the org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAPITokenStore_GetByToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	clientID := uuid.New()
	tokenID := uuid.New()
	userID := uuid.New()
	raw := "143_sk_testtoken"
	hash := HashAPIToken(raw)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`UPDATE api_tokens .* FROM api_clients`).
		WithArgs(pgx.NamedArgs{"token_hash": hash, "last_used_ip": "203.0.113.10", "last_used_user_agent": "agent"}).
		WillReturnRows(pgxmock.NewRows([]string{
			"token_id", "org_id", "api_client_id", "token_name", "token_hash", "token_prefix", "scopes", "repository_ids", "expires_at", "revoked_at", "created_by_user_id", "client_status", "client_name",
		}).AddRow(tokenID, orgID, clientID, "prod", hash, "143_sk_test", []string{"sessions:create"}, []uuid.UUID{}, nil, nil, &userID, models.APIClientStatusEnabled, "production-ci"))

	resolved, err := NewAPITokenStore(mock).GetByToken(context.Background(), raw, "203.0.113.10", "agent")
	require.NoError(t, err, "GetByToken should resolve an active token")
	require.Equal(t, tokenID, resolved.Token.ID, "GetByToken should return token metadata")
	require.Equal(t, &userID, resolved.Token.CreatedByUserID, "GetByToken should return token creator for API-owned preview records")
	require.Equal(t, clientID, resolved.Client.ID, "GetByToken should return client metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
