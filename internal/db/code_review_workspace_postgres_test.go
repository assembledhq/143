package db

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func newReviewWorkspaceFixture(t *testing.T) reviewWorkspaceFixture {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for review workspace concurrency proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	schema := "review_workspace_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated review workspace schema")
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err, "parse disposable PostgreSQL settings")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "open review workspace pool")
	t.Cleanup(func() {
		pool.Close()
		_, dropErr := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, dropErr, "drop isolated review workspace schema")
		require.NoError(t, admin.Close(context.Background()), "close disposable PostgreSQL connection")
	})
	_, err = pool.Exec(ctx, `
		CREATE TABLE nodes(id text PRIMARY KEY,status text NOT NULL,last_heartbeat_at timestamptz NOT NULL);
		CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL,origin text NOT NULL,status text NOT NULL,
			revision_context jsonb,container_id text,worker_node_id text,sandbox_state text NOT NULL DEFAULT 'none',
			workspace_generation bigint NOT NULL DEFAULT 0,snapshot_key text,turn_holding_container boolean NOT NULL DEFAULT false);
		CREATE TABLE code_review_session_metadata(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,
			policy_id uuid NOT NULL,repository_id uuid NOT NULL,head_sha text NOT NULL,status text NOT NULL,stale boolean NOT NULL DEFAULT false);
		CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,queue text NOT NULL,
			job_type text NOT NULL,payload jsonb NOT NULL DEFAULT '{}',priority integer NOT NULL DEFAULT 0,
			status text NOT NULL DEFAULT 'pending',max_attempts integer NOT NULL DEFAULT 3,dedupe_key text,target_node_id text,
			locked_by_node_id text,run_owner_id text,owner_kind text,lock_token uuid,locked_at timestamptz,
			lease_expires_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz);
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
	_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata
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
