package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSessionReviewLoopStore_CreateLoopFiltersOrgOnRead(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	startedBy := uuid.New()
	startedAt := time.Now().UTC()

	mock.ExpectQuery("INSERT INTO session_review_loops").
		WithArgs(anyArgs(20)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "started_at"}).AddRow(uuid.New(), startedAt))

	loop := &models.SessionReviewLoop{
		OrgID:           orgID,
		SessionID:       sessionID,
		ThreadID:        &threadID,
		Status:          models.ReviewLoopStatusRunning,
		Source:          models.ReviewLoopSourceManual,
		AgentType:       models.AgentTypeClaudeCode,
		MaxPasses:       2,
		StartedByUserID: &startedBy,
	}
	err = store.CreateLoop(context.Background(), loop)
	require.NoError(t, err, "CreateLoop should insert a loop row")

	rows := pgxmock.NewRows(reviewLoopColumnsForTest()).AddRow(
		loop.ID, orgID, sessionID, nil, &threadID, "running", "manual", nil, nil, nil, "claude_code", 2, "minimal", 0,
		false, nil, nil, nil, nil, nil, &startedBy, startedAt, nil,
	)
	mock.ExpectQuery("SELECT .+ FROM session_review_loops WHERE id = @id AND org_id = @org_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(rows)

	got, err := store.GetLoopByID(context.Background(), orgID, loop.ID)
	require.NoError(t, err, "GetLoopByID should read by org and id")
	require.Equal(t, orgID, got.OrgID, "GetLoopByID should return the org-scoped row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_CreatePassAndLatestByLoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	loopID := uuid.New()

	mock.ExpectQuery("INSERT INTO session_review_loop_passes").
		WithArgs(anyArgs(16)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "review_started_at"}).AddRow(uuid.New(), time.Now().UTC()))

	pass := &models.SessionReviewLoopPass{
		OrgID:     orgID,
		LoopID:    loopID,
		SessionID: sessionID,
		PassIndex: 1,
		Status:    models.ReviewLoopPassStatusReviewing,
	}
	err = store.CreatePass(context.Background(), pass)
	require.NoError(t, err, "CreatePass should insert a pass row")

	reviewMessageID := int64(42)
	reviewStartedAt := time.Now().UTC()
	rows := pgxmock.NewRows(reviewLoopPassColumnsForTest()).AddRow(
		pass.ID, orgID, loopID, sessionID, 1, &reviewMessageID, nil, nil, "reviewing", nil, nil, nil,
		&reviewStartedAt, nil, nil, nil, nil,
	)
	mock.ExpectQuery("SELECT .+ FROM session_review_loop_passes WHERE org_id = @org_id AND loop_id = @loop_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(rows)

	got, err := store.GetLatestPass(context.Background(), orgID, loopID)
	require.NoError(t, err, "GetLatestPass should filter by org and loop")
	require.Equal(t, pass.ID, got.ID, "GetLatestPass should return the newest pass")
	require.Equal(t, reviewMessageID, *got.ReviewMessageID, "GetLatestPass should scan message ids")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_GetFreshCleanPublicationLoopUsesExactEvidence(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID, loopID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	revision := int64(14)
	headSHA := "0123456789abcdef0123456789abcdef01234567"
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT .+ FROM session_review_loops").
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows(reviewLoopColumnsForTest()).AddRow(
			loopID, orgID, sessionID, nil, nil, "clean", "publication", &changesetID, &revision, &headSHA,
			"claude_code", 2, "minimal", 2, true, nil, nil, nil, nil, nil, nil, now, &now,
		))

	loop, err := NewSessionReviewLoopStore(mock).GetFreshCleanPublicationLoop(
		context.Background(), orgID, sessionID, changesetID, revision, headSHA,
	)
	require.NoError(t, err, "fresh review lookup should find exact revision-bound evidence")
	require.Equal(t, loopID, loop.ID, "fresh review lookup should return the matching clean publication loop")
	require.Equal(t, &revision, loop.WorkspaceRevision, "fresh review lookup should preserve workspace revision evidence")
	require.Equal(t, &headSHA, loop.DesiredHeadSHA, "fresh review lookup should preserve desired head evidence")
	require.NoError(t, mock.ExpectationsWereMet(), "fresh review lookup should filter by tenant, session, changeset, revision, and head")
}

func TestSessionReviewLoopStore_RefreshPublicationEvidenceAdvancesPublishedCheckpoint(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, loopID := uuid.New(), uuid.New()
	revision := int64(15)
	headSHA := "0123456789abcdef0123456789abcdef01234567"
	args := pgx.NamedArgs{
		"org_id": orgID, "loop_id": loopID, "workspace_revision": revision,
		"desired_head_sha": headSHA,
	}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE session_review_loops[\s\S]+workspace_revision = @workspace_revision[\s\S]+desired_head_sha = @desired_head_sha`).
		WithArgs(args).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE session_publications[\s\S]+review_workspace_revision = @workspace_revision[\s\S]+review_desired_head_sha = @desired_head_sha[\s\S]+desired_head_sha = @desired_head_sha[\s\S]+published_head_sha = @desired_head_sha`).
		WithArgs(args).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err = NewSessionReviewLoopStore(mock).RefreshPublicationEvidence(context.Background(), orgID, loopID, revision, headSHA)
	require.NoError(t, err, "refreshing pushed review fixes should atomically advance review and publication-owned checkpoints")
	require.NoError(t, mock.ExpectationsWereMet(), "review evidence refresh should commit both checkpoint updates")
}

func TestSessionReviewLoopStore_PublicationCleanTransitionIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID, loopID, passID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","session_id":"` + sessionID.String() + `"}`)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE session_review_loop_passes").WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("UPDATE session_review_loops").WithArgs(anyArgs(4)...).
		WillReturnRows(pgxmock.NewRows([]string{"session_id", "source"}).AddRow(sessionID, models.ReviewLoopSourcePublication))
	mock.ExpectQuery(`UPDATE session_publications AS publication[\s\S]+COALESCE\(publication\.completed_at, now\(\)\)[\s\S]+ELSE publication\.completed_at END`).WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}).AddRow(payload, models.SessionPublicationJobQueueAgent, changesetID))
	mock.ExpectQuery("INSERT INTO jobs").WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectQuery("SELECT request_payload, job_queue, changeset_id").WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}))
	mock.ExpectCommit()

	err = NewSessionReviewLoopStore(mock).MarkPassClean(
		context.Background(), orgID, loopID, passID, models.ReviewLoopDecisionClean, "REVIEW_CLEAN",
	)
	require.NoError(t, err, "clean publication review should advance the gate and enqueue in one transaction")
	require.NoError(t, mock.ExpectationsWereMet(), "clean publication review should commit loop, gate, and original open_pr enqueue atomically")
}

func TestSessionReviewLoopStore_ListStrandedPublicationLoopsRequiresInactiveThreadWithoutLiveJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, loopID, threadID := uuid.New(), uuid.New(), uuid.New()
	inactiveBefore := time.Now().UTC()
	mock.ExpectQuery(`SELECT loop\.id AS loop_id, loop\.thread_id[\s\S]+loop\.org_id = @org_id[\s\S]+loop\.source = 'publication'[\s\S]+thread\.status IN \('idle', 'completed', 'failed', 'cancelled'\)[\s\S]+jobs\.status IN \('pending', 'running'\)`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "inactive_before": inactiveBefore, "limit": 25}).
		WillReturnRows(pgxmock.NewRows([]string{"loop_id", "thread_id"}).AddRow(loopID, threadID))

	loops, err := NewSessionReviewLoopStore(mock).ListStrandedPublicationLoops(
		context.Background(), orgID, inactiveBefore, 25,
	)

	require.NoError(t, err, "stranded publication review lookup should succeed")
	require.Equal(t, []StrandedPublicationReviewLoop{{LoopID: loopID, ThreadID: threadID}}, loops, "lookup should return the inactive loop without live continuation work")
	require.NoError(t, mock.ExpectationsWereMet(), "stranded lookup should enforce tenant, publication, thread, age, and live-job predicates")
}

func TestSessionReviewLoopStore_RestartStrandedPublicationLoopIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, loopID, changesetID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	inactiveBefore := time.Now().UTC()
	summary := "restart stranded review"
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","session_id":"` + sessionID.String() + `"}`)

	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE session_review_loops AS loop[\s\S]+loop\.org_id = @org_id[\s\S]+thread\.status IN \('idle', 'completed', 'failed', 'cancelled'\)[\s\S]+jobs\.status IN \('pending', 'running'\)`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "loop_id": loopID, "inactive_before": inactiveBefore, "summary": summary}).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))
	mock.ExpectQuery(`UPDATE session_publications[\s\S]+review_loop_id = NULL[\s\S]+org_id = @org_id AND session_id = @session_id`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "loop_id": loopID}).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}).
			AddRow(payload, models.SessionPublicationJobQueueAgent, changesetID))
	mock.ExpectQuery("INSERT INTO jobs").WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	restarted, err := NewSessionReviewLoopStore(mock).RestartStrandedPublicationLoop(
		context.Background(), orgID, loopID, inactiveBefore, summary,
	)

	require.NoError(t, err, "stranded publication review restart should succeed")
	require.True(t, restarted, "eligible stranded review should be restarted")
	require.NoError(t, mock.ExpectationsWereMet(), "restart should retire the loop, clear its evidence, and enqueue the original publication atomically")
}

func TestFinishPublicationReviewInvalidatesStaleCleanEvidence(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, changesetID, loopID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	payload := json.RawMessage(`{"changeset_id":"` + changesetID.String() + `"}`)
	mock.ExpectQuery("UPDATE session_publications AS publication[\\s\\S]+@status <> 'clean'[\\s\\S]+session.workspace_revision = loop.workspace_revision[\\s\\S]+changeset.head_sha = loop.desired_head_sha").
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}))
	mock.ExpectQuery("UPDATE session_publications[\\s\\S]+review_loop_id = NULL").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}).AddRow(payload, models.SessionPublicationJobQueueAgent, changesetID))
	mock.ExpectQuery("INSERT INTO jobs").WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	err = finishPublicationReviewOn(context.Background(), mock, orgID, loopID, models.ReviewLoopStatusClean)
	require.NoError(t, err, "stale clean evidence should be invalidated and rescheduled")
	require.NoError(t, mock.ExpectationsWereMet(), "stale evidence should require both current revision and current head before the gate passes")
}

func TestFinishPublicationReviewBlocksNonCleanResultEvenWhenEvidenceMoved(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, changesetID, loopID := uuid.New(), uuid.New(), uuid.New()
	payload := json.RawMessage(`{"changeset_id":"` + changesetID.String() + `"}`)
	// Every evidence comparison must sit behind the `@status <> 'clean'` guard.
	// A non-clean result that declined to block on drifted evidence would strand
	// the publication pending against an already-finished loop.
	mock.ExpectQuery("UPDATE session_publications AS publication[\\s\\S]+@status <> 'clean'[\\s\\S]+publication.review_workspace_revision = loop.workspace_revision[\\s\\S]+publication.review_desired_head_sha = loop.desired_head_sha[\\s\\S]+session.workspace_revision = loop.workspace_revision").
		WithArgs(anyArgs(5)...).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}).
			AddRow(payload, models.SessionPublicationJobQueueAgent, changesetID))

	err = finishPublicationReviewOn(context.Background(), mock, orgID, loopID, models.ReviewLoopStatusNeedsHumanDecision)
	require.NoError(t, err, "a non-clean terminal review should block its linked publication without requiring fresh clean evidence")
	require.NoError(t, mock.ExpectationsWereMet(), "non-clean review completion should not enqueue publication")
}

func TestFinishPublicationReviewMapsLoopOutcomeToOneGateMeaning(t *testing.T) {
	t.Parallel()

	// The gate value alone tells a reader whether the publication is settled,
	// so each gate must imply exactly one publication state — the same one
	// SetReviewGate writes for it. A cancelled loop is somebody stopping the
	// review, not a failure, so it stays live and awaits a human.
	tests := []struct {
		name          string
		status        models.ReviewLoopStatus
		expectedGate  models.SessionPublicationReviewGateState
		expectedState models.SessionPublicationState
	}{
		{
			name: "clean", status: models.ReviewLoopStatusClean,
			expectedGate: models.SessionPublicationReviewGatePassed, expectedState: models.SessionPublicationStateReadyToPublish,
		},
		{
			name: "needs human decision", status: models.ReviewLoopStatusNeedsHumanDecision,
			expectedGate: models.SessionPublicationReviewGateNeedsHuman, expectedState: models.SessionPublicationStateReviewPending,
		},
		{
			name: "cancelled", status: models.ReviewLoopStatusCancelled,
			expectedGate: models.SessionPublicationReviewGateNeedsHuman, expectedState: models.SessionPublicationStateReviewPending,
		},
		{
			name: "failed", status: models.ReviewLoopStatusFailed,
			expectedGate: models.SessionPublicationReviewGateFailed, expectedState: models.SessionPublicationStateTerminalFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create a database mock")
			t.Cleanup(mock.Close)
			orgID, changesetID, loopID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			payload := json.RawMessage(`{"changeset_id":"` + changesetID.String() + `"}`)
			mock.ExpectQuery(`UPDATE session_publications AS publication[\s\S]+COALESCE\(publication\.completed_at, now\(\)\)[\s\S]+ELSE publication\.completed_at END`).
				WithArgs(pgx.NamedArgs{
					"org_id": orgID, "loop_id": loopID, "gate": tt.expectedGate,
					"state": tt.expectedState, "status": tt.status,
				}).
				WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}).
					AddRow(payload, models.SessionPublicationJobQueueAgent, changesetID))
			if tt.status == models.ReviewLoopStatusClean {
				mock.ExpectQuery("INSERT INTO jobs").WithArgs(anyArgs(6)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			}

			err = finishPublicationReviewOn(context.Background(), mock, orgID, loopID, tt.status)
			require.NoError(t, err, "a terminal review loop should settle its linked publication")
			require.NoError(t, mock.ExpectationsWereMet(), "each review outcome should write one gate and its matching publication state")
		})
	}
}

func TestSessionReviewLoopStore_TerminalManualLoopResumesParkedPublication(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID, loopID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	payload := json.RawMessage(`{"changeset_id":"` + changesetID.String() + `"}`)
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE session_review_loops").WithArgs(anyArgs(4)...).
		WillReturnRows(pgxmock.NewRows([]string{"session_id", "source"}).AddRow(sessionID, models.ReviewLoopSourceManual))
	mock.ExpectQuery("SELECT request_payload, job_queue, changeset_id").WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}).AddRow(payload, models.SessionPublicationJobQueueAgent, changesetID))
	mock.ExpectQuery("INSERT INTO jobs").WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	err = NewSessionReviewLoopStore(mock).MarkLoopNeedsHumanDecision(context.Background(), orgID, loopID, "manual review stopped")
	require.NoError(t, err, "terminal manual review should atomically resume the parked publication intent")
	require.NoError(t, mock.ExpectationsWereMet(), "parked publication should reuse its original queue and payload after the session review slot opens")
}

func TestSessionReviewLoopStore_CreateLoopWithInitialPassIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	startedAt := time.Now().UTC()
	reviewStartedAt := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO session_review_loops").
		WithArgs(anyArgs(20)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "started_at"}).AddRow(loopID, startedAt))
	mock.ExpectQuery("INSERT INTO session_review_loop_passes").
		WithArgs(anyArgs(16)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "review_started_at"}).AddRow(passID, reviewStartedAt))
	mock.ExpectCommit()

	loop := &models.SessionReviewLoop{
		OrgID:     orgID,
		SessionID: sessionID,
		ThreadID:  &threadID,
		Status:    models.ReviewLoopStatusRunning,
		Source:    models.ReviewLoopSourceManual,
		AgentType: models.AgentTypeCodex,
		MaxPasses: 2,
	}
	pass := &models.SessionReviewLoopPass{
		OrgID:     orgID,
		SessionID: sessionID,
		PassIndex: 1,
		Status:    models.ReviewLoopPassStatusReviewing,
	}

	err = store.CreateLoopWithInitialPass(context.Background(), loop, pass)

	require.NoError(t, err, "CreateLoopWithInitialPass should create both rows atomically")
	require.Equal(t, loopID, loop.ID, "CreateLoopWithInitialPass should return the loop id")
	require.Equal(t, loopID, pass.LoopID, "CreateLoopWithInitialPass should attach the first pass to the loop")
	require.Equal(t, passID, pass.ID, "CreateLoopWithInitialPass should return the pass id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_CreateLoopWithInitialPassRollsBackOnPassFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO session_review_loops").
		WithArgs(anyArgs(20)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "started_at"}).AddRow(uuid.New(), time.Now().UTC()))
	mock.ExpectQuery("INSERT INTO session_review_loop_passes").
		WithArgs(anyArgs(16)...).
		WillReturnError(errors.New("pass insert failed"))
	mock.ExpectRollback()

	loop := &models.SessionReviewLoop{
		OrgID:     orgID,
		SessionID: sessionID,
		Status:    models.ReviewLoopStatusRunning,
		Source:    models.ReviewLoopSourceManual,
		AgentType: models.AgentTypeCodex,
		MaxPasses: 2,
	}
	pass := &models.SessionReviewLoopPass{
		OrgID:     orgID,
		SessionID: sessionID,
		PassIndex: 1,
		Status:    models.ReviewLoopPassStatusReviewing,
	}

	err = store.CreateLoopWithInitialPass(context.Background(), loop, pass)

	require.Error(t, err, "CreateLoopWithInitialPass should fail when pass creation fails")
	require.ErrorContains(t, err, "pass insert failed", "CreateLoopWithInitialPass should surface the pass insert failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_MarkPassCleanAndEnqueueOpenPRIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	jobID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE session_review_loop_passes").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectManualReviewLoopTerminal(mock)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	payload := map[string]any{"session_id": uuid.New().String(), "org_id": orgID.String()}
	err = store.MarkPassCleanAndEnqueueOpenPR(context.Background(), orgID, loopID, passID, models.ReviewLoopDecisionClean, "clean", payload, "open_pr:test")
	require.NoError(t, err, "clean terminal write should atomically enqueue open_pr")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_MarkPassCleanAndEnqueueOpenPRRollsBackOnEnqueueFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE session_review_loop_passes").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectManualReviewLoopTerminal(mock)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(anyArgs(6)...).
		WillReturnError(errors.New("enqueue failed"))
	mock.ExpectRollback()

	payload := map[string]any{"session_id": uuid.New().String(), "org_id": orgID.String()}
	err = store.MarkPassCleanAndEnqueueOpenPR(context.Background(), orgID, loopID, passID, models.ReviewLoopDecisionClean, "clean", payload, "open_pr:test")
	require.Error(t, err, "clean terminal write should fail when open_pr cannot be enqueued")
	require.ErrorContains(t, err, "enqueue open_pr", "clean terminal write should identify the failed enqueue")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_MarkPassNeedsHumanDecisionAndEnqueueOpenPRIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	jobID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE session_review_loop_passes").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectManualReviewLoopTerminal(mock)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	payload := map[string]any{"session_id": uuid.New().String(), "org_id": orgID.String()}
	err = store.MarkPassNeedsHumanDecisionAndEnqueueOpenPR(context.Background(), orgID, loopID, passID, models.ReviewLoopDecisionNeedsFix, "needs human", payload, "open_pr:test")
	require.NoError(t, err, "needs-human terminal write should persist pass decision and enqueue open_pr atomically")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_MarkPassNeedsHumanDecisionIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE session_review_loop_passes").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectManualReviewLoopTerminal(mock)
	mock.ExpectCommit()

	err = store.MarkPassNeedsHumanDecision(context.Background(), orgID, loopID, passID, models.ReviewLoopDecisionNeedsFix, "needs human")
	require.NoError(t, err, "needs-human terminal write should persist pass decision and loop state atomically")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewLoopStore_MarkLoopFailedAndEnqueueOpenPRIsAtomic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionReviewLoopStore(mock)
	orgID := uuid.New()
	loopID := uuid.New()
	jobID := uuid.New()

	mock.ExpectBegin()
	expectManualReviewLoopTerminal(mock)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	payload := map[string]any{"session_id": uuid.New().String(), "org_id": orgID.String()}
	err = store.MarkLoopFailedAndEnqueueOpenPR(context.Background(), orgID, loopID, "failed", payload, "open_pr:test")
	require.NoError(t, err, "failed terminal write should enqueue open_pr atomically")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func reviewLoopColumnsForTest() []string {
	return []string{
		"id", "org_id", "session_id", "automation_run_id", "thread_id", "status", "source",
		"changeset_id", "workspace_revision", "desired_head_sha", "agent_type",
		"max_passes", "fix_mode", "completed_passes", "review_required", "bypassed_by_user_id", "bypass_reason",
		"loop_start_checkpoint_key", "latest_checkpoint_key", "latest_summary", "started_by_user_id", "started_at", "completed_at",
	}
}

func expectManualReviewLoopTerminal(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery("UPDATE session_review_loops").
		WithArgs(anyArgs(4)...).
		WillReturnRows(pgxmock.NewRows([]string{"session_id", "source"}).AddRow(uuid.New(), "manual"))
	mock.ExpectQuery("SELECT request_payload, job_queue, changeset_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"request_payload", "job_queue", "changeset_id"}))
}

func reviewLoopPassColumnsForTest() []string {
	return []string{
		"id", "org_id", "loop_id", "session_id", "pass_index", "review_message_id", "decision_message_id", "fix_message_id",
		"status", "agent_decision", "review_output", "fix_summary", "review_started_at", "review_completed_at",
		"fix_started_at", "fix_completed_at", "summary",
	}
}
