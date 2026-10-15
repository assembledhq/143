package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestOutcomeReconciliationOnceAllJobStatesPostgres(t *testing.T) {
	t.Parallel()
	for _, status := range []models.JobStatus{models.JobStatusPending, models.JobStatusRunning, models.JobStatusSucceeded, models.JobStatusFailed, models.JobStatusCancelled, models.JobStatusDeadLetter} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			a, b, orgID := newReconciliationOncePostgres(t)
			ctx := context.Background()
			opts := outcomeReconciliationOnceOpts(orgID, "2026-04-23")
			id, err := NewJobStore(a).EnqueueWithOpts(ctx, orgID, opts)
			require.NoError(t, err, "seed a legacy admission using the unchanged daily key")
			_, err = a.Exec(ctx, `UPDATE jobs SET status=$3,completed_at=CASE WHEN $3 IN ('pending','running') THEN NULL ELSE now() END WHERE org_id=$1 AND id=$2`, orgID, id, status)
			require.NoError(t, err, "move the existing outcome job to the requested lifecycle state")
			for range 2 {
				actual, err := NewJobStore(b).EnqueueCodeReviewOutcomeReconciliationOnce(ctx, orgID, opts)
				require.NoError(t, err, "each sequential producer must observe retained admission regardless of status")
				require.Equal(t, uuid.Nil, actual, "all lifecycle states must absorb repeated same-day admissions")
			}
			var count int
			require.NoError(t, a.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2`, orgID, *opts.DedupeKey).Scan(&count), "count all retained daily outcome rows")
			require.Equal(t, 1, count, "terminal completion must not allow another same-day outcome job")
		})
	}
}

func TestOutcomeReconciliationOnceConcurrentStoresPostgres(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"pending", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			a, b, orgID := newReconciliationOncePostgres(t)
			if status == "succeeded" {
				_, err := a.Exec(context.Background(), `ALTER TABLE jobs ALTER COLUMN status SET DEFAULT 'succeeded'`)
				require.NoError(t, err, "make each inserted outcome job terminal before another producer acquires its lock")
			}
			stores := []*JobStore{NewJobStore(a), NewJobStore(b)}
			opts := outcomeReconciliationOnceOpts(orgID, "2026-04-23")
			const requests = 24
			ids, errs := make([]uuid.UUID, requests), make([]error, requests)
			start := make(chan struct{})
			var wait sync.WaitGroup
			for i := range requests {
				wait.Go(func() {
					<-start
					ids[i], errs[i] = stores[i%len(stores)].EnqueueCodeReviewOutcomeReconciliationOnce(context.Background(), orgID, opts)
				})
			}
			close(start)
			wait.Wait()
			admitted := 0
			for i, id := range ids {
				require.NoError(t, errs[i], "independent store pools must serialize daily admissions")
				if id != uuid.Nil {
					admitted++
				}
			}
			require.Equal(t, 1, admitted, "a concurrent daily burst must admit exactly one job even when it finishes immediately")
			var count int
			require.NoError(t, a.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2`, orgID, *opts.DedupeKey).Scan(&count), "count committed admissions across both pools")
			require.Equal(t, 1, count, "waiting producers need READ COMMITTED visibility after acquiring their advisory lock")
		})
	}
}

func TestOutcomeReconciliationOnceDayAndScopeIsolationPostgres(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"next UTC day", "another tenant", "another job type", "another queue"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			a, b, orgID := newReconciliationOncePostgres(t)
			ctx := context.Background()
			seedOrg := orgID
			seed := outcomeReconciliationOnceOpts(orgID, "2026-04-23")
			if scope == "another tenant" {
				seedOrg = uuid.New()
				_, err := a.Exec(ctx, `INSERT INTO organizations(id) VALUES($1)`, seedOrg)
				require.NoError(t, err, "create an independent tenant with the same deliberately mismatched legacy key")
			}
			if scope == "another job type" {
				seed.JobType = "synthetic_other_job"
			}
			if scope == "another queue" {
				seed.Queue = "default"
			}
			id, err := NewJobStore(a).EnqueueWithOpts(ctx, seedOrg, seed)
			require.NoError(t, err, "seed the independently scoped existing job")
			_, err = a.Exec(ctx, `UPDATE jobs SET status='succeeded',completed_at=now() WHERE org_id=$1 AND id=$2`, seedOrg, id)
			require.NoError(t, err, "remove active-index conflicts so the full admission predicate is exercised")
			day := "2026-04-23"
			if scope == "next UTC day" {
				day = "2026-04-24"
			}
			opts := outcomeReconciliationOnceOpts(orgID, day)
			actual, err := NewJobStore(b).EnqueueCodeReviewOutcomeReconciliationOnce(ctx, orgID, opts)
			require.NoError(t, err, "a different day, tenant, type or queue must not count as this admission")
			require.NotEqual(t, uuid.Nil, actual, "independent scope must retain its required daily outcome work")
			duplicate, err := NewJobStore(a).EnqueueCodeReviewOutcomeReconciliationOnce(ctx, orgID, opts)
			require.NoError(t, err, "the exact admitted scope must then be observable across stores")
			require.Equal(t, uuid.Nil, duplicate, "only the exact same scope must be absorbed")
			var count int
			require.NoError(t, a.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=ANY($1)`, []uuid.UUID{orgID, seedOrg}).Scan(&count), "count both independently admitted jobs")
			require.Equal(t, 2, count, "scope isolation must neither suppress required work nor add duplicates")
		})
	}
}

type outcomeAdmissionCommitPool struct {
	*pgxpool.Pool
	commit func(context.Context, pgx.Tx) error
}

func (p outcomeAdmissionCommitPool) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := p.Pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return outcomeAdmissionCommitTx{Tx: tx, commit: p.commit}, nil
}

type outcomeAdmissionCommitTx struct {
	pgx.Tx
	commit func(context.Context, pgx.Tx) error
}

func (t outcomeAdmissionCommitTx) Commit(ctx context.Context) error {
	return t.commit(ctx, t.Tx)
}

func TestOutcomeReconciliationOnceCommitAndNotificationPostgres(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "rolled back", "commit acknowledgement lost"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a, b, orgID := newReconciliationOncePostgres(t)
			server := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "open an isolated outcome notification cache")
			t.Cleanup(func() { require.NoError(t, client.Close(), "close the isolated outcome notification cache") })
			baseline := server.CommandCount()
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseCommit := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseCommit)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			commitErr := errors.New("injected outcome commit error")
			observed := outcomeAdmissionCommitPool{Pool: a, commit: func(ctx context.Context, tx pgx.Tx) error {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if mode == "rolled back" {
					return commitErr
				}
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				if mode == "commit acknowledgement lost" {
					return commitErr
				}
				return nil
			}}
			store := NewJobStore(observed)
			store.SetNotifier(cache.NewJobNotifier(client, zerolog.Nop()))
			opts := outcomeReconciliationOnceOpts(orgID, "2026-04-23")
			type result struct {
				id  uuid.UUID
				err error
			}
			completed := make(chan result, 1)
			go func() {
				id, err := store.EnqueueCodeReviewOutcomeReconciliationOnce(ctx, orgID, opts)
				completed <- result{id, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("outcome admission did not reach its controlled commit")
			}
			var count int
			require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, orgID).Scan(&count), "read durable daily admission before commit")
			require.Equal(t, 0, count, "uncommitted daily work must be invisible to an independent producer")
			require.Equal(t, baseline, server.CommandCount(), "outcome work must not notify before durable commit")
			releaseCommit()
			var finished result
			select {
			case finished = <-completed:
			case <-ctx.Done():
				t.Fatal("outcome admission did not finish after its controlled commit was released")
			}
			if mode == "success" {
				require.NoError(t, finished.err, "acknowledged commit must return its durable outcome job")
				require.NotEqual(t, uuid.Nil, finished.id, "acknowledged admission must identify the committed job")
				require.Equal(t, baseline+1, server.CommandCount(), "successful outcome admission must publish exactly one wake-up")
			} else {
				require.ErrorIs(t, finished.err, commitErr, "failed or unacknowledged commit must propagate without an automatic retry")
				require.Equal(t, uuid.Nil, finished.id, "uncertain commit must not claim an accepted job identity")
				require.Equal(t, baseline, server.CommandCount(), "failed or uncertain commit must not publish a wake-up")
			}
			expectedCount := 1
			if mode == "rolled back" {
				expectedCount = 0
			}
			require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, orgID).Scan(&count), "inspect the actual outcome of a failed or acknowledged commit")
			require.Equal(t, expectedCount, count, "the caller must preserve the actual durable outcome rather than blindly reenqueuing")
			retryID, err := NewJobStore(b).EnqueueCodeReviewOutcomeReconciliationOnce(ctx, orgID, opts)
			require.NoError(t, err, "a later producer must reconcile retained admission before inserting")
			if mode == "rolled back" {
				require.NotEqual(t, uuid.Nil, retryID, "a proven rollback must leave the daily scope eligible")
			} else {
				require.Equal(t, uuid.Nil, retryID, "an acknowledged or uncertain durable commit must absorb the later producer")
			}
			require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, orgID).Scan(&count), "count rows after a later producer reconciles admission")
			require.Equal(t, 1, count, "every commit outcome must converge to one daily admission")
		})
	}
}

func TestOutcomeReconciliationOncePreservesOrdinaryRetryPostgres(t *testing.T) {
	t.Parallel()
	a, b, orgID := newReconciliationOncePostgres(t)
	ctx := context.Background()
	store := NewJobStore(a)
	opts := outcomeReconciliationOnceOpts(orgID, "2026-04-23")
	id, err := store.EnqueueCodeReviewOutcomeReconciliationOnce(ctx, orgID, opts)
	require.NoError(t, err, "admit the daily outcome repair")
	token := uuid.New()
	retryAt := time.Date(2026, 4, 23, 22, 5, 0, 0, time.UTC)
	_, err = a.Exec(ctx, `UPDATE jobs SET status='running',attempts=1,lock_token=$3 WHERE org_id=$1 AND id=$2`, orgID, id, token)
	require.NoError(t, err, "establish a normal running outcome job lease")
	owned, err := store.RetryWithLease(ctx, id, token, "transient provider failure", retryAt)
	require.NoError(t, err, "the admitted outcome job must retain its ordinary fenced retry")
	require.True(t, owned, "the current lease must still own its retried outcome job")
	var status string
	var attempts int
	var runAt time.Time
	require.NoError(t, b.QueryRow(ctx, `SELECT status,attempts,run_at FROM jobs WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&status, &attempts, &runAt), "inspect the admitted job's unchanged retry state")
	require.Equal(t, []any{"pending", 1, retryAt}, []any{status, attempts, runAt.UTC()}, "retry must preserve job identity, attempts and selected backoff")
	duplicate, err := NewJobStore(b).EnqueueCodeReviewOutcomeReconciliationOnce(ctx, orgID, opts)
	require.NoError(t, err, "new daily producers must observe the existing retry admission")
	require.Equal(t, uuid.Nil, duplicate, "retrying admitted work must not create an additional daily job")
	_, err = a.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, orgID, id)
	require.NoError(t, err, "finish the same daily outcome job")
	ordinary, err := store.EnqueueWithOpts(ctx, orgID, opts)
	require.NoError(t, err, "ordinary enqueue must retain active-only deduplication")
	require.NotEqual(t, uuid.Nil, ordinary, "the opt-in daily fence must not suppress explicit ordinary enqueue after a terminal job")
	activeDuplicate, err := store.EnqueueWithOpts(ctx, orgID, opts)
	require.NoError(t, err, "ordinary enqueue must still absorb an active duplicate")
	require.Equal(t, uuid.Nil, activeDuplicate, "the existing active unique index must remain in effect")
}
