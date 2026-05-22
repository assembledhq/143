package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

// mockCredentialStore implements the credentialStore interface for testing.
type mockCredentialStore struct {
	upsertFn       func(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
	listFn         func(ctx context.Context, orgID uuid.UUID) ([]models.CredentialSummary, error)
	disableFn      func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
	updateStatusFn func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status models.CredentialStatus) error
}

func (m *mockCredentialStore) Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, orgID, cfg)
	}
	return nil
}

func (m *mockCredentialStore) ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.CredentialSummary, error) {
	if m.listFn != nil {
		return m.listFn(ctx, orgID)
	}
	return nil, nil
}

func (m *mockCredentialStore) Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	if m.disableFn != nil {
		return m.disableFn(ctx, orgID, provider)
	}
	return nil
}

func (m *mockCredentialStore) UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status models.CredentialStatus) error {
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, orgID, provider, status)
	}
	return nil
}

func newCredentialRequest(t *testing.T, method, path string, body any, orgID uuid.UUID, provider string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		switch v := body.(type) {
		case string:
			buf.WriteString(v)
		default:
			require.NoError(t, json.NewEncoder(&buf).Encode(body), "encoding request body should not error")
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")

	// Set org ID in context.
	ctx := middleware.WithOrgID(req.Context(), orgID)

	// Set chi URL param if provider is set.
	if provider != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", provider)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}

	return req.WithContext(ctx)
}

func TestCredentialHandler_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		store        *mockCredentialStore
		expectedCode int
	}{
		{
			name: "returns summaries",
			store: &mockCredentialStore{
				listFn: func(_ context.Context, _ uuid.UUID) ([]models.CredentialSummary, error) {
					return []models.CredentialSummary{
						{Provider: models.ProviderAnthropic, Configured: true, Status: "active", MaskedKey: "sk-ant...test"},
						{Provider: models.ProviderOpenAI, Configured: false},
					}, nil
				},
			},
			expectedCode: http.StatusOK,
		},
		{
			name: "store error",
			store: &mockCredentialStore{
				listFn: func(_ context.Context, _ uuid.UUID) ([]models.CredentialSummary, error) {
					return nil, fmt.Errorf("db error")
				},
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCredentialHandler(tt.store)
			orgID := uuid.New()
			req := newCredentialRequest(t, http.MethodGet, "/api/v1/settings/credentials", nil, orgID, "")
			rr := httptest.NewRecorder()

			handler.List(rr, req)
			require.Equal(t, tt.expectedCode, rr.Code, "handler should return expected status code")

			if tt.expectedCode == http.StatusOK {
				var resp models.ListResponse[models.CredentialSummary]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
				require.Len(t, resp.Data, 2, "response should contain 2 summaries")
			}
		})
	}
}

func TestCredentialHandler_Update(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		provider     string
		body         string
		store        *mockCredentialStore
		expectedCode int
	}{
		{
			name:     "updates anthropic credential",
			provider: "anthropic",
			body:     `{"api_key":"sk-ant-newkey123456"}`,
			store: &mockCredentialStore{
				upsertFn: func(_ context.Context, _ uuid.UUID, cfg models.ProviderConfig) error {
					ac, ok := cfg.(models.AnthropicConfig)
					if !ok {
						return fmt.Errorf("expected AnthropicConfig")
					}
					if ac.APIKey != "sk-ant-newkey123456" {
						return fmt.Errorf("unexpected api_key: %s", ac.APIKey)
					}
					return nil
				},
			},
			expectedCode: http.StatusOK,
		},
		{
			name:     "updates openai credential with api_type",
			provider: "openai",
			body:     `{"api_key":"sk-test123","api_type":"responses"}`,
			store: &mockCredentialStore{
				upsertFn: func(_ context.Context, _ uuid.UUID, cfg models.ProviderConfig) error {
					oc, ok := cfg.(models.OpenAIConfig)
					if !ok {
						return fmt.Errorf("expected OpenAIConfig")
					}
					if oc.APIType != "responses" {
						return fmt.Errorf("unexpected api_type: %s", oc.APIType)
					}
					return nil
				},
			},
			expectedCode: http.StatusOK,
		},
		{
			name:         "invalid provider",
			provider:     "invalid",
			body:         `{"api_key":"test"}`,
			store:        &mockCredentialStore{},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "invalid json",
			provider:     "anthropic",
			body:         `{bad json`,
			store:        &mockCredentialStore{},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:     "store error",
			provider: "anthropic",
			body:     `{"api_key":"sk-ant-test"}`,
			store: &mockCredentialStore{
				upsertFn: func(_ context.Context, _ uuid.UUID, _ models.ProviderConfig) error {
					return fmt.Errorf("db error")
				},
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:         "rejects empty api_key for anthropic",
			provider:     "anthropic",
			body:         `{"api_key":""}`,
			store:        &mockCredentialStore{},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "rejects empty api_key for openai",
			provider:     "openai",
			body:         `{"api_key":""}`,
			store:        &mockCredentialStore{},
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCredentialHandler(tt.store)
			orgID := uuid.New()
			req := newCredentialRequest(t, http.MethodPut, "/api/v1/settings/credentials/"+tt.provider, tt.body, orgID, tt.provider)
			rr := httptest.NewRecorder()

			handler.Update(rr, req)
			require.Equal(t, tt.expectedCode, rr.Code, "handler should return expected status code")

			if tt.expectedCode == http.StatusOK {
				var resp models.SingleResponse[models.CredentialSummary]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response should be valid JSON")
				require.True(t, resp.Data.Configured, "summary should be configured")
			}
		})
	}
}

func TestCredentialHandler_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		provider     string
		store        *mockCredentialStore
		expectedCode int
	}{
		{
			name:         "deletes credential",
			provider:     "anthropic",
			store:        &mockCredentialStore{},
			expectedCode: http.StatusNoContent,
		},
		{
			name:         "invalid provider",
			provider:     "invalid",
			store:        &mockCredentialStore{},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:     "store error",
			provider: "anthropic",
			store: &mockCredentialStore{
				disableFn: func(_ context.Context, _ uuid.UUID, _ models.ProviderName) error {
					return fmt.Errorf("db error")
				},
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCredentialHandler(tt.store)
			orgID := uuid.New()
			req := newCredentialRequest(t, http.MethodDelete, "/api/v1/settings/credentials/"+tt.provider, nil, orgID, tt.provider)
			rr := httptest.NewRecorder()

			handler.Delete(rr, req)
			require.Equal(t, tt.expectedCode, rr.Code, "handler should return expected status code")
		})
	}
}

// fakeOrgStore is a minimal in-memory orgSettingsMutator for the self-heal
// tests. It captures the last Update call so we can assert on the patched
// settings JSON.
type fakeOrgStore struct {
	org        models.Organization
	getErr     error
	updateErr  error
	lastUpdate *models.Organization
}

func (f *fakeOrgStore) GetByID(_ context.Context, _ uuid.UUID) (models.Organization, error) {
	if f.getErr != nil {
		return models.Organization{}, f.getErr
	}
	return f.org, nil
}

func (f *fakeOrgStore) Update(_ context.Context, org *models.Organization) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	dup := *org
	dup.Settings = append(json.RawMessage(nil), org.Settings...)
	f.lastUpdate = &dup
	f.org = dup
	return nil
}

func TestCredentialHandler_Delete_SelfHealsCappedLLMModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		settings          string
		listSummaries     []models.CredentialSummary
		llmDefaults       map[string]string
		expectReset       bool
		expectedFinalJSON string
	}{
		{
			// Org had its own OpenAI key + gpt-5.4. They delete the key. The
			// platform default still serves OpenAI but caps at mini/nano —
			// gpt-5.4 must be cleared so it stops billing the platform.
			name:              "clears llm_model when post-delete state would route through capped platform default",
			settings:          `{"llm_model":"gpt-5.4"}`,
			listSummaries:     []models.CredentialSummary{{Provider: models.ProviderOpenAI, Configured: false}},
			llmDefaults:       map[string]string{"openai": "sk-...platform"},
			expectReset:       true,
			expectedFinalJSON: `{"llm_model":""}`,
		},
		{
			// Same delete, but model is already mini — well within the cap.
			// Don't churn the org's setting.
			name:          "leaves llm_model alone when current value is still allowed under platform default",
			settings:      `{"llm_model":"gpt-5.4-mini"}`,
			listSummaries: []models.CredentialSummary{{Provider: models.ProviderOpenAI, Configured: false}},
			llmDefaults:   map[string]string{"openai": "sk-...platform"},
			expectReset:   false,
		},
		{
			// Org keeps an Anthropic key; gpt-5.4 still has no runtime key path
			// after the OpenAI delete, so the persisted model must be cleared.
			name:              "clears llm_model when no provider serves the model at all",
			settings:          `{"llm_model":"gpt-5.4"}`,
			listSummaries:     []models.CredentialSummary{{Provider: models.ProviderAnthropic, Configured: true}},
			llmDefaults:       map[string]string{},
			expectReset:       true,
			expectedFinalJSON: `{"llm_model":""}`,
		},
		{
			name:          "no-op when llm_model is empty",
			settings:      `{}`,
			listSummaries: []models.CredentialSummary{},
			llmDefaults:   map[string]string{"openai": "sk-...platform"},
			expectReset:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			credStore := &mockCredentialStore{
				listFn: func(_ context.Context, _ uuid.UUID) ([]models.CredentialSummary, error) {
					return tt.listSummaries, nil
				},
			}
			orgStore := &fakeOrgStore{
				org: models.Organization{
					ID:       orgID,
					Settings: json.RawMessage(tt.settings),
				},
			}

			handler := NewCredentialHandler(credStore)
			handler.SetSelfHeal(orgStore, tt.llmDefaults)

			req := newCredentialRequest(t, http.MethodDelete, "/api/v1/settings/credentials/openai", nil, orgID, "openai")
			rr := httptest.NewRecorder()
			handler.Delete(rr, req)
			require.Equal(t, http.StatusNoContent, rr.Code)

			if tt.expectReset {
				require.NotNil(t, orgStore.lastUpdate, "self-heal should have written the org back")
				require.JSONEq(t, tt.expectedFinalJSON, string(orgStore.lastUpdate.Settings))
			} else {
				require.Nil(t, orgStore.lastUpdate, "self-heal should not write when current model is allowed")
			}
		})
	}
}

func TestCredentialHandler_Delete_SelfHealNotWiredIsNoOp(t *testing.T) {
	t.Parallel()

	// No SetSelfHeal call: Delete must still succeed without touching org settings.
	handler := NewCredentialHandler(&mockCredentialStore{})
	orgID := uuid.New()
	req := newCredentialRequest(t, http.MethodDelete, "/api/v1/settings/credentials/openai", nil, orgID, "openai")
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
}

// Self-heal is a best-effort step: any error inside it is logged but never
// fails the credential delete. These tests lock that contract in by exercising
// each error branch and confirming the response stays 204 with no Update call.
func TestCredentialHandler_Delete_SelfHealErrorPathsAreBestEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		credLfn func(_ context.Context, _ uuid.UUID) ([]models.CredentialSummary, error)
		org     models.Organization
		getErr  error
		updErr  error
	}{
		{
			name:   "GetByID error is swallowed",
			getErr: fmt.Errorf("db down"),
		},
		{
			name: "invalid settings JSON is swallowed",
			org: models.Organization{
				Settings: json.RawMessage(`{"llm_model":`), // truncated → unmarshal fails
			},
		},
		{
			name: "ListSummaries error is swallowed",
			org: models.Organization{
				Settings: json.RawMessage(`{"llm_model":"gpt-5.4"}`),
			},
			credLfn: func(_ context.Context, _ uuid.UUID) ([]models.CredentialSummary, error) {
				return nil, fmt.Errorf("list down")
			},
		},
		{
			name: "Update error is swallowed",
			org: models.Organization{
				Settings: json.RawMessage(`{"llm_model":"gpt-5.4"}`),
			},
			updErr: fmt.Errorf("write down"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			tt.org.ID = orgID
			credStore := &mockCredentialStore{listFn: tt.credLfn}
			orgStore := &fakeOrgStore{org: tt.org, getErr: tt.getErr, updateErr: tt.updErr}

			handler := NewCredentialHandler(credStore)
			handler.SetSelfHeal(orgStore, map[string]string{"openai": "sk-...platform"})

			req := newCredentialRequest(t, http.MethodDelete, "/api/v1/settings/credentials/openai", nil, orgID, "openai")
			rr := httptest.NewRecorder()
			handler.Delete(rr, req)

			require.Equal(t, http.StatusNoContent, rr.Code,
				"self-heal failures must never break the credential delete itself")
		})
	}
}
