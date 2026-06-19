package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestEvalBootstrapStore_Create(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO eval_bootstrap_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(newID, now))

	store := NewEvalBootstrapStore(mock)
	run := &models.EvalBootstrapRun{
		OrgID:     orgID,
		RepoID:    repoID,
		Status:    models.EvalBootstrapStatusPending,
		CreatedBy: &userID,
	}

	err = store.Create(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, newID, run.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBootstrapStore_GetByID(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		orgID := uuid.New()
		runID := uuid.New()
		repoID := uuid.New()
		now := time.Now()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "status", "candidates", "session_id",
				"thread_id", "created_by", "created_at", "completed_at", "error_message",
			}).AddRow(runID, orgID, repoID, "completed", []byte(`[]`), nil, nil, nil, now, nil, nil))

		store := NewEvalBootstrapStore(mock)
		run, err := store.GetByID(context.Background(), orgID, runID)
		require.NoError(t, err)
		require.Equal(t, runID, run.ID)
		require.Equal(t, models.EvalBootstrapStatus("completed"), run.Status)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		orgID := uuid.New()
		runID := uuid.New()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "status", "candidates", "session_id",
				"thread_id", "created_by", "created_at", "completed_at", "error_message",
			}))

		store := NewEvalBootstrapStore(mock)
		_, err = store.GetByID(context.Background(), orgID, runID)
		require.Error(t, err)
	})
}

func TestEvalBootstrapStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_bootstrap_runs SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalBootstrapStore(mock)
	err = store.UpdateStatus(context.Background(), orgID, runID, models.EvalBootstrapStatusRunning, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBootstrapStore_UpdateResult(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_bootstrap_runs").
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalBootstrapStore(mock)
	err = store.UpdateResult(context.Background(), orgID, runID,
		models.EvalBootstrapStatusCompleted, []byte(`[{"pr_number":1}]`), nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBootstrapStore_CreateCandidate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	candidateID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	payload := json.RawMessage(`{"pr_number":42,"pr_title":"Fix flaky checkout","base_commit_sha":"abc123","solution_commit_sha":"def456","solution_diff":"diff --git","issue_description":"Checkout flakes","scoring_criteria":[],"complexity":"moderate","fitness_score":0.91,"fitness_reasoning":"Good regression"}`)

	mock.ExpectQuery("INSERT INTO eval_bootstrap_candidates").
		WithArgs(anyArgs(17)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "thread_id", "repo_id", "candidate_index", "status", "created_at"}).
			AddRow(candidateID, sessionID, &threadID, repoID, 0, string(models.EvalBootstrapCandidateStatusProposed), now))

	store := NewEvalBootstrapStore(mock)
	candidate := &models.EvalBootstrapCandidateRow{
		OrgID:          orgID,
		BootstrapRunID: runID,
		Payload:        payload,
		CreatedByTool:  "eval_add",
	}

	err = store.CreateCandidate(context.Background(), candidate)
	require.NoError(t, err, "CreateCandidate should insert a proposed candidate")
	require.Equal(t, candidateID, candidate.ID, "CreateCandidate should scan the generated candidate ID")
	require.Equal(t, sessionID, candidate.SessionID, "CreateCandidate should scan the linked session ID")
	require.NotNil(t, candidate.ThreadID, "CreateCandidate should scan the linked thread ID")
	require.Equal(t, threadID, *candidate.ThreadID, "CreateCandidate should scan the linked thread ID")
	require.Equal(t, repoID, candidate.RepoID, "CreateCandidate should scan the linked repo ID")
	require.Equal(t, 0, candidate.CandidateIndex, "CreateCandidate should scan the candidate index")
	require.Equal(t, models.EvalBootstrapCandidateStatusProposed, candidate.Status, "CreateCandidate should default status to proposed")
	require.Equal(t, now, candidate.CreatedAt, "CreateCandidate should scan created_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalBootstrapStore_ListCandidatesByRun(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	candidateID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	criteria := json.RawMessage(`[{"name":"fixes","grader_type":"llm_judge","weight":1}]`)
	evidence := json.RawMessage(`{"changed_files":["auth.go"]}`)

	mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_candidates WHERE org_id = @org_id AND bootstrap_run_id = @bootstrap_run_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "bootstrap_run_id", "session_id", "thread_id", "repo_id", "candidate_index",
			"pr_number", "pr_title", "base_commit_sha", "solution_commit_sha", "solution_diff",
			"issue_description", "scoring_criteria", "complexity", "fitness_score", "fitness_reasoning",
			"evidence", "warnings", "payload", "status", "rejection_reason", "created_by_tool",
			"reviewed_by", "reviewed_at", "accepted_task_id", "created_at",
		}).AddRow(candidateID, orgID, runID, sessionID, &threadID, repoID, 0, 42, "Fix auth", "abc123", "def456",
			"diff --git", "Auth broken", criteria, string(models.EvalComplexityModerate), 0.9, "good", evidence,
			[]string{"No test command"}, []byte(`{"pr_number":42}`), string(models.EvalBootstrapCandidateStatusProposed), nil, "eval_add", nil, nil, nil, now))

	store := NewEvalBootstrapStore(mock)
	candidates, err := store.ListCandidatesByRun(context.Background(), orgID, runID)
	require.NoError(t, err, "ListCandidatesByRun should return candidates for the org and bootstrap run")
	require.Equal(t, []models.EvalBootstrapCandidateRow{{
		ID:                candidateID,
		OrgID:             orgID,
		BootstrapRunID:    runID,
		SessionID:         sessionID,
		ThreadID:          &threadID,
		RepoID:            repoID,
		CandidateIndex:    0,
		PRNumber:          42,
		PRTitle:           "Fix auth",
		BaseCommitSHA:     "abc123",
		SolutionCommitSHA: "def456",
		SolutionDiff:      "diff --git",
		IssueDescription:  "Auth broken",
		ScoringCriteria:   criteria,
		Complexity:        models.EvalComplexityModerate,
		FitnessScore:      0.9,
		FitnessReasoning:  "good",
		Evidence:          evidence,
		Warnings:          []string{"No test command"},
		Payload:           json.RawMessage(`{"pr_number":42}`),
		Status:            models.EvalBootstrapCandidateStatusProposed,
		CreatedByTool:     "eval_add",
		CreatedAt:         now,
	}}, candidates, "ListCandidatesByRun should scan the expected candidate rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalBootstrapStore_UpdateCandidateReview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	candidateID := uuid.New()
	reviewerID := uuid.New()
	reason := "Needs a deterministic test command."

	mock.ExpectExec("UPDATE eval_bootstrap_candidates").
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalBootstrapStore(mock)
	err = store.UpdateCandidateReview(context.Background(), orgID, candidateID,
		models.EvalBootstrapCandidateStatusNeedsRevision, &reason, &reviewerID)
	require.NoError(t, err, "UpdateCandidateReview should persist reviewer status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalBootstrapStore_GetBySessionThread(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE org_id = @org_id AND session_id = @session_id AND thread_id = @thread_id").
		WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "status", "candidates", "session_id",
			"thread_id", "created_by", "created_at", "completed_at", "error_message",
		}).AddRow(runID, orgID, repoID, string(models.EvalBootstrapStatusRunning), nil, &sessionID, &threadID, nil, now, nil, nil))

	store := NewEvalBootstrapStore(mock)
	run, err := store.GetBySessionThread(context.Background(), orgID, sessionID, threadID)
	require.NoError(t, err, "GetBySessionThread should find the bootstrap run for the scoped session thread")
	require.Equal(t, runID, run.ID, "GetBySessionThread should return the expected bootstrap run")
	require.NotNil(t, run.ThreadID, "GetBySessionThread should scan thread_id")
	require.Equal(t, threadID, *run.ThreadID, "GetBySessionThread should preserve thread_id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
