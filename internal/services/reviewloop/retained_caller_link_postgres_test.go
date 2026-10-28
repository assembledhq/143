package reviewloop

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type retainedBypassCommitPool struct {
	*pgxpool.Pool
	ready, release  chan struct{}
	commit, unknown bool
}

func (p *retainedBypassCommitPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return retainedBypassCommitTx{Tx: tx, owner: p}, nil
}

type retainedBypassCommitTx struct {
	pgx.Tx
	owner *retainedBypassCommitPool
}

func (t retainedBypassCommitTx) Commit(ctx context.Context) error {
	close(t.owner.ready)
	select {
	case <-t.owner.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if !t.owner.commit {
		if err := t.Tx.Rollback(ctx); err != nil {
			return err
		}
		return errRetainedStartAck
	}
	if err := t.Tx.Commit(ctx); err != nil {
		return err
	}
	if t.owner.unknown {
		return errRetainedStartAck
	}
	return nil
}

func TestRetainedPublicationLateLinkBypassPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		commit, unknown bool
	}{
		{name: "known bypass commit", commit: true},
		{name: "bypass commit acknowledgement lost", commit: true, unknown: true},
		{name: "bypass rollback acknowledgement ambiguous", unknown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			// Caller writer, stale linker, and an independent lock observer.
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 3)
			org, session, publication := retainedPublicationFixture(t, ctx, pool)
			base, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "use unchanged bounded startup units")
			claim := &retainedStartFaultRunner{base: base, phase: "claim", commit: true}
			runtime := &retainedStartTestRuntime{pool: pool}
			revision, head := int64(1), "synthetic-original-head"
			req := StartReviewLoopRequest{MaxPasses: 2, Source: models.ReviewLoopSourcePublication, ChangesetID: &publication.ChangesetID, WorkspaceRevision: &revision, DesiredHeadSHA: &head, ReviewRequired: true}
			loop, err := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(claim)).Start(ctx, org, session.ID, req)
			require.NoError(t, err, "commit a real original loop before withholding only its claim acknowledgement")
			require.True(t, claim.fired, "fixture must leave an actual unresolved retained attempt")
			bypass := &retainedBypassCommitPool{Pool: pool, ready: make(chan struct{}), release: make(chan struct{}), commit: tt.commit, unknown: tt.unknown}
			bypassed := make(chan error, 1)
			go func() { bypassed <- db.NewSessionPublicationStore(bypass).ApplyReviewBypass(ctx, org, &publication) }()
			select {
			case <-bypass.ready:
			case err := <-bypassed:
				t.Fatalf("bypass did not reach its real commit boundary: %v", err)
			case <-ctx.Done():
				t.Fatal("bypass must reach commit while holding ordered caller/start/loop locks")
			}
			linked := make(chan error, 1)
			go func() {
				linked <- db.NewSessionPublicationStore(pool).LinkReviewLoop(ctx, org, session.ID, publication.ChangesetID, loop.ID, 2, revision, head)
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND query LIKE 'UPDATE session_publications%' AND cardinality(pg_blocking_pids(pid))>0)`).Scan(&waiting)
				return err == nil && waiting
			}, time.Second, time.Millisecond, "stale linker must actually wait behind bypass using its pre-commit statement snapshot")
			close(bypass.release)
			bypassErr, linkErr := <-bypassed, <-linked
			if tt.unknown {
				require.ErrorIs(t, bypassErr, errRetainedStartAck, "suppress only the actual commit or rollback acknowledgement")
			} else {
				require.NoError(t, bypassErr, "known bypass commit returns success")
			}
			if tt.commit {
				require.ErrorIs(t, linkErr, pgx.ErrNoRows, "fresh caller-row CAS must reject relinking after committed explicit bypass")
			} else {
				require.NoError(t, linkErr, "a genuinely rolled-back bypass leaves the original pending caller linkable")
			}
			actual, err := db.NewSessionPublicationStore(pool).GetByChangeset(ctx, org, session.ID, publication.ChangesetID)
			require.NoError(t, err, "read authoritative caller after both writers terminate")
			require.Equal(t, &loop.ID, actual.ReviewStartID, "neither bypass nor late linking may replace the original retained identity")
			if tt.commit {
				require.Equal(t, models.SessionPublicationReviewGateNotRequired, actual.ReviewGateState, "late old snapshot cannot reopen bypassed review gate")
				require.Nil(t, actual.ReviewLoopID, "committed bypass keeps the mutable loop link cleared")
			} else {
				require.Equal(t, models.SessionPublicationReviewGatePending, actual.ReviewGateState, "actual rollback preserves the original pending gate")
				require.Equal(t, &loop.ID, actual.ReviewLoopID, "late original link succeeds only after genuine bypass rollback")
			}
			require.Equal(t, 0, runtime.sends, "uncertain committed claim and caller writers never release a send")
		})
	}
}
