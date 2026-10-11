package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func apiClientLifecycleRequest(org, user uuid.UUID, client, token, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), org)
	// Deliberately use a different user.OrgID: tenant scope comes from middleware.
	if user != uuid.Nil {
		ctx = middleware.WithUser(ctx, &models.User{ID: user, OrgID: uuid.New()})
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", client)
	route.URLParams.Add("token_id", token)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
}

func apiClientLifecycleRows(client models.APIClient) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "org_id", "name", "description", "status", "created_by_user_id", "disabled_by_user_id", "disabled_at", "created_at", "updated_at"}).AddRow(client.ID, client.OrgID, client.Name, client.Description, client.Status, client.CreatedByUserID, client.DisabledByUserID, client.DisabledAt, client.CreatedAt, client.UpdatedAt)
}

func apiTokenLifecycleRows(tokens ...models.APIToken) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "api_client_id", "name", "token_hash", "token_prefix", "scopes", "repository_ids", "allowed_ip_cidrs", "expires_at", "last_used_at", "last_used_ip", "last_used_user_agent", "revoked_by_user_id", "revoked_at", "created_by_user_id", "created_at"})
	for _, token := range tokens {
		rows.AddRow(token.ID, token.OrgID, token.APIClientID, token.Name, token.TokenHash, token.TokenPrefix, token.Scopes, token.RepositoryIDs, token.AllowedIPCidrs, token.ExpiresAt, token.LastUsedAt, token.LastUsedIP, token.LastUsedUserAgent, token.RevokedByUserID, token.RevokedAt, token.CreatedByUserID, token.CreatedAt)
	}
	return rows
}

func TestAPIClientHandlerReadLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, endpoint, code string
		empty, invalid       bool
		queryErr             error
		status               int
	}{
		{name: "list clients", endpoint: "list", status: 200},
		{name: "empty clients array", endpoint: "list", empty: true, status: 200},
		{name: "list clients failure", endpoint: "list", queryErr: errors.New("private DB failure"), status: 500, code: "LIST_FAILED"},
		{name: "get client", endpoint: "get", status: 200},
		{name: "get foreign client", endpoint: "get", queryErr: pgx.ErrNoRows, status: 404, code: "NOT_FOUND"},
		{name: "get client failure", endpoint: "get", queryErr: errors.New("private DB failure"), status: 500, code: "GET_FAILED"},
		{name: "get invalid ID", endpoint: "get", invalid: true, status: 400, code: "INVALID_ID"},
		{name: "list tokens", endpoint: "tokens", status: 200},
		{name: "empty tokens array", endpoint: "tokens", empty: true, status: 200},
		{name: "list tokens failure", endpoint: "tokens", queryErr: errors.New("private DB failure"), status: 500, code: "LIST_FAILED"},
		{name: "tokens invalid client ID", endpoint: "tokens", invalid: true, status: 400, code: "INVALID_ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, clientID := uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated store mock")
			defer mock.Close()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			client := models.APIClient{ID: clientID, OrgID: org, Name: "CI", Status: models.APIClientStatusEnabled, CreatedAt: now, UpdatedAt: now}
			token := models.APIToken{ID: uuid.New(), OrgID: org, APIClientID: clientID, Name: "read-only", TokenHash: "sha256:private", TokenPrefix: "143_sk_test", Scopes: []string{"sessions:read"}, RepositoryIDs: []uuid.UUID{}, AllowedIPCidrs: []string{}, CreatedAt: now}
			if !tt.invalid {
				query := `SELECT .* FROM api_clients WHERE org_id = @org_id ORDER BY`
				args := pgx.NamedArgs{"org_id": org}
				rows := apiClientLifecycleRows(client)
				if tt.endpoint == "get" {
					query = `SELECT .* FROM api_clients WHERE org_id = @org_id AND id = @id`
					args["id"] = clientID
				}
				if tt.endpoint == "tokens" {
					query = `SELECT .* FROM api_tokens WHERE org_id = @org_id AND api_client_id = @api_client_id AND revoked_at IS NULL`
					args["api_client_id"] = clientID
					rows = apiTokenLifecycleRows(token)
				}
				if tt.empty {
					if tt.endpoint == "tokens" {
						rows = apiTokenLifecycleRows()
					} else {
						rows = pgxmock.NewRows([]string{"id", "org_id", "name", "description", "status", "created_by_user_id", "disabled_by_user_id", "disabled_at", "created_at", "updated_at"})
					}
				}
				expectation := mock.ExpectQuery(query).WithArgs(args)
				if tt.queryErr != nil {
					expectation.WillReturnError(tt.queryErr)
				} else {
					expectation.WillReturnRows(rows)
				}
			}
			h := NewAPIClientHandler(db.NewAPIClientStore(mock), db.NewAPITokenStore(mock))
			id := clientID.String()
			if tt.invalid {
				id = "invalid"
			}
			req := apiClientLifecycleRequest(org, uuid.Nil, id, "", "")
			rr := httptest.NewRecorder()
			switch tt.endpoint {
			case "list":
				h.List(rr, req)
			case "get":
				h.Get(rr, req)
			case "tokens":
				h.ListTokens(rr, req)
			}
			require.Equal(t, tt.status, rr.Code, "read endpoint should preserve dependency and validation outcomes")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else if tt.endpoint == "get" {
				var response models.SingleResponse[models.APIClient]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "client envelope should decode")
				require.Equal(t, client, response.Data, "get should return exact client metadata")
			} else if tt.endpoint == "list" {
				expected := []models.APIClient{client}
				if tt.empty {
					expected = []models.APIClient{}
				}
				var response models.ListResponse[models.APIClient]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "client list envelope should decode")
				require.Equal(t, expected, response.Data, "list should use arrays and return exact scoped clients")
			} else {
				// Hashes are intentionally omitted from the public token response.
				token.TokenHash = ""
				expected := []models.APIToken{token}
				if tt.empty {
					expected = []models.APIToken{}
				}
				var response models.ListResponse[models.APIToken]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "token list envelope should decode")
				require.Equal(t, expected, response.Data, "list should return exact public token metadata")
				require.NotContains(t, rr.Body.String(), "sha256:private", "token hash must not be exposed")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "reads must enforce tenant, target, and active-token predicates")
		})
	}
}

func requireAPIClientError(t *testing.T, rr *httptest.ResponseRecorder, code string) {
	t.Helper()
	var response models.ErrorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "API failure must use JSON envelope")
	require.Equal(t, code, response.Error.Code, "API failure must use stable machine-readable code")
	require.NotContains(t, rr.Body.String(), "private DB failure", "internal dependency details must remain private")
}

func TestAPIClientHandlerRevocationLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, code                                     string
		revoke, anonymous, invalidClient, invalidToken bool
		affected                                       int64
		queryErr                                       error
		status                                         int
	}{
		{name: "disable client", affected: 1, status: 204},
		{name: "missing or already disabled client", status: 404, code: "NOT_FOUND"},
		{name: "disable failure", queryErr: errors.New("private DB failure"), status: 500, code: "DISABLE_FAILED"},
		{name: "disable anonymous", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "disable malformed target", invalidClient: true, status: 400, code: "INVALID_ID"},
		{name: "revoke token", revoke: true, affected: 1, status: 204},
		{name: "foreign or already revoked token", revoke: true, status: 404, code: "NOT_FOUND"},
		{name: "revoke failure", revoke: true, queryErr: errors.New("private DB failure"), status: 500, code: "REVOKE_FAILED"},
		{name: "revoke anonymous", revoke: true, anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "revoke malformed client", revoke: true, invalidClient: true, status: 400, code: "INVALID_ID"},
		{name: "revoke malformed token", revoke: true, invalidToken: true, status: 400, code: "INVALID_ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, user, client, token := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create revocation fixture")
			defer mock.Close()
			if !tt.anonymous && !tt.invalidClient && !tt.invalidToken {
				query := `UPDATE api_clients SET status = 'disabled', disabled_by_user_id = @disabled_by_user_id, disabled_at = now(), updated_at = now() WHERE org_id = @org_id AND id = @id AND status <> 'disabled'`
				args := pgx.NamedArgs{"org_id": org, "id": client, "disabled_by_user_id": user}
				tag := "UPDATE"
				if tt.revoke {
					query = `UPDATE api_tokens SET revoked_at = now(), revoked_by_user_id = @revoked_by_user_id WHERE org_id = @org_id AND api_client_id = @api_client_id AND id = @id AND revoked_at IS NULL`
					args = pgx.NamedArgs{"org_id": org, "api_client_id": client, "id": token, "revoked_by_user_id": user}
				}
				expectation := mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(args)
				if tt.queryErr != nil {
					expectation.WillReturnError(tt.queryErr)
				} else {
					expectation.WillReturnResult(pgxmock.NewResult(tag, tt.affected))
				}
			}
			h := NewAPIClientHandler(db.NewAPIClientStore(mock), db.NewAPITokenStore(mock))
			clientPath, tokenPath := client.String(), token.String()
			if tt.anonymous {
				user = uuid.Nil
			}
			if tt.invalidClient {
				clientPath = "invalid"
			}
			if tt.invalidToken {
				tokenPath = "invalid"
			}
			rr := httptest.NewRecorder()
			req := apiClientLifecycleRequest(org, user, clientPath, tokenPath, "")
			if tt.revoke {
				h.RevokeToken(rr, req)
			} else {
				h.Delete(rr, req)
			}
			require.Equal(t, tt.status, rr.Code, "revocation should distinguish invalid, foreign, and failed mutations")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else {
				require.Equal(t, "", rr.Body.String(), "successful revocation should return no content")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "revocation must scope org, client, token, and actor")
		})
	}
}

func TestAPIClientHandlerUpdateLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, code       string
		anonymous, invalid     bool
		getErr, updateErr      error
		status                 int
		nameValue, statusValue string
		description            *string
	}{
		{name: "patch name and clear description", body: `{"name":"  release bot  ","description":"  "}`, status: 200, nameValue: "release bot", statusValue: "enabled"},
		{name: "reenable clears disable metadata", body: `{"status":"enabled"}`, status: 200, nameValue: "CI", statusValue: "enabled"},
		{name: "invalid JSON", body: `{`, status: 400, code: "INVALID_JSON"},
		{name: "empty name", body: `{"name":" "}`, status: 400, code: "MISSING_FIELD"},
		{name: "invalid status", body: `{"status":"unknown"}`, status: 400, code: "INVALID_STATUS"},
		{name: "unknown client", getErr: pgx.ErrNoRows, status: 404, code: "NOT_FOUND"},
		{name: "read failure", getErr: errors.New("private DB failure"), status: 500, code: "GET_FAILED"},
		{name: "update failure", body: `{"name":"release bot"}`, nameValue: "release bot", statusValue: "disabled", updateErr: errors.New("private DB failure"), status: 500, code: "UPDATE_FAILED"},
		{name: "anonymous", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "malformed ID", invalid: true, status: 400, code: "INVALID_ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, user, id := uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			description := "old description"
			original := models.APIClient{ID: id, OrgID: org, Name: "CI", Description: &description, Status: models.APIClientStatusDisabled, DisabledByUserID: &user, DisabledAt: &now, CreatedAt: now, UpdatedAt: now}
			expected := original
			expected.Name = tt.nameValue
			expected.Status = models.APIClientStatus(tt.statusValue)
			if tt.name == "patch name and clear description" {
				expected.Description = nil
				original.Status = models.APIClientStatusEnabled
				original.DisabledByUserID = nil
				original.DisabledAt = nil
				expected.DisabledByUserID = nil
				expected.DisabledAt = nil
			}
			if tt.name == "reenable clears disable metadata" {
				expected.DisabledByUserID = nil
				expected.DisabledAt = nil
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize client update fixture")
			defer mock.Close()
			if !tt.anonymous && !tt.invalid {
				q := mock.ExpectQuery(`SELECT .* FROM api_clients WHERE org_id = @org_id AND id = @id`).WithArgs(pgx.NamedArgs{"org_id": org, "id": id})
				if tt.getErr != nil {
					q.WillReturnError(tt.getErr)
				} else {
					q.WillReturnRows(apiClientLifecycleRows(original))
				}
				if tt.status == 200 || tt.updateErr != nil {
					update := mock.ExpectQuery(`UPDATE api_clients .* WHERE org_id = @org_id AND id = @id RETURNING`).WithArgs(pgx.NamedArgs{"id": id, "org_id": org, "name": expected.Name, "description": expected.Description, "status": expected.Status, "disabled_by_user_id": expected.DisabledByUserID, "disabled_at": expected.DisabledAt})
					if tt.updateErr != nil {
						update.WillReturnError(tt.updateErr)
					} else {
						update.WillReturnRows(apiClientLifecycleRows(expected))
					}
				}
			}
			h := NewAPIClientHandler(db.NewAPIClientStore(mock), db.NewAPITokenStore(mock))
			path := id.String()
			if tt.invalid {
				path = "bad"
			}
			if tt.anonymous {
				user = uuid.Nil
			}
			rr := httptest.NewRecorder()
			h.Update(rr, apiClientLifecycleRequest(org, user, path, "", tt.body))
			require.Equal(t, tt.status, rr.Code, "client patch should validate input and preserve store failures")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else {
				var response models.SingleResponse[models.APIClient]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "client patch envelope should decode")
				require.Equal(t, expected, response.Data, "patch should preserve omitted fields and clear disable metadata on reenable")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "patch must scope reads and writes to the authenticated tenant")
		})
	}
}

// Match the generated bearer token without replacing the package-level generator.
// Each fixture records the actual hash and prefix and checks their consistency
// against the plaintext returned once in the response.
type issuedTokenField struct {
	field     string
	token     *models.APIToken
	rows      *pgxmock.Rows
	populated *bool
}

func (a issuedTokenField) Match(value any) bool {
	valueString, ok := value.(string)
	if !ok {
		return false
	}
	if a.field == "hash" {
		if len(valueString) != 71 || !strings.HasPrefix(valueString, "sha256:") {
			return false
		}
		a.token.TokenHash = valueString
	} else {
		if len(valueString) != 11 || !strings.HasPrefix(valueString, "143_sk_") {
			return false
		}
		a.token.TokenPrefix = valueString
	}
	if a.token.TokenHash != "" && a.token.TokenPrefix != "" && !*a.populated {
		token := a.token
		a.rows.AddRow(token.ID, token.OrgID, token.APIClientID, token.Name, token.TokenHash, token.TokenPrefix, token.Scopes, token.RepositoryIDs, token.AllowedIPCidrs, token.ExpiresAt, token.LastUsedAt, token.LastUsedIP, token.LastUsedUserAgent, token.RevokedByUserID, token.RevokedAt, token.CreatedByUserID, token.CreatedAt)
		*a.populated = true
	}
	return true
}

func TestAPIClientHandlerCreateKeyTransaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, code, failAt string
		anonymous, unconfigured  bool
		status                   int
	}{
		{name: "create atomically", status: 201},
		{name: "begin fails", failAt: "begin", status: 500, code: "CREATE_FAILED"},
		{name: "client insert fails", failAt: "client", status: 500, code: "CREATE_FAILED"},
		{name: "token insert fails rolls back client", failAt: "token", status: 500, code: "CREATE_FAILED"},
		{name: "commit fails", failAt: "commit", status: 500, code: "CREATE_FAILED"},
		{name: "anonymous", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "unconfigured", unconfigured: true, status: 500, code: "CREATE_FAILED"},
		{name: "invalid JSON", body: `{`, status: 400, code: "INVALID_JSON"},
		{name: "missing integration name", body: `{"token_name":"bot","scopes":["sessions:read"]}`, status: 400, code: "MISSING_FIELD"},
		{name: "missing token name", body: `{"integration_name":"CI","scopes":["sessions:read"]}`, status: 400, code: "MISSING_FIELD"},
		{name: "invalid scope", body: `{"integration_name":"CI","token_name":"bot","scopes":["secret:everything"]}`, status: 400, code: "INVALID_SCOPE"},
		{name: "invalid expiry", body: `{"integration_name":"CI","token_name":"bot","scopes":["sessions:read"],"expires_at":"yesterday"}`, status: 400, code: "INVALID_TIMESTAMP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, user, clientID, tokenID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			description := "automation"
			client := models.APIClient{ID: clientID, OrgID: org, Name: "CI", Description: &description, Status: models.APIClientStatusEnabled, CreatedByUserID: &user, CreatedAt: now, UpdatedAt: now}
			token := models.APIToken{ID: tokenID, OrgID: org, APIClientID: clientID, Name: "release bot", Scopes: []string{"sessions:read"}, AllowedIPCidrs: []string{"127.0.0.1"}, CreatedByUserID: &user, CreatedAt: now}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create transaction fixture")
			defer mock.Close()
			if tt.body == "" && !tt.anonymous && !tt.unconfigured {
				if tt.failAt == "begin" {
					mock.ExpectBegin().WillReturnError(errors.New("private DB failure"))
				} else {
					mock.ExpectBegin()
					insertClient := mock.ExpectQuery(`INSERT INTO api_clients`).WithArgs(pgx.NamedArgs{"org_id": org, "name": "CI", "description": &description, "status": models.APIClientStatusEnabled, "created_by_user_id": &user})
					if tt.failAt == "client" {
						insertClient.WillReturnError(errors.New("private DB failure"))
					} else {
						insertClient.WillReturnRows(apiClientLifecycleRows(client))
						rows := apiTokenLifecycleRows()
						populated := false
						insertToken := mock.ExpectQuery(`INSERT INTO api_tokens`).WithArgs(pgx.NamedArgs{
							"org_id": org, "api_client_id": clientID, "name": "release bot",
							"token_hash":   issuedTokenField{field: "hash", token: &token, rows: rows, populated: &populated},
							"token_prefix": issuedTokenField{field: "prefix", token: &token, rows: rows, populated: &populated},
							"scopes":       token.Scopes, "repository_ids": token.RepositoryIDs, "allowed_ip_cidrs": token.AllowedIPCidrs,
							"expires_at": token.ExpiresAt, "created_by_user_id": &user,
						})
						if tt.failAt == "token" {
							insertToken.WillReturnError(errors.New("private DB failure"))
						} else {
							insertToken.WillReturnRows(rows)
							if tt.failAt == "commit" {
								mock.ExpectCommit().WillReturnError(errors.New("private DB failure"))
							} else {
								mock.ExpectCommit()
							}
						}
					}
					mock.ExpectRollback()
				}
			}
			h := NewAPIClientHandler(db.NewAPIClientStore(mock), db.NewAPITokenStore(mock))
			if !tt.unconfigured {
				h.SetTxStarter(mock)
			}
			if tt.anonymous {
				user = uuid.Nil
			}
			body := tt.body
			if body == "" {
				body = `{"integration_name":"  CI  ","description":" automation ","token_name":" release bot ","scopes":["sessions:read"],"allowed_ip_cidrs":[" 127.0.0.1 "]}`
			}
			rr := httptest.NewRecorder()
			h.CreateAPIKey(rr, apiClientLifecycleRequest(org, user, "", "", body))
			require.Equal(t, tt.status, rr.Code, "key creation should fail safely at each transaction stage")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else {
				var response models.SingleResponse[createAPIKeyResponse]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "new key response should decode")
				require.Equal(t, client, response.Data.Client, "created client should preserve tenant and creator")
				require.Equal(t, token.TokenHash, db.HashAPIToken(response.Data.Token.Token), "returned secret must correspond to stored hash")
				require.Equal(t, token.TokenPrefix, db.APITokenPrefix(response.Data.Token.Token), "public prefix must match issued secret")
				token.TokenHash = ""
				require.Equal(t, token, response.Data.Token.APIToken, "token response should omit secret hash and retain restrictions")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "client and token must commit together or roll back together")
		})
	}
}

func TestAPIClientHandlerCreateTokenLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, code                    string
		anonymous, invalidID, insertFailure bool
		lookupError                         error
		status                              int
	}{
		{name: "issue restricted token", status: http.StatusCreated},
		{name: "insert failure returns no secret", insertFailure: true, status: 500, code: "CREATE_FAILED"},
		{name: "foreign client", lookupError: pgx.ErrNoRows, status: 404, code: "NOT_FOUND"},
		{name: "lookup unavailable", lookupError: errors.New("private DB failure"), status: 500, code: "GET_FAILED"},
		{name: "anonymous", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "invalid client ID", invalidID: true, status: 400, code: "INVALID_ID"},
		{name: "invalid JSON", body: "{", status: 400, code: "INVALID_JSON"},
		{name: "blank name", body: `{"name":"  "}`, status: 400, code: "MISSING_FIELD"},
		{name: "unknown scope", body: `{"name":"bot","scopes":["private:all"]}`, status: 400, code: "INVALID_SCOPE"},
		{name: "invalid address restriction", body: `{"name":"bot","scopes":["sessions:read"],"allowed_ip_cidrs":["invalid"]}`, status: 400, code: "INVALID_IP_ALLOWLIST"},
		{name: "invalid expiry", body: `{"name":"bot","scopes":["sessions:read"],"expires_at":"tomorrow"}`, status: 400, code: "INVALID_TIMESTAMP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, user, clientID, repoID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated token issuance fixture")
			defer mock.Close()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			expires := now.Add(24 * time.Hour)
			token := models.APIToken{ID: uuid.New(), OrgID: org, APIClientID: clientID, Name: "release bot", Scopes: []string{"sessions:read"}, RepositoryIDs: []uuid.UUID{repoID}, AllowedIPCidrs: []string{"127.0.0.1"}, ExpiresAt: &expires, CreatedByUserID: &user, CreatedAt: now}
			if !tt.anonymous && !tt.invalidID {
				lookup := mock.ExpectQuery(`SELECT .* FROM api_clients WHERE org_id = @org_id AND id = @id`).WithArgs(pgx.NamedArgs{"org_id": org, "id": clientID})
				if tt.lookupError != nil {
					lookup.WillReturnError(tt.lookupError)
				} else {
					lookup.WillReturnRows(apiClientLifecycleRows(models.APIClient{ID: clientID, OrgID: org, Status: models.APIClientStatusEnabled}))
				}
				if tt.body == "" && tt.lookupError == nil {
					rows, populated := apiTokenLifecycleRows(), false
					insert := mock.ExpectQuery(`INSERT INTO api_tokens`).WithArgs(pgx.NamedArgs{
						"org_id": org, "api_client_id": clientID, "name": token.Name,
						"token_hash":   issuedTokenField{field: "hash", token: &token, rows: rows, populated: &populated},
						"token_prefix": issuedTokenField{field: "prefix", token: &token, rows: rows, populated: &populated},
						"scopes":       token.Scopes, "repository_ids": token.RepositoryIDs, "allowed_ip_cidrs": token.AllowedIPCidrs,
						"expires_at": token.ExpiresAt, "created_by_user_id": &user,
					})
					if tt.insertFailure {
						insert.WillReturnError(errors.New("private DB failure"))
					} else {
						insert.WillReturnRows(rows)
					}
				}
			}
			body := tt.body
			if body == "" {
				body = `{"name":" release bot ","scopes":["sessions:read"],"repository_ids":["` + repoID.String() + `"],"allowed_ip_cidrs":[" 127.0.0.1 "],"expires_at":"` + expires.Format(time.RFC3339) + `"}`
			}
			clientRoute := clientID.String()
			if tt.invalidID {
				clientRoute = "invalid"
			}
			if tt.anonymous {
				user = uuid.Nil
			}
			rr := httptest.NewRecorder()
			NewAPIClientHandler(db.NewAPIClientStore(mock), db.NewAPITokenStore(mock)).CreateToken(rr, apiClientLifecycleRequest(org, user, clientRoute, "", body))
			require.Equal(t, tt.status, rr.Code, "token issuance must reject invalid input and fail without exposing secrets")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
				require.NotContains(t, rr.Body.String(), "143_sk_", "failed issuance must not return the generated secret")
			} else {
				var response models.SingleResponse[createAPITokenResponse]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "issued token response must decode")
				require.Equal(t, token.TokenHash, db.HashAPIToken(response.Data.Token), "returned secret must match its durable hash")
				require.Equal(t, token.TokenPrefix, db.APITokenPrefix(response.Data.Token), "public prefix must match issued secret")
				token.TokenHash = ""
				require.Equal(t, token, response.Data.APIToken, "response must preserve every restriction and hide the stored hash")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "issuance must bind the authenticated tenant, client and creator")
		})
	}
}
