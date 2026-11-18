package db

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionTurnPersistenceInTx(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, variant, failure string }{
		{name: "authoritative snapshot"},
		{name: "nil result normalization", variant: "nil"},
		{name: "head only persists without snapshot", variant: "head only"},
		{name: "empty diff with base remains authoritative", variant: "empty diff"},
		{name: "row error", failure: "row"},
		{name: "head error", failure: "head"},
		{name: "snapshot insertion error", failure: "snapshot"},
		{name: "workspace revision error", failure: "revision"},
		{name: "workspace revision collection error", failure: "revision scan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionTurnPersistenceFixture(tt.variant)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this test caller's concrete transaction")
			t.Cleanup(mock.Close)
			mock.ExpectBegin()
			owner, err := mock.Begin(context.Background())
			require.NoError(t, err, "the caller alone begins the outer transaction")
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			expectedEffects, expectedErr := expectSessionTurnPersistence(t, mock, fixture, tt.failure)
			effects, persistenceErr := store.UpdateTurnCompleteInTx(context.Background(), fixture.orgID, tx, fixture.sessionID, 2, fixture.result, "synthetic-agent", "synthetic-checkpoint")
			if expectedErr != nil {
				require.Error(t, persistenceErr, "every persistence-stage failure must propagate to the caller")
				if tt.failure != "revision scan" {
					require.ErrorIs(t, persistenceErr, expectedErr, "persistence errors must retain their original cause")
				}
				require.Equal(t, SessionPersistenceEffects{}, effects, "failed units must not expose partially collected effects")
				mock.ExpectRollback()
				require.NoError(t, owner.Rollback(context.Background()), "only the caller aborts its whole failed unit")
			} else {
				require.NoError(t, persistenceErr, "the standalone operation must use only its supplied transaction")
				require.Equal(t, expectedEffects, effects, "only an authoritative snapshot may return sealed workspace effect data")
				mock.ExpectQuery("^SELECT 42$").WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow(42))
				var value int
				require.NoError(t, tx.QueryRow(context.Background(), "SELECT 42").Scan(&value), "the outer transaction must remain available for more work")
				require.Equal(t, 42, value, "subsequent work must compose in the same still-open transaction")
				require.Empty(t, redis.Keys(), "enabled streams must remain silent before the caller's commit")
				mock.ExpectCommit()
				require.NoError(t, owner.Commit(context.Background()), "only the outer caller commits the complete unit")
				require.Empty(t, redis.Keys(), "a commit alone must not invoke captured passive effects")
				store.PublishCommittedSessionPersistenceEffects(context.Background(), fixture.orgID, effects)
				requireSessionPersistenceWorkspaceEvent(t, redis, fixture, expectedEffects.workspaceChanged)
			}
			require.Empty(t, tx.settlements, "standalone persistence must never begin a savepoint, commit, abort or retry")
			require.NoError(t, mock.ExpectationsWereMet(), "the exact legacy row, head and snapshot sequence must retain every tenant-scoped argument")
		})
	}
}

func TestSessionTurnPersistenceInTxRejectsNilTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *sessionPersistenceOwnedTx
	tests := []struct {
		name string
		tx   pgx.Tx
	}{
		{name: "nil interface"},
		{name: "typed nil", tx: typedNil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionPersistenceFixture()
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			effects, err := store.UpdateTurnCompleteInTx(context.Background(), fixture.orgID, tt.tx, fixture.sessionID, 2, fixture.result, "synthetic-agent", "synthetic-checkpoint")
			require.ErrorContains(t, err, "transaction is required", "invalid transactions must fail before any fallback database access")
			require.Equal(t, SessionPersistenceEffects{}, effects, "invalid transactions cannot produce accepted effect data")
			require.Empty(t, redis.Keys(), "invalid transactions must not notify readers")
		})
	}
}

func TestSessionTurnPersistenceLegacyFastPath(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, variant string }{
		{name: "nil result uses direct DBTX", variant: "nil"},
		{name: "diff without base uses direct DBTX", variant: "no base"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionTurnPersistenceFixture(tt.variant)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the legacy direct-database mock")
			t.Cleanup(mock.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, mock)
			_, expectedErr := expectSessionTurnPersistence(t, mock, fixture, "")
			require.NoError(t, expectedErr, "the direct path must require only its original row mutation")
			require.NoError(t, store.UpdateTurnComplete(context.Background(), fixture.orgID, fixture.sessionID, 2, fixture.result, "synthetic-agent", "synthetic-checkpoint"), "diff-free and head-free legacy calls must not require transaction support")
			require.Empty(t, redis.Keys(), "the legacy direct fast path must not gain a new stream event")
			require.NoError(t, mock.ExpectationsWereMet(), "the fast path must issue no Begin, Commit, snapshot write or extra query")
		})
	}
}

func newSessionTurnPersistenceFixture(variant string) sessionPersistenceFixture {
	fixture := newSessionPersistenceFixture()
	switch variant {
	case "nil":
		fixture.result = nil
	case "head only":
		fixture.result.Diff, fixture.result.DiffBaseCommitSHA = nil, nil
	case "empty diff":
		fixture.result.Diff = stringPtr("")
	case "no base":
		fixture.result.DiffBaseCommitSHA, fixture.result.DiffHeadCommitSHA = nil, nil
	}
	return fixture
}

func TestSessionTurnPersistenceLegacyTransactionEffects(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, variant, failure string }{
		{name: "snapshot commit"},
		{name: "empty snapshot commit", variant: "empty diff"},
		{name: "head only commit", variant: "head only"},
		{name: "begin failure", failure: "begin"},
		{name: "row failure", failure: "row"},
		{name: "head failure", failure: "head"},
		{name: "snapshot failure", failure: "snapshot"},
		{name: "revision failure", failure: "revision"},
		{name: "commit failure", failure: "commit"},
		{name: "optional notification failure", failure: "notification"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionTurnPersistenceFixture(tt.variant)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated transaction-owning turn wrapper database")
			t.Cleanup(mock.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, mock)
			commands := captureSessionScalarNotifications(redis)
			failure := errors.New("synthetic legacy turn " + tt.failure)
			if tt.failure == "begin" {
				mock.ExpectBegin().WillReturnError(failure)
			} else {
				mock.ExpectBegin()
				_, writeErr := expectSessionTurnPersistence(t, mock, fixture, tt.failure)
				if writeErr != nil {
					failure = writeErr
					mock.ExpectRollback()
				} else if tt.failure == "commit" {
					mock.ExpectCommit().WillReturnError(failure)
					mock.ExpectRollback()
				} else {
					mock.ExpectCommit()
				}
			}
			if tt.failure == "notification" {
				redis.SetError("ERR synthetic optional workspace notification failure")
			}
			err = store.UpdateTurnComplete(context.Background(), fixture.orgID, fixture.sessionID, 2, fixture.result, "synthetic-agent", "synthetic-checkpoint")
			if tt.failure != "" && tt.failure != "notification" {
				require.ErrorIs(t, err, failure, "the legacy turn wrapper must preserve every original begin, persistence and commit error")
				require.Empty(t, commands(), "failed or uncertain commits must not even attempt workspace publication")
				require.Empty(t, redis.Keys(), "failed units must never notify a revision that did not commit")
			} else {
				require.NoError(t, err, "optional workspace notification failure cannot reject already committed turn persistence")
				if tt.failure == "notification" {
					require.Empty(t, redis.Keys(), "a failed optional notification must not fabricate an accepted event")
				} else {
					changed := shouldPersistDiffSnapshot(fixture.result)
					requireSessionPersistenceWorkspaceEvent(t, redis, fixture, changed)
					var expected []string
					if changed {
						expected = []string{"XADD:143:stream:{ses:" + fixture.sessionID.String() + "}:events"}
					}
					require.Equal(t, expected, commands(), "the legacy turn wrapper may emit only the existing post-commit workspace event, not a new status event or stream expiry")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the legacy wrapper must own its single transaction and unchanged row/head/snapshot pipeline")
		})
	}
}

func expectSessionTurnPersistence(t *testing.T, mock pgxmock.PgxPoolIface, fixture sessionPersistenceFixture, failure string) (SessionPersistenceEffects, error) {
	t.Helper()
	fail := errors.New("synthetic turn " + failure + " failure")
	result := fixture.result
	if result == nil {
		result = &models.SessionResult{}
	}
	diffStats := computeDiffStatsForResult(result)
	row := mock.ExpectExec(`(?s)UPDATE sessions.+WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": fixture.sessionID, "org_id": fixture.orgID, "current_turn": 2, "agent_session_id": "synthetic-agent", "snapshot_key": "synthetic-checkpoint", "token_usage": result.TokenUsage, "model_used": result.ModelUsed, "result_summary": result.ResultSummary, "diff": result.Diff, "error": result.Error, "base_commit_sha": result.DiffBaseCommitSHA, "diff_collected_at": result.DiffCollectedAt, "diff_stats": diffStats})
	if failure == "row" {
		row.WillReturnError(fail)
		return SessionPersistenceEffects{}, fail
	}
	row.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if shouldPersistChangesetHead(result) {
		head := mock.ExpectExec(`(?s)UPDATE session_changesets.+WHERE org_id = @org_id AND session_id = @session_id AND is_primary`).WithArgs(pgx.NamedArgs{"org_id": fixture.orgID, "session_id": fixture.sessionID, "head_sha": *result.DiffHeadCommitSHA})
		if failure == "head" {
			head.WillReturnError(fail)
			return SessionPersistenceEffects{}, fail
		}
		head.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	if !shouldPersistDiffSnapshot(result) {
		return SessionPersistenceEffects{}, nil
	}
	stats := parseDiffStatsPayload(diffStats)
	snapshot := mock.ExpectQuery(`(?s)INSERT INTO session_diff_snapshots.+WHERE id = @session_id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"session_id": fixture.sessionID, "org_id": fixture.orgID, "turn_number": 2, "source": models.SessionDiffSource("turn_complete"), "base_commit_sha": *result.DiffBaseCommitSHA, "head_commit_sha": result.DiffHeadCommitSHA, "workspace_dirty": true, "diff": *result.Diff, "files_changed": stats.FilesChanged, "lines_added": stats.Added, "lines_removed": stats.Removed, "captured_at": fixture.at, "review_bundle_key": result.ReviewBundleKey, "review_bundle_version": result.ReviewBundleVersion, "review_bundle_compressed_bytes": int64(0), "review_bundle_uncompressed_bytes": int64(0), "review_bundle_file_count": 0, "review_bundle_skipped_count": 0, "review_bundle_truncated": false})
	if failure == "snapshot" {
		snapshot.WillReturnError(fail)
		return SessionPersistenceEffects{}, fail
	}
	snapshot.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(fixture.snapshotID))
	revision := mock.ExpectQuery(`(?s)UPDATE sessions.+WHERE id = @session_id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"snapshot_id": fixture.snapshotID, "captured_at": fixture.at, "session_id": fixture.sessionID, "org_id": fixture.orgID})
	if failure == "revision" {
		revision.WillReturnError(fail)
		return SessionPersistenceEffects{}, fail
	}
	if failure == "revision scan" {
		revision.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow("invalid", fixture.at))
		return SessionPersistenceEffects{}, fail
	}
	revision.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(9), fixture.at))
	return SessionPersistenceEffects{orgID: fixture.orgID, sessionID: fixture.sessionID, workspaceRevision: workspaceRevisionUpdate{Revision: 9, UpdatedAt: fixture.at}, workspaceChanged: true}, nil
}
