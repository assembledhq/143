package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	appcrypto "github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestJoinTokenHandler_GetLinkReturnsInstallCommand(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	tokenID := uuid.New()
	creatorID := uuid.New()
	now := time.Now()
	rawToken := "143j_AbCdEfGhIjKlMnOpQrStUvWx"
	mock := newPgxMock(t)
	handler := NewJoinTokenHandler(db.NewOrgJoinTokenStore(mock), "https://143.example")

	mock.ExpectQuery(`(?s)SELECT .*org_join_tokens`).
		WithArgs(pgx.NamedArgs{"id": tokenID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(orgJoinTokenTestColumns()).AddRow(
			tokenID, orgID, db.HashAPIToken(rawToken), "143j_AbCdEfGh", models.RoleMember, "Eng link",
			[]byte("v0:"+rawToken), creatorID, nil, 0, nil, nil, nil, now,
		))

	req := newJoinTokenRequest(http.MethodGet, "/api/v1/org/join-tokens/"+tokenID.String()+"/link", orgID, userID, map[string]string{
		"id": tokenID.String(),
	})
	w := httptest.NewRecorder()
	handler.GetLink(w, req)

	require.NoError(t, mock.ExpectationsWereMet(), "GetLink should satisfy all DB expectations")
	require.Equal(t, http.StatusOK, w.Code, "GetLink should return OK for an active recoverable token, body: %s", w.Body.String())
	var body struct {
		Data struct {
			ID             string `json:"id"`
			TokenPrefix    string `json:"token_prefix"`
			InstallCommand string `json:"install_command"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "GetLink response should be valid JSON")
	require.Equal(t, tokenID.String(), body.Data.ID, "GetLink should return the requested token id")
	require.Equal(t, "143j_AbCdEfGh", body.Data.TokenPrefix, "GetLink should return the display prefix")
	require.Equal(t, "curl -fsSL https://143.example/install/"+rawToken+" | sh", body.Data.InstallCommand, "GetLink should return the install command")
}

func TestJoinTokenHandler_GetLinkRejectsLegacyRows(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	tokenID := uuid.New()
	creatorID := uuid.New()
	now := time.Now()
	mock := newPgxMock(t)
	handler := NewJoinTokenHandler(db.NewOrgJoinTokenStore(mock), "https://143.example")

	mock.ExpectQuery(`(?s)SELECT .*org_join_tokens`).
		WithArgs(pgx.NamedArgs{"id": tokenID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(orgJoinTokenTestColumns()).AddRow(
			tokenID, orgID, "sha256:legacy", "143j_Legacy1", models.RoleMember, "Legacy link",
			nil, creatorID, nil, 0, nil, nil, nil, now,
		))

	req := newJoinTokenRequest(http.MethodGet, "/api/v1/org/join-tokens/"+tokenID.String()+"/link", orgID, userID, map[string]string{
		"id": tokenID.String(),
	})
	w := httptest.NewRecorder()
	handler.GetLink(w, req)

	require.NoError(t, mock.ExpectationsWereMet(), "GetLink should satisfy all DB expectations")
	require.Equal(t, http.StatusConflict, w.Code, "GetLink should reject legacy rows without encrypted raw tokens, body: %s", w.Body.String())
	require.Contains(t, w.Body.String(), "JOIN_TOKEN_NOT_RECOVERABLE", "GetLink should return a stable legacy-row error code")
}

func TestJoinTokenHandler_CreateStoresRecoverableRawToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	tokenID := uuid.New()
	now := time.Now()
	mock := newPgxMock(t)
	handler := NewJoinTokenHandler(db.NewOrgJoinTokenStore(mock), "https://143.example")

	mock.ExpectQuery(`(?s)INSERT INTO org_join_tokens`).
		WithArgs(pgx.NamedArgs{
			"org_id":              orgID,
			"token_hash":          pgxmock.AnyArg(),
			"token_prefix":        pgxmock.AnyArg(),
			"raw_token_encrypted": encryptedOrgJoinTokenArg{},
			"role":                models.RoleMember,
			"name":                "Eng link",
			"created_by_user_id":  userID,
			"max_uses":            (*int)(nil),
			"expires_at":          (*time.Time)(nil),
		}).
		WillReturnRows(pgxmock.NewRows(orgJoinTokenTestColumns()).AddRow(
			tokenID, orgID, "sha256:test", "143j_TestTok", models.RoleMember, "Eng link",
			[]byte("v0:143j_TestToken123456789012"), userID, nil, 0, nil, nil, nil, now,
		))

	req := newJoinTokenRequest(http.MethodPost, "/api/v1/org/join-tokens", orgID, userID, nil)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/org/join-tokens", strings.NewReader(`{"name":"Eng link","role":"member"}`)).WithContext(req.Context())
	w := httptest.NewRecorder()
	handler.Create(w, req)

	require.NoError(t, mock.ExpectationsWereMet(), "Create should insert the encrypted raw token")
	require.Equal(t, http.StatusCreated, w.Code, "Create should return created for a recoverable join token, body: %s", w.Body.String())
}

func TestJoinTokenHandler_ListExcludesRevokedTokens(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	tokenID := uuid.New()
	creatorID := uuid.New()
	now := time.Now()
	mock := newPgxMock(t)
	handler := NewJoinTokenHandler(db.NewOrgJoinTokenStore(mock), "https://143.example")

	// The list query must filter revoked links at the database so they don't
	// clutter the settings list. Pin the WHERE clause here.
	mock.ExpectQuery(`(?s)SELECT .*FROM org_join_tokens.*revoked_at IS NULL`).
		WithArgs(pgx.NamedArgs{"org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(orgJoinTokenTestColumns()).AddRow(
			tokenID, orgID, "sha256:test", "143j_ActiveTk", models.RoleMember, "Active link",
			[]byte("v0:143j_TestToken123456789012"), creatorID, nil, 0, nil, nil, nil, now,
		))

	req := newJoinTokenRequest(http.MethodGet, "/api/v1/org/join-tokens", orgID, userID, nil)
	w := httptest.NewRecorder()
	handler.List(w, req)

	require.NoError(t, mock.ExpectationsWereMet(), "List should filter revoked tokens in its query")
	require.Equal(t, http.StatusOK, w.Code, "List should return OK, body: %s", w.Body.String())
	var body struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "List response should be valid JSON")
	require.Len(t, body.Data, 1, "List should return the active token")
	require.Equal(t, "active", body.Data[0].Status, "listed tokens should never be revoked")
}

func newJoinTokenRequest(method, path string, orgID, userID uuid.UUID, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	if len(params) > 0 {
		rctx := chi.NewRouteContext()
		for key, value := range params {
			rctx.URLParams.Add(key, value)
		}
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func orgJoinTokenTestColumns() []string {
	return []string{
		"id", "org_id", "token_hash", "token_prefix", "role", "name", "raw_token_encrypted",
		"created_by_user_id", "max_uses", "use_count", "expires_at", "revoked_at", "revoked_by_user_id",
		"created_at",
	}
}

type encryptedOrgJoinTokenArg struct{}

func (encryptedOrgJoinTokenArg) Match(v interface{}) bool {
	data, ok := v.([]byte)
	if !ok {
		return false
	}
	plaintext, err := appcrypto.DevDecrypt(data)
	if err != nil {
		return false
	}
	return regexp.MustCompile(`^143j_[A-Za-z0-9]{24}$`).Match(plaintext)
}
