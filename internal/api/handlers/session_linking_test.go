package handlers

import (
	"context"
	"encoding/json"
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

var sessionIssueLinkHandlerColumns = []string{
	"id", "org_id", "session_id", "issue_id", "role",
	"position", "added_by_user_id", "created_at",
	"issue_title", "issue_source", "external_id", "description",
	"repository_id", "issue_status",
	// Migration 102 — Linear workspace slug is left-joined off
	// session_issue_link_provider_state for deep-link rendering.
	"issue_workspace_slug",
	"linear_last_skipped_reason",
	"linear_primary_snapshot",
}

func TestSessionHandler_EnrichSessionLinks(t *testing.T) {
	t.Parallel()

	t.Run("loads linked issues and preserves the effective primary issue", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		handler.SetIssueLinkStore(db.NewSessionIssueLinkStore(mock))
		handler.SetIssueSnapshotStore(db.NewSessionTurnIssueSnapshotStore(mock))

		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		repoID := uuid.New()
		now := time.Now().UTC()
		title := "Fix checkout timeout"
		source := models.IssueSourceLinear

		mock.ExpectQuery("SELECT .+ FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(sessionIssueLinkHandlerColumns).AddRow(
					uuid.New(), orgID, sessionID, issueID, string(models.SessionIssueLinkRolePrimary),
					0, nil, now,
					&title, &source, nil, nil,
					&repoID, nil,
					nil, // issue_workspace_slug — not set on legacy fixtures
					nil, // linear_last_skipped_reason
					nil, // linear_primary_snapshot
				),
			)

		session := models.Session{ID: sessionID, PrimaryIssueID: &issueID}
		handler.enrichSessionLinks(context.Background(), orgID, &session)

		require.Len(t, session.LinkedIssues, 1, "enrichSessionLinks should attach the session's linked issues")
		require.NotNil(t, session.PrimaryIssueID, "enrichSessionLinks should preserve the primary issue id")
		require.Equal(t, issueID, *session.PrimaryIssueID, "enrichSessionLinks should preserve the primary issue id unchanged")
		require.NotNil(t, handler.issueSnapshots, "SetIssueSnapshotStore should store the provided snapshot store")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("swallows link lookup errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		handler.SetIssueLinkStore(db.NewSessionIssueLinkStore(mock))

		orgID := uuid.New()
		sessionID := uuid.New()
		mock.ExpectQuery("SELECT .+ FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(context.Canceled)

		session := models.Session{ID: sessionID}
		handler.enrichSessionLinks(context.Background(), orgID, &session)

		require.Empty(t, session.LinkedIssues, "enrichSessionLinks should leave LinkedIssues untouched when loading fails")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestSessionHandler_TriggerFix_RequiresRepository(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()
	handler := newSessionHandler(t, mock)

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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+issueID.String()+"/trigger", strings.NewReader(`{"agent_type":"codex"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.TriggerFix(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "TriggerFix should reject issues without a repository")
	require.Contains(t, w.Body.String(), "REPOSITORY_REQUIRED", "TriggerFix should return the repository-required error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_EndSession_AddsIssueSnapshotIDToJobPayload(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	snapshotID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetIssueSnapshotStore(db.NewSessionTurnIssueSnapshotStore(mock))

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 2, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil, nil, nil, nil, nil, nil, nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				nil,
				nil,
				now,
			),
		)
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id .+ RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 2, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil, nil, nil, nil, nil, nil, nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				nil,
				nil,
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_turn_issue_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "session_id", "turn_number", "linked_issues", "created_at"}).
				AddRow(snapshotID, orgID, sessionID, 2, []byte(`[]`), now),
		)
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			snapshotKey:     "snapshots/test.tar",
			prCreationState: "queued",
		}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/end", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.EndSession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "EndSession should succeed when snapshot lookup succeeds")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_RetrySession_EnrichesLinks(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetIssueLinkStore(db.NewSessionIssueLinkStore(mock))

	mock.ExpectQuery("SELECT status FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("failed"))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "pending", "semi", "low",
				nil, nil, nil, nil,
				nil, false, nil, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil, nil, nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				nil,
				nil,
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionIssueLinkHandlerColumns).AddRow(
				uuid.New(), orgID, sessionID, issueID, string(models.SessionIssueLinkRolePrimary),
				0, nil, now,
				nil, nil, nil, nil,
				nil, nil,
				nil, // issue_workspace_slug
				nil, // linear_last_skipped_reason
				nil, // linear_primary_snapshot
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/retry", strings.NewReader(`{"mode":"start_over"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.RetrySession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "RetrySession should return the refreshed session")
	require.Contains(t, w.Body.String(), "\"linked_issues\"", "RetrySession should include linked issues after enrichment")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestInternalIssueHandler_DispatchSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	handler := newInternalIssueHandler(t, mock)
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	description := "Investigate the checkout timeout."
	issue := &models.Issue{
		ID:           uuid.New(),
		OrgID:        orgID,
		RepositoryID: &repoID,
		Title:        "Fix checkout timeout",
		Description:  &description,
	}

	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
				AddRow(orgID, "Acme", json.RawMessage(`{}`), now, now),
		)
	expectIssueSessionCreate(mock, sessionID, now)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", nil)
	returnedSessionID, err := handler.dispatchSession(req, orgID, issue)

	require.NoError(t, err, "dispatchSession should create and enqueue the session")
	require.NotNil(t, returnedSessionID, "dispatchSession should return the created session id")
	require.Equal(t, sessionID, *returnedSessionID, "dispatchSession should return the created session id from the store")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
