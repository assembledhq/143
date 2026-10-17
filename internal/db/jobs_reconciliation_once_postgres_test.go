package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func newReconciliationOncePostgres(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for periodic reconciliation admission proof")
	}
	return newReconciliationOncePostgresDatabase(t, url, "")
}

func newReconciliationOncePostgresDatabase(t *testing.T, url, database string) (*pgxpool.Pool, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	adminConfig, err := pgx.ParseConfig(url)
	require.NoError(t, err, "parse isolated admission database")
	if database != "" {
		adminConfig.Database = database
	}
	adminConfig.RuntimeParams["statement_timeout"] = "10000"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	require.NoError(t, err, "connect isolated admission database")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close admission schema connection") })
	if database != "" {
		var actualDatabase string
		require.NoError(t, admin.QueryRow(ctx, `SELECT current_database()`).Scan(&actualDatabase), "read the fixture schema owner's actual database")
		require.Equal(t, database, actualDatabase, "the fixture schema owner must use the explicitly overridden database")
	}
	schema := "job_admission_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create owned admission schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "drop only the owned admission schema")
	})
	pools := make([]*pgxpool.Pool, 2)
	for i := range pools {
		cfg, err := pgxpool.ParseConfig(url)
		require.NoError(t, err, "parse independent admission pool")
		cfg.ConnConfig.Database = adminConfig.Database
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
		cfg.ConnConfig.RuntimeParams["lock_timeout"] = "10000"
		cfg.MaxConns = 4
		pools[i], err = pgxpool.NewWithConfig(ctx, cfg)
		require.NoError(t, err, "open independent admission pool")
		t.Cleanup(pools[i].Close)
		if database != "" {
			var actualDatabase string
			require.NoError(t, pools[i].QueryRow(ctx, `SELECT current_database()`).Scan(&actualDatabase), "read each independent fixture pool's actual database")
			require.Equal(t, database, actualDatabase, "each fixture pool must use the explicitly overridden database")
		}
	}
	_, err = pools[0].Exec(ctx, `CREATE TABLE organizations(id uuid PRIMARY KEY)`)
	require.NoError(t, err, "create admission tenant fixture")
	initial, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000001_init.up.sql"))
	require.NoError(t, err, "read actual jobs schema")
	start := strings.Index(string(initial), "CREATE TABLE jobs (")
	require.Greater(t, start, 0, "find actual jobs table definition")
	end := strings.Index(string(initial)[start:], "-- nodes")
	require.Greater(t, end, 0, "find end of actual jobs indexes")
	_, err = pools[0].Exec(ctx, string(initial)[start:start+end])
	require.NoError(t, err, "create actual jobs table and active dedupe index")
	_, err = pools[0].Exec(ctx, `ALTER TABLE jobs ADD COLUMN run_owner_id text, ADD COLUMN owner_kind text DEFAULT 'worker', ADD COLUMN lock_token uuid, ADD COLUMN lease_expires_at timestamptz, ADD COLUMN retry_window_started_at timestamptz`)
	require.NoError(t, err, "add current fenced retry columns")
	addContinuationRetryFixtureSchema(t, pools[0])
	index := reconciliationOnceLookupIndexMigration(t)
	const concurrentDeclaration = "CREATE INDEX CONCURRENTLY idx_jobs_org_queue_dedupe_created"
	require.Equal(t, 1, strings.Count(index, concurrentDeclaration), "fixture must replace exactly the existing lookup-index declaration")
	// This disposable table has no live writers to protect. The production
	// partial concurrent build waits for unrelated older snapshots anywhere
	// in the database, including other parallel test schemas. Preserve its
	// actual keys and predicate while avoiding that global snapshot barrier.
	index = strings.Replace(index, concurrentDeclaration, "CREATE INDEX idx_jobs_org_queue_dedupe_created", 1)
	_, err = pools[0].Exec(ctx, index)
	require.NoError(t, err, "create the actual lookup index without waiting for unrelated test snapshots")
	orgID := uuid.New()
	_, err = pools[0].Exec(ctx, `INSERT INTO organizations(id) VALUES($1)`, orgID)
	require.NoError(t, err, "seed admission tenant")
	return pools[0], pools[1], orgID
}

func reconciliationOnceLookupIndexMigration(t *testing.T) string {
	t.Helper()
	index, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000294_code_review_preparation_job_lookup.up.sql"))
	require.NoError(t, err, "read the production terminal-inclusive lookup index")
	return string(index)
}

func requireReconciliationOnceLookupIndexCatalog(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	type indexCatalog struct {
		Valid, Ready, Unique bool
		Method               string
		KeyCount, TotalCount int
		Keys                 []string
		Predicate            string
	}
	var actual indexCatalog
	err := pool.QueryRow(context.Background(), `
		SELECT i.indisvalid, i.indisready, i.indisunique, am.amname,
		       i.indnkeyatts, i.indnatts,
		       ARRAY(SELECT a.attname::text
		             FROM unnest(i.indkey::smallint[]) WITH ORDINALITY AS k(attnum, position)
		             JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum
		             ORDER BY k.position),
		       pg_get_expr(i.indpred, i.indrelid)
		FROM pg_index i
		JOIN pg_class idx ON idx.oid=i.indexrelid
		JOIN pg_am am ON am.oid=idx.relam
		WHERE idx.relnamespace=current_schema()::regnamespace
		  AND idx.relname='idx_jobs_org_queue_dedupe_created'
		  AND i.indrelid='jobs'::regclass`).Scan(
		&actual.Valid, &actual.Ready, &actual.Unique, &actual.Method,
		&actual.KeyCount, &actual.TotalCount, &actual.Keys, &actual.Predicate)
	require.NoError(t, err, "inspect the actual lookup index in this fixture's schema")
	require.Equal(t, indexCatalog{
		Valid: true, Ready: true, Unique: false, Method: "btree", KeyCount: 5, TotalCount: 5,
		Keys: []string{"org_id", "queue", "dedupe_key", "created_at", "id"}, Predicate: "(dedupe_key IS NOT NULL)",
	}, actual, "fixture must retain the production index's validity, access method, ordered keys and partial predicate")
}

func TestReconciliationOnceFixtureIgnoresUnrelatedOldSnapshotPostgres(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL for the unrelated old-snapshot fixture regression")
	}
	ctx := context.Background()
	config, err := pgx.ParseConfig(raw)
	require.NoError(t, err, "parse the disposable database server")
	admin, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err, "connect the disposable database owner")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close the disposable database owner") })
	name := "reconciliation_snapshot_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	require.NoError(t, err, "create a database owned only by this snapshot regression")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		require.NoError(t, err, "drop only the owned regression database after all connections close")
	})
	ownedConfig := config.Copy()
	ownedConfig.Database = name
	control, _, _ := newReconciliationOncePostgresDatabase(t, raw, name)
	var controlDatabase string
	require.NoError(t, control.QueryRow(ctx, `SELECT current_database()`).Scan(&controlDatabase), "read the actual concurrent-control database")
	require.Equal(t, name, controlDatabase, "the concurrent control must use only this test's owned child database")
	_, err = control.Exec(ctx, `DROP INDEX idx_jobs_org_queue_dedupe_created;
		CREATE TABLE public.reconciliation_snapshot_anchor(value integer NOT NULL);
		INSERT INTO public.reconciliation_snapshot_anchor VALUES(1)`)
	require.NoError(t, err, "prepare the original concurrent-build control and an unrelated reader table")

	reader, err := pgx.ConnectConfig(ctx, ownedConfig)
	require.NoError(t, err, "open an independent old-snapshot reader")
	t.Cleanup(func() { require.NoError(t, reader.Close(ctx), "close the old-snapshot reader connection") })
	readerTx, err := reader.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err, "begin a repeatable-read transaction unrelated to the indexed jobs table")
	t.Cleanup(func() {
		if rollbackErr := readerTx.Rollback(ctx); !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			require.NoError(t, rollbackErr, "release an unfinished old-snapshot transaction")
		}
	})
	var readerPID int32
	var value int
	var oldSnapshot, readerDatabase string
	err = readerTx.QueryRow(ctx, `SELECT pg_backend_pid(), value, pg_current_snapshot()::text, current_database()
		FROM public.reconciliation_snapshot_anchor`).Scan(&readerPID, &value, &oldSnapshot, &readerDatabase)
	require.NoError(t, err, "establish a real older snapshot by reading an unrelated row")
	require.Equal(t, name, readerDatabase, "the old reader must hold a snapshot only inside the owned child database")
	require.Equal(t, 1, value, "the held reader must have positively read its unrelated fixture")
	require.NotEmpty(t, oldSnapshot, "the reader must retain an actual repeatable-read snapshot")

	builder, err := control.Acquire(ctx)
	require.NoError(t, err, "reserve the original concurrent-index builder connection")
	t.Cleanup(builder.Release)
	var builderDatabase string
	require.NoError(t, builder.QueryRow(ctx, `SELECT current_database()`).Scan(&builderDatabase), "read the reserved concurrent builder's actual database")
	require.Equal(t, name, builderDatabase, "the original concurrent builder must use only the owned child database")
	// Only the deliberately blocked control has no server statement or lock
	// deadline; its client context remains bounded. Repaired fixtures keep
	// their existing ten-second statement and lock timeouts unchanged.
	_, err = builder.Exec(ctx, `SET statement_timeout=0; SET lock_timeout=0`)
	require.NoError(t, err, "let the controlled original build remain blocked until the reader is released")
	builderPID := builder.Conn().PgConn().PID()
	migration := reconciliationOnceLookupIndexMigration(t)
	controlCtx, cancelControl := context.WithTimeout(ctx, 30*time.Second)
	controlDone := make(chan error, 1)
	controlJoined := false
	t.Cleanup(func() {
		cancelControl()
		if !controlJoined {
			controlErr := <-controlDone
			if controlErr != nil {
				t.Logf("original concurrent-index control stopped during failure cleanup: %v", controlErr)
			}
		}
	})
	go func() {
		_, buildErr := builder.Exec(controlCtx, migration)
		controlDone <- buildErr
	}()

	waitingForReader := func() (bool, error) {
		var waiting bool
		err := control.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_stat_progress_create_index p
			JOIN pg_stat_activity a ON a.pid=p.current_locker_pid
			WHERE p.pid=$1 AND p.command='CREATE INDEX CONCURRENTLY'
			  AND p.phase='waiting for old snapshots' AND p.current_locker_pid=$2
			  AND p.relid='jobs'::regclass AND p.index_relid='idx_jobs_org_queue_dedupe_created'::regclass
			  AND a.state='idle in transaction' AND a.backend_xmin IS NOT NULL
		)`, builderPID, readerPID).Scan(&waiting)
		return waiting, err
	}
	var progressErr error
	require.Eventually(t, func() bool {
		var waiting bool
		waiting, progressErr = waitingForReader()
		return progressErr == nil && waiting
	}, 5*time.Second, 10*time.Millisecond, "the original partial concurrent index must positively wait for this exact unrelated reader")
	require.NoError(t, progressErr, "read the controlled builder's actual PostgreSQL progress")

	a, b, orgID := newReconciliationOncePostgresDatabase(t, raw, name)
	var fixtureDatabase string
	require.NoError(t, a.QueryRow(ctx, `SELECT current_database()`).Scan(&fixtureDatabase), "read the actual repaired-fixture database")
	require.Equal(t, name, fixtureDatabase, "the repaired fixture must use the same owned child database as the controlled blocker")
	requireReconciliationOnceLookupIndexCatalog(t, a)
	opts := reconciliationOnceOpts(orgID, "20260423220")
	id, err := NewJobStore(a).EnqueuePullRequestReconciliationOnce(ctx, orgID, opts)
	require.NoError(t, err, "the repaired fixture must admit work while the unrelated snapshot remains held")
	require.NotEqual(t, uuid.Nil, id, "the repaired fixture must create a durable admission")
	duplicate, err := NewJobStore(b).EnqueuePullRequestReconciliationOnce(ctx, orgID, opts)
	require.NoError(t, err, "the independent store must observe the same durable admission")
	require.Equal(t, uuid.Nil, duplicate, "the fixture change must preserve duplicate-admission fencing")
	waiting, err := waitingForReader()
	require.NoError(t, err, "inspect the original build after the repaired fixture and admission complete")
	require.True(t, waiting, "the original control must still be blocked on the exact reader after the repaired fixture succeeds")
	var retainedSnapshot string
	require.NoError(t, readerTx.QueryRow(ctx, `SELECT pg_current_snapshot()::text`).Scan(&retainedSnapshot), "confirm the older reader remains open")
	require.Equal(t, oldSnapshot, retainedSnapshot, "fixture creation must not release or replace the unrelated repeatable-read snapshot")

	require.NoError(t, readerTx.Commit(ctx), "release the exact older snapshot only after the repaired fixture proof")
	select {
	case controlErr := <-controlDone:
		controlJoined = true
		require.NoError(t, controlErr, "the unmodified production concurrent index must complete after its reader is released")
	case <-controlCtx.Done():
		t.Fatal("original concurrent-index control did not finish after its old snapshot was released")
	}
	requireReconciliationOnceLookupIndexCatalog(t, control)
}

func reconciliationOnceOpts(orgID uuid.UUID, bucket string) EnqueueOpts {
	key := "reconcile_pull_request_state:" + orgID.String() + ":" + bucket
	return EnqueueOpts{Queue: "default", JobType: "reconcile_pull_request_state", Payload: map[string]any{"org_id": orgID.String(), "limit": 50}, Priority: 2, DedupeKey: &key}
}

func TestReconciliationOnceAllJobStatesPostgres(t *testing.T) {
	t.Parallel()
	for _, status := range []models.JobStatus{models.JobStatusPending, models.JobStatusRunning, models.JobStatusSucceeded, models.JobStatusFailed, models.JobStatusCancelled, models.JobStatusDeadLetter} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			a, b, orgID := newReconciliationOncePostgres(t)
			ctx := context.Background()
			opts := reconciliationOnceOpts(orgID, "20260423220")
			// Seed through the ordinary legacy enqueue path, retaining rolling compatibility.
			id, err := NewJobStore(a).EnqueueWithOpts(ctx, orgID, opts)
			require.NoError(t, err, "seed the existing raw bucket key")
			_, err = a.Exec(ctx, `UPDATE jobs SET status=$3,completed_at=CASE WHEN $3 IN ('pending','running') THEN NULL ELSE now() END WHERE org_id=$1 AND id=$2`, orgID, id, status)
			require.NoError(t, err, "place prior admission in the requested lifecycle state")
			actual, err := NewJobStore(b).EnqueuePullRequestReconciliationOnce(ctx, orgID, opts)
			require.NoError(t, err, "another store must retain the existing bucket admission")
			require.Equal(t, uuid.Nil, actual, "every lifecycle state must prevent a fresh same-bucket admission")
			var count int
			require.NoError(t, a.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, orgID).Scan(&count), "count the complete tenant admission history")
			require.Equal(t, 1, count, "terminal completion must not create another bucket job")
		})
	}
}

func TestReconciliationOnceConcurrentStoresPostgres(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"pending", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			a, b, orgID := newReconciliationOncePostgres(t)
			if status == "succeeded" {
				_, err := a.Exec(context.Background(), `ALTER TABLE jobs ALTER COLUMN status SET DEFAULT 'succeeded'`)
				require.NoError(t, err, "make admitted jobs terminal before a waiting producer can observe them")
			}
			stores := []*JobStore{NewJobStore(a), NewJobStore(b)}
			opts := reconciliationOnceOpts(orgID, "20260423220")
			const requests = 24
			ids, errs := make([]uuid.UUID, requests), make([]error, requests)
			start := make(chan struct{})
			var wait sync.WaitGroup
			for i := range requests {
				wait.Go(func() {
					<-start
					ids[i], errs[i] = stores[i%2].EnqueuePullRequestReconciliationOnce(context.Background(), orgID, opts)
				})
			}
			close(start)
			wait.Wait()
			admitted := 0
			for i, id := range ids {
				require.NoError(t, errs[i], "independent stores must serialize concurrent admissions")
				if id != uuid.Nil {
					admitted++
				}
			}
			require.Equal(t, 1, admitted, "a concurrent burst must admit exactly one job across pools")
			var count int
			require.NoError(t, a.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2`, orgID, *opts.DedupeKey).Scan(&count), "count all admitted rows")
			require.Equal(t, 1, count, "the raw persisted bucket key must remain unchanged")
		})
	}
}

func TestReconciliationOnceNextBucketAndTenantPostgres(t *testing.T) {
	t.Parallel()
	a, b, orgID := newReconciliationOncePostgres(t)
	otherOrg := uuid.New()
	_, err := a.Exec(context.Background(), `INSERT INTO organizations(id) VALUES($1)`, otherOrg)
	require.NoError(t, err, "seed independent tenant")
	for _, tt := range []struct {
		name   string
		org    uuid.UUID
		bucket string
	}{{"first bucket", orgID, "20260423220"}, {"next bucket", orgID, "20260423221"}, {"other tenant", otherOrg, "20260423220"}} {
		id, err := NewJobStore(b).EnqueuePullRequestReconciliationOnce(context.Background(), tt.org, reconciliationOnceOpts(tt.org, tt.bucket))
		require.NoError(t, err, "admit %s independently", tt.name)
		require.NotEqual(t, uuid.Nil, id, "%s must retain its independent periodic work", tt.name)
	}
	var count int
	require.NoError(t, a.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE org_id=ANY($1)`, []uuid.UUID{orgID, otherOrg}).Scan(&count), "count independent bucket admissions")
	require.Equal(t, 3, count, "distinct tenant and bucket keys must not suppress each other")
}

type reconciliationCommitPool struct {
	*pgxpool.Pool
	beforeCommit func(context.Context) error
}

func (p reconciliationCommitPool) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := p.Pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return reconciliationCommitTx{Tx: tx, beforeCommit: p.beforeCommit}, nil
}

type reconciliationCommitTx struct {
	pgx.Tx
	beforeCommit func(context.Context) error
}

func (t reconciliationCommitTx) Commit(ctx context.Context) error {
	if err := t.beforeCommit(ctx); err != nil {
		return err
	}
	return t.Tx.Commit(ctx)
}

func TestReconciliationOnceCommitBeforeNotifyPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		fail bool
	}{{"commit publishes durable work", false}, {"failed commit rolls back without notification", true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, b, orgID := newReconciliationOncePostgres(t)
			server := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "open isolated notification cache")
			t.Cleanup(func() { require.NoError(t, client.Close(), "close isolated notification cache") })
			baseline := server.CommandCount()
			entered, release := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			commitErr := errors.New("simulated commit failure")
			observed := reconciliationCommitPool{Pool: a, beforeCommit: func(ctx context.Context) error {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if tt.fail {
					return commitErr
				}
				return nil
			}}
			store := NewJobStore(observed)
			store.SetLogger(zerolog.Nop())
			store.SetNotifier(cache.NewJobNotifier(client, zerolog.Nop()))
			opts := reconciliationOnceOpts(orgID, "20260423220")
			type result struct {
				id  uuid.UUID
				err error
			}
			completed := make(chan result, 1)
			go func() {
				id, err := store.EnqueuePullRequestReconciliationOnce(ctx, orgID, opts)
				completed <- result{id, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("admission did not reach its controlled commit")
			}
			var beforeCount int
			require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, orgID).Scan(&beforeCount), "inspect durable rows before commit")
			require.Equal(t, 0, beforeCount, "uncommitted admission must not be visible")
			require.Equal(t, baseline, server.CommandCount(), "queue wake-up must not precede durable commit")
			close(release)
			finished := <-completed
			if tt.fail {
				require.ErrorIs(t, finished.err, commitErr, "commit failure must propagate")
				require.Equal(t, uuid.Nil, finished.id, "failed commit must not return an admitted identity")
				require.Equal(t, baseline, server.CommandCount(), "failed commit must publish no wake-up")
				require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, orgID).Scan(&beforeCount), "inspect rollback outcome")
				require.Equal(t, 0, beforeCount, "rollback must not strand a periodic admission marker")
				id, err := NewJobStore(b).EnqueuePullRequestReconciliationOnce(ctx, orgID, opts)
				require.NoError(t, err, "retry after rolled-back admission must succeed")
				require.NotEqual(t, uuid.Nil, id, "rollback must leave the bucket eligible")
			} else {
				require.NoError(t, finished.err, "successful commit must return durable admission")
				require.NotEqual(t, uuid.Nil, finished.id, "successful commit must identify the new job")
				require.Equal(t, baseline+1, server.CommandCount(), "successful commit must publish exactly one wake-up")
				require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, orgID).Scan(&beforeCount), "inspect durable admission after notification")
				require.Equal(t, 1, beforeCount, "published work must be durably visible")
				duplicateStore := NewJobStore(b)
				duplicateStore.SetLogger(zerolog.Nop())
				duplicateStore.SetNotifier(cache.NewJobNotifier(client, zerolog.Nop()))
				id, err := duplicateStore.EnqueuePullRequestReconciliationOnce(ctx, orgID, opts)
				require.NoError(t, err, "repeat admission should be absorbed")
				require.Equal(t, uuid.Nil, id, "repeat admission must return a no-op")
				require.Equal(t, baseline+1, server.CommandCount(), "absorbed admission must publish no additional wake-up")
			}
		})
	}
}

func TestReconciliationOncePreservesOrdinaryRetryPostgres(t *testing.T) {
	t.Parallel()
	a, b, orgID := newReconciliationOncePostgres(t)
	ctx := context.Background()
	store := NewJobStore(a)
	opts := reconciliationOnceOpts(orgID, "20260423220")
	id, err := store.EnqueuePullRequestReconciliationOnce(ctx, orgID, opts)
	require.NoError(t, err, "admit the periodic job")
	token := uuid.New()
	retryAt := time.Date(2026, 4, 23, 22, 5, 0, 0, time.UTC)
	_, err = a.Exec(ctx, `UPDATE jobs SET status='running',attempts=1,lock_token=$3 WHERE org_id=$1 AND id=$2`, orgID, id, token)
	require.NoError(t, err, "establish an ordinary running lease")
	owned, err := store.RetryWithLease(ctx, id, token, "transient failure", retryAt)
	require.NoError(t, err, "ordinary fenced retry must remain available")
	require.True(t, owned, "the current lease may retry its admitted job")
	var status string
	var attempts int
	var runAt time.Time
	require.NoError(t, b.QueryRow(ctx, `SELECT status,attempts,run_at FROM jobs WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&status, &attempts, &runAt), "inspect the same retried job")
	require.Equal(t, []any{"pending", 1, retryAt}, []any{status, attempts, runAt.UTC()}, "retry must preserve identity, attempts and the selected backoff")
	_, err = a.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, orgID, id)
	require.NoError(t, err, "finish periodic admission")
	ordinary, err := store.EnqueueWithOpts(ctx, orgID, opts)
	require.NoError(t, err, "ordinary enqueue intentionally retains terminal re-admission semantics")
	require.NotEqual(t, uuid.Nil, ordinary, "the opt-in method must not change ordinary enqueue behavior")
}
