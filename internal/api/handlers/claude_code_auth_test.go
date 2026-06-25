package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
)

func claudeTestLogger() zerolog.Logger {
	return zerolog.Nop()
}

func claudeAddOrgContext(r *http.Request) *http.Request {
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := middleware.WithOrgID(r.Context(), orgID)
	// Handlers now require a user + active role on the context: scope
	// resolution defaults to org scope, which is admin-gated. Inject a stub
	// admin user so the existing tests behave the same way they did before
	// scope resolution was added.
	ctx = middleware.WithUser(ctx, &models.User{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000a1")})
	ctx = middleware.WithActiveRole(ctx, "admin")
	return r.WithContext(ctx)
}

// claudeStoreStub is a minimal claudecodeauth.CredentialStore implementation for
// handler tests. Only the methods the handler exercises are meaningful; the
// rest return zero/error defaults.
type claudeStoreStub struct {
	insertPendingAuthErr        error
	upsertWithLabelErr          error
	upsertWithLabelScope        models.Scope
	upsertWithLabelCreatedBy    *uuid.UUID
	upsertWithLabelLabel        string
	upsertWithLabelConfig       models.ProviderConfig
	disableErr                  error
	disabled                    bool
	existsForProviderByIDResult bool
	getByIDCredential           *models.DecryptedCredential
}

func (s *claudeStoreStub) UpsertWithLabel(_ context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	if s.upsertWithLabelErr != nil {
		return nil, s.upsertWithLabelErr
	}
	s.upsertWithLabelScope = scope
	s.upsertWithLabelCreatedBy = createdBy
	s.upsertWithLabelLabel = label
	s.upsertWithLabelConfig = cfg
	id := uuid.New()
	return &id, nil
}
func (s *claudeStoreStub) InsertPendingAuth(context.Context, models.Scope, *uuid.UUID, string, models.ProviderConfig) (*uuid.UUID, error) {
	if s.insertPendingAuthErr != nil {
		return nil, s.insertPendingAuthErr
	}
	id := uuid.New()
	return &id, nil
}
func (s *claudeStoreStub) GetByID(context.Context, models.Scope, uuid.UUID) (*models.DecryptedCredential, error) {
	if s.getByIDCredential != nil {
		return s.getByIDCredential, nil
	}
	if s.existsForProviderByIDResult {
		return &models.DecryptedCredential{
			ID:       uuid.New(),
			OrgID:    uuid.New(),
			Provider: models.ProviderAnthropicSubscription,
			Label:    "team-a",
			Config: models.AnthropicSubscriptionConfig{
				AccessToken:  "access",
				RefreshToken: "refresh",
			},
			Status: "active",
		}, nil
	}
	return nil, claudecodeauth.ErrCredentialNotFound
}
func (s *claudeStoreStub) GetByProviderAndLabel(context.Context, models.Scope, models.ProviderName, string) (*models.DecryptedCredential, error) {
	// Use the sentinel the service understands so CompleteOAuth maps it to
	// ErrPendingAuthNotFound (404); a plain errors.New would now be treated
	// as a real DB failure and surface as a 500.
	return nil, claudecodeauth.ErrCredentialNotFound
}
func (s *claudeStoreStub) ListByProvider(context.Context, models.Scope, models.ProviderName) ([]models.DecryptedCredential, error) {
	return nil, nil
}
func (s *claudeStoreStub) ClaimNextLabeledRoundRobin(context.Context, models.Scope, models.ProviderName) (*models.DecryptedCredential, error) {
	return nil, claudecodeauth.ErrCredentialNotFound
}
func (s *claudeStoreStub) DisableByID(context.Context, models.Scope, uuid.UUID) error {
	if s.disableErr != nil {
		return s.disableErr
	}
	s.disabled = true
	return nil
}
func (s *claudeStoreStub) UpdateStatusByID(context.Context, models.Scope, uuid.UUID, models.CodingCredentialRowStatus) error {
	return nil
}
func (s *claudeStoreStub) UpsertByID(context.Context, models.Scope, uuid.UUID, models.ProviderConfig) error {
	return nil
}
func (s *claudeStoreStub) ExistsForProviderByID(context.Context, models.Scope, uuid.UUID, models.ProviderName) (bool, error) {
	return s.existsForProviderByIDResult, nil
}
func (s *claudeStoreStub) DisableLabeled(context.Context, models.Scope, models.ProviderName) error {
	s.disabled = true
	return nil
}
func (s *claudeStoreStub) HasActiveLabeled(context.Context, models.Scope, models.ProviderName) (bool, error) {
	return false, nil
}

func claudeTestJWTWithExp(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	signature := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	return header + "." + payload + "." + signature
}

func TestClaudeCodeAuthHandler_Initiate_RequiresLabel(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := bytes.NewBufferString(`{"label":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/initiate", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "empty label should be rejected")
	require.Contains(t, w.Body.String(), "INVALID_LABEL")
}

func TestClaudeCodeAuthHandler_Initiate_LabelTaken(t *testing.T) {
	t.Parallel()

	store := &claudeStoreStub{
		insertPendingAuthErr: &db.ErrCredentialLabelTaken{Label: "work", ExistingStatus: "active"},
	}
	svc := claudecodeauth.NewService(store, claudeTestLogger())

	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{"label":"work"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/initiate", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "duplicate active label should return 409")
	require.Contains(t, w.Body.String(), "LABEL_TAKEN")
}

func TestClaudeCodeAuthHandler_Initiate_ReturnsAuthorizeURL(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{"label":"team-a"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/initiate", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp models.SingleResponse[claudecodeauth.InitiateResponse]
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.NotEmpty(t, resp.Data.AuthorizeURL)
	require.NotEmpty(t, resp.Data.State)
	require.Contains(t, resp.Data.AuthorizeURL, "code_challenge=")
	require.Contains(t, resp.Data.AuthorizeURL, "code_challenge_method=S256")
}

func TestClaudeCodeAuthHandler_StoreOAuthToken_StoresSetupToken(t *testing.T) {
	t.Parallel()

	store := &claudeStoreStub{}
	svc := claudecodeauth.NewService(store, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	token := claudeTestJWTWithExp(t, time.Now().Add(24*time.Hour))
	body := strings.NewReader(fmt.Sprintf(`{"label":"personal claude","scope":"personal","oauth_token":%q}`, token))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/oauth-token", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.StoreOAuthToken(w, req)

	require.Equal(t, http.StatusOK, w.Code, "valid setup token should be accepted")
	require.Equal(t, "personal claude", store.upsertWithLabelLabel, "setup-token endpoint should store the provided label")
	require.True(t, store.upsertWithLabelScope.IsPersonal(), "setup-token endpoint should honor personal scope")
	require.NotNil(t, store.upsertWithLabelCreatedBy, "setup-token endpoint should record the creator")
	cfg, ok := store.upsertWithLabelConfig.(models.AnthropicSubscriptionConfig)
	require.True(t, ok, "setup-token endpoint should store an Anthropic subscription config")
	require.Equal(t, models.AnthropicSubscriptionAuthModeSetupToken, cfg.AuthMode, "setup-token endpoint should mark the auth mode")
	require.Equal(t, token, cfg.OAuthToken, "setup-token endpoint should store the pasted token")
	require.True(t, cfg.OAuthTokenExpiresAt.After(time.Now()), "setup-token endpoint should store a future token expiration")
	require.Empty(t, cfg.AccessToken, "setup-token endpoint should not store rotating access tokens")
	require.Empty(t, cfg.RefreshToken, "setup-token endpoint should not store rotating refresh tokens")
	require.NotContains(t, w.Body.String(), token, "setup-token response should not echo token material")
}

func TestClaudeCodeAuthHandler_StoreOAuthToken_RejectsExpiredJWT(t *testing.T) {
	t.Parallel()

	store := &claudeStoreStub{}
	svc := claudecodeauth.NewService(store, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	token := claudeTestJWTWithExp(t, time.Now().Add(-time.Hour))
	body := strings.NewReader(fmt.Sprintf(`{"label":"team-a","oauth_token":%q}`, token))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/oauth-token", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.StoreOAuthToken(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "expired setup-token JWT should be rejected")
	require.Contains(t, w.Body.String(), "INVALID_TOKEN", "expired setup-token response should identify the invalid token")
	require.Nil(t, store.upsertWithLabelConfig, "expired setup token should not be stored")
	require.NotContains(t, w.Body.String(), token, "expired setup-token response should not echo token material")
}

func TestClaudeCodeAuthHandler_Complete_MissingBody(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/complete", nil)
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Complete(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REQUEST")
}

func TestClaudeCodeAuthHandler_Complete_RequiresLabelAndCode(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{"label":"team-a","code":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/complete", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Complete(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_CODE")
}

func TestClaudeCodeAuthHandler_Complete_RejectsOverlongCode(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{"label":"team-a","code":"` + strings.Repeat("a", claudeCodePasteMax) + `x"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/complete", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Complete(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "overlong pasted codes should be rejected before service processing")
	require.Contains(t, w.Body.String(), "INVALID_CODE", "handler should return the invalid-code error code for overlong pasted codes")
}

func TestClaudeCodeAuthHandler_Complete_NoPendingRow(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{"label":"team-a","code":"abc123#state456"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/complete", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Complete(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "PENDING_AUTH_NOT_FOUND")
}

func TestClaudeCodeAuthHandler_DisconnectByPath_NotFound(t *testing.T) {
	t.Parallel()

	store := &claudeStoreStub{existsForProviderByIDResult: false}
	svc := claudecodeauth.NewService(store, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	credID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/claude-code-auth/subscriptions/"+credID.String(), nil)
	req = claudeAddOrgContext(req)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", credID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	handler.DisconnectByPath(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.False(t, store.disabled, "unknown credential should not be disabled")
}

func TestClaudeCodeAuthHandler_DisconnectAll(t *testing.T) {
	t.Parallel()

	store := &claudeStoreStub{}
	svc := claudecodeauth.NewService(store, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/disconnect", nil)
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.DisconnectAll(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, store.disabled, "DisconnectAll should call DisableLabeled")
}

func TestClaudeCodeAuthHandler_Initiate_InvalidJSON(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{not json`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/initiate", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REQUEST")
}

func TestClaudeCodeAuthHandler_Initiate_LabelTooLong(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	longLabel := strings.Repeat("x", 101)
	body := strings.NewReader(`{"label":"` + longLabel + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/initiate", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_LABEL")
}

func TestClaudeCodeAuthHandler_Initiate_ServiceFailure(t *testing.T) {
	t.Parallel()

	store := &claudeStoreStub{insertPendingAuthErr: errors.New("boom")}
	svc := claudecodeauth.NewService(store, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{"label":"team-a"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/initiate", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "AUTH_INITIATE_FAILED")
}

func TestClaudeCodeAuthHandler_Complete_InvalidJSON(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{bad json`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/complete", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Complete(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_REQUEST")
}

func TestClaudeCodeAuthHandler_Complete_EmptyLabel(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	body := strings.NewReader(`{"label":"","code":"abc#def"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/complete", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Complete(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_LABEL")
}

func TestClaudeCodeAuthHandler_Complete_LabelTooLong(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	longLabel := strings.Repeat("x", 101)
	body := strings.NewReader(`{"label":"` + longLabel + `","code":"abc#def"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/claude-code-auth/complete", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Complete(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_LABEL")
}

func TestClaudeCodeAuthHandler_List(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/claude-code-auth/subscriptions", nil)
	req = claudeAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.List(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"data":[]`)
}

func TestClaudeCodeAuthHandler_DisconnectByPath_InvalidUUID(t *testing.T) {
	t.Parallel()

	svc := claudecodeauth.NewService(&claudeStoreStub{}, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/claude-code-auth/subscriptions/not-a-uuid", nil)
	req = claudeAddOrgContext(req)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	handler.DisconnectByPath(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestClaudeCodeAuthHandler_DisconnectByPath_Success(t *testing.T) {
	t.Parallel()

	store := &claudeStoreStub{existsForProviderByIDResult: true}
	svc := claudecodeauth.NewService(store, claudeTestLogger())
	handler := NewClaudeCodeAuthHandler(svc, claudeTestLogger())

	credID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/claude-code-auth/subscriptions/"+credID.String(), nil)
	req = claudeAddOrgContext(req)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", credID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	handler.DisconnectByPath(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, store.disabled, "existing credential should be disabled")
	require.Contains(t, w.Body.String(), `"disconnected":true`)
}
