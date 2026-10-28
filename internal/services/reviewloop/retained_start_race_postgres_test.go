package reviewloop

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type retainedT1Barrier struct {
	base      threadsvc.SendTransactions
	arrived   chan<- struct{}
	release   <-chan struct{}
	conflicts *atomic.Int32
}

func (r retainedT1Barrier) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.base.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return work(ctx, retainedT1BarrierTx{Tx: tx, gate: r})
	})
}

type retainedT1BarrierTx struct {
	pgx.Tx
	gate retainedT1Barrier
}

func (t retainedT1BarrierTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "INSERT INTO session_review_loops(") {
		select {
		case t.gate.arrived <- struct{}{}:
		case <-ctx.Done():
			return pgconn.CommandTag{}, ctx.Err()
		}
		select {
		case <-t.gate.release:
		case <-ctx.Done():
			return pgconn.CommandTag{}, ctx.Err()
		}
	}
	tag, err := t.Tx.Exec(ctx, sql, args...)
	if isRunningLoopConflict(err) {
		t.gate.conflicts.Add(1)
	}
	return tag, err
}

func TestRetainedConcurrentT1RunningConflictPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
	org, session, _ := retainedPublicationFixture(t, ctx, pool)
	sessions, threads, jobs := db.NewSessionStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
	deps, err := threadsvc.NewSendDependencies(pool, sessions, threads, jobs)
	require.NoError(t, err, "construct original bounded transaction owner")
	threadService, err := threadsvc.NewService(threads, sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), jobs, deps, zerolog.Nop())
	require.NoError(t, err, "use actual row-only creation for both contenders")
	runtime := RuntimeAdapter{Sessions: sessions, Threads: threadService}
	ids := make([]uuid.UUID, 2)
	for n := range ids {
		runner := &retainedStartFaultRunner{base: deps.Transactions, phase: "T0", commit: true}
		service := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(runner))
		_, err := service.Start(ctx, org, session.ID, StartReviewLoopRequest{MaxPasses: 2})
		var pending *StartPendingError
		require.ErrorAs(t, err, &pending, "retain each real manual T0 before either loop is installed")
		require.True(t, runner.fired, "each preparation must actually commit before acknowledgement suppression")
		ids[n] = pending.LoopID
	}
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	var conflicts atomic.Int32
	type result struct {
		id   uuid.UUID
		loop *models.SessionReviewLoop
		err  error
	}
	results := make(chan result, 2)
	for _, id := range ids {
		// Separate fault owners avoid shared test state. The winning claim is
		// actually committed but unacknowledged, so neither caller may send.
		claim := &retainedStartFaultRunner{base: deps.Transactions, phase: "claim", commit: true}
		gate := retainedT1Barrier{base: claim, arrived: arrived, release: release, conflicts: &conflicts}
		service := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(gate))
		go func() { loop, err := service.RecoverStart(ctx, org, session.ID, id); results <- result{id, loop, err} }()
	}
	for range ids {
		select {
		case <-arrived:
		case early := <-results:
			close(release)
			t.Fatalf("contender finished before both INSERT barriers: %v", early.err)
		case <-ctx.Done():
			close(release)
			t.Fatal("both genuine T1 transactions must reach INSERT after their independent conflict reads")
		}
	}
	close(release)
	var winner, loser uuid.UUID
	for range ids {
		select {
		case result := <-results:
			if errors.Is(result.err, ErrReviewLoopStartRefused) {
				loser = result.id
				require.Nil(t, result.loop, "rolled-back losing T1 must never expose a created loop")
			} else {
				require.NoError(t, result.err, "winning T1 returns known creation despite unknown claim acknowledgement")
				winner = result.id
				require.Equal(t, winner, result.loop.ID, "winner preserves its original preminted identity")
			}
		case <-ctx.Done():
			t.Fatal("both T1 contenders must terminate")
		}
	}
	require.NotEqual(t, uuid.Nil, winner, "exactly one concurrent T1 wins")
	require.NotEqual(t, uuid.Nil, loser, "the actual unique-index loser is durably refused after known rollback")
	require.Equal(t, int32(1), conflicts.Load(), "test must exercise the actual unique-index conflict, not only the earlier existence check")
	var state, detail string
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,detail FROM review_loop_start_intents WHERE org_id=$1 AND id=$2`, org, loser).Scan(&state, &detail), "read losing retained generation")
	require.Equal(t, [2]string{"refused", "running_conflict"}, [2]string{state, detail}, "loser is finite and remains correlated to its original start")
	var graph [3]int
	require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_review_loops WHERE org_id=$1),(SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1),(SELECT count(*) FROM session_messages WHERE org_id=$1)`, org).Scan(&graph[0], &graph[1], &graph[2]), "read complete durable graph after both T1 units")
	require.Equal(t, [3]int{1, 1, 0}, graph, "only the winning loop and pass commit, and an unacknowledged claim sends nothing")
}
