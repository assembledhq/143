package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewHandler_GetPolicyReturnsPromptFields(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	config := models.DefaultCodeReviewPolicyConfig()
	config.ReviewInstructions = "team review guidance"
	config.AutomatedApprovalPolicy = "team approval guidance"
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	expectCodeReviewResolvedPolicy(t, mock, orgID, config)
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-review-policies", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.GetPolicy(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "policy GET should succeed")
	var response models.SingleResponse[models.CodeReviewResolvedPolicy]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "policy GET response should be valid JSON")
	require.Equal(t, config.ReviewInstructions, response.Data.Config.ReviewInstructions, "policy GET should return review instructions")
	require.Equal(t, config.AutomatedApprovalPolicy, response.Data.Config.AutomatedApprovalPolicy, "policy GET should return automated approval policy")
}

// Get backs the ?evidence=<session id> deep link used by the dispute queue and
// by every GitHub dispute reply, so it has to answer for reviews the windowed
// list endpoint would never return.
func TestCodeReviewHandler_GetReportsMissingAndMalformedSessions(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	tests := []struct {
		name string
		// emptyResult exercises the "no such review" path with a genuinely empty
		// result set, which is what CollectOneRow turns into pgx.ErrNoRows.
		emptyResult  bool
		malformedID  bool
		storeErr     error
		expectedCode int
		expectedErr  string
	}{
		{name: "malformed session id", malformedID: true, expectedCode: http.StatusBadRequest, expectedErr: "INVALID_ID"},
		{name: "unknown review", emptyResult: true, expectedCode: http.StatusNotFound, expectedErr: "CODE_REVIEW_NOT_FOUND"},
		{
			name: "store failure", storeErr: errors.New("connection refused"),
			expectedCode: http.StatusInternalServerError, expectedErr: "CODE_REVIEW_LOAD_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionID := uuid.New()
			routeID := sessionID.String()
			if tt.malformedID {
				routeID = "not-a-uuid"
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			if !tt.malformedID {
				expectation := mock.ExpectQuery("SELECT[\\s\\S]+FROM code_review_session_metadata").
					WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
				if tt.emptyResult {
					expectation.WillReturnRows(pgxmock.NewRows([]string{"id"}))
				} else {
					expectation.WillReturnError(tt.storeErr)
				}
			}
			handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/"+routeID, nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			routeCtx := chi.NewRouteContext()
			routeCtx.URLParams.Add("id", routeID)
			req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
			rr := httptest.NewRecorder()

			handler.Get(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "body: %s", rr.Body.String())
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "error body should be valid JSON")
			require.Equal(t, tt.expectedErr, body.Error.Code)
			require.NoError(t, mock.ExpectationsWereMet(), "a malformed id must not reach the database")
		})
	}
}

func TestCodeReviewHandler_PromptExamplesReturnsSeparateCollections(t *testing.T) {
	t.Parallel()
	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/prompt-examples", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.PromptExamples(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "prompt examples should be readable by authenticated policy viewers")
	var response models.SingleResponse[models.CodeReviewPromptExamplesResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "prompt example response should be valid JSON")
	require.Equal(t, models.CodeReviewPromptExamples(), response.Data.ReviewInstructions, "response should keep review-instruction examples in their own collection")
	require.Equal(t, models.CodeReviewAutomatedApprovalExamples(), response.Data.AutomatedApprovalPolicies, "response should keep approval examples in their own collection")
}

func TestCodeReviewHandler_PolicyEventValidatesEventName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body string
		expected   int
	}{
		{name: "accepts privacy safe event", body: `{"event":"code_review_policy_viewed","scope":"organization","configured":true}`, expected: http.StatusNoContent},
		{name: "rejects unknown event", body: `{"event":"prompt_contents_here"}`, expected: http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := NewCodeReviewHandler(nil, nil)
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/code-reviews/policy-events", strings.NewReader(tt.body))
			handler.PolicyEvent(rr, req)
			require.Equal(t, tt.expected, rr.Code, "policy event endpoint should accept only bounded event names")
		})
	}
}

func TestCodeReviewHandler_PutPolicyRejectsRepositoryScope(t *testing.T) {
	t.Parallel()
	repositoryID := uuid.New()
	body, err := json.Marshal(map[string]any{"repository_id": repositoryID, "config": models.DefaultCodeReviewPolicyConfig()})
	require.NoError(t, err, "repository-scoped policy request should marshal")
	handler := NewCodeReviewHandler(nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/code-review-policies", bytes.NewReader(body))
	handler.PutPolicy(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "repository-scoped policy writes should be rejected")
	require.Contains(t, rr.Body.String(), "CODE_REVIEW_POLICY_SCOPE_UNSUPPORTED", "repository-scoped writes should explain the organization-wide policy contract")
}

func TestCodeReviewHandler_PutPolicyRejectsEmptyApprovalPolicyWithFieldDetails(t *testing.T) {
	t.Parallel()
	orgID, userID := uuid.New(), uuid.New()
	config := models.DefaultCodeReviewPolicyConfig()
	config.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	config.AutomatedApprovalPolicy = ""
	body, err := json.Marshal(map[string]any{"config": config})
	require.NoError(t, err, "policy request should marshal")
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/code-review-policies", bytes.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.PutPolicy(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "empty approval policy should be rejected in approve mode")
	var response models.ErrorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "invalid policy response should be valid JSON")
	require.Equal(t, "CODE_REVIEW_POLICY_INVALID", response.Error.Code, "invalid prompt should use policy validation code")
	require.Equal(t, map[string]any{"field": "automated_approval_policy"}, response.Error.Details, "invalid prompt should identify its field")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid prompt should fail before database mutation")
}

func TestCodeReviewHandler_PutPolicyRejectsUnknownEditSource(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(map[string]any{"config": models.DefaultCodeReviewPolicyConfig(), "source": "contains prompt text"})
	require.NoError(t, err, "policy request should marshal")
	handler := NewCodeReviewHandler(nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/code-review-policies", bytes.NewReader(body))
	handler.PutPolicy(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "policy writes should accept only bounded privacy-safe edit sources")
	require.Contains(t, rr.Body.String(), "INVALID_SOURCE", "unknown edit source should use the structured source error")
}

func TestCodeReviewHandler_PutPolicyRetainsEachOmittedPromptIndependently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		omittedField string
	}{
		{name: "retains omitted review instructions", omittedField: "review_instructions"},
		{name: "retains omitted automated approval policy", omittedField: "automated_approval_policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, userID, policyID := uuid.New(), uuid.New(), uuid.New()
			current := models.DefaultCodeReviewPolicyConfig()
			current.ReviewInstructions = "persisted review guidance"
			current.AutomatedApprovalPolicy = "persisted approval guidance"
			requested := current
			requested.Enabled = false
			var configMap map[string]any
			rawConfig, err := json.Marshal(requested)
			require.NoError(t, err, "policy config should marshal")
			require.NoError(t, json.Unmarshal(rawConfig, &configMap), "policy config should decode to map")
			delete(configMap, tt.omittedField)
			body, err := json.Marshal(map[string]any{"config": configMap})
			require.NoError(t, err, "compatibility request should marshal")

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			expectCodeReviewResolvedPolicy(t, mock, orgID, current)
			description, risk, roster := marshalCodeReviewPolicyPartsForHandlerTest(t, requested)
			mock.ExpectBegin()
			mock.ExpectExec("pg_advisory_xact_lock").
				WithArgs("code_review_policy:" + orgID.String()).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("SELECT COALESCE").WithArgs(pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(2))
			mock.ExpectExec("UPDATE code_review_policies").WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectQuery("INSERT INTO code_review_policies").WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				current.ReviewInstructions, current.AutomatedApprovalPolicy, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).WillReturnRows(codeReviewPolicyRowsForHandlerTest().AddRow(policyID, orgID, nil, true, 2, requested.Enabled, requested.ApprovalMode, current.ReviewInstructions, current.AutomatedApprovalPolicy, description, risk, roster, requested.InlineCommentLimit, &userID, time.Now().UTC()))
			mock.ExpectCommit()
			handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/code-review-policies", bytes.NewReader(body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.PutPolicy(rr, req)

			require.Equal(t, http.StatusOK, rr.Code, "older client request should remain compatible")
			require.NoError(t, mock.ExpectationsWereMet(), "compatibility update should preserve both prompt values")
		})
	}
}

func expectCodeReviewResolvedPolicy(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, config models.CodeReviewPolicyConfig) {
	t.Helper()
	description, risk, roster := marshalCodeReviewPolicyPartsForHandlerTest(t, config)
	mock.ExpectQuery("FROM code_review_policies").WithArgs(pgxmock.AnyArg()).WillReturnRows(
		codeReviewPolicyRowsForHandlerTest().AddRow(uuid.New(), orgID, nil, true, 1, config.Enabled, config.ApprovalMode, config.ReviewInstructions, config.AutomatedApprovalPolicy, description, risk, roster, config.InlineCommentLimit, nil, time.Now().UTC()),
	)
}

func codeReviewPolicyRowsForHandlerTest() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode", "review_instructions", "automated_approval_policy", "description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at"})
}

func marshalCodeReviewPolicyPartsForHandlerTest(t *testing.T, config models.CodeReviewPolicyConfig) ([]byte, []byte, []byte) {
	t.Helper()
	values := []any{config.DescriptionPolicy, config.RiskPolicy, config.AgentRoster}
	encoded := make([][]byte, len(values))
	for idx, value := range values {
		var err error
		encoded[idx], err = json.Marshal(value)
		require.NoError(t, err, "policy JSON section should marshal")
	}
	return encoded[0], encoded[1], encoded[2]
}

func TestCodeReviewHandler_SetupGitHubTriggerMapsMissingUserAuth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	handler := NewCodeReviewHandler(nil, nil)
	handler.SetGitHubTriggerSetupService(codereviewsvc.NewGitHubTriggerSetupService(
		&codeReviewTriggerHandlerStoreStub{},
		&codeReviewTriggerHandlerRepoStub{repo: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/api"}},
		&codeReviewTriggerHandlerAuthStub{err: ghservice.ErrGitHubAppUserCredentialMissing},
		zerolog.Nop(),
	))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/code-review-github-trigger/setup", bytes.NewBufferString(`{"repository_id":"`+repoID.String()+`"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.SetupGitHubTrigger(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code, "missing GitHub user authorization should return a conflict")
	require.Contains(t, rr.Body.String(), "GITHUB_USER_AUTH_REQUIRED", "response should expose the reconnect error code")
}

func TestCodeReviewHandler_ListRejectsInvalidOutcome(t *testing.T) {
	t.Parallel()

	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?outcome=bogus", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "an invalid outcome filter should return a bad request")
	require.Contains(t, rr.Body.String(), "INVALID_OUTCOME", "the response should identify the invalid outcome filter")
}

func TestCodeReviewHandler_ListRejectsInvalidActivityStatus(t *testing.T) {
	t.Parallel()

	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?activity_status=bogus", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "an invalid activity status should return a bad request")
	require.Contains(t, rr.Body.String(), "INVALID_ACTIVITY_STATUS", "the response should identify the invalid activity status")
}

func TestCodeReviewHandler_ListRejectsInvalidCursor(t *testing.T) {
	t.Parallel()

	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?cursor=not-a-uuid", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "a malformed cursor should return a bad request")
	require.Contains(t, rr.Body.String(), "INVALID_CURSOR", "the response should identify the invalid cursor")
}

func TestCodeReviewHandler_ListReturnsPaginationMetadata(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*m\.status <> 'stale'.*m\.superseded_by_session_id IS NULL`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`(?s)m\.status <> 'stale'.*m\.superseded_by_session_id IS NULL.*ORDER BY m\.created_at DESC, m\.id DESC`).
		WithArgs(pgxmock.AnyArg(), 51).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "phase", "status_code",
			"status_message", "retry_at", "last_error_at", "retryable_failure", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
			"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
			"retry_eligible", "session_title", "repository_name", "github_repo", "github_pr_number",
			"github_pr_url", "pull_request_title", "pull_request_author",
		}))
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?activity_status=current", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "an empty code review page should load successfully")
	require.JSONEq(t, `{"data":[],"meta":{"total_count":0}}`, rr.Body.String(), "the response should include an exact zero total")
	require.NoError(t, mock.ExpectationsWereMet(), "the handler should execute count and page queries")
}

func TestCodeReviewHandler_ListAppliesTimeWindow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectQuery(`SELECT COUNT\(\*\).*m\.created_at >= @created_after`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`m\.created_at >= @created_after`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), 51).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "phase", "status_code", "status_message",
			"retry_at", "last_error_at", "retryable_failure", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
			"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at", "retry_eligible",
			"session_title", "repository_name", "github_repo", "github_pr_number", "github_pr_url",
			"pull_request_title", "pull_request_author",
		}))
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?created_after=2026-06-01T00:00:00Z", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "list should accept an RFC3339 time-window boundary")
	require.NoError(t, mock.ExpectationsWereMet(), "count and rows should share the time-window boundary")
}

func TestCodeReviewHandler_StatsReturnsFilteredAggregates(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repositoryID := uuid.New()
	medianTurnaround := 480.0
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectQuery(`(?s)FROM code_review_session_metadata m.*m\.repository_id = @repository_id.*m\.decision = @decision.*m\.status <> 'stale'.*m\.superseded_by_session_id IS NULL.*m\.status = @status.*m\.acceptable = @acceptable.*LOWER\(COALESCE\(NULLIF\(s\.revision_context->>'pull_request_author', ''\), 'Unknown'\)\) = LOWER\(@author\).*m\.created_at >= @created_after.*pr\.title ILIKE @search`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"reviews_completed",
			"automatically_approved",
			"needs_human_review",
			"median_turnaround_seconds",
		}).AddRow(128, 92, 21, medianTurnaround))
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/code-reviews/stats?repository_id="+repositoryID.String()+
			"&decision=needs_human_review&activity_status=current&status=completed&risk=needs_review&author=Anya&search=auth&created_after=2026-06-01T00:00:00Z",
		nil,
	)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Stats(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "stats should return filtered aggregate metrics")
	var response models.SingleResponse[models.CodeReviewStats]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "stats response should be valid JSON")
	require.Equal(t, models.CodeReviewStats{
		ReviewsCompleted:        128,
		AutomaticallyApproved:   92,
		NeedsHumanReview:        21,
		MedianTurnaroundSeconds: &medianTurnaround,
	}, response.Data, "stats should return all four overview metrics")
	require.NoError(t, mock.ExpectationsWereMet(), "stats should apply all whole-page filters")
}

func TestCodeReviewHandler_StatsRejectsInvalidTimeWindow(t *testing.T) {
	t.Parallel()

	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/stats?created_after=not-a-time", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.Stats(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "stats should reject an invalid time-window boundary")
	require.Contains(t, rr.Body.String(), "INVALID_TIMESTAMP", "invalid time windows should use the shared timestamp error")
}

func TestCodeReviewHandler_AnalyticsReturnsReport(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repositoryID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectQuery(`(?s)WITH first_attempt AS MATERIALIZED.*m\.repository_id = @repository_id.*FROM summary s`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"prs_reviewed", "prs_with_completed_round", "approved_by_143", "not_approved",
			"approved_first_round", "median_rounds_to_approval",
			"prs_with_failed_attempt", "prs_with_stale_attempt",
			"prs_with_change_breakdown", "median_additions", "median_deletions", "prs_with_findings",
			"prs_with_blocking_findings", "total_findings", "needs_human_review",
			"comment_only", "blocked", "approval_not_posted", "approval_rounds", "authors",
			"non_approval_reasons",
		}).AddRow(
			6, 5, 3, 2, 2, 1.0, 1, 0,
			0, -1, -1, 1,
			1, 2, 2, 0, 0, 0,
			[]byte(`[{"bucket":"round_1","prs":2},{"bucket":"round_2","prs":1},{"bucket":"round_3","prs":0},{"bucket":"round_4_plus","prs":0},{"bucket":"not_yet_approved","prs":3}]`),
			[]byte(`[]`), []byte(`[]`),
		))
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/analytics?repository_id="+repositoryID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.Analytics(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "analytics should return the selected report")
	require.JSONEq(t, `{
		"data": {
			"summary": {
				"prs_reviewed": 6,
				"prs_with_completed_round": 5,
				"approved_by_143": 3,
				"not_approved": 2,
				"approved_first_round": 2,
				"median_rounds_to_approval": 1,
				"needs_human_review": 2,
				"comment_only": 0,
				"blocked": 0,
				"approval_not_posted": 0,
				"prs_with_failed_attempt": 1,
				"prs_with_stale_attempt": 0,
				"prs_with_change_breakdown": 0,
				"median_additions": null,
				"median_deletions": null,
				"prs_with_findings": 1,
				"prs_with_blocking_findings": 1,
				"total_findings": 2
			},
			"approval_rounds": [
				{"bucket":"round_1","prs":2},
				{"bucket":"round_2","prs":1},
				{"bucket":"round_3","prs":0},
				{"bucket":"round_4_plus","prs":0},
				{"bucket":"not_yet_approved","prs":3}
			],
			"authors": [],
			"non_approval_reasons": []
		}
	}`, rr.Body.String(), "analytics should return exact nullable metrics and empty breakdowns")
	require.NoError(t, mock.ExpectationsWereMet(), "analytics handler should apply the repository scope to every query")
}

func TestCodeReviewHandler_AnalyticsRejectsInvalidFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		query        string
		expectedCode string
	}{
		{name: "repository", query: "?repository_id=invalid", expectedCode: "INVALID_REPOSITORY_ID"},
		{name: "time window", query: "?created_after=invalid", expectedCode: "INVALID_TIMESTAMP"},
		{name: "author sort", query: "?author_sort_by=unsafe", expectedCode: "INVALID_ANALYTICS_SORT"},
		{name: "author sort order", query: "?author_sort_by=reviews&author_sort_order=sideways", expectedCode: "INVALID_SORT_ORDER"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/analytics"+tt.query, nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
			rr := httptest.NewRecorder()

			handler.Analytics(rr, req)

			require.Equal(t, http.StatusBadRequest, rr.Code, "invalid analytics filters should return a bad request")
			require.Contains(t, rr.Body.String(), tt.expectedCode, "analytics should identify the invalid filter")
		})
	}
}

func TestCodeReviewHandler_ListRejectsCursorOutsideOrganization(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	cursor, err := encodeCodeReviewListCursor(
		uuid.New(),
		db.CodeReviewListFilters{Limit: 50},
		uuid.New(),
		time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		nil,
	)
	require.NoError(t, err, "a cursor for another organization should encode")
	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?cursor="+cursor, nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "a cursor outside the organization should return a bad request")
	require.Contains(t, rr.Body.String(), "INVALID_CURSOR", "the response should identify an inaccessible cursor")
}

func TestCodeReviewListCursorRejectsChangedFilterButAllowsMutableRowChanges(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	running := models.CodeReviewSessionStatusRunning
	filters := db.CodeReviewListFilters{
		Status: &running, Author: "anya", Search: "invoice", Limit: 50,
		SortBy: "repository", SortOrder: "asc",
	}
	id := uuid.New()
	createdAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	repository := "acme/api"
	cursor, err := encodeCodeReviewListCursor(orgID, filters, id, createdAt, &codeReviewSortCursor{Text: &repository})
	require.NoError(t, err, "the cursor should encode immutable ordering values and filter scope")

	decoded, err := decodeCodeReviewListCursor(cursor, orgID, filters)

	require.NoError(t, err, "the same request filters should decode without consulting mutable row state")
	require.Equal(t, id, decoded.ID, "the cursor should preserve the review ID anchor")
	require.Equal(t, createdAt, decoded.CreatedAt, "the cursor should preserve the creation-time anchor")
	require.Equal(t, repository, *decoded.Sort.Text, "the cursor should preserve the backend-sort value")

	changedFilters := filters
	changedFilters.Search = "payments"
	_, err = decodeCodeReviewListCursor(cursor, orgID, changedFilters)
	require.Error(t, err, "a cursor reused with a different filter collection should be rejected")

	current := models.CodeReviewActivityStatusCurrent
	changedFilters = filters
	changedFilters.ActivityStatus = &current
	_, err = decodeCodeReviewListCursor(cursor, orgID, changedFilters)
	require.Error(t, err, "a cursor reused with a different activity status should be rejected")

	changedFilters = filters
	changedFilters.SortOrder = "desc"
	_, err = decodeCodeReviewListCursor(cursor, orgID, changedFilters)
	require.Error(t, err, "a cursor reused with a different backend sort should be rejected")

	changedFilters = filters
	changedFilters.Author = "sam"
	_, err = decodeCodeReviewListCursor(cursor, orgID, changedFilters)
	require.Error(t, err, "a cursor reused with a different author should be rejected")
}

func TestCodeReviewSortCursorRoundTripsEveryListSort(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	completedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	repository := "acme/api"
	approved := models.CodeReviewDecisionApproved
	reviewID := int64(4242)
	item := models.CodeReviewListItem{
		CodeReviewSessionMetadata: models.CodeReviewSessionMetadata{
			ID:          uuid.New(),
			Status:      models.CodeReviewSessionStatusCompleted,
			Decision:    &approved,
			Acceptable:  func() *bool { acceptable := true; return &acceptable }(),
			CompletedAt: &completedAt,
		},
		RepositoryName: &repository,
		GitHubPRNumber: 143,
	}
	item.GitHubReviewID = &reviewID

	tests := []struct {
		sortBy   string
		expected any
	}{
		{sortBy: "pull_request", expected: 143},
		// The label-derived sorts anchor on the rank the ORDER BY assigns, not
		// on the raw column, so a cursor cannot land in the wrong label group.
		{sortBy: "outcome", expected: 0},
		{sortBy: "risk", expected: 0},
		{sortBy: "run_status", expected: 2},
		{sortBy: "repository", expected: repository},
		{sortBy: "completed", expected: completedAt},
	}

	for _, tt := range tests {
		t.Run(tt.sortBy, func(t *testing.T) {
			t.Parallel()

			for _, order := range []string{"asc", "desc"} {
				filters := db.CodeReviewListFilters{Limit: 50, SortBy: tt.sortBy, SortOrder: order}
				sortCursor, err := codeReviewSortCursorForItem(filters.SortBy, item)
				require.NoError(t, err, "an allowlisted sort should produce a cursor anchor")

				raw, err := encodeCodeReviewListCursor(orgID, filters, item.ID, item.CreatedAt.Add(time.Second), sortCursor)
				require.NoError(t, err, "the sort anchor should encode")

				decoded, err := decodeCodeReviewListCursor(raw, orgID, filters)
				require.NoError(t, err, "the sort anchor should decode under the same filters")
				require.NoError(t, applyCodeReviewSortCursor(&filters, decoded.Sort), "the decoded anchor should apply to the query filters")
				require.Equal(t, tt.expected, filters.CursorSortValue, "the applied anchor should keep the value and type the ORDER BY compares against")
				require.False(t, filters.CursorSortNull, "a populated anchor should not select the null partition")
			}
		})
	}
}

func TestCodeReviewSortCursorRejectsIncompleteAndMistypedAnchors(t *testing.T) {
	t.Parallel()

	nullableItem := models.CodeReviewListItem{}
	sortCursor, err := codeReviewSortCursorForItem("completed", nullableItem)
	require.NoError(t, err, "a review that never completed should still anchor")
	require.True(t, sortCursor.Null, "a null sort value should anchor on the null partition")

	_, err = codeReviewSortCursorForItem("unsupported", nullableItem)
	require.Error(t, err, "an unsupported sort should not silently produce an anchor")

	// repositories.full_name is NOT NULL behind an inner join, so a missing name
	// is a broken invariant, not a null partition to page into.
	_, err = codeReviewSortCursorForItem("repository", nullableItem)
	require.Error(t, err, "a repository sort should refuse to anchor on a missing repository name")

	filters := db.CodeReviewListFilters{SortBy: "repository", SortOrder: "asc"}
	require.Error(t,
		applyCodeReviewSortCursor(&filters, &codeReviewSortCursor{Null: true}),
		"a hand-edited null flag should be rejected for a sort that cannot produce one",
	)

	filters = db.CodeReviewListFilters{SortBy: "completed", SortOrder: "asc"}
	require.Error(t, applyCodeReviewSortCursor(&filters, nil), "a sorted request needs a sort anchor")

	// A cursor minted for one sort must not be reinterpreted under another: the
	// scope hash normally blocks this, so this guards the last line of defence.
	repository := "acme/api"
	filters = db.CodeReviewListFilters{SortBy: "completed", SortOrder: "asc"}
	require.Error(t,
		applyCodeReviewSortCursor(&filters, &codeReviewSortCursor{Text: &repository}),
		"a text anchor should not satisfy a timestamp sort",
	)

	filters = db.CodeReviewListFilters{SortBy: "unsupported", SortOrder: "asc"}
	require.Error(t,
		applyCodeReviewSortCursor(&filters, &codeReviewSortCursor{Null: true}),
		"an unsupported sort should be rejected while applying the anchor",
	)

	filters = db.CodeReviewListFilters{}
	require.NoError(t, applyCodeReviewSortCursor(&filters, nil), "the default order needs no sort anchor")
	require.Nil(t, filters.CursorSortValue, "the default order should not set a sort anchor")
}

func TestCodeReviewAnalyticsIgnoresListOnlySortParameters(t *testing.T) {
	t.Parallel()

	// sort_by orders the reviews table; the analytics report has its own
	// ordering, so it must neither honor nor reject the list parameter.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/analytics?sort_by=unsafe&sort_order=sideways", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	filters, ok := parseCodeReviewAnalyticsFilters(rr, req)

	require.True(t, ok, "a list-only sort parameter should not fail the analytics request")
	require.Equal(t, http.StatusOK, rr.Code, "no error should be written for a list-only sort parameter")
	require.Empty(t, filters.AuthorSortBy, "the list sort should not leak into the analytics author ordering")
}

func TestCodeReviewAnalyticsUsesOnlyPRCohortFilters(t *testing.T) {
	t.Parallel()

	repositoryID := uuid.New()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/code-reviews/analytics?repository_id="+repositoryID.String()+
			"&created_after=2026-07-01T00:00:00Z&decision=blocked&outcome=completed_not_approved"+
			"&activity_status=failed&status=stale&risk=needs_review&author=octocat&search=ignored"+
			"&author_sort_by=first_round&author_sort_order=desc",
		nil,
	)
	rr := httptest.NewRecorder()

	filters, ok := parseCodeReviewAnalyticsFilters(rr, req)

	require.True(t, ok, "PR-centric analytics should accept its cohort and author-sort filters")
	require.Equal(t, &repositoryID, filters.RepositoryID, "analytics should preserve the repository cohort filter")
	require.Equal(t, "first_round", filters.AuthorSortBy, "analytics should accept the first-round PR sort")
	require.Equal(t, "desc", filters.AuthorSortOrder, "analytics should preserve the author sort direction")
	require.NotNil(t, filters.CreatedAfter, "analytics should preserve the first-request lower boundary")
	require.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), *filters.CreatedAfter, "analytics should parse the cohort boundary exactly")
}

func TestCodeReviewHandler_Retry(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	replacementID := uuid.New()
	tests := []struct {
		name         string
		pathID       string
		service      *codeReviewRetryServiceStub
		expectedCode int
		expectedBody string
		retryAfter   string
	}{
		{name: "rejects invalid session id", pathID: "invalid", service: &codeReviewRetryServiceStub{}, expectedCode: http.StatusBadRequest, expectedBody: "INVALID_ID"},
		{name: "returns missing review", pathID: sessionID.String(), service: &codeReviewRetryServiceStub{err: pgx.ErrNoRows}, expectedCode: http.StatusNotFound, expectedBody: "CODE_REVIEW_NOT_FOUND"},
		{
			name: "returns retry conflict details", pathID: sessionID.String(),
			service: &codeReviewRetryServiceStub{err: &codereviewsvc.RetryReviewConflictError{
				Code: codereviewsvc.RetryReviewConflictSuperseded, Message: "already replaced",
			}},
			expectedCode: http.StatusConflict, expectedBody: `"reason":"superseded"`,
		},
		{
			name: "maps GitHub rate limits", pathID: sessionID.String(),
			service: &codeReviewRetryServiceStub{err: &ghservice.GitHubAPIError{
				Method: http.MethodGet, Path: "/repos/acme/repo/pulls/42", StatusCode: http.StatusTooManyRequests,
				Header: http.Header{"Retry-After": []string{"60"}},
			}},
			expectedCode: http.StatusTooManyRequests, expectedBody: "GITHUB_RATE_LIMITED", retryAfter: "60",
		},
		{
			name: "accepts a replacement attempt", pathID: sessionID.String(),
			service: &codeReviewRetryServiceStub{result: codereviewsvc.RetryReviewResult{
				PreviousSessionID: sessionID, SessionID: replacementID, MetadataID: uuid.New(), JobID: uuid.New(),
			}},
			expectedCode: http.StatusAccepted, expectedBody: replacementID.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			handler.SetRetryService(tt.service)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/code-reviews/"+tt.pathID+"/retry", nil)
			routeContext := chi.NewRouteContext()
			routeContext.URLParams.Add("id", tt.pathID)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.Retry(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "retry handler should return the expected status")
			require.Contains(t, rr.Body.String(), tt.expectedBody, "retry handler should return the expected response contract")
			require.Equal(t, tt.retryAfter, rr.Header().Get("Retry-After"), "retry handler should expose GitHub's requested wait")
			if tt.pathID == sessionID.String() {
				require.Equal(t, codereviewsvc.RetryReviewInput{OrgID: orgID, SessionID: sessionID}, tt.service.input, "retry handler should preserve org tenancy and session id")
			}
		})
	}
}

func TestCodeReviewHandler_RetryAuditsCompensatedDispatchFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	orgID := uuid.New()
	userID := uuid.New()
	previousSessionID := uuid.New()
	replacementSessionID := uuid.New()
	metadataID := uuid.New()
	handler := NewCodeReviewHandler(nil, nil)
	handler.SetRetryService(&codeReviewRetryServiceStub{
		result: codereviewsvc.RetryReviewResult{
			PreviousSessionID: previousSessionID,
			SessionID:         replacementSessionID,
			MetadataID:        metadataID,
		},
		err: errors.New("queue unavailable"),
	})
	handler.SetAuditEmitter(newAuditEmitterForTest(mock))
	expectAuditInsert(mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/code-reviews/"+previousSessionID.String()+"/retry", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", previousSessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Retry(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "failed dispatch should remain visible to the caller")
	require.NoError(t, mock.ExpectationsWereMet(), "compensated replacement creation should emit an audit row")
}

type codeReviewRetryServiceStub struct {
	input  codereviewsvc.RetryReviewInput
	result codereviewsvc.RetryReviewResult
	err    error
}

func (s *codeReviewRetryServiceStub) RetryReview(_ context.Context, input codereviewsvc.RetryReviewInput) (codereviewsvc.RetryReviewResult, error) {
	s.input = input
	return s.result, s.err
}

type codeReviewTriggerHandlerStoreStub struct{}

func (s *codeReviewTriggerHandlerStoreStub) GetActiveGitHubTrigger(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewGitHubTriggerSetting, error) {
	return models.CodeReviewGitHubTriggerSetting{}, pgx.ErrNoRows
}

func (s *codeReviewTriggerHandlerStoreStub) SaveGitHubTrigger(context.Context, uuid.UUID, db.SaveCodeReviewGitHubTriggerParams) (models.CodeReviewGitHubTriggerSetting, error) {
	return models.CodeReviewGitHubTriggerSetting{}, nil
}

func (s *codeReviewTriggerHandlerStoreStub) DeactivateGitHubTrigger(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) error {
	return nil
}

type codeReviewTriggerHandlerRepoStub struct {
	repo models.Repository
}

func (s *codeReviewTriggerHandlerRepoStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	return s.repo, nil
}

type codeReviewTriggerHandlerAuthStub struct {
	err error
}

func (s *codeReviewTriggerHandlerAuthStub) GetValidCredential(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
	return nil, s.err
}

func TestCodeReviewHandler_streamOrgIDFromRequest(t *testing.T) {
	t.Parallel()

	ctxOrgID := uuid.New()
	userID := uuid.New()
	requestedOrgID := uuid.New()

	tests := []struct {
		name             string
		query            string
		setupMemberships func() *stubPullRequestMembershipStore
		expectedOrgID    uuid.UUID
		expectedErr      error
	}{
		{
			name:          "uses active org when request does not override it",
			expectedOrgID: ctxOrgID,
		},
		{
			name:  "uses requested org when user belongs to it",
			query: "?org_id=" + requestedOrgID.String(),
			setupMemberships: func() *stubPullRequestMembershipStore {
				return &stubPullRequestMembershipStore{
					getFunc: func(_ context.Context, gotUserID, gotOrgID uuid.UUID) (models.OrganizationMembership, error) {
						require.Equal(t, userID, gotUserID, "streamOrgIDFromRequest should validate membership for the current user")
						require.Equal(t, requestedOrgID, gotOrgID, "streamOrgIDFromRequest should validate the explicitly requested org")
						return models.OrganizationMembership{UserID: gotUserID, OrgID: gotOrgID, Role: models.RoleMember}, nil
					},
				}
			},
			expectedOrgID: requestedOrgID,
		},
		{
			name:        "rejects malformed requested org IDs",
			query:       "?org_id=not-a-uuid",
			expectedErr: errCodeReviewStreamOrgInvalid,
		},
		{
			name:  "rejects requested orgs the user is not a member of",
			query: "?org_id=" + requestedOrgID.String(),
			setupMemberships: func() *stubPullRequestMembershipStore {
				return &stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, pgx.ErrNoRows
					},
				}
			},
			expectedErr: errCodeReviewStreamOrgForbidden,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			if tt.setupMemberships != nil {
				handler.SetMembershipStore(tt.setupMemberships())
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/stream"+tt.query, nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), ctxOrgID))
			req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: ctxOrgID}))

			orgID, err := handler.streamOrgIDFromRequest(req)
			if tt.expectedErr != nil {
				require.Error(t, err, "streamOrgIDFromRequest should reject invalid explicit org selections")
				require.True(t, errors.Is(err, tt.expectedErr), "streamOrgIDFromRequest should return the expected error sentinel")
				return
			}

			require.NoError(t, err, "streamOrgIDFromRequest should resolve the stream org without error")
			require.Equal(t, tt.expectedOrgID, orgID, "streamOrgIDFromRequest should resolve the expected org ID")
		})
	}
}

func TestCodeReviewHandler_streamOrgIDFromRequest_AdditionalErrors(t *testing.T) {
	t.Parallel()

	ctxOrgID := uuid.New()
	requestedOrgID := uuid.New()

	tests := []struct {
		name           string
		withUser       bool
		membershipErr  error
		expectedErr    error
		expectedSubstr string
	}{
		{
			name:        "returns unauthorized when explicit org requested without user",
			withUser:    false,
			expectedErr: errCodeReviewStreamUnauthorized,
		},
		{
			name:           "returns config error when membership store missing",
			withUser:       true,
			expectedSubstr: "membership store not configured",
		},
		{
			name:           "returns membership lookup errors",
			withUser:       true,
			membershipErr:  errors.New("db down"),
			expectedSubstr: "db down",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			if tt.membershipErr != nil {
				handler.SetMembershipStore(&stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, tt.membershipErr
					},
				})
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/stream?org_id="+requestedOrgID.String(), nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), ctxOrgID))
			if tt.withUser {
				req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), OrgID: ctxOrgID}))
			}

			_, err := handler.streamOrgIDFromRequest(req)
			require.Error(t, err, "streamOrgIDFromRequest should fail for this scenario")
			if tt.expectedErr != nil {
				require.True(t, errors.Is(err, tt.expectedErr), "streamOrgIDFromRequest should return the expected sentinel error")
			}
			if tt.expectedSubstr != "" {
				require.Contains(t, err.Error(), tt.expectedSubstr, "streamOrgIDFromRequest should preserve the underlying error context")
			}
		})
	}
}
