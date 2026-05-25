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
