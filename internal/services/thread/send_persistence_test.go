package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewSendDependencies(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, missing string }{
		{"complete", ""}, {"missing pool", "pool"}, {"missing sessions", "sessions"},
		{"missing threads", "threads"}, {"missing jobs", "jobs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := &pgxpool.Pool{}
			sessions, threads, jobs := db.NewSessionStore(nil), db.NewSessionThreadStore(nil), db.NewJobStore(nil)
			switch tt.missing {
			case "pool":
				pool = nil
			case "sessions":
				sessions = nil
			case "threads":
				threads = nil
			case "jobs":
				jobs = nil
			}
			actual, err := NewSendDependencies(pool, sessions, threads, jobs)
			if tt.missing != "" {
				require.Error(t, err, "missing production dependencies must reject without acquiring a connection")
				require.Equal(t, SendDependencies{}, actual, "construction failure must expose no partial send dependencies")
				return
			}
			require.NoError(t, err, "complete construction must not query the uninitialized pool")
			require.NoError(t, actual.Validate(), "production construction must supply every mandatory dependency")
			require.IsType(t, &db.ThreadSendTransactions{}, actual.Transactions, "production must use the sealed pool-backed runner")
			require.Equal(t, &sqlLegacySendPersistence{sessions: sessions, threads: threads, jobs: jobs}, actual.Legacy, "persistence must retain the exact configured stores")
			require.Equal(t, &sqlSendEffects{sessions: sessions, threads: threads, jobs: jobs}, actual.Effects, "effects must retain publishers on usable store handles")
		})
	}
}

func TestSendDependenciesValidate(t *testing.T) {
	t.Parallel()
	var nilTransactions *db.ThreadSendTransactions
	var nilPersistence *sqlLegacySendPersistence
	var nilEffects *sqlSendEffects
	tests := []struct {
		name   string
		change func(*SendDependencies)
		want   string
	}{
		{"valid", func(*SendDependencies) {}, ""},
		{"nil transactions", func(d *SendDependencies) { d.Transactions = nil }, "transactions"},
		{"typed nil transactions", func(d *SendDependencies) { d.Transactions = nilTransactions }, "transactions"},
		{"nil persistence", func(d *SendDependencies) { d.Legacy = nil }, "legacy persistence"},
		{"typed nil persistence", func(d *SendDependencies) { d.Legacy = nilPersistence }, "legacy persistence"},
		{"nil effects", func(d *SendDependencies) { d.Effects = nil }, "effects"},
		{"typed nil effects", func(d *SendDependencies) { d.Effects = nilEffects }, "effects"},
		{"zero", func(d *SendDependencies) { *d = SendDependencies{} }, "transactions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps, err := NewSendDependencies(&pgxpool.Pool{}, db.NewSessionStore(nil), db.NewSessionThreadStore(nil), db.NewJobStore(nil))
			require.NoError(t, err, "construct inert dependencies without a database operation")
			tt.change(&deps)
			err = deps.Validate()
			if tt.want == "" {
				require.NoError(t, err, "complete dependencies must validate")
			} else {
				require.EqualError(t, err, "thread send: "+tt.want+" is required", "nil interface values must fail before routing or SQL")
			}
		})
	}
}

type sendPersistenceStatement struct {
	SQL  string
	Args []any
}

// This supplied transaction records ownership calls separately from queries.
// It never provides lifecycle authority or simulates a mode resolver.
type sendPersistenceTx struct {
	pgx.Tx
	reader     db.DBTX
	statements []sendPersistenceStatement
	contexts   []context.Context
	settled    []string
	commitErr  error
}

func (tx *sendPersistenceTx) record(ctx context.Context, sql string, args []any) {
	tx.statements = append(tx.statements, sendPersistenceStatement{strings.Join(strings.Fields(sql), " "), args})
	tx.contexts = append(tx.contexts, ctx)
}

func (tx *sendPersistenceTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.record(ctx, sql, args)
	return tx.reader.Query(ctx, sql, args...)
}

func (tx *sendPersistenceTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.record(ctx, sql, args)
	return tx.reader.QueryRow(ctx, sql, args...)
}

func (tx *sendPersistenceTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.record(ctx, sql, args)
	return tx.reader.Exec(ctx, sql, args...)
}

func (tx *sendPersistenceTx) Begin(context.Context) (pgx.Tx, error) {
	tx.settled = append(tx.settled, "begin")
	return nil, errors.New("adapter cannot begin")
}

func (tx *sendPersistenceTx) Commit(context.Context) error {
	tx.settled = append(tx.settled, "commit")
	return tx.commitErr
}

func (tx *sendPersistenceTx) Rollback(context.Context) error {
	tx.settled = append(tx.settled, "rollback")
	return nil
}

type sendPersistenceFixture struct {
	org, session, thread uuid.UUID
}

func newSendPersistenceFixture() sendPersistenceFixture {
	return sendPersistenceFixture{uuid.New(), uuid.New(), uuid.New()}
}

func newSQLSendPersistence(database db.DBTX) *sqlLegacySendPersistence {
	return &sqlLegacySendPersistence{sessions: db.NewSessionStore(database), threads: db.NewSessionThreadStore(database), jobs: db.NewJobStore(database)}
}

type sendPersistenceOperation struct {
	name, query string
	exec        bool
	call        func(context.Context, *sqlLegacySendPersistence, pgx.Tx, sendPersistenceFixture) error
	direct      func(context.Context, *sqlLegacySendPersistence, pgx.Tx, sendPersistenceFixture) error
}

func sendPersistenceOperations() []sendPersistenceOperation {
	return []sendPersistenceOperation{
		{"idle thread", "WITH locked_threads", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, _, err := p.ClaimIdleThreadInTx(ctx, tx, f.org, f.session, f.thread, 2)
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, _, err := p.threads.ClaimIdleForSessionInTx(ctx, f.org, tx, f.session, f.thread, 2)
				return err
			}},
		{"resume thread", "WITH locked_threads", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, _, err := p.ClaimResumeThreadInTx(ctx, tx, f.org, f.session, f.thread, 2)
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, _, err := p.threads.ClaimForResumeInSessionInTx(ctx, f.org, tx, f.session, f.thread, 2)
				return err
			}},
		{"idle session", "UPDATE sessions", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.ClaimIdleSessionInTx(ctx, tx, f.org, f.session)
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.sessions.ClaimIdleInTx(ctx, f.org, tx, f.session)
				return err
			}},
		{"resume session", "UPDATE sessions", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.ClaimResumeSessionInTx(ctx, tx, f.org, f.session)
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.sessions.ClaimForResumeInTx(ctx, f.org, tx, f.session)
				return err
			}},
		{"thread status", "UPDATE session_threads", true,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.UpdateThreadStatusInTx(ctx, tx, f.org, f.thread, models.ThreadStatusIdle)
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.threads.UpdateStatusInTx(ctx, f.org, tx, f.thread, models.ThreadStatusIdle)
				return err
			}},
		{"session status", "UPDATE sessions", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.UpdateSessionStatusInTx(ctx, tx, f.org, f.session, models.SessionStatusIdle)
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.sessions.UpdateStatusInTx(ctx, f.org, tx, f.session, models.SessionStatusIdle)
				return err
			}},
		{"pending", "UPDATE session_threads", true,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.IncrementPendingMessagesInTx(ctx, tx, f.org, f.thread)
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.threads.IncrementPendingMessagesInTx(ctx, f.org, tx, f.thread)
				return err
			}},
		{"message", "INSERT INTO session_messages", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				return p.CreateMessageInTx(ctx, tx, f.org, f.message(""))
			}, func(ctx context.Context, _ *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				return db.NewSessionMessageStore(tx).Create(ctx, f.message(""))
			}},
		{"sourced message", "INSERT INTO session_messages", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				return p.CreateMessageInTx(ctx, tx, f.org, f.message(models.SessionMessageSourceAgentTool))
			}, func(ctx context.Context, _ *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				return db.NewSessionMessageStore(tx).CreateWithSource(ctx, f.message(models.SessionMessageSourceAgentTool))
			}},
		{"inbox", "WITH locked_thread", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.AppendInboxInTx(ctx, tx, f.org, f.inbox())
				return err
			}, func(ctx context.Context, _ *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := db.NewThreadInboxStore(tx).AppendForMessage(ctx, f.org, f.inbox())
				return err
			}},
		{"enqueue", "INSERT INTO jobs", false,
			func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.EnqueueInTx(ctx, tx, f.org, f.enqueue())
				return err
			}, func(ctx context.Context, p *sqlLegacySendPersistence, tx pgx.Tx, f sendPersistenceFixture) error {
				_, err := p.jobs.EnqueueInTxWithOpts(ctx, tx, f.org, f.enqueue())
				return err
			}},
	}
}

func (f sendPersistenceFixture) message(source models.SessionMessageSource) *models.SessionMessage {
	return &models.SessionMessage{OrgID: f.org, SessionID: f.session, ThreadID: &f.thread, Role: models.MessageRoleUser, Content: "synthetic input", TurnNumber: 2, Source: source}
}

func (f sendPersistenceFixture) inbox() db.AppendThreadInboxEntryParams {
	return db.AppendThreadInboxEntryParams{SessionID: f.session, ThreadID: f.thread, MessageID: 42, ClientMessageID: "synthetic-client-key", EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: json.RawMessage(`{"message_id":42}`)}
}

func (f sendPersistenceFixture) enqueue() db.EnqueueOpts {
	key, node := "synthetic-dedupe", "synthetic-node"
	return db.EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: map[string]string{"session_id": f.session.String(), "thread_id": f.thread.String(), "org_id": f.org.String()}, Priority: 5, DedupeKey: &key, TargetNodeID: &node}
}

func TestLegacySendPersistenceDelegatesExactSuppliedTransaction(t *testing.T) {
	t.Parallel()
	for _, tt := range sendPersistenceOperations() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture, failure := newSendPersistenceFixture(), errors.New("synthetic database failure")
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated supplied transaction reader")
			t.Cleanup(mock.Close)
			p := newSQLSendPersistence(nil)
			actualTx, expectedTx := &sendPersistenceTx{reader: mock}, &sendPersistenceTx{reader: mock}
			argCount := map[string]int{"idle thread": 5, "resume thread": 5, "idle session": 2, "resume session": 3, "thread status": 3, "session status": 3, "pending": 2, "message": 11, "sourced message": 12, "inbox": 7, "enqueue": 7}[tt.name]
			for range 2 {
				if tt.exec {
					mock.ExpectExec(tt.query).WithArgs(sendPersistenceAnyArgs(argCount)...).WillReturnError(failure)
				} else {
					mock.ExpectQuery(tt.query).WithArgs(sendPersistenceAnyArgs(argCount)...).WillReturnError(failure)
				}
			}
			ctx := context.Background()
			actualErr := tt.call(ctx, p, actualTx, fixture)
			expectedErr := tt.direct(ctx, p, expectedTx, fixture)
			require.ErrorIs(t, actualErr, failure, "adapter must retain the original database error identity")
			require.EqualError(t, actualErr, expectedErr.Error(), "adapter must preserve the established helper's error semantics")
			require.Equal(t, expectedTx.statements, actualTx.statements, "adapter must preserve exact SQL and every caller argument on the supplied transaction")
			require.Equal(t, []context.Context{ctx}, actualTx.contexts, "adapter must pass the caller's context unchanged")
			require.Empty(t, actualTx.settled, "persistence must never begin, commit or roll back its owner's transaction")
			require.NoError(t, mock.ExpectationsWereMet(), "adapter must perform no hidden diagnostics, settings, retries or pool fallback")
		})
	}
}

func TestLegacySendPersistenceRejectsInvalidBoundaryBeforeSQL(t *testing.T) {
	t.Parallel()
	var nilTx *sendPersistenceTx
	tests := []struct{ name string }{{"nil adapter"}, {"zero adapter"}, {"nil context"}, {"cancelled context"}, {"nil tx"}, {"typed nil tx"}, {"zero organization"}}
	for _, operation := range sendPersistenceOperations() {
		for _, tt := range tests {
			t.Run(operation.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "create a strict reader with no allowed SQL")
				t.Cleanup(mock.Close)
				p, fixture := newSQLSendPersistence(mock), newSendPersistenceFixture()
				ctx := context.Background()
				var tx pgx.Tx = &sendPersistenceTx{reader: mock}
				switch tt.name {
				case "nil adapter":
					p = nil
				case "zero adapter":
					p = &sqlLegacySendPersistence{}
				case "nil context":
					ctx = nil
				case "cancelled context":
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				case "nil tx":
					tx = nil
				case "typed nil tx":
					tx = nilTx
				case "zero organization":
					fixture.org = uuid.Nil
				}
				require.Error(t, operation.call(ctx, p, tx, fixture), "invalid boundary values must fail without database or ownership calls")
				require.NoError(t, mock.ExpectationsWereMet(), "boundary rejection must occur before any SQL")
			})
		}
	}
}

func TestLegacySendPersistenceRejectsMalformedMessage(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{{"nil"}, {"foreign organization"}, {"zero session"}, {"zero pointed thread"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSendPersistenceFixture()
			message := fixture.message("")
			switch tt.name {
			case "nil":
				message = nil
			case "foreign organization":
				message.OrgID = uuid.New()
			case "zero session":
				message.SessionID = uuid.Nil
			case "zero pointed thread":
				message.ThreadID = new(uuid.UUID)
			}
			p := newSQLSendPersistence(nil)
			err := p.CreateMessageInTx(context.Background(), &sendPersistenceTx{}, fixture.org, message)
			require.EqualError(t, err, "thread send: message must have the exact organization and nonzero scope", "message shape must reject before any transaction access")
		})
	}
}

func TestLegacySendPersistenceRejectsZeroScopeBeforeSQL(t *testing.T) {
	t.Parallel()
	for _, operation := range sendPersistenceOperations() {
		if operation.name == "enqueue" {
			continue // The job's immutable payload, not this shape adapter, owns its scope.
		}
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSendPersistenceFixture()
			fixture.session, fixture.thread = uuid.Nil, uuid.Nil
			err := operation.call(context.Background(), newSQLSendPersistence(nil), &sendPersistenceTx{}, fixture)
			require.Error(t, err, "zero explicit mutation scope must reject before transaction access")
		})
	}
}

func TestLegacySendPersistenceClaimDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, status string
		resume, want bool
	}{
		{"idle at limit", "idle", false, true}, {"idle below limit", "idle", false, false},
		{"resume at limit", "completed", true, true}, {"resume wrong status", "running", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated post-unit diagnostic reader")
			t.Cleanup(mock.Close)
			fixture := newSendPersistenceFixture()
			count := 2
			if tt.name == "idle below limit" {
				count = 1
			}
			mock.ExpectQuery(`SELECT[\s\S]*COALESCE[\s\S]*org_id = @org_id[\s\S]*sibling_active`).
				WithArgs(sendPersistenceAnyArgs(3)...).
				WillReturnRows(pgxmock.NewRows([]string{"target_status", "sibling_active"}).AddRow(tt.status, count))
			p := newSQLSendPersistence(mock)
			var actual bool
			if tt.resume {
				actual, err = p.IsAtResumeClaimLimit(context.Background(), fixture.org, fixture.session, fixture.thread, 2)
			} else {
				actual, err = p.IsAtIdleClaimLimit(context.Background(), fixture.org, fixture.session, fixture.thread, 2)
			}
			require.NoError(t, err, "diagnostics must use only the configured pool-backed reader")
			require.Equal(t, tt.want, actual, "diagnostic status and cap policy must match the original fixed claim kind")
			require.NoError(t, mock.ExpectationsWereMet(), "diagnostics must not acquire a transaction or invoke a mutation")
		})
	}
}

func sendPersistenceThreadRows(f sendPersistenceFixture, at time.Time, pending int) *pgxmock.Rows {
	return pgxmock.NewRows(strings.Fields(`id session_id org_id agent_type model_override reasoning_effort
		label instructions file_scope status agent_session_id current_turn last_activity_at result_summary diff
		failure_explanation failure_category started_at completed_at created_at created_by_source created_by_thread_id
		archived_at base_snapshot_key cost_cents pending_message_count cancel_requested_at runtime_stop_reason
		runtime_graceful_stop_at recovery_state recovery_reason recovery_event_history execution_mode filesystem_mode`)).AddRow(
		f.thread, f.session, f.org, models.AgentTypeClaudeCode, nil, nil, "synthetic", nil, nil, models.ThreadStatusRunning,
		nil, 2, nil, nil, nil, nil, nil, nil, nil, at, models.ThreadCreatedBySourceUser, nil, nil, nil, float64(0), pending,
		nil, "", nil, "", "", []byte("[]"), models.ThreadExecutionModeWork, models.ThreadFilesystemModeReadWrite,
	)
}

func TestLegacySendPersistenceOpaqueEffectsAndOwnerPublication(t *testing.T) {
	t.Parallel()
	tests := []struct{ outcome string }{{"commit"}, {"rollback"}, {"unknown commit"}}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the supplied transaction reader")
			t.Cleanup(mock.Close)
			redis := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + redis.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "enable real isolated publishers so premature effects cannot be concealed")
			t.Cleanup(func() { require.NoError(t, client.Close(), "close the isolated publisher client") })
			p := newSQLSendPersistence(mock)
			streams := cache.NewSessionStreams(client, zerolog.Nop(), nil)
			p.threads.SetStreams(streams)
			p.sessions.SetStreams(streams)
			effects := &sqlSendEffects{sessions: p.sessions, threads: p.threads, jobs: p.jobs}
			fixture, at := newSendPersistenceFixture(), time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			tx := &sendPersistenceTx{reader: mock}
			mock.ExpectQuery("WITH locked_threads").WithArgs(sendPersistenceAnyArgs(5)...).WillReturnRows(sendPersistenceThreadRows(fixture, at, 0))
			claimed, claimEffects, err := p.ClaimIdleThreadInTx(context.Background(), tx, fixture.org, fixture.session, fixture.thread, 3)
			require.NoError(t, err, "claim must return the original row and opaque notification token")
			require.NotEqual(t, db.ThreadClaimPersistenceEffects{}, claimEffects, "real persistence must produce a nonzero opaque claim token")
			mock.ExpectExec("UPDATE session_threads").WithArgs(sendPersistenceAnyArgs(2)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			pendingEffects, err := p.IncrementPendingMessagesInTx(context.Background(), tx, fixture.org, fixture.thread)
			require.NoError(t, err, "pending write must return its own opaque reload token")
			require.NotEqual(t, db.ThreadPendingMessagePersistenceEffects{}, pendingEffects, "pending effects must come from the actual persistence helper")
			mock.ExpectExec("UPDATE session_threads").WithArgs(sendPersistenceAnyArgs(3)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			statusEffects, err := p.UpdateThreadStatusInTx(context.Background(), tx, fixture.org, fixture.thread, models.ThreadStatusIdle)
			require.NoError(t, err, "status write must return its own opaque reload token")
			require.NotEqual(t, db.ThreadPersistenceEffects{}, statusEffects, "status effects must come from the actual persistence helper")
			require.Empty(t, tx.settled, "adapters must leave all transaction ownership with the caller")
			require.Empty(t, redis.Keys(), "no configured publisher may run before explicit owner publication")
			if tt.outcome != "commit" {
				// The owner discards every token on rollback or unknown commit;
				// none of these tokens can independently establish commit.
				if tt.outcome == "rollback" {
					require.NoError(t, tx.Rollback(context.Background()), "the owner alone rolls back the unit")
					require.Equal(t, []string{"rollback"}, tx.settled, "discarded effects must not settle another transaction")
				} else {
					tx.commitErr = errors.New("synthetic unknown commit")
					require.ErrorIs(t, tx.Commit(context.Background()), tx.commitErr, "an unknown commit must leave effects unusable")
					require.Equal(t, []string{"commit"}, tx.settled, "unknown commit must not retry or begin another unit")
				}
				require.Empty(t, redis.Keys(), "rollback and unknown commit must publish no discarded effects")
				require.NoError(t, mock.ExpectationsWereMet(), "discarding tokens must not reload or publish")
				return
			}
			require.NoError(t, tx.Commit(context.Background()), "only the transaction owner confirms commit")
			effects.PublishThreadClaim(context.Background(), fixture.org, claimEffects)
			for _, publish := range []func(){
				func() { effects.PublishThreadPending(context.Background(), fixture.org, pendingEffects) },
				func() { effects.PublishThreadStatus(context.Background(), fixture.org, statusEffects) },
			} {
				mock.ExpectQuery("SELECT").WithArgs(fixture.thread, fixture.org).WillReturnRows(sendPersistenceThreadRows(fixture, at, 7))
				publish()
			}
			parent := models.Session{ID: fixture.session, OrgID: fixture.org, Status: models.SessionStatusRunning}
			effects.PublishSessionStatus(context.Background(), fixture.org, parent)
			entries, err := redis.Stream("143:stream:{ses:" + fixture.session.String() + "}:events")
			require.NoError(t, err, "explicit publication must produce the three established thread events")
			var actual []models.SessionStreamEvent
			for _, entry := range entries {
				var event models.SessionStreamEvent
				require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode each exact forwarded legacy event")
				actual = append(actual, event)
			}
			later := claimed
			later.PendingMessageCount = 7
			wantData := []any{models.NewThreadRuntimeEvent(claimed), models.NewThreadInboxEvent(later), models.NewThreadRuntimeEvent(later)}
			wantTypes := []models.SessionStreamEventType{models.SessionStreamEventThreadRuntimeUpdated, models.SessionStreamEventThreadInboxQueued, models.SessionStreamEventThreadRuntimeUpdated}
			var expected []models.SessionStreamEvent
			for i, data := range wantData {
				encoded, marshalErr := json.Marshal(models.SessionStreamEvent{Type: wantTypes[i], SessionID: fixture.session, OrgID: fixture.org, Data: data})
				require.NoError(t, marshalErr, "encode the complete expected event payload")
				var event models.SessionStreamEvent
				require.NoError(t, json.Unmarshal(encoded, &event), "normalize expected generic event data identically")
				expected = append(expected, event)
			}
			require.Equal(t, expected, actual, "opaque tokens must retain claim snapshots but reload current pending/status state")
			parentEntries, err := redis.Stream("143:stream:{ses:" + fixture.session.String() + "}:status")
			require.NoError(t, err, "parent publication must use the original status stream")
			var parentPayload models.Session
			require.NoError(t, json.Unmarshal([]byte(parentEntries[0].Values[1]), &parentPayload), "decode the exact published parent snapshot")
			require.Equal(t, parent, parentPayload, "parent publisher must forward the supplied snapshot without a by-ID reload")
			require.Equal(t, []string{"commit"}, tx.settled, "effects must not settle the transaction again")
			require.NoError(t, mock.ExpectationsWereMet(), "only pending/status effects may perform postcommit pool reloads")
		})
	}
}

func TestLegacySendPersistenceMessageInsertReturnsOriginalValues(t *testing.T) {
	t.Parallel()
	tests := []struct{ source models.SessionMessageSource }{{""}, {models.SessionMessageSourceAgentTool}}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("source=%s", tt.source), func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated message insert reader")
			t.Cleanup(mock.Close)
			fixture, at := newSendPersistenceFixture(), time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			message := fixture.message(tt.source)
			expected := *message
			expected.ID, expected.CreatedAt = 42, at
			argCount := 11
			if tt.source != "" {
				argCount++
			}
			mock.ExpectQuery("INSERT INTO session_messages").WithArgs(sendPersistenceAnyArgs(argCount)...).WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(42), at))
			err = newSQLSendPersistence(nil).CreateMessageInTx(context.Background(), &sendPersistenceTx{reader: mock}, fixture.org, message)
			require.NoError(t, err, "both legacy insert variants must use the caller's transaction")
			require.Equal(t, expected, *message, "the adapter must preserve source and all input fields while returning the store's ID/time")
			require.NoError(t, mock.ExpectationsWereMet(), "message insertion must not use a pool fallback")
		})
	}
}

func TestLegacySendPersistenceEnqueueRequiresExplicitNotify(t *testing.T) {
	t.Parallel()
	tests := []struct{ outcome string }{{"inserted"}, {"dedupe collision"}, {"query error"}}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated enqueue reader")
			t.Cleanup(mock.Close)
			server := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "enable the real job notifier")
			t.Cleanup(func() { require.NoError(t, client.Close(), "close the isolated job notifier") })
			p := newSQLSendPersistence(nil)
			p.jobs.SetNotifier(cache.NewJobNotifier(client, zerolog.Nop()))
			effects := &sqlSendEffects{sessions: p.sessions, threads: p.threads, jobs: p.jobs}
			fixture, expectedID, failure := newSendPersistenceFixture(), uuid.New(), errors.New("synthetic enqueue failure")
			query := mock.ExpectQuery("INSERT INTO jobs").WithArgs(sendPersistenceAnyArgs(7)...)
			switch tt.outcome {
			case "inserted":
				query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(expectedID))
			case "dedupe collision":
				expectedID = uuid.Nil
				query.WillReturnRows(pgxmock.NewRows([]string{"id"}))
			case "query error":
				expectedID = uuid.Nil
				query.WillReturnError(failure)
			}
			tx, before := &sendPersistenceTx{reader: mock}, server.CommandCount()
			actualID, err := p.EnqueueInTx(context.Background(), tx, fixture.org, fixture.enqueue())
			require.Equal(t, expectedID, actualID, "legacy nil-ID collision semantics must not be converted into consumer authority")
			require.Equal(t, before, server.CommandCount(), "enqueue persistence must not notify before its owner confirms commit")
			require.Empty(t, tx.settled, "enqueue must not settle its supplied transaction")
			if tt.outcome == "query error" {
				require.ErrorIs(t, err, failure, "original enqueue errors must reach the owner without notification")
			} else {
				require.NoError(t, err, "insertion and absorbed dedupe collision retain the legacy success envelope")
				require.NoError(t, tx.Commit(context.Background()), "the owner alone confirms this unit's commit")
				effects.NotifyJob(context.Background(), actualID)
				expectedCommands := before
				if actualID != uuid.Nil {
					expectedCommands++
				}
				require.Equal(t, expectedCommands, server.CommandCount(), "explicit postcommit notification must preserve the nil-ID no-wake behavior")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "enqueue may use only the supplied transaction")
		})
	}
}

func sendPersistenceAnyArgs(count int) []any {
	args := make([]any, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}
