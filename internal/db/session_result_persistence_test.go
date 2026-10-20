package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type sessionPersistenceFixture struct {
	orgID, sessionID, phaseID, threadID, snapshotID uuid.UUID
	at                                              time.Time
	result                                          *models.SessionResult
}

func newSessionPersistenceFixture() sessionPersistenceFixture {
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	diff, base, head := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1 @@\n+hello\n", "synthetic-base", "synthetic-head"
	return sessionPersistenceFixture{
		orgID: uuid.New(), sessionID: uuid.New(), phaseID: uuid.New(), threadID: uuid.New(), snapshotID: uuid.New(), at: at,
		result: &models.SessionResult{Diff: &diff, DiffBaseCommitSHA: &base, DiffHeadCommitSHA: &head, DiffCollectedAt: &at, DiffWorkspaceDirty: true, ResultSummary: stringPtr("synthetic result"), TokenUsage: json.RawMessage(`{"input":2}`)},
	}
}

type sessionPersistenceOwnedTx struct {
	pgx.Tx
	settlements []string
}

func (tx *sessionPersistenceOwnedTx) Begin(context.Context) (pgx.Tx, error) {
	tx.settlements = append(tx.settlements, "begin")
	return nil, errors.New("nested transaction is forbidden")
}

func (tx *sessionPersistenceOwnedTx) Commit(context.Context) error {
	tx.settlements = append(tx.settlements, "commit")
	return errors.New("inner commit is forbidden")
}

func (tx *sessionPersistenceOwnedTx) Rollback(context.Context) error {
	tx.settlements = append(tx.settlements, "rollback")
	return errors.New("inner rollback is forbidden")
}

func TestSessionPersistenceInTx(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, failure string }{}
	for _, kind := range []string{"result", "status", "turn"} {
		for _, failure := range []string{"", "row", "phase", "phase conflict", "phase scan"} {
			tests = append(tests, struct{ kind, failure string }{kind, failure})
		}
		if kind != "status" {
			for _, failure := range []string{"nil result", "head only", "head", "snapshot", "revision", "revision scan"} {
				tests = append(tests, struct{ kind, failure string }{kind, failure})
			}
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.failure, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionPersistenceFixture()
			if tt.failure == "nil result" {
				fixture.result = nil
			} else if tt.failure == "head only" {
				fixture.result.Diff, fixture.result.DiffBaseCommitSHA = nil, nil
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this caller's transaction mock")
			t.Cleanup(mock.Close)
			mock.ExpectBegin()
			underlying, err := mock.Begin(context.Background())
			require.NoError(t, err, "the test caller alone begins the transaction")
			tx := &sessionPersistenceOwnedTx{Tx: underlying}
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			expectedPhase, expectedSession, expectedEffects, expectedErr := expectSessionPersistenceWrites(t, mock, fixture, tt.kind, tt.failure)
			phase, session, effects, err := callSessionPersistenceInTx(store, tx, fixture, tt.kind)
			if expectedErr != nil {
				require.Error(t, err, "a persistence-stage failure must propagate to the outer transaction owner")
				if tt.failure != "phase scan" && tt.failure != "revision scan" {
					require.ErrorIs(t, err, expectedErr, "persistence failures must retain their original classification")
				}
				require.Equal(t, models.SessionActivityPhase{}, phase, "failed units must not return an accepted phase")
				require.Equal(t, models.Session{}, session, "failed units must not return an accepted session")
				require.Equal(t, SessionPersistenceEffects{}, effects, "failed units must discard partially collected workspace effects")
			} else {
				require.NoError(t, err, "the persistence unit must use the existing caller transaction")
				require.Equal(t, expectedPhase, phase, "return the complete changed phase rather than an identity-only surrogate")
				require.Equal(t, expectedSession, session, "return the complete changed session when this operation returns one")
				require.Equal(t, expectedEffects, effects, "return only sealed workspace notification data for a persisted diff snapshot")
				mock.ExpectQuery("^SELECT 42$").WillReturnRows(pgxmock.NewRows([]string{"value"}).AddRow(42))
				var value int
				require.NoError(t, tx.QueryRow(context.Background(), "SELECT 42").Scan(&value), "the caller transaction must remain usable for further composition")
				require.Equal(t, 42, value, "later caller-owned work must use the same still-open transaction")
			}
			require.Empty(t, redis.Keys(), "enabled publishers must emit no event from any InTx path, including after a partial failure")
			require.Empty(t, tx.settlements, "transitive persistence must not begin a savepoint, commit or roll back")
			if expectedErr != nil {
				mock.ExpectRollback()
				require.NoError(t, underlying.Rollback(context.Background()), "only the outer owner aborts the whole failed unit")
			} else {
				mock.ExpectCommit()
				require.NoError(t, underlying.Commit(context.Background()), "only the outer owner makes the composed writes durable")
				require.Empty(t, redis.Keys(), "commit alone must not publish captured workspace effects")
				store.PublishCommittedSessionPersistenceEffects(context.Background(), uuid.New(), effects)
				require.Empty(t, redis.Keys(), "a foreign organization must not publish captured effects")
				store.PublishCommittedSessionPersistenceEffects(context.Background(), fixture.orgID, effects)
				requireSessionPersistenceWorkspaceEvent(t, redis, fixture, expectedEffects.workspaceChanged)
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all SQL must use only the supplied transaction with the original tenant filters and no retries")
		})
	}
}

func TestSessionPersistenceInTxRejectsNilTransaction(t *testing.T) {
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
			for _, kind := range []string{"result", "status", "turn"} {
				t.Run(kind, func(t *testing.T) {
					t.Parallel()
					store, redis := sessionPersistenceStoreWithStreams(t, nil)
					phase, session, effects, err := callSessionPersistenceInTx(store, tt.tx, newSessionPersistenceFixture(), kind)
					require.ErrorContains(t, err, "transaction is required", "an invalid transaction must fail without falling back to any store database")
					require.Equal(t, models.SessionActivityPhase{}, phase, "invalid transactions cannot produce changed phases")
					require.Equal(t, models.Session{}, session, "invalid transactions cannot produce changed sessions")
					require.Equal(t, SessionPersistenceEffects{}, effects, "invalid transactions cannot produce deferred effects")
					require.Empty(t, redis.Keys(), "invalid transaction paths must not notify live readers")
				})
			}
		})
	}
}

func TestSessionPersistenceLegacyWrappers(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, failure string }{}
	for _, kind := range []string{"result", "status", "turn"} {
		for _, failure := range []string{"", "begin", "phase", "commit"} {
			tests = append(tests, struct{ kind, failure string }{kind, failure})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.failure, func(t *testing.T) {
			t.Parallel()
			fixture := newSessionPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the legacy wrapper's pool mock")
			t.Cleanup(mock.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, mock)
			failure := errors.New("synthetic " + tt.failure + " failure")
			begin := mock.ExpectBegin()
			var expectedPhase models.SessionActivityPhase
			var expectedSession models.Session
			if tt.failure == "begin" {
				begin.WillReturnError(failure)
			} else {
				var persistenceErr error
				expectedPhase, expectedSession, _, persistenceErr = expectSessionPersistenceWrites(t, mock, fixture, tt.kind, tt.failure)
				if tt.failure == "phase" {
					failure = persistenceErr
					mock.ExpectRollback()
				} else {
					commit := mock.ExpectCommit()
					if tt.failure == "commit" {
						commit.WillReturnError(failure)
						mock.ExpectRollback()
					}
				}
			}
			phase, session, err := callSessionPersistenceWrapper(store, fixture, tt.kind)
			if tt.failure != "" {
				require.ErrorIs(t, err, failure, "the legacy wrapper must preserve the original begin, persistence or commit error")
				require.Equal(t, models.SessionActivityPhase{}, phase, "an uncommitted wrapper must not return an accepted phase")
				require.Equal(t, models.Session{}, session, "an uncommitted wrapper must not return an accepted session")
				require.Empty(t, redis.Keys(), "legacy wrappers must not notify before a confirmed successful commit")
			} else {
				require.NoError(t, err, "the wrapper must compose the same persistence implementation and commit once")
				require.Equal(t, expectedPhase, phase, "legacy wrappers must retain their complete returned phase")
				require.Equal(t, expectedSession, session, "legacy wrappers must retain their complete returned session")
				requireSessionPersistenceWorkspaceEvent(t, redis, fixture, tt.kind != "status")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "legacy wrappers must retain transaction boundaries and the exact original SQL ordering")
		})
	}
}

func sessionPersistenceStoreWithStreams(t *testing.T, database DBTX) (*SessionStore, *miniredis.Miniredis) {
	t.Helper()
	redis := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redis.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "enable a real Redis publisher so absent dependency wiring cannot hide early publication")
	t.Cleanup(func() {
		require.NoError(t, client.Close(), "close this test's Redis publisher")
	})
	store := NewSessionStore(database)
	store.SetStreams(cache.NewSessionStreams(client, zerolog.Nop(), nil))
	return store, redis
}

func callSessionPersistenceInTx(store *SessionStore, tx pgx.Tx, fixture sessionPersistenceFixture, kind string) (models.SessionActivityPhase, models.Session, SessionPersistenceEffects, error) {
	ctx := context.Background()
	switch kind {
	case "result":
		return store.UpdateResultAndCompleteActivityPhaseInTx(ctx, fixture.orgID, tx, fixture.sessionID, fixture.phaseID, models.SessionStatusCompleted, fixture.result, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, fixture.at)
	case "status":
		phase, session, err := store.UpdateStatusAndCompleteActivityPhaseInTx(ctx, fixture.orgID, tx, fixture.sessionID, fixture.phaseID, models.SessionStatusCompleted, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, fixture.at)
		return phase, session, SessionPersistenceEffects{}, err
	default:
		phase, effects, err := store.UpdateTurnCompleteAndCompleteActivityPhaseInTx(ctx, fixture.orgID, tx, fixture.sessionID, fixture.phaseID, 2, fixture.result, "synthetic-agent", "synthetic-checkpoint", models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, fixture.at)
		return phase, models.Session{}, effects, err
	}
}

func callSessionPersistenceWrapper(store *SessionStore, fixture sessionPersistenceFixture, kind string) (models.SessionActivityPhase, models.Session, error) {
	ctx := context.Background()
	switch kind {
	case "result":
		return store.UpdateResultAndCompleteActivityPhase(ctx, fixture.orgID, fixture.sessionID, fixture.phaseID, models.SessionStatusCompleted, fixture.result, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, fixture.at)
	case "status":
		return store.UpdateStatusAndCompleteActivityPhase(ctx, fixture.orgID, fixture.sessionID, fixture.phaseID, models.SessionStatusCompleted, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, fixture.at)
	default:
		phase, err := store.UpdateTurnCompleteAndCompleteActivityPhase(ctx, fixture.orgID, fixture.sessionID, fixture.phaseID, 2, fixture.result, "synthetic-agent", "synthetic-checkpoint", models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, fixture.at)
		return phase, models.Session{}, err
	}
}

func expectSessionPersistenceWrites(t *testing.T, mock pgxmock.PgxPoolIface, fixture sessionPersistenceFixture, kind, failure string) (models.SessionActivityPhase, models.Session, SessionPersistenceEffects, error) {
	t.Helper()
	fail := errors.New("synthetic persistence " + failure)
	result := fixture.result
	if result == nil {
		result = &models.SessionResult{}
	}
	diffStats := computeDiffStatsForResult(result)
	if kind == "turn" {
		write := mock.ExpectExec(`(?s)UPDATE sessions.+WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": fixture.sessionID, "org_id": fixture.orgID, "current_turn": 2, "agent_session_id": "synthetic-agent", "snapshot_key": "synthetic-checkpoint", "token_usage": result.TokenUsage, "model_used": result.ModelUsed, "result_summary": result.ResultSummary, "diff": result.Diff, "error": result.Error, "base_commit_sha": result.DiffBaseCommitSHA, "diff_collected_at": result.DiffCollectedAt, "diff_stats": diffStats})
		if failure == "row" {
			write.WillReturnError(fail)
			return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
		}
		write.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	} else {
		arguments := pgx.NamedArgs{"id": fixture.sessionID, "org_id": fixture.orgID, "status": "completed"}
		if kind == "result" {
			arguments["token_usage"], arguments["model_used"], arguments["result_summary"], arguments["diff"], arguments["error"] = result.TokenUsage, result.ModelUsed, result.ResultSummary, result.Diff, result.Error
			arguments["failure_explanation"], arguments["failure_category"], arguments["failure_next_steps"], arguments["failure_retry_advised"] = result.FailureExplanation, result.FailureCategory, result.FailureNextSteps, result.FailureRetryAdvised
			arguments["base_commit_sha"], arguments["diff_collected_at"], arguments["diff_stats"] = result.DiffBaseCommitSHA, result.DiffCollectedAt, diffStats
		}
		write := mock.ExpectQuery(`(?s)UPDATE sessions.+WHERE id = @id AND org_id = @org_id`).WithArgs(arguments)
		if failure == "row" {
			write.WillReturnError(fail)
			return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
		}
		write.WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(fixture.sessionID, uuid.Nil, fixture.orgID, fixture.at)...))
	}
	if kind != "status" && shouldPersistChangesetHead(result) {
		write := mock.ExpectExec(`(?s)UPDATE session_changesets.+WHERE org_id = @org_id AND session_id = @session_id AND is_primary`).WithArgs(pgx.NamedArgs{"org_id": fixture.orgID, "session_id": fixture.sessionID, "head_sha": *result.DiffHeadCommitSHA})
		if failure == "head" {
			write.WillReturnError(fail)
			return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
		}
		write.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	}
	effects := SessionPersistenceEffects{}
	if kind != "status" && shouldPersistDiffSnapshot(result) {
		turn := 0
		if kind == "turn" {
			turn = 2
		}
		stats := parseDiffStatsPayload(diffStats)
		write := mock.ExpectQuery(`(?s)INSERT INTO session_diff_snapshots.+WHERE id = @session_id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"session_id": fixture.sessionID, "org_id": fixture.orgID, "turn_number": turn, "source": models.SessionDiffSource("turn_complete"), "base_commit_sha": *result.DiffBaseCommitSHA, "head_commit_sha": result.DiffHeadCommitSHA, "workspace_dirty": true, "diff": *result.Diff, "files_changed": stats.FilesChanged, "lines_added": stats.Added, "lines_removed": stats.Removed, "captured_at": fixture.at, "review_bundle_key": result.ReviewBundleKey, "review_bundle_version": result.ReviewBundleVersion, "review_bundle_compressed_bytes": int64(0), "review_bundle_uncompressed_bytes": int64(0), "review_bundle_file_count": 0, "review_bundle_skipped_count": 0, "review_bundle_truncated": false})
		if failure == "snapshot" {
			write.WillReturnError(fail)
			return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
		}
		write.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(fixture.snapshotID))
		revision := mock.ExpectQuery(`(?s)UPDATE sessions.+WHERE id = @session_id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"snapshot_id": fixture.snapshotID, "captured_at": fixture.at, "session_id": fixture.sessionID, "org_id": fixture.orgID})
		if failure == "revision" {
			revision.WillReturnError(fail)
			return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
		}
		if failure == "revision scan" {
			revision.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow("invalid", fixture.at))
			return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
		}
		revision.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(9), fixture.at))
		effects = SessionPersistenceEffects{orgID: fixture.orgID, sessionID: fixture.sessionID, workspaceRevision: workspaceRevisionUpdate{Revision: 9, UpdatedAt: fixture.at}, workspaceChanged: true}
	}
	phaseWrite := mock.ExpectQuery(`(?s)UPDATE session_activity_phases.+WHERE org_id = \$1 AND id = \$2`).WithArgs(fixture.orgID, fixture.phaseID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, fixture.at)
	if failure == "phase" {
		phaseWrite.WillReturnError(fail)
		return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
	}
	if failure == "phase conflict" {
		phaseWrite.WillReturnRows(pgxmock.NewRows(activityPhaseTestColumns))
		return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, ErrActivityPhaseConflict
	}
	if failure == "phase scan" {
		phaseWrite.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("invalid"))
		return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fail
	}
	startedAt := fixture.at.Add(-time.Minute)
	phaseWrite.WillReturnRows(pgxmock.NewRows(activityPhaseTestColumns).AddRow(fixture.phaseID, fixture.orgID, fixture.sessionID, fixture.threadID, 1, 1, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, startedAt, &fixture.at, nil, models.ActivityPhaseTriggerInitial, nil, nil, nil, startedAt, fixture.at))
	phase := models.SessionActivityPhase{ID: fixture.phaseID, OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: fixture.threadID, TurnNumber: 1, PhaseNumber: 1, Status: models.ActivityPhaseStatusCompleted, BoundaryReason: models.ActivityPhaseBoundaryFinalResponse, StartedAt: startedAt, CompletedAt: &fixture.at, TriggerKind: models.ActivityPhaseTriggerInitial, CreatedAt: startedAt, UpdatedAt: fixture.at}
	session := models.Session{}
	if kind != "turn" {
		started, completed := fixture.at.Add(-time.Hour), fixture.at.Add(-5*time.Minute)
		session = models.Session{ID: fixture.sessionID, OrgID: fixture.orgID, Origin: models.SessionOriginIssueTrigger, InteractionMode: models.SessionInteractionModeSingleRun, ValidationPolicy: models.SessionValidationPolicyOnTurnComplete, AgentType: "claude-code", Status: models.SessionStatusCompleted, AutonomyLevel: "supervised", TokenMode: "low", StartedAt: &started, CompletedAt: &completed, LastActivityAt: fixture.at, SandboxState: "none", WorkspaceRevisionUpdatedAt: fixture.at, PRCreationState: "idle", PRPushState: "idle", BranchCreationState: "idle", LinearPrepareState: "none", CreatedAt: fixture.at.Add(-2 * time.Hour)}
	}
	return phase, session, effects, nil
}

func requireSessionPersistenceWorkspaceEvent(t *testing.T, redis *miniredis.Miniredis, fixture sessionPersistenceFixture, expected bool) {
	t.Helper()
	require.False(t, redis.Exists("143:stream:{ses:"+fixture.sessionID.String()+"}:status"), "session-status publication remains the outer service caller's responsibility")
	key := "143:stream:{ses:" + fixture.sessionID.String() + "}:events"
	if !expected {
		require.False(t, redis.Exists(key), "status-only or diff-free persistence has no workspace event")
		return
	}
	entries, err := redis.Stream(key)
	require.NoError(t, err, "explicit post-commit publication must append to the existing workspace event stream")
	var actual []models.SessionStreamEvent
	for _, entry := range entries {
		var event models.SessionStreamEvent
		require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode the complete published workspace event")
		actual = append(actual, event)
	}
	data, err := json.Marshal(models.SessionWorkspaceGenerationChangedEvent{SessionID: fixture.sessionID, OrgID: fixture.orgID, WorkspaceRevision: 9, WorkspaceRevisionUpdatedAt: fixture.at, Reason: "diff_snapshot"})
	require.NoError(t, err, "encode this fixture's exact expected workspace data")
	var expectedData any
	require.NoError(t, json.Unmarshal(data, &expectedData), "normalize only interface-encoded event data for exact event comparison")
	require.Equal(t, []models.SessionStreamEvent{{Type: models.SessionStreamEventWorkspaceGenerationChanged, SessionID: fixture.sessionID, OrgID: fixture.orgID, Data: expectedData}}, actual, "one explicit publication must retain every field of the original workspace notification")
}
