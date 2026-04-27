package sessionreview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestSessionReviewReadiness(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/a b/a\n"
	emptyDiff := ""
	snapshot := "snapshots/foo.tar"

	cases := []struct {
		name      string
		session   models.Session
		wantReady bool
		wantHint  string
	}{
		{
			name: "idle session with diff and snapshot is ready",
			session: models.Session{
				Status:       string(models.SessionStatusIdle),
				SandboxState: string(models.SandboxStateSnapshotted),
				SnapshotKey:  &snapshot,
				Diff:         &diff,
			},
			wantReady: true,
		},
		{
			name: "completed session is resumable",
			session: models.Session{
				Status:       string(models.SessionStatusCompleted),
				SandboxState: string(models.SandboxStateSnapshotted),
				SnapshotKey:  &snapshot,
				Diff:         &diff,
			},
			wantReady: true,
		},
		{
			name: "running session is rejected",
			session: models.Session{
				Status:       string(models.SessionStatusRunning),
				SandboxState: string(models.SandboxStateRunning),
				SnapshotKey:  &snapshot,
				Diff:         &diff,
			},
			wantReady: false,
			wantHint:  "currently running",
		},
		{
			name: "destroyed sandbox is rejected even if status looks resumable",
			session: models.Session{
				Status:       string(models.SessionStatusIdle),
				SandboxState: string(models.SandboxStateDestroyed),
				SnapshotKey:  &snapshot,
				Diff:         &diff,
			},
			wantReady: false,
			wantHint:  "expired",
		},
		{
			name: "session with no snapshot is rejected so /review never falls into the fresh-clone path",
			session: models.Session{
				Status:       string(models.SessionStatusIdle),
				SandboxState: string(models.SandboxStateSnapshotted),
				Diff:         &diff,
			},
			wantReady: false,
			wantHint:  "no snapshot",
		},
		{
			name: "session with no diff is rejected so reviews always have something to look at",
			session: models.Session{
				Status:       string(models.SessionStatusIdle),
				SandboxState: string(models.SandboxStateSnapshotted),
				SnapshotKey:  &snapshot,
				Diff:         &emptyDiff,
			},
			wantReady: false,
			wantHint:  "no changes",
		},
		{
			name: "session with unsupported status is rejected",
			session: models.Session{
				Status:       string(models.SessionStatusSkipped),
				SandboxState: string(models.SandboxStateSnapshotted),
				SnapshotKey:  &snapshot,
				Diff:         &diff,
			},
			wantReady: false,
			wantHint:  "not resumable",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason, ok := sessionReviewReadiness(tc.session)
			require.Equal(t, tc.wantReady, ok)
			if !tc.wantReady {
				require.Contains(t, reason, tc.wantHint)
			}
		})
	}
}

func TestBuildReviewRevisionContextRoundTrip(t *testing.T) {
	t.Parallel()

	diff := "--- a/foo\n+++ b/foo\n"
	session := models.Session{Diff: &diff}

	for _, mode := range []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity} {
		raw, err := buildReviewRevisionContext(session, mode)
		require.NoError(t, err, "buildReviewRevisionContext should succeed for %s", mode)

		// Round-trip through agent.ParseRevisionContext to confirm the
		// orchestrator will see exactly what we wrote — this catches drift
		// between the JSON tags on RevisionContext / SessionReviewContext.
		ctx, err := agent.ParseRevisionContext(raw)
		require.NoError(t, err)
		require.NotNil(t, ctx.ReviewContext)
		require.Equal(t, mode, ctx.ReviewContext.Mode)
		require.Equal(t, diff, ctx.ReviewContext.PreviousDiff)
		require.NotEmpty(t, ctx.ReviewContext.RequestSummary)

		// Repair fields must stay clear; reviews are session-native and
		// must not leak into the PR repair plumbing.
		require.Empty(t, ctx.RepairAction)
		require.Nil(t, ctx.RepairContext)

		// Sanity: format-for-continuation does not double-emit a review
		// directive when only the review context is set.
		require.Equal(t, "", agent.FormatRevisionContextForContinuation(ctx))

		// And the encoded payload uses the documented field name so the
		// frontend / other readers can rely on the JSON shape.
		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &decoded))
		_, hasReview := decoded["review_context"]
		require.True(t, hasReview, "encoded payload must use the review_context key")
	}
}

func TestReviewPromptForMode(t *testing.T) {
	t.Parallel()
	require.Contains(t, reviewPromptForMode(models.SessionReviewModeDefault), "review")
	require.Contains(t, reviewPromptForMode(models.SessionReviewModeSecurity), "security")
}

// sessionReviewSessionColumns mirrors the column ordering of session-store
// SELECTs/UPDATEs used by ClaimIdle / ClaimForResume. Kept in this test file
// because the production code reaches into pgx via the SessionStore directly
// and we need to feed the exact row shape it expects to scan, including the
// derived primary_issue_id field returned by sessionSelectColumns.
var sessionReviewSessionColumns = []string{
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

func newSessionReviewSessionRow(sessionID, orgID uuid.UUID, status string, snapshotKey *string, diff *string, now time.Time) []any {
	primaryIssueID := uuid.New()
	return []any{
		sessionID, &primaryIssueID, orgID, models.SessionOriginManual, models.SessionInteractionModeInteractive, models.SessionValidationPolicyOnTurnComplete, models.AgentTypeClaudeCode, status, "semi", "low",
		nil, nil, nil, nil,
		nil, nil, false, &now, nil, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, diff,
		nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, 0, now, "snapshotted", snapshotKey,
		nil, nil, nil, "", "",
		0, 0, "", nil,
		nil, "", "", int64(0), nil,
		"", nil, nil, 0,
		nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, "idle", (*string)(nil), nil, nil, nil, now,
	}
}

// reviewModesAlwaysClaude returns Claude-Code-style modes for any agent
// type. Lets the test focus on the transactional path without dragging in
// the real adapter map.
func reviewModesAlwaysClaude(models.AgentType) []models.SessionReviewMode {
	return []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity}
}

func TestStartReview_HappyPath_ClaimsIdleAndPersistsInOrder(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	snapshot := "snapshots/foo.tar"
	diff := "diff --git a/a b/a\n"

	// 1. Capabilities pre-flight: GetByID against the session.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
			newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
		))

	// 2. Transaction begins.
	mock.ExpectBegin()

	// 3. ClaimIdle wins on the first try (UPDATE ... WHERE status='idle' RETURNING ...).
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
			newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusRunning), &snapshot, &diff, now)...,
		))

	// 4. UpdateRevisionContext fires BEFORE message create — this ordering is
	// load-bearing because the orchestrator reads session.revision_context on
	// the next turn. Asserting the SQL pattern + the call ordering catches
	// regressions that would let the message + job land before the
	// directive.
	mock.ExpectExec("UPDATE sessions.+SET revision_context").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// 5. Message INSERT.
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

	// 6. Job enqueue inside the same tx — must use continue_session, not run_agent.
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

	// 7. Commit.
	mock.ExpectCommit()

	svc := NewService(Deps{
		Sessions:        db.NewSessionStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
		Jobs:            db.NewJobStore(mock),
		ReviewModes:     reviewModesAlwaysClaude,
		Logger:          zerolog.New(io.Discard),
	})

	resp, err := svc.StartReview(context.Background(), orgID, sessionID, userID, models.SessionReviewModeDefault)
	require.NoError(t, err, "StartReview should succeed when ClaimIdle wins")
	require.Equal(t, sessionID, resp.SessionID)
	require.Equal(t, models.SessionReviewModeDefault, resp.Mode)
	require.NoError(t, mock.ExpectationsWereMet(), "all StartReview expectations should fire in order")
}

func TestStartReview_FallsBackToClaimForResumeWhenIdleClaimMissesNoRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	snapshot := "snapshots/foo.tar"
	diff := "diff --git a/a b/a\n"

	// Pre-flight: session is "completed" (not idle). Capabilities.GetByID.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
			newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusCompleted), &snapshot, &diff, now)...,
		))

	mock.ExpectBegin()

	// ClaimIdle returns ErrNoRows (the session is completed, not idle).
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	// ClaimForResume succeeds against the completed session.
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
			newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusRunning), &snapshot, &diff, now)...,
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

	svc := NewService(Deps{
		Sessions:        db.NewSessionStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
		Jobs:            db.NewJobStore(mock),
		ReviewModes:     reviewModesAlwaysClaude,
		Logger:          zerolog.New(io.Discard),
	})

	resp, err := svc.StartReview(context.Background(), orgID, sessionID, userID, models.SessionReviewModeSecurity)
	require.NoError(t, err, "StartReview should fall back to ClaimForResume on a terminal-but-resumable session")
	require.Equal(t, models.SessionReviewModeSecurity, resp.Mode)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStartReview_RejectsAtCapabilityCheck(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		modes       []models.SessionReviewMode
		modeArg     models.SessionReviewMode
		row         func(orgID, sessionID uuid.UUID, now time.Time) []any
		wantErrSent bool // true if we expect StartReview to return an error before the tx
	}{
		{
			name:    "agent without native review modes",
			modes:   nil,
			modeArg: models.SessionReviewModeDefault,
			row: func(orgID, sessionID uuid.UUID, now time.Time) []any {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				return newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)
			},
			wantErrSent: true,
		},
		{
			name:    "destroyed sandbox",
			modes:   []models.SessionReviewMode{models.SessionReviewModeDefault},
			modeArg: models.SessionReviewModeDefault,
			row: func(orgID, sessionID uuid.UUID, now time.Time) []any {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				row := newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)
				// sandbox_state is at index 41 in the column list.
				row[41] = string(models.SandboxStateDestroyed)
				return row
			},
			wantErrSent: true,
		},
		{
			name:    "no snapshot key",
			modes:   []models.SessionReviewMode{models.SessionReviewModeDefault},
			modeArg: models.SessionReviewModeDefault,
			row: func(orgID, sessionID uuid.UUID, now time.Time) []any {
				diff := "diff --git a/a b/a\n"
				return newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), nil, &diff, now)
			},
			wantErrSent: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			now := time.Now().UTC()
			orgID := uuid.New()
			sessionID := uuid.New()

			mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(tc.row(orgID, sessionID, now)...))

			svc := NewService(Deps{
				Sessions:        db.NewSessionStore(mock),
				SessionMessages: db.NewSessionMessageStore(mock),
				Jobs:            db.NewJobStore(mock),
				ReviewModes:     func(models.AgentType) []models.SessionReviewMode { return tc.modes },
				Logger:          zerolog.New(io.Discard),
			})

			_, err = svc.StartReview(context.Background(), orgID, sessionID, uuid.New(), tc.modeArg)
			if tc.wantErrSent {
				require.Error(t, err, "StartReview should reject before opening a transaction")
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet(), "no extra DB calls beyond the initial GetByID should have fired")
		})
	}
}

func TestStartReview_InvalidModeReturnsErrReviewModeUnsupported(t *testing.T) {
	t.Parallel()

	// No DB calls expected — validation rejects before GetByID.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	svc := NewService(Deps{
		Sessions:        db.NewSessionStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
		Jobs:            db.NewJobStore(mock),
		ReviewModes:     reviewModesAlwaysClaude,
		Logger:          zerolog.New(io.Discard),
	})

	_, err = svc.StartReview(context.Background(), uuid.New(), uuid.New(), uuid.New(), models.SessionReviewMode("nope"))
	require.ErrorIs(t, err, ErrReviewModeUnsupported, "invalid mode strings must surface as the typed unsupported error so the handler returns 400")
	require.NoError(t, mock.ExpectationsWereMet(), "validation must reject before any DB call")
}

func TestCapabilities_ModesIsNeverNil(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	snapshot := "snapshots/foo.tar"
	diff := "diff --git a/a b/a\n"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
			newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
		))

	svc := NewService(Deps{
		Sessions:        db.NewSessionStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
		Jobs:            db.NewJobStore(mock),
		// Provider returns nil — exactly the case where the encoded JSON
		// must still be `"modes": []` so the React component doesn't crash
		// reading .length on a null.
		ReviewModes: func(models.AgentType) []models.SessionReviewMode { return nil },
		Logger:      zerolog.New(io.Discard),
	})

	caps, err := svc.Capabilities(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.NotNil(t, caps.Modes, "Modes must be a non-nil slice so JSON encodes [] not null")
	require.Equal(t, 0, len(caps.Modes))
	require.False(t, caps.CanReview)

	encoded, err := json.Marshal(caps)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"modes":[]`, "encoded payload must use [] for empty modes")
}

func TestCapabilities_ErrorAndReadinessPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, now time.Time)
		reviewModes  ReviewModeProvider
		expectedErr  error
		expectReady  bool
		expectedHint string
	}{
		{
			name: "returns session not found",
			setupMock: func(mock pgxmock.PgxPoolIface, _, _ uuid.UUID, _ time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
			},
			reviewModes: func(models.AgentType) []models.SessionReviewMode {
				return []models.SessionReviewMode{models.SessionReviewModeDefault}
			},
			expectedErr: ErrSessionNotFound,
		},
		{
			name: "wraps load errors",
			setupMock: func(mock pgxmock.PgxPoolIface, _, _ uuid.UUID, _ time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("database offline"))
			},
			reviewModes: func(models.AgentType) []models.SessionReviewMode {
				return []models.SessionReviewMode{models.SessionReviewModeDefault}
			},
			expectedHint: "load session",
		},
		{
			name: "returns readiness reason for running session",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusRunning), &snapshot, &diff, now)...,
					))
			},
			reviewModes: func(models.AgentType) []models.SessionReviewMode {
				return []models.SessionReviewMode{models.SessionReviewModeDefault}
			},
			expectedHint: "currently running",
		},
		{
			name: "returns ready capabilities",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusCompleted), &snapshot, &diff, now)...,
					))
			},
			reviewModes: func(models.AgentType) []models.SessionReviewMode {
				return []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity}
			},
			expectReady: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock.NewPool should create the capability test store")
			defer mock.Close()

			now := time.Now().UTC()
			orgID := uuid.New()
			sessionID := uuid.New()
			tt.setupMock(mock, orgID, sessionID, now)

			svc := NewService(Deps{
				Sessions:        db.NewSessionStore(mock),
				SessionMessages: db.NewSessionMessageStore(mock),
				Jobs:            db.NewJobStore(mock),
				ReviewModes:     tt.reviewModes,
				Logger:          zerolog.New(io.Discard),
			})

			caps, err := svc.Capabilities(context.Background(), orgID, sessionID)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "Capabilities should surface typed service errors for caller-specific HTTP mapping")
				require.Nil(t, caps, "Capabilities should not return payload data when the session lookup fails")
			} else if tt.expectedHint == "load session" {
				require.Error(t, err, "Capabilities should wrap unexpected store errors")
				require.Contains(t, err.Error(), tt.expectedHint, "Capabilities should annotate unexpected store failures with context")
			} else {
				require.NoError(t, err, "Capabilities should succeed for readiness-only scenarios")
				require.NotNil(t, caps, "Capabilities should return a payload when the session lookup succeeds")
				require.Equal(t, tt.expectReady, caps.CanReview, "Capabilities should reflect the session's review readiness")
				if tt.expectedHint != "" {
					require.Contains(t, caps.Reason, tt.expectedHint, "Capabilities should surface the session readiness reason for blocked reviews")
				}
			}

			require.NoError(t, mock.ExpectationsWereMet(), "Capabilities should satisfy the expected query plan")
		})
	}
}

func TestStartReview_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        models.SessionReviewMode
		setupMock   func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, userID uuid.UUID, now time.Time)
		reviewModes ReviewModeProvider
		expectedErr error
		contains    string
	}{
		{
			name: "wraps session load errors",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, _, _ uuid.UUID, _ uuid.UUID, _ time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("database offline"))
			},
			reviewModes: reviewModesAlwaysClaude,
			contains:    "load session",
		},
		{
			name: "rejects supported-but-unconfigured review mode",
			mode: models.SessionReviewModeSecurity,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
					))
			},
			reviewModes: func(models.AgentType) []models.SessionReviewMode {
				return []models.SessionReviewMode{models.SessionReviewModeDefault}
			},
			expectedErr: ErrReviewModeUnsupported,
		},
		{
			name: "returns begin transaction error",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
					))
				mock.ExpectBegin().WillReturnError(errors.New("tx unavailable"))
			},
			reviewModes: reviewModesAlwaysClaude,
			contains:    "begin session review tx",
		},
		{
			name: "returns not resumable when both claim paths miss",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusCompleted), &snapshot, &diff, now)...,
					))
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
				mock.ExpectRollback()
			},
			reviewModes: reviewModesAlwaysClaude,
			expectedErr: ErrSessionNotResumable,
		},
		{
			name: "returns not resumable when claim queries fail unexpectedly",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusCompleted), &snapshot, &diff, now)...,
					))
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("claim idle failed"))
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("claim resume failed"))
				mock.ExpectRollback()
			},
			reviewModes: reviewModesAlwaysClaude,
			expectedErr: ErrSessionNotResumable,
		},
		{
			name: "returns update revision context error",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
					))
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusRunning), &snapshot, &diff, now)...,
					))
				mock.ExpectExec("UPDATE sessions.+SET revision_context").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("update failed"))
				mock.ExpectRollback()
			},
			reviewModes: reviewModesAlwaysClaude,
			contains:    "persist review revision context",
		},
		{
			name: "returns create message error",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
					))
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusRunning), &snapshot, &diff, now)...,
					))
				mock.ExpectExec("UPDATE sessions.+SET revision_context").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnError(errors.New("insert message failed"))
				mock.ExpectRollback()
			},
			reviewModes: reviewModesAlwaysClaude,
			contains:    "create review message",
		},
		{
			name: "returns enqueue error",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
					))
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusRunning), &snapshot, &diff, now)...,
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
					WillReturnError(errors.New("enqueue failed"))
				mock.ExpectRollback()
			},
			reviewModes: reviewModesAlwaysClaude,
			contains:    "enqueue continue_session",
		},
		{
			name: "returns commit error",
			mode: models.SessionReviewModeDefault,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, _ uuid.UUID, now time.Time) {
				snapshot := "snapshots/foo.tar"
				diff := "diff --git a/a b/a\n"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusIdle), &snapshot, &diff, now)...,
					))
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionReviewSessionColumns).AddRow(
						newSessionReviewSessionRow(sessionID, orgID, string(models.SessionStatusRunning), &snapshot, &diff, now)...,
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
				mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
				mock.ExpectRollback()
			},
			reviewModes: reviewModesAlwaysClaude,
			contains:    "commit session review",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock.NewPool should create the review service mock")
			defer mock.Close()

			now := time.Now().UTC()
			orgID := uuid.New()
			sessionID := uuid.New()
			userID := uuid.New()
			tt.setupMock(mock, orgID, sessionID, userID, now)

			svc := NewService(Deps{
				Sessions:        db.NewSessionStore(mock),
				SessionMessages: db.NewSessionMessageStore(mock),
				Jobs:            db.NewJobStore(mock),
				ReviewModes:     tt.reviewModes,
				Logger:          zerolog.New(io.Discard),
			})

			resp, err := svc.StartReview(context.Background(), orgID, sessionID, userID, tt.mode)
			require.Nil(t, resp, "StartReview should not return a response payload on failure")
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "StartReview should surface typed errors for expected review failure modes")
			} else {
				require.Error(t, err, "StartReview should return an annotated error for infrastructure failures")
				require.Contains(t, err.Error(), tt.contains, "StartReview should explain which step in the review transaction failed")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "StartReview should satisfy the expected store interactions")
		})
	}
}
