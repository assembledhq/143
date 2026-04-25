package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sessionreview"
)

var sessionReviewHandlerSessionColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "snapshot_key",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "pr_creation_state", "pr_creation_error", "diff_collected_at", "latest_diff_snapshot_id", "deleted_at", "created_at",
}

func newSessionReviewHandlerSessionRow(sessionID, orgID uuid.UUID, status string, sandboxState string, snapshotKey *string, diff *string, now time.Time) []any {
	primaryIssueID := uuid.New()
	return []any{
		sessionID, &primaryIssueID, orgID, models.SessionOriginManual, models.SessionInteractionModeInteractive, models.SessionValidationPolicyOnTurnComplete, models.AgentTypeClaudeCode, status, "semi", "low",
		nil, nil, nil, nil,
		nil, nil, false, &now, nil, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, diff,
		nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, 0, now, sandboxState, snapshotKey,
		nil, nil, nil, "", "",
		0, 0, "", nil,
		nil, "", "", int64(0), nil,
		"", nil, nil, 0,
		nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, "idle", (*string)(nil), nil, nil, nil, now,
	}
}

func newSessionReviewHandlerForTest(mock pgxmock.PgxPoolIface, reviewModes sessionreview.ReviewModeProvider) *SessionReviewHandler {
	service := sessionreview.NewService(sessionreview.Deps{
		Sessions:        db.NewSessionStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
		Jobs:            db.NewJobStore(mock),
		ReviewModes:     reviewModes,
		Logger:          zerolog.Nop(),
	})
	return NewSessionReviewHandler(service, zerolog.Nop())
}

func sessionReviewRequest(t *testing.T, method string, body []byte, orgID uuid.UUID, user *models.User, sessionID string) *http.Request {
	t.Helper()

	req := httptest.NewRequest(method, fmt.Sprintf("/api/v1/sessions/%s/review", sessionID), bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ctx := middleware.WithOrgID(req.Context(), orgID)
	if user != nil {
		ctx = middleware.WithUser(ctx, user)
	}
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", sessionID)
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
	return req
}

func TestNewSessionReviewHandler(t *testing.T) {
	t.Parallel()

	handler := NewSessionReviewHandler(nil, zerolog.Nop())
	require.NotNil(t, handler, "NewSessionReviewHandler should construct a handler even before wiring an audit emitter")
}

func TestSessionReviewHandler_Capabilities(t *testing.T) {
	t.Parallel()

	t.Run("returns bad request for invalid session id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock.NewPool should create the handler mock")
		defer mock.Close()

		handler := newSessionReviewHandlerForTest(mock, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/not-a-uuid/review-capabilities", nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", "not-a-uuid")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

		w := httptest.NewRecorder()
		handler.Capabilities(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code, "Capabilities should reject malformed session IDs before touching the database")
	})

	t.Run("maps not found and success responses", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name         string
			setup        func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, now time.Time)
			reviewModes  sessionreview.ReviewModeProvider
			expectedCode int
			assertBody   func(t *testing.T, body []byte)
		}{
			{
				name: "returns not found",
				setup: func(mock pgxmock.PgxPoolIface, _, _ uuid.UUID, _ time.Time) {
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnError(pgx.ErrNoRows)
				},
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault}
				},
				expectedCode: http.StatusNotFound,
			},
			{
				name: "returns success payload",
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					diff := "diff --git a/foo b/foo\n"
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusIdle), string(models.SandboxStateSnapshotted), &snapshot, &diff, now)...,
						))
				},
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity}
				},
				expectedCode: http.StatusOK,
				assertBody: func(t *testing.T, body []byte) {
					t.Helper()
					var resp models.SingleResponse[models.SessionReviewCapabilities]
					require.NoError(t, json.Unmarshal(body, &resp), "Capabilities should return JSON on success")
					require.True(t, resp.Data.CanReview, "Capabilities should mark resumable Claude sessions as reviewable")
					require.Equal(t, []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity}, resp.Data.Modes, "Capabilities should echo the adapter's supported review modes")
				},
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "pgxmock.NewPool should create the handler mock")
				defer mock.Close()

				now := time.Now().UTC()
				orgID := uuid.New()
				sessionID := uuid.New()
				tt.setup(mock, orgID, sessionID, now)
				handler := newSessionReviewHandlerForTest(mock, tt.reviewModes)

				req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/sessions/%s/review-capabilities", sessionID), nil)
				req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
				routeCtx := chi.NewRouteContext()
				routeCtx.URLParams.Add("id", sessionID.String())
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

				w := httptest.NewRecorder()
				handler.Capabilities(w, req)

				require.Equal(t, tt.expectedCode, w.Code, "Capabilities should map the service result to the expected HTTP status")
				if tt.assertBody != nil {
					tt.assertBody(t, w.Body.Bytes())
				}
				require.NoError(t, mock.ExpectationsWereMet(), "Capabilities should consume every expected store call")
			})
		}
	})
}

func TestSessionReviewHandler_Start(t *testing.T) {
	t.Parallel()

	t.Run("returns unauthorized when user is missing", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock.NewPool should create the handler mock")
		defer mock.Close()

		handler := newSessionReviewHandlerForTest(mock, nil)
		req := sessionReviewRequest(t, http.MethodPost, nil, uuid.New(), nil, uuid.New().String())
		w := httptest.NewRecorder()

		handler.Start(w, req)

		require.Equal(t, http.StatusUnauthorized, w.Code, "Start should require an authenticated user before attempting a review")
	})

	t.Run("returns bad request for invalid session id and malformed body", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock.NewPool should create the handler mock")
		defer mock.Close()

		orgID := uuid.New()
		user := &models.User{ID: uuid.New(), OrgID: orgID}
		handler := newSessionReviewHandlerForTest(mock, nil)

		invalidIDReq := sessionReviewRequest(t, http.MethodPost, nil, orgID, user, "not-a-uuid")
		invalidIDW := httptest.NewRecorder()
		handler.Start(invalidIDW, invalidIDReq)
		require.Equal(t, http.StatusBadRequest, invalidIDW.Code, "Start should reject malformed session IDs before decoding the request body")

		validID := uuid.New().String()
		invalidBodyReq := sessionReviewRequest(t, http.MethodPost, []byte("{"), orgID, user, validID)
		invalidBodyW := httptest.NewRecorder()
		handler.Start(invalidBodyW, invalidBodyReq)
		require.Equal(t, http.StatusBadRequest, invalidBodyW.Code, "Start should reject malformed JSON payloads")
	})

	t.Run("maps service errors and success", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name         string
			body         []byte
			reviewModes  sessionreview.ReviewModeProvider
			setup        func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID, now time.Time)
			expectedCode int
			assertBody   func(t *testing.T, body []byte)
		}{
			{
				name: "maps session not found",
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault}
				},
				setup: func(mock pgxmock.PgxPoolIface, _, _, _ uuid.UUID, _ time.Time) {
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnError(pgx.ErrNoRows)
				},
				expectedCode: http.StatusNotFound,
			},
			{
				name: "maps snapshot expired",
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault}
				},
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					diff := "diff --git a/foo b/foo\n"
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusIdle), string(models.SandboxStateDestroyed), &snapshot, &diff, now)...,
						))
				},
				expectedCode: http.StatusGone,
			},
			{
				name: "maps no changes",
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault}
				},
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					emptyDiff := ""
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusIdle), string(models.SandboxStateSnapshotted), &snapshot, &emptyDiff, now)...,
						))
				},
				expectedCode: http.StatusConflict,
			},
			{
				name: "maps not resumable",
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault}
				},
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					diff := "diff --git a/foo b/foo\n"
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusSkipped), string(models.SandboxStateSnapshotted), &snapshot, &diff, now)...,
						))
				},
				expectedCode: http.StatusConflict,
			},
			{
				name: "maps unsupported agent",
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return nil
				},
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					diff := "diff --git a/foo b/foo\n"
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusIdle), string(models.SandboxStateSnapshotted), &snapshot, &diff, now)...,
						))
				},
				expectedCode: http.StatusNotImplemented,
			},
			{
				name: "maps unsupported mode",
				body: []byte(`{"mode":"security"}`),
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault}
				},
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					diff := "diff --git a/foo b/foo\n"
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusIdle), string(models.SandboxStateSnapshotted), &snapshot, &diff, now)...,
						))
				},
				expectedCode: http.StatusBadRequest,
			},
			{
				name: "maps internal service error",
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault}
				},
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					diff := "diff --git a/foo b/foo\n"
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusIdle), string(models.SandboxStateSnapshotted), &snapshot, &diff, now)...,
						))
					mock.ExpectBegin().WillReturnError(fmt.Errorf("db unavailable"))
				},
				expectedCode: http.StatusInternalServerError,
			},
			{
				name: "returns accepted on success and emits audit log",
				reviewModes: func(models.AgentType) []models.SessionReviewMode {
					return []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity}
				},
				setup: func(mock pgxmock.PgxPoolIface, orgID, sessionID, _ uuid.UUID, now time.Time) {
					snapshot := "snapshots/session.tar"
					diff := "diff --git a/foo b/foo\n"
					mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusIdle), string(models.SandboxStateSnapshotted), &snapshot, &diff, now)...,
						))
					mock.ExpectBegin()
					mock.ExpectQuery("UPDATE sessions").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnRows(pgxmock.NewRows(sessionReviewHandlerSessionColumns).AddRow(
							newSessionReviewHandlerSessionRow(sessionID, orgID, string(models.SessionStatusRunning), string(models.SandboxStateSnapshotted), &snapshot, &diff, now)...,
						))
					mock.ExpectExec("UPDATE sessions.+SET revision_context").
						WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
						WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					mock.ExpectQuery("INSERT INTO session_messages").
						WithArgs(
							pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
							pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						).
						WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
					mock.ExpectQuery("INSERT INTO jobs").
						WithArgs(pgx.NamedArgs{
							"org_id":     orgID,
							"queue":      "agent",
							"job_type":   "continue_session",
							"payload":    pgxmock.AnyArg(),
							"priority":   5,
							"dedupe_key": (*string)(nil),
						}).
						WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
					mock.ExpectCommit()
					expectAuditInsert(mock)
				},
				expectedCode: http.StatusAccepted,
				assertBody: func(t *testing.T, body []byte) {
					t.Helper()
					var resp models.SingleResponse[models.SessionReviewResponse]
					require.NoError(t, json.Unmarshal(body, &resp), "Start should return a JSON payload for accepted reviews")
					require.Equal(t, models.SessionReviewModeDefault, resp.Data.Mode, "Start should default an empty request body to the default review mode")
				},
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "pgxmock.NewPool should create the handler mock")
				defer mock.Close()

				now := time.Now().UTC()
				orgID := uuid.New()
				sessionID := uuid.New()
				userID := uuid.New()
				tt.setup(mock, orgID, sessionID, userID, now)

				handler := newSessionReviewHandlerForTest(mock, tt.reviewModes)
				if tt.name == "returns accepted on success and emits audit log" {
					handler.SetAuditEmitter(newAuditEmitterForTest(mock))
				}

				req := sessionReviewRequest(t, http.MethodPost, tt.body, orgID, &models.User{ID: userID, OrgID: orgID}, sessionID.String())
				w := httptest.NewRecorder()

				handler.Start(w, req)

				require.Equal(t, tt.expectedCode, w.Code, "Start should map the service outcome to the correct HTTP status")
				if tt.assertBody != nil {
					tt.assertBody(t, w.Body.Bytes())
				}
				require.NoError(t, mock.ExpectationsWereMet(), "Start should satisfy the expected review transaction and audit calls")
			})
		}
	})
}
