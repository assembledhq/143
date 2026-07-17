package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

const prHandlerSecret = "test-secret-32-chars-long-enough-xxx"

func newPRHandlerRequest(t *testing.T, token, sessionID string, body map[string]any) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/sessions/"+sessionID+"/pr", &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionID", sessionID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func newPRHandler(mock pgxmock.PgxPoolIface) *InternalPullRequestHandler {
	return NewInternalPullRequestHandler(
		db.NewSessionStore(mock),
		db.NewPullRequestStore(mock),
		db.NewJobStore(mock),
		prHandlerSecret,
		zerolog.Nop(),
	)
}

func TestInternalPullRequestHandler_Create_MissingToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionID := uuid.New()
	req := newPRHandlerRequest(t, "", sessionID.String(), nil)
	rr := httptest.NewRecorder()
	newPRHandler(mock).Create(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
	require.Contains(t, rr.Body.String(), "UNAUTHORIZED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInternalPullRequestHandler_Create_InvalidToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionID := uuid.New()
	req := newPRHandlerRequest(t, "not-a-valid-token", sessionID.String(), nil)
	rr := httptest.NewRecorder()
	newPRHandler(mock).Create(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
	require.Contains(t, rr.Body.String(), "UNAUTHORIZED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInternalPullRequestHandler_Create_SessionScopeMismatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	tokenSession := uuid.New()
	urlSession := uuid.New() // different session

	// Token scoped to tokenSession, but request targets urlSession.
	token, err := auth.GenerateSessionToken(prHandlerSecret, orgID, repoID, tokenSession, 5*time.Minute)
	require.NoError(t, err)

	req := newPRHandlerRequest(t, token, urlSession.String(), nil)
	rr := httptest.NewRecorder()
	newPRHandler(mock).Create(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "SESSION_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInternalPullRequestHandler_Create_RepoScopedTokenRejected(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()

	// Old-style repo-scoped token (no session ID in claims).
	token, err := auth.GenerateInternalToken(prHandlerSecret, orgID, repoID, 5*time.Minute)
	require.NoError(t, err)

	req := newPRHandlerRequest(t, token, sessionID.String(), nil)
	rr := httptest.NewRecorder()
	newPRHandler(mock).Create(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code)
	require.Contains(t, rr.Body.String(), "SESSION_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInternalPullRequestHandler_Create_AutomationGoalImprovementTokenRejected(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionThreadTokenWithClaims(
		prHandlerSecret,
		orgID,
		repoID,
		sessionID,
		nil,
		[]string{"automation-goal-improvement:complete"},
		string(models.SessionOriginAutomationGoalImprovement),
		nil,
		5*time.Minute,
	)
	require.NoError(t, err, "automation goal improvement token should be generated")

	req := newPRHandlerRequest(t, token, sessionID.String(), nil)
	rr := httptest.NewRecorder()
	newPRHandler(mock).Create(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "goal improvement sessions should not be allowed to create PRs")
	require.Contains(t, rr.Body.String(), "TOOL_NOT_AVAILABLE", "response should explain the tool is unavailable")
	require.NoError(t, mock.ExpectationsWereMet(), "no database calls should be made")
}

func TestInternalPullRequestHandler_Create_InvalidAuthorMode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()

	token, err := auth.GenerateSessionToken(prHandlerSecret, orgID, repoID, sessionID, 5*time.Minute)
	require.NoError(t, err)

	req := newPRHandlerRequest(t, token, sessionID.String(), map[string]any{"author_mode": "unknown"})
	rr := httptest.NewRecorder()
	newPRHandler(mock).Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_AUTHOR_MODE")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestInternalPullRequestHandler_Create_CurrentSessionDerivesIdentityFromToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	t.Cleanup(mock.Close)
	orgID, repoID, sessionID := uuid.New(), uuid.New(), uuid.New()
	token, err := auth.GenerateSessionToken(prHandlerSecret, orgID, repoID, sessionID, 5*time.Minute)
	require.NoError(t, err, "session-scoped token should be generated")

	body := bytes.NewBufferString(`{"author_mode":"unknown"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/session/pr", body)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	newPRHandler(mock).Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "current-session route should derive the valid session ID from token claims before body validation")
	require.Contains(t, rr.Body.String(), "INVALID_AUTHOR_MODE", "current-session route should reach body validation without a path session ID")
	require.NoError(t, mock.ExpectationsWereMet(), "current-session identity validation should not query the database for an invalid body")
}
