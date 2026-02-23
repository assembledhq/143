package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codexauth"
)

func codexTestLogger() zerolog.Logger {
	return zerolog.Nop()
}

func codexAddOrgContext(r *http.Request) *http.Request {
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := middleware.WithOrgID(r.Context(), orgID)
	return r.WithContext(ctx)
}

type codexCredentialStoreStub struct {
	disableErr error
	disabled   bool
}

func (s *codexCredentialStoreStub) Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error {
	return nil
}

func (s *codexCredentialStoreStub) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	return nil, errors.New("not found")
}

func (s *codexCredentialStoreStub) UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error {
	return nil
}

func (s *codexCredentialStoreStub) Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	if s.disableErr != nil {
		return s.disableErr
	}
	s.disabled = true
	return nil
}

func TestCodexAuthHandler_Initiate(t *testing.T) {
	t.Parallel()

	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"device_code":      "dev_test_123",
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

	handler := NewCodexAuthHandler(svc)

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

	handler := NewCodexAuthHandler(svc)

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

	handler := NewCodexAuthHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/codex-auth/initiate", nil)
	req = codexAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Initiate(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "initiate should return 500 on error")

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "error response should be valid JSON")
}

func TestCodexAuthHandler_Disconnect_Error(t *testing.T) {
	t.Parallel()

	store := &codexCredentialStoreStub{disableErr: errors.New("db error")}
	svc := codexauth.NewService(store, codexTestLogger())
	handler := NewCodexAuthHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/codex-auth/disconnect", nil)
	req = codexAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Disconnect(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "disconnect should return 500 when store fails")
}

func TestCodexAuthHandler_Disconnect_ReturnsJSON(t *testing.T) {
	t.Parallel()

	store := &codexCredentialStoreStub{}
	svc := codexauth.NewService(store, codexTestLogger())
	handler := NewCodexAuthHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/codex-auth/disconnect", nil)
	req = codexAddOrgContext(req)
	w := httptest.NewRecorder()

	handler.Disconnect(w, req)

	require.Equal(t, http.StatusOK, w.Code, "disconnect should return 200 so the API client can parse JSON")
	require.Equal(t, "application/json", w.Header().Get("Content-Type"), "disconnect should return JSON content type")

	var resp models.SingleResponse[map[string]bool]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "disconnect response should be valid JSON")
	require.Equal(t, true, resp.Data["disconnected"], "disconnect should return disconnected=true")
	require.Equal(t, true, store.disabled, "disconnect should disable the stored ChatGPT credential")
}
