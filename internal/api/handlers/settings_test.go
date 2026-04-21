package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func orgColumns() []string {
	return []string{"id", "name", "settings", "created_at", "updated_at"}
}

func TestSettingsHandler_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "returns organization settings successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(orgColumns()).AddRow(
							orgID, "Test Org", json.RawMessage(`{"theme":"dark"}`), now, now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "Test Org",
		},
		{
			name: "returns not found when organization does not exist",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(orgColumns()))
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewOrganizationStore(mock)
			handler := NewSettingsHandler(store, nil)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSettingsHandler_GetLLMDefaults(t *testing.T) {
	t.Parallel()

	defaults := map[string]string{
		"anthropic": "sk-a••••test",
	}
	handler := NewSettingsHandler(nil, defaults)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/llm-defaults", nil)
	w := httptest.NewRecorder()

	handler.GetLLMDefaults(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK")
	require.Contains(t, w.Body.String(), "anthropic", "response should contain provider name")
	require.Contains(t, w.Body.String(), "sk-a", "response should contain masked key prefix")
}

func TestSettingsHandler_GetLLMDefaults_Empty(t *testing.T) {
	t.Parallel()

	handler := NewSettingsHandler(nil, map[string]string{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/llm-defaults", nil)
	w := httptest.NewRecorder()

	handler.GetLLMDefaults(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	dataMap, ok := resp["data"].(map[string]any)
	require.True(t, ok, "data should be a map")
	require.Empty(t, dataMap, "should return empty map when no providers configured")
}

func TestSettingsHandler_GetLLMModels(t *testing.T) {
	t.Parallel()

	handler := NewSettingsHandler(nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/llm-models", nil)
	w := httptest.NewRecorder()

	handler.GetLLMModels(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK")
	require.Contains(t, w.Body.String(), "anthropic", "response should contain anthropic provider")
	require.Contains(t, w.Body.String(), "openai", "response should contain openai provider")
	require.Contains(t, w.Body.String(), "claude-sonnet-4-6", "response should contain Claude model")
	require.Contains(t, w.Body.String(), "gpt-5.4-mini", "response should contain OpenAI model")
}

func TestSettingsHandler_Update(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "updates organization settings successfully",
			body: `{"name":"Updated Org"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				// GetByID
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(orgColumns()).AddRow(
							orgID, "Test Org", json.RawMessage(`{}`), now, now,
						),
					)
				// Update
				mock.ExpectQuery("UPDATE organizations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "Updated Org",
		},
		{
			name: "returns bad request for invalid JSON body",
			body: `not json`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_JSON",
		},
		{
			name: "returns bad request for invalid pm_model",
			body: `{"settings":{"pm_model":"bad-model"}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_SETTINGS",
		},
		{
			name: "returns bad request for invalid llm_model",
			body: `{"settings":{"llm_model":"bad-model"}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_SETTINGS",
		},
		{
			name: "accepts valid llm_model",
			body: `{"settings":{"llm_model":"gpt-5.4-mini"}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(orgColumns()).AddRow(
							orgID, "Test Org", json.RawMessage(`{}`), now, now,
						),
					)
				mock.ExpectQuery("UPDATE organizations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "gpt-5.4-mini",
		},
		{
			name: "returns bad request for invalid codex model in agent_config",
			body: `{"settings":{"agent_config":{"codex":{"OPENAI_MODEL":"not-a-model"}}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_SETTINGS",
		},
		{
			name: "accepts provider model as pm_model",
			body: `{"settings":{"pm_model":"claude-sonnet-4-5"}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(orgColumns()).AddRow(
							orgID, "Test Org", json.RawMessage(`{}`), now, now,
						),
					)
				mock.ExpectQuery("UPDATE organizations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "claude-sonnet-4-5",
		},
		{
			name: "updates successfully with supported models",
			body: `{"settings":{"pm_model":"sonnet","agent_config":{"codex":{"OPENAI_MODEL":"gpt-5.3-codex"},"claude_code":{"ANTHROPIC_MODEL":"claude-sonnet-4-5"},"gemini_cli":{"GEMINI_MODEL":"gemini-3.1-pro-preview"}}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(orgColumns()).AddRow(
							orgID, "Test Org", json.RawMessage(`{}`), now, now,
						),
					)
				mock.ExpectQuery("UPDATE organizations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "gpt-5.3-codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			store := db.NewOrganizationStore(mock)
			handler := NewSettingsHandler(store, nil)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(tt.body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Update(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
