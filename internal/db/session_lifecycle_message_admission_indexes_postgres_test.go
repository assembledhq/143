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
	"github.com/stretchr/testify/require"
)

type admissionLookupIndexCatalog struct {
	Name, Table, Method      string
	Keys                     []string
	Predicate                string
	KeyCount, AttributeCount int
	Unique, Valid, Ready     bool
}

func admissionLookupIndexNames() []string {
	return []string{"idx_admission_active_repair_lookup", "idx_admission_pending_question_lookup"}
}

func readAdmissionLookupIndexCatalog(t *testing.T, ctx context.Context, q DBTX) []admissionLookupIndexCatalog {
	t.Helper()
	rows, err := q.Query(ctx, "SELECT ix.relname,tab.relname,am.amname,ARRAY(SELECT regexp_replace(pg_get_indexdef(i.indexrelid,k.n::integer,false),'\\s+',' ','g') FROM unnest(i.indkey) WITH ORDINALITY k(attnum,n) ORDER BY k.n),regexp_replace(COALESCE(pg_get_expr(i.indpred,i.indrelid),''),'\\s+',' ','g'),i.indnkeyatts::integer,i.indnatts::integer,i.indisunique,i.indisvalid,i.indisready FROM unnest($1::text[]) wanted(name) JOIN pg_class ix ON ix.oid=to_regclass('public.'||wanted.name) JOIN pg_index i ON i.indexrelid=ix.oid JOIN pg_class tab ON tab.oid=i.indrelid JOIN pg_am am ON am.oid=ix.relam ORDER BY ix.relname", admissionLookupIndexNames())
	require.NoError(t, err, "inspect actual key order partial predicates included attributes access method uniqueness and concurrent-build readiness")
	actual, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (admissionLookupIndexCatalog, error) {
		var v admissionLookupIndexCatalog
		err := row.Scan(&v.Name, &v.Table, &v.Method, &v.Keys, &v.Predicate, &v.KeyCount, &v.AttributeCount, &v.Unique, &v.Valid, &v.Ready)
		return v, err
	})
	require.NoError(t, err, "collect each installed admission index rather than trust its name")
	return actual
}

func TestMessageAdmissionLookupIndexCatalogPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	expected := []admissionLookupIndexCatalog{
		{Name: "idx_admission_active_repair_lookup", Table: "pull_request_repair_runs", Method: "btree", Keys: []string{"( CASE WHEN active THEN org_id ELSE NULL::uuid END)", "( CASE WHEN active THEN session_id ELSE NULL::uuid END)", "id"}, Predicate: "( CASE WHEN active THEN org_id ELSE NULL::uuid END IS NOT NULL)", KeyCount: 3, AttributeCount: 3, Valid: true, Ready: true},
		{Name: "idx_admission_pending_question_lookup", Table: "session_questions", Method: "btree", Keys: []string{"( CASE WHEN (status = 'pending'::text) THEN org_id ELSE NULL::uuid END)", "( CASE WHEN (status = 'pending'::text) THEN session_id ELSE NULL::uuid END)"}, Predicate: "( CASE WHEN (status = 'pending'::text) THEN org_id ELSE NULL::uuid END IS NOT NULL)", KeyCount: 2, AttributeCount: 2, Valid: true, Ready: true},
	}
	require.Equal(t, expected, readAdmissionLookupIndexCatalog(t, context.Background(), pool), "both nonunique partial indexes must contain only their exact scoped keys and finish valid and ready")
}

func TestMessageAdmissionLookupRollbackFloorPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, kind string }{
		{"positive parent without cutover", "positive"},
		{"active cutover without positive stamp", "active"},
		{"accepted waiting ordinary dispatch", "waiting"},
		{"retained workspace recovery", "recovery"},
		{"terminal job retained by runtime", "runtime"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			org, session, lane := seedRuntimeSeedThread(t, pool)
			switch tt.kind {
			case "positive":
				predicateExec(t, pool, "UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2", org, session)
			case "active":
				insertLifecycleCutoverPostgresRow(t, ctx, pool, lifecycleCutoverObservation{orgID: org, sessionID: session, version: 1, state: models.LifecycleCutoverStateActive})
			case "waiting":
				predicateExec(t, pool, "INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,next_attempt_at) VALUES(gen_random_uuid(),$1,$2,$3,'message','synthetic_waiting','synthetic-waiting',repeat('a',64),'{}',1,'waiting','none',now())", org, session, lane)
			case "recovery":
				seedLifecycleWorkspaceRecovery(t, pool, org, session)
			case "runtime":
				job := seedLifecycleConsumerJob(t, pool, org, models.JobStatusFailed)
				runtime := createSeedRuntime(t, pool, org, session, lane)
				predicateExec(t, pool, "UPDATE thread_runtimes SET job_id=$3,job_attempt_token=gen_random_uuid(),allocation_digest=repeat('a',64),allocation_expected_turn=0,input_dispatch_ids=ARRAY[gen_random_uuid()],admission_session_epoch=0,admission_thread_epoch=0,provider_invocation_ordinal=0 WHERE org_id=$1 AND id=$2", org, runtime.ID, job)
			}
			beforeRows := lifecycleIndexRollbackTenantRows(t, ctx, pool, org)
			beforeIndexes := readAdmissionLookupIndexCatalog(t, ctx, pool)
			require.Len(t, beforeIndexes, 2, "both new indexes must exist before attempting the highest down")
			raw, err := os.ReadFile("../../migrations/000383_admission_lookup_rollback_floor.down.sql")
			require.NoError(t, err, "read the actual highest rollback file rather than duplicate its guard")
			_, err = pool.Exec(ctx, string(raw))
			requireSessionLifecycleSQLState(t, err, "55000", "the first supported down must refuse before either concurrent index drop")
			require.Equal(t, beforeRows, lifecycleIndexRollbackTenantRows(t, ctx, pool, org), "failed preflight must preserve all retained tenant fields byte-for-byte")
			require.Equal(t, beforeIndexes, readAdmissionLookupIndexCatalog(t, ctx, pool), "failed preflight must preserve both exact index shapes and readiness")
		})
	}
}

// A compact fingerprint keeps this helper available to the resource proof
// without serializing large public-output histories.
func admissionLookupHistoryFingerprint(t *testing.T, ctx context.Context, q DBTX, org, foreign uuid.UUID) [2]json.RawMessage {
	t.Helper()
	var out [2]json.RawMessage
	for i, table := range []string{"pull_request_repair_runs", "session_questions"} {
		query := "SELECT jsonb_build_array(count(*),COALESCE(sum(hashtextextended(to_jsonb(r)::text,0)::numeric),0)::text,COALESCE(sum(hashtextextended(to_jsonb(r)::text,17)::numeric),0)::text) FROM " + table + " r WHERE org_id IN ($1,$2)"
		require.NoError(t, q.QueryRow(ctx, query, org, foreign).Scan(&out[i]), "fingerprint exact count and every synthetic history field without publishing rows")
	}
	return out
}
