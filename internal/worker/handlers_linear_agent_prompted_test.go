package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestPromptedWithoutCreated covers the dispatcher race: Linear can
// deliver `prompted` before its companion `created` event lands. The
// dispatcher already 200'd Linear so Linear will not redeliver, which
// means only the worker job's retry path can keep the follow-up message
// alive. The handler's contract is therefore to return a RetryableError
// with a short fixed wait so the created handler has time to attach the
// session_id; returning nil would silently drop the user's comment.
func TestPromptedWithoutCreated(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()

	// Lookup returns a row with session_id = NULL (created hasn't
	// completed). The handler must short-circuit without invoking any
	// other store, but it must surface a retryable error.
	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_pending",
			"iss_1", "ACS-1",
			"app_1", "user_1",
			nil /* session_id NULL */, "pending", &now,
			now, now,
		))

	deps := LinearAgentEventHandlerDeps{
		// Stores left intentionally empty — the handler must NOT reach
		// SessionMessages.Create or Sessions.ClaimIdle when session_id
		// is nil. A Stores=nil deref would explode if the short-circuit
		// regressed; this is the cheapest way to assert the contract.
		Stores: &Stores{},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	payload := linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_pending",
		LinearCommentID:      "comment_1",
	}

	err = handleLinearAgentPrompted(context.Background(), deps, store, payload, zerolog.Nop())
	var retryable *RetryableError
	require.ErrorAs(t, err, &retryable,
		"prompted-without-created must return a RetryableError so the worker re-runs the job; "+
			"Linear already received 200 for the webhook and will not redeliver")
	require.NotNil(t, retryable.RetryAfter,
		"the retry must use a fixed short wait, not fall through to exponential backoff that would delay the follow-up comment for minutes")
	require.NoError(t, mock.ExpectationsWereMet(),
		"only the Lookup query should fire — no claims, no message inserts, no continue_session enqueue")
}

// TestPromptedDeadlineExceededWithoutBridgeRowReturnsTerminal pins the H4
// fix: when the bridge row never appears (Linear sent prompted but no
// created), the worker must stop retrying once the job's age exceeds
// promptedAwaitingCreateDeadline. Without this bound, RetryableError's
// preserveAttempts semantics combined with the 5s backoff would loop until
// the global 8-min worker cap dead-letters the job — wasted work and no
// user-facing signal.
func TestPromptedDeadlineExceededWithoutBridgeRowReturnsTerminal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)

	// Synthesize a job context whose CreatedAt is well past the
	// awaiting-created deadline.
	ctx := jobctx.WithJobCreatedAt(context.Background(), time.Now().Add(-2*promptedAwaitingCreateDeadline))

	err = handleLinearAgentPrompted(ctx, deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_never_created",
		LinearCommentID:      "comment_1",
	}, zerolog.Nop())
	require.NoError(t, err, "expired prompted-without-created job must drop terminally (return nil), not keep retrying")
	require.NoError(t, mock.ExpectationsWereMet(), "only the Lookup query should fire on the terminal path")
}

func TestPromptedLookupMissRetriesForOutOfOrderCreated(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_missing",
		LinearCommentID:      "comment_1",
	}, zerolog.Nop())
	var retryable *RetryableError
	require.ErrorAs(t, err, &retryable, "missing AgentSession row should retry because prompted can beat created delivery")
	require.NotNil(t, retryable.RetryAfter, "lookup-miss retry should use the short prompted-created race backoff")
	require.NoError(t, mock.ExpectationsWereMet(), "handler should only lookup the agent session before retrying")
}

// TestLinearFetchErrorIsTransient_TerminalErrors pins the bounded-retry
// contract for the prompted handler's comment-fetch path. ErrUnauthorized
// MUST be terminal because the worker's RetryableError contract does not
// consume the Attempts counter — an "unauthorized is transient" classifier
// would loop forever on a revoked token (see review finding H3).
// ErrCommentNotFound is also terminal because Linear deleted the comment;
// retrying produces the same 404.
func TestLinearFetchErrorIsTransient_TerminalErrors(t *testing.T) {
	t.Parallel()

	require.False(t, linearFetchErrorIsTransient(linear.ErrUnauthorized),
		"401 must be terminal so the handler falls back to placeholder text instead of retrying forever")
	require.False(t, linearFetchErrorIsTransient(linear.ErrCommentNotFound),
		"comment-not-found must be terminal — Linear deleted the comment")
	require.False(t, linearFetchErrorIsTransient(nil), "nil error is not transient")
}

// TestLinearFetchErrorIsTransient_RetryableErrors pins that genuine transient
// failures (rate limits, unknown / network) remain classified as retryable.
func TestLinearFetchErrorIsTransient_RetryableErrors(t *testing.T) {
	t.Parallel()

	require.True(t, linearFetchErrorIsTransient(&linear.RateLimitError{RetryAfter: "10"}),
		"rate-limit must be transient")
	require.True(t, linearFetchErrorIsTransient(errors.New("dial tcp: connection refused")),
		"network errors must be transient")
}

func TestResolvePromptedCommentBodyPrefersWebhookAgentActivityBody(t *testing.T) {
	t.Parallel()

	body, err := resolvePromptedCommentBody(context.Background(), LinearAgentEventHandlerDeps{
		ClientForOrg: func(context.Context, uuid.UUID) (linear.Client, error) {
			return nil, errors.New("must not fetch Linear when webhook carries the prompt body")
		},
	}, linearAgentEventPayload{
		LinearCommentID:  "comment_1",
		LinearPromptBody: "Please add regression coverage.",
	}, uuid.New(), zerolog.Nop())

	require.NoError(t, err, "prompt body from webhook should not require a Linear comment fetch")
	require.Equal(t, "Please add regression coverage.", body, "prompted handler should use Linear's agentActivity.body verbatim")
}

// TestAppendPromptedMessageToRunningSessionRollsBackOnCommitFailure pins
// the running-session append's transactional semantics: if the COMMIT
// itself fails (e.g. the connection dropped between INSERT and the
// commit packet), the deferred Rollback must run so the session_messages
// insert doesn't get stuck in an indeterminate pending state on the
// connection. Without rollback, a subsequent reuse of the connection
// would either inherit the open transaction or surface a confusing
// "current transaction is aborted" error far from the cause.
func TestAppendPromptedMessageToRunningSessionRollsBackOnCommitFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	commitErr := errors.New("commit dropped connection")

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn\s+FROM sessions.*FOR UPDATE`).
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusRunning, 7))
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(sessionID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), 8, models.MessageRoleUser, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(99), now))
	mock.ExpectCommit().WillReturnError(commitErr)
	mock.ExpectRollback()

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
		},
		Logger: zerolog.Nop(),
	}
	err = appendPromptedMessageToRunningSession(
		context.Background(),
		deps,
		orgID,
		uuid.New(),
		db.SessionMessageAppendState{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, CurrentTurn: 7},
		linearAgentEventPayload{LinearAgentSessionID: "as_committed_fail"},
		"please retry",
		zerolog.Nop(),
	)
	require.Error(t, err, "a failed COMMIT must surface the error to the caller so the worker retries")
	require.ErrorIs(t, err, commitErr, "the underlying commit error should be wrapped, not swallowed")
	require.NoError(t, mock.ExpectationsWereMet(),
		"the deferred Rollback must fire when committed=false, even on commit failure")
}

func TestAppendPromptedMessageAndEnqueueContinueSkipsDuplicateComment(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()
	session := models.Session{
		ID:          sessionID,
		OrgID:       orgID,
		CurrentTurn: 3,
	}
	msg := &models.SessionMessage{
		SessionID:  sessionID,
		OrgID:      orgID,
		TurnNumber: 4,
		Role:       models.MessageRoleUser,
		Content:    "please retry",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO linear_agent_prompted_messages").
		WithArgs(orgID, rowID, sessionID, "comment_1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	err = appendPromptedMessageAndEnqueueContinue(
		context.Background(),
		&Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
			Jobs:            db.NewJobStore(mock),
		},
		orgID,
		rowID,
		"comment_1",
		session,
		msg,
	)

	require.ErrorIs(t, err, errPromptedMessageAlreadyProcessed, "duplicate Linear prompted comment should be reported without inserting another message")
	require.NoError(t, mock.ExpectationsWereMet(), "duplicate prompted comment should reserve-check inside the transaction and skip message/job writes")
}

func TestPromptedRunningSessionAppendsUnderSessionLockWithoutContinuationJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_running",
			"iss_1", "ACS-1",
			"", "",
			&sessionID, "in_progress", &now,
			now, now,
		))
	mock.ExpectQuery("(?s)UPDATE sessions.*status = 'idle'").
		WithArgs(sessionID, orgID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusRunning, 3))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn\s+FROM sessions.*FOR UPDATE`).
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusRunning, 3))
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(sessionID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), 4, models.MessageRoleUser, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(42), now))
	mock.ExpectCommit()

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
			Jobs:            db.NewJobStore(mock),
		},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_running",
	}, zerolog.Nop())
	require.NoError(t, err, "running-session prompted comments should append under a session lock without trying to enqueue another continuation")
	require.NoError(t, mock.ExpectationsWereMet(), "running-session fast path should create one message transactionally and no job")
}

func TestPromptedRunningSessionAppendsWhenRevisionPromptsDisabled(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	allowRevision := false

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_running_no_revision",
			"iss_1", "ACS-1",
			"", "",
			&sessionID, "in_progress", &now,
			now, now,
		))
	mock.ExpectQuery("(?s)UPDATE sessions.*status = 'idle'").
		WithArgs(sessionID, orgID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusRunning, 5))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn\s+FROM sessions.*FOR UPDATE`).
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusRunning, 5))
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(sessionID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), 6, models.MessageRoleUser, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(43), now))
	mock.ExpectCommit()

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
			Jobs:            db.NewJobStore(mock),
		},
		SettingsLoader: func(_ context.Context, gotOrgID uuid.UUID) (models.LinearAgentSettings, error) {
			require.Equal(t, orgID, gotOrgID, "settings loader should be scoped to the prompted event org")
			return models.LinearAgentSettings{AllowRevisionPerPrompt: &allowRevision}, nil
		},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_running_no_revision",
	}, zerolog.Nop())
	require.NoError(t, err, "revision-disabled settings should not block comments while the session is still running")
	require.NoError(t, mock.ExpectationsWereMet(), "handler should check running append state before applying the revision-disabled terminal response")
}

func TestPromptedRunningSessionRetriesWhenTurnCompletesBeforeAppend(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_race",
			"iss_1", "ACS-1",
			"", "",
			&sessionID, "in_progress", &now,
			now, now,
		))
	mock.ExpectQuery("(?s)UPDATE sessions.*status = 'idle'").
		WithArgs(sessionID, orgID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusRunning, 3))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn\s+FROM sessions.*FOR UPDATE`).
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusIdle, 4))
	mock.ExpectRollback()

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
			Jobs:            db.NewJobStore(mock),
		},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_race",
	}, zerolog.Nop())
	var retryable *RetryableError
	require.ErrorAs(t, err, &retryable, "prompted race should retry instead of inserting a message after the turn already went idle")
	require.NotNil(t, retryable.RetryAfter, "running-to-idle race retry should use a short bounded delay")
	require.NoError(t, mock.ExpectationsWereMet(), "handler should not insert a message or enqueue a job after the locked status is no longer running")
}

func TestPromptedTerminalSessionRespectsDisabledRevisionPrompts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	allowRevision := false

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_done",
			"iss_1", "ACS-1",
			"", "",
			&sessionID, "complete", &now,
			now, now,
		))
	mock.ExpectQuery("(?s)UPDATE sessions.*status = 'idle'").
		WithArgs(sessionID, orgID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusCompleted, 9))

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
			Jobs:            db.NewJobStore(mock),
		},
		SettingsLoader: func(_ context.Context, gotOrgID uuid.UUID) (models.LinearAgentSettings, error) {
			require.Equal(t, orgID, gotOrgID, "settings loader should be scoped to the prompted event org")
			return models.LinearAgentSettings{AllowRevisionPerPrompt: &allowRevision}, nil
		},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_done",
	}, zerolog.Nop())
	require.NoError(t, err, "disabled revision prompts should be ignored instead of reopening a terminal session")
	require.NoError(t, mock.ExpectationsWereMet(), "handler should check append state then stop after disabled revision without ClaimForResume")
}

func TestPromptedIdleSessionRollsBackMessageWhenContinueEnqueueFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()
	enqueueErr := errors.New("enqueue failed")

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_idle",
			"iss_1", "ACS-1",
			"", "",
			&sessionID, "in_progress", &now,
			now, now,
		))
	mock.ExpectQuery("(?s)UPDATE sessions.*status = 'idle'").
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, issueID, orgID, models.SessionStatusRunning, 7, nil, nil)...))
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(sessionID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), 8, models.MessageRoleUser, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(44), now))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnError(enqueueErr)
	mock.ExpectRollback()
	mock.ExpectQuery("UPDATE sessions SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, issueID, orgID, models.SessionStatusIdle, 7, nil, nil)...))

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
			Jobs:            db.NewJobStore(mock),
		},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_idle",
	}, zerolog.Nop())
	require.Error(t, err, "enqueue failure should surface so the worker retries the prompted job")
	require.Contains(t, err.Error(), "enqueue continue_session", "error should identify the failed atomic append/enqueue step")
	require.NoError(t, mock.ExpectationsWereMet(), "message insert and enqueue should happen inside one transaction that rolls back on enqueue failure")
}

func TestPromptedResumedTerminalSessionRestoresOriginalStatusWhenContinueEnqueueFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()
	enqueueErr := errors.New("enqueue failed")

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, uuid.New(), "as_completed",
			"iss_1", "ACS-1",
			"", "",
			&sessionID, "complete", &now,
			now, now,
		))
	mock.ExpectQuery("(?s)UPDATE sessions.*status = 'idle'").
		WithArgs(sessionID, orgID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT\s+id, org_id, status, current_turn`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "status", "current_turn"}).
			AddRow(sessionID, orgID, models.SessionStatusCompleted, 7))
	mock.ExpectQuery(`(?s)UPDATE sessions\s+SET status = 'running', completed_at = NULL,\s+last_activity_at = now\(\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, issueID, orgID, models.SessionStatusRunning, 7, nil, nil)...))
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(sessionID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), 8, models.MessageRoleUser, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(45), now))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnError(enqueueErr)
	mock.ExpectRollback()
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, issueID, orgID, models.SessionStatusCompleted, 7, nil, nil)...))
	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions:        db.NewSessionStore(mock),
			SessionMessages: db.NewSessionMessageStore(mock),
			Jobs:            db.NewJobStore(mock),
		},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)
	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                orgID.String(),
		LinearAgentSessionID: "as_completed",
	}, zerolog.Nop())
	require.Error(t, err, "enqueue failure should surface so the prompted job can retry")
	require.Contains(t, err.Error(), "enqueue continue_session", "error should identify the failed atomic append/enqueue step")
	require.NoError(t, mock.ExpectationsWereMet(), "resumed terminal session should be restored to its original completed status when enqueue fails")
}

func TestEnqueueContinueForLinearAgentPinsSandboxOwner(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	containerID := "container-1"
	workerNodeID := "worker-1"
	dedupe := db.ContinueSessionDedupeKey(sessionID)
	generatedID := uuid.New()

	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(orgID, "agent", "continue_session", pgxmock.AnyArg(), 5, &dedupe, &workerNodeID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generatedID))

	err = enqueueContinueForLinearAgent(context.Background(), &Stores{Jobs: db.NewJobStore(mock)}, orgID, models.Session{
		ID:           sessionID,
		OrgID:        orgID,
		ContainerID:  &containerID,
		WorkerNodeID: &workerNodeID,
	})
	require.NoError(t, err, "continue_session enqueue should succeed when target node is recorded")
	require.NoError(t, mock.ExpectationsWereMet(), "continue_session job should be pinned to the sandbox owner")
}

// TestPromptedInvalidOrgID covers the malformed-payload path. The
// dispatcher pre-validates this, but defense in depth at the worker
// layer surfaces the failure as a clean error rather than a panic.
func TestPromptedInvalidOrgID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{},
		Logger: zerolog.Nop(),
	}
	store := db.NewLinearAgentSessionStore(mock)

	err = handleLinearAgentPrompted(context.Background(), deps, store, linearAgentEventPayload{
		Action:               "prompted",
		OrgID:                "not-a-uuid",
		LinearAgentSessionID: "as_1",
	}, zerolog.Nop())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid org_id")
	// No DB calls — the handler must reject malformed payloads before
	// reaching the store.
	require.NoError(t, mock.ExpectationsWereMet())
}
