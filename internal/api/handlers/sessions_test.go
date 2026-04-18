package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newSessionHandler(t *testing.T, mock pgxmock.PgxPoolIface) *SessionHandler {
	t.Helper()
	return NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewValidationStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		nil, // llmClient — not needed in unit tests
		zerolog.Nop(),
	)
}

// sessionColumns is the standard column set for sessions queries.
// Must match sessionSelectColumns in session_store.go. Update all inline
// AddRow calls in this file when adding/removing/reordering columns.
var sessionColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "snapshot_key",
	"target_branch", "working_branch", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "team_id", "deleted_at", "created_at",
}

func TestSessionHandler_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns agent runs for org successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil,                      // project_task_id
							nil,                      // model_override
							nil,                      // triggered_by_user_id
							nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name: "returns empty list when no runs exist",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.Session]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of runs")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_List_WithRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ repository_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?repository_id="+repoID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 when filtering by repository_id")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return filtered sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?repository_id=not-a-uuid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid repository_id")
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID", "error code should indicate invalid repository_id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?status=bogus", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid status")
	require.Contains(t, w.Body.String(), "INVALID_STATUS", "error code should indicate invalid status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_CommaSeparatedStatuses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND status = ANY").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				runID, issueID, orgID, "claude-code", "pending", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 0, nil, "none", nil,
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?status=pending,running", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 for comma-separated statuses")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return filtered sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionCursorRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Nanosecond)
	id := uuid.New()

	encoded := encodeSessionCursor(now, id)
	decodedTime, decodedID, err := decodeSessionCursor(encoded)
	require.NoError(t, err)
	require.True(t, now.Equal(decodedTime), "decoded time should match")
	require.Equal(t, id, decodedID, "decoded ID should match")
}

func TestDecodeSessionCursor_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!!invalid!!!"},
		{name: "missing comma", cursor: "bm9jb21tYQ=="},                                                     // "nocomma"
		{name: "bad timestamp", cursor: "YmFkdGltZSwwMTIzNDU2Ny04OWFiLWNkZWYtMDEyMy00NTY3ODlhYmNkZWY="}, // "badtime,..."
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeSessionCursor(tt.cursor)
			require.Error(t, err)
		})
	}
}

func TestSessionHandler_List_WithCursor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	cursorTime := now.Add(-time.Hour)
	cursorID := uuid.New()
	cursor := encodeSessionCursor(cursorTime, cursorID)

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND \\(created_at, id\\) <").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil, nil,
				nil, 0, nil, "none", nil,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?cursor="+cursor, nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 with cursor")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return sessions after cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidCursor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?cursor=invalid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "returns agent run by ID successfully",
			idParam: "", // will be set to a valid UUID in the subtest
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil,                      // project_task_id
							nil,                      // model_override
							nil,                      // triggered_by_user_id
							nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "running",
		},
		{
			name:         "returns bad request for invalid UUID",
			idParam:      "invalid",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+idParam, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// triggerFixIssueMock sets up the common mock for a successful issue lookup,
// agent run creation, and job enqueue for TriggerFix tests.
func triggerFixIssueMock(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
	now := time.Now()
	issueID := uuid.New()

	// Mock issue lookup
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	// Mock agent run create (17 named args)
	runID := uuid.New()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

	// Mock job enqueue (6 named args)
	jobID := uuid.New()
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
}

func triggerFixIssueAndOrgDefaultMock(mock pgxmock.PgxPoolIface, orgID uuid.UUID, defaultAgentType string) {
	issueID := uuid.New()
	now := time.Now()
	settings := fmt.Sprintf(`{"default_agent_type":"%s"}`, defaultAgentType)

	// Mock issue lookup
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	// Mock org lookup for default agent type.
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
				AddRow(orgID, "Acme", []byte(settings), now, now),
		)

	// Mock agent run create (17 named args)
	runID := uuid.New()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

	// Mock job enqueue (6 named args)
	jobID := uuid.New()
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
}

func TestSessionHandler_TriggerFix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "triggers fix with org default agent type when request omits agent_type",
			idParam: "",
			body:    "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				triggerFixIssueAndOrgDefaultMock(mock, orgID, "gemini_cli")
			},
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:    "falls back to codex when org default agent type is missing",
			idParam: "",
			body:    "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				triggerFixIssueAndOrgDefaultMock(mock, orgID, "")
			},
			expectedCode: http.StatusCreated,
			expectedBody: "codex",
		},
		{
			name:         "triggers fix with gemini_cli agent type",
			idParam:      "",
			body:         `{"agent_type":"gemini_cli"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:         "triggers fix with codex agent type",
			idParam:      "",
			body:         `{"agent_type":"codex"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "codex",
		},
		{
			name:         "triggers fix with high token mode",
			idParam:      "",
			body:         `{"agent_type":"codex","token_mode":"high"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "high",
		},
		{
			name:    "rejects invalid agent type",
			idParam: "",
			body:    `{"agent_type":"invalid_agent"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
							"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
							"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
							"created_at", "updated_at", "deleted_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AGENT_TYPE",
		},
		{
			name:    "rejects invalid token mode",
			idParam: "",
			body:    `{"agent_type":"codex","token_mode":"extreme"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
							"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
							"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
							"created_at", "updated_at", "deleted_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_TOKEN_MODE",
		},
		{
			name:         "returns bad request for invalid issue ID",
			idParam:      "bad-id",
			body:         "",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			var bodyReader *strings.Reader
			if tt.body != "" {
				bodyReader = strings.NewReader(tt.body)
			} else {
				bodyReader = strings.NewReader("")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+idParam+"/fix", bodyReader)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.TriggerFix(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_GetValidation_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	validationID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM validations WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "status",
				"direction_check", "correctness_check", "quality_check", "security_scan",
				"regression_test_check", "coverage_delta", "ci_check", "details",
				"started_at", "completed_at", "created_at",
			}).AddRow(
				validationID, runID, orgID, "passed",
				"pass", "pass", "pass", "pass",
				"skipped", nil, "pass", nil,
				&now, &now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/validation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetValidation(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for validation lookup")

	var resp models.SingleResponse[models.Validation]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, "passed", resp.Data.Status, "should return the validation with passed status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_ListQuestions_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM session_questions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "question_text", "options", "context",
				"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
			}).AddRow(
				qID, runID, orgID, "Which fix approach?", nil, nil,
				nil, nil, nil, nil, "pending", now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/questions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListQuestions(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for questions list")

	var resp models.ListResponse[models.SessionQuestion]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return one question for the run")
	require.Equal(t, "Which fix approach?", resp.Data[0].QuestionText, "should return the question text")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_AnswerQuestion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "answers question successfully",
			body: `{"answer": "Option A"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID) {
				now := time.Now()

				// Mock answer update
				mock.ExpectExec("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				// Mock get by ID after answer
				mock.ExpectQuery("SELECT .+ FROM session_questions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "session_id", "org_id", "question_text", "options", "context",
							"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
						}).AddRow(
							qID, runID, orgID, "Which fix?", nil, nil,
							nil, stringPtr("Option A"), &userID, &now, "answered", now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "answered",
		},
		{
			name:         "returns bad request when answer is empty",
			body:         `{"answer": ""}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_ANSWER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			runID := uuid.New()
			qID := uuid.New()
			userID := uuid.New()

			handler := newSessionHandler(t, mock)
			tt.setupMock(mock, orgID, runID, qID, userID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/"+qID.String()+"/answer", strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", runID.String())
			rctx.URLParams.Add("qid", qID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.AnswerQuestion(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_GetPullRequest_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	prID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "github_pr_number", "github_pr_url",
				"github_repo", "title", "body", "status", "review_status",
				"authored_by", "ci_status", "merged_at", "created_at", "updated_at",
			}).AddRow(
				prID, &runID, orgID, 42, "https://github.com/org/repo/pull/42",
				"org/repo", "Fix bug", nil, "open", "pending",
				"app", "", nil, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK")

	var resp models.SingleResponse[models.PullRequest]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 42, resp.Data.GitHubPRNumber, "should return the PR number")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetPullRequest_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_GetValidation_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/validation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetValidation(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_ListQuestions_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/questions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListQuestions(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_AnswerQuestion_InvalidQID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/bad-id/answer", strings.NewReader(`{"answer":"yes"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	rctx.URLParams.Add("qid", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.AnswerQuestion(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid question ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_AnswerQuestion_NoUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/"+qID.String()+"/answer", strings.NewReader(`{"answer":"yes"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	rctx.URLParams.Add("qid", qID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	// No user set in context
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.AnswerQuestion(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "should return 401 when no user in context")
	require.Contains(t, w.Body.String(), "UNAUTHORIZED")
}

func TestSessionHandler_TriggerFix_InvalidAutonomyLevel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+issueID.String()+"/fix", strings.NewReader(`{"agent_type":"codex","autonomy_level":"chaos"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.TriggerFix(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid autonomy level")
	require.Contains(t, w.Body.String(), "INVALID_AUTONOMY_LEVEL")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetLogs_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	// Mock session lookup.
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	// Mock log listing.
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, orgID, nil, now, "info", "Starting agent", nil, nil).
				AddRow(int64(2), runID, orgID, nil, now, "info", "Agent completed", nil, nil),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionLog]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 2, len(resp.Data))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetLogs_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_GetLogs_EmptyLogs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionLog]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 0, len(resp.Data))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_StreamLogs_TerminalRun(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	issueID := uuid.New()

	handler := newSessionHandler(t, mock)

	// Mock session lookup — terminal status triggers GetLogs fallback.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	// GetLogs path: second session lookup + log listing.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, orgID, nil, now, "info", "Done", nil, nil),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_StreamLogs_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_CreateManual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "creates manual session successfully",
			body: `{"message":"Fix the login bug","agent_type":"claude_code"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				runID := uuid.New()
				jobID := uuid.New()

				// Mock org settings lookup
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))

				// Mock issue upsert (16 named args)
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))

				// Mock session create (17 named args)
				mock.ExpectQuery("INSERT INTO sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

				// Mock concurrency check
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				// Mock job enqueue (6 named args)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "claude_code",
		},
		{
			name: "uses org default agent type when not specified",
			body: `{"message":"Fix the login bug"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				runID := uuid.New()
				jobID := uuid.New()

				// Mock org lookup for default agent type.
				mock.ExpectQuery("SELECT .+ FROM organizations").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
							AddRow(orgID, "Acme", []byte(`{"default_agent_type":"gemini_cli"}`), now, now),
					)

				// Mock issue upsert
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))

				// Mock session create
				mock.ExpectQuery("INSERT INTO sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

				// Mock concurrency check
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				// Mock job enqueue
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:         "returns bad request for empty message",
			body:         `{"message":"  ","agent_type":"claude_code"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_MESSAGE",
		},
		{
			name:         "returns bad request for invalid body",
			body:         `{invalid`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BODY",
		},
		{
			name: "returns bad request for invalid agent type",
			body: `{"message":"Fix bug","agent_type":"invalid"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AGENT_TYPE",
		},
		{
			name: "returns bad request for invalid autonomy level",
			body: `{"message":"Fix bug","agent_type":"claude_code","autonomy_level":"chaos"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AUTONOMY_LEVEL",
		},
		{
			name: "returns bad request for invalid token mode",
			body: `{"message":"Fix bug","agent_type":"claude_code","token_mode":"extreme"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_TOKEN_MODE",
		},
		{
			name:         "returns bad request for invalid branch characters",
			body:         `{"message":"Fix bug","agent_type":"claude_code","branch":"main..exploit"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BRANCH",
		},
		{
			name:         "returns bad request for invalid repository_id format",
			body:         `{"message":"Fix bug","agent_type":"claude_code","repository_id":"not-a-uuid"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REPOSITORY_ID",
		},
		{
			name: "returns not found for non-existent repository",
			body: `{"message":"Fix bug","agent_type":"claude_code","repository_id":"` + uuid.New().String() + `"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "platform", "platform_id", "full_name",
						"default_branch", "installed_at", "created_at", "updated_at",
					}))
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "REPOSITORY_NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(tt.body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.CreateManual(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			require.Contains(t, w.Body.String(), tt.expectedBody)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionHandler_EndSession_EnqueuesValidation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, &now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)
	mock.ExpectExec("UPDATE sessions SET status = @status, completed_at = now\\(\\) WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/end", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.EndSession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ending an idle non-manual session should enqueue validation")
	require.Contains(t, w.Body.String(), `"status":"completed"`, "response should return the completed session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_EndSession_ManualSkipsValidation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID, // triggered_by_user_id
				nil, 1, &now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)
	mock.ExpectExec("UPDATE sessions SET status = @status, completed_at = now\\(\\) WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Expect open_pr job instead of validate.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/end", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.EndSession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ending a manual session should skip validation and enqueue open_pr")
	require.Contains(t, w.Body.String(), `"status":"completed"`, "response should return the completed session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBuildManualSessionDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		images   []string
		expected string
	}{
		{
			name:     "message only",
			message:  "Fix the bug",
			images:   nil,
			expected: "Fix the bug",
		},
		{
			name:     "message with images",
			message:  "Fix the bug",
			images:   []string{"https://example.com/img1.png", "https://example.com/img2.png"},
			expected: "Fix the bug\n\n### Attached images\n- https://example.com/img1.png\n- https://example.com/img2.png",
		},
		{
			name:     "empty images slice",
			message:  "Fix the bug",
			images:   []string{},
			expected: "Fix the bug",
		},
		{
			name:     "blank image URLs filtered",
			message:  "Fix the bug",
			images:   []string{"  ", "https://example.com/img.png"},
			expected: "Fix the bug\n\n### Attached images\n- https://example.com/img.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := buildManualSessionDescription(tt.message, tt.images)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestManualSessionTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		expected string
	}{
		{
			name:     "short message",
			message:  "Fix the login bug",
			expected: "Fix the login bug",
		},
		{
			name:     "empty message",
			message:  "",
			expected: "Manual Session",
		},
		{
			name:     "whitespace only",
			message:  "   ",
			expected: "Manual Session",
		},
		{
			name:     "multiline uses first line",
			message:  "Fix the login bug\nMore details here",
			expected: "Fix the login bug",
		},
		{
			name:     "long message truncated",
			message:  strings.Repeat("a", 200),
			expected: strings.Repeat("a", 120) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := manualSessionTitle(tt.message)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestIsValidGitRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref   string
		valid bool
	}{
		{"main", true},
		{"feature/add-auth", true},
		{"fix-123", true},
		{"refs/heads/main", true},
		{"", false},
		{"main..develop", false},
		{"branch~1", false},
		{"branch^2", false},
		{"branch:file", false},
		{"branch name", false},
		{"branch\\path", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, isValidGitRef(tt.ref))
		})
	}
}

// messageColumns is the standard column set for session_messages queries.
var messageColumns = []string{
	"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "token_usage", "created_at",
}

func TestSessionHandler_ListMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns messages for session",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				// Session lookup.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, &now, "snapshotted", nil,
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				// Messages query.
				userID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(messageColumns).
							AddRow(int64(1), sessionID, orgID, nil, &userID, 1, "user", "Hello", nil, nil, now).
							AddRow(int64(2), sessionID, orgID, nil, nil, 1, "assistant", "Hi there", nil, nil, now),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name: "returns empty list for session with no messages",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 0, nil, "none", nil,
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(messageColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID, sessionID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/messages", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.ListMessages(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.SessionMessage]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of messages")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_SendMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "sends message and enqueues continue_session job",
			body: `{"message":"Please add tests"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				// GetByID — session is idle.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, &now, "snapshotted", stringPtr("snapshots/test"),
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				// ClaimIdle succeeds.
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, &now, "snapshotted", stringPtr("snapshots/test"),
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				// Create message.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				// Enqueue job.
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Please add tests",
		},
		{
			name: "sends message to running session without enqueuing job",
			body: `{"message":"Quick note while you work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				// GetByID — session is running.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 2, &now, "running", nil,
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				// Create message — no ClaimIdle, no ClaimForResume, no Enqueue.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Quick note while you work",
		},
		{
			name: "rejects empty message",
			body: `{"message":""}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_MESSAGE",
		},
		{
			name: "rejects when session is not idle or resumable",
			body: `{"message":"More work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				// GetByID — session is pending (not running, not idle, not terminal).
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "pending", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 0, nil, "none", nil,
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				// ClaimIdle fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				// ClaimForResume also fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectedCode: http.StatusConflict,
			expectedBody: "NOT_RESUMABLE",
		},
		{
			name: "rejects message to completed session with destroyed sandbox snapshot",
			body: `{"message":"Continue please"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 3, &now, "destroyed", nil,
							nil, nil, nil, nil, nil,
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusGone,
			expectedBody: "SNAPSHOT_EXPIRED",
		},
		{
			name: "rejects message to idle session with destroyed sandbox snapshot",
			body: `{"message":"Continue please"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 2, &now, "destroyed", nil,
							nil, nil, nil, nil, nil,
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusGone,
			expectedBody: "SNAPSHOT_EXPIRED",
		},
		{
			name: "sends message to completed session via ClaimForResume",
			body: `{"message":"Continue working on this"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				// GetByID — session is completed.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, &now, "snapshotted", stringPtr("snapshots/test"),
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				// ClaimIdle fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				// ClaimForResume succeeds.
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, &now, "snapshotted", stringPtr("snapshots/test"),
							nil, // target_branch
							nil, // working_branch
							nil, // repository_id
							nil, // diff_stats
							nil, // diff_history
							nil, // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil, // automation_run_id
							nil, // team_id
							nil, // deleted_at
							now,
						),
					)
				// Create message.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				// Enqueue job.
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Continue working on this",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			userID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID, sessionID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.SendMessage(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_ListMessages_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/messages", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListMessages(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_SendMessage_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/bad-id/messages", strings.NewReader(`{"message":"test"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.SendMessage(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

// mockLLMClient is a test double for llm.Client.
// The WaitGroup lets the test verify that the handler waits for the LLM call
// to finish before returning a response (i.e. the call is synchronous).
type mockLLMClient struct {
	response string
	err      error
	wg       sync.WaitGroup
}

func (m *mockLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	defer m.wg.Done()
	return m.response, m.err
}

func newMockLLMClient(response string, err error) *mockLLMClient {
	m := &mockLLMClient{response: response, err: err}
	m.wg.Add(1)
	return m
}

func TestSessionHandler_CreateManual_WithLLMTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()

	llmClient := newMockLLMClient("Fix authentication login flow", nil)
	handler := NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewValidationStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		llmClient,
		zerolog.Nop(),
	)

	now := time.Now()
	issueID := uuid.New()
	runID := uuid.New()
	jobID := uuid.New()

	// Mock org settings lookup
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))

	// Mock issue upsert
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))

	// Mock session create
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

	// Mock concurrency check
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Mock job enqueue
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	// Mock UpdateTitle call
	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual",
		strings.NewReader(`{"message":"The login page throws a 500 error when users try to authenticate with SSO","agent_type":"claude_code"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	// WaitGroup confirms the LLM was called synchronously before the response.
	llmClient.wg.Wait()

	require.Equal(t, http.StatusCreated, w.Code)

	var resp models.SingleResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Data.Title)
	require.Equal(t, "Fix authentication login flow", *resp.Data.Title)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreateManual_LLMError_Returns500(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()

	llmClient := newMockLLMClient("", fmt.Errorf("rate limited"))
	handler := NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewValidationStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		llmClient,
		zerolog.Nop(),
	)

	now := time.Now()
	issueID := uuid.New()
	runID := uuid.New()
	jobID := uuid.New()

	// Mock org settings lookup
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))

	// Mock issue upsert
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))

	// Mock session create
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

	// Mock concurrency check
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Mock job enqueue
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	// No UpdateTitle mock — the LLM error means it should never be called.

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual",
		strings.NewReader(`{"message":"Fix the login bug","agent_type":"claude_code"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	// WaitGroup confirms the LLM was called synchronously.
	llmClient.wg.Wait()

	// LLM failure should propagate as a 500 error.
	require.Equal(t, http.StatusInternalServerError, w.Code, "LLM title generation failure should return 500")

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreatePR_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil, // target_branch
				nil, // working_branch
				nil, // repository_id
				nil, // diff_stats
				nil, // diff_history
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	// Mock PR lookup — no existing PR (returns empty result).
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))

	// Mock job enqueue.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "should return 202 Accepted")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should indicate job was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_NoDiff(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Mock session lookup — session has no diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil, // diff is nil
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, nil, "none", nil,
				nil, nil, nil, nil, nil,
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 when session has no diff")
	require.Contains(t, w.Body.String(), "NO_DIFF", "error code should indicate missing diff")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_AlreadyExists(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	prID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, nil, "none", nil,
				nil, nil, nil, nil, nil,
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	// Mock PR lookup - PR already exists.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
				"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
			}).AddRow(
				prID, &sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", (*time.Time)(nil), now, now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should return 409 when PR already exists")
	require.Contains(t, w.Body.String(), "PR_EXISTS", "error code should indicate PR already exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SessionNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Mock session lookup — not found.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "should return 404 when session not found")
	require.Contains(t, w.Body.String(), "NOT_FOUND", "error code should indicate session not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_PRLookupDBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, nil, "none", nil,
				nil, nil, nil, nil, nil,
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	// Mock PR lookup — returns a database error.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "should return 500 on PR lookup DB error")
	require.Contains(t, w.Body.String(), "INTERNAL_ERROR", "error code should indicate internal error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// mockCanceller implements SessionCanceller for testing.
type mockCanceller struct {
	called    bool
	sessionID uuid.UUID
	result    bool
}

func (m *mockCanceller) CancelSession(sessionID uuid.UUID) bool {
	m.called = true
	m.sessionID = sessionID
	return m.result
}

func TestSessionHandler_CancelSession_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, &now, "running", nil,
				nil, nil, nil, nil, nil,
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "cancel should return 202 Accepted")
	require.Contains(t, w.Body.String(), `"status":"running"`, "response should still show running status")
	require.True(t, canceller.called, "canceller should have been called")
	require.Equal(t, sessionID, canceller.sessionID, "canceller should receive correct session ID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CancelSession_NotRunning(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, &now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil, nil, nil, nil, nil,
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "cancelling non-running session should return 409")
	require.Contains(t, w.Body.String(), "NOT_RUNNING")
	require.False(t, canceller.called, "canceller should not be called for non-running sessions")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CancelSession_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/not-a-uuid/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
	require.False(t, canceller.called)
}

func TestSessionHandler_CancelSession_NoCanceller(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	// Don't set canceller — leave it nil.

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(
				sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, &now, "running", nil,
				nil, nil, nil, nil, nil,
				nil, // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil, // automation_run_id
				nil, // team_id
				nil, // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "CANCEL_UNAVAILABLE")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_ArchiveSession(t *testing.T) {
	t.Parallel()

	t.Run("archives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should return 200")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 401 when user is not authenticated", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		sessionID := uuid.New()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, uuid.New())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("returns 404 when session not found", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionHandler_UnarchiveSession(t *testing.T) {
	t.Parallel()

	t.Run("unarchives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "unarchive should return 200")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 404 when session not found or not archived", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 400 for invalid session ID", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/not-a-uuid/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "not-a-uuid")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, uuid.New())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func stringPtr(s string) *string {
	return &s
}
