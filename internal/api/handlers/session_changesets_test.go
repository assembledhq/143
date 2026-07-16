package handlers

import (
	"context"
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
