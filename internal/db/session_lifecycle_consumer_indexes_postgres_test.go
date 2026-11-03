package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestLifecycleConsumerLookupIndexCatalogPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	type index struct {
		Name, Table          string
		Columns              []string
		Predicate            string
		Unique, Valid, Ready bool
	}
	expected := []index{
		{Name: "idx_automation_consumer_job", Table: "automation_runs", Columns: []string{"org_id", "job_id", "id"}, Predicate: "(job_id IS NOT NULL)", Unique: false, Valid: true, Ready: true},
		{Name: "idx_automation_consumer_target", Table: "automation_runs", Columns: []string{"org_id", "target_id", "target_generation", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_automation_consumer_thread", Table: "automation_runs", Columns: []string{"org_id", "thread_id", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_dispatch_consumer_thread", Table: "session_input_dispatches", Columns: []string{"org_id", "thread_id", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_executor_consumer_session", Table: "session_executors", Columns: []string{"org_id", "session_id", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_executor_consumer_thread", Table: "session_executors", Columns: []string{"org_id", "thread_id", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_holder_consumer_runtime", Table: "session_sandbox_holders", Columns: []string{"org_id", "holder_kind", "holder_id", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_holder_consumer_session", Table: "session_sandbox_holders", Columns: []string{"org_id", "session_id", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_jobs_lifecycle_nonterminal_input_consumers", Table: "jobs", Columns: []string{"( CASE WHEN ((job_type = ANY (ARRAY['run_agent'::text, 'continue_session'::text])) AND (status = ANY (ARRAY['pending'::text, 'running'::text, 'awaiting_start_control'::text]))) THEN org_id ELSE NULL::uuid END)", "id"}, Predicate: "( CASE WHEN ((job_type = ANY (ARRAY['run_agent'::text, 'continue_session'::text])) AND (status = ANY (ARRAY['pending'::text, 'running'::text, 'awaiting_start_control'::text]))) THEN org_id ELSE NULL::uuid END IS NOT NULL)", Unique: false, Valid: true, Ready: true},
		{Name: "idx_recheck_consumer_session", Table: "code_review_recheck_dispatches", Columns: []string{"org_id", "session_id", "assessment_id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_runtime_consumer_thread", Table: "thread_runtimes", Columns: []string{"org_id", "thread_id", "id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_sessions_workspace_recovery_due", Table: "sessions", Columns: []string{"workspace_recovery_next_attempt_at", "id"}, Predicate: "(workspace_recovery_id IS NOT NULL)", Unique: false, Valid: true, Ready: true},
		{Name: "idx_sessions_workspace_recovery_id", Table: "sessions", Columns: []string{"org_id", "workspace_recovery_id"}, Predicate: "(workspace_recovery_id IS NOT NULL)", Unique: true, Valid: true, Ready: true},
		{Name: "idx_start_preparations_request", Table: "session_runtime_start_preparations", Columns: []string{"org_id", "session_id", "request_id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_start_preparations_scope", Table: "session_runtime_start_preparations", Columns: []string{"org_id", "session_id", "thread_id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
		{Name: "idx_start_preparations_thread", Table: "session_runtime_start_preparations", Columns: []string{"org_id", "thread_id"}, Predicate: "", Unique: false, Valid: true, Ready: true},
	}
	names := make([]string, len(expected))
	for n, item := range expected {
		names[n] = item.Name
	}
	rows, err := pool.Query(context.Background(), `SELECT ix.relname,tab.relname,
  ARRAY(SELECT regexp_replace(pg_get_indexdef(i.indexrelid,k.n::integer,false),'\s+',' ','g') FROM unnest(i.indkey) WITH ORDINALITY k(attnum,n) ORDER BY k.n),
  COALESCE(regexp_replace(pg_get_expr(i.indpred,i.indrelid),'\s+',' ','g'),''),i.indisunique,i.indisvalid,i.indisready
  FROM unnest($1::text[]) wanted(name)
  JOIN pg_class ix ON ix.oid=to_regclass(wanted.name)
  JOIN pg_index i ON i.indexrelid=ix.oid
  JOIN pg_class tab ON tab.oid=i.indrelid
  ORDER BY ix.relname`, names)
	require.NoError(t, err, "inspect actual index keys, predicates and concurrent-build readiness rather than trusting names or source")
	actual, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (index, error) {
		var value index
		err := row.Scan(&value.Name, &value.Table, &value.Columns, &value.Predicate, &value.Unique, &value.Valid, &value.Ready)
		return value, err
	})
	require.NoError(t, err, "collect every installed scope, reverse-evidence, retention-FK and recovery index")
	require.Equal(t, expected, actual, "all-status discovery and child-FK indexes must be complete, while recovery and nonnull-job predicates must remain exact and every concurrent build valid")
}
