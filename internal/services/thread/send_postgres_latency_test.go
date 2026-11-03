package thread

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// latencySendConn puts delay on both directions of the real PostgreSQL wire,
// including cold statement preparation. Sleeping once per SQL call would miss
// the extra protocol round trips that this regression exercises.
type latencySendConn struct {
	net.Conn
	delay time.Duration
}

type sendLatencyTrace struct{ t *testing.T }
type sendLatencyTraceKey struct{}
type sendLatencyQuery struct {
	sql   string
	start time.Time
}

func (tr sendLatencyTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, sendLatencyTraceKey{}, sendLatencyQuery{sql: strings.Join(strings.Fields(data.SQL), " "), start: time.Now()})
}
func (tr sendLatencyTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	q := ctx.Value(sendLatencyTraceKey{}).(sendLatencyQuery)
	tr.t.Logf("query %.0fms %.100s err=%v", float64(time.Since(q.start))/float64(time.Millisecond), q.sql, data.Err)
}

func (c *latencySendConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		time.Sleep(c.delay)
	}
	return n, err
}

func (c *latencySendConn) Write(p []byte) (int, error) {
	time.Sleep(c.delay)
	return c.Conn.Write(p)
}

func TestThreadSendLegacyRemoteDatabasePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		delay    time.Duration
		status   string
		key      string
		answer   bool
		capacity bool
	}{
		{name: "local"},
		{name: "remote capacity queue", delay: 45 * time.Millisecond, capacity: true},
		{name: "remote cold statements", delay: 45 * time.Millisecond},
		{name: "remote keyed send", delay: 45 * time.Millisecond, key: "synthetic-key"},
		{name: "remote resume", delay: 45 * time.Millisecond, status: "completed"},
		{name: "remote keyed resume", delay: 45 * time.Millisecond, status: "completed", key: "synthetic-key"},
		{name: "remote awaiting input", delay: 45 * time.Millisecond, status: "awaiting_input"},
		{name: "remote answering input", delay: 45 * time.Millisecond, status: "awaiting_input", answer: true},
		{name: "remote queue", delay: 45 * time.Millisecond, status: "running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			laneCount := 1
			if tt.capacity {
				laneCount = models.MaxRunningThreadsPerSession + 1
			}
			f := newSendPostgresFixture(t, false, laneCount)
			if tt.capacity {
				sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2 AND id<>$3`, f.org, f.session, f.lanes[0])
			}
			if tt.answer {
				question := models.SessionQuestion{OrgID: f.org, SessionID: f.session, QuestionText: "Synthetic question", Status: models.SessionQuestionStatusPending, Options: []string{}}
				require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					return db.NewSessionQuestionStore(tx).Create(ctx, &question)
				}), "seed a real pending question")
				sendPostgresExec(t, f, `INSERT INTO session_human_input_requests(org_id,session_id,thread_id,agent_type,request_kind,title,body) VALUES($1,$2,$3,'codex','free_text','Synthetic question','Synthetic body')`, f.org, f.session, f.lanes[0])
			}
			if tt.status != "" {
				sendPostgresExec(t, f, `UPDATE sessions SET status=$3 WHERE org_id=$1 AND id=$2`, f.org, f.session, tt.status)
				sendPostgresExec(t, f, `UPDATE session_threads SET status=$3 WHERE org_id=$1 AND session_id=$2`, f.org, f.session, tt.status)
			}
			config := f.pool.Config()
			config.ConnConfig.Tracer = sendLatencyTrace{t: t}
			config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return &latencySendConn{Conn: conn, delay: tt.delay}, nil
			}
			pool, err := pgxpool.NewWithConfig(context.Background(), config)
			require.NoError(t, err, "create isolated pool with simulated network delay")
			t.Cleanup(pool.Close)

			f.pool = pool
			service, _ := f.service(t, true)
			if tt.answer {
				service.SetQuestionStore(db.NewSessionQuestionStore(pool))
				service.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(pool))
			}
			result, err := service.SendMessage(context.Background(), f.input(0, tt.key))
			if err != nil {
				t.Logf("send cause: %v", errors.Unwrap(err))
			}
			require.NoError(t, err, "an ordinary legacy send must finish against a remote database")
			if tt.answer {
				require.NotNil(t, result.AnsweredQuestion, "question answer must commit")
				require.NotNil(t, result.AnsweredHumanInput, "human input answer must commit")
			}
			expected := sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}
			if tt.status == "running" {
				expected.continueJobs = 0
				expected.liveJobs = 1
				expected.pending = 1
			}
			if tt.capacity {
				expected.continueJobs = 0
				expected.pending = 1
				expected.threadStatus = models.ThreadStatusIdle
			}
			require.Equal(t, expected, sendPostgresReadGraph(t, f), "the bounded send must commit its complete message, inbox, job and claim graph")
		})
	}
}

// Inject a real wire deadline after the first provisional message write, while
// retaining the actual production transaction runner and all send guards.
type deadlineSendPersistence struct{ LegacySendPersistence }

func (p deadlineSendPersistence) CreateMessageInTx(ctx context.Context, tx pgx.Tx, org uuid.UUID, message *models.SessionMessage) error {
	if err := p.LegacySendPersistence.CreateMessageInTx(ctx, tx, org, message); err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err := tx.Exec(queryCtx, "SELECT pg_sleep(1)")
	return err
}
func TestThreadSendPrecommitDeadlinePostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, false, 1)
	service, effects := f.service(t, true)
	service.send.Legacy = deadlineSendPersistence{LegacySendPersistence: service.send.Legacy}
	result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-deadline"))
	require.ErrorIs(t, err, context.DeadlineExceeded, "precommit cancellation must retain the actual cause")
	require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "closed connection before COMMIT is a known abort")
	require.Nil(t, result, "an aborted send cannot return provisional acceptance")
	require.Equal(t, sendPostgresGraph{parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}, sendPostgresReadGraph(t, f), "claims, message, inbox and job must all roll back")
	require.Empty(t, effects.jobs, "an aborted send must not notify workers")
	require.Zero(t, effects.claims, "an aborted send must not publish a provisional claim")
	require.Zero(t, effects.pending, "an aborted send must not publish pending counts")
}
