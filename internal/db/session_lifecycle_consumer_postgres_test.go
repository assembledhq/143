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

func TestLifecycleConsumerScopedReferencesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write func(*testing.T, *pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) error
	}{
		{name: "dispatch cannot adopt foreign job", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, _ uuid.UUID) error {
			_, foreign := seedLifecycleStatusJob(t, pool, models.JobStatusPending)
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, foreign, nil, nil)
			return err
		}},
		{name: "dispatch cannot invent job", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, _ uuid.UUID) error {
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, uuid.New(), nil, nil)
			return err
		}},
		{name: "dispatch cannot adopt foreign runtime", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, job uuid.UUID) error {
			otherOrg, otherSession, otherLane := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, otherOrg, otherSession, otherLane)
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, job, &runtime.ID, &runtime.LeaseToken)
			return err
		}},
		{name: "dispatch cannot adopt another session runtime", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, job uuid.UUID) error {
			other := seedLifecycleConsumerSession(t, pool, org)
			runtime := createSeedRuntime(t, pool, org, other.ID, *other.PrimaryThreadID)
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, job, &runtime.ID, &runtime.LeaseToken)
			return err
		}},
		{name: "dispatch cannot adopt sibling runtime", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, job uuid.UUID) error {
			otherLane := models.SessionThread{OrgID: org, SessionID: session, Label: "Synthetic sibling", AgentType: models.DefaultDefaultAgentType, Status: models.ThreadStatusIdle}
			require.NoError(t, NewSessionThreadStore(pool).Create(context.Background(), &otherLane, 10), "create an independent sibling lane for exact runtime-route rejection")
			runtime := createSeedRuntime(t, pool, org, session, otherLane.ID)
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, job, &runtime.ID, &runtime.LeaseToken)
			return err
		}},
		{name: "referenced job cannot disappear", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, job uuid.UUID) error {
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, job, nil, nil)
			require.NoError(t, err, "bind an exact same-tenant job before testing deletion policy")
			_, err = pool.Exec(context.Background(), `DELETE FROM jobs WHERE org_id=$1 AND id=$2`, org, job)
			return err
		}},
		{name: "referenced runtime cannot disappear", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, job uuid.UUID) error {
			runtime := createSeedRuntime(t, pool, org, session, lane)
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, job, &runtime.ID, &runtime.LeaseToken)
			require.NoError(t, err, "bind the exact job and lane runtime as one structural consumer")
			_, err = pool.Exec(context.Background(), `DELETE FROM thread_runtimes WHERE org_id=$1 AND id=$2`, org, runtime.ID)
			return err
		}},
		{name: "runtime cannot adopt foreign job", write: func(t *testing.T, pool *pgxpool.Pool, org, session, lane, _ uuid.UUID) error {
			runtime := createSeedRuntime(t, pool, org, session, lane)
			_, foreign := seedLifecycleStatusJob(t, pool, models.JobStatusPending)
			_, err := pool.Exec(context.Background(), `UPDATE thread_runtimes SET job_id=$3,job_attempt_token=gen_random_uuid(),allocation_digest=repeat('a',64),allocation_expected_turn=0,input_dispatch_ids=ARRAY[gen_random_uuid()],admission_session_epoch=0,admission_thread_epoch=0,provider_invocation_ordinal=0 WHERE org_id=$1 AND id=$2`, org, runtime.ID, foreign)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, lane := seedRuntimeSeedThread(t, pool)
			job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusPending)
			err := tt.write(t, pool, org, session, lane, job)
			requireSessionLifecycleSQLState(t, err, "23503", "immediate non-cascading scoped references must reject wrong tenant, wrong route, missing parent and evidence deletion")
		})
	}
}

func TestLifecycleConsumerJobRetentionPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	org, session, lane := seedRuntimeSeedThread(t, pool)
	dispatchJob := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
	dispatchID, err := insertLifecycleConsumerDispatch(pool, org, session, lane, dispatchJob, nil, nil)
	require.NoError(t, err, "retain a terminal job through its durable dispatch independently of job status")
	runtimeJob := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
	resourceJob := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
	_, err = pool.Exec(context.Background(), `UPDATE jobs SET job_type='snapshot_object_cleanup' WHERE org_id=$1 AND id=$2`, org, resourceJob)
	require.NoError(t, err, "retain unresolved exact-object cleanup obligations independently of a terminal job status")
	runtime := createSeedRuntime(t, pool, org, session, lane)
	_, err = pool.Exec(context.Background(), `UPDATE thread_runtimes SET job_id=$3,job_attempt_token=gen_random_uuid(),allocation_digest=repeat('a',64),allocation_expected_turn=0,input_dispatch_ids=ARRAY[$4::uuid],admission_session_epoch=0,admission_thread_epoch=0,provider_invocation_ordinal=0 WHERE org_id=$1 AND id=$2`, org, runtime.ID, runtimeJob, dispatchID)
	require.NoError(t, err, "retain a separate terminal job through immutable runtime allocation history")
	unrelated := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
	before := []json.RawMessage{lifecycleStatusJobSnapshot(t, pool, org, dispatchJob), lifecycleStatusJobSnapshot(t, pool, org, runtimeJob), lifecycleStatusJobSnapshot(t, pool, org, resourceJob)}
	deleted, err := NewJobStore(pool).DeleteExpiredCompleted(context.Background(), 30)
	require.NoError(t, err, "cleanup must filter referenced dispatch and runtime jobs before trying the physical delete")
	require.Equal(t, int64(1), deleted, "only the unrelated old terminal job is eligible")
	var remaining bool
	err = pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM jobs WHERE org_id=$1 AND id=$2)`, org, unrelated).Scan(&remaining)
	require.NoError(t, err, "verify physical removal of the unrelated terminal job")
	require.False(t, remaining, "retained evidence must not prevent unrelated terminal cleanup")
	require.Equal(t, before, []json.RawMessage{lifecycleStatusJobSnapshot(t, pool, org, dispatchJob), lifecycleStatusJobSnapshot(t, pool, org, runtimeJob), lifecycleStatusJobSnapshot(t, pool, org, resourceJob)}, "retention must preserve every field of referenced jobs and unresolved object obligations")
}

func TestLifecycleRuntimeAllocationExpectedTurnPostgres(t *testing.T) {
	t.Parallel()
	zero, negative, largest := int32(0), int32(-1), int32(2147483647)
	tests := []struct {
		name        string
		turn        *int32
		legacyOnly  bool
		expectedErr bool
	}{
		{name: "bound allocation requires expected turn", expectedErr: true},
		{name: "bound allocation rejects negative turn", turn: &negative, expectedErr: true},
		{name: "bound allocation accepts initial turn", turn: &zero},
		{name: "bound allocation accepts largest supported turn", turn: &largest},
		{name: "legacy runtime cannot carry a partial turn", turn: &zero, legacyOnly: true, expectedErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, lane := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, org, session, lane)
			job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusPending)
			var before, after json.RawMessage
			err := pool.QueryRow(context.Background(), `SELECT to_jsonb(r) FROM thread_runtimes r WHERE org_id=$1 AND id=$2`, org, runtime.ID).Scan(&before)
			require.NoError(t, err, "capture the complete unbound runtime before attempting allocation metadata")
			if tt.legacyOnly {
				_, err = pool.Exec(context.Background(), `UPDATE thread_runtimes SET allocation_expected_turn=$3 WHERE org_id=$1 AND id=$2`, org, runtime.ID, tt.turn)
			} else {
				_, err = pool.Exec(context.Background(), `UPDATE thread_runtimes SET job_id=$3,job_attempt_token=gen_random_uuid(),allocation_digest=repeat('a',64),allocation_expected_turn=$4,input_dispatch_ids=ARRAY[gen_random_uuid()],admission_session_epoch=0,admission_thread_epoch=0,provider_invocation_ordinal=0 WHERE org_id=$1 AND id=$2`, org, runtime.ID, job, tt.turn)
			}
			if tt.expectedErr {
				requireSessionLifecycleSQLState(t, err, "23514", "allocation shape must retain a complete nonnegative immutable expected-turn identity")
				err = pool.QueryRow(context.Background(), `SELECT to_jsonb(r) FROM thread_runtimes r WHERE org_id=$1 AND id=$2`, org, runtime.ID).Scan(&after)
				require.NoError(t, err, "read back the refused allocation without retaining a partial turn")
				require.Equal(t, before, after, "a refused allocation must preserve every runtime field")
				return
			}
			require.NoError(t, err, "an explicit supported allocation turn must fit the complete binding shape")
			var actual int32
			err = pool.QueryRow(context.Background(), `SELECT allocation_expected_turn FROM thread_runtimes WHERE org_id=$1 AND id=$2 AND job_id=$3`, org, runtime.ID, job).Scan(&actual)
			require.NoError(t, err, "read back the exact job-scoped immutable allocation turn")
			require.Equal(t, *tt.turn, actual, "the stored allocation turn must equal the supplied attempt turn exactly")
		})
	}
}

func TestLifecycleConsumerReferenceCatalogPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	type reference struct {
		Name, Child, Parent                      string
		ChildColumns, ParentColumns              []string
		Validated, Deferrable, InitiallyDeferred bool
		OnDelete, OnUpdate                       string
	}
	rows, err := pool.Query(context.Background(), `SELECT c.conname,child.relname,parent.relname,
		ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n),
		ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=k.attnum ORDER BY k.n),
		c.convalidated,c.condeferrable,c.condeferred,c.confdeltype::text,c.confupdtype::text
		FROM pg_constraint c JOIN pg_class child ON child.oid=c.conrelid JOIN pg_class parent ON parent.oid=c.confrelid
		WHERE c.conname IN ('session_input_dispatches_consumer_job_fk','session_input_dispatches_consumer_runtime_fk','thread_runtimes_consumer_job_fk')
		AND c.conrelid IN ('session_input_dispatches'::regclass,'thread_runtimes'::regclass) ORDER BY c.conname`)
	require.NoError(t, err, "inspect the real full-schema foreign keys rather than inferring scope from their names")
	actual, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reference, error) {
		var value reference
		err := row.Scan(&value.Name, &value.Child, &value.Parent, &value.ChildColumns, &value.ParentColumns, &value.Validated, &value.Deferrable, &value.InitiallyDeferred, &value.OnDelete, &value.OnUpdate)
		return value, err
	})
	require.NoError(t, err, "collect every referenced column and deletion policy")
	expected := []reference{
		{Name: "session_input_dispatches_consumer_job_fk", Child: "session_input_dispatches", Parent: "jobs", ChildColumns: []string{"org_id", "job_id"}, ParentColumns: []string{"org_id", "id"}, Validated: true, OnDelete: "a", OnUpdate: "a"},
		{Name: "session_input_dispatches_consumer_runtime_fk", Child: "session_input_dispatches", Parent: "thread_runtimes", ChildColumns: []string{"org_id", "session_id", "thread_id", "runtime_id"}, ParentColumns: []string{"org_id", "session_id", "thread_id", "id"}, Validated: true, OnDelete: "a", OnUpdate: "a"},
		{Name: "thread_runtimes_consumer_job_fk", Child: "thread_runtimes", Parent: "jobs", ChildColumns: []string{"org_id", "job_id"}, ParentColumns: []string{"org_id", "id"}, Validated: true, OnDelete: "a", OnUpdate: "a"},
	}
	require.Equal(t, expected, actual, "consumer FKs must be fully validated, immediate and non-cascading at their exact tenant/session/lane scope")
}

func TestLifecycleConsumerReferenceRollbackRefusalPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, migration string
		runtime         bool
	}{
		{name: "retention cannot discard an existing binding", migration: "000356_runtime_consumer_job_reference.down.sql"},
		{name: "job reference cannot be removed while retained", migration: "000357_dispatch_consumer_job_reference.down.sql"},
		{name: "runtime reference cannot be removed while retained", migration: "000358_dispatch_consumer_runtime_reference.down.sql", runtime: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, lane := seedRuntimeSeedThread(t, pool)
			job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
			var runtimeID, runtimeLease *uuid.UUID
			if tt.runtime {
				runtime := createSeedRuntime(t, pool, org, session, lane)
				runtimeID, runtimeLease = &runtime.ID, &runtime.LeaseToken
			}
			_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, job, runtimeID, runtimeLease)
			require.NoError(t, err, "retain a real structural binding before removing any guard")
			before := lifecycleStatusJobSnapshot(t, pool, org, job)
			migration, err := os.ReadFile("../../migrations/" + tt.migration)
			require.NoError(t, err, "read the exact reference rollback migration")
			tx := beginSessionLifecycleProbe(t, context.Background(), pool)
			_, err = tx.Exec(context.Background(), string(migration))
			requireSessionLifecycleSQLState(t, err, "55000", "a down migration cannot remove retained bindings or resume unsafe generic retention")
			require.NoError(t, tx.Rollback(context.Background()), "discard a refused rollback before independent readback")
			require.Equal(t, before, lifecycleStatusJobSnapshot(t, pool, org, job), "refused rollback must preserve the exact original job and payload")
			_, err = pool.Exec(context.Background(), `DELETE FROM jobs WHERE org_id=$1 AND id=$2`, org, job)
			requireSessionLifecycleSQLState(t, err, "23503", "the failed rollback must leave the immediate non-cascading reference guard installed")
		})
	}
}

func TestLifecycleConsumerInactiveMigrationRoundTripPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	org, session, lane := seedRuntimeSeedThread(t, pool)
	runtime := createSeedRuntime(t, pool, org, session, lane)
	var before, after json.RawMessage
	err := pool.QueryRow(context.Background(), `SELECT to_jsonb(r) FROM thread_runtimes r WHERE org_id=$1 AND id=$2`, org, runtime.ID).Scan(&before)
	require.NoError(t, err, "capture every legacy runtime field before dependency-ordered rollback")
	files := []string{
		"000380_lifecycle_consumer_index_rollback_floor.down.sql",
		"000379_legacy_input_consumer_jobs_index.down.sql",
		"000378_automation_consumer_job_index.down.sql",
		"000377_holder_consumer_runtime_index.down.sql",
		"000376_holder_consumer_session_index.down.sql",
		"000375_automation_consumer_target_index.down.sql",
		"000374_automation_consumer_thread_index.down.sql",
		"000373_recheck_consumer_session_index.down.sql",
		"000372_executor_consumer_thread_index.down.sql",
		"000371_executor_consumer_session_index.down.sql",
		"000370_runtime_consumer_thread_lookup.down.sql",
		"000369_dispatch_consumer_thread_index.down.sql",
		"000368_workspace_recovery_due_index.down.sql",
		"000367_workspace_recovery_identity_index.down.sql",
		"000366_validate_preparation_references.down.sql", "000365_workspace_recovery_reservation.down.sql", "000364_start_preparation_request_reference.down.sql", "000363_start_preparation_job_reference.down.sql", "000362_start_preparation_thread_reference.down.sql", "000361_start_preparation_session_reference.down.sql", "000360_runtime_start_preparations.down.sql",
		"000359_validate_consumer_references.down.sql", "000358_dispatch_consumer_runtime_reference.down.sql", "000357_dispatch_consumer_job_reference.down.sql", "000356_runtime_consumer_job_reference.down.sql", "000355_runtime_consumer_job_index.down.sql", "000354_dispatch_input_consumers.down.sql", "000353_runtime_input_allocation.down.sql",
		"000353_runtime_input_allocation.up.sql", "000354_dispatch_input_consumers.up.sql", "000355_runtime_consumer_job_index.up.sql", "000356_runtime_consumer_job_reference.up.sql", "000357_dispatch_consumer_job_reference.up.sql", "000358_dispatch_consumer_runtime_reference.up.sql", "000359_validate_consumer_references.up.sql",
		"000360_runtime_start_preparations.up.sql", "000361_start_preparation_session_reference.up.sql", "000362_start_preparation_thread_reference.up.sql", "000363_start_preparation_job_reference.up.sql", "000364_start_preparation_request_reference.up.sql", "000365_workspace_recovery_reservation.up.sql", "000366_validate_preparation_references.up.sql",
		"000367_workspace_recovery_identity_index.up.sql",
		"000368_workspace_recovery_due_index.up.sql",
		"000369_dispatch_consumer_thread_index.up.sql",
		"000370_runtime_consumer_thread_lookup.up.sql",
		"000371_executor_consumer_session_index.up.sql",
		"000372_executor_consumer_thread_index.up.sql",
		"000373_recheck_consumer_session_index.up.sql",
		"000374_automation_consumer_thread_index.up.sql",
		"000375_automation_consumer_target_index.up.sql",
		"000376_holder_consumer_session_index.up.sql",
		"000377_holder_consumer_runtime_index.up.sql",
		"000378_automation_consumer_job_index.up.sql",
		"000379_legacy_input_consumer_jobs_index.up.sql",
		"000380_lifecycle_consumer_index_rollback_floor.up.sql",
	}
	for _, name := range files {
		migration, err := os.ReadFile("../../migrations/" + name)
		require.NoError(t, err, "read the exact dependency-ordered consumer migration: %s", name)
		_, err = pool.Exec(context.Background(), string(migration))
		require.NoError(t, err, "never-activated schema rollback and reinstall must preserve existing runtime data: %s", name)
	}
	err = pool.QueryRow(context.Background(), `SELECT to_jsonb(r) FROM thread_runtimes r WHERE org_id=$1 AND id=$2`, org, runtime.ID).Scan(&after)
	require.NoError(t, err, "read back every legacy runtime field after the complete round trip")
	require.Equal(t, before, after, "passive schema installation and inactive rollback must neither infer allocation authority nor rewrite existing runtimes")
}

func TestLifecycleConsumerRetainedJobsDoNotStarveBatchPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	org, session, lane := seedRuntimeSeedThread(t, pool)
	_, err := pool.Exec(context.Background(), `WITH retained AS (
		INSERT INTO jobs(org_id,queue,job_type,payload,status,updated_at)
		SELECT $1,'agent','synthetic_consumer','{}','succeeded',now()-interval '90 days' FROM generate_series(1,10001) RETURNING id
	) INSERT INTO session_input_dispatches(org_id,session_id,thread_id,session_epoch,thread_epoch,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,job_id)
	SELECT $1,$2,$3,0,0,'initial','synthetic_retention',id::text,repeat('a',64),'{}',1,'bound','job',id FROM retained`, org, session, lane)
	require.NoError(t, err, "place more protected jobs than a cleanup batch before the younger eligible rows")
	eligible := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
	deleted, err := NewJobStore(pool).DeleteExpiredCompleted(context.Background(), 30)
	require.NoError(t, err, "reference exclusions must run in the candidate query before its limit")
	require.Equal(t, int64(1), deleted, "a full older protected batch must not starve an unrelated expired job")
	var counts []int64
	rows, err := pool.Query(context.Background(), `SELECT count(*) FROM jobs WHERE org_id=$1 AND id=$2 UNION ALL SELECT count(*) FROM jobs WHERE org_id=$1`, org, eligible)
	require.NoError(t, err, "read back exact eligible and retained cardinalities")
	counts, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err, "collect the physical cleanup result")
	require.Equal(t, []int64{0, 10001}, counts, "only the eligible job may disappear despite the candidate batch being smaller than protected history")
}

func insertLifecycleConsumerDispatch(pool *pgxpool.Pool, org, session, lane, job uuid.UUID, runtime, lease *uuid.UUID) (uuid.UUID, error) {
	id := uuid.New()
	kind := "job"
	var jobToken *uuid.UUID
	if runtime != nil {
		kind = "runtime"
		token := uuid.New()
		jobToken = &token
	}
	_, err := pool.Exec(context.Background(), `INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,session_epoch,thread_epoch,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,job_id,runtime_id,job_lock_token,runtime_lease_token) VALUES($1,$2,$3,$4,0,0,'initial','synthetic_consumer',$1::uuid::text,repeat('a',64),'{}',1,'bound',$5,$6,$7,$8,$9)`, id, org, session, lane, kind, job, runtime, jobToken, lease)
	return id, err
}

func seedLifecycleConsumerJob(t *testing.T, pool *pgxpool.Pool, org uuid.UUID, status models.JobStatus) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(context.Background(), `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,updated_at) VALUES($1,$2,'agent','synthetic_consumer','{}',$3,$4)`, id, org, status, time.Now().Add(-40*24*time.Hour))
	require.NoError(t, err, "create an exact synthetic consumer job without inferring a binding from payload")
	return id
}

func seedLifecycleConsumerSession(t *testing.T, pool *pgxpool.Pool, org uuid.UUID) models.Session {
	t.Helper()
	session := models.Session{OrgID: org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(context.Background(), &session), "create a distinct session with its own primary lane under the same tenant")
	require.NotNil(t, session.PrimaryThreadID, "the same-tenant fixture must have a real different primary lane")
	return session
}
