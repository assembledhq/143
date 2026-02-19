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
	updateStatusFn func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error
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

func (m *mockCredentialStore) UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error {
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
