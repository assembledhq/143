package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type threadPersistenceFixture struct {
	orgID, sessionID, threadID uuid.UUID
	status                     models.ThreadStatus
	turn                       int
	agentSessionID             string
	result                     *models.SessionResult
	at                         time.Time
}

func newThreadPersistenceFixture() threadPersistenceFixture {
	summary, diff, rawError, explanation, category := "synthetic summary", "synthetic diff", "synthetic raw failure", "synthetic explanation", "synthetic_category"
	return threadPersistenceFixture{
		orgID: uuid.New(), sessionID: uuid.New(), threadID: uuid.New(), status: models.ThreadStatusFailed,
		turn: 2, agentSessionID: "synthetic-provider", at: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		result: &models.SessionResult{ResultSummary: &summary, Diff: &diff, Error: &rawError, FailureExplanation: &explanation, FailureCategory: &category},
	}
}

type threadPersistenceWrite struct {
	SQL  string
	Args []any
}

type threadPersistenceTx struct {
	pgx.Tx
	reader      DBTX
	writes      []threadPersistenceWrite
	contexts    []context.Context
	reads       int
	settlements []string
	writeErr    error
	rows        int64
}

func (tx *threadPersistenceTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.writes = append(tx.writes, threadPersistenceWrite{SQL: strings.Join(strings.Fields(sql), " "), Args: args})
	tx.contexts = append(tx.contexts, ctx)
	if tx.writeErr != nil {
		return pgconn.CommandTag{}, tx.writeErr
	}
	if tx.rows == 0 {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (tx *threadPersistenceTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.reads++
	if tx.reader == nil {
		return nil, errors.New("unexpected persistence read")
	}
	return tx.reader.Query(ctx, sql, args...)
}

func (tx *threadPersistenceTx) Begin(context.Context) (pgx.Tx, error) {
	tx.settlements = append(tx.settlements, "begin")
	return nil, errors.New("inner begin is forbidden")
}

func (tx *threadPersistenceTx) Commit(context.Context) error {
	tx.settlements = append(tx.settlements, "commit")
	return errors.New("inner commit is forbidden")
}

func (tx *threadPersistenceTx) Rollback(context.Context) error {
	tx.settlements = append(tx.settlements, "rollback")
	return errors.New("inner rollback is forbidden")
}

func callThreadPersistenceInTx(ctx context.Context, store *SessionThreadStore, tx pgx.Tx, f threadPersistenceFixture, kind string) (ThreadPersistenceEffects, error) {
	switch kind {
	case "status":
		return store.UpdateStatusInTx(ctx, f.orgID, tx, f.threadID, f.status)
	case "complete":
		return store.CompleteTurnInTx(ctx, f.orgID, tx, f.threadID, f.turn, f.agentSessionID)
	case "result":
		return store.UpdateResultInTx(ctx, f.orgID, tx, f.threadID, f.status, f.result)
	default:
		return store.UpdateTurnCompleteInTx(ctx, f.orgID, tx, f.threadID, f.turn, f.result, f.agentSessionID)
	}
}

func callThreadPersistenceWrapper(store *SessionThreadStore, f threadPersistenceFixture, kind string) error {
	switch kind {
	case "status":
		return store.UpdateStatus(context.Background(), f.orgID, f.threadID, f.status)
	case "complete":
		return store.CompleteTurn(context.Background(), f.orgID, f.threadID, f.turn, f.agentSessionID)
	case "result":
		return store.UpdateResult(context.Background(), f.orgID, f.threadID, f.status, f.result)
	default:
		return store.UpdateTurnComplete(context.Background(), f.orgID, f.threadID, f.turn, f.result, f.agentSessionID)
	}
}

func expectedThreadPersistenceWrite(f threadPersistenceFixture, kind string) threadPersistenceWrite {
	args := pgx.NamedArgs{"id": f.threadID, "org_id": f.orgID}
	var query string
	switch kind {
	case "status":
		args["status"] = f.status
		query = "UPDATE session_threads SET status = @status WHERE id = @id AND org_id = @org_id"
		if f.status == models.ThreadStatusRunning {
			query = "UPDATE session_threads SET status = @status, started_at = now(), cancel_requested_at = NULL WHERE id = @id AND org_id = @org_id"
		} else if f.status == models.ThreadStatusCompleted || f.status == models.ThreadStatusFailed || f.status == models.ThreadStatusCancelled {
			query = "UPDATE session_threads SET status = @status, completed_at = now() WHERE id = @id AND org_id = @org_id"
		}
	case "complete":
		query = "UPDATE session_threads SET status = 'idle', current_turn = @current_turn, last_activity_at = now(), agent_session_id = COALESCE(@agent_session_id, agent_session_id) WHERE id = @id AND org_id = @org_id"
		args["current_turn"] = f.turn
		var provider *string
		if f.agentSessionID != "" {
			provider = &f.agentSessionID
		}
		args["agent_session_id"] = provider
	case "result":
		query = "UPDATE session_threads SET status = @status, completed_at = CASE WHEN @status IN ('completed', 'failed', 'cancelled') THEN now() ELSE completed_at END, result_summary = @result_summary, diff = COALESCE(@diff, diff), failure_explanation = @failure_explanation, failure_category = @failure_category WHERE id = @id AND org_id = @org_id"
		args["status"], args["result_summary"], args["diff"], args["failure_category"] = f.status, f.result.ResultSummary, f.result.Diff, f.result.FailureCategory
		args["failure_explanation"] = f.result.FailureExplanation
		if f.result.FailureExplanation == nil {
			args["failure_explanation"] = f.result.Error
		}
	default:
		query = "UPDATE session_threads SET status = 'idle', current_turn = @current_turn, last_activity_at = now(), agent_session_id = @agent_session_id, result_summary = @result_summary, diff = COALESCE(@diff, diff), failure_explanation = NULL, failure_category = NULL WHERE id = @id AND org_id = @org_id"
		args["current_turn"], args["agent_session_id"], args["result_summary"], args["diff"] = f.turn, f.agentSessionID, f.result.ResultSummary, f.result.Diff
	}
	return threadPersistenceWrite{SQL: query, Args: []any{args}}
}

func threadPersistenceStoreWithStreams(t *testing.T, database DBTX) (*SessionThreadStore, *miniredis.Miniredis) {
	t.Helper()
	redis := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redis.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "enable this test's real Redis publisher so missing wiring cannot conceal early effects")
	t.Cleanup(func() { require.NoError(t, client.Close(), "close the isolated thread-event publisher") })
	store := NewSessionThreadStore(database)
	store.SetStreams(cache.NewSessionStreams(client, zerolog.Nop(), nil))
	return store, redis
}

func TestThreadPersistenceInTx(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, outcome string }{}
	for _, kind := range []string{"status", "complete", "result", "turn"} {
		for _, outcome := range []string{"success", "write error", "cancelled", "missing row"} {
			tests = append(tests, struct{ kind, outcome string }{kind, outcome})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tx, unused := &threadPersistenceTx{rows: 1}, &threadPersistenceTx{rows: 1}
			failure := errors.New("synthetic write failure")
			if tt.outcome == "write error" {
				tx.writeErr = failure
			} else if tt.outcome == "cancelled" {
				tx.writeErr = context.Canceled
				cancel()
			} else if tt.outcome == "missing row" {
				tx.rows = 0
			}
			store, redis := threadPersistenceStoreWithStreams(t, unused)
			effects, err := callThreadPersistenceInTx(ctx, store, tx, fixture, tt.kind)
			switch tt.outcome {
			case "success":
				require.NoError(t, err, "the existing thread mutation must compose in the supplied transaction")
				require.Equal(t, ThreadPersistenceEffects{orgID: fixture.orgID, threadID: fixture.threadID, runtimeChanged: true}, effects, "successful persistence returns only the exact tenant-scoped reload identity")
			case "write error":
				require.Equal(t, failure, err, "the exact original database error must reach the outer owner unchanged")
				require.Equal(t, ThreadPersistenceEffects{}, effects, "failed writes must return no accepted notification identity")
			case "cancelled":
				require.Equal(t, context.Canceled, err, "cancellation must retain its exact original classification")
				require.Equal(t, ThreadPersistenceEffects{}, effects, "cancelled writes must return no accepted notification identity")
			default:
				require.EqualError(t, err, "thread not found", "missing rows must retain the exact legacy error")
				require.Equal(t, ThreadPersistenceEffects{}, effects, "missing rows must return no accepted notification identity")
			}
			require.Equal(t, []threadPersistenceWrite{expectedThreadPersistenceWrite(fixture, tt.kind)}, tx.writes, "every SQL predicate and named argument must retain the legacy mutation contract")
			require.Equal(t, []context.Context{ctx}, tx.contexts, "persistence must pass the caller's exact context, including cancellation, to its database operation")
			require.Zero(t, tx.reads, "InTx must not reload a row to publish before commit")
			require.Empty(t, tx.settlements, "only the caller may begin, commit or roll back the unit")
			require.Empty(t, unused.writes, "persistence must never borrow the receiver's separate database handle")
			require.Zero(t, unused.reads, "persistence must not read through the receiver's separate database handle")
			require.Empty(t, redis.Keys(), "enabled publishers must remain silent on every uncommitted success and failure path")
		})
	}
}

func TestThreadPersistenceLegacyWriteFailures(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, outcome string }{}
	for _, kind := range []string{"status", "complete", "result", "turn"} {
		for _, outcome := range []string{"write error", "missing row"} {
			tests = append(tests, struct{ kind, outcome string }{kind, outcome})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			failure := errors.New("synthetic legacy write failure")
			database := &threadPersistenceTx{rows: 1}
			if tt.outcome == "write error" {
				database.writeErr = failure
			} else {
				database.rows = 0
			}
			store, redis := threadPersistenceStoreWithStreams(t, database)
			err := callThreadPersistenceWrapper(store, fixture, tt.kind)
			if tt.outcome == "write error" {
				require.Equal(t, failure, err, "extraction must preserve the exact original legacy execution error")
			} else {
				require.EqualError(t, err, "thread not found", "legacy wrappers must retain the original missing-row error")
			}
			require.Equal(t, []threadPersistenceWrite{expectedThreadPersistenceWrite(fixture, tt.kind)}, database.writes, "legacy failure paths must execute only the unchanged mutation")
			require.Zero(t, database.reads, "legacy failed writes must not attempt notification reloads")
			require.Empty(t, database.settlements, "legacy wrappers must not add a new transaction boundary")
			require.Empty(t, redis.Keys(), "legacy write failure must not advertise an unapplied runtime update")
		})
	}
}

func TestThreadPersistenceInTxRejectsMissingInputs(t *testing.T) {
	t.Parallel()
	var typedNil *threadPersistenceTx
	tests := []struct {
		kind, missing, expected string
		tx                      pgx.Tx
	}{}
	for _, operation := range []struct{ kind, prefix string }{{"status", "persist thread status"}, {"complete", "persist thread turn"}, {"result", "persist thread result"}, {"turn", "persist thread turn result"}} {
		for _, missing := range []struct {
			name string
			tx   pgx.Tx
		}{{"nil transaction", nil}, {"typed nil transaction", typedNil}} {
			tests = append(tests, struct {
				kind, missing, expected string
				tx                      pgx.Tx
			}{operation.kind, missing.name, operation.prefix + ": transaction is required", missing.tx})
		}
		if operation.kind == "result" || operation.kind == "turn" {
			tests = append(tests, struct {
				kind, missing, expected string
				tx                      pgx.Tx
			}{operation.kind, "nil result", operation.prefix + ": result is required", &threadPersistenceTx{rows: 1}})
		}
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.missing, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			if tt.missing == "nil result" {
				fixture.result = nil
			}
			unused := &threadPersistenceTx{rows: 1}
			store, redis := threadPersistenceStoreWithStreams(t, unused)
			effects, err := callThreadPersistenceInTx(context.Background(), store, tt.tx, fixture, tt.kind)
			require.EqualError(t, err, tt.expected, "invalid explicit inputs must be rejected before any database access")
			require.Equal(t, ThreadPersistenceEffects{}, effects, "invalid inputs must never return publishable effects")
			require.Empty(t, unused.writes, "invalid input handling must not fall back to the receiver database")
			if tx, ok := tt.tx.(*threadPersistenceTx); ok && tx != nil {
				require.Empty(t, tx.writes, "nil results must be rejected before staging a write")
				require.Empty(t, tx.settlements, "input rejection must leave transaction ownership untouched")
			}
			require.Empty(t, redis.Keys(), "invalid inputs must never publish even with streams configured")
		})
	}
}

func TestThreadPersistenceLegacyAndArgumentCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct{ kind, variant string }{{"status", "running"}, {"status", "completed"}, {"status", "cancelled"}, {"status", "idle"}, {"complete", "empty provider"}, {"complete", "provider"}, {"result", "nil diff"}, {"result", "empty diff"}, {"result", "raw error fallback"}, {"result", "empty result"}, {"turn", "empty provider"}, {"turn", "nil diff"}, {"turn", "empty diff"}}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.variant, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			switch tt.variant {
			case "running", "completed", "cancelled", "idle":
				fixture.status = models.ThreadStatus(tt.variant)
			case "empty provider":
				fixture.agentSessionID = ""
			case "nil diff":
				fixture.result.Diff = nil
			case "empty diff":
				empty := ""
				fixture.result.Diff = &empty
			case "raw error fallback":
				fixture.result.FailureExplanation = nil
			case "empty result":
				fixture.result = &models.SessionResult{}
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the legacy notification reload mock")
			t.Cleanup(mock.Close)
			row := newSessionThreadRow(fixture.threadID, fixture.sessionID, fixture.orgID, "synthetic thread", fixture.at)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT "+sessionThreadSelectColumns+" FROM session_threads WHERE id = @id AND org_id = @org_id AND archived_at IS NULL")).
				WithArgs(fixture.threadID, fixture.orgID).WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
			legacy := &threadPersistenceTx{reader: mock, rows: 1}
			store, redis := threadPersistenceStoreWithStreams(t, legacy)
			require.NoError(t, callThreadPersistenceWrapper(store, fixture, tt.kind), "legacy wrappers must retain their write and immediate best-effort notification behavior")
			require.Equal(t, []threadPersistenceWrite{expectedThreadPersistenceWrite(fixture, tt.kind)}, legacy.writes, "legacy SQL and exact nil-versus-empty argument values must remain unchanged")
			require.Equal(t, 1, legacy.reads, "legacy success must still reload once before returning to its caller")
			tx := &threadPersistenceTx{rows: 1}
			_, err = callThreadPersistenceInTx(context.Background(), store, tx, fixture, tt.kind)
			require.NoError(t, err, "the matching InTx entrypoint must accept the same non-nil argument values")
			require.Equal(t, legacy.writes, tx.writes, "wrapper and explicit-transaction entrypoints must share the exact same mutation rather than duplicate SQL owners")
			requireThreadPersistenceEvent(t, redis, models.ThreadRuntimeEvent{SessionID: fixture.sessionID, ThreadID: fixture.threadID, OrgID: fixture.orgID, Status: models.ThreadStatusPending})
			require.NoError(t, mock.ExpectationsWereMet(), "the legacy publication query must retain its exact tenant and archive scope")
		})
	}
}

func TestThreadPersistenceLegacyNilResult(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"result", "turn"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			fixture.result = nil
			database := &threadPersistenceTx{rows: 1}
			store, redis := threadPersistenceStoreWithStreams(t, database)
			require.Panics(t, func() {
				require.NoError(t, callThreadPersistenceWrapper(store, fixture, kind), "a legacy nil result must panic before returning a persistence outcome")
			}, "extraction must not silently normalize a nil legacy result into destructive empty fields")
			require.Empty(t, database.writes, "legacy nil dereference must still occur before any persistence")
			require.Empty(t, redis.Keys(), "a rejected legacy call must not send a runtime notification")
		})
	}
}

func TestThreadPersistenceCommittedEffects(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "zero effects", "foreign organization", "reload failure", "publish failure", "streams disabled"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			fixture := newThreadPersistenceFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create this post-commit adapter's isolated reload handle")
			t.Cleanup(mock.Close)
			store, redis := threadPersistenceStoreWithStreams(t, mock)
			var logs bytes.Buffer
			store.SetLogger(zerolog.New(&logs))
			effects := ThreadPersistenceEffects{orgID: fixture.orgID, threadID: fixture.threadID, runtimeChanged: true}
			orgID := fixture.orgID
			if outcome == "zero effects" {
				effects = ThreadPersistenceEffects{}
			} else if outcome == "foreign organization" {
				orgID = uuid.New()
			} else if outcome == "streams disabled" {
				store.SetStreams(nil)
			} else {
				query := mock.ExpectQuery("SELECT").WithArgs(fixture.threadID, fixture.orgID)
				if outcome == "reload failure" {
					query.WillReturnError(errors.New("synthetic reload failure"))
				} else {
					row := newSessionThreadRow(fixture.threadID, fixture.sessionID, fixture.orgID, "synthetic later state", fixture.at)
					row[9], row[11] = models.ThreadStatusIdle, 9
					query.WillReturnRows(pgxmock.NewRows(sessionThreadTestColumns).AddRow(row...))
					if outcome == "publish failure" {
						redis.SetError("synthetic Redis failure")
					}
				}
			}
			store.PublishCommittedThreadPersistenceEffects(context.Background(), orgID, effects)
			if outcome == "success" {
				requireThreadPersistenceEvent(t, redis, models.ThreadRuntimeEvent{SessionID: fixture.sessionID, ThreadID: fixture.threadID, OrgID: fixture.orgID, Status: models.ThreadStatusIdle, CurrentTurn: 9})
			} else {
				require.Empty(t, redis.Keys(), "rejected or failed notification delivery must not create an event")
			}
			if outcome == "reload failure" || outcome == "publish failure" {
				var record map[string]any
				require.NoError(t, json.Unmarshal(logs.Bytes(), &record), "best-effort post-commit failures must remain observable through structured logs")
				require.Equal(t, "warn", record["level"], "notification failures must warn without rejecting already committed work")
			}
			if outcome == "foreign organization" {
				var record map[string]any
				require.NoError(t, json.Unmarshal(logs.Bytes(), &record), "foreign effects must be rejected explicitly rather than reaching an unexpected reload whose error is swallowed")
				require.Equal(t, map[string]any{"level": "error", "org_id": orgID.String(), "effects_org_id": fixture.orgID.String(), "message": "refusing to publish thread persistence effects for another organization"}, record, "reject the exact foreign effect identity before any database or stream access")
			} else if outcome == "zero effects" {
				require.Empty(t, logs.String(), "zero effects must return without even an unexpected reload warning")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "post-commit publication must perform only the expected tenant-scoped current-state reload")
		})
	}
}

func requireThreadPersistenceEvent(t *testing.T, redis *miniredis.Miniredis, expected models.ThreadRuntimeEvent) {
	t.Helper()
	entries, err := redis.Stream("143:stream:{ses:" + expected.SessionID.String() + "}:events")
	require.NoError(t, err, "explicit thread publication must use the existing session event stream")
	var actual []models.SessionStreamEvent
	for _, entry := range entries {
		var event models.SessionStreamEvent
		require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode every published thread runtime event")
		actual = append(actual, event)
	}
	encoded, err := json.Marshal(expected)
	require.NoError(t, err, "encode the complete expected current-state runtime data")
	var data any
	require.NoError(t, json.Unmarshal(encoded, &data), "normalize only the event's interface-encoded payload for exact comparison")
	require.Equal(t, []models.SessionStreamEvent{{Type: models.SessionStreamEventThreadRuntimeUpdated, SessionID: expected.SessionID, OrgID: expected.OrgID, Data: data}}, actual, "one explicit adapter call must preserve the complete current visible runtime event, not an earlier write snapshot")
}
