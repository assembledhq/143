package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecyclePreparationShapeAndScopePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, assignment, state string
	}{
		{"missing job", "job_id=gen_random_uuid()", "23503"},
		{"foreign session", "session_id=gen_random_uuid()", "23503"},
		{"foreign lane", "thread_id=gen_random_uuid()", "23503"},
		{"missing exact request", "dispatch_kind='answer',request_id=gen_random_uuid()", "23503"},
		{"zero preparation token", "token='00000000-0000-0000-0000-000000000000'", "23514"},
		{"zero tenant", "org_id='00000000-0000-0000-0000-000000000000'", "23514"},
		{"zero session", "session_id='00000000-0000-0000-0000-000000000000'", "23514"},
		{"zero lane", "thread_id='00000000-0000-0000-0000-000000000000'", "23514"},
		{"missing lane epoch", "thread_epoch=NULL", "23514"},
		{"negative epoch", "session_epoch=-1", "23514"},
		{"empty input set", "input_dispatch_ids='{}'", "23514"},
		{"duplicate input set", "input_dispatch_ids=ARRAY[input_dispatch_ids[1],input_dispatch_ids[1]]", "23514"},
		{"ordinary input cannot carry request", "request_id=gen_random_uuid()", "23514"},
		{"unknown execution state", "state='expired'", "23514"},
		{"closed requires revocation", "state='closed'", "23514"},
		{"unknown check state", "check_disposition='success',check_receipt='{}'", "23514"},
		{"check requires object", "check_disposition='verified',check_receipt='null'", "23514"},
		{"consumed check cannot be retryable", "check_disposition='retryable',check_receipt='{}',check_consumed_at=now()", "23514"},
		{"physical operation requires identity", "physical_operation_state='unknown'", "23514"},
		{"target cannot be null", "target='null'", "23514"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, lane := seedRuntimeSeedThread(t, pool)
			job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusPending)
			id := seedLifecyclePreparation(t, pool, org, session, &lane, job, "preparing", "none")
			before := lifecyclePreparationSnapshot(t, pool, org, id)
			_, err := pool.Exec(context.Background(), "UPDATE session_runtime_start_preparations SET "+tt.assignment+" WHERE org_id=$1 AND id=$2", org, id)
			requireSessionLifecycleSQLState(t, err, tt.state, "new preparation shape and exact parent routes must fail without partial mutation")
			require.Equal(t, before, lifecyclePreparationSnapshot(t, pool, org, id), "rejected preparation writes must preserve every original field")
		})
	}
}

func TestLifecyclePreparationUncertainOperationRetentionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, query string }{
		{"delete closed uncertain operation", "DELETE FROM session_runtime_start_preparations WHERE org_id=$1 AND id=$2"},
		{"replace revoked token", "UPDATE session_runtime_start_preparations SET token=gen_random_uuid() WHERE org_id=$1 AND id=$2"},
		{"replace job claim", "UPDATE session_runtime_start_preparations SET job_attempt_token=gen_random_uuid() WHERE org_id=$1 AND id=$2"},
		{"replace fixed input set", "UPDATE session_runtime_start_preparations SET input_dispatch_ids=ARRAY[gen_random_uuid()] WHERE org_id=$1 AND id=$2"},
		{"replace exact operation", "UPDATE session_runtime_start_preparations SET physical_operation_id=gen_random_uuid() WHERE org_id=$1 AND id=$2"},
		{"replace restore source", "UPDATE session_runtime_start_preparations SET physical_source_key='synthetic/replacement' WHERE org_id=$1 AND id=$2"},
		{"replace target", "UPDATE session_runtime_start_preparations SET target='{}' WHERE org_id=$1 AND id=$2"},
		{"rewrite target numeric encoding", "UPDATE session_runtime_start_preparations SET target=jsonb_set(target,'{version}','1.0') WHERE org_id=$1 AND id=$2"},
		{"rewrite resume numeric encoding", "UPDATE session_runtime_start_preparations SET resume_identity=jsonb_set(resume_identity,'{version}','1.0') WHERE org_id=$1 AND id=$2"},
		{"rewrite physical numeric encoding", "UPDATE session_runtime_start_preparations SET physical_operation=jsonb_set(physical_operation,'{version}','1.0') WHERE org_id=$1 AND id=$2"},
		{"clear uncertain operation", "UPDATE session_runtime_start_preparations SET physical_operation_state='none',physical_operation_id=NULL,physical_operation_scope=NULL,physical_operation=NULL,physical_source_key=NULL WHERE org_id=$1 AND id=$2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, lane := seedRuntimeSeedThread(t, pool)
			job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
			id := seedLifecyclePreparation(t, pool, org, session, &lane, job, "closed", "unknown")
			before := lifecyclePreparationSnapshot(t, pool, org, id)
			_, err := pool.Exec(context.Background(), tt.query, org, id)
			requireSessionLifecycleSQLState(t, err, "55000", "closed, revoked, expired and terminal-job state must not erase an unresolved physical operation")
			require.Equal(t, before, lifecyclePreparationSnapshot(t, pool, org, id), "the complete original physical identity and restore source must remain discoverable")
		})
	}
}

func TestLifecyclePreparationClosureAndJobRetentionPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	org, session, lane := seedRuntimeSeedThread(t, pool)
	job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
	id := seedLifecyclePreparation(t, pool, org, session, &lane, job, "preparing", "in_progress")
	var before, after json.RawMessage
	query := `SELECT to_jsonb(p)-ARRAY['state','revoked_at','updated_at'] FROM session_runtime_start_preparations p WHERE org_id=$1 AND id=$2`
	require.NoError(t, pool.QueryRow(context.Background(), query, org, id).Scan(&before), "capture exact operation identity before execution-only closure")
	_, err := pool.Exec(context.Background(), `UPDATE session_runtime_start_preparations SET state='closed',revoked_at=now(),updated_at=now() WHERE org_id=$1 AND id=$2`, org, id)
	require.NoError(t, err, "execution can close without claiming its in-flight target operation stopped")
	require.NoError(t, pool.QueryRow(context.Background(), query, org, id).Scan(&after), "read all retained physical fields after logical closure")
	require.Equal(t, before, after, "execution closure must preserve original tokens, immutable input set and physical/source identity")
	jobBefore := lifecycleStatusJobSnapshot(t, pool, org, job)
	unrelated := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
	deleted, err := NewJobStore(pool).DeleteExpiredCompleted(context.Background(), 30)
	require.NoError(t, err, "terminal cleanup must exclude preparation references before its candidate limit")
	require.Equal(t, int64(1), deleted, "only the unrelated expired job may be deleted")
	require.Equal(t, jobBefore, lifecycleStatusJobSnapshot(t, pool, org, job), "a closed preparation still retains its exact terminal job and immutable payload")
	var exists bool
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM jobs WHERE org_id=$1 AND id=$2)`, org, unrelated).Scan(&exists), "check the unrelated physical cleanup outcome")
	require.False(t, exists, "preparation retention must not block unrelated terminal cleanup")
}

func TestLifecyclePreparationRetainedJobsDoNotStarveBatchPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	org, session, lane := seedRuntimeSeedThread(t, pool)
	_, err := pool.Exec(context.Background(), `WITH protected AS (
		INSERT INTO jobs(org_id,queue,job_type,payload,status,updated_at)
		SELECT $1,'agent','synthetic_preparation','{}','failed',now()-interval '90 days'
		FROM generate_series(1,10001) RETURNING id
	) INSERT INTO session_runtime_start_preparations
		(org_id,session_id,thread_id,job_id,input_dispatch_ids,dispatch_kind,session_epoch,thread_epoch,version,token,job_attempt_token,state,deadline_at,next_attempt_at,revoked_at,target,resume_identity)
	SELECT $1,$2,$3,id,ARRAY[gen_random_uuid()],'message',0,0,1,gen_random_uuid(),gen_random_uuid(),
		'closed',now()-interval '1 day',now(),now(),'{}','{}' FROM protected`, org, session, lane)
	require.NoError(t, err, "place more retained preparations than a cleanup batch before younger eligible work")
	eligible := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
	deleted, err := NewJobStore(pool).DeleteExpiredCompleted(context.Background(), 30)
	require.NoError(t, err, "preparation exclusions must apply before LIMIT, not only in a failing foreign-key delete")
	require.Equal(t, int64(1), deleted, "a full batch of older retained preparations must not starve the unrelated eligible job")
	rows, err := pool.Query(context.Background(), `SELECT count(*) FROM jobs WHERE org_id=$1 AND id=$2 UNION ALL SELECT count(*) FROM jobs WHERE org_id=$1`, org, eligible)
	require.NoError(t, err, "inspect both exact eligible removal and retained history count")
	counts, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err, "collect the durable preparation-retention result")
	require.Equal(t, []int64{0, 10001}, counts, "cleanup must remove exactly the unrelated job while preserving every referenced preparation job")
}

func TestLifecyclePreparationReferenceCatalogPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	type reference struct {
		Name, Parent                string
		ChildColumns, ParentColumns []string
		Validated, Deferrable       bool
		Delete, Update              string
	}
	rows, err := pool.Query(context.Background(), `SELECT c.conname,c.confrelid::regclass::text,
		ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n),
		ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=k.attnum ORDER BY k.n),
		c.convalidated,c.condeferrable,c.confdeltype::text,c.confupdtype::text
		FROM pg_constraint c WHERE c.conrelid='session_runtime_start_preparations'::regclass
		AND c.conname IN ('start_preparations_job_fk','start_preparations_request_fk','start_preparations_session_fk','start_preparations_thread_fk') ORDER BY c.conname`)
	require.NoError(t, err, "inspect exact composite preparation references in the fully migrated schema")
	actual, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reference, error) {
		var value reference
		err := row.Scan(&value.Name, &value.Parent, &value.ChildColumns, &value.ParentColumns, &value.Validated, &value.Deferrable, &value.Delete, &value.Update)
		return value, err
	})
	require.NoError(t, err, "collect scoped reference columns and real deletion/update policy")
	expected := []reference{
		{"start_preparations_job_fk", "jobs", []string{"org_id", "job_id"}, []string{"org_id", "id"}, true, false, "a", "a"},
		{"start_preparations_request_fk", "session_human_input_requests", []string{"org_id", "session_id", "request_id"}, []string{"org_id", "session_id", "id"}, true, false, "a", "a"},
		{"start_preparations_session_fk", "sessions", []string{"org_id", "session_id"}, []string{"org_id", "id"}, true, false, "a", "a"},
		{"start_preparations_thread_fk", "session_threads", []string{"org_id", "session_id", "thread_id"}, []string{"org_id", "session_id", "id"}, true, false, "a", "a"},
	}
	require.Equal(t, expected, actual, "every new preparation parent must be validated, immediate and tenant/route scoped without cascade or SET NULL")
}

func TestLifecyclePreparationRetainedRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, migration string }{
		{"table", "000360_runtime_start_preparations.down.sql"},
		{"session reference", "000361_start_preparation_session_reference.down.sql"},
		{"thread reference", "000362_start_preparation_thread_reference.down.sql"},
		{"job retention", "000363_start_preparation_job_reference.down.sql"},
		{"request reference", "000364_start_preparation_request_reference.down.sql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, lane := seedRuntimeSeedThread(t, pool)
			job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
			id := seedLifecyclePreparation(t, pool, org, session, &lane, job, "closed", "unknown")
			before := lifecyclePreparationSnapshot(t, pool, org, id)
			raw, err := os.ReadFile("../../migrations/" + tt.migration)
			require.NoError(t, err, "read only the named preparation rollback")
			_, err = pool.Exec(context.Background(), string(raw))
			requireSessionLifecycleSQLState(t, err, "55000", "rollback must not discard retained current preparation or weaken its structural references")
			require.Equal(t, before, lifecyclePreparationSnapshot(t, pool, org, id), "refused rollback must preserve every preparation field")
		})
	}
}

func TestLifecycleWorkspaceRecoveryRetentionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, assignment string }{
		{"clear reservation", "workspace_recovery_id=NULL,workspace_recovery_token=NULL,workspace_recovery=NULL,workspace_recovery_next_attempt_at=NULL"},
		{"replace recovery", "workspace_recovery_id=gen_random_uuid()"},
		{"replace token", "workspace_recovery_token=gen_random_uuid()"},
		{"move tenant without touching reservation", "org_id=gen_random_uuid()"},
		{"move session without touching reservation", "id=gen_random_uuid()"},
		{"move tenant while touching reservation", "org_id=gen_random_uuid(),workspace_recovery=workspace_recovery"},
		{"move session while touching reservation", "id=gen_random_uuid(),workspace_recovery=workspace_recovery"},
		{"replace acknowledged scope", "workspace_recovery=jsonb_set(workspace_recovery,'{session_epoch}','1')"},
		{"replace reserved descriptor", "workspace_recovery=jsonb_set(workspace_recovery,'{reservation}','{}')"},
		{"replace physical operation", "workspace_recovery=jsonb_set(workspace_recovery,'{physical_operation}','{}')"},
		{"rewrite reservation numeric encoding", "workspace_recovery=jsonb_set(workspace_recovery,'{reservation,version}','1.0')"},
		{"rewrite physical numeric encoding", "workspace_recovery=jsonb_set(workspace_recovery,'{physical_operation,version}','1.0')"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, _ := seedRuntimeSeedThread(t, pool)
			seedLifecycleWorkspaceRecovery(t, pool, org, session)
			before := lifecycleWorkspaceRecoverySnapshot(t, pool, org, session)
			_, err := pool.Exec(context.Background(), "UPDATE sessions SET "+tt.assignment+" WHERE org_id=$1 AND id=$2", org, session)
			requireSessionLifecycleSQLState(t, err, "55000", "no parsed status, expired lease or revoked token may silently clear or replace the recovery reservation")
			require.Equal(t, before, lifecycleWorkspaceRecoverySnapshot(t, pool, org, session), "exact recovery and physical-operation identity must survive rejected replacement")
		})
	}
}

func TestLifecycleWorkspaceRecoveryShapePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, expression string }{
		{"missing envelope", "NULL"},
		{"empty envelope", "'{}'::jsonb"},
		{"string version", "jsonb_set($3::jsonb,'{version}','\"1\"')"},
		{"missing epoch", "$3::jsonb-'session_epoch'"},
		{"threaded recovery", "$3::jsonb||jsonb_build_object('thread_id',gen_random_uuid())"},
		{"missing reservation", "$3::jsonb-'reservation'"},
		{"null physical operation", "jsonb_set($3::jsonb,'{physical_operation}','null')"},
		{"unknown physical status", "jsonb_set($3::jsonb,'{physical_operation_state}','\"expired\"')"},
		{"unsettled with proof", "$3::jsonb||jsonb_build_object('physical_settlement_receipt','{}'::jsonb)"},
		{"settled without proof", "jsonb_set($3::jsonb,'{physical_operation_state}','\"settled\"')"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, _ := seedRuntimeSeedThread(t, pool)
			query := "UPDATE sessions SET workspace_recovery_id=gen_random_uuid(),workspace_recovery_token=gen_random_uuid(),workspace_recovery_next_attempt_at=now(),workspace_recovery=" + tt.expression + " WHERE org_id=$1 AND id=$2"
			args := []any{org, session}
			if tt.expression != "NULL" && tt.expression != "'{}'::jsonb" {
				args = append(args, syntheticLifecycleRecoveryEnvelope())
			}
			_, err := pool.Exec(context.Background(), query, args...)
			requireSessionLifecycleSQLState(t, err, "23514", "malformed recovery slots must not become invisible or gain lane-local semantics")
		})
	}
}

func TestLifecycleWorkspaceRecoveryDeleteAndRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		hardDelete, rollback bool
	}{
		{name: "ordinary delete"},
		{name: "explicit hard delete", hardDelete: true},
		{name: "schema rollback", rollback: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, _ := seedRuntimeSeedThread(t, pool)
			seedLifecycleWorkspaceRecovery(t, pool, org, session)
			before := lifecycleWorkspaceRecoverySnapshot(t, pool, org, session)
			var err error
			if tt.rollback {
				raw, readErr := os.ReadFile("../../migrations/000365_workspace_recovery_reservation.down.sql")
				require.NoError(t, readErr, "read the exact recovery reservation rollback")
				_, err = pool.Exec(context.Background(), string(raw))
			} else {
				tx := beginSessionLifecycleProbe(t, context.Background(), pool)
				if tt.hardDelete {
					_, err = tx.Exec(context.Background(), `SET LOCAL app.allow_hard_delete=true`)
					require.NoError(t, err, "select the real legacy hard-delete path rather than only its soft-delete redirect")
				}
				_, err = tx.Exec(context.Background(), `DELETE FROM sessions WHERE org_id=$1 AND id=$2`, org, session)
				requireSessionLifecycleSQLState(t, err, "55000", "a recovery reservation must survive both real hard delete and ordinary raw deletion")
				require.NoError(t, tx.Rollback(context.Background()), "settle only this synthetic rejected deletion")
			}
			requireSessionLifecycleSQLState(t, err, "55000", "neither deletion nor schema rollback can erase a retained whole-workspace reservation")
			require.Equal(t, before, lifecycleWorkspaceRecoverySnapshot(t, pool, org, session), "retained recovery identity and physical observation must remain exact after refusal")
		})
	}
}

// These fixtures prove passive representation/retention only. Their deliberately
// synthetic JSON objects are not typed target, acknowledgement or host proof.
func seedLifecyclePreparation(t *testing.T, pool *pgxpool.Pool, org, session uuid.UUID, lane *uuid.UUID, job uuid.UUID, state, physical string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var laneEpoch *int64
	if lane != nil {
		zero := int64(0)
		laneEpoch = &zero
	}
	_, err := pool.Exec(context.Background(), `INSERT INTO session_runtime_start_preparations
		(id,org_id,session_id,thread_id,job_id,input_dispatch_ids,dispatch_kind,session_epoch,thread_epoch,version,token,job_attempt_token,state,deadline_at,next_attempt_at,revoked_at,target,resume_identity,
		physical_operation_state,physical_operation_id,physical_operation_scope,physical_operation,physical_source_key,physical_settlement_receipt)
		VALUES($1,$2,$3,$4,$5,ARRAY[gen_random_uuid()],'message',0,$6,1,gen_random_uuid(),gen_random_uuid(),$7,now()-interval '1 hour',now()-interval '1 minute',CASE WHEN $7::text='closed' THEN now() END,
		'{"synthetic":"target","version":1}','{"synthetic":"identity","version":1}',$8,
		CASE WHEN $8::text<>'none' THEN gen_random_uuid() END,
		CASE WHEN $8::text<>'none' THEN 'whole_session' END,
		CASE WHEN $8::text<>'none' THEN '{"synthetic":"operation","version":1}'::jsonb END,
		CASE WHEN $8::text<>'none' THEN 'synthetic/source' END,
		CASE WHEN $8::text IN ('settled','fenced_drained') THEN '{"synthetic":"settlement"}'::jsonb END)`, id, org, session, lane, job, laneEpoch, state, physical)
	require.NoError(t, err, "seed one current preparation with expired execution budget and separately recorded physical operation")
	return id
}

func lifecyclePreparationSnapshot(t *testing.T, pool *pgxpool.Pool, org, id uuid.UUID) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT to_jsonb(p) FROM session_runtime_start_preparations p WHERE org_id=$1 AND id=$2`, org, id).Scan(&raw), "read the complete exact-tenant retained preparation")
	return raw
}

func syntheticLifecycleRecoveryEnvelope() json.RawMessage {
	return json.RawMessage(`{"schema_version":1,"version":1,"session_epoch":0,"state":"uncertain","reservation":{"synthetic":"reservation","version":1},"physical_operation_state":"unknown","physical_operation":{"synthetic":"operation","version":1}}`)
}

func seedLifecycleWorkspaceRecovery(t *testing.T, pool *pgxpool.Pool, org, session uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE sessions SET workspace_recovery_id=gen_random_uuid(),workspace_recovery_token=gen_random_uuid(),workspace_recovery=$3,workspace_recovery_next_attempt_at=now()-interval '1 hour' WHERE org_id=$1 AND id=$2`, org, session, syntheticLifecycleRecoveryEnvelope())
	require.NoError(t, err, "retain whole-session recovery independently of expired retry/owner timing")
}

func lifecycleWorkspaceRecoverySnapshot(t *testing.T, pool *pgxpool.Pool, org, session uuid.UUID) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT jsonb_build_object('id',workspace_recovery_id,'token',workspace_recovery_token,'snapshot',workspace_recovery,'due',workspace_recovery_next_attempt_at) FROM sessions WHERE org_id=$1 AND id=$2`, org, session).Scan(&raw), "read every current recovery field without treating it as execution authority")
	return raw
}

func TestLifecycleReservationLegacyCascadeResetPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		preparation, recovery bool
	}{
		{name: "legacy rows without new reservations"},
		{name: "retained preparation", preparation: true},
		{name: "retained recovery", recovery: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, session, lane := seedRuntimeSeedThread(t, pool)
			job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
			if tt.preparation {
				seedLifecyclePreparation(t, pool, org, session, &lane, job, "closed", "unknown")
			}
			if tt.recovery {
				seedLifecycleWorkspaceRecovery(t, pool, org, session)
			}
			_, err := pool.Exec(context.Background(), `TRUNCATE jobs,sessions,organizations CASCADE`)
			if tt.preparation || tt.recovery {
				requireSessionLifecycleSQLState(t, err, "55000", "cascade reset must retain unresolved preparation and recovery evidence")
			} else {
				require.NoError(t, err, "ordinary legacy integration reset must remain available when no new reservation exists")
			}
			var count int
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE org_id=$1 AND id=$2`, org, session).Scan(&count), "read exact session existence after the atomic cascade attempt")
			want := 0
			if tt.preparation || tt.recovery {
				want = 1
			}
			require.Equal(t, want, count, "an inactive legacy reset may complete but retained physical reservation prevents every cascade effect")
		})
	}
}
