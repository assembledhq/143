package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/publicationintent"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var sessionChangesetColumns = []string{
	"id", "org_id", "session_id", "is_primary", "order_index", "title", "summary",
	"status", "target_branch", "base_branch", "working_branch", "stacked_on_changeset_id",
	"head_sha", "expected_remote_head_sha", "base_head_sha", "worktree_path", "materialization_error", "materialized_diff",
	"restack_delta_kind", "restack_delta_summary", "restack_confirmation_required", "pr_creation_state", "pr_creation_error", "created_at", "updated_at",
}

func changesetRequest(method, target, sessionID string, changesetID *uuid.UUID, orgID uuid.UUID, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID)
	if changesetID != nil {
		rctx.URLParams.Add("changeset_id", changesetID.String())
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(middleware.WithOrgID(ctx, orgID))
}

func TestPublicationRequestFromContextPreservesCallerProvenance(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	tests := []struct {
		name            string
		ctx             context.Context
		wantSource      models.SessionPublicationSource
		wantTrigger     models.SessionPublicationTriggerKind
		wantRequestedBy *uuid.UUID
		wantRole        string
	}{
		{
			name: "authenticated UI action is user owned",
			ctx: middleware.WithActiveRole(
				middleware.WithUser(context.Background(), &models.User{ID: userID}),
				string(models.RoleMember),
			),
			wantSource: models.SessionPublicationSourceUser, wantTrigger: models.SessionPublicationTriggerExplicitAction,
			wantRequestedBy: &userID, wantRole: string(models.RoleMember),
		},
		{
			name: "internal changeset action remains agent owned",
			ctx: withPublicationRequestProvenance(
				middleware.WithUser(context.Background(), &models.User{ID: userID}),
				models.SessionPublicationSourceAgentTool,
				models.SessionPublicationTriggerExplicitAction,
			),
			wantSource: models.SessionPublicationSourceAgentTool, wantTrigger: models.SessionPublicationTriggerExplicitAction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source, trigger, requestedBy, role := publicationRequestFromContext(tt.ctx)

			require.Equal(t, tt.wantSource, source, "request source should retain the caller channel")
			require.Equal(t, tt.wantTrigger, trigger, "request trigger should retain why publication was requested")
			require.Equal(t, tt.wantRequestedBy, requestedBy, "only user-channel requests should claim a requesting user")
			require.Equal(t, tt.wantRole, role, "only user-channel requests should claim an active role")
		})
	}
}

func TestSessionHandlerCreateChangeset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		sessionID  string
		body       string
		setup      func(pgxmock.PgxPoolIface, uuid.UUID, uuid.UUID)
		wantStatus int
		wantBody   string
	}{
		{name: "rejects invalid session", sessionID: "invalid", body: `{"title":"API"}`, wantStatus: http.StatusBadRequest, wantBody: "INVALID_ID"},
		{name: "requires title", sessionID: uuid.NewString(), body: `{"title":"  "}`, wantStatus: http.StatusBadRequest, wantBody: "TITLE_REQUIRED"},
		{
			name: "creates tenant scoped metadata", sessionID: uuid.NewString(), body: `{"title":" API ","summary":" Endpoints "}`,
			setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now, changesetID := time.Now().UTC(), uuid.New()
				mock.ExpectQuery(`INSERT INTO session_changesets .+ RETURNING`).
					WithArgs(orgID, sessionID, "API", "Endpoints", (*uuid.UUID)(nil)).
					WillReturnRows(pgxmock.NewRows(sessionChangesetColumns).AddRow(
						changesetID, orgID, sessionID, false, 1, "API", "Endpoints", models.ChangesetStatusPlanned,
						"main", "main", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now,
					))
			},
			wantStatus: http.StatusCreated, wantBody: `"title":"API"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create database mock")
			t.Cleanup(mock.Close)
			orgID := uuid.New()
			sessionID, _ := uuid.Parse(tt.sessionID)
			if tt.setup != nil {
				tt.setup(mock, orgID, sessionID)
			}
			h := newSessionHandler(t, mock)
			h.SetChangesetStore(db.NewSessionChangesetStore(mock))
			w := httptest.NewRecorder()
			h.CreateChangeset(w, changesetRequest(http.MethodPost, "/changesets", tt.sessionID, nil, orgID, tt.body))
			require.Equal(t, tt.wantStatus, w.Code, "handler should return expected status")
			require.Contains(t, w.Body.String(), tt.wantBody, "handler should return expected response")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandlerUpdateChangeset(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	title := "API integration"
	mock.ExpectQuery(`UPDATE session_changesets SET.+WHERE org_id = .+ AND session_id = .+ AND id = .+RETURNING`).
		WithArgs(&title, (*string)(nil), orgID, sessionID, changesetID).
		WillReturnRows(pgxmock.NewRows(sessionChangesetColumns).AddRow(
			changesetID, orgID, sessionID, false, 1, title, "Endpoints", models.ChangesetStatusPlanned,
			"main", "main", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now,
		))
	h := newSessionHandler(t, mock)
	h.SetChangesetStore(db.NewSessionChangesetStore(mock))
	w := httptest.NewRecorder()
	h.UpdateChangeset(w, changesetRequest(http.MethodPatch, "/changesets/"+changesetID.String(), sessionID.String(), &changesetID, orgID, `{"title":" API integration "}`))
	require.Equal(t, http.StatusOK, w.Code, "metadata update should succeed")
	require.Contains(t, w.Body.String(), `"title":"API integration"`, "response should contain normalized title")
	require.NoError(t, mock.ExpectationsWereMet(), "update should scope by org, session, and changeset")
}

func TestSessionHandlerPublishChangesetStackUsesCoordinator(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID := uuid.New(), uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM sessions").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(
			sessionID, orgID, models.SessionStatusCompleted, nil, nil, models.SandboxStateSnapshotted, nil, now,
		)...))
	firstBranch, secondBranch := "143/stack-1", "143/stack-2"
	firstWorktree, secondWorktree := "/workspace/stack-1", "/workspace/stack-2"
	firstHead, secondHead := "head-1", "head-2"
	mock.ExpectQuery("SELECT .*FROM session_changesets").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionChangesetColumns).
			AddRow(firstID, orgID, sessionID, true, 0, "First", "", models.ChangesetStatusReady,
				"main", "main", &firstBranch, nil, &firstHead, nil, nil, &firstWorktree, nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now).
			AddRow(secondID, orgID, sessionID, false, 1, "Second", "", models.ChangesetStatusReady,
				firstBranch, firstBranch, &secondBranch, &firstID, &secondHead, nil, nil, &secondWorktree, nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now))
	coordinator := &internalPRCoordinatorStub{result: &publicationintent.PublicationIntentResult{
		Status: publicationintent.ResultPRQueued, SessionID: sessionID,
	}}
	h := newSessionHandler(t, mock)
	h.SetChangesetStore(db.NewSessionChangesetStore(mock))
	h.SetPublicationIntentCoordinator(coordinator, true)
	req := changesetRequest(http.MethodPost, "/sessions/"+sessionID.String()+"/changesets/publish-stack", sessionID.String(), nil, orgID, `{"author_mode":"auto"}`)
	req = req.WithContext(middleware.WithActiveRole(req.Context(), string(models.RoleMember)))
	w := httptest.NewRecorder()

	h.PublishChangesetStack(w, req)

	require.NoError(t, mock.ExpectationsWereMet(), "stack endpoint should consume the scoped session and changeset queries")
	require.Equal(t, http.StatusAccepted, w.Code, "stack publication should return the coordinator's asynchronous contract: %s", w.Body.String())
	require.Contains(t, w.Body.String(), `"status":"queued"`, "stack publication should report coordinated queueing")
	require.Contains(t, w.Body.String(), `"publication_id":`, "stack publication should use the typed snake-case publication response")
	require.Len(t, coordinator.requests, 2, "stack publication should coordinate every active changeset")
	require.Equal(t, firstID, *coordinator.requests[0].ChangesetID, "first stack request should retain changeset order")
	require.Equal(t, secondID, *coordinator.requests[1].ChangesetID, "second stack request should retain changeset order")
	require.Equal(t, models.SessionPublicationSourceUser, coordinator.requests[0].Source, "dashboard stack publication should retain user provenance")
	require.Equal(t, models.SessionPublicationTriggerExplicitAction, coordinator.requests[0].TriggerKind, "dashboard stack publication should retain its explicit trigger")
}

// Aborting mid-stack would report failure for changesets whose durable intent
// is already queued, so a later rejection has to be reported per changeset
// alongside the work that did start.
func TestSessionHandlerPublishChangesetStackReportsPartialRejection(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID := uuid.New(), uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM sessions").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(
			sessionID, orgID, models.SessionStatusCompleted, nil, nil, models.SandboxStateSnapshotted, nil, now,
		)...))
	firstBranch, secondBranch := "143/stack-1", "143/stack-2"
	firstWorktree, secondWorktree := "/workspace/stack-1", "/workspace/stack-2"
	firstHead, secondHead := "head-1", "head-2"
	mock.ExpectQuery("SELECT .*FROM session_changesets").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionChangesetColumns).
			AddRow(firstID, orgID, sessionID, true, 0, "First", "", models.ChangesetStatusReady,
				"main", "main", &firstBranch, nil, &firstHead, nil, nil, &firstWorktree, nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now).
			AddRow(secondID, orgID, sessionID, false, 1, "Second", "", models.ChangesetStatusReady,
				firstBranch, firstBranch, &secondBranch, &firstID, &secondHead, nil, nil, &secondWorktree, nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now))
	coordinator := &internalPRCoordinatorStub{
		result:     &publicationintent.PublicationIntentResult{Status: publicationintent.ResultPRQueued, SessionID: sessionID},
		failOnCall: 2,
		failWith:   &publicationintent.Error{Code: publicationintent.ErrorWorkspaceNotReady, Err: errors.New("worktree is missing")},
	}
	h := newSessionHandler(t, mock)
	h.SetChangesetStore(db.NewSessionChangesetStore(mock))
	h.SetPublicationIntentCoordinator(coordinator, true)
	req := changesetRequest(http.MethodPost, "/sessions/"+sessionID.String()+"/changesets/publish-stack", sessionID.String(), nil, orgID, `{"author_mode":"auto"}`)
	req = req.WithContext(middleware.WithActiveRole(req.Context(), string(models.RoleMember)))
	w := httptest.NewRecorder()

	h.PublishChangesetStack(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "a stack that partially started must not be reported as a failed request: %s", w.Body.String())
	body := w.Body.String()
	require.Contains(t, body, `"status":"rejected"`, "the response should surface the rejected changeset")
	require.Contains(t, body, secondID.String(), "the rejection should name the changeset it belongs to")
	require.Contains(t, body, string(publicationintent.ErrorWorkspaceNotReady), "the rejection should retain the coordinator's typed error code")
	require.Contains(t, body, "This pull request is not ready to publish.", "the rejection should return actionable safe copy")
	require.NotContains(t, body, "worktree is missing", "the response must not expose the coordinator's internal error detail")
	require.Contains(t, body, firstID.String(), "the response should still report the changeset that was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "stack endpoint should consume the scoped session and changeset queries")
}

// The first changeset failing means nothing was coordinated, so the caller
// should still get the ordinary typed error rather than a 202 describing a
// stack that never started.
func TestSessionHandlerPublishChangesetStackRejectsBeforeAnyIntent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID := uuid.New(), uuid.New()
	firstID := uuid.New()
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM sessions").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(
			sessionID, orgID, models.SessionStatusCompleted, nil, nil, models.SandboxStateSnapshotted, nil, now,
		)...))
	firstBranch, firstWorktree, firstHead := "143/stack-1", "/workspace/stack-1", "head-1"
	mock.ExpectQuery("SELECT .*FROM session_changesets").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionChangesetColumns).
			AddRow(firstID, orgID, sessionID, true, 0, "First", "", models.ChangesetStatusReady,
				"main", "main", &firstBranch, nil, &firstHead, nil, nil, &firstWorktree, nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now))
	coordinator := &internalPRCoordinatorStub{
		failOnCall: 1,
		failWith:   &publicationintent.Error{Code: publicationintent.ErrorSessionNotEligible, Err: errors.New("session cannot create a new pull request")},
	}
	h := newSessionHandler(t, mock)
	h.SetChangesetStore(db.NewSessionChangesetStore(mock))
	h.SetPublicationIntentCoordinator(coordinator, true)
	req := changesetRequest(http.MethodPost, "/sessions/"+sessionID.String()+"/changesets/publish-stack", sessionID.String(), nil, orgID, `{"author_mode":"auto"}`)
	req = req.WithContext(middleware.WithActiveRole(req.Context(), string(models.RoleMember)))
	w := httptest.NewRecorder()

	h.PublishChangesetStack(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "an entirely rejected stack should keep the typed conflict: %s", w.Body.String())
	require.Contains(t, w.Body.String(), string(publicationintent.ErrorSessionNotEligible), "the conflict should retain the coordinator's error code")
	require.NoError(t, mock.ExpectationsWereMet(), "stack endpoint should consume the scoped session and changeset queries")
}

func TestSessionHandlerListChangesetsHandlesSessionLookupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		lookupErr  error
		wantStatus int
		wantCode   string
	}{
		{name: "missing session", lookupErr: pgx.ErrNoRows, wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{name: "database failure", lookupErr: fmt.Errorf("connection reset by peer"), wantStatus: http.StatusInternalServerError, wantCode: "SESSION_LOOKUP_FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create database mock")
			t.Cleanup(mock.Close)
			orgID, sessionID := uuid.New(), uuid.New()
			mock.ExpectQuery(`(?s)SELECT .+ FROM sessions`).
				WithArgs(sessionID, orgID).
				WillReturnError(tt.lookupErr)
			h := newSessionHandler(t, mock)
			h.SetChangesetStore(db.NewSessionChangesetStore(mock))
			w := httptest.NewRecorder()
			h.ListChangesets(w, changesetRequest(http.MethodGet, "/changesets", sessionID.String(), nil, orgID, ""))
			require.Equal(t, tt.wantStatus, w.Code, "handler should classify the session lookup error")
			require.Contains(t, w.Body.String(), tt.wantCode, "handler should return the expected API error code")
			require.NoError(t, mock.ExpectationsWereMet(), "session existence lookup should be tenant scoped")
		})
	}
}
