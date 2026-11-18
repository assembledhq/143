package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionScalarPersistenceInTx(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, variant, failure string }{
		{kind: "result"}, {kind: "result", variant: "nil"}, {kind: "result", variant: "head only"}, {kind: "result", variant: "empty diff"},
		{kind: "result", failure: "row"}, {kind: "result", failure: "no row"}, {kind: "result", failure: "row scan"},
		{kind: "result", failure: "head"}, {kind: "result", failure: "snapshot"}, {kind: "result", failure: "revision"}, {kind: "result", failure: "revision scan"},
		{kind: "status"}, {kind: "status", failure: "row"}, {kind: "status", failure: "no row"}, {kind: "status", failure: "row scan"},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.variant+"/"+tt.failure, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionTurnPersistenceFixture(tt.variant)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated caller transaction mock")
			t.Cleanup(mock.Close)
			mock.ExpectBegin()
			owner, err := mock.Begin(context.Background())
			require.NoError(t, err, "only the caller starts the concrete transaction")
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			redis.SetTime(fixture.at)
			expectedSession, expectedEffects, expectedErr := expectSessionScalarPersistence(t, mock, fixture, tt.kind, tt.failure)
			session, effects, persistenceErr := callSessionScalarPersistence(store, tx, fixture, tt.kind)
			require.Empty(t, redis.Keys(), "successful and partially failed InTx writes must leave enabled status and workspace publishers silent")
			if expectedErr != nil {
				require.Error(t, persistenceErr, "every query, collection, head and snapshot error must reach the outer owner")
				if !strings.Contains(tt.failure, "scan") {
					require.ErrorIs(t, persistenceErr, expectedErr, "ordinary persistence failures must preserve their raw cause")
				}
				require.Equal(t, models.Session{}, session, "failed persistence must not expose a partially accepted returned session")
				require.Equal(t, SessionPersistenceEffects{}, effects, "failed persistence must discard partially collected effect data")
				mock.ExpectRollback()
				require.NoError(t, owner.Rollback(context.Background()), "only the outer owner aborts the failed unit")
			} else {
				require.NoError(t, persistenceErr, "scalar persistence must compose in the supplied transaction without a store database")
				require.Equal(t, expectedSession, session, "return every field of the unchanged UPDATE RETURNING projection")
				require.Equal(t, expectedEffects, effects, "only authoritative snapshot persistence may capture workspace notification data")
				mock.ExpectQuery("^SELECT 42$").WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow(42))
				var value int
				require.NoError(t, tx.QueryRow(context.Background(), "SELECT 42").Scan(&value), "the owner must retain its usable transaction for further composed work")
				require.Equal(t, 42, value, "later work must execute in the same caller-owned transaction")
				mock.ExpectCommit()
				require.NoError(t, owner.Commit(context.Background()), "only the outer owner commits the complete unit")
				require.Empty(t, redis.Keys(), "even a successful caller commit must not implicitly emit either kind of notification")
				store.PublishCommittedSessionPersistenceEffects(context.Background(), fixture.orgID, effects)
				requireSessionPersistenceWorkspaceEvent(t, redis, fixture, expectedEffects.workspaceChanged)
			}
			require.Empty(t, tx.settlements, "no scalar persistence path may begin, settle, savepoint or retry the caller's unit")
			require.NoError(t, mock.ExpectationsWereMet(), "all original tenant-scoped row, head and snapshot arguments must use only the supplied transaction")
		})
	}
}

func TestSessionScalarPersistenceRejectsNilTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *sessionPersistenceOwnedTx
	tests := []struct {
		name, kind string
		tx         pgx.Tx
	}{
		{name: "nil result transaction", kind: "result"}, {name: "typed nil result transaction", kind: "result", tx: typedNil},
		{name: "nil status transaction", kind: "status"}, {name: "typed nil status transaction", kind: "status", tx: typedNil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			session, effects, err := callSessionScalarPersistence(store, tt.tx, newSessionPersistenceFixture(), tt.kind)
			require.ErrorContains(t, err, "transaction is required", "nil concrete transactions must be rejected without pool fallback or panic")
			require.Equal(t, models.Session{}, session, "missing transactions cannot return an accepted session")
			require.Equal(t, SessionPersistenceEffects{}, effects, "missing transactions cannot capture notification effects")
			require.Empty(t, redis.Keys(), "invalid transaction inputs must never notify readers")
		})
	}
}

func TestSessionScalarPersistenceLegacyWrappers(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, kind, variant, failure string }{
		{name: "status direct write", kind: "status"}, {name: "status raw query error", kind: "status", failure: "row"},
		{name: "nil result direct write", kind: "result", variant: "nil"}, {name: "diff without base direct write", kind: "result", variant: "no base"},
		{name: "head-only transaction", kind: "result", variant: "head only"}, {name: "snapshot transaction", kind: "result"},
		{name: "empty authoritative snapshot", kind: "result", variant: "empty diff"},
		{name: "begin failure", kind: "result", failure: "begin"}, {name: "row failure", kind: "result", failure: "row"},
		{name: "snapshot failure", kind: "result", failure: "snapshot"}, {name: "commit failure", kind: "result", failure: "commit"},
		{name: "notification failure remains optional", kind: "result", failure: "notification"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionTurnPersistenceFixture(tt.variant)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the original wrapper's database mock")
			t.Cleanup(mock.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, mock)
			redis.SetTime(fixture.at)
			commands := captureSessionScalarNotifications(redis)
			transactional := tt.kind == "result" && (shouldPersistDiffSnapshot(fixture.result) || shouldPersistChangesetHead(fixture.result))
			failure := errors.New("synthetic wrapper " + tt.failure)
			var expected models.Session
			if tt.failure == "begin" {
				mock.ExpectBegin().WillReturnError(failure)
			} else {
				if transactional {
					mock.ExpectBegin()
				}
				var writeErr error
				expected, _, writeErr = expectSessionScalarPersistence(t, mock, fixture, tt.kind, tt.failure)
				if writeErr != nil {
					failure = writeErr
					if transactional {
						mock.ExpectRollback()
					}
				} else if transactional {
					commit := mock.ExpectCommit()
					if tt.failure == "commit" {
						commit.WillReturnError(failure)
						mock.ExpectRollback()
					}
				}
			}
			if tt.failure == "notification" {
				redis.SetError("ERR synthetic optional notification failure")
			}
			if tt.kind == "status" {
				err = store.UpdateStatus(context.Background(), fixture.orgID, fixture.sessionID, models.SessionStatusCompleted)
			} else {
				err = store.UpdateResult(context.Background(), fixture.orgID, fixture.sessionID, models.SessionStatusCompleted, fixture.result)
			}
			if tt.failure != "" && tt.failure != "notification" {
				require.ErrorIs(t, err, failure, "legacy wrappers must preserve raw begin, persistence and commit errors")
				require.Empty(t, redis.Keys(), "no failed or uncertain transaction may publish a session status or workspace effect")
			} else {
				require.NoError(t, err, "legacy persistence success must not be rejected because optional notification failed")
				if tt.failure == "notification" {
					require.Empty(t, redis.Keys(), "failed optional notifications must not fabricate accepted Redis records")
				} else {
					requireSessionScalarStatusEvent(t, redis, fixture, expected)
					prefix := "143:stream:{ses:" + fixture.sessionID.String() + "}:"
					expectedCommands := []string{"XADD:" + prefix + "status", "EXPIREAT:" + prefix + "logs", "EXPIREAT:" + prefix + "status", "EXPIREAT:" + prefix + "events"}
					if tt.kind == "result" && shouldPersistDiffSnapshot(fixture.result) {
						expectedCommands = append(expectedCommands, "XADD:"+prefix+"events")
					}
					require.Equal(t, expectedCommands, commands(), "legacy publication must keep status, terminal stream expiry and then workspace notification in the original exact order")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "legacy direct paths must gain no transaction and transactional paths must retain their original settlement")
		})
	}
}

func callSessionScalarPersistence(store *SessionStore, tx pgx.Tx, fixture sessionPersistenceFixture, kind string) (models.Session, SessionPersistenceEffects, error) {
	if kind == "status" {
		session, err := store.UpdateStatusInTx(context.Background(), fixture.orgID, tx, fixture.sessionID, models.SessionStatusCompleted)
		return session, SessionPersistenceEffects{}, err
	}
	return store.UpdateResultInTx(context.Background(), fixture.orgID, tx, fixture.sessionID, models.SessionStatusCompleted, fixture.result)
}

func expectSessionScalarPersistence(t *testing.T, mock pgxmock.PgxPoolIface, fixture sessionPersistenceFixture, kind, failure string) (models.Session, SessionPersistenceEffects, error) {
	t.Helper()
	fail := errors.New("synthetic scalar " + failure)
	result := fixture.result
	if result == nil {
		result = &models.SessionResult{}
	}
	diffStats := computeDiffStatsForResult(result)
	arguments := pgx.NamedArgs{"id": fixture.sessionID, "org_id": fixture.orgID, "status": "completed"}
	if kind == "result" {
		arguments["token_usage"], arguments["model_used"], arguments["result_summary"], arguments["diff"], arguments["error"] = result.TokenUsage, result.ModelUsed, result.ResultSummary, result.Diff, result.Error
		arguments["failure_explanation"], arguments["failure_category"], arguments["failure_next_steps"], arguments["failure_retry_advised"] = result.FailureExplanation, result.FailureCategory, result.FailureNextSteps, result.FailureRetryAdvised
		arguments["base_commit_sha"], arguments["diff_collected_at"], arguments["diff_stats"] = result.DiffBaseCommitSHA, result.DiffCollectedAt, diffStats
	}
	write := mock.ExpectQuery(`(?s)UPDATE sessions.+WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`).WithArgs(arguments)
	switch failure {
	case "row":
		write.WillReturnError(fail)
		return models.Session{}, SessionPersistenceEffects{}, fail
	case "no row":
		write.WillReturnRows(pgxmock.NewRows(sessionTestColumns))
		return models.Session{}, SessionPersistenceEffects{}, pgx.ErrNoRows
	case "row scan":
		write.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("invalid"))
		return models.Session{}, SessionPersistenceEffects{}, fail
	}
	write.WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(fixture.sessionID, uuid.Nil, fixture.orgID, fixture.at)...))
	if kind == "result" && shouldPersistChangesetHead(result) {
		head := mock.ExpectExec(`(?s)UPDATE session_changesets.+WHERE org_id = @org_id AND session_id = @session_id AND is_primary`).WithArgs(pgx.NamedArgs{"org_id": fixture.orgID, "session_id": fixture.sessionID, "head_sha": *result.DiffHeadCommitSHA})
		if failure == "head" {
			head.WillReturnError(fail)
			return models.Session{}, SessionPersistenceEffects{}, fail
		}
		head.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	effects := SessionPersistenceEffects{}
	if kind == "result" && shouldPersistDiffSnapshot(result) {
		stats := parseDiffStatsPayload(diffStats)
		snapshot := mock.ExpectQuery(`(?s)INSERT INTO session_diff_snapshots.+WHERE id = @session_id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"session_id": fixture.sessionID, "org_id": fixture.orgID, "turn_number": 0, "source": models.SessionDiffSource("turn_complete"), "base_commit_sha": *result.DiffBaseCommitSHA, "head_commit_sha": result.DiffHeadCommitSHA, "workspace_dirty": true, "diff": *result.Diff, "files_changed": stats.FilesChanged, "lines_added": stats.Added, "lines_removed": stats.Removed, "captured_at": fixture.at, "review_bundle_key": result.ReviewBundleKey, "review_bundle_version": result.ReviewBundleVersion, "review_bundle_compressed_bytes": int64(0), "review_bundle_uncompressed_bytes": int64(0), "review_bundle_file_count": 0, "review_bundle_skipped_count": 0, "review_bundle_truncated": false})
		if failure == "snapshot" {
			snapshot.WillReturnError(fail)
			return models.Session{}, SessionPersistenceEffects{}, fail
		}
		snapshot.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(fixture.snapshotID))
		revision := mock.ExpectQuery(`(?s)UPDATE sessions.+WHERE id = @session_id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"snapshot_id": fixture.snapshotID, "captured_at": fixture.at, "session_id": fixture.sessionID, "org_id": fixture.orgID})
		if failure == "revision" {
			revision.WillReturnError(fail)
			return models.Session{}, SessionPersistenceEffects{}, fail
		}
		if failure == "revision scan" {
			revision.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow("invalid", fixture.at))
			return models.Session{}, SessionPersistenceEffects{}, fail
		}
		revision.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(9), fixture.at))
		effects = SessionPersistenceEffects{orgID: fixture.orgID, sessionID: fixture.sessionID, workspaceRevision: workspaceRevisionUpdate{Revision: 9, UpdatedAt: fixture.at}, workspaceChanged: true}
	}
	started, completed := fixture.at.Add(-time.Hour), fixture.at.Add(-5*time.Minute)
	session := models.Session{ID: fixture.sessionID, OrgID: fixture.orgID, Origin: models.SessionOriginIssueTrigger, InteractionMode: models.SessionInteractionModeSingleRun, ValidationPolicy: models.SessionValidationPolicyOnTurnComplete, AgentType: "claude-code", Status: models.SessionStatusCompleted, AutonomyLevel: "supervised", TokenMode: "low", StartedAt: &started, CompletedAt: &completed, LastActivityAt: fixture.at, SandboxState: "none", WorkspaceRevisionUpdatedAt: fixture.at, PRCreationState: "idle", PRPushState: "idle", BranchCreationState: "idle", LinearPrepareState: "none", CreatedAt: fixture.at.Add(-2 * time.Hour)}
	return session, effects, nil
}

func requireSessionScalarStatusEvent(t *testing.T, redis *miniredis.Miniredis, fixture sessionPersistenceFixture, expected models.Session) {
	t.Helper()
	key := "143:stream:{ses:" + fixture.sessionID.String() + "}:status"
	entries, err := redis.Stream(key)
	require.NoError(t, err, "legacy status publication must append to the established session stream")
	var actual []string
	for _, entry := range entries {
		actual = append(actual, entry.Values[1])
	}
	encoded, err := json.Marshal(expected)
	require.NoError(t, err, "encode the entire expected original returned row using the existing public wire contract")
	require.Equal(t, []string{string(encoded)}, actual, "publish the exact serialized original returned row without reloading a newer snapshot projection")
	require.Greater(t, redis.TTL(key), time.Duration(0), "terminal status publication must retain its existing stream expiry")
}

func captureSessionScalarNotifications(redis *miniredis.Miniredis) func() []string {
	var mu sync.Mutex
	var commands []string
	redis.Server().SetPreHook(func(_ *server.Peer, command string, args ...string) bool {
		if (strings.EqualFold(command, "XADD") || strings.EqualFold(command, "EXPIREAT")) && len(args) > 0 {
			mu.Lock()
			commands = append(commands, strings.ToUpper(command)+":"+args[0])
			mu.Unlock()
		}
		return false
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), commands...)
	}
}
