package handlers

import (
	"bytes"
	"context"
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
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func orgColumns() []string {
	return []string{"id", "name", "settings", "created_at", "updated_at"}
}

type testOrgSettingsInvalidator struct {
	called bool
	orgID  uuid.UUID
}

func (i *testOrgSettingsInvalidator) InvalidateOrg(orgID uuid.UUID) {
	i.called = true
	i.orgID = orgID
}

type testStaticEgressWorkerChecker struct {
	available bool
	err       error
}

func (c testStaticEgressWorkerChecker) HasStaticEgressCapableWorker(context.Context, string) (bool, error) {
	return c.available, c.err
}

type testRuntimeStatusSessionCounter struct {
	count int
	err   error
}

func (c testRuntimeStatusSessionCounter) CountRunningByOrg(context.Context, uuid.UUID) (int, error) {
	return c.count, c.err
}

type testRuntimeStatusPreviewCounter struct {
	count int
	err   error
}

func (c testRuntimeStatusPreviewCounter) CountActivePreviewsByOrg(context.Context, uuid.UUID) (int, error) {
	return c.count, c.err
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

func TestSettingsHandler_GetNetworkStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{"sandbox_network":{"static_egress_enabled":true}}`),
				now,
				now,
			),
		)

	handler := NewSettingsHandler(db.NewOrganizationStore(mock), nil)
	handler.SetStaticEgressStatus(StaticEgressStatus{
		Available: true,
		PublicIP:  "203.0.113.10",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/network", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetNetworkStatus(w, req)
	require.Equal(t, http.StatusOK, w.Code, "network status should return success")
	require.Contains(t, w.Body.String(), `"static_egress_available":true`, "network status should report platform availability")
	require.Contains(t, w.Body.String(), `"static_egress_enabled":true`, "network status should report the org setting")
	require.Contains(t, w.Body.String(), `"static_egress_public_ip":"203.0.113.10"`, "network status should expose the customer allowlist IP")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSettingsHandler_GetNetworkStatusRequiresCapableWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{"sandbox_network":{"static_egress_enabled":true}}`),
				now,
				now,
			),
		)

	handler := NewSettingsHandler(db.NewOrganizationStore(mock), nil)
	handler.SetStaticEgressStatus(StaticEgressStatus{
		Available: true,
		PublicIP:  "203.0.113.10",
	})
	handler.SetStaticEgressWorkerChecker(testStaticEgressWorkerChecker{available: false})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/network", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetNetworkStatus(w, req)
	require.Equal(t, http.StatusOK, w.Code, "network status should return success")
	require.Contains(t, w.Body.String(), `"static_egress_available":false`, "network status should be unavailable without capable workers")
	require.Contains(t, w.Body.String(), `"static_egress_unavailable_reason":"static egress is not currently available for new sandbox starts"`, "network status should explain worker availability generically")
	require.Contains(t, w.Body.String(), `"static_egress_public_ip":"203.0.113.10"`, "network status should still expose the configured allowlist IP")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSettingsHandler_GetRuntimeStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{"sandbox_network":{"static_egress_enabled":true},"max_concurrent_runs":5,"preview_max_previews_per_user":4}`),
				now,
				now,
			),
		)

	handler := NewSettingsHandler(db.NewOrganizationStore(mock), nil)
	handler.SetStaticEgressStatus(StaticEgressStatus{
		Available: true,
		PublicIP:  "203.0.113.10",
	})
	handler.SetRuntimeStatusCounters(
		testRuntimeStatusSessionCounter{count: 2},
		testRuntimeStatusPreviewCounter{count: 3},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/runtime/status", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.GetRuntimeStatus(w, req)
	require.Equal(t, http.StatusOK, w.Code, "runtime status should return success")
	require.Contains(t, w.Body.String(), `"static_egress":{"available":true,"enabled":true,"public_ip":"203.0.113.10"}`, "runtime status should include sanitized static egress state")
	require.Contains(t, w.Body.String(), `"capacity":{"state":"normal","active_agent_runs":2,"max_concurrent_agent_runs":5,"active_previews":3,"max_previews_per_user":4}`, "runtime status should include sanitized capacity counts")
	require.NotContains(t, w.Body.String(), "static_egress_unavailable_reason", "runtime status must not expose backend diagnostics")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
			name: "trims organization name before saving",
			body: `{"name":"  Updated Org  "}`,
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
			expectedBody: "Updated Org",
		},
		{
			name: "rejects empty organization name",
			body: `{"name":"   "}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_NAME",
		},
		{
			name: "rejects organization name that is too long",
			body: `{"name":"` + strings.Repeat("a", maxOrgNameLen+1) + `"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "NAME_TOO_LONG",
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
			name: "returns bad request when platform default caps llm_model",
			body: `{"settings":{"llm_model":"gpt-5.4"}}`,
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
			name: "accepts preview capacity within bounds",
			body: `{"settings":{"preview_max_previews_per_user":4}}`,
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
			expectedBody: "preview_max_previews_per_user",
		},
		{
			name: "returns bad request for preview capacity below minimum",
			body: `{"settings":{"preview_max_previews_per_user":-1}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_SETTINGS",
		},
		{
			name: "returns bad request for invalid sandbox resource tier",
			body: `{"settings":{"sandbox_resources":{"agent_default_tier":"xlarge"}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_SETTINGS",
		},
		{
			name: "returns bad request for preview resource cap above platform maximum",
			body: `{"settings":{"sandbox_resources":{"preview_max_memory_mib":99999}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_SETTINGS",
		},
		{
			name: "returns bad request for invalid sandbox lifecycle retention",
			body: `{"settings":{"sandbox_lifecycle":{"completed_session_retention_minutes":99999}}}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				// no DB calls expected
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_SETTINGS",
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
			body: `{"settings":{"pm_model":"claude-sonnet-4-5","agent_config":{"codex":{"OPENAI_MODEL":"gpt-5.3-codex"},"claude_code":{"ANTHROPIC_MODEL":"claude-sonnet-4-5"},"gemini_cli":{"GEMINI_MODEL":"gemini-3.1-pro-preview"}}}}`,
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
			handler := NewSettingsHandler(store, map[string]string{"openai": "sk-...platform"})

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

func TestSettingsHandler_UpdateRejectsNonBooleanAgentTabTools(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store, nil)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`{"settings":{"coding_agent_tab_tools_enabled":"yes"}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "Update should reject non-boolean tab-tool settings")
	require.Contains(t, w.Body.String(), "INVALID_SETTINGS", "response should use a stable settings validation error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSettingsHandler_Update_BlocksCappedPlatformModelWithOrgCredential(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()

	handler := NewSettingsHandler(db.NewOrganizationStore(mock), map[string]string{"openai": "sk-...platform"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`{"settings":{"llm_model":"gpt-5.4"}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "org-owned OpenAI keys should not unlock models while runtime uses platform defaults")
	require.Contains(t, w.Body.String(), "capped", "response should explain the platform default model cap")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSettingsHandler_Update_SkipsNoOpPatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{"default_agent_type":"codex"}`),
				now,
				now,
			),
		)

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`{"settings":{"default_agent_type":"codex"}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return success for a no-op patch")
	require.Contains(t, w.Body.String(), `"default_agent_type":"codex"`, "response should return the existing settings")
	require.NoError(t, mock.ExpectationsWereMet(), "no-op patch should not issue an UPDATE")
}

func TestSettingsHandler_UpdateAllowsStaticEgressEnableWhenUnavailable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{}`),
				now,
				now,
			),
		)
	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
		)

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store, nil)
	handler.SetStaticEgressStatus(StaticEgressStatus{
		Available: true,
		PublicIP:  "203.0.113.10",
	})
	handler.SetStaticEgressWorkerChecker(testStaticEgressWorkerChecker{available: false})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`{"settings":{"sandbox_network":{"static_egress_enabled":true}}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusOK, w.Code, "enabling static egress should succeed even when workers are not currently capable")
	require.Contains(t, w.Body.String(), `"static_egress_enabled":true`, "response should persist the requested static egress setting")
	require.NoError(t, mock.ExpectationsWereMet(), "unavailable static egress should still issue an UPDATE")
}

func TestSettingsHandler_Update_LogsPatchMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{"default_agent_type":"codex"}`),
				now,
				now,
			),
		)
	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
		)

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store, nil)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`{"settings":{"default_agent_type":"claude_code"}}`))
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Referer", "https://app.example.com/settings/agent")
	req.Header.Set("User-Agent", "TestBrowser/1.0")
	req = req.WithContext(logger.WithContext(middleware.WithOrgID(req.Context(), orgID)))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should update settings successfully")
	require.Contains(t, logs.String(), `"message":"settings patch applied"`, "patch updates should be logged")
	require.Contains(t, logs.String(), `"changed_keys":["default_agent_type"]`, "log should include the changed settings key")
	require.Contains(t, logs.String(), `"origin":"https://app.example.com"`, "log should include the request origin")
	require.Contains(t, logs.String(), `"referer":"https://app.example.com/settings/agent"`, "log should include the request referer")
	require.Contains(t, logs.String(), `"user_agent":"TestBrowser/1.0"`, "log should include the request user agent")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSettingsHandler_Update_ReturnsErrorWhenStoreUpdateFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{"default_agent_type":"codex"}`),
				now,
				now,
			),
		)
	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(assertAnError("write failed"))

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`{"settings":{"default_agent_type":"claude_code"}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code, "should return 500 when the organization update fails")
	require.Contains(t, w.Body.String(), "UPDATE_FAILED", "response should surface the update failure code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSettingsHandler_Update_InvalidatesOrgSettingsCacheOnSuccess(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(
				orgID,
				"Test Org",
				json.RawMessage(`{"default_agent_type":"codex"}`),
				now,
				now,
			),
		)
	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).AddRow(now),
		)

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store, nil)
	invalidator := &testOrgSettingsInvalidator{}
	handler.SetOrgSettingsInvalidator(invalidator)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(`{"settings":{"default_agent_type":"claude_code"}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should update settings successfully")
	require.True(t, invalidator.called, "successful updates should invalidate the cached org settings")
	require.Equal(t, orgID, invalidator.orgID, "invalidator should receive the updated org id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMergeSettingsJSON_ShallowKey(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"llm_model":"gpt-5.3","pm_schedule_hours":4}`)
	patch := json.RawMessage(`{"llm_model":"gpt-5.4"}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, "gpt-5.4", parsed["llm_model"])
	// Sibling unrelated key must survive.
	require.EqualValues(t, 4, parsed["pm_schedule_hours"])
}

func TestMergeSettingsJSON_NestedObjectPreservesSiblingProviders(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{
		"agent_config": {
			"codex": {"OPENAI_API_KEY":"sk-old"},
			"claude_code": {"ANTHROPIC_API_KEY":"claude-key"}
		}
	}`)
	// Patch only codex's OPENAI_API_KEY. claude_code must not be wiped.
	patch := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_API_KEY":"sk-new"}}}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)

	var parsed struct {
		AgentConfig map[string]map[string]string `json:"agent_config"`
	}
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, "sk-new", parsed.AgentConfig["codex"]["OPENAI_API_KEY"])
	require.Equal(t, "claude-key", parsed.AgentConfig["claude_code"]["ANTHROPIC_API_KEY"],
		"claude_code provider should not be wiped by a patch to codex")
}

func TestMergeSettingsJSON_NestedObjectPreservesSiblingEnvVars(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_API_KEY":"sk-old","OPENAI_BASE_URL":"https://api.openai.com"}}}`)
	patch := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_API_KEY":"sk-new"}}}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)

	var parsed struct {
		AgentConfig map[string]map[string]string `json:"agent_config"`
	}
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, "sk-new", parsed.AgentConfig["codex"]["OPENAI_API_KEY"])
	require.Equal(t, "https://api.openai.com", parsed.AgentConfig["codex"]["OPENAI_BASE_URL"],
		"sibling env vars inside the same provider should survive")
}

func TestMergeSettingsJSON_ArrayReplaces(t *testing.T) {
	t.Parallel()
	// Arrays must REPLACE, not index-merge — a user removing an element
	// from focus_areas must see the list shrink.
	existing := json.RawMessage(`{"product_context":{"focus_areas":["a","b","c"]}}`)
	patch := json.RawMessage(`{"product_context":{"focus_areas":["a"]}}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)

	var parsed struct {
		ProductContext struct {
			FocusAreas []string `json:"focus_areas"`
		} `json:"product_context"`
	}
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, []string{"a"}, parsed.ProductContext.FocusAreas)
}

func TestMergeSettingsJSON_NestedObjectPreservesSiblingFields(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"product_context":{"philosophy":"old","direction":"keep","focus_areas":["a"]}}`)
	patch := json.RawMessage(`{"product_context":{"philosophy":"new"}}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)

	var parsed struct {
		ProductContext struct {
			Philosophy string   `json:"philosophy"`
			Direction  string   `json:"direction"`
			FocusAreas []string `json:"focus_areas"`
		} `json:"product_context"`
	}
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, "new", parsed.ProductContext.Philosophy)
	require.Equal(t, "keep", parsed.ProductContext.Direction, "direction should survive a philosophy-only patch")
	require.Equal(t, []string{"a"}, parsed.ProductContext.FocusAreas)
}

func TestMergeSettingsJSON_EmptyExisting(t *testing.T) {
	t.Parallel()
	got, err := mergeSettingsJSON(nil, json.RawMessage(`{"llm_model":"gpt-5.4"}`))
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, "gpt-5.4", parsed["llm_model"])
}

func TestMergeSettingsJSON_ScalarReplacesObject(t *testing.T) {
	t.Parallel()
	// Type mismatch: incoming scalar replaces existing object. Should not panic.
	existing := json.RawMessage(`{"llm_model":{"nested":true}}`)
	patch := json.RawMessage(`{"llm_model":"gpt-5.4"}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, "gpt-5.4", parsed["llm_model"])
}

func TestMergeSettingsJSON_NullIncomingValueReplaces(t *testing.T) {
	t.Parallel()
	// Sending { "agent_config": null } should clear the nested object rather
	// than being absorbed silently — this is how callers opt out of a section.
	existing := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_API_KEY":"sk-a"}},"llm_model":"gpt-5.4"}`)
	patch := json.RawMessage(`{"agent_config":null}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Nil(t, parsed["agent_config"])
	// Unrelated siblings are preserved.
	require.Equal(t, "gpt-5.4", parsed["llm_model"])
}

func TestMergeSettingsJSON_RejectsNonObjectPatch(t *testing.T) {
	t.Parallel()
	// Top-level scalars/arrays cannot express a field patch; we reject
	// rather than silently discarding.
	existing := json.RawMessage(`{"llm_model":"gpt-5.4"}`)

	_, err := mergeSettingsJSON(existing, json.RawMessage(`"not-an-object"`))
	require.Error(t, err)

	_, err = mergeSettingsJSON(existing, json.RawMessage(`[1,2,3]`))
	require.Error(t, err)

	_, err = mergeSettingsJSON(existing, json.RawMessage(`null`))
	require.Error(t, err)
}

// Race scenario: a sensitive "Save key" click and a concurrent autosave both
// land server-side in close succession. The two patches hit `mergeSettingsJSON`
// sequentially but each reads its own snapshot of the existing blob — so the
// second patch must not wipe the field the first one just wrote. Deep merge
// at the nested-object level is what makes this safe.
func TestMergeSettingsJSON_SensitiveSaveAndAutosaveInterleaveCleanly(t *testing.T) {
	t.Parallel()
	// Starting state: codex has two sibling env vars; claude_code has a key.
	existing := json.RawMessage(`{
		"agent_config": {
			"codex": {"OPENAI_BASE_URL":"https://api.openai.com"},
			"claude_code": {"ANTHROPIC_API_KEY":"claude-key"}
		},
		"default_agent_type": "codex"
	}`)

	// 1) Sensitive save: user submits an OPENAI_API_KEY. The client sends the
	//    full merged agent_config it has in cache.
	sensitivePatch := json.RawMessage(`{
		"agent_config": {
			"codex": {"OPENAI_BASE_URL":"https://api.openai.com","OPENAI_API_KEY":"sk-secret"},
			"claude_code": {"ANTHROPIC_API_KEY":"claude-key"}
		}
	}`)
	afterSensitive, err := mergeSettingsJSON(existing, sensitivePatch)
	require.NoError(t, err)

	// 2) Autosave: in parallel, the user flipped `default_agent_type`. Because
	//    the client hadn't optimistically applied the sensitive save yet, its
	//    autosave payload doesn't carry OPENAI_API_KEY. The key must survive.
	autosavePatch := json.RawMessage(`{"default_agent_type":"claude_code"}`)
	afterBoth, err := mergeSettingsJSON(afterSensitive, autosavePatch)
	require.NoError(t, err)

	var parsed struct {
		AgentConfig      map[string]map[string]string `json:"agent_config"`
		DefaultAgentType string                       `json:"default_agent_type"`
	}
	require.NoError(t, json.Unmarshal(afterBoth, &parsed))
	require.Equal(t, "sk-secret", parsed.AgentConfig["codex"]["OPENAI_API_KEY"], "sensitive key survives the autosave that followed")
	require.Equal(t, "https://api.openai.com", parsed.AgentConfig["codex"]["OPENAI_BASE_URL"])
	require.Equal(t, "claude-key", parsed.AgentConfig["claude_code"]["ANTHROPIC_API_KEY"])
	require.Equal(t, "claude_code", parsed.DefaultAgentType)

	// Reverse order: autosave lands first, sensitive save follows. The
	// sensitive client sends the agent_config it last saw, which doesn't
	// include the freshly-changed default_agent_type — but because the patch
	// only touches `agent_config`, the server's deep merge preserves the
	// top-level `default_agent_type` update.
	afterAutosave, err := mergeSettingsJSON(existing, autosavePatch)
	require.NoError(t, err)
	afterAutosaveThenSensitive, err := mergeSettingsJSON(afterAutosave, sensitivePatch)
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal(afterAutosaveThenSensitive, &parsed))
	require.Equal(t, "sk-secret", parsed.AgentConfig["codex"]["OPENAI_API_KEY"])
	require.Equal(t, "claude_code", parsed.DefaultAgentType, "autosave's top-level change survives the sensitive save that followed")
}

// Regression check for the UseNumber switch: integer settings values must
// round-trip through mergeSettingsJSON without being promoted to floats.
func TestMergeSettingsJSON_PreservesIntegerEncoding(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"max_concurrent_runs":5,"pm_schedule_hours":4}`)
	patch := json.RawMessage(`{"llm_model":"gpt-5.4"}`)

	got, err := mergeSettingsJSON(existing, patch)
	require.NoError(t, err)
	// Expect the integers to serialize back as integers, not "5.0" / "4.0".
	require.Contains(t, string(got), `"max_concurrent_runs":5`)
	require.Contains(t, string(got), `"pm_schedule_hours":4`)

	// Large integers beyond float64's 2^53 range must survive too. This
	// value (9_007_199_254_740_993) loses precision when funneled through
	// float64 and re-encoded.
	bigExisting := json.RawMessage(`{"big":9007199254740993}`)
	bigGot, err := mergeSettingsJSON(bigExisting, json.RawMessage(`{"other":"x"}`))
	require.NoError(t, err)
	require.Contains(t, string(bigGot), `"big":9007199254740993`)
}

func TestTopLevelSettingsPatchKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      *json.RawMessage
		expected []string
	}{
		{
			name:     "returns nil for nil input",
			raw:      nil,
			expected: nil,
		},
		{
			name: "returns nil for malformed json",
			raw: func() *json.RawMessage {
				raw := json.RawMessage(`{"default_agent_type":`)
				return &raw
			}(),
			expected: nil,
		},
		{
			name: "returns sorted top level keys",
			raw: func() *json.RawMessage {
				raw := json.RawMessage(`{"z":1,"a":{"nested":true},"m":null}`)
				return &raw
			}(),
			expected: []string{"a", "m", "z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, topLevelSettingsPatchKeys(tt.raw), "should return the expected top-level patch keys")
		})
	}
}

func TestSettingsHandler_PlatformLLMProviders(t *testing.T) {
	t.Parallel()
	h := NewSettingsHandler(nil, map[string]string{
		"openai":    "sk-...platform",
		"anthropic": "sk-ant-...platform",
	})
	got := h.platformLLMProviders()
	require.True(t, got["openai"])
	require.True(t, got["anthropic"])
	require.False(t, got["gemini"])
}

func TestSettingsHandler_PlatformLLMProviders_Empty(t *testing.T) {
	t.Parallel()
	h := NewSettingsHandler(nil, nil)
	require.Empty(t, h.platformLLMProviders())
}

func TestSettingsHandler_Update_RejectsCappedModelOnPlatformDefault(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	store := db.NewOrganizationStore(mock)
	// Org has no own credentials; platform default OpenAI key is wired.
	// gpt-5.4 should be rejected before any DB write happens.
	handler := NewSettingsHandler(store, map[string]string{"openai": "sk-...platform"})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings",
		strings.NewReader(`{"settings":{"llm_model":"gpt-5.4"}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_SETTINGS")
	require.Contains(t, w.Body.String(), "default openai key is capped")
	require.NoError(t, mock.ExpectationsWereMet(), "no DB calls should happen on a rejected patch")
}

func TestSettingsHandler_Update_AllowsSafePlatformModelPatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(orgColumns()).AddRow(orgID, "Test Org", json.RawMessage(`{}`), now, now),
		)
	mock.ExpectQuery("UPDATE organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at"}).AddRow(now))

	store := db.NewOrganizationStore(mock)
	handler := NewSettingsHandler(store, map[string]string{"openai": "sk-...platform"})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings",
		strings.NewReader(`{"settings":{"llm_model":"gpt-5.4-mini"}}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	w := httptest.NewRecorder()

	handler.Update(w, req)
	require.Equal(t, http.StatusOK, w.Code, "safe platform model patches should be allowed")
	require.NoError(t, mock.ExpectationsWereMet(), "settings patch should read and update the organization")
}

func assertAnError(msg string) error {
	return &testError{msg: msg}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
