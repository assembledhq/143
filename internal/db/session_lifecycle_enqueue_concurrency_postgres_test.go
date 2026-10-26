package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLifecycleEnqueueConcurrentUniqueConflictPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		commit bool
		want   lifecycleEnqueueDisposition
	}{
		{name: "committed competitor requires reconciliation", commit: true, want: lifecycleEnqueuePendingReconciliation},
		{name: "aborted competitor permits a new exact job", want: lifecycleEnqueueNewJob},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 4)
			binding, opts := seedLifecycleEnqueueInputs(t, pool)
			baseline := lifecycleEnqueueGraph(t, pool, binding.orgID)
			ownerCtx, stopOwner := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopOwner()
			competitor, err := pool.Begin(ownerCtx)
			require.NoError(t, err, "begin the independent uncommitted global-key competitor")
			defer func() { _ = competitor.Rollback(context.Background()) }()
			competitorID := uuid.New()
			id, err := enqueueOnWithID(ownerCtx, competitor, binding.orgID, opts, competitorID)
			require.NoError(t, err, "hold the global queue/key conflict in a different real transaction")
			require.Equal(t, competitorID, id, "the competitor must not masquerade as the expected immutable job")
			var competitorPID uint32
			require.NoError(t, competitor.QueryRow(ownerCtx, `SELECT pg_backend_pid()`).Scan(&competitorPID), "retain the exact lock holder for the database wait barrier")
			var competitorBefore json.RawMessage
			require.NoError(t, competitor.QueryRow(ownerCtx, `SELECT to_jsonb(j) FROM jobs j WHERE org_id=$1 AND id=$2`, binding.orgID, competitorID).Scan(&competitorBefore), "retain every field of the independent competitor before settlement")

			ctx, cancel, tx := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
			defer cancel()
			defer func() { _ = tx.Rollback(context.Background()) }()
			_, err = lockLifecycleScopeInTx(ctx, tx, binding.orgID, lifecycleScope{sessionID: binding.fence.sessionID, threadIDs: []uuid.UUID{binding.fence.threadID}})
			require.NoError(t, err, "the candidate owner must hold its real canonical parent/lane scope")
			var candidatePID uint32
			require.NoError(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&candidatePID), "identify the exact candidate rather than relying on a goroutine-start signal")
			// Acquire the independent observer before starting the INSERT, so
			// pool/connection setup cannot consume its unchanged lock budget.
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve one independent observer within the existing candidate unit budget")
			defer observer.Release()
			type outcome struct {
				result lifecycleEnqueueResult
				err    error
			}
			done := make(chan outcome, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				result, err := enqueueLifecycleJobInTx(ctx, tx, binding.orgID, binding, opts, nil)
				done <- outcome{result: result, err: err}
			}()
			// Join before the earlier transaction/pool cleanup defers run, even
			// when a failed barrier assertion exits this test with Goexit.
			defer func() {
				cancel()
				<-finished
			}()

			// Observe the real unique-key wait until this unit's existing
			// deadline, not a second arbitrary race-sensitive timeout. Neither
			// a goroutine signal nor elapsed time is evidence of blocking.
			observations := time.NewTicker(time.Millisecond)
			defer observations.Stop()
			for {
				var blocked bool
				err := observer.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2::integer))`, competitorPID, candidatePID).Scan(&blocked)
				require.NoError(t, err, "the independent observer must remain usable within the unchanged candidate lifecycle deadline")
				if blocked {
					break
				}
				select {
				case <-observations.C:
				case early := <-done:
					t.Fatalf("candidate completed before PostgreSQL proved the exact unique-key wait: disposition=%s error=%v", early.result.disposition, early.err)
				case <-ctx.Done():
					t.Fatal("PostgreSQL must prove the candidate INSERT waits on the exact competitor within the unchanged lifecycle owner budget")
				}
			}
			if tt.commit {
				require.NoError(t, competitor.Commit(ownerCtx), "commit the actual lock holder only after the candidate reaches its database wait")
			} else {
				require.NoError(t, competitor.Rollback(ownerCtx), "abort the actual lock holder only after the candidate reaches its database wait")
			}
			var actual outcome
			select {
			case actual = <-done:
			case <-ctx.Done():
				t.Fatal("candidate must resolve the unique conflict within its unchanged lifecycle owner budget")
			}
			require.NoError(t, actual.err, "the conflict winner's actual commit or rollback must produce a finite enqueue disposition")
			wantID := uuid.Nil
			if tt.want == lifecycleEnqueueNewJob {
				wantID = binding.jobID
			}
			require.Equal(t, lifecycleEnqueueResult{disposition: tt.want, jobID: wantID}, actual.result, "a committed different job cannot be adopted, while an aborted collision cannot fabricate consumption")
			var usable bool
			require.NoError(t, tx.QueryRow(ctx, `SELECT true`).Scan(&usable), "enqueue must leave the outer transaction open after either conflict outcome")
			require.True(t, usable, "only the outer owner can bind inputs and settle its unit")
			require.NoError(t, tx.Rollback(ctx), "discard any candidate job rather than commit a test input without its admission binding")
			if tt.commit {
				require.Equal(t, competitorBefore, lifecycleStatusJobSnapshot(t, pool, binding.orgID, competitorID), "all fields of the committed competitor remain untouched by the losing candidate")
				var count int
				require.NoError(t, pool.QueryRow(ownerCtx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND id=$2`, binding.orgID, binding.jobID).Scan(&count), "inspect only the candidate's exact tenant/job identity")
				require.Equal(t, 0, count, "committed global-key collision must never create a second input consumer")
			} else {
				require.Equal(t, baseline, lifecycleEnqueueGraph(t, pool, binding.orgID), "competitor abort and candidate rollback must restore every original job/input field")
			}
		})
	}
}
