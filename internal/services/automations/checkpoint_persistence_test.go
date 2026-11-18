package automations

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type checkpointOwnerStarter struct {
	pgxmock.PgxPoolIface
	tx           *checkpointOwnerTx
	beforeCommit func()
}

func (s *checkpointOwnerStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.PgxPoolIface.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &checkpointOwnerTx{Tx: tx, owner: s}
	return s.tx, nil
}

type checkpointOwnerTx struct {
	pgx.Tx
	owner     *checkpointOwnerStarter
	nested    int
	committed bool
}

func (tx *checkpointOwnerTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nested++
	return nil, errors.New("checkpoint must not create a nested transaction")
}

func (tx *checkpointOwnerTx) Commit(ctx context.Context) error {
	tx.owner.beforeCommit()
	err := tx.Tx.Commit(ctx)
	tx.committed = err == nil
	return err
}

func TestCheckpointPersistenceTurnOwner(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, failure string }{
		{name: "success publishes once after commit"},
		{name: "begin error", failure: "begin"},
		{name: "missing token", failure: "token"},
		{name: "attempt lock error", failure: "lock"},
		{name: "attempt no longer owned", failure: "unowned"},
		{name: "checkpoint key invalid", failure: "key"},
		{name: "checkpoint generation invalid", failure: "generation"},
		{name: "checkpoint SQL error", failure: "query"},
		{name: "checkpoint row rejected", failure: "rejected"},
		{name: "checkpoint scan error", failure: "scan"},
		{name: "uncertain outer commit", failure: "commit"},
		{name: "optional notification error preserves acceptance", failure: "notification"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the isolated actual turn-owner mock")
			t.Cleanup(mock.Close)
			starter := &checkpointOwnerStarter{PgxPoolIface: mock}
			sessions, redis := turnAttemptStreams(t, starter)
			starter.beforeCommit = func() {
				require.Empty(t, redis.Keys(), "even the final instant before owner commit must have no checkpoint event")
			}
			commands := turnAttemptCaptureCommands(redis, func() error {
				if starter.tx == nil || !starter.tx.committed {
					return errors.New("checkpoint event preceded confirmed owner commit")
				}
				return nil
			})
			store := NewTurnStore(starter, sessions, db.NewAutomationRunStore(starter), nil, nil, nil)
			orgID, sessionID, runID, lock, generation := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			at := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.FixedZone("synthetic", 3600))
			key, head, fingerprint := "synthetic-checkpoint", "synthetic-head", "synthetic-dependencies"
			if tt.failure == "token" {
				lock = uuid.Nil
			}
			if tt.failure == "key" {
				key = " \t "
			}
			if tt.failure == "generation" {
				generation = uuid.Nil
			}
			failure := errors.New("synthetic checkpoint owner " + tt.failure)
			begin := mock.ExpectBegin()
			if tt.failure == "begin" {
				begin.WillReturnError(failure)
			}
			if tt.failure != "begin" {
				if tt.failure != "token" {
					lockQuery := mock.ExpectQuery(`(?s)SELECT r.id.+JOIN jobs j ON j.id = r.job_id AND j.org_id = r.org_id.+r.dispatch_state = 'executing'.+r.attempt_lock_token = @lock_token.+j.status = 'running' AND j.lock_token = @lock_token.+FOR UPDATE OF r, j`).WithArgs(pgx.NamedArgs{"id": runID, "org_id": orgID, "lock_token": lock})
					switch tt.failure {
					case "lock":
						lockQuery.WillReturnError(failure)
					case "unowned":
						lockQuery.WillReturnRows(pgxmock.NewRows([]string{"id"}))
					default:
						lockQuery.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(runID))
					}
				}
				if tt.failure != "token" && tt.failure != "lock" && tt.failure != "unowned" && tt.failure != "key" && tt.failure != "generation" {
					args := pgx.NamedArgs{"id": sessionID, "org_id": orgID, "generation_id": generation, "agent_session_id": "synthetic-agent", "snapshot_key": key, "checkpoint_kind": string(models.CheckpointKindTurnComplete), "checkpoint_capability": string(models.CheckpointCapabilityFullResume), "checkpoint_size_bytes": int64(1234), "checkpointed_at": at.UTC(), "checkpoint_error": nil, "runtime_stop_reason": string(models.RuntimeStopReasonSoftBudget), "checkpoint_head_sha": &head, "dependency_fingerprint": &fingerprint, "review_complete": true, "lock_token": lock}
					write := mock.ExpectQuery(`(?s)^WITH published AS.+UPDATE sessions s.+s.org_id = @org_id.+s.deleted_at IS NULL.+s.automation_owner_generation_id = @generation_id.+FROM jobs j.+UPDATE automation_target_sessions g.+g.org_id = p.org_id AND g.session_id = p.id.+JOIN provenance g ON true`).WithArgs(args)
					switch tt.failure {
					case "query":
						write.WillReturnError(failure)
					case "rejected":
						write.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}))
					case "scan":
						write.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow("invalid", at))
					default:
						write.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(9), at))
					}
				}
				if tt.failure == "" || tt.failure == "notification" || tt.failure == "commit" {
					commit := mock.ExpectCommit()
					if tt.failure == "commit" {
						commit.WillReturnError(failure)
						mock.ExpectRollback()
					}
				} else {
					mock.ExpectRollback()
				}
			}
			if tt.failure == "notification" {
				redis.SetError("ERR synthetic checkpoint notification failure")
			}
			published, err := store.PublishCheckpointWithProvenance(context.Background(), orgID, sessionID, runID, lock, "synthetic-agent", key, models.CheckpointKindTurnComplete, models.CheckpointCapabilityFullResume, 1234, at, models.RuntimeStopReasonSoftBudget, models.CheckpointProvenance{GenerationID: generation, HeadSHA: head, DependencyFingerprint: &fingerprint, ReviewComplete: true})
			expectedError := ""
			switch tt.failure {
			case "begin", "commit":
				expectedError = failure.Error()
			case "token":
				expectedError = "lock automation attempt: lock token is required"
			case "lock":
				expectedError = "lock automation attempt: " + failure.Error()
			case "key":
				expectedError = "publish checkpoint with provenance: snapshot key is required"
			case "generation":
				expectedError = "publish checkpoint with provenance: generation is required"
			case "query":
				expectedError = "publish checkpoint with provenance: " + failure.Error()
			}
			if tt.failure == "scan" {
				require.ErrorContains(t, err, "collect published checkpoint with provenance:", "the owner preserves the exact collection error family")
			} else if expectedError != "" {
				require.EqualError(t, err, expectedError, "preserve the existing raw owner errors and precise persistence wrappers")
			} else {
				require.NoError(t, err, "false ownership and best-effort notification preserve their existing error-free result")
			}
			accepted := tt.failure == "" || tt.failure == "notification"
			require.Equal(t, accepted, published, "only confirmed physical owner commit accepts the checkpoint")
			if starter.tx != nil {
				require.Zero(t, starter.tx.nested, "the concrete checkpoint seam must never create an inner transaction or savepoint")
			}
			actualCommands, hookErrors := commands()
			require.Empty(t, hookErrors, "every accepted event must occur after the owner's successful Commit returned")
			if accepted && tt.failure != "notification" {
				require.Equal(t, []string{"XADD:143:stream:{ses:" + sessionID.String() + "}:events"}, actualCommands, "the owner emits exactly one checkpoint event and no status or expiry commands")
				requireAutomationCheckpointEvent(t, redis, orgID, sessionID, 9, at.UTC())
			} else {
				require.Empty(t, redis.Keys(), "all failed or uncertain owners, and failed optional notifications, leave no accepted event")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the real owner must retain run/job lock, supplied-tx SQL, one commit and original rollback behavior")
		})
	}
}

func requireAutomationCheckpointEvent(t *testing.T, redis *miniredis.Miniredis, orgID, sessionID uuid.UUID, revision int64, at time.Time) {
	t.Helper()
	key := "143:stream:{ses:" + sessionID.String() + "}:events"
	require.Equal(t, []string{key}, redis.Keys(), "a checkpoint wake must not invent status, readiness or other stream writes")
	entries, err := redis.Stream(key)
	require.NoError(t, err, "inspect the actual original-store workspace event")
	var actual []models.SessionStreamEvent
	for _, entry := range entries {
		var event models.SessionStreamEvent
		require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode every field of the checkpoint event")
		actual = append(actual, event)
	}
	payload, err := json.Marshal(models.SessionWorkspaceGenerationChangedEvent{SessionID: sessionID, OrgID: orgID, WorkspaceRevision: revision, WorkspaceRevisionUpdatedAt: at, Reason: "checkpoint"})
	require.NoError(t, err, "encode the unchanged exact checkpoint notification payload")
	var data any
	require.NoError(t, json.Unmarshal(payload, &data), "normalize only the interface-encoded event data")
	require.Equal(t, []models.SessionStreamEvent{{Type: models.SessionStreamEventWorkspaceGenerationChanged, SessionID: sessionID, OrgID: orgID, Data: data}}, actual, "preserve the exact original checkpoint event reason, timestamp, revision and tenant")
}

func TestCheckpointPersistenceActualOwnerWiring(t *testing.T) {
	t.Parallel()
	file, err := parser.ParseFile(token.NewFileSet(), "turn_store.go", nil, 0)
	require.NoError(t, err, "inspect the real checkpoint transaction owner rather than a test-only adapter")
	var calls []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "PublishCheckpointWithProvenance" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok {
				calls = append(calls, selector.Sel.Name)
			}
			if ok && selector.Sel.Name == "PublishCheckpointWithProvenanceInTx" {
				require.GreaterOrEqual(t, len(call.Args), 3, "the concrete seam must receive the owner's actual transaction")
				tx, ok := call.Args[2].(*ast.Ident)
				require.True(t, ok && tx.Name == "tx", "the owner must pass its run/job-locked transaction without an alternate pool or context adapter")
			}
			return true
		})
	}
	require.Equal(t, []string{"Begin", "Rollback", "LockAttempt", "PublishCheckpointWithProvenanceInTx", "Commit", "PublishCommittedSessionPersistenceEffects"}, calls, "pin the actual owner ordering and prohibit the early-publishing WithTx wrapper path")
}
