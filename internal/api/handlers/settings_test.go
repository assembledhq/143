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
