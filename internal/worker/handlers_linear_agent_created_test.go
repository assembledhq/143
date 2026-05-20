package worker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type linearAgentCreatedDeadLetterClient struct {
	linear.Client

	activityCalls int
	updateCalls   int
	lastActivity  linear.AgentActivityInput
	lastUpdate    linear.AgentSessionUpdateInput
}

func (c *linearAgentCreatedDeadLetterClient) AgentActivityCreate(_ context.Context, in linear.AgentActivityInput) (linear.AgentActivityResult, error) {
	c.activityCalls++
	c.lastActivity = in
	return linear.AgentActivityResult{ActivityID: "act_error"}, nil
}

func (c *linearAgentCreatedDeadLetterClient) AgentSessionUpdate(_ context.Context, in linear.AgentSessionUpdateInput) error {
	c.updateCalls++
	c.lastUpdate = in
	return nil
}

func TestCreateAndAttachLinearAgentSessionUsesSingleTransaction(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	agentSessionRowID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()

	t.Run("commits session rows and bridge attach together", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "test should create pgx mock")
		defer mock.Close()

		sessionID := uuid.New()
		primaryThreadID := uuid.New()
		session := &models.Session{
			OrgID:          orgID,
			AgentType:      models.AgentTypeCodex,
			Status:         string(models.SessionStatusPending),
			PrimaryIssueID: &issueID,
		}

		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO sessions").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).
				AddRow(sessionID, now, now))
		mock.ExpectQuery("INSERT INTO session_threads").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(primaryThreadID))
		mock.ExpectExec("INSERT INTO session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec("UPDATE linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit()

		err = createAndAttachLinearAgentSession(context.Background(), &Stores{
			Sessions: db.NewSessionStore(mock),
		}, orgID, agentSessionRowID, session)
		require.NoError(t, err, "session create and bridge attach should commit together")
		require.Equal(t, sessionID, session.ID, "helper should preserve the generated session id for downstream tail steps")
		require.Equal(t, primaryThreadID, *session.PrimaryThreadID, "helper should preserve the generated primary thread id")
		require.NoError(t, mock.ExpectationsWereMet(), "all transactional expectations should be met")
	})

	t.Run("rolls back the session when bridge attach fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "test should create pgx mock")
		defer mock.Close()

		sessionID := uuid.New()
		session := &models.Session{
			OrgID:          orgID,
			AgentType:      models.AgentTypeCodex,
			Status:         string(models.SessionStatusPending),
			PrimaryIssueID: &issueID,
		}
		attachErr := errors.New("attach failed")

		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO sessions").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).
				AddRow(sessionID, now, now))
		mock.ExpectQuery("INSERT INTO session_threads").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
		mock.ExpectExec("INSERT INTO session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec("UPDATE linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(attachErr)
		mock.ExpectRollback()

		err = createAndAttachLinearAgentSession(context.Background(), &Stores{
			Sessions: db.NewSessionStore(mock),
		}, orgID, agentSessionRowID, session)
		require.Error(t, err, "attach failure should abort the transaction so the session is not orphaned")
		require.Contains(t, err.Error(), "attach session", "error should identify the bridge attach step")
		require.NoError(t, mock.ExpectationsWereMet(), "failed attach should roll back the uncommitted session rows")
	})
}

// TestReconcileLinearAgentCreatedNoPrimaryIssueOnlyReenqueuesRunAgent covers
// the retry-recovery hot path of handleLinearAgentCreated: when a re-delivered
// job finds a bridge row that already has session_id attached but the session
// was created without a primary issue link, reconcile must skip the
// FetchIssue + provider-state branch and only re-enqueue run_agent. Without
// this guarantee, transient writer failures between AttachSession and
// run_agent enqueue could leave the Linear AgentSession stuck mid-flight on
// a permanent retry.
func TestReconcileLinearAgentCreatedNoPrimaryIssueOnlyReenqueuesRunAgent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	dedupe := db.RunAgentDedupeKey(sessionID)

	// reconcile's first GetByID — session has no primary issue id so the
	// provider-state branch must be skipped.
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, uuid.Nil, orgID, string(models.SessionStatusPending), 0, nil, nil)...))

	// enqueueRunAgentForLinearAgent re-fetches the session before enqueueing.
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, uuid.Nil, orgID, string(models.SessionStatusPending), 0, nil, nil)...))

	// run_agent enqueue is idempotent (ON CONFLICT DO NOTHING dedupes on
	// (queue, dedupe_key)); this row simulates the happy-path insert.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(orgID, "agent", "run_agent", pgxmock.AnyArg(), 5, &dedupe).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions: db.NewSessionStore(mock),
			Jobs:     db.NewJobStore(mock),
		},
		// ClientForOrg / ProviderState intentionally nil — the no-primary-
		// issue branch must not call FetchIssue or writeAgentProviderState.
	}
	row := &db.LinearAgentSession{
		OrgID:                orgID,
		LinearAgentSessionID: "as_1",
		LinearIssueID:        "iss_1",
	}
	err = reconcileLinearAgentCreated(context.Background(), deps, row, sessionID, linearAgentEventPayload{
		LinearAgentSessionID: "as_1",
		LinearIssueID:        "iss_1",
	}, zerolog.Nop())
	require.NoError(t, err, "reconcile with no primary issue should succeed by only re-enqueueing run_agent")
	require.NoError(t, mock.ExpectationsWereMet(),
		"reconcile must not touch FetchIssue, the SessionIssueLinks store, or the LinearProviderStateStore on the no-primary-issue branch")
}

func TestLinearAgentCreatedDeadLetterHookEmitsErrorAndMarksBridge(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	client := &linearAgentCreatedDeadLetterClient{}
	clientCalls := 0

	mock.ExpectQuery("SELECT id, org_id, integration_id, linear_agent_session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, integrationID, "as_1",
			"iss_1", "VIR-102",
			"", "",
			nil, "pending", &now,
			now, now,
		))
	mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))
	mock.ExpectExec("UPDATE linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE linear_agent_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ctx := jobctx.WithDeadLetterHooks(context.Background())
	registerLinearAgentCreatedDeadLetter(ctx, LinearAgentEventHandlerDeps{
		ClientForOrg: func(context.Context, uuid.UUID) (linear.Client, error) {
			clientCalls++
			return client, nil
		},
	}, db.NewLinearAgentSessionStore(mock), db.NewLinearAgentActivityLogStore(mock), orgID, rowID, "as_1", zerolog.Nop())

	jobctx.RunDeadLetterHooks(ctx, errors.New("upsert linear issue failed"))
	require.Equal(t, 1, clientCalls, "dead-letter hook should resolve a Linear client before emitting")
	require.NoError(t, mock.ExpectationsWereMet(), "dead-letter hook should persist the local bridge state")
	require.Equal(t, 1, client.activityCalls, "dead-letter hook should emit exactly one Linear error activity")
	require.Equal(t, "as_1", client.lastActivity.AgentSessionID, "error activity should target the stuck Linear AgentSession")
	require.Equal(t, string(models.LinearAgentActivityError), client.lastActivity.Type, "dead-letter activity should render as an error")
	require.Contains(t, client.lastActivity.Body, "internal error", "dead-letter activity should explain that the agent failed before starting")
	require.Equal(t, 1, client.updateCalls, "dead-letter hook should pin the Linear AgentSession into an error state")
	require.Equal(t, "error", client.lastUpdate.State, "Linear AgentSession should be explicitly pinned to error")
}

// TestReconcileLinearAgentCreatedRejectsZeroValueSession pins the guardrail
// that protects against a future SessionStore schema change silently
// returning a zero-value row. The session_id is the worker's only handle
// on the 143 session; a zero-value scan would silently route the
// reconcile through the "no primary issue" branch without ever loading
// the real row.
func TestReconcileLinearAgentCreatedRejectsZeroValueSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()

	// Return a session row whose id column is the zero uuid. The
	// guardrail at the top of reconcileLinearAgentCreated must refuse
	// this rather than proceed with the rest of the pipeline.
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(uuid.Nil, uuid.Nil, orgID, string(models.SessionStatusPending), 0, nil, nil)...))

	deps := LinearAgentEventHandlerDeps{
		Stores: &Stores{
			Sessions: db.NewSessionStore(mock),
			Jobs:     db.NewJobStore(mock),
		},
	}
	row := &db.LinearAgentSession{
		OrgID:                orgID,
		LinearAgentSessionID: "as_1",
		LinearIssueID:        "iss_1",
	}
	err = reconcileLinearAgentCreated(context.Background(), deps, row, sessionID, linearAgentEventPayload{
		LinearAgentSessionID: "as_1",
		LinearIssueID:        "iss_1",
	}, zerolog.Nop())
	require.Error(t, err, "zero-value session scan must surface as an error, not silently fall through to the no-primary-issue branch")
	require.Contains(t, err.Error(), "zero-value", "error should identify the zero-value guardrail")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleLinearAgentCreatedReemitsBootstrapBeforeIssueFetch(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("handlers_linear_agent_created.go")
	require.NoError(t, err, "created handler source should be readable")

	body := string(src)
	emit := strings.Index(body, "emitLinearAgentBootstrap(")
	fetch := strings.Index(body, "client.FetchIssue(ctx, issueIdent)")
	require.NotEqual(t, -1, emit, "created handler should re-emit the dispatcher bootstrap activity")
	require.NotEqual(t, -1, fetch, "created handler should still fetch the live Linear issue")
	require.Less(t, emit, fetch, "worker bootstrap re-emit should happen before the potentially slower live issue fetch")
}
