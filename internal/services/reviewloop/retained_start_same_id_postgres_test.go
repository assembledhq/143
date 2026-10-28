package reviewloop

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type retainedSameIDHeldT1 struct {
	pid   int32
	since time.Time
}

// The gate wraps the genuine bounded transaction callback after its actual T1
// writes, while its locks and provisional graph are still owned. It changes
// only the acknowledgement (commit case) or callback result (rollback case).
type retainedSameIDT1Gate struct {
	base    threadsvc.SendTransactions
	commit  bool
	ready   chan<- retainedSameIDHeldT1
	release <-chan struct{}
	fired   atomic.Bool
}

func (r *retainedSameIDT1Gate) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	selected := false
	err := r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		capture := &retainedStartTestTx{Tx: tx}
		if err := work(unitCtx, capture); err != nil {
			return err
		}
		if capture.phase != "T1" || !r.fired.CompareAndSwap(false, true) {
			return nil
		}
		selected = true
		var pid int32
		if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		select {
		case r.ready <- retainedSameIDHeldT1{pid: pid, since: time.Now()}:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		select {
		case <-r.release:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		if !r.commit {
			return errRetainedStartAck
		}
		return nil
	})
	if selected && r.commit && err == nil {
		return errRetainedStartAck
	}
	return err
}

type retainedSameIDPIDRunner struct {
	base threadsvc.SendTransactions
	pid  chan<- int32
	once sync.Once
}

func (r *retainedSameIDPIDRunner) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		var pid int32
		if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		r.once.Do(func() { r.pid <- pid })
		return work(unitCtx, tx)
	})
}

type retainedSameIDRuntime struct {
	RuntimeAdapter
	reads, creates, sends atomic.Int32
}

func (r *retainedSameIDRuntime) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	r.reads.Add(1)
	return r.RuntimeAdapter.GetSession(ctx, orgID, sessionID)
}

func (r *retainedSameIDRuntime) CreateThread(ctx context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	r.creates.Add(1)
	return r.RuntimeAdapter.CreateThread(ctx, input)
}

func (r *retainedSameIDRuntime) SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	r.sends.Add(1)
	return r.RuntimeAdapter.SendMessage(ctx, input)
}

func TestRetainedSameIDRecoveryWaitsForT1Postgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		commit bool
	}{
		{name: "adopts original after held T1 commits", commit: true},
		{name: "completes original after held T1 rolls back"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			// Three literal checkouts are required: the actual T1 holder, the
			// blocked same-ID recovery, and an independently warmed observer.
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 3)
			require.Equal(t, int32(3), pool.Config().MaxConns, "same-ID proof retains only its holder, contender and independent observer slots")
			orgID := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic same-ID startup')`, orgID)
			require.NoError(t, err, "seed only this isolated synthetic tenant")
			sessions, threads, jobs := db.NewSessionStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
			session := models.Session{OrgID: orgID, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle,
				AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual,
				InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, sessions.Create(ctx, &session), "create the genuine session and primary thread before preparation")
			_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/same-id',sandbox_state='snapshotted' WHERE org_id=$1 AND id=$2`, orgID, session.ID)
			require.NoError(t, err, "make the one fresh Start ordinarily eligible")
			_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable retained creation only in the disposable test database")
			base, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "use the existing production transaction owner and unchanged two-second budget")
			deps, err := threadsvc.NewSendDependencies(pool, sessions, threads, jobs)
			require.NoError(t, err, "construct actual thread send dependencies rather than a synthetic accepting sender")
			threadService, err := threadsvc.NewService(threads, sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), jobs, deps, zerolog.Nop())
			require.NoError(t, err, "use genuine exact-ID row creation and adoption under legitimate concurrent checkouts")
			threadService.SetThreadInboxStore(db.NewThreadInboxStore(pool), pool)
			runtime := &retainedSameIDRuntime{RuntimeAdapter: RuntimeAdapter{Sessions: sessions, Threads: threadService}}
			store := db.NewSessionReviewLoopStore(pool)
			t0 := &retainedStartFaultRunner{base: base, phase: "T0", commit: true}
			_, err = NewService(store, runtime, WithStartTransactions(t0)).Start(ctx, orgID, session.ID, StartReviewLoopRequest{MaxPasses: 2})
			var pending *StartPendingError
			require.ErrorAs(t, err, &pending, "actual committed T0 with lost acknowledgement retains the sole original correlation")
			require.True(t, t0.fired, "the preparation fault must follow an actual T0 commit")
			id := pending.LoopID
			var original db.ReviewLoopStartIntent
			require.NoError(t, base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				var found bool
				var err error
				original, found, err = db.ReadReviewLoopStartInTx(unitCtx, orgID, session.ID, id, tx)
				if err == nil && !found {
					return db.ErrReviewLoopStartIdentity
				}
				return err
			}), "read the actual frozen original before either T1 contender")
			frozen := original.Preparation()
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the independent third connection before either T1 caller starts")
			defer observer.Release()
			var observerPID int32
			require.NoError(t, observer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&observerPID), "warm the observer so first-use connection setup cannot consume a held transaction budget")
			ready, release := make(chan retainedSameIDHeldT1, 1), make(chan struct{})
			var releaseOnce sync.Once
			releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseGate()
			firstClaim := &retainedStartFaultRunner{base: base, phase: "claim", commit: true}
			gate := &retainedSameIDT1Gate{base: firstClaim, commit: tt.commit, ready: ready, release: release}
			type outcome struct {
				loop *models.SessionReviewLoop
				err  error
			}
			firstDone, secondDone := make(chan outcome, 1), make(chan outcome, 1)
			first := NewService(store, runtime, WithStartTransactions(gate))
			go func() {
				loop, err := first.RecoverStart(ctx, orgID, session.ID, id)
				firstDone <- outcome{loop, err}
			}()
			var held retainedSameIDHeldT1
			select {
			case held = <-ready:
			case early := <-firstDone:
				t.Fatalf("first recovery finished before actual held T1: %v", early.err)
			case <-ctx.Done():
				t.Fatal("actual T1 never reached its held callback boundary")
			}
			require.NotEqual(t, observerPID, held.pid, "observer must remain independent of the actual T1 holder")
			secondPIDReady := make(chan int32, 1)
			secondPIDOwner := &retainedSameIDPIDRunner{base: base, pid: secondPIDReady}
			secondClaim := &retainedStartFaultRunner{base: secondPIDOwner, phase: "claim", commit: true}
			second := NewService(store, runtime, WithStartTransactions(secondClaim))
			go func() {
				loop, err := second.RecoverStart(ctx, orgID, session.ID, id)
				secondDone <- outcome{loop, err}
			}()
			var secondPID int32
			select {
			case secondPID = <-secondPIDReady:
			case early := <-secondDone:
				t.Fatalf("second recovery finished before acquiring its actual transaction: %v", early.err)
			case <-ctx.Done():
				t.Fatal("same-ID contender never acquired its second bounded checkout")
			}
			require.NotEqual(t, held.pid, secondPID, "same-ID contender must own a distinct backend from T1")
			require.NotEqual(t, observerPID, secondPID, "lock observation must use neither contender's backend")
			var observationErr error
			require.Eventually(t, func() bool {
				var waiting bool
				observationErr = observer.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids(a.pid)) AND a.wait_event_type='Lock'
 AND a.query LIKE '%FROM review_loop_start_intents%' AND a.query LIKE '%FOR UPDATE%'
 FROM pg_stat_activity a WHERE a.datname=current_database() AND a.pid=$2`, held.pid, secondPID).Scan(&waiting)
				return observationErr == nil && waiting
			}, 500*time.Millisecond, 5*time.Millisecond, "actual same-ID recovery must wait on the original start row held by in-flight T1")
			releaseGate()
			require.NoError(t, observationErr, "the independent observer must read actual backend blocking evidence")
			require.Less(t, time.Since(held.since), time.Second, "release the real T1 promptly without extending its production transaction budget")
			var firstResult, secondResult outcome
			select {
			case firstResult = <-firstDone:
			case <-ctx.Done():
				t.Fatal("first T1 owner must finish after gate release")
			}
			select {
			case secondResult = <-secondDone:
			case <-ctx.Done():
				t.Fatal("same-ID contender must finish after original T1 commit or rollback")
			}
			require.True(t, gate.fired.Load(), "the first owner must genuinely reach and hold actual T1 writes")
			require.ErrorIs(t, firstResult.err, ErrReviewLoopStartPending, "first owner remains pending after either suppressed commit acknowledgement or real callback rollback")
			require.Nil(t, firstResult.loop, "unacknowledged first creation cannot be returned as known created")
			require.NoError(t, secondResult.err, "same-ID follow-up adopts the committed original or completes the rolled-back original")
			require.NotNil(t, secondResult.loop, "the later independently confirmed original creation must be returned")
			require.Equal(t, id, secondResult.loop.ID, "in-flight recovery must preserve the one preminted loop identity")
			require.Equal(t, &frozen.ThreadID, secondResult.loop.ThreadID, "E1 adoption and both T1 paths preserve the one original review thread")
			require.True(t, secondClaim.fired, "winning startup claim must actually commit before its acknowledgement is withheld")
			require.Equal(t, int32(0), runtime.sends.Load(), "an unacknowledged winning claim releases zero SendMessage calls")
			require.Equal(t, int32(1), runtime.reads.Load(), "same-ID recovery must not reload fresh session policy or invoke another Start")
			expectedCreates := int32(1)
			if !tt.commit {
				expectedCreates = 2
			}
			require.Equal(t, expectedCreates, runtime.creates.Load(), "only rollback recovery repeats row-only exact-ID adoption; committed recovery skips E1")
			var after db.ReviewLoopStartIntent
			require.NoError(t, base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				var found bool
				var err error
				after, found, err = db.ReadReviewLoopStartInTx(unitCtx, orgID, session.ID, id, tx)
				if err == nil && !found {
					return db.ErrReviewLoopStartIdentity
				}
				return err
			}), "read retained preparation after both actual owner outcomes settle")
			require.Equal(t, frozen, after.Preparation(), "all frozen scope, command, envelope, digests and preminted identities remain byte-for-byte unchanged")
			require.Equal(t, "created", after.State(), "the original retained generation must be created after the follow-up succeeds")
			var passID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2 AND loop_id=$3`, orgID, session.ID, id).Scan(&passID), "read the actual single bound first pass")
			require.Equal(t, frozen.PassID, passID, "both in-flight outcomes preserve the one preminted first-pass identity")
			var graph [5]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1),(SELECT count(*) FROM session_threads WHERE org_id=$1),(SELECT count(*) FROM session_review_loops WHERE org_id=$1),(SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1),(SELECT count(*) FROM session_messages WHERE org_id=$1)`, orgID).Scan(&graph[0], &graph[1], &graph[2], &graph[3], &graph[4]), "count the complete original graph after holder and contender both complete")
			require.Equal(t, [5]int{1, 2, 1, 1, 0}, graph, "one retained start, primary plus original review thread, one loop/pass and no message must survive")
		})
	}
}
