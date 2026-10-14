package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewWorkspaceColdEligible(t *testing.T) {
	t.Parallel()
	repositoryID, otherRepositoryID := uuid.New(), uuid.New()
	snapshot := "checkpoint"
	tests := []struct {
		name     string
		session  models.Session
		eligible bool
	}{
		{name: "cold review", session: models.Session{Origin: models.SessionOriginCodeReview, RepositoryID: &repositoryID}, eligible: true},
		{name: "snapshot without generation advance uses ordinary recovery", session: models.Session{Origin: models.SessionOriginCodeReview, RepositoryID: &repositoryID, SnapshotKey: &snapshot}},
		{name: "snapshot after generation advance uses ordinary recovery", session: models.Session{Origin: models.SessionOriginCodeReview, RepositoryID: &repositoryID, SnapshotKey: &snapshot, WorkspaceGeneration: 1}},
		{name: "another repository uses ordinary recovery", session: models.Session{Origin: models.SessionOriginCodeReview, RepositoryID: &otherRepositoryID}},
		{name: "another origin uses ordinary recovery", session: models.Session{Origin: models.SessionOriginManual, RepositoryID: &repositoryID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.eligible, codeReviewWorkspaceColdEligible(tt.session, repositoryID), "controller and initializer should agree on cold workspace eligibility")
		})
	}
}

func TestCodeReviewWorkspacePreparationStarted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		prepare  bool
		priorJob bool
		want     bool
	}{
		{name: "preparation disabled"},
		{name: "first preflight has no preparation checkpoint", prepare: true},
		{name: "prior enqueue skips repeated GitHub preflight", prepare: true, priorJob: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated preparation checkpoint database mock")
			defer mock.Close()
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New(), MetadataID: uuid.New()}
			stores := &Stores{Sessions: db.NewSessionStore(mock), Jobs: db.NewJobStore(mock)}
			if tt.prepare {
				mock.ExpectQuery("SELECT workspace_generation FROM sessions").WithArgs(job.OrgID, job.SessionID).
					WillReturnRows(pgxmock.NewRows([]string{"workspace_generation"}).AddRow(int64(0)))
				rows := pgxmock.NewRows([]string{"created_at"})
				if tt.priorJob {
					rows.AddRow(time.Date(2026, time.September, 23, 18, 0, 0, 0, time.UTC))
				}
				mock.ExpectQuery("SELECT created_at[\\s\\S]*FROM jobs").WithArgs(job.OrgID, job.MetadataID.String(), job.SessionID.String()).
					WillReturnRows(rows)
			}
			started, err := codeReviewWorkspacePreparationStarted(context.Background(), stores,
				&Services{CodeReviewWorkspacePreparationEnabled: tt.prepare}, job)
			require.NoError(t, err, "preparation checkpoint lookup should finish without an error")
			require.Equal(t, tt.want, started, "a prior durable enqueue should suppress repeated GitHub preflight")
			require.NoError(t, mock.ExpectationsWereMet(), "checkpoint lookup should preserve the tenant's first preparation across generations")
		})
	}
}

func TestPrepareCodeReviewWorkspaceHandlerHonorsPreparationSwitch(t *testing.T) {
	t.Parallel()
	err := newPrepareCodeReviewWorkspaceHandler(nil,
		&Services{CodeReviewWorkspacePreparationEnabled: false},
		zerolog.Nop())(context.Background(), models.JobTypePrepareCodeReviewWorkspace, nil)
	require.NoError(t, err, "a queued preparation should no-op when the rollout switch is disabled")
}

func TestEnsureCodeReviewWorkspaceReadyFallsBackWhenPreparationCannotServe(t *testing.T) {
	t.Parallel()
	snapshot := "checkpoint-before-first-completed-turn"
	tests := []struct {
		name                string
		snapshot            *string
		workspaceGen        int64
		wrongRepository     bool
		terminalPreparation bool
		failureStage        string
		cleanupFailure      bool
		timedOutPreparation bool
		cancelError         bool
		afterPreflight      bool
		laterTurn           bool
	}{
		{name: "snapshot with generation zero", snapshot: &snapshot},
		{name: "repository mismatch", wrongRepository: true},
		{name: "dead-lettered preparation stops without re-enqueue", terminalPreparation: true},
		{name: "persisted unconfirmed cleanup stays terminal", terminalPreparation: true, cleanupFailure: true},
		{name: "persisted unconfirmed cleanup survives expired budget", terminalPreparation: true, timedOutPreparation: true, cleanupFailure: true},
		{name: "expired safe clone failure retains budget wording", terminalPreparation: true, timedOutPreparation: true, failureStage: agent.CodeReviewPreparationStageClone},
		{name: "slow live preparation is cancelled and fails visibly", timedOutPreparation: true},
		{name: "uncertain cancellation cannot restart preparation", timedOutPreparation: true, cancelError: true},
		{name: "holder lost during preflight recovers within the first budget", afterPreflight: true},
		{name: "publication generation cannot reset the initial budget", workspaceGen: 1, timedOutPreparation: true},
		{name: "later synthesis recovery uses its current turn budget", workspaceGen: 2, laterTurn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated workspace gate database mock")
			defer mock.Close()
			orgID, sessionID, reviewID, repositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, MetadataID: reviewID, HeadSHA: "head-1"}
			mock.ExpectQuery("SELECT s.container_id, s.worker_node_id, s.workspace_generation").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "review_id": reviewID, "session_id": sessionID, "head_sha": job.HeadSHA}).
				WillReturnRows(pgxmock.NewRows([]string{"container_id", "worker_node_id", "workspace_generation"}))
			now := time.Now().UTC()
			var phase *models.CodeReviewPhase
			if tt.laterTurn {
				value := models.CodeReviewPhaseSynthesizing
				phase = &value
			}
			mock.ExpectQuery("FROM code_review_session_metadata").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
				WillReturnRows(newCodeReviewMetadataRows().AddRow(
					reviewID, orgID, sessionID, repositoryID, uuid.New(), uuid.New(),
					"base-1", "head-1", false, models.CodeReviewTriggerSourceAppReviewer,
					models.CodeReviewSessionStatusRunning, phase, nil, nil, nil, nil, false,
					nil, nil, false, nil, "output-key", nil, nil, nil, nil, nil, nil, now))
			sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusRunning, 0, nil, tt.snapshot)
			setWorkerSessionColumn(sessionRow, "origin", models.SessionOriginCodeReview)
			if tt.wrongRepository {
				otherRepositoryID := uuid.New()
				setWorkerSessionColumn(sessionRow, "repository_id", &otherRepositoryID)
			} else {
				setWorkerSessionColumn(sessionRow, "repository_id", &repositoryID)
			}
			setWorkerSessionColumn(sessionRow, "workspace_generation", tt.workspaceGen)
			mock.ExpectQuery("FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
				WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
			createdRows := pgxmock.NewRows([]string{"created_at"})
			if tt.terminalPreparation || tt.timedOutPreparation || tt.afterPreflight || tt.laterTurn {
				createdAt := time.Now().UTC()
				if tt.timedOutPreparation {
					createdAt = createdAt.Add(-codeReviewWorkspaceWaitLimit - time.Minute)
				}
				if tt.laterTurn {
					createdAt = createdAt.Add(-24 * time.Hour)
				}
				createdRows.AddRow(createdAt)
			}
			key := codeReviewWorkspacePreparationKey(reviewID, sessionID, tt.workspaceGen)
			mock.ExpectQuery("SELECT created_at[\\s\\S]*FROM jobs").WithArgs(orgID, reviewID.String(), sessionID.String()).WillReturnRows(createdRows)
			statusRows := pgxmock.NewRows([]string{"status", "last_error"})
			if tt.terminalPreparation {
				stage := tt.failureStage
				if stage == "" {
					stage = agent.CodeReviewPreparationStageDependencySetup
				}
				cause := errors.New("safe internal failure")
				if tt.cleanupFailure {
					cause = agent.ErrCodeReviewPreparationCleanup
				}
				statusRows.AddRow(models.JobStatusDeadLetter, agent.NewCodeReviewWorkspacePreparationError(stage, cause).Error())
			} else if tt.afterPreflight {
				statusRows.AddRow(models.JobStatusSucceeded, "")
			}
			mock.ExpectQuery("SELECT status,[\\s\\S]*FROM jobs").WithArgs(orgID, "agent", key).WillReturnRows(statusRows)
			if tt.timedOutPreparation && !tt.terminalPreparation {
				cancel := mock.ExpectExec("UPDATE jobs").WithArgs(orgID, "agent", key)
				if tt.cancelError {
					cancel.WillReturnError(errors.New("connection lost after cancellation attempt"))
				} else {
					cancel.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				}
			}
			stores := &Stores{
				CodeReviewWorkspaces: db.NewCodeReviewWorkspaceStore(mock),
				CodeReviews:          db.NewCodeReviewStore(mock), Sessions: db.NewSessionStore(mock), Jobs: db.NewJobStore(mock),
			}
			gate := ensureCodeReviewWorkspaceReady
			if tt.afterPreflight {
				gate = ensureCodeReviewWorkspaceReadyAfterPreflight
			}
			err = gate(context.Background(), stores,
				&Services{CodeReviewWorkspacePreparationEnabled: true},
				zerolog.Nop(), job)
			if tt.terminalPreparation || tt.timedOutPreparation {
				var setupFailure *codeReviewWorkspaceGateFailure
				require.ErrorAs(t, err, &setupFailure, "failed setup must stop fan-out and expose a fenced operational failure")
				require.Equal(t, tt.workspaceGen, setupFailure.Generation, "terminal projection must use the observed workspace generation")
				if tt.cleanupFailure {
					require.ErrorIs(t, err, agent.ErrCodeReviewPreparationCleanup, "the expired gate must never replace persisted cleanup uncertainty with ordinary timeout")
					require.Equal(t, "Review workspace preparation could not confirm process cleanup. Recreate the review workspace and retry. Review did not run.", setupFailure.Err.Error(), "cleanup failure must require workspace recreation even after its durable window expires")
				}
				if tt.timedOutPreparation {
					require.ErrorIs(t, err, context.DeadlineExceeded, "timeout must remain distinct from a code finding")
					if tt.failureStage != "" {
						require.Equal(t, "Review workspace preparation exceeded its total eight-minute budget during repository clone. Review did not run.", setupFailure.Err.Error(), "expired persisted safe diagnostics must retain both the total budget and concrete clone stage")
					}
				}
			} else {
				require.NoError(t, err, "legitimate workspace recovery should remain available within the original budget")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "workspace gate should stop before enqueuing an impossible preparation")
		})
	}
}

func TestEnsureCodeReviewWorkspaceReadyEnqueuesColdPreparation(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated cold preparation database mock")
	defer mock.Close()
	orgID, sessionID, reviewID, repositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, MetadataID: reviewID, HeadSHA: "head-1"}
	key := codeReviewWorkspacePreparationKey(reviewID, sessionID, 0)
	mock.ExpectQuery("SELECT s.container_id, s.worker_node_id, s.workspace_generation").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "review_id": reviewID, "session_id": sessionID, "head_sha": job.HeadSHA}).
		WillReturnRows(pgxmock.NewRows([]string{"container_id", "worker_node_id", "workspace_generation"}))
	now := time.Now().UTC()
	mock.ExpectQuery("FROM code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newCodeReviewMetadataRows().AddRow(
			reviewID, orgID, sessionID, repositoryID, uuid.New(), uuid.New(),
			"base-1", "head-1", false, models.CodeReviewTriggerSourceAppReviewer,
			models.CodeReviewSessionStatusRunning, nil, nil, nil, nil, nil, false,
			nil, nil, false, nil, "output-key", nil, nil, nil, nil, nil, nil, now))
	sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusRunning, 0, nil, nil)
	setWorkerSessionColumn(sessionRow, "origin", models.SessionOriginCodeReview)
	setWorkerSessionColumn(sessionRow, "repository_id", &repositoryID)
	mock.ExpectQuery("FROM sessions").WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
	mock.ExpectQuery("SELECT created_at[\\s\\S]*FROM jobs").WithArgs(orgID, reviewID.String(), sessionID.String()).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectQuery("SELECT status,[\\s\\S]*FROM jobs").WithArgs(orgID, "agent", key).
		WillReturnRows(pgxmock.NewRows([]string{"status", "last_error"}))
	mock.ExpectQuery("WITH candidates AS").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-1"))
	mock.ExpectQuery("INSERT INTO jobs").WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT created_at[\\s\\S]*FROM jobs").WithArgs(orgID, reviewID.String(), sessionID.String()).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}).AddRow(now))
	stores := &Stores{
		CodeReviewWorkspaces: db.NewCodeReviewWorkspaceStore(mock),
		CodeReviews:          db.NewCodeReviewStore(mock), Sessions: db.NewSessionStore(mock), Jobs: db.NewJobStore(mock),
	}
	err = ensureCodeReviewWorkspaceReady(context.Background(), stores,
		&Services{CodeReviewWorkspacePreparationEnabled: true},
		zerolog.Nop(), job)
	var retry *RetryableError
	require.ErrorAs(t, err, &retry, "a cold workspace should enqueue preparation and defer reviewer fan-out")
	require.Equal(t, now, *retry.RetryWindowStartedAt, "the first durable preparation enqueue should start the wait window")
	require.NoError(t, mock.ExpectationsWereMet(), "cold preparation should enqueue exactly one initialization job")
}

func TestCodeReviewControllerWaitLetsReadinessGateOwnDeadline(t *testing.T) {
	t.Parallel()
	startedAt := time.Now().Add(-codeReviewWorkspaceWaitLimit + time.Second)
	retry, ok := codeReviewControllerWorkspaceWaitError("preparation pending", startedAt).(*RetryableError)
	require.True(t, ok, "controller wait should use a retryable job deferral")
	require.Equal(t, startedAt, *retry.RetryWindowStartedAt, "controller wait should remain anchored to the first durable preparation enqueue")
	timedOut, _ := retryableDurationExceeded(startedAt, retry, time.Now())
	require.False(t, timedOut, "worker should let the next readiness check persist the fenced operational timeout")
}
