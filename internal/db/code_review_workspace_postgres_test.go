package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type reviewWorkspaceFixture struct {
	pool    *pgxpool.Pool
	org     uuid.UUID
	review  uuid.UUID
	session uuid.UUID
	policy  uuid.UUID
	repo    uuid.UUID
	job     uuid.UUID
	token   uuid.UUID
	node    string
	params  PublishCodeReviewWorkspaceParams
}

// Bound aggregate fixture connections: four fixtures, each one admin plus five pool connections.
var reviewWorkspaceFixtureSlots = make(chan struct{}, 4)

func newReviewWorkspaceFixture(t *testing.T) reviewWorkspaceFixture {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for review workspace concurrency proof")
	}
	reviewWorkspaceFixtureSlots <- struct{}{}
	t.Cleanup(func() { <-reviewWorkspaceFixtureSlots })
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	t.Cleanup(func() {
		require.NoError(t, admin.Close(context.Background()), "close disposable PostgreSQL connection")
	})
	schema := "review_workspace_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated review workspace schema")
	t.Cleanup(func() {
		_, dropErr := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, dropErr, "drop isolated review workspace schema")
	})
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err, "parse disposable PostgreSQL settings")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 5
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
	})
	require.NoError(t, err, "open review workspace pool")
	_, err = pool.Exec(ctx, `
		CREATE TABLE nodes(id text PRIMARY KEY,status text NOT NULL,last_heartbeat_at timestamptz NOT NULL);
		CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL,origin text NOT NULL,status text NOT NULL,
			revision_context jsonb,container_id text,worker_node_id text,sandbox_state text NOT NULL DEFAULT 'none',
			workspace_generation bigint NOT NULL DEFAULT 0,snapshot_key text,turn_holding_container boolean NOT NULL DEFAULT false);
		CREATE TABLE code_review_session_metadata(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,
			policy_id uuid NOT NULL,repository_id uuid NOT NULL,head_sha text NOT NULL,status text NOT NULL,stale boolean NOT NULL DEFAULT false,review_output_key text NOT NULL DEFAULT 'review-output');
		CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,queue text NOT NULL,
			job_type text NOT NULL,payload jsonb NOT NULL DEFAULT '{}',priority integer NOT NULL DEFAULT 0,
			status text NOT NULL DEFAULT 'pending',max_attempts integer NOT NULL DEFAULT 3,dedupe_key text,target_node_id text,
			locked_by_node_id text,run_owner_id text,owner_kind text,lock_token uuid,locked_at timestamptz,
			lease_expires_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz,run_at timestamptz NOT NULL DEFAULT now(),
			attempts integer NOT NULL DEFAULT 1,last_error text);
		CREATE UNIQUE INDEX idx_workspace_jobs_dedupe ON jobs(queue,dedupe_key)
			WHERE dedupe_key IS NOT NULL AND status IN ('pending','running');
		CREATE TABLE session_sandbox_holders(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,
			session_id uuid NOT NULL,container_id text NOT NULL,holder_kind text NOT NULL,holder_id uuid NOT NULL,
			owner_node_id text NOT NULL,lease_token uuid NOT NULL,status text NOT NULL,heartbeat_at timestamptz NOT NULL,
			expires_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),released_at timestamptz,
			updated_at timestamptz NOT NULL DEFAULT now());
		CREATE UNIQUE INDEX idx_workspace_one_active_holder ON session_sandbox_holders(org_id,session_id,holder_kind,holder_id)
			WHERE status IN ('active','draining');
		CREATE TABLE preview_instances(org_id uuid NOT NULL,session_id uuid NOT NULL,preview_holding_container boolean NOT NULL);
	`)
	require.NoError(t, err, "create review workspace fencing tables")
	f := reviewWorkspaceFixture{pool: pool, org: uuid.New(), review: uuid.New(), session: uuid.New(),
		policy: uuid.New(), repo: uuid.New(), job: uuid.New(), token: uuid.New(), node: "workspace-worker"}
	_, err = pool.Exec(ctx, `INSERT INTO nodes VALUES($1,'active',now())`, f.node)
	require.NoError(t, err, "seed live worker node")
	_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status,revision_context)
		VALUES($1,$2,'code_review','idle','{"head_sha":"head-1"}')`, f.session, f.org)
	require.NoError(t, err, "seed cold review session")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,policy_id,repository_id,head_sha,status,stale)
		VALUES($1,$2,$3,$4,$5,'head-1','running',false)`, f.review, f.org, f.session, f.policy, f.repo)
	require.NoError(t, err, "seed active review metadata")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,status,locked_by_node_id,lock_token,lease_expires_at)
		VALUES($1,$2,'agent','prepare_code_review_workspace','running',$3,$4,now()+interval '5 minutes')`, f.job, f.org, f.node, f.token)
	require.NoError(t, err, "seed leased initializer")
	f.params = PublishCodeReviewWorkspaceParams{
		OrgID: f.org, ReviewID: f.review, SessionID: f.session, PolicyID: f.policy,
		RepositoryID: f.repo, JobID: f.job, JobLockToken: f.token, OwnerNodeID: f.node,
		ContainerID: "container-one", ExpectedHead: "head-1", ExpectedGeneration: 0,
	}
	return f
}

func TestCodeReviewWorkspaceConcurrentPublication(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	store := NewCodeReviewWorkspaceStore(f.pool)
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for _, id := range []string{"container-one", "container-two"} {
		wg.Add(1)
		go func(containerID string) {
			defer wg.Done()
			p := f.params
			p.ContainerID = containerID
			published, err := store.PublishPrepared(context.Background(), f.org, p)
			results <- published
			errs <- err
		}(id)
	}
	wg.Wait()
	close(results)
	close(errs)
	var winners int
	for err := range errs {
		require.NoError(t, err, "concurrent publication should resolve by CAS without a database error")
	}
	for published := range results {
		if published {
			winners++
		}
	}
	require.Equal(t, 1, winners, "exactly one initializer should publish a container")
	var generation, holders int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT workspace_generation FROM sessions WHERE id=$1`, f.session).Scan(&generation), "read published generation")
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM session_sandbox_holders WHERE status='active'`).Scan(&holders), "count active review holders")
	require.Equal(t, 1, generation, "one publication should advance the workspace generation once")
	require.Equal(t, 1, holders, "one publication should create exactly one live review holder")
}

func TestCodeReviewWorkspaceLeaseLossAndCrashRecovery(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	store := NewCodeReviewWorkspaceStore(f.pool)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, f.job)
	require.NoError(t, err, "simulate initializer crash and expired lease")
	published, err := store.PublishPrepared(ctx, f.org, f.params)
	require.NoError(t, err, "expired initializer should be fenced without a database error")
	require.False(t, published, "expired initializer must not publish its unpublished container")
	newJob, newToken := uuid.New(), uuid.New()
	_, err = f.pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,status,locked_by_node_id,lock_token,lease_expires_at)
		VALUES($1,$2,'agent','prepare_code_review_workspace','running',$3,$4,now()+interval '5 minutes')`, newJob, f.org, f.node, newToken)
	require.NoError(t, err, "seed replacement initializer after crash")
	p := f.params
	p.JobID, p.JobLockToken, p.ContainerID = newJob, newToken, "replacement"
	published, err = store.PublishPrepared(ctx, f.org, p)
	require.NoError(t, err, "replacement initializer should publish under its live lease")
	require.True(t, published, "replacement initializer should recover the cold review")
	ready, err := store.Readiness(ctx, f.org, f.review, f.session, "head-1")
	require.NoError(t, err, "read recovered workspace")
	require.Equal(t, CodeReviewWorkspaceReadiness{ContainerID: "replacement", OwnerNodeID: f.node, Generation: 1, Ready: true}, ready, "readiness must identify the replacement container")
}

func TestCodeReviewWorkspaceCancellationBeforeReadiness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		update string
	}{
		{name: "review cancelled", update: `UPDATE code_review_session_metadata SET status='cancelled'`},
		{name: "parent cancelled", update: `UPDATE sessions SET status='cancelled'`},
		{name: "head changed", update: `UPDATE code_review_session_metadata SET head_sha='head-2'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			_, err := f.pool.Exec(context.Background(), tt.update)
			require.NoError(t, err, "change authoritative review state before publication")
			published, err := NewCodeReviewWorkspaceStore(f.pool).PublishPrepared(context.Background(), f.org, f.params)
			require.NoError(t, err, "stale initializer should be rejected without a database error")
			require.False(t, published, "cancelled or changed review must not publish readiness")
			ready, err := NewCodeReviewWorkspaceStore(f.pool).Readiness(context.Background(), f.org, f.review, f.session, "head-1")
			require.NoError(t, err, "check readiness after cancellation")
			require.False(t, ready.Ready, "review must remain unready after cancellation")
		})
	}
}

func TestCodeReviewWorkspaceMissingContainerRecovery(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	ctx := context.Background()
	store := NewCodeReviewWorkspaceStore(f.pool)
	published, err := store.PublishPrepared(ctx, f.org, f.params)
	require.NoError(t, err, "publish first prepared container")
	require.True(t, published, "first prepared container should win")
	_, err = f.pool.Exec(ctx, `UPDATE session_sandbox_holders SET expires_at=now()-interval '1 second'`)
	require.NoError(t, err, "expire idle review holder before an owner liveness check")
	rearmParams := f.params
	rearmParams.ExpectedGeneration = 1
	rearmed, err := store.RearmExisting(ctx, f.org, rearmParams)
	require.NoError(t, err, "live owner should rearm an expired holder")
	require.True(t, rearmed, "a verified live container should regain its review holder")
	cleared, err := store.ReconcileMissing(ctx, f.org, f.review, f.session, "container-one", f.node, f.node)
	require.NoError(t, err, "owner should reconcile a physically missing container")
	require.True(t, cleared, "missing container should be cleared before replacement")
	var releasedAt time.Time
	err = f.pool.QueryRow(ctx, `SELECT released_at FROM session_sandbox_holders
		WHERE org_id=$1 AND session_id=$2 AND container_id='container-one' AND status='expired'`, f.org, f.session).Scan(&releasedAt)
	require.NoError(t, err, "reconciled holder should record when its container was released")
	require.False(t, releasedAt.IsZero(), "expired holder should retain its release timestamp")
	newJob, newToken := uuid.New(), uuid.New()
	_, err = f.pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,status,locked_by_node_id,lock_token,lease_expires_at)
		VALUES($1,$2,'agent','prepare_code_review_workspace','running',$3,$4,now()+interval '5 minutes')`, newJob, f.org, f.node, newToken)
	require.NoError(t, err, "seed recovery initializer")
	p := f.params
	p.JobID, p.JobLockToken, p.ContainerID, p.ExpectedGeneration = newJob, newToken, "container-two", 1
	published, err = store.PublishPrepared(ctx, f.org, p)
	require.NoError(t, err, "publish replacement container")
	require.True(t, published, "recovery initializer should publish at the new generation")
	ready, err := store.Readiness(ctx, f.org, f.review, f.session, "head-1")
	require.NoError(t, err, "read replacement workspace")
	require.Equal(t, CodeReviewWorkspaceReadiness{ContainerID: "container-two", OwnerNodeID: f.node, Generation: 2, Ready: true}, ready, "readiness must move to the replacement container")
}

func TestCodeReviewWorkspaceRemoteRecoveryRequiresDeadOwner(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	ctx := context.Background()
	store := NewCodeReviewWorkspaceStore(f.pool)
	published, err := store.PublishPrepared(ctx, f.org, f.params)
	require.NoError(t, err, "publish owner workspace before recovery")
	require.True(t, published, "owner should publish one workspace")
	cleared, err := store.ReconcileMissing(ctx, f.org, f.review, f.session, "container-one", f.node, "other-worker")
	require.NoError(t, err, "remote recovery should be refused without a database error")
	require.False(t, cleared, "a live remote owner's container must not be cleared by another worker")
	_, err = f.pool.Exec(ctx, `UPDATE nodes SET status='dead' WHERE id=$1`, f.node)
	require.NoError(t, err, "mark the owner definitively dead")
	cleared, err = store.ReconcileMissing(ctx, f.org, f.review, f.session, "container-one", f.node, "other-worker")
	require.NoError(t, err, "recover workspace after owner death")
	require.True(t, cleared, "a dead owner's container reference may be cleared for replacement")
}

func TestCodeReviewWorkspaceJobDedupe(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	ctx := context.Background()
	key := "review-prepare:" + f.review.String() + ":0"
	store := NewJobStore(f.pool)
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := store.EnqueueWithOpts(ctx, f.org, EnqueueOpts{
				Queue: "agent", JobType: models.JobTypePrepareCodeReviewWorkspace,
				Payload: map[string]any{"session_id": f.session}, DedupeKey: &key,
			})
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	var created int
	for err := range errs {
		require.NoError(t, err, "parallel enqueue should not fail")
	}
	for id := range ids {
		if id != uuid.Nil {
			created++
		}
	}
	require.Equal(t, 1, created, "one active preparation job should own the dedupe key")
}

func TestCodeReviewWorkspaceWaitWindowSurvivesJobReplacement(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	ctx := context.Background()
	key := "code_review_prepare:" + f.review.String() + ":" + f.session.String() + ":0"
	_, err := f.pool.Exec(ctx, `UPDATE jobs SET dedupe_key=$1,status='failed',created_at=now()-interval '10 minutes' WHERE id=$2`, key, f.job)
	require.NoError(t, err, "record the first failed preparation attempt")
	_, err = f.pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,dedupe_key,payload)
		VALUES($1,'agent','prepare_code_review_workspace',$2,$3)`, f.org, key, map[string]any{"session_id": f.session.String()})
	require.NoError(t, err, "enqueue a replacement preparation without resetting the phase")
	startedAt, err := NewJobStore(f.pool).FirstJobCreatedAtByDedupeKey(ctx, f.org, "agent", key)
	require.NoError(t, err, "load the durable start of the workspace wait")
	require.Greater(t, time.Since(startedAt), 9*time.Minute, "replacement job must retain the first wait deadline")
	_, err = NewJobStore(f.pool).FirstJobCreatedAtByDedupeKey(ctx, uuid.New(), "agent", key)
	require.Error(t, err, "another organization must not see this review's preparation jobs")
}

func TestCodeReviewWorkspaceTimedOutPreparationFencesPublication(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	ctx := context.Background()
	key := "code_review_prepare:" + f.review.String() + ":" + f.session.String() + ":0"
	_, err := f.pool.Exec(ctx, `UPDATE jobs SET dedupe_key=$1 WHERE id=$2`, key, f.job)
	require.NoError(t, err, "associate the live initializer with its review generation")
	jobs := NewJobStore(f.pool)
	cancelled, err := jobs.CancelActiveCodeReviewPreparation(ctx, uuid.New(), "agent", key)
	require.NoError(t, err, "cancelling another organization's preparation should be a no-op")
	require.Equal(t, int64(0), cancelled, "another organization must not cancel this preparation")
	cancelled, err = jobs.CancelActiveCodeReviewPreparation(ctx, f.org, "agent", key)
	require.NoError(t, err, "cancel the timed-out live initializer")
	require.Equal(t, int64(1), cancelled, "the exact live preparation should be cancelled")
	status, err := jobs.LatestJobStatusByDedupeKey(ctx, f.org, "agent", key)
	require.NoError(t, err, "read the terminal preparation status")
	require.Equal(t, models.JobStatusCancelled, status, "the cancelled preparation should release its queue lease")
	published, err := NewCodeReviewWorkspaceStore(f.pool).PublishPrepared(ctx, f.org, f.params)
	require.NoError(t, err, "publication after cancellation should fail by fencing, not a database error")
	require.False(t, published, "a cancelled initializer must not publish after reviewer fallback")
}

func TestActiveCodeReviewPreparationProtectsOnlyLiveLease(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `UPDATE jobs SET payload=$1 WHERE id=$2`, map[string]any{"session_id": f.session.String()}, f.job)
	require.NoError(t, err, "attach the preparation session identity")
	refs, err := NewSessionStore(f.pool).ListActiveCodeReviewPreparations(ctx)
	require.NoError(t, err, "list live preparation leases for host GC")
	require.Equal(t, []string{f.token.String()}, refs, "the active attempt should protect only its own unpublished container")
	_, err = f.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, f.job)
	require.NoError(t, err, "expire the preparation lease")
	refs, err = NewSessionStore(f.pool).ListActiveCodeReviewPreparations(ctx)
	require.NoError(t, err, "list preparations after lease expiry")
	require.Empty(t, refs, "GC may reclaim an unpublished container after its publish lease expires")
}

func seedReviewWorkspaceController(t *testing.T, f reviewWorkspaceFixture) RetryCodeReviewWorkspaceControllerParams {
	t.Helper()
	p := RetryCodeReviewWorkspaceControllerParams{
		OrgID: f.org, ReviewID: f.review, SessionID: f.session, JobID: uuid.New(), JobLockToken: uuid.New(),
		ExpectedHead: "head-1", RunAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond),
		ErrorMessage: CodeReviewWorkspaceWaitErrorPrefix + " preparation pending",
	}
	payload := map[string]any{"org_id": f.org, "metadata_id": f.review, "session_id": f.session,
		"policy_id": f.policy, "repository_id": f.repo, "head_sha": "head-1", "review_output_key": "review-output"}
	_, err := f.pool.Exec(context.Background(), `INSERT INTO jobs(id,org_id,queue,job_type,status,payload,
		locked_by_node_id,lock_token,lease_expires_at,run_at,attempts,last_error)
		VALUES($1,$2,'agent','run_code_review','running',$3,$4,$5,clock_timestamp()+interval '5 minutes',$6,2,$7)`,
		p.JobID, f.org, payload, f.node, p.JobLockToken, p.RunAt, p.ErrorMessage)
	require.NoError(t, err, "seed the exact current review controller lease")
	return p
}

type reviewWorkspaceControllerState struct {
	Status    string
	RunAt     time.Time
	Token     *uuid.UUID
	Owner     *string
	Attempts  int
	LastError *string
}

func readReviewWorkspaceController(t *testing.T, f reviewWorkspaceFixture, id uuid.UUID) reviewWorkspaceControllerState {
	t.Helper()
	var state reviewWorkspaceControllerState
	err := f.pool.QueryRow(context.Background(), `SELECT status,run_at,lock_token,locked_by_node_id,attempts,last_error
		FROM jobs WHERE org_id=$1 AND id=$2`, f.org, id).Scan(&state.Status, &state.RunAt, &state.Token, &state.Owner, &state.Attempts, &state.LastError)
	require.NoError(t, err, "read controller scheduling and lease state")
	return state
}

// The wrapper pauses after PostgreSQL actually owns the advisory lock. It does
// not simulate readiness or mutate SQL; both competing paths run real stores.
type reviewWorkspaceGatedDB struct {
	TxStarter
	started  chan uint32
	acquired chan struct{}
	release  <-chan struct{}
}

func (d reviewWorkspaceGatedDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if d.started != nil {
		d.started <- tx.Conn().PgConn().PID()
	}
	if d.release == nil {
		return tx, nil
	}
	return &reviewWorkspaceGatedTx{Tx: tx, acquired: d.acquired, release: d.release}, nil
}

type reviewWorkspaceGatedTx struct {
	pgx.Tx
	acquired chan struct{}
	release  <-chan struct{}
}

func (tx *reviewWorkspaceGatedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil && strings.Contains(sql, "pg_advisory_xact_lock") {
		close(tx.acquired)
		select {
		case <-tx.release:
		case <-ctx.Done():
			return tag, ctx.Err()
		}
	}
	return tag, err
}

func TestCodeReviewWorkspaceCompletionRetryRace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		publishFirst bool
		rearm        bool
	}{
		{name: "publication commits before pending retry", publishFirst: true},
		{name: "pending retry commits before publication"},
		{name: "rearm commits before pending retry", publishFirst: true, rearm: true},
		{name: "pending retry commits before rearm", rearm: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p := seedReviewWorkspaceController(t, f)
			f.params.ControllerJobID = p.JobID
			if tt.rearm {
				published, err := NewCodeReviewWorkspaceStore(f.pool).PublishPrepared(ctx, f.org, f.params)
				require.NoError(t, err, "prepare prior workspace before expiring its holder")
				require.True(t, published, "prior workspace should be recorded")
				_, err = f.pool.Exec(ctx, `UPDATE session_sandbox_holders SET expires_at=clock_timestamp()-interval '1 second'`)
				require.NoError(t, err, "expire the prior holder so readiness requires rearm")
				f.params.ExpectedGeneration = 1
			}
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			acquired := make(chan struct{})
			started := make(chan uint32, 1)
			first := NewCodeReviewWorkspaceStore(reviewWorkspaceGatedDB{TxStarter: f.pool, acquired: acquired, release: release})
			second := NewCodeReviewWorkspaceStore(reviewWorkspaceGatedDB{TxStarter: f.pool, started: started})
			publish := func(s *CodeReviewWorkspaceStore) error {
				var won bool
				var err error
				if tt.rearm {
					won, err = s.RearmExisting(ctx, f.org, f.params)
				} else {
					won, err = s.PublishPrepared(ctx, f.org, f.params)
				}
				if err == nil && !won {
					return fmt.Errorf("valid preparation failed to publish")
				}
				return err
			}
			retry := func(s *CodeReviewWorkspaceStore) error {
				_, ok, err := s.RetryControllerWithLease(ctx, f.org, p)
				if err == nil && !ok {
					return fmt.Errorf("valid controller failed retry fencing")
				}
				return err
			}
			firstOp, secondOp := retry, publish
			if tt.publishFirst {
				firstOp, secondOp = publish, retry
			}
			firstResult, secondResult := make(chan error, 1), make(chan error, 1)
			go func() { firstResult <- firstOp(first) }()
			select {
			case <-acquired:
			case <-ctx.Done():
				t.Fatal("first transition did not acquire advisory lock")
			}
			go func() { secondResult <- secondOp(second) }()
			var pid uint32
			select {
			case pid = <-started:
			case <-ctx.Done():
				t.Fatal("competing transition did not begin")
			}
			require.Eventually(t, func() bool {
				var waiting bool
				err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, pid).Scan(&waiting)
				return err == nil && waiting
			}, 2*time.Second, 5*time.Millisecond, "the competing real transaction must wait on the same readiness lock")
			unblock()
			require.NoError(t, <-firstResult, "first readiness transition should commit")
			require.NoError(t, <-secondResult, "second readiness transition should observe the committed winner")
			state := readReviewWorkspaceController(t, f, p.JobID)
			require.Equal(t, "pending", state.Status, "completion should leave the same controller pending")
			require.Nil(t, state.Token, "only the controller retry should release its lease")
			require.Nil(t, state.Owner, "controller retry should clear its worker owner")
			require.Equal(t, 1, state.Attempts, "dependency wait should refund exactly one attempt")
			require.WithinDuration(t, time.Now(), state.RunAt, 3*time.Second, "both race orderings must make the existing controller runnable immediately")
			var controllers int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review'`, f.org).Scan(&controllers), "count exact existing controller jobs")
			require.Equal(t, 1, controllers, "completion must never enqueue a replacement controller")
		})
	}
}

func TestCodeReviewWorkspacePublicationWakeFences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*testing.T, reviewWorkspaceFixture, RetryCodeReviewWorkspaceControllerParams)
		noParent bool
		wake     bool
	}{
		{name: "pending workspace wait wakes", wake: true},
		{name: "running controller lease remains intact", mutate: func(t *testing.T, f reviewWorkspaceFixture, p RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET status='running' WHERE id=$1`, p.JobID)
		}},
		{name: "completed controller remains terminal", mutate: func(t *testing.T, f reviewWorkspaceFixture, p RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET status='succeeded' WHERE id=$1`, p.JobID)
		}},
		{name: "late publication preserves GitHub backoff", mutate: func(t *testing.T, f reviewWorkspaceFixture, p RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET last_error='GitHub provider rate limit' WHERE id=$1`, p.JobID)
		}},
		{name: "legacy payload has no parent", noParent: true},
		{name: "wrong parent tenant", mutate: func(t *testing.T, f reviewWorkspaceFixture, p RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET payload=jsonb_set(payload,'{org_id}',to_jsonb($2::text)) WHERE id=$1`, p.JobID, uuid.New().String())
		}},
		{name: "wrong parent head", mutate: func(t *testing.T, f reviewWorkspaceFixture, p RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET payload=jsonb_set(payload,'{head_sha}','"other-head"') WHERE id=$1`, p.JobID)
		}},
		{name: "wrong parent review output", mutate: func(t *testing.T, f reviewWorkspaceFixture, p RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET payload=jsonb_set(payload,'{review_output_key}','"other-output"') WHERE id=$1`, p.JobID)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			p := seedReviewWorkspaceController(t, f)
			workspaceWakeExec(t, f, `UPDATE jobs SET status='pending' WHERE id=$1`, p.JobID)
			if tt.mutate != nil {
				tt.mutate(t, f, p)
			}
			if !tt.noParent {
				f.params.ControllerJobID = p.JobID
			}
			before := readReviewWorkspaceController(t, f, p.JobID)
			store := NewCodeReviewWorkspaceStore(f.pool)
			published, err := store.PublishPrepared(context.Background(), f.org, f.params)
			require.NoError(t, err, "valid publication should not depend on whether a controller can wake")
			require.True(t, published, "valid workspace should publish once")
			after := readReviewWorkspaceController(t, f, p.JobID)
			if tt.wake {
				require.WithinDuration(t, time.Now(), after.RunAt, 3*time.Second, "pending workspace wait should wake immediately")
				before.RunAt = after.RunAt
			}
			require.Equal(t, before, after, "publication must preserve controller identity, attempts, lease, and unrelated scheduling")
			published, err = store.PublishPrepared(context.Background(), f.org, f.params)
			require.NoError(t, err, "duplicate publication should be a fenced no-op")
			require.False(t, published, "duplicate publication cannot advance generation or holder twice")
			require.Equal(t, after, readReviewWorkspaceController(t, f, p.JobID), "duplicate publication cannot reschedule or alter a controller")
		})
	}
}

func workspaceWakeExec(t *testing.T, f reviewWorkspaceFixture, query string, args ...any) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), query, args...)
	require.NoError(t, err, "update isolated workspace wake fixture")
}

func TestCodeReviewWorkspaceControllerRetryFences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mutate      func(*testing.T, reviewWorkspaceFixture, *RetryCodeReviewWorkspaceControllerParams)
		wantErr     bool
		normalRetry bool
	}{
		{name: "stale fencing token", mutate: func(_ *testing.T, _ reviewWorkspaceFixture, p *RetryCodeReviewWorkspaceControllerParams) {
			p.JobLockToken = uuid.New()
		}},
		{name: "wrong tenant", mutate: func(_ *testing.T, _ reviewWorkspaceFixture, p *RetryCodeReviewWorkspaceControllerParams) {
			p.OrgID = uuid.New()
		}, wantErr: true},
		{name: "missing parent", mutate: func(_ *testing.T, _ reviewWorkspaceFixture, p *RetryCodeReviewWorkspaceControllerParams) {
			p.JobID = uuid.Nil
		}, wantErr: true},
		{name: "expired current lease", mutate: func(t *testing.T, f reviewWorkspaceFixture, p *RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, p.JobID)
		}},
		{name: "cancelled review", normalRetry: true, mutate: func(t *testing.T, f reviewWorkspaceFixture, _ *RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE code_review_session_metadata SET status='cancelled'`)
		}},
		{name: "cancelled session", normalRetry: true, mutate: func(t *testing.T, f reviewWorkspaceFixture, _ *RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE sessions SET status='cancelled'`)
		}},
		{name: "changed head", normalRetry: true, mutate: func(t *testing.T, f reviewWorkspaceFixture, _ *RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE code_review_session_metadata SET head_sha='other-head'`)
		}},
		{name: "wrong payload session", mutate: func(t *testing.T, f reviewWorkspaceFixture, p *RetryCodeReviewWorkspaceControllerParams) {
			workspaceWakeExec(t, f, `UPDATE jobs SET payload=jsonb_set(payload,'{session_id}',to_jsonb($2::text)) WHERE id=$1`, p.JobID, uuid.New().String())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			p := seedReviewWorkspaceController(t, f)
			id := p.JobID
			tt.mutate(t, f, &p)
			before := readReviewWorkspaceController(t, f, id)
			runAt, retried, err := NewCodeReviewWorkspaceStore(f.pool).RetryControllerWithLease(context.Background(), f.org, p)
			if tt.wantErr {
				require.Error(t, err, "invalid request identity must fail before changing scheduling")
			} else {
				require.NoError(t, err, "stale authoritative state should fence retry without a database error")
			}
			if tt.normalRetry {
				require.True(t, retried, "owned controller must requeue to settle terminal or changed review state promptly")
				require.Equal(t, p.RunAt, runAt.UTC(), "terminal or changed review must retain ordinary dependency retry time")
				after := readReviewWorkspaceController(t, f, id)
				require.Equal(t, "pending", after.Status, "owned controller should remain schedulable for terminal settlement")
				require.Nil(t, after.Token, "owned controller retry must release its lease")
				return
			}
			require.False(t, retried, "invalid or stale controller cannot release the current lease")
			require.Equal(t, before, readReviewWorkspaceController(t, f, id), "fenced retry must preserve the exact controller state")
		})
	}
}

func TestCodeReviewWorkspaceWaitUsesCurrentTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		transition  string
		expiry      string
		normalRetry bool
	}{
		{name: "preparation lease expires while publication waits", transition: "publish", expiry: "preparation"},
		{name: "owner heartbeat expires while publication waits", transition: "publish", expiry: "node"},
		{name: "preparation lease expires while rearm waits", transition: "rearm", expiry: "preparation"},
		{name: "controller lease expires while retry waits", transition: "retry", expiry: "controller"},
		{name: "holder expires while controller retry waits", transition: "retry", expiry: "holder", normalRetry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			p := seedReviewWorkspaceController(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			f.params.ControllerJobID = p.JobID
			if tt.transition == "rearm" || tt.expiry == "holder" {
				published, err := NewCodeReviewWorkspaceStore(f.pool).PublishPrepared(ctx, f.org, f.params)
				require.NoError(t, err, "publish an initial container before time fencing proof")
				require.True(t, published, "initial workspace must be ready")
				f.params.ExpectedGeneration = 1
				if tt.transition == "rearm" {
					workspaceWakeExec(t, f, `UPDATE session_sandbox_holders SET expires_at=clock_timestamp()-interval '1 second'`)
				}
			}
			before := readReviewWorkspaceController(t, f, p.JobID)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			acquired := make(chan struct{})
			store := NewCodeReviewWorkspaceStore(reviewWorkspaceGatedDB{TxStarter: f.pool, acquired: acquired, release: release})
			type result struct {
				ok    bool
				err   error
				runAt time.Time
			}
			done := make(chan result, 1)
			go func() {
				var r result
				switch tt.transition {
				case "publish":
					r.ok, r.err = store.PublishPrepared(ctx, f.org, f.params)
				case "rearm":
					r.ok, r.err = store.RearmExisting(ctx, f.org, f.params)
				case "retry":
					r.runAt, r.ok, r.err = store.RetryControllerWithLease(ctx, f.org, p)
				}
				done <- r
			}()
			select {
			case <-acquired:
			case <-ctx.Done():
				t.Fatal("transition never reached its actual database lock")
			}
			var deadline time.Time
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT clock_timestamp()+interval '150 milliseconds'`).Scan(&deadline), "set a wall-clock deadline later than transaction start")
			switch tt.expiry {
			case "preparation":
				workspaceWakeExec(t, f, `UPDATE jobs SET lease_expires_at=$2 WHERE id=$1`, f.job, deadline)
			case "controller":
				workspaceWakeExec(t, f, `UPDATE jobs SET lease_expires_at=$2 WHERE id=$1`, p.JobID, deadline)
			case "node":
				workspaceWakeExec(t, f, `UPDATE nodes SET last_heartbeat_at=$2::timestamptz-interval '90 seconds' WHERE id=$1`, f.node, deadline)
			case "holder":
				workspaceWakeExec(t, f, `UPDATE session_sandbox_holders SET expires_at=$1`, deadline)
			}
			require.Eventually(t, func() bool {
				var expired bool
				err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()>$1`, deadline).Scan(&expired)
				return err == nil && expired
			}, time.Second, 5*time.Millisecond, "wall-clock lease must expire while transition transaction is already open")
			unblock()
			r := <-done
			require.NoError(t, r.err, "expired ownership should fail by fencing rather than a database error")
			if tt.normalRetry {
				require.True(t, r.ok, "an owned controller with expired readiness should retain its normal retry")
				require.Equal(t, p.RunAt, r.runAt.UTC(), "expired holder must never shorten the retry using transaction-start time")
			} else {
				require.False(t, r.ok, "a lease or node that expired during a lock wait must not authorize publication or retry")
				require.Equal(t, before, readReviewWorkspaceController(t, f, p.JobID), "failed completion must preserve parent scheduling and lease identity")
			}
		})
	}
}

func TestCodeReviewWorkspaceStaleGenerationCannotWake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		rearm bool
	}{
		{name: "new publication generation mismatch"},
		{name: "existing holder generation mismatch", rearm: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			p := seedReviewWorkspaceController(t, f)
			store := NewCodeReviewWorkspaceStore(f.pool)
			if tt.rearm {
				published, err := store.PublishPrepared(context.Background(), f.org, f.params)
				require.NoError(t, err, "publish initial generation")
				require.True(t, published, "initial generation should publish")
				workspaceWakeExec(t, f, `UPDATE session_sandbox_holders SET expires_at=clock_timestamp()-interval '1 second'`)
			}
			workspaceWakeExec(t, f, `UPDATE sessions SET workspace_generation=workspace_generation+1`)
			workspaceWakeExec(t, f, `UPDATE jobs SET status='pending' WHERE id=$1`, p.JobID)
			f.params.ControllerJobID = p.JobID
			before := readReviewWorkspaceController(t, f, p.JobID)
			var published bool
			var err error
			if tt.rearm {
				published, err = store.RearmExisting(context.Background(), f.org, f.params)
			} else {
				published, err = store.PublishPrepared(context.Background(), f.org, f.params)
			}
			require.NoError(t, err, "generation mismatch should fence without a database error")
			require.False(t, published, "stale preparation generation cannot publish or rearm")
			require.Equal(t, before, readReviewWorkspaceController(t, f, p.JobID), "stale generation must never wake its controller")
		})
	}
}

func TestCodeReviewWorkspaceReadyRetryRequiresCurrentParentMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		update string
	}{
		{name: "policy changed", update: `UPDATE code_review_session_metadata SET policy_id=gen_random_uuid()`},
		{name: "repository changed", update: `UPDATE code_review_session_metadata SET repository_id=gen_random_uuid()`},
		{name: "output identity changed", update: `UPDATE code_review_session_metadata SET review_output_key='replacement-output'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			p := seedReviewWorkspaceController(t, f)
			store := NewCodeReviewWorkspaceStore(f.pool)
			published, err := store.PublishPrepared(context.Background(), f.org, f.params)
			require.NoError(t, err, "publish ready workspace before changing current identity")
			require.True(t, published, "workspace should initially be ready")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			blocker, err := f.pool.Begin(ctx)
			require.NoError(t, err, "begin a competing owner of the controller row")
			defer func() { _ = blocker.Rollback(context.Background()) }()
			_, err = blocker.Exec(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR UPDATE`, f.org, p.JobID)
			require.NoError(t, err, "hold controller row while retry starts its statement snapshot")
			started := make(chan uint32, 1)
			retryStore := NewCodeReviewWorkspaceStore(reviewWorkspaceGatedDB{TxStarter: f.pool, started: started})
			type result struct {
				runAt time.Time
				ok    bool
				err   error
			}
			done := make(chan result, 1)
			go func() {
				runAt, ok, err := retryStore.RetryControllerWithLease(ctx, f.org, p)
				done <- result{runAt: runAt, ok: ok, err: err}
			}()
			var pid uint32
			select {
			case pid = <-started:
			case r := <-done:
				require.NoError(t, r.err, "transition must begin before the row-lock proof")
				t.Fatal("transition completed before exposing its database backend")
			case <-ctx.Done():
				t.Fatal("transition did not begin before the row-lock proof deadline")
			}
			require.Eventually(t, func() bool {
				var waiting bool
				err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&waiting)
				return err == nil && waiting
			}, time.Second, 5*time.Millisecond, "controller retry must block after beginning its first row-lock statement")
			workspaceWakeExec(t, f, tt.update)
			require.NoError(t, blocker.Commit(ctx), "release controller row after authoritative metadata changed")
			r := <-done
			require.NoError(t, r.err, "still-owned controller can requeue after metadata change")
			require.True(t, r.ok, "current controller lease should be released for ordinary retry")
			require.Equal(t, p.RunAt, r.runAt.UTC(), "identity matching must use the fresh readiness snapshot after row wait")
		})
	}
}

func TestCodeReviewWorkspacePublicationRechecksOwnershipAfterParentLock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		rearm      bool
		nodeExpiry bool
	}{
		{name: "publish lease expires behind parent row lock"},
		{name: "publish owner heartbeat expires behind parent row lock", nodeExpiry: true},
		{name: "rearm lease expires behind parent row lock", rearm: true},
		{name: "rearm owner heartbeat expires behind parent row lock", rearm: true, nodeExpiry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			p := seedReviewWorkspaceController(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			if tt.rearm {
				ok, err := NewCodeReviewWorkspaceStore(f.pool).PublishPrepared(ctx, f.org, f.params)
				require.NoError(t, err, "record existing workspace before testing rearm lock wait")
				require.True(t, ok, "initial workspace should publish")
				f.params.ExpectedGeneration = 1
				workspaceWakeExec(t, f, `UPDATE session_sandbox_holders SET expires_at=clock_timestamp()-interval '1 second'`)
			}
			f.params.ControllerJobID = p.JobID
			workspaceWakeExec(t, f, `UPDATE jobs SET status='pending' WHERE id=$1`, p.JobID)
			before := readReviewWorkspaceController(t, f, p.JobID)
			blocker, err := f.pool.Begin(ctx)
			require.NoError(t, err, "begin independent transaction owning the pending parent row")
			defer func() { _ = blocker.Rollback(context.Background()) }()
			_, err = blocker.Exec(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR UPDATE`, f.org, p.JobID)
			require.NoError(t, err, "hold the parent row before preparation tries to nudge it")
			var deadline time.Time
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT clock_timestamp()+interval '5 seconds'`).Scan(&deadline), "choose ownership expiry later than transaction start")
			if tt.nodeExpiry {
				workspaceWakeExec(t, f, `UPDATE nodes SET last_heartbeat_at=$2::timestamptz-interval '90 seconds' WHERE id=$1`, f.node, deadline)
			} else {
				workspaceWakeExec(t, f, `UPDATE jobs SET lease_expires_at=$2 WHERE id=$1`, f.job, deadline)
			}
			started := make(chan uint32, 1)
			store := NewCodeReviewWorkspaceStore(reviewWorkspaceGatedDB{TxStarter: f.pool, started: started})
			type result struct {
				ok  bool
				err error
			}
			done := make(chan result, 1)
			go func() {
				var r result
				if tt.rearm {
					r.ok, r.err = store.RearmExisting(ctx, f.org, f.params)
				} else {
					r.ok, r.err = store.PublishPrepared(ctx, f.org, f.params)
				}
				done <- r
			}()
			var pid uint32
			select {
			case pid = <-started:
			case r := <-done:
				require.NoError(t, r.err, "transition must begin before the row-lock proof")
				t.Fatal("transition completed before exposing its database backend")
			case <-ctx.Done():
				t.Fatal("transition did not begin before the row-lock proof deadline")
			}
			require.Eventually(t, func() bool {
				var waiting bool
				err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&waiting)
				return err == nil && waiting
			}, 3*time.Second, 5*time.Millisecond, "publication must really wait on the parent row after validating its preparation lease")
			require.Eventually(t, func() bool {
				var expired bool
				err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()>$1`, deadline).Scan(&expired)
				return err == nil && expired
			}, 6*time.Second, 10*time.Millisecond, "ownership must expire during the real parent row lock wait")
			require.NoError(t, blocker.Commit(ctx), "release parent row after ownership has expired")
			r := <-done
			require.NoError(t, r.err, "ownership expiry should roll back publication without a database error")
			require.False(t, r.ok, "publication must fail its final ownership check after nudge lock waiting")
			require.Equal(t, before, readReviewWorkspaceController(t, f, p.JobID), "rolled-back publication must not shorten the parent retry")
			ready, err := NewCodeReviewWorkspaceStore(f.pool).Readiness(ctx, f.org, f.review, f.session, "head-1")
			require.NoError(t, err, "check readiness after the fenced transaction rolls back")
			require.False(t, ready.Ready, "failed preparation ownership must not leave a newly ready holder")
		})
	}
}
