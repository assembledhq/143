package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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
		WithArgs(anyArgs(17)...).
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
		loop.ID, orgID, sessionID, nil, &threadID, "running", "manual", "claude_code", 2, "minimal", 0,
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
		WithArgs(anyArgs(17)...).
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
		WithArgs(anyArgs(17)...).
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
	mock.ExpectExec("UPDATE session_review_loops").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
	mock.ExpectExec("UPDATE session_review_loops").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
	mock.ExpectExec("UPDATE session_review_loops").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
	mock.ExpectExec("UPDATE session_review_loops").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
	mock.ExpectExec("UPDATE session_review_loops").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
		"id", "org_id", "session_id", "automation_run_id", "thread_id", "status", "source", "agent_type",
		"max_passes", "fix_mode", "completed_passes", "review_required", "bypassed_by_user_id", "bypass_reason",
		"loop_start_checkpoint_key", "latest_checkpoint_key", "latest_summary", "started_by_user_id", "started_at", "completed_at",
	}
}

func reviewLoopPassColumnsForTest() []string {
	return []string{
		"id", "org_id", "loop_id", "session_id", "pass_index", "review_message_id", "decision_message_id", "fix_message_id",
		"status", "agent_decision", "review_output", "fix_summary", "review_started_at", "review_completed_at",
		"fix_started_at", "fix_completed_at", "summary",
	}
}
