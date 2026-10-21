package automations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type turnAttemptUnitStarter struct {
	pgxmock.PgxPoolIface
	tx           *turnAttemptUnitTx
	beforeCommit func()
}

func (s *turnAttemptUnitStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.PgxPoolIface.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &turnAttemptUnitTx{Tx: tx, owner: s}
	return s.tx, nil
}

type turnAttemptUnitTx struct {
	pgx.Tx
	owner  *turnAttemptUnitStarter
	nested int
}

func (tx *turnAttemptUnitTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nested++
	return nil, errors.New("nested attempt transaction forbidden")
}

func (tx *turnAttemptUnitTx) Commit(ctx context.Context) error {
	if tx.owner.beforeCommit != nil {
		tx.owner.beforeCommit()
	}
	return tx.Tx.Commit(ctx)
}

func TestTurnAttemptPersistenceOwner(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, kind, failure string }{
		{name: "awaiting-input status", kind: "status"}, {name: "failed result with nil normalization", kind: "result"},
		{name: "diff-free completed turn has no new event", kind: "turn"}, {name: "multiple writes preserve effect order", kind: "ordered"},
		{name: "begin failure", kind: "status", failure: "begin"}, {name: "status query failure", kind: "status", failure: "row"},
		{name: "result query failure", kind: "result", failure: "row"}, {name: "turn query failure", kind: "turn", failure: "row"},
		{name: "later callback failure discards captured status", kind: "status", failure: "callback"},
		{name: "uncertain commit discards captured status", kind: "status", failure: "commit"},
		{name: "optional notification failure does not reject committed work", kind: "result", failure: "notification"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create one isolated physical-owner mock")
			t.Cleanup(mock.Close)
			starter := &turnAttemptUnitStarter{PgxPoolIface: mock}
			sessions, redis := turnAttemptStreams(t, starter)
			records := turnAttemptCaptureCommands(redis, nil)
			store := NewTurnStore(starter, sessions, nil, nil, nil, nil)
			orgID, sessionID := uuid.New(), uuid.New()
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			redis.SetTime(at)
			failure := errors.New("synthetic attempt " + tt.failure)
			begin := mock.ExpectBegin()
			if tt.failure == "begin" {
				begin.WillReturnError(failure)
			}
			starter.beforeCommit = func() {
				require.Empty(t, redis.Keys(), "even the last instant before outer commit must have no status or workspace wake")
			}
			var expectedStatus []models.Session
			if tt.failure != "begin" {
				kinds := []string{tt.kind}
				if tt.kind == "ordered" {
					kinds = []string{"status", "result", "turn"}
				}
				for _, kind := range kinds {
					projection := models.Session{ID: sessionID, OrgID: orgID, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd, CapabilitySnapshot: []models.AgentCapabilitySnapshotItem{}}
					projection.Status = models.SessionStatusAwaitingInput
					if kind == "result" {
						projection.Status, projection.CompletedAt = models.SessionStatusFailed, &at
					}
					if kind == "turn" {
						write := mock.ExpectExec(`(?s)UPDATE sessions.+WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID, "current_turn": 2, "agent_session_id": "synthetic-agent", "snapshot_key": "synthetic-checkpoint", "token_usage": json.RawMessage(nil), "model_used": (*string)(nil), "result_summary": (*string)(nil), "diff": (*string)(nil), "error": (*string)(nil), "base_commit_sha": (*string)(nil), "diff_collected_at": (*time.Time)(nil), "diff_stats": json.RawMessage(nil)})
						if tt.failure == "row" {
							write.WillReturnError(failure)
						} else {
							write.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
						}
					} else {
						args := pgx.NamedArgs{"id": sessionID, "org_id": orgID, "status": string(projection.Status)}
						if kind == "result" {
							args = pgx.NamedArgs{"id": sessionID, "org_id": orgID, "status": string(projection.Status), "token_usage": json.RawMessage(nil), "model_used": (*string)(nil), "result_summary": (*string)(nil), "diff": (*string)(nil), "error": (*string)(nil), "failure_explanation": (*string)(nil), "failure_category": (*string)(nil), "failure_next_steps": []string(nil), "failure_retry_advised": false, "base_commit_sha": (*string)(nil), "diff_collected_at": (*time.Time)(nil), "diff_stats": json.RawMessage(nil)}
						}
						write := mock.ExpectQuery(`(?s)UPDATE sessions.+WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`).WithArgs(args)
						if tt.failure == "row" {
							write.WillReturnError(failure)
						} else {
							write.WillReturnRows(turnAttemptSessionRows(projection))
						}
						expectedStatus = append(expectedStatus, projection)
					}
				}
				if tt.failure == "row" || tt.failure == "callback" {
					mock.ExpectRollback()
				} else {
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
			err = store.EndAttempt(context.Background(), func(ctx context.Context, tx pgx.Tx, bound agent.SessionStore) error {
				kinds := []string{tt.kind}
				if tt.kind == "ordered" {
					kinds = []string{"status", "result", "turn"}
				}
				for _, kind := range kinds {
					var err error
					switch kind {
					case "status":
						err = bound.UpdateStatus(ctx, orgID, sessionID, models.SessionStatusAwaitingInput)
					case "result":
						err = bound.UpdateResult(ctx, orgID, sessionID, models.SessionStatusFailed, nil)
					default:
						err = bound.UpdateTurnComplete(ctx, orgID, sessionID, 2, nil, "synthetic-agent", "synthetic-checkpoint")
					}
					if err != nil {
						return err
					}
					require.Empty(t, redis.Keys(), "the actual transaction-bound callback must never publish its captured effects")
				}
				if tt.failure == "callback" {
					return failure
				}
				return nil
			})
			if tt.failure != "" && tt.failure != "notification" {
				require.ErrorIs(t, err, failure, "the owner must preserve raw begin, persistence, callback and commit errors without retry")
				require.Empty(t, redis.Keys(), "every failed or uncertain owner must discard all captured effects")
			} else {
				require.NoError(t, err, "an optional wake failure cannot reject successfully committed persistence")
				if tt.failure != "notification" {
					requireTurnAttemptStatusEvents(t, redis, sessionID, expectedStatus)
					var expectedCommands []string
					for _, session := range expectedStatus {
						prefix := "143:stream:{ses:" + sessionID.String() + "}:"
						expectedCommands = append(expectedCommands, "XADD:"+prefix+"status")
						if session.Status == models.SessionStatusFailed {
							expectedCommands = append(expectedCommands, "EXPIREAT:"+prefix+"logs", "EXPIREAT:"+prefix+"status", "EXPIREAT:"+prefix+"events")
						}
					}
					commands, hookErrors := records()
					require.Empty(t, hookErrors, "notification observation must not fail")
					require.Equal(t, expectedCommands, commands, "the owner must preserve call-order status and terminal-expiry notifications without adding a diff-free turn event")
				}
			}
			if starter.tx != nil {
				require.Zero(t, starter.tx.nested, "no persistence callback may begin an inner transaction or savepoint")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all callback work must remain on the one supplied transaction without pool fallback or retries")
		})
	}
}

func TestTurnAttemptPersistenceRequiresConcreteTransaction(t *testing.T) {
	t.Parallel()
	tests := []string{"status", "result", "turn"}
	for _, kind := range tests {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			bound := newTurnAttemptSessions(nil, db.NewSessionStore(nil))
			var err error
			switch kind {
			case "status":
				err = bound.UpdateStatus(context.Background(), uuid.New(), uuid.New(), models.SessionStatusAwaitingInput)
			case "result":
				err = bound.UpdateResult(context.Background(), uuid.New(), uuid.New(), models.SessionStatusFailed, nil)
			default:
				err = bound.UpdateTurnComplete(context.Background(), uuid.New(), uuid.New(), 1, nil, "", "")
			}
			require.ErrorContains(t, err, "transaction is required", "the facade must use the explicit concrete seam rather than its embedded store as a fallback")
			require.Empty(t, bound.effects, "invalid transaction inputs cannot collect accepted notifications")
		})
	}
}

func turnAttemptSessionRows(session models.Session) *pgxmock.Rows {
	value, definition := reflect.ValueOf(session), reflect.TypeOf(session)
	var columns []string
	var row []any
	for i := 0; i < definition.NumField(); i++ {
		column := definition.Field(i).Tag.Get("db")
		if column == "-" {
			continue
		}
		if column == "" {
			column = definition.Field(i).Name
		}
		columns, row = append(columns, column), append(row, value.Field(i).Interface())
	}
	return pgxmock.NewRows(columns).AddRow(row...)
}

func turnAttemptStreams(t *testing.T, database db.DBTX) (*db.SessionStore, *miniredis.Miniredis) {
	t.Helper()
	redis := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redis.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "enable the real original-store Redis publisher so absent wiring cannot hide early effects")
	t.Cleanup(func() { require.NoError(t, client.Close(), "close this case's Redis client") })
	store := db.NewSessionStore(database)
	store.SetStreams(cache.NewSessionStreams(client, zerolog.Nop(), nil))
	return store, redis
}

func turnAttemptCaptureCommands(redis *miniredis.Miniredis, verify func() error) func() ([]string, []error) {
	var mu sync.Mutex
	var commands []string
	var hookErrors []error
	redis.Server().SetPreHook(func(_ *server.Peer, command string, args ...string) bool {
		if len(args) == 0 || (!strings.EqualFold(command, "XADD") && !strings.EqualFold(command, "EXPIREAT")) {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		commands = append(commands, strings.ToUpper(command)+":"+args[0])
		if strings.EqualFold(command, "XADD") && verify != nil {
			if err := verify(); err != nil {
				hookErrors = append(hookErrors, err)
			}
		}
		return false
	})
	return func() ([]string, []error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), commands...), append([]error(nil), hookErrors...)
	}
}

func requireTurnAttemptStatusEvents(t *testing.T, redis *miniredis.Miniredis, sessionID uuid.UUID, sessions []models.Session) {
	t.Helper()
	var actual, expected []string
	key := "143:stream:{ses:" + sessionID.String() + "}:status"
	if redis.Exists(key) {
		entries, err := redis.Stream(key)
		require.NoError(t, err, "read the exact post-commit status stream")
		for _, entry := range entries {
			actual = append(actual, entry.Values[1])
		}
	}
	for _, session := range sessions {
		encoded, err := json.Marshal(session)
		require.NoError(t, err, "encode every original returned status field")
		expected = append(expected, string(encoded))
	}
	require.Equal(t, expected, actual, "owner publication must retain the exact ordered serialized returned rows, not later reloads")
}
