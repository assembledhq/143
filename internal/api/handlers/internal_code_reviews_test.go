package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type stubInternalCodeReviewSessions struct {
	session models.Session
	err     error
}

func (s stubInternalCodeReviewSessions) GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if s.err != nil {
		return models.Session{}, s.err
	}
	return s.session, nil
}

type internalCodeReviewFixture struct {
	orgID     uuid.UUID
	repoID    uuid.UUID
	sessionID uuid.UUID
	token     string
	handler   *InternalCodeReviewHandler
	mock      pgxmock.PgxPoolIface
}

func newInternalCodeReviewFixture(t *testing.T, capabilities ...models.AgentCapabilityID) internalCodeReviewFixture {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	t.Cleanup(mock.Close)

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	secret := "test-secret-32-chars-long-enough"

	snapshot := make([]models.AgentCapabilitySnapshotItem, 0, len(capabilities))
	for _, id := range capabilities {
		snapshot = append(snapshot, models.AgentCapabilitySnapshotItem{ID: id, AccessLevel: models.AgentCapabilityAccessRead})
	}
	sessions := stubInternalCodeReviewSessions{session: models.Session{
		ID:                 sessionID,
		OrgID:              orgID,
		RepositoryID:       &repoID,
		CapabilitySnapshot: snapshot,
	}}

	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, 5*time.Minute)
	require.NoError(t, err, "session token should be generated")

	return internalCodeReviewFixture{
		orgID:     orgID,
		repoID:    repoID,
		sessionID: sessionID,
		token:     token,
		handler:   NewInternalCodeReviewHandler(db.NewCodeReviewStore(mock), sessions, secret),
		mock:      mock,
	}
}

var internalCodeReviewListColumns = []string{
	"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
	"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
	"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
	"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at", "session_title",
	"repository_name", "github_repo", "github_pr_number", "github_pr_url", "pull_request_title", "pull_request_author",
}

func internalCodeReviewListRow(fx internalCodeReviewFixture, reviewID, reviewSessionID uuid.UUID, body *string) []any {
	decision := models.CodeReviewDecisionBlocked
	acceptable := false
	title := "Code review for acme/repo#42"
	repoName := "acme/repo"
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	return []any{
		reviewID, fx.orgID, reviewSessionID, fx.repoID, uuid.New(), uuid.New(),
		"base", "head", false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusCompleted, &decision, &acceptable, false,
		nil, "key-" + reviewID.String(), nil, nil, nil, body, nil, &now, now, &title,
		&repoName, "acme/repo", 42, "https://github.com/acme/repo/pull/42", "Fix auth bug", "devin",
	}
}

func TestInternalCodeReviewHandler_MissingToken(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilityReviewFeedback)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews", nil)
	rec := httptest.NewRecorder()
	fx.handler.List(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "missing token should be rejected")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "no database calls should be made")
}

func TestInternalCodeReviewHandler_CapabilityDenied(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilitySessionHistory)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token)
	rec := httptest.NewRecorder()
	fx.handler.List(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code, "sessions without review_feedback should be denied")
	require.Contains(t, rec.Body.String(), "CAPABILITY_DENIED", "denial should name the capability gate")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "no database calls should be made")
}

func TestInternalCodeReviewHandler_ListScopesToSessionRepository(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilityReviewFeedback)
	reviewID := uuid.New()
	reviewSessionID := uuid.New()
	body := "143 Code Reviewer blocked this PR"

	// Named args: org_id, limit, repository_id, decision.
	fx.mock.ExpectQuery(`m\.repository_id = @repository_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(internalCodeReviewListColumns).
			AddRow(internalCodeReviewListRow(fx, reviewID, reviewSessionID, &body)...))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews?decision=blocked&limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token)
	rec := httptest.NewRecorder()
	fx.handler.List(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "list should succeed: %s", rec.Body.String())
	var resp struct {
		Data []map[string]any `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "list response should be JSON")
	require.Len(t, resp.Data, 1, "list should return the mocked review")
	require.Equal(t, reviewSessionID.String(), resp.Data[0]["session_id"], "list rows should carry the review session ID")
	require.Equal(t, "blocked", resp.Data[0]["decision"], "list rows should carry the decision")
	require.Equal(t, float64(42), resp.Data[0]["github_pr_number"], "list rows should carry PR context")
	require.NotContains(t, resp.Data[0], "final_review_body", "list rows should omit the full review body")
	require.Equal(t, reviewID.String(), resp.Meta.NextCursor, "a full page should return the last row ID as cursor")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "list should query with the repository filter")
}

func TestInternalCodeReviewHandler_ListRejectsInvalidDecision(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilityReviewFeedback)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews?decision=maybe", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token)
	rec := httptest.NewRecorder()
	fx.handler.List(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, "invalid decision should be rejected")
	require.Contains(t, rec.Body.String(), "INVALID_DECISION", "error should name the invalid filter")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "no database calls should be made")
}

func TestInternalCodeReviewHandler_GetReturnsFindingsAndAgentResults(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilityReviewFeedback)
	reviewSessionID := uuid.New()
	body := "143 Code Reviewer blocked this PR"
	findingID := uuid.New()
	agentResultID := uuid.New()
	commentID := int64(9001)
	path := "internal/api/router.go"
	rawOutput := strings.Repeat("x", 40)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	fx.mock.ExpectQuery(`m\.session_id = @session_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(internalCodeReviewListColumns).
			AddRow(internalCodeReviewListRow(fx, uuid.New(), reviewSessionID, &body)...))
	fx.mock.ExpectQuery("FROM code_review_findings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "agent_result_id", "dedupe_key", "severity",
			"confidence", "path", "start_line", "end_line", "summary", "body", "selected_for_inline", "github_comment_id", "created_at",
		}).AddRow(
			findingID, fx.orgID, reviewSessionID, &agentResultID, "dedupe-1", models.CodeReviewFindingSeverityHigh,
			models.CodeReviewFindingConfidenceHigh, &path, internalCodeReviewIntPtr(10), internalCodeReviewIntPtr(12), "Missing org filter", "The query drops org_id.", true, &commentID, now,
		))
	fx.mock.ExpectQuery("FROM code_review_agent_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "agent_provider", "agent_model", "role", "status", "raw_output", "structured_result", "created_at",
		}).AddRow(
			agentResultID, fx.orgID, reviewSessionID, "claude_code", internalCodeReviewStrPtr("claude-sonnet-5"), models.CodeReviewAgentRoleReviewer,
			models.CodeReviewAgentResultStatusCompleted, &rawOutput, json.RawMessage(`{"verdict":"blocked"}`), now,
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews/"+reviewSessionID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+fx.token)
	req = withChiURLParam(req, "session_id", reviewSessionID.String())
	rec := httptest.NewRecorder()
	fx.handler.Get(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "get should succeed: %s", rec.Body.String())
	var resp struct {
		Data struct {
			FinalReviewBody string `json:"final_review_body"`
			Findings        []struct {
				Summary        string `json:"summary"`
				PostedToGitHub bool   `json:"posted_to_github"`
			} `json:"findings"`
			AgentResults []struct {
				AgentProvider    string          `json:"agent_provider"`
				StructuredResult json.RawMessage `json:"structured_result"`
				RawOutput        *string         `json:"raw_output"`
				RawOutputRunes   int             `json:"raw_output_runes"`
			} `json:"agent_results"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "get response should be JSON")
	require.Equal(t, body, resp.Data.FinalReviewBody, "detail should include the posted review body")
	require.Len(t, resp.Data.Findings, 1, "detail should include findings")
	require.True(t, resp.Data.Findings[0].PostedToGitHub, "finding with a GitHub comment should be marked posted")
	require.Len(t, resp.Data.AgentResults, 1, "detail should include agent results")
	require.JSONEq(t, `{"verdict":"blocked"}`, string(resp.Data.AgentResults[0].StructuredResult), "detail should include structured verdicts")
	require.Nil(t, resp.Data.AgentResults[0].RawOutput, "raw output should be omitted unless requested")
	require.Equal(t, len(rawOutput), resp.Data.AgentResults[0].RawOutputRunes, "detail should report raw output size")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "get should load review, findings, and agent results")
}

func TestInternalCodeReviewHandler_GetHidesOtherRepositories(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilityReviewFeedback)
	reviewSessionID := uuid.New()
	row := internalCodeReviewListRow(fx, uuid.New(), reviewSessionID, nil)
	row[3] = uuid.New() // repository_id differs from the token's repo

	fx.mock.ExpectQuery(`m\.session_id = @session_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(internalCodeReviewListColumns).AddRow(row...))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews/"+reviewSessionID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+fx.token)
	req = withChiURLParam(req, "session_id", reviewSessionID.String())
	rec := httptest.NewRecorder()
	fx.handler.Get(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, "reviews in sibling repositories should be invisible")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "only the review lookup should run")
}

func TestInternalCodeReviewHandler_PolicyFallsBackToDefault(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilityReviewFeedback)
	fx.mock.ExpectQuery("FROM code_review_policies").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"review_instructions", "automated_approval_policy", "description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at",
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews/policy", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token)
	rec := httptest.NewRecorder()
	fx.handler.Policy(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "policy should resolve: %s", rec.Body.String())
	var resp struct {
		Data struct {
			Source string `json:"source"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "policy response should be JSON")
	require.Equal(t, "default", resp.Data.Source, "orgs without a saved policy should resolve the default config")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "policy should query the active org policy")
}

func TestInternalCodeReviewHandler_PolicyByIDReturnsHistoricalVersion(t *testing.T) {
	t.Parallel()

	fx := newInternalCodeReviewFixture(t, models.AgentCapabilityReviewFeedback)
	policyID := uuid.New()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	fx.mock.ExpectQuery("FROM code_review_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"review_instructions", "automated_approval_policy", "description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at",
		}).AddRow(
			policyID, fx.orgID, nil, false, 3, true, models.CodeReviewApprovalModeApproveAcceptable,
			"Focus on tenancy bugs", "Approve doc-only changes", []byte(`{}`), []byte(`{}`), []byte(`{}`), 5, nil, now,
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/code-reviews/policies/"+policyID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+fx.token)
	req = withChiURLParam(req, "policy_id", policyID.String())
	rec := httptest.NewRecorder()
	fx.handler.PolicyByID(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "policy version lookup should succeed: %s", rec.Body.String())
	var resp struct {
		Data struct {
			Version            int    `json:"version"`
			ReviewInstructions string `json:"review_instructions"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "policy version response should be JSON")
	require.Equal(t, 3, resp.Data.Version, "historical policy version should be returned")
	require.Equal(t, "Focus on tenancy bugs", resp.Data.ReviewInstructions, "historical review instructions should be returned")
	require.NoError(t, fx.mock.ExpectationsWereMet(), "policy version should be loaded by ID")
}

func withChiURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func internalCodeReviewIntPtr(v int) *int       { return &v }
func internalCodeReviewStrPtr(v string) *string { return &v }
