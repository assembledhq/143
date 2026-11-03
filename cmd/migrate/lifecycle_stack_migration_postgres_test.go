package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestLifecycleStackMainCompatibilityRoundTripPostgres(t *testing.T) {
	t.Parallel()
	// The existing fixture reserves exactly three connections: administrator,
	// real golang-migrate driver, and inspector. All dependent boundaries share
	// this isolated database; no step holds a transaction across concurrent DDL.
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	require.NoError(t, migration.Migrate(345), "install the actual main migration before the renumbered lifecycle stack")
	f := seedCallerIntentMigrationLegacy(t, ctx, conn)
	metadata := seedCallerDispatchStatusMigrationMetadata(t, ctx, conn, f)
	dispute := uuid.New()
	result, err := conn.Exec(ctx, `INSERT INTO code_review_decision_disputes
(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,
decision,source,source_body_hash,body,reassessment_status,semantic_input_hash_at_filing)
SELECT $1,org_id,session_id,pull_request_id,repository_id,policy_id,head_sha,
'blocked','api','synthetic-dispute-body','Synthetic retained objection','superseded','synthetic-dispute-input'
FROM code_review_session_metadata WHERE org_id=$2 AND id=$3`, dispute, f.org, metadata)
	require.NoError(t, err, "main migration 345 must admit a genuine superseded dispute before the lifecycle stack exists")
	require.Equal(t, int64(1), result.RowsAffected(), "the compatibility fixture must contain one concrete retained main-schema dispute")

	// Capture every existing field rather than normalize a handpicked subset.
	// Later additive columns are checked separately for passive empty defaults.
	tables := []string{
		"organizations", "integrations", "repositories", "pull_requests", "code_review_policies",
		"sessions", "session_threads", "code_review_session_metadata", "code_review_agent_results",
		"session_review_loops", "session_review_loop_passes", "code_review_decision_disputes",
		"automation_runs", "session_publications", "jobs", "thread_runtimes", "session_input_dispatches",
	}
	columns := make(map[string][]string, len(tables))
	for _, table := range tables {
		var names []string
		err := conn.QueryRow(ctx, `SELECT array_agg(column_name::text ORDER BY ordinal_position)
FROM information_schema.columns WHERE table_schema='public' AND table_name=$1`, table).Scan(&names)
		require.NoError(t, err, "capture the complete main-schema column set for each retained synthetic relation")
		require.NotEmpty(t, names, "every selected legacy relation must actually exist at main migration 345")
		columns[table] = names
	}
	snapshot := func() []json.RawMessage {
		values := make([]json.RawMessage, 0, len(tables))
		for _, table := range tables {
			scope := "org_id"
			if table == "organizations" {
				scope = "id"
			}
			var value json.RawMessage
			err := conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(projected.row ORDER BY projected.row::text),'[]'::jsonb)
FROM (SELECT (SELECT jsonb_object_agg(key,value) FROM jsonb_each(to_jsonb(r))
WHERE key=ANY($2::text[])) AS row FROM `+pgx.Identifier{table}.Sanitize()+` r
WHERE `+pgx.Identifier{scope}.Sanitize()+`=$1) projected`, f.org, columns[table]).Scan(&value)
			require.NoError(t, err, "read every original legacy field without float64 conversion or loss of integer precision")
			values = append(values, value)
		}
		return values
	}
	before := snapshot()
	disputeConstraint := func() string {
		var definition string
		err := conn.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
WHERE conrelid='code_review_decision_disputes'::regclass
AND conname='chk_code_review_decision_disputes_reassessment_status' AND convalidated`).Scan(&definition)
		require.NoError(t, err, "main's exact validated superseded-status constraint must remain installed")
		return definition
	}
	constraintBefore := disputeConstraint()
	catalog := func() map[string][][]any {
		var value json.RawMessage
		err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
'columns',(SELECT jsonb_agg(jsonb_build_array(r.relname,a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,pg_get_expr(d.adbin,d.adrelid),a.attidentity,a.attgenerated) ORDER BY r.relname,a.attnum) FROM pg_attribute a JOIN pg_class r ON r.oid=a.attrelid LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE r.relnamespace='public'::regnamespace AND r.relkind IN ('r','p','v','m','f') AND a.attnum>0 AND NOT a.attisdropped),
'constraints',(SELECT jsonb_agg(jsonb_build_array(r.relname,c.conname,pg_get_constraintdef(c.oid),c.convalidated,c.condeferrable,c.condeferred) ORDER BY r.relname,c.conname) FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid WHERE c.connamespace='public'::regnamespace),
'indexes',(SELECT jsonb_agg(jsonb_build_array(ix.relname,pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready,i.indislive) ORDER BY ix.relname) FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid WHERE ix.relnamespace='public'::regnamespace),
'triggers',(SELECT jsonb_agg(jsonb_build_array(r.relname,t.tgname,t.tgenabled,pg_get_triggerdef(t.oid)) ORDER BY r.relname,t.tgname) FROM pg_trigger t JOIN pg_class r ON r.oid=t.tgrelid WHERE NOT t.tgisinternal AND r.relnamespace='public'::regnamespace),
'functions',(SELECT jsonb_agg(jsonb_build_array(p.proname,pg_get_function_identity_arguments(p.oid),pg_get_functiondef(p.oid)) ORDER BY p.proname,pg_get_function_identity_arguments(p.oid)) FROM pg_proc p WHERE p.pronamespace='public'::regnamespace AND p.prokind IN ('f','p')))`).Scan(&value)
		require.NoError(t, err, "snapshot semantic public-schema definitions without unstable object IDs, generated FK triggers or sequence values")
		var decoded map[string][][]any
		require.NoError(t, json.Unmarshal(value, &decoded), "decode catalog definitions so a mismatch identifies one readable schema object")
		return decoded
	}
	requireCatalog := func(expected, actual map[string][][]any, message string) {
		require.Equal(t, len(expected), len(actual), "%s: preserve all catalog categories", message)
		for _, category := range []string{"columns", "constraints", "indexes", "triggers", "functions"} {
			rows, ok := actual[category]
			require.True(t, ok, "%s: retain catalog category %s", message, category)
			require.Equal(t, len(expected[category]), len(rows), "%s: retain every %s object", message, category)
			for index, object := range expected[category] {
				// Retain exact SQL ordering, including column order, and exact
				// function source. Decode only to make failures readable.
				require.Equal(t, object, rows[index], "%s: catalog %s object %v", message, category, object[:2])
			}
		}
	}
	mainCatalog := catalog()
	var installedCatalog map[string][][]any
	assertBoundary := func(version uint) {
		requireCallerIntentMigrationLedger(t, ctx, migration, conn, version, false)
		require.Equal(t, before, snapshot(), "every original synthetic business field and row must survive the complete inactive stack roundtrip")
		require.Equal(t, constraintBefore, disputeConstraint(), "the lifecycle stack and its rollback must preserve main's exact dispute allowance")
		var status string
		require.NoError(t, conn.QueryRow(ctx, `SELECT reassessment_status FROM code_review_decision_disputes
WHERE org_id=$1 AND id=$2`, f.org, dispute).Scan(&status), "independently read the retained main-schema dispute at each committed boundary")
		require.Equal(t, "superseded", status, "rolling back the lifecycle stack must never roll back or rewrite main migration 345")
		var participants [5]int64
		err := conn.QueryRow(ctx, `SELECT
(SELECT count(*) FROM sessions s WHERE org_id=$1 AND (lifecycle_version<>0
 OR to_jsonb(s)->>'workspace_recovery_id' IS NOT NULL OR to_jsonb(s)->>'workspace_recovery_token' IS NOT NULL
 OR to_jsonb(s)->>'workspace_recovery' IS NOT NULL OR to_jsonb(s)->>'workspace_recovery_next_attempt_at' IS NOT NULL)),
(SELECT count(*) FROM session_threads WHERE org_id=$1 AND lifecycle_version<>0),
(SELECT count(*) FROM session_lifecycle_cutovers WHERE org_id=$1),
(SELECT count(*) FROM session_input_dispatches WHERE org_id=$1),
(SELECT count(*) FROM session_input_workspace_compatibility WHERE org_id=$1)`, f.org).
			Scan(&participants[0], &participants[1], &participants[2], &participants[3], &participants[4])
		require.NoError(t, err, "inspect activation and retained lifecycle evidence without invoking any lifecycle writer")
		require.Equal(t, [5]int64{}, participants, "installing or rolling back the stack must not activate sessions or manufacture lifecycle participants")
		if version >= 360 {
			var preparations int64
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM session_runtime_start_preparations WHERE org_id=$1`, f.org).Scan(&preparations), "inspect the new runtime preparation relation only after its actual migration exists")
			require.Zero(t, preparations, "the passive stack must not allocate runtime preparations")
		}
		ready := version == 392
		requireCallerIntentMigrationCatalog(t, ctx, conn, version, ready, ready, ready)
		if version == 345 {
			requireCatalog(mainCatalog, catalog(), "inactive stack rollback must restore main's complete semantic public catalog")
		}
		if ready {
			requireCallerDispatchStatusMigrationCatalog(t, ctx, conn, version, true, true)
			currentCatalog := catalog()
			if installedCatalog == nil {
				installedCatalog = currentCatalog
			} else {
				requireCatalog(installedCatalog, currentCatalog, "reapplying the stack must recreate the exact same complete semantic public catalog")
			}
		}
	}
	assertBoundary(345)
	steps := []struct {
		name    string
		version uint
	}{
		{name: "protocol retention", version: 348},
		{name: "consumer ownership", version: 380},
		{name: "complete passive stack", version: 392},
		{name: "rollback only the unpublished stack", version: 345},
		{name: "reapply the complete passive stack", version: 392},
	}
	// These are sequential state transitions of one real database, so they
	// cannot be independent parallel subtests or use synthetic ledger writes.
	for _, step := range steps {
		require.NoError(t, migration.Migrate(step.version), "complete the real driver transition for %s", step.name)
		assertBoundary(step.version)
	}
}
