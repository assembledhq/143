package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
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
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		nil, // llmClient — not needed in unit tests
	)
}

// sessionColumns is the standard column set for sessions queries.
var sessionColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override",
	"created_at",
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
							nil, nil, nil, nil,
							nil, nil, nil, nil, nil,
							nil, nil, nil,
							nil, // project_task_id
							nil, // model_override
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
							nil, nil, nil, nil,
							nil, nil, nil, nil, nil,
							nil, nil, nil,
							nil, // project_task_id
							nil, // model_override
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
				"created_at", "updated_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now,
			),
		)

	// Mock agent run create (14 named args)
	runID := uuid.New()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
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
				"created_at", "updated_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now,
			),
		)

	// Mock org lookup for default agent type.
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
				AddRow(orgID, "Acme", []byte(settings), now, now),
		)

	// Mock agent run create (14 named args)
	runID := uuid.New()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
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
							"created_at", "updated_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now,
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
							"created_at", "updated_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now,
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
				"merged_at", "created_at", "updated_at",
			}).AddRow(
				prID, runID, orgID, 42, "https://github.com/org/repo/pull/42",
				"org/repo", "Fix bug", nil, "open", "pending",
				nil, now, now,
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
				"created_at", "updated_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now,
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
				nil, nil, nil, nil,
				nil, nil, nil, nil, nil,
				nil, nil, nil,
				nil, nil,
				now,
			),
		)

	// Mock log listing.
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "timestamp", "level", "message", "metadata"}).
				AddRow(int64(1), runID, now, "info", "Starting agent", nil).
				AddRow(int64(2), runID, now, "info", "Agent completed", nil),
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
				nil, nil, nil, nil,
				nil, nil, nil, nil, nil,
				nil, nil, nil,
				nil, nil,
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "timestamp", "level", "message", "metadata"}))

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
				nil, nil, nil, nil,
				nil, nil, nil, nil, nil,
				nil, nil, nil,
				nil, nil,
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
				nil, nil, nil, nil,
				nil, nil, nil, nil, nil,
				nil, nil, nil,
				nil, nil,
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "timestamp", "level", "message", "metadata"}).
				AddRow(int64(1), runID, now, "info", "Done", nil),
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

				// Mock issue upsert (16 named args)
				mock.ExpectQuery("INSERT INTO issues").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))

				// Mock session create (14 named args)
				mock.ExpectQuery("INSERT INTO sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

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
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

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
			name:         "returns bad request for invalid agent type",
			body:         `{"message":"Fix bug","agent_type":"invalid"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AGENT_TYPE",
		},
		{
			name:         "returns bad request for invalid autonomy level",
			body:         `{"message":"Fix bug","agent_type":"claude_code","autonomy_level":"chaos"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AUTONOMY_LEVEL",
		},
		{
			name:         "returns bad request for invalid token mode",
			body:         `{"message":"Fix bug","agent_type":"claude_code","token_mode":"extreme"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_TOKEN_MODE",
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

func stringPtr(s string) *string {
	return &s
}
