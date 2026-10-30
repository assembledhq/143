package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestJobStatusNonterminalDedupePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   models.JobStatus
		consumer bool
	}{
		{name: "pending owns key", status: models.JobStatusPending, consumer: true},
		{name: "running owns key", status: models.JobStatusRunning, consumer: true},
		{name: "parked owns key without lease", status: models.JobStatusAwaitingStartControl, consumer: true},
		{name: "succeeded releases key", status: models.JobStatusSucceeded},
		{name: "failed releases key", status: models.JobStatusFailed},
		{name: "cancelled releases key", status: models.JobStatusCancelled},
		{name: "dead letter releases key", status: models.JobStatusDeadLetter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			ctx := context.Background()
			orgID, jobID := seedLifecycleStatusJob(t, pool, tt.status)
			key := "synthetic:" + jobID.String()
			before := lifecycleStatusJobSnapshot(t, pool, orgID, jobID)
			store := NewJobStore(pool)
			actual, err := store.GetActiveByDedupeKey(ctx, orgID, "agent", key)
			if tt.consumer {
				require.NoError(t, err, "nonterminal input ownership must remain visible independently of execution leases")
				require.Equal(t, ActiveJobRef{ID: jobID, Status: tt.status}, actual, "the lookup must identify the exact persisted consumer")
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows, "terminal history must not occupy an active input key")
				require.Equal(t, ActiveJobRef{}, actual, "terminal lookup must not expose an adopted job")
			}
			has, err := store.HasActiveByDedupeKey(ctx, orgID, "agent", key)
			require.NoError(t, err, "the existence lookup must use the same nonterminal policy")
			require.Equal(t, tt.consumer, has, "the existence and exact-reference lookups must agree")
			newID, err := store.Enqueue(ctx, orgID, "agent", "synthetic_status_probe", map[string]string{"input": "new"}, 0, &key)
			require.NoError(t, err, "legacy enqueue keeps its existing nil-ID collision contract")
			require.Equal(t, tt.consumer, newID == uuid.Nil, "only a nonterminal consumer may collide on this exact global queue/key")
			otherQueueID, err := store.Enqueue(ctx, orgID, "synthetic_other_queue", "synthetic_status_probe", nil, 0, &key)
			require.NoError(t, err, "a different queue has an independent dedupe namespace")
			require.NotEqual(t, uuid.Nil, otherQueueID, "the same key in another queue must not adopt this consumer")
			foreignOrg := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic foreign status tenant')`, foreignOrg)
			require.NoError(t, err, "create a separate tenant for global collision isolation")
			foreignID, err := store.Enqueue(ctx, foreignOrg, "agent", "synthetic_status_probe", nil, 0, &key)
			require.NoError(t, err, "the global queue/key scope must remain unchanged across tenants")
			require.Equal(t, uuid.Nil, foreignID, "a foreign global collision is still not successful input binding")
			foreign, err := store.GetActiveByDedupeKey(ctx, foreignOrg, "agent", key)
			require.ErrorIs(t, err, pgx.ErrNoRows, "a collision must never leak or adopt another tenant's consumer")
			require.Equal(t, ActiveJobRef{}, foreign, "a foreign collision must expose no job reference")
			require.Equal(t, before, lifecycleStatusJobSnapshot(t, pool, orgID, jobID), "lookup and collision handling must never append input or mutate the original job")
		})
	}
}

func TestJobStatusParkedExcludedFromExecutionPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	orgID, jobID := seedLifecycleStatusJob(t, pool, models.JobStatusAwaitingStartControl)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO nodes(id,mode,status,last_heartbeat_at) VALUES('synthetic-status-worker','worker','active',now())`)
	require.NoError(t, err, "create a fresh eligible synthetic worker so claim exclusion is not caused by absent capacity")
	before := lifecycleStatusJobSnapshot(t, pool, orgID, jobID)
	store := NewJobStore(pool)
	claimed, err := store.ClaimNextRunnable(ctx, "synthetic-status-worker", "synthetic-status-owner", uuid.New(), time.Minute)
	require.NoError(t, err, "claim must skip parked intent rather than treat it as runnable or expired")
	require.Nil(t, claimed, "no runtime execution lease may be created for parked intent")
	token := uuid.New()
	mutations := []struct {
		name string
		run  func() (bool, error)
	}{
		{name: "success", run: func() (bool, error) { return store.MarkSucceededWithLease(ctx, jobID, token) }},
		{name: "failure", run: func() (bool, error) { return store.MarkFailedWithLease(ctx, jobID, token, "synthetic") }},
		{name: "nonconsuming retry", run: func() (bool, error) {
			return store.RetryWithoutConsumingAttemptWithLease(ctx, jobID, token, "synthetic", time.Now())
		}},
	}
	for _, mutation := range mutations {
		changed, err := mutation.run()
		require.NoError(t, err, "lease mutation must leave an unleased parked job untouched: %s", mutation.name)
		require.False(t, changed, "parked ownership cannot be converted by an execution-lease operation: %s", mutation.name)
	}
	reclaimed, err := store.ReclaimLostRunningJobs(ctx, time.Now().Add(time.Hour), 100)
	require.NoError(t, err, "lost-running recovery must exclude parked intent even with an old timestamp")
	require.Equal(t, int64(0), reclaimed, "age and missing lease must not make parked control reclaimable")
	deleted, err := store.DeleteExpiredCompleted(ctx, 1)
	require.NoError(t, err, "terminal retention must remain a separate policy")
	require.Equal(t, int64(0), deleted, "retention must preserve old parked input ownership")
	require.Equal(t, before, lifecycleStatusJobSnapshot(t, pool, orgID, jobID), "claim, lease mutation, reaping and retention must preserve the complete parked job")
}

func TestJobStatusParkedRollbackRefusalPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	orgID, jobID := seedLifecycleStatusJob(t, pool, models.JobStatusAwaitingStartControl)
	before := lifecycleStatusJobSnapshot(t, pool, orgID, jobID)
	migration, err := os.ReadFile("../../migrations/000352_jobs_start_control_status.down.sql")
	require.NoError(t, err, "read the exact shipped parked-status rollback policy")
	tx := beginSessionLifecycleProbe(t, context.Background(), pool)
	_, err = tx.Exec(context.Background(), string(migration))
	requireSessionLifecycleSQLState(t, err, "55000", "rollback must refuse retained parked intent rather than rewrite it or drop its ownership")
	require.NoError(t, tx.Rollback(context.Background()), "discard the refused migration transaction")
	require.Equal(t, before, lifecycleStatusJobSnapshot(t, pool, orgID, jobID), "rollback refusal must preserve the full parked job and payload")
}

func TestJobStatusParkedLeaseCheckPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, assignment string }{
		{name: "node ownership", assignment: `locked_by_node_id='synthetic-worker'`},
		{name: "run ownership", assignment: `run_owner_id='synthetic-owner'`},
		{name: "execution token", assignment: `lock_token=gen_random_uuid()`},
		{name: "claim timestamp", assignment: `locked_at=now()`},
		{name: "renewable lease", assignment: `lease_expires_at=now()-interval '1 day'`},
		{name: "terminal completion timestamp", assignment: `completed_at=now()`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			orgID, jobID := seedLifecycleStatusJob(t, pool, models.JobStatusAwaitingStartControl)
			before := lifecycleStatusJobSnapshot(t, pool, orgID, jobID)
			_, err := pool.Exec(context.Background(), `UPDATE jobs SET `+tt.assignment+` WHERE org_id=$1 AND id=$2`, orgID, jobID)
			requireSessionLifecycleSQLState(t, err, "23514", "a parked binding must reject every execution-lease or terminal-state slot")
			require.Equal(t, before, lifecycleStatusJobSnapshot(t, pool, orgID, jobID), "a rejected lease assignment must preserve every parked job field")
		})
	}
}

func TestJobStatusInactiveMigrationRoundTripPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	orgID, jobID := seedLifecycleStatusJob(t, pool, models.JobStatusPending)
	before := lifecycleStatusJobSnapshot(t, pool, orgID, jobID)
	files := []string{
		"000352_jobs_start_control_status.down.sql", "000351_jobs_start_control_checks_validate.down.sql", "000350_jobs_start_control_checks.down.sql", "000349_jobs_nonterminal_dedupe.down.sql",
		"000349_jobs_nonterminal_dedupe.up.sql", "000350_jobs_start_control_checks.up.sql", "000351_jobs_start_control_checks_validate.up.sql", "000352_jobs_start_control_status.up.sql",
	}
	for _, name := range files {
		migration, err := os.ReadFile("../../migrations/" + name)
		require.NoError(t, err, "read the exact dependency-ordered status migration: %s", name)
		_, err = pool.Exec(context.Background(), string(migration))
		require.NoError(t, err, "inactive rollback and reinstall must preserve standalone concurrent execution: %s", name)
	}
	require.Equal(t, before, lifecycleStatusJobSnapshot(t, pool, orgID, jobID), "an inactive status rollback/reinstall must preserve legacy payloads and lease metadata")
	var valid, unique, oldPresent bool
	var columns []string
	err := pool.QueryRow(context.Background(), `SELECT i.indisvalid,i.indisunique,ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY AS key(attnum,n) JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=key.attnum ORDER BY key.n),to_regclass('idx_jobs_dedupe') IS NOT NULL FROM pg_index i WHERE i.indexrelid='idx_jobs_nonterminal_dedupe'::regclass`).Scan(&valid, &unique, &columns, &oldPresent)
	require.NoError(t, err, "inspect the installed index rather than assuming CREATE success establishes its scope")
	require.Equal(t, []bool{true, true, true}, []bool{valid, unique, oldPresent}, "the broader unique index must be valid while the old compatible index remains")
	require.Equal(t, []string{"queue", "dedupe_key"}, columns, "the replacement index must retain the original global queue/key scope, not add tenant scope")
}

func TestLifecycleRuntimeInputSetShapePostgres(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "nonempty exact set", value: "{" + id.String() + "}", valid: true},
		{name: "empty array", value: "{}"},
		{name: "duplicate input", value: "{" + id.String() + "," + id.String() + "}"},
		{name: "null element", value: "{NULL}"},
		{name: "zero identity", value: "{00000000-0000-0000-0000-000000000000}"},
		{name: "multidimensional cannot become an alternate encoding", value: "{{" + id.String() + "}}"},
		{name: "alternate array bound", value: "[0:0]={" + id.String() + "}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			var valid bool
			err := pool.QueryRow(context.Background(), `SELECT lifecycle143_valid_input_dispatch_set($1::uuid[])`, tt.value).Scan(&valid)
			require.NoError(t, err, "shape validation must reject alternate array forms without a PostgreSQL array operator error")
			require.Equal(t, tt.valid, valid, "an immutable input set must be nonempty, unique, nonzero and canonically one-dimensional")
		})
	}
}

func seedLifecycleStatusJob(t *testing.T, pool *pgxpool.Pool, status models.JobStatus) (uuid.UUID, uuid.UUID) {
	t.Helper()
	orgID, jobID := uuid.New(), uuid.New()
	_, err := pool.Exec(context.Background(), `INSERT INTO organizations(id,name) VALUES($1,'Synthetic lifecycle status tenant')`, orgID)
	require.NoError(t, err, "create an isolated tenant for the status-policy fixture")
	_, err = pool.Exec(context.Background(), `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,dedupe_key,attempts,max_attempts,updated_at,run_at) VALUES($1,$2,'agent','synthetic_status_probe','{"input":"fixed"}',$3,$4,2,7,now()-interval '60 days',now()-interval '1 day')`, jobID, orgID, status, "synthetic:"+jobID.String())
	require.NoError(t, err, "persist a synthetic input job with exact status and no execution lease")
	return orgID, jobID
}

func lifecycleStatusJobSnapshot(t *testing.T, database DBTX, orgID, jobID uuid.UUID) json.RawMessage {
	t.Helper()
	var result json.RawMessage
	err := database.QueryRow(context.Background(), `SELECT to_jsonb(j) FROM jobs j WHERE org_id=$1 AND id=$2`, orgID, jobID).Scan(&result)
	require.NoError(t, err, "capture all exact job fields independently of the policy under test")
	return result
}
