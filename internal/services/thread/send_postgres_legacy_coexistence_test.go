package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These are actual common store entrypoints, source-compared with A2
// 53a26ab654db2462439a1380da035e558610580a. They do not execute an old
// binary, an old service graph, a provider, or a mixed-build deployment.
// Logs deliberately have no parent/lane FK after migration 211. Messages and
// runtimes retain FKs; StartPhase also explicitly locks its lane. The source
// comparison and broader H limitations belong in private validation evidence.
type legacyCoexistencePause struct {
	ctx context.Context
	tx  pgx.Tx
	pid uint32
}

type legacyCoexistencePersistence struct {
	LegacySendPersistence
	ready   chan<- legacyCoexistencePause
	release <-chan struct{}
}

func (p *legacyCoexistencePersistence) ClaimIdleThreadInTx(ctx context.Context, tx pgx.Tx, org, session, lane uuid.UUID, max int) (models.SessionThread, db.ThreadClaimPersistenceEffects, error) {
	row, effect, err := p.LegacySendPersistence.ClaimIdleThreadInTx(ctx, tx, org, session, lane, max)
	// Include actual collection misses: the original claim CTE has completed
	// on resume, mid-turn and capacity paths before this barrier is exposed.
	select {
	case p.ready <- legacyCoexistencePause{ctx: ctx, tx: tx, pid: tx.Conn().PgConn().PID()}:
	case <-ctx.Done():
		return row, effect, ctx.Err()
	}
	if waitErr := waitSendPostgresBarrier(ctx, p.release); waitErr != nil {
		return row, effect, waitErr
	}
	return row, effect, err
}

func legacyCoexistenceFixture(t *testing.T, branch string) sendPostgresFixture {
	t.Helper()
	lanes := 2
	if branch == "capacity" {
		lanes = 4
	}
	f := newSendPostgresFixture(t, false, lanes)
	sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
	sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2 AND id=ANY($3::uuid[])`, f.org, f.session, f.lanes[1:])
	status := models.ThreadStatusIdle
	if branch == "resume" {
		status = models.ThreadStatusCompleted
	} else if branch == "midturn" {
		status = models.ThreadStatusRunning
	}
	sendPostgresExec(t, f, `UPDATE session_threads SET status=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.lanes[0], status)
	return f
}

func legacyCoexistenceSendGraph(branch string) sendPostgresGraph {
	want := sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}
	if branch == "midturn" {
		want.continueJobs, want.liveJobs, want.pending = 0, 1, 1
	} else if branch == "capacity" {
		want.continueJobs, want.pending, want.threadStatus = 0, 1, models.ThreadStatusIdle
	}
	return want
}

type legacyCoexistencePeerResult struct {
	table    string
	id       any
	snapshot json.RawMessage
	err      error
	elapsed  time.Duration
}

func readLegacyCoexistencePeer(ctx context.Context, q db.DBTX, f sendPostgresFixture, table string, id any) (json.RawMessage, error) {
	var row json.RawMessage
	queries := map[string]string{
		"session_logs":            `SELECT to_jsonb(r) FROM session_logs r WHERE org_id=$1 AND session_id=$2 AND id=$3`,
		"session_messages":        `SELECT to_jsonb(r) FROM session_messages r WHERE org_id=$1 AND session_id=$2 AND id=$3`,
		"session_activity_phases": `SELECT to_jsonb(r) FROM session_activity_phases r WHERE org_id=$1 AND session_id=$2 AND id=$3`,
		"thread_runtimes":         `SELECT to_jsonb(r) FROM thread_runtimes r WHERE org_id=$1 AND session_id=$2 AND id=$3`,
		"session_threads":         `SELECT to_jsonb(r) FROM session_threads r WHERE org_id=$1 AND session_id=$2 AND id=$3`,
		"sessions":                `SELECT to_jsonb(r) FROM sessions r WHERE org_id=$1 AND id=$2`,
	}
	query, ok := queries[table]
	if !ok {
		return nil, fmt.Errorf("unrecognized synthetic peer table %q", table)
	}
	args := []any{f.org, f.session, id}
	if table == "sessions" {
		if id != f.session {
			return nil, fmt.Errorf("synthetic peer parent identity mismatch")
		}
		args = args[:2]
	}
	err := q.QueryRow(ctx, query, args...).Scan(&row)
	return row, err
}

func runLegacyCoexistencePeer(ctx context.Context, conn *pgxpool.Conn, f sendPostgresFixture, kind string) legacyCoexistencePeerResult {
	started := time.Now()
	lane := f.lanes[1]
	result := legacyCoexistencePeerResult{}
	switch kind {
	case "log":
		row := models.SessionLog{OrgID: f.org, SessionID: f.session, ThreadID: &lane, Level: "info", Message: "Synthetic sibling progress log", TurnNumber: 1}
		result.err = db.NewSessionLogStore(conn).Create(ctx, &row)
		result.table, result.id = "session_logs", row.ID
	case "message":
		row := models.SessionMessage{OrgID: f.org, SessionID: f.session, ThreadID: &lane, Role: models.MessageRoleAssistant, Content: "Synthetic sibling assistant message", TurnNumber: 1}
		result.err = db.NewSessionMessageStore(conn).Create(ctx, &row)
		result.table, result.id = "session_messages", row.ID
	case "phase":
		row, err := db.NewSessionActivityPhaseStore(conn).StartPhase(ctx, f.org, f.session, lane, 1, nil, nil, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
		result.err, result.table, result.id = err, "session_activity_phases", row.ID
	case "runtime":
		row, err := db.NewThreadRuntimeStore(conn).CreateStarting(ctx, f.org, db.CreateThreadRuntimeParams{SessionID: f.session, ThreadID: lane, SandboxID: uuid.New(), ContainerID: "synthetic-sibling-container", RuntimeHandleID: "synthetic-sibling-runtime", AgentType: models.DefaultDefaultAgentType, OwnerNodeID: "synthetic-sibling-worker", LeaseToken: uuid.New(), LeaseDuration: time.Minute})
		result.err, result.table, result.id = err, "thread_runtimes", row.ID
	case "thread completion":
		summary := "Synthetic sibling turn completed"
		result.err = db.NewSessionThreadStore(conn).UpdateTurnComplete(ctx, f.org, lane, 1, &models.SessionResult{ResultSummary: &summary}, "synthetic-sibling-conversation")
		result.table, result.id = "session_threads", lane
	case "parent completion":
		summary := "Synthetic parent turn completed"
		result.err = db.NewSessionStore(conn).UpdateTurnComplete(ctx, f.org, f.session, 1, &models.SessionResult{ResultSummary: &summary}, "synthetic-parent-conversation", "synthetic-parent-snapshot")
		result.table, result.id = "sessions", f.session
	default:
		result.err = fmt.Errorf("unrecognized synthetic peer kind %q", kind)
	}
	if result.err == nil {
		result.snapshot, result.err = readLegacyCoexistencePeer(ctx, conn, f, result.table, result.id)
	}
	result.elapsed = time.Since(started)
	return result
}

func requireLegacyCoexistenceWait(t *testing.T, owner legacyCoexistencePause, peerPID uint32) string {
	t.Helper()
	for {
		require.NoError(t, owner.ctx.Err(), "actual sibling lock wait must be positively observed within the unchanged send unit budget")
		_, err := owner.tx.Exec(owner.ctx, `SELECT pg_stat_clear_snapshot()`)
		require.NoError(t, err, "refresh only the current transaction's statistics snapshot while the service is paused")
		var eventType, event *string
		var blockers []int32
		err = owner.tx.QueryRow(owner.ctx, `SELECT wait_event_type,wait_event,pg_blocking_pids(pid) FROM pg_stat_activity WHERE pid=$1`, int32(peerPID)).Scan(&eventType, &event, &blockers)
		require.NoError(t, err, "observe the actual competing backend through the command's existing connection")
		if eventType != nil && *eventType == "Lock" {
			require.Contains(t, blockers, int32(owner.pid), "the observed sibling wait must be blocked by this actual whole-send owner")
			require.NotNil(t, event, "a real lock wait must expose its PostgreSQL wait event")
			return *event
		}
		// Bounded SQL round trips and scheduler yields replace timing sleeps.
		runtime.Gosched()
	}
}

func TestThreadSendLegacyCommonStoreSiblingProgressPostgres(t *testing.T) {
	t.Parallel()
	branches := []string{"idle", "resume", "midturn", "capacity"}
	writers := []struct {
		kind string
		wait bool
	}{
		{kind: "log"},
		{kind: "message", wait: true},
		{kind: "phase", wait: true},
		{kind: "runtime", wait: true},
		{kind: "thread completion", wait: true},
		{kind: "parent completion", wait: true},
	}
	for _, branch := range branches {
		for _, writer := range writers {
			t.Run(branch+"/"+writer.kind, func(t *testing.T) {
				t.Parallel()
				f := legacyCoexistenceFixture(t, branch)
				service, _ := f.service(t, true)
				ctx, cancel := context.WithTimeout(context.Background(), 3*db.LifecycleTransactionTimeout)
				defer cancel()
				peer, err := f.pool.Acquire(ctx)
				require.NoError(t, err, "reserve one actual common-store connection within the unchanged two-connection capacity")
				defer peer.Release()
				ready, release := make(chan legacyCoexistencePause, 1), make(chan struct{})
				var workers sync.WaitGroup
				defer func() {
					cancel()
					select {
					case <-release:
					default:
						close(release)
					}
					workers.Wait()
				}()
				service.send.Legacy = &legacyCoexistencePersistence{LegacySendPersistence: service.send.Legacy, ready: ready, release: release}
				runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f}
				service.send.Transactions = runner
				type outcome struct {
					result  *SendMessageResult
					err     error
					elapsed time.Duration
				}
				sendDone := make(chan outcome, 1)
				workers.Add(1)
				go func() {
					defer workers.Done()
					started := time.Now()
					result, err := service.SendMessage(ctx, f.input(0, "synthetic-common-store-coexistence"))
					sendDone <- outcome{result, err, time.Since(started)}
				}()
				var owner legacyCoexistencePause
				select {
				case owner = <-ready:
				case ended := <-sendDone:
					t.Fatalf("actual send ended before its post-claim coexistence barrier: %v", ended.err)
				case <-ctx.Done():
					t.Fatal("actual whole-send claim barrier did not arrive within the bounded test context")
				}
				peerDone := make(chan legacyCoexistencePeerResult, 1)
				peerCtx, peerCancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
				defer peerCancel()
				workers.Add(1)
				go func() { defer workers.Done(); peerDone <- runLegacyCoexistencePeer(peerCtx, peer, f, writer.kind) }()
				var peerResult legacyCoexistencePeerResult
				waitEvent := "none"
				if writer.wait {
					waitEvent = requireLegacyCoexistenceWait(t, owner, peer.Conn().PgConn().PID())
				} else {
					select {
					case peerResult = <-peerDone:
						require.NoError(t, peerResult.err, "FK-free logging must make actual progress while the send remains paused holding its locks")
					case <-owner.ctx.Done():
						t.Fatal("FK-free logging failed to complete before releasing the whole-send barrier")
					}
				}
				close(release)
				var sent outcome
				select {
				case sent = <-sendDone:
				case <-ctx.Done():
					t.Fatal("whole send failed bounded progress after the observed sibling wait was released")
				}
				if writer.wait {
					select {
					case peerResult = <-peerDone:
					case <-ctx.Done():
						t.Fatal("actual sibling store failed bounded progress after whole-send commit")
					}
				}
				require.NoError(t, sent.err, "the exact send-first schedule must commit without deadlock, contention exhaustion, or retry")
				require.NotNil(t, sent.result.Message, "the real sender must expose its durable whole-command input")
				require.NoError(t, peerResult.err, "the actual sibling store must complete after any observed lock wait")
				requireSendPostgresLegacyUnitGuards(t, runner.units)
				require.Equal(t, legacyCoexistenceSendGraph(branch), runner.units[1].provisional, "the supplied command must contain the entire correct send graph before any waiting peer can proceed")
				actual, err := readLegacyCoexistencePeer(ctx, f.pool, f, peerResult.table, peerResult.id)
				require.NoError(t, err, "independently read the exact row committed by the common store")
				require.JSONEq(t, string(peerResult.snapshot), string(actual), "common-store progress must retain every committed peer row field")
				require.Equal(t, int32(2), f.pool.Config().MaxConns, "coexistence proof may not increase connection capacity")
				t.Logf("synthetic coexistence branch=%s writer=%s wait_event=%s send_units=2 send_attempts=%d send_elapsed=%s peer_elapsed=%s observed_sqlstate=none no_returned_deadlock=true", branch, writer.kind, waitEvent, runner.units[1].attempts, sent.elapsed, peerResult.elapsed)
			})
		}
	}
}

func TestThreadSendLegacyCommonStorePeerFirstContentionPostgres(t *testing.T) {
	t.Parallel()
	branches := []string{"idle", "resume", "midturn", "capacity"}
	tests := []struct {
		name            string
		parent, exhaust bool
	}{
		{name: "parent transient", parent: true},
		{name: "sibling transient"},
		{name: "parent sustained", parent: true, exhaust: true},
		{name: "sibling sustained", exhaust: true},
	}
	for _, branch := range branches {
		for _, tt := range tests {
			t.Run(branch+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				f := legacyCoexistenceFixture(t, branch)
				service, effects := f.service(t, true)
				baseline := sendPostgresReadSnapshot(t, f)
				ctx, cancel := context.WithTimeout(context.Background(), 3*db.LifecycleTransactionTimeout)
				defer cancel()
				peer, err := f.pool.Acquire(ctx)
				require.NoError(t, err, "reserve one independent peer within the existing two-connection fixture")
				defer peer.Release()
				held, err := peer.Begin(ctx)
				require.NoError(t, err, "start an explicit adversarial peer store transaction, not an alleged historical service transaction")
				defer func() {
					if held != nil {
						_ = held.Rollback(context.Background())
					}
				}()
				if tt.parent {
					err = db.NewSessionStore(held).UpdateStatus(ctx, f.org, f.session, models.SessionStatusRunning)
				} else {
					err = db.NewSessionThreadStore(held).UpdateStatus(ctx, f.org, f.lanes[1], models.ThreadStatusRunning)
				}
				require.NoError(t, err, "the peer must execute the actual common-store UPDATE and retain its real row lock")
				runner := &legacyContentionTransactions{base: service.send.Transactions, fixture: f}
				runner.beforeAttempt = func(unit, attempt int) error {
					if unit == 2 && attempt == 2 && !tt.exhaust {
						if err := held.Commit(ctx); err != nil {
							return err
						}
						held = nil
					}
					return nil
				}
				service.send.Transactions = runner
				started := time.Now()
				result, err := service.SendMessage(ctx, f.input(0, "synthetic-peer-first-common-store"))
				elapsed := time.Since(started)
				require.Equal(t, 2, len(runner.units), "peer-first contention may retry only the whole command after the single entry observation")
				attempts := runner.units[1].attempts
				wantAttempts := 2
				if tt.exhaust {
					wantAttempts = 3
				}
				require.Equal(t, wantAttempts, len(attempts), "actual common-store locks must produce the unchanged bounded whole-command retry count")
				if tt.exhaust {
					var contention *db.LifecycleContentionError
					require.ErrorAs(t, err, &contention, "sustained common-store conflict must report typed confirmed-aborted contention for the handler's 409 mapping")
					require.Equal(t, 3, contention.Attempts, "sustained peer retains the original production retry budget")
					require.Nil(t, result, "exhausted contention cannot return accepted input")
					require.NoError(t, held.Rollback(ctx), "release the peer only after confirmed bounded send exhaustion")
					held = nil
					require.JSONEq(t, string(baseline), string(sendPostgresReadSnapshot(t, f)), "whole send and rolled-back peer must leave every original row unchanged")
					requireSendPostgresNoEffects(t, effects)
				} else {
					require.NoError(t, err, "transient common-store conflict must make progress after its actual peer commit")
					require.NotNil(t, result.Message, "successful retry must return the final committed input")
					require.Equal(t, legacyCoexistenceSendGraph(branch), sendPostgresReadGraph(t, f), "peer commit and whole-send retry must preserve the selected branch and commit exactly one complete graph")
				}
				diagnostic := runner.units[0].attempts[0].diagnostic
				for i, attempt := range attempts {
					require.Equal(t, diagnostic, attempt.diagnostic, "real common-store contention cannot remint command correlation")
					if tt.exhaust || i == 0 {
						var pg *pgconn.PgError
						require.ErrorAs(t, attempt.err, &pg, "failed attempts must retain the actual NOWAIT PostgreSQL error")
						require.Equal(t, "55P03", pg.Code, "actual guard conflict is bounded contention, not a fabricated marker or hidden deadlock")
					} else {
						require.NoError(t, attempt.err, "final attempt must complete before actual commit")
					}
				}
				t.Logf("synthetic peer-first branch=%s parent=%t sustained=%t attempts=%d send_elapsed=%s observed_sqlstate=55P03 no_returned_deadlock=true hosted_http_unverified=true", branch, tt.parent, tt.exhaust, len(attempts), elapsed)
			})
		}
	}
}
