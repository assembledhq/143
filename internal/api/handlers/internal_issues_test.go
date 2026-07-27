package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
)

func newInternalIssueHandler(t *testing.T, mock pgxmock.PgxPoolIface) *InternalIssueHandler {
	t.Helper()
	return NewInternalIssueHandler(
		db.NewIssueStore(mock),
		db.NewSessionStore(mock),
		db.NewJobStore(mock),
		db.NewOrganizationStore(mock),
		"test-secret-32-chars-long-enough",
		zerolog.Nop(),
	)
}

func TestInternalIssueHandler_MissingToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)

	body := `{"title":"test","description":"desc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.Create(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInternalIssueHandler_InvalidToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)

	body := `{"title":"test","description":"desc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer invalid.token")
	rec := httptest.NewRecorder()
	handler.Create(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInternalIssueHandler_AutomationGoalImprovementTokenRejected(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	token, err := auth.GenerateSessionThreadTokenWithClaims(
		handler.signingSecret,
		uuid.New(),
		uuid.New(),
		uuid.New(),
		nil,
		[]string{"automation-goal-improvement:complete"},
		string(models.SessionOriginAutomationGoalImprovement),
		nil,
		5*time.Minute,
	)
	require.NoError(t, err, "automation goal improvement token should be generated")

	body := `{"title":"test","description":"desc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.Create(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, "goal improvement sessions should not be allowed to create issues")
	require.Contains(t, rec.Body.String(), "TOOL_NOT_AVAILABLE", "response should explain the tool is unavailable")
	require.NoError(t, mock.ExpectationsWereMet(), "no database calls should be made")
}

func TestInternalIssueHandler_MissingTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	token := validToken(t, handler.signingSecret)

	body := `{"title":"","description":"desc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.Create(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInternalIssueHandler_MissingDescription(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	token := validToken(t, handler.signingSecret)

	body := `{"title":"test issue","description":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.Create(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInternalIssueHandler_InvalidSeverity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	token := validToken(t, handler.signingSecret)

	body := `{"title":"test","description":"desc","severity":"extreme"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.Create(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInternalIssueHandler_InvalidBody(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	token := validToken(t, handler.signingSecret)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString("{invalid"))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.Create(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInternalIssueHandler_RateLimited(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	token := validToken(t, handler.signingSecret)

	// Exhaust the rate limit.
	tokenHash := hashToken(token)
	for i := 0; i < maxIssuesPerAgentRun; i++ {
		require.True(t, handler.incrementAndCheck(tokenHash))
	}

	body := `{"title":"rate limited","description":"should fail"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.Create(rec, req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestInternalIssueHandler_NonPMCallerStillCreatesAndDispatchesIssue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	token, err := auth.GenerateSessionThreadTokenWithClaims(
		handler.signingSecret,
		orgID,
		repoID,
		uuid.New(),
		nil,
		[]string{"issues:create"},
		string(models.SessionOriginManual),
		nil,
		time.Minute,
	)
	require.NoError(t, err, "ordinary session token should be generated")

	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(sessionAnyArgs(16)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))
	mock.ExpectQuery("SELECT id, name, settings, created_at, updated_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test", []byte(`{}`), now, now))
	expectIssueSessionCreate(mock, sessionID, now)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(sessionAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewBufferString(`{"title":"ordinary work","description":"keep this capability"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.Create(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "non-PM internal issue creation should remain available")
	require.Contains(t, rec.Body.String(), sessionID.String(), "successful creation should return the dispatched session")
	require.NoError(t, mock.ExpectationsWereMet(), "issue, session, link, and run_agent enqueue should all occur")
}

func TestIncrementAndCheck(t *testing.T) {
	t.Parallel()

	handler := &InternalIssueHandler{
		perTokenCount: make(map[string]int),
	}

	tokenHash := "test-hash"
	for i := 0; i < maxIssuesPerAgentRun; i++ {
		require.True(t, handler.incrementAndCheck(tokenHash), "should allow issue %d", i+1)
	}
	require.False(t, handler.incrementAndCheck(tokenHash), "should reject after limit")
}

func TestHashToken(t *testing.T) {
	t.Parallel()

	h1 := hashToken("token-a")
	h2 := hashToken("token-b")
	h3 := hashToken("token-a")

	require.NotEqual(t, h1, h2)
	require.Equal(t, h1, h3)
	require.Len(t, h1, 16)
}

func TestCreateIssueResponse_JSON(t *testing.T) {
	t.Parallel()

	resp := createIssueResponse{ID: "id-1", Title: "title-1"}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	require.Contains(t, string(data), `"id":"id-1"`)
	require.NotContains(t, string(data), "session_id")

	sid := "s-123"
	resp.SessionID = &sid
	data, err = json.Marshal(resp)
	require.NoError(t, err)
	require.Contains(t, string(data), `"session_id":"s-123"`)
}

func validToken(t *testing.T, secret string) string {
	t.Helper()
	token, err := auth.GenerateInternalToken(secret, uuid.New(), uuid.New(), 5*time.Minute)
	require.NoError(t, err)
	return token
}
