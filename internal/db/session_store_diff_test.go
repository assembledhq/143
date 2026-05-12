package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestParseDiffStatsPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      json.RawMessage
		expected diffStatsPayload
	}{
		{
			name:     "returns zero value for empty payload",
			raw:      nil,
			expected: diffStatsPayload{},
		},
		{
			name:     "returns zero value for invalid payload",
			raw:      json.RawMessage(`{"added":"bad"}`),
			expected: diffStatsPayload{},
		},
		{
			name:     "parses valid payload",
			raw:      json.RawMessage(`{"added":3,"removed":2,"files_changed":1}`),
			expected: diffStatsPayload{Added: 3, Removed: 2, FilesChanged: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, parseDiffStatsPayload(tt.raw), "parseDiffStatsPayload should return the expected payload")
		})
	}
}

func TestSessionStore_UpdateResult_WithDiffSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotID := uuid.New()
	collectedAt := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	diff := "--- a/a.go\n+++ b/a.go\n"
	headSHA := "head123"
	baseSHA := "base123"
	result := &models.SessionResult{
		Diff:               &diff,
		DiffBaseCommitSHA:  &baseSHA,
		DiffHeadCommitSHA:  &headSHA,
		DiffWorkspaceDirty: true,
		DiffCollectedAt:    &collectedAt,
		DiffSource:         "review",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).AddRow(
				newAgentSessionRow(sessionID, uuid.New(), orgID, collectedAt)...,
			),
		)
	mock.ExpectQuery("INSERT INTO session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(snapshotID))
	mock.ExpectExec("UPDATE sessions\\s+SET latest_diff_snapshot_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err = store.UpdateResult(context.Background(), orgID, sessionID, "completed", result)
	require.NoError(t, err, "UpdateResult should persist a diff snapshot when provenance is present")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateResult_WithDiffSnapshotBeginFailure(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(noTxDB{})
	diff := "diff"
	baseSHA := "base123"
	result := &models.SessionResult{
		Diff:               &diff,
		DiffBaseCommitSHA:  &baseSHA,
		DiffWorkspaceDirty: true,
	}

	err := store.UpdateResult(context.Background(), uuid.New(), uuid.New(), "completed", result)
	require.Error(t, err, "UpdateResult should return an error when transactions are unavailable")
	require.Contains(t, err.Error(), "does not support transactions", "UpdateResult should surface the missing transaction support")
}

func TestSessionStore_UpdateTurnComplete_WithDiffSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotID := uuid.New()
	diff := "--- a/a.go\n+++ b/a.go\n"
	baseSHA := "base123"
	result := &models.SessionResult{
		Diff:               &diff,
		DiffBaseCommitSHA:  &baseSHA,
		DiffWorkspaceDirty: true,
	}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE sessions.+SET status = 'idle'.+pr_creation_state = 'idle', pr_creation_error = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(snapshotID))
	mock.ExpectExec("UPDATE sessions\\s+SET latest_diff_snapshot_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err = store.UpdateTurnComplete(context.Background(), orgID, sessionID, 2, result, "agent-123", "snap-key")
	require.NoError(t, err, "UpdateTurnComplete should persist a diff snapshot when provenance is present")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateTurnComplete_WithDiffSnapshotInsertFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	diff := "diff"
	baseSHA := "base123"
	result := &models.SessionResult{
		Diff:              &diff,
		DiffBaseCommitSHA: &baseSHA,
	}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE sessions.+SET status = 'idle'.+pr_creation_state = 'idle', pr_creation_error = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("insert failed"))
	mock.ExpectRollback()

	err = store.UpdateTurnComplete(context.Background(), uuid.New(), uuid.New(), 2, result, "agent-123", "snap-key")
	require.Error(t, err, "UpdateTurnComplete should return an error when snapshot insertion fails")
	require.Contains(t, err.Error(), "insert session diff snapshot", "UpdateTurnComplete should surface the snapshot insertion failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_MarkLatestDiffSnapshotPushed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	headSHA := "abc1234567890abcdef1234567890abcdef12345"

	mock.ExpectExec("UPDATE session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.MarkLatestDiffSnapshotPushed(context.Background(), orgID, sessionID, headSHA)
	require.NoError(t, err, "MarkLatestDiffSnapshotPushed should update the latest diff snapshot state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionStore_UpdateResult_PreservesDiffWhenNil verifies that the
// UPDATE sessions ... SET diff = COALESCE(@diff, diff) clause is in place,
// so that a turn that did not collect a fresh diff (sessiondiff.Collect
// returned ErrNoBaseCommitSHA, the agent produced no changes against the
// base, etc. — strPtr converts the empty string to a nil *string) does not
// blank out the previously persisted authoritative diff. This is the bug
// users hit when pushing a PR or resolving conflicts: every clean-tree
// continue turn used to overwrite the Changes tab with NULL.
func TestSessionStore_UpdateResult_PreservesDiffWhenNil(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	collectedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// result.Diff is nil — the prior turn's diff must be preserved.
	result := &models.SessionResult{}

	// The SQL must contain `diff = COALESCE(@diff, diff)` so a NULL @diff
	// leaves the existing value intact. Same for diff_stats and
	// diff_collected_at to keep them consistent with the preserved diff.
	mock.ExpectQuery(`UPDATE sessions[\s\S]+diff = COALESCE\(@diff, diff\)[\s\S]+diff_collected_at = COALESCE\(@diff_collected_at, diff_collected_at\)[\s\S]+diff_stats = COALESCE\(@diff_stats, diff_stats\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).AddRow(
				newAgentSessionRow(sessionID, uuid.New(), orgID, collectedAt)...,
			),
		)

	err = store.UpdateResult(context.Background(), orgID, sessionID, "completed", result)
	require.NoError(t, err, "UpdateResult should succeed when diff is nil")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionStore_UpdateTurnComplete_PreservesDiffWhenNil mirrors the
// UpdateResult test for the continue-session write path — the same COALESCE
// guard applies.
func TestSessionStore_UpdateTurnComplete_PreservesDiffWhenNil(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	result := &models.SessionResult{}

	mock.ExpectExec(`UPDATE sessions[\s\S]+SET status = 'idle'[\s\S]+diff = COALESCE\(@diff, diff\)[\s\S]+diff_collected_at = COALESCE\(@diff_collected_at, diff_collected_at\)[\s\S]+diff_stats = COALESCE\(@diff_stats, diff_stats\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateTurnComplete(context.Background(), orgID, sessionID, 2, result, "agent-123", "snap-key")
	require.NoError(t, err, "UpdateTurnComplete should succeed when diff is nil")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateBaseCommitSHA(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectExec("UPDATE sessions SET base_commit_sha").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateBaseCommitSHA(context.Background(), orgID, sessionID, "base123")
	require.NoError(t, err, "UpdateBaseCommitSHA should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
