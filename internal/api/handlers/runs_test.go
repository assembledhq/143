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

func newRunHandler(t *testing.T, mock pgxmock.PgxPoolIface) *RunHandler {
	t.Helper()
	return NewRunHandler(
		db.NewAgentRunStore(mock),
		db.NewAgentRunLogStore(mock),
		db.NewAgentRunQuestionStore(mock),
		db.NewValidationStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
	)
}

// agentRunColumns is the standard column set for agent_runs queries.
var agentRunColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_run_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "pm_approach", "pm_reasoning",
	"project_task_id",
	"created_at",
}

func TestRunHandler_List(t *testing.T) {
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
				mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(agentRunColumns).AddRow(
							runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, &now, &now, nil,
							nil, nil, nil, nil,
							nil, nil, nil, nil, nil,
							nil, nil, nil,
							nil, // project_task_id
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
				mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(agentRunColumns))
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
			handler := newRunHandler(t, mock)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.AgentRun]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of runs")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRunHandler_Get(t *testing.T) {
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
						pgxmock.NewRows(agentRunColumns).AddRow(
							runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
							nil, nil, nil, nil,
							nil, &now, nil, nil,
							nil, nil, nil, nil,
							nil, nil, nil, nil, nil,
							nil, nil, nil,
							nil, // project_task_id
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
			handler := newRunHandler(t, mock)

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

	// Mock agent run create (13 named args)
	runID := uuid.New()
	mock.ExpectQuery("INSERT INTO agent_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
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

	// Mock agent run create (13 named args)
	runID := uuid.New()
	mock.ExpectQuery("INSERT INTO agent_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(runID, now))

	// Mock job enqueue (6 named args)
	jobID := uuid.New()
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
}

func TestRunHandler_TriggerFix(t *testing.T) {
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
			handler := newRunHandler(t, mock)

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

func TestRunHandler_GetValidation_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	validationID := uuid.New()
	now := time.Now()

	handler := newRunHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM validations WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "agent_run_id", "org_id", "status",
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

func TestRunHandler_ListQuestions_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	now := time.Now()

	handler := newRunHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM agent_run_questions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "agent_run_id", "org_id", "question_text", "options", "context",
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

	var resp models.ListResponse[models.AgentRunQuestion]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return one question for the run")
	require.Equal(t, "Which fix approach?", resp.Data[0].QuestionText, "should return the question text")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRunHandler_AnswerQuestion(t *testing.T) {
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
				mock.ExpectExec("UPDATE agent_run_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				// Mock get by ID after answer
				mock.ExpectQuery("SELECT .+ FROM agent_run_questions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "agent_run_id", "org_id", "question_text", "options", "context",
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

			handler := newRunHandler(t, mock)
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

func TestRunHandler_GetPullRequest_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	prID := uuid.New()
	now := time.Now()

	handler := newRunHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "agent_run_id", "org_id", "github_pr_number", "github_pr_url",
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

func TestRunHandler_GetPullRequest_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newRunHandler(t, mock)

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

func TestRunHandler_GetValidation_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newRunHandler(t, mock)

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

func TestRunHandler_ListQuestions_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newRunHandler(t, mock)

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

func TestRunHandler_AnswerQuestion_InvalidQID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	handler := newRunHandler(t, mock)

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

func TestRunHandler_AnswerQuestion_NoUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	handler := newRunHandler(t, mock)

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

func TestRunHandler_TriggerFix_InvalidAutonomyLevel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	handler := newRunHandler(t, mock)

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

func stringPtr(s string) *string {
	return &s
}
