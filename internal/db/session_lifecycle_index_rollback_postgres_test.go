package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// The supported sequential down path starts with 380 as one standalone Exec,
// before any concurrent index removal in 379..367. This tests that first
// refusal boundary, not a migration bypass or production activation procedure.
// The existing inactive round-trip test separately covers successful rollback.
func TestLifecycleConsumerIndexRollbackFloorPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, evidence, preparationState, physicalState, errorText string
	}{
		{name: "retained positive stamp without active cutover", evidence: "positive", errorText: "LIFECYCLE_GUARD_PROVENANCE_REQUIRED"},
		{name: "active cutover without positive parent stamp", evidence: "active", errorText: "LIFECYCLE_GUARD_PROVENANCE_REQUIRED"},
		{name: "expired retained workspace recovery", evidence: "recovery", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "expired preparing row", evidence: "preparation", preparationState: "preparing", physicalState: "none", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "expired authorized row", evidence: "preparation", preparationState: "authorized", physicalState: "none", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "closed revoked preparation without physical operation", evidence: "preparation", preparationState: "closed", physicalState: "none", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "closed preparation with structural settlement", evidence: "preparation", preparationState: "closed", physicalState: "settled", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "bound dispatch retains terminal job", evidence: "dispatch", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "runtime allocation retains terminal job without dispatch", evidence: "runtime", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "parked job without dispatch", evidence: "parked", errorText: "retained lifecycle consumers prevent index rollback"},
		{name: "terminal snapshot cleanup obligation", evidence: "cleanup", errorText: "retained lifecycle consumers prevent index rollback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			org, session, lane := seedRuntimeSeedThread(t, pool)
			var duplicateRecoverySession uuid.UUID
			switch tt.evidence {
			case "positive":
				_, err := pool.Exec(ctx, "UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2", org, session)
				require.NoError(t, err, "retain a positive synthetic parent stamp without inventing an active cutover or disabling provenance guards")
			case "active":
				// Deliberately raw synthetic active metadata with an unstamped
				// parent is retained evidence, not validated activation authority.
				insertLifecycleCutoverPostgresRow(t, ctx, pool, lifecycleCutoverObservation{orgID: org, sessionID: session, version: 1, state: models.LifecycleCutoverStateActive})
			case "recovery":
				seedLifecycleWorkspaceRecovery(t, pool, org, session)
				duplicateRecoverySession = seedLifecycleConsumerSession(t, pool, org).ID
			case "preparation":
				job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
				seedLifecyclePreparation(t, pool, org, session, &lane, job, tt.preparationState, tt.physicalState)
			case "dispatch":
				job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
				_, err := insertLifecycleConsumerDispatch(pool, org, session, lane, job, nil, nil)
				require.NoError(t, err, "retain a real bound input and its terminal job independently of status-based liveness")
			case "runtime":
				job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
				runtime := createSeedRuntime(t, pool, org, session, lane)
				_, err := pool.Exec(ctx, `UPDATE thread_runtimes SET job_id=$3,job_attempt_token=gen_random_uuid(),allocation_digest=repeat('a',64),
				 allocation_expected_turn=0,input_dispatch_ids=ARRAY[gen_random_uuid()],admission_session_epoch=0,
				 admission_thread_epoch=0,provider_invocation_ordinal=0 WHERE org_id=$1 AND id=$2`, org, runtime.ID, job)
				require.NoError(t, err, "retain exact structural allocation fields and a real terminal job without adding a dispatch that could mask the runtime branch")
			case "parked":
				seedLifecycleConsumerJob(t, pool, org, models.JobStatusAwaitingStartControl)
			case "cleanup":
				job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusSucceeded)
				_, err := pool.Exec(ctx, "UPDATE jobs SET job_type='snapshot_object_cleanup' WHERE org_id=$1 AND id=$2", org, job)
				require.NoError(t, err, "retain an exact-object cleanup obligation independently of terminal status")
			}
			beforeRows := lifecycleIndexRollbackTenantRows(t, ctx, pool, org)
			beforeIndexes := lifecycleIndexRollbackCatalog(t, ctx, pool)
			expectedNames := lifecycleIndexRollbackNames()
			var actualNames []string
			expectedIndexes := append([]lifecycleIndexRollbackIndex(nil), beforeIndexes...)
			for i := range expectedIndexes {
				actualNames = append(actualNames, expectedIndexes[i].name)
				require.NotEmpty(t, expectedIndexes[i].definition, "every supporting index must have a complete installed definition before testing rollback refusal")
				expectedIndexes[i].unique = expectedIndexes[i].name == "idx_sessions_workspace_recovery_id"
				expectedIndexes[i].valid, expectedIndexes[i].ready = true, true
			}
			require.Equal(t, expectedNames, actualNames, "the test must cover every exact preparation and standalone consumer lookup index without silently skipping a missing index")
			require.Equal(t, expectedIndexes, beforeIndexes, "the preflight fixture must start with the complete valid and ready index floor and exactly one retained-recovery unique index")
			migration, err := os.ReadFile("../../migrations/000380_lifecycle_consumer_index_rollback_floor.down.sql")
			require.NoError(t, err, "read the actual first shipped rollback preflight rather than duplicate its guard policy")
			// No explicit transaction wraps this call: concurrent index downs
			// use the same standalone Exec boundary in the supported sequence.
			_, err = pool.Exec(ctx, string(migration))
			requireSessionLifecycleSQLState(t, err, "55000", "supported sequential rollback must stop at 380 before reaching any concurrent index removal")
			require.ErrorContains(t, err, tt.errorText, "rollback must refuse because of this exact retained provenance or consumer evidence, not an incidental SQL or isolation failure")
			require.Equal(t, beforeRows, lifecycleIndexRollbackTenantRows(t, ctx, pool, org), "failed standalone preflight must preserve every raw tenant session, job, dispatch, runtime, preparation and provenance field")
			require.Equal(t, beforeIndexes, lifecycleIndexRollbackCatalog(t, ctx, pool), "preflight refusal must leave every supporting index definition, uniqueness, validity and readiness byte-for-byte unchanged")
			if duplicateRecoverySession != uuid.Nil {
				_, err := pool.Exec(ctx, `UPDATE sessions candidate SET workspace_recovery_id=retained.workspace_recovery_id,
				 workspace_recovery_token=gen_random_uuid(),workspace_recovery=retained.workspace_recovery,
				 workspace_recovery_next_attempt_at=retained.workspace_recovery_next_attempt_at
				 FROM sessions retained WHERE candidate.org_id=$1 AND candidate.id=$2
				 AND retained.org_id=$1 AND retained.id=$3`, org, duplicateRecoverySession, session)
				requireSessionLifecycleSQLState(t, err, "23505", "the recovery identity unique index must still reject a duplicate retained reservation after the rollback preflight refuses")
				var uniqueError *pgconn.PgError
				require.ErrorAs(t, err, &uniqueError, "duplicate retained recovery must report an actual PostgreSQL uniqueness violation")
				require.Equal(t, "idx_sessions_workspace_recovery_id", uniqueError.ConstraintName, "the surviving recovery index, not an unrelated uniqueness constraint, must reject the duplicate")
				require.Equal(t, beforeRows, lifecycleIndexRollbackTenantRows(t, ctx, pool, org), "a rejected duplicate-recovery probe must leave every tenant row unchanged too")
				require.Equal(t, beforeIndexes, lifecycleIndexRollbackCatalog(t, ctx, pool), "the real uniqueness probe must not replace, rebuild or weaken any supporting index")
			}
		})
	}
}

type lifecycleIndexRollbackIndex struct {
	name, definition     string
	unique, valid, ready bool
}

func lifecycleIndexRollbackNames() []string {
	return []string{
		"idx_automation_consumer_job", "idx_automation_consumer_target", "idx_automation_consumer_thread",
		"idx_dispatch_consumer_thread", "idx_executor_consumer_session", "idx_executor_consumer_thread",
		"idx_holder_consumer_runtime", "idx_holder_consumer_session", "idx_jobs_lifecycle_nonterminal_input_consumers", "idx_recheck_consumer_session", "idx_runtime_consumer_thread",
		"idx_sessions_workspace_recovery_due", "idx_sessions_workspace_recovery_id",
		"idx_start_preparations_request", "idx_start_preparations_scope", "idx_start_preparations_thread",
	}
}

func lifecycleIndexRollbackCatalog(t *testing.T, ctx context.Context, database DBTX) []lifecycleIndexRollbackIndex {
	t.Helper()
	rows, err := database.Query(ctx, `SELECT wanted.name,COALESCE(pg_get_indexdef(ix.oid),''),
	 COALESCE(i.indisunique,false),COALESCE(i.indisvalid,false),COALESCE(i.indisready,false)
	 FROM unnest($1::text[]) wanted(name)
	 LEFT JOIN pg_class ix ON ix.oid=to_regclass('public.'||wanted.name)
	 LEFT JOIN pg_index i ON i.indexrelid=ix.oid ORDER BY wanted.name`, lifecycleIndexRollbackNames())
	require.NoError(t, err, "read exact public index definitions and unique/valid/ready flags without assuming a name implies an installed index")
	indexes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleIndexRollbackIndex, error) {
		var index lifecycleIndexRollbackIndex
		err := row.Scan(&index.name, &index.definition, &index.unique, &index.valid, &index.ready)
		return index, err
	})
	require.NoError(t, err, "collect every expected supporting index including explicit missing-index placeholders")
	return indexes
}

func lifecycleIndexRollbackTenantRows(t *testing.T, ctx context.Context, database DBTX, orgID uuid.UUID) json.RawMessage {
	t.Helper()
	var rows json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
	 'sessions',(SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]'::jsonb) FROM sessions s WHERE s.org_id=$1),
	 'jobs',(SELECT COALESCE(jsonb_agg(to_jsonb(j) ORDER BY j.id),'[]'::jsonb) FROM jobs j WHERE j.org_id=$1),
	 'dispatches',(SELECT COALESCE(jsonb_agg(to_jsonb(d) ORDER BY d.id),'[]'::jsonb) FROM session_input_dispatches d WHERE d.org_id=$1),
	 'runtimes',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM thread_runtimes r WHERE r.org_id=$1),
	 'preparations',(SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY p.id),'[]'::jsonb) FROM session_runtime_start_preparations p WHERE p.org_id=$1),
	 'cutovers',(SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.session_id),'[]'::jsonb) FROM session_lifecycle_cutovers c WHERE c.org_id=$1),
	 'threads',(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb) FROM session_threads t WHERE t.org_id=$1))`, orgID).Scan(&rows)
	require.NoError(t, err, "snapshot every tenant row and column touched or retained by the rollback floor, including inactive siblings and cutover metadata")
	return rows
}
