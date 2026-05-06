package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codexauth"
)

func codexTestLogger() zerolog.Logger {
	return zerolog.Nop()
}

func codexBufferedLogger(buf *bytes.Buffer) zerolog.Logger {
	return zerolog.New(buf)
}

func codexAddOrgContext(r *http.Request) *http.Request {
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

type codexCredentialStoreStub struct {
	disableErr error
	disabled   bool
	// getByIDResult is returned by GetByID if set, otherwise "not found".
	getByIDResult *models.DecryptedCredential
	// existsForProviderByIDResult is returned by ExistsForProviderByID; default false means "not ours".
	existsForProviderByIDResult bool
	// insertPendingAuthErr overrides the default "not implemented" error
	// returned by InsertPendingAuth, letting tests inject typed errors like
	// *db.ErrCredentialLabelTaken to exercise handler branches.
	insertPendingAuthErr error
}

func (s *codexCredentialStoreStub) Disable(_ context.Context, _ models.Scope, _ models.ProviderName) error {
	if s.disableErr != nil {
		return s.disableErr
	}
	s.disabled = true
	return nil
}

func (s *codexCredentialStoreStub) UpsertWithLabel(_ context.Context, _ models.Scope, _ *uuid.UUID, _ string, _ models.ProviderConfig) (*uuid.UUID, error) {
	return nil, errors.New("not implemented")
}

func (s *codexCredentialStoreStub) InsertPendingAuth(_ context.Context, _ models.Scope, _ *uuid.UUID, _ string, _ models.ProviderConfig) (*uuid.UUID, error) {
	if s.insertPendingAuthErr != nil {
		return nil, s.insertPendingAuthErr
	}
	return nil, errors.New("not implemented")
}

func (s *codexCredentialStoreStub) GetByID(_ context.Context, _ models.Scope, _ uuid.UUID) (*models.DecryptedCredential, error) {
	if s.getByIDResult != nil {
		return s.getByIDResult, nil
	}
	return nil, errors.New("not found")
}

func (s *codexCredentialStoreStub) ListByProvider(_ context.Context, _ models.Scope, _ models.ProviderName) ([]models.DecryptedCredential, error) {
	return nil, nil
}

func (s *codexCredentialStoreStub) GetByProviderAndLabel(_ context.Context, _ models.Scope, _ models.ProviderName, _ string) (*models.DecryptedCredential, error) {
	return nil, errors.New("not found")
}

func (s *codexCredentialStoreStub) ClaimNextRoundRobin(_ context.Context, _ models.Scope, _ models.ProviderName) (*models.DecryptedCredential, error) {
	return nil, errors.New("not found")
}

func (s *codexCredentialStoreStub) DisableByID(_ context.Context, _ models.Scope, _ uuid.UUID) error {
	if s.disableErr != nil {
		return s.disableErr
	}
	s.disabled = true
	return nil
}

func (s *codexCredentialStoreStub) UpdateStatusByID(_ context.Context, _ models.Scope, _ uuid.UUID, _ string) error {
	return nil
}

func (s *codexCredentialStoreStub) UpsertByID(_ context.Context, _ models.Scope, _ uuid.UUID, _ models.ProviderConfig) error {
	return nil
}

func (s *codexCredentialStoreStub) ExistsForProviderByID(_ context.Context, _ models.Scope, _ uuid.UUID, _ models.ProviderName) (bool, error) {
	return s.existsForProviderByIDResult, nil
}

func TestCodexAuthHandler_Initiate(t *testing.T) {
	t.Parallel()

	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"device_auth_id":   "dev_test_123",
			"user_code":        "TEST-CODE",
			"verification_uri": "https://auth.openai.com/codex/device",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	defer mockOpenAI.Close()

	svc := codexauth.NewService(nil, codexTestLogger())
	svc.SetHTTPClient(mockOpenAI.Client())
	svc.SetIssuer(mockOpenAI.URL)

	handler := NewCodexAuthHandler(svc, codexTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/codex-auth/initiate", nil)
	req = codexAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusOK, w.Code, "initiate should return 200")

	var resp models.SingleResponse[codexauth.DeviceAuthResponse]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "initiate response should be valid JSON")
	require.Equal(t, "TEST-CODE", resp.Data.UserCode, "initiate should return expected user code")
}

func TestCodexAuthHandler_Status_NoPending(t *testing.T) {
	t.Parallel()

	svc := codexauth.NewService(nil, codexTestLogger())

	handler := NewCodexAuthHandler(svc, codexTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/codex-auth/status", nil)
	req = codexAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Status(w, req)

	require.Equal(t, http.StatusOK, w.Code, "status should return 200 when no auth is pending")

	var resp models.SingleResponse[codexauth.AuthStatus]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "status response should be valid JSON")
	require.Equal(t, "none", resp.Data.Status, "status should report none when no auth flow exists")
}

func TestCodexAuthHandler_Initiate_Error(t *testing.T) {
	t.Parallel()

	// Use an unreachable server URL to force an error.
	svc := codexauth.NewService(nil, codexTestLogger())
	svc.SetIssuer("http://127.0.0.1:1") // unreachable port
	var logBuf bytes.Buffer
	logger := codexBufferedLogger(&logBuf)

	handler := NewCodexAuthHandler(svc, logger)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/codex-auth/initiate", nil)
	req = codexAddOrgContext(req)
	req = req.WithContext(logger.WithContext(req.Context()))
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "initiate should return 500 on error")

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "error response should be valid JSON")
	require.Contains(t, logBuf.String(), "failed to initiate device auth", "initiate error should be logged with context")
	require.Contains(t, logBuf.String(), "device auth request", "initiate error log should include wrapped service error details")
}

func codexAddRouteParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestCodexAuthHandler_Disconnect_Error(t *testing.T) {
	t.Parallel()

	testOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	credID := uuid.New()
	store := &codexCredentialStoreStub{
		disableErr:                  errors.New("db error"),
		existsForProviderByIDResult: true,
		getByIDResult:               &models.DecryptedCredential{ID: credID, OrgID: testOrgID},
	}
	svc := codexauth.NewService(store, codexTestLogger())
	handler := NewCodexAuthHandler(svc, codexTestLogger())

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/codex-auth/subscriptions/"+credID.String(), nil)
	req = codexAddOrgContext(req)
	req = codexAddRouteParam(req, "id", credID.String())
	w := httptest.NewRecorder()

	handler.DisconnectByPath(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "disconnect should return 500 when store fails")
}

func TestCodexAuthHandler_Disconnect_ReturnsJSON(t *testing.T) {
	t.Parallel()

	testOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	credID := uuid.New()
	store := &codexCredentialStoreStub{
		existsForProviderByIDResult: true,
		getByIDResult:               &models.DecryptedCredential{ID: credID, OrgID: testOrgID},
	}
	svc := codexauth.NewService(store, codexTestLogger())
	handler := NewCodexAuthHandler(svc, codexTestLogger())

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/codex-auth/subscriptions/"+credID.String(), nil)
	req = codexAddOrgContext(req)
	req = codexAddRouteParam(req, "id", credID.String())
	w := httptest.NewRecorder()

	handler.DisconnectByPath(w, req)

	require.Equal(t, http.StatusOK, w.Code, "disconnect should return 200 so the API client can parse JSON")
	require.Equal(t, "application/json", w.Header().Get("Content-Type"), "disconnect should return JSON content type")

	var resp models.SingleResponse[map[string]bool]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "disconnect response should be valid JSON")
	require.Equal(t, true, resp.Data["disconnected"], "disconnect should return disconnected=true")
	require.Equal(t, true, store.disabled, "disconnect should disable the stored ChatGPT credential")
}

// TestCodexAuthHandler_Initiate_LabelTaken verifies that when InsertPendingAuth
// returns a *db.ErrCredentialLabelTaken, the handler converts it to a 409
// Conflict response with code LABEL_TAKEN and a status-aware message — so the
// frontend can show "this label is already connected" instead of a generic 500.
func TestCodexAuthHandler_Initiate_LabelTaken(t *testing.T) {
	t.Parallel()

	// The upstream OpenAI call must succeed so the service proceeds to the
	// DB persist step, where our stub injects the conflict.
	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"device_auth_id":   "dev_test_123",
			"user_code":        "TEST-CODE",
			"verification_uri": "https://auth.openai.com/codex/device",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	defer mockOpenAI.Close()

	store := &codexCredentialStoreStub{
		insertPendingAuthErr: &db.ErrCredentialLabelTaken{Label: "Team A", ExistingStatus: "active"},
	}
	svc := codexauth.NewService(store, codexTestLogger())
	svc.SetHTTPClient(mockOpenAI.Client())
	svc.SetIssuer(mockOpenAI.URL)

	handler := NewCodexAuthHandler(svc, codexTestLogger())

	reqBody := bytes.NewReader([]byte(`{"label":"Team A"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/codex-auth/initiate", reqBody)
	req.Header.Set("Content-Type", "application/json")
	req = codexAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "initiate should return 409 when the label is taken")

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "error response should be valid JSON")
	require.Equal(t, "LABEL_TAKEN", resp.Error.Code, "initiate should surface LABEL_TAKEN error code")
	require.Contains(t, resp.Error.Message, "Team A", "error message should mention the offending label")
	require.Contains(t, resp.Error.Message, "already connected", "active-status message should guide the user to disconnect")
}
