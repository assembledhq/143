package db

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func seedMaintenanceInbound(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	org, integration, installation := uuid.New(), uuid.New(), uuid.New()
	maintenanceExec(t, pool, `INSERT INTO organizations VALUES($1)`, org)
	maintenanceExec(t, pool, `INSERT INTO integrations VALUES($1,$2)`, integration, org)
	maintenanceExec(t, pool, `INSERT INTO slack_installations VALUES($1,$2)`, installation, org)
	return org, integration, installation
}

func insertMaintenanceWebhook(t *testing.T, pool *pgxpool.Pool, org, integration uuid.UUID, old bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	created := time.Now().UTC()
	if old {
		created = created.Add(-40 * 24 * time.Hour)
	}
	maintenanceExec(t, pool, `INSERT INTO webhook_deliveries(id,org_id,integration_id,provider,event_type,created_at,payload) VALUES($1,$2,$3,'slack','test',$4,'{"retained":true}')`, id, org, integration, created)
	return id
}

func insertMaintenanceJob(t *testing.T, pool *pgxpool.Pool, org uuid.UUID, status string, old bool, jobType string, payload json.RawMessage) uuid.UUID {
	t.Helper()
	id := uuid.New()
	updated := time.Now().UTC()
	if old {
		updated = updated.Add(-40 * 24 * time.Hour)
	}
	maintenanceExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,status,updated_at,payload) VALUES($1,$2,'maintenance',$3,$4,$5,$6)`, id, org, jobType, status, updated, payload)
	return id
}

func TestRetentionPostgresWebhookReferences(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	org, integration, installation := seedMaintenanceInbound(t, pool)
	foreign, _, foreignInstallation := seedMaintenanceInbound(t, pool)
	keep := []string{}
	for _, status := range []string{"received", "enqueued", "processed", "failed", "ignored"} {
		id := insertMaintenanceWebhook(t, pool, org, integration, true)
		maintenanceExec(t, pool, `INSERT INTO slack_inbound_events(org_id,slack_installation_id,slack_team_id,event_type,payload,status,webhook_delivery_id,received_at) VALUES($1,$2,'team','test','{}',$3,$4,now()-interval '1 day')`, org, installation, status, id)
		keep = append(keep, id.String())
	}
	for _, status := range []string{"received", "processed", "failed", "ignored"} {
		id := insertMaintenanceWebhook(t, pool, org, integration, true)
		maintenanceExec(t, pool, `INSERT INTO pagerduty_inbound_events(org_id,provider_event_id,event_type,payload,status,webhook_delivery_id,created_at) VALUES($1,$2,'test','{}',$3,$4,now()-interval '80 days')`, org, id.String(), status, id)
		keep = append(keep, id.String())
	}
	crossOrg := insertMaintenanceWebhook(t, pool, org, integration, true)
	maintenanceExec(t, pool, `INSERT INTO slack_inbound_events(org_id,slack_installation_id,slack_team_id,event_type,payload,webhook_delivery_id) VALUES($1,$2,'team','test','{}',$3)`, foreign, foreignInstallation, crossOrg)
	keep = append(keep, crossOrg.String())
	recent := insertMaintenanceWebhook(t, pool, org, integration, false)
	keep = append(keep, recent.String())
	insertMaintenanceWebhook(t, pool, org, integration, true)
	insertMaintenanceWebhook(t, pool, foreign, integration, true)
	slackBefore := maintenanceIDs(t, pool, "slack_inbound_events")
	pagerBefore := maintenanceIDs(t, pool, "pagerduty_inbound_events")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_webhook_deliveries", 0), "zero retention must preserve every parent")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_webhook_deliveries", -1), "negative retention must preserve every parent")
	require.Equal(t, int64(2), maintenanceCleanup(t, pool, "delete_expired_webhook_deliveries", 30), "both orgs' unreferenced expired parents should delete")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_webhook_deliveries", 30), "repeated webhook retention should be idempotent")
	sort.Strings(keep)
	require.Equal(t, keep, maintenanceIDs(t, pool, "webhook_deliveries"), "all exact ledger parents and recent deliveries should remain")
	require.Equal(t, slackBefore, maintenanceIDs(t, pool, "slack_inbound_events"), "Slack history including redacted payloads should remain unchanged")
	require.Equal(t, pagerBefore, maintenanceIDs(t, pool, "pagerduty_inbound_events"), "PagerDuty history should remain unchanged")
}

func TestRetentionPostgresJobsAndReviewGuards(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	completed := time.Now().UTC().Add(-time.Hour)
	f := seedMaintenanceReview(t, pool, &completed)
	foreign := seedMaintenanceReview(t, pool, &completed)
	maintenanceExec(t, pool, `INSERT INTO nodes VALUES('fixture-host')`)
	keep := []string{}
	for _, status := range []string{"starting", "running", "draining", "requeued", "completed", "failed", "lost"} {
		id := insertMaintenanceJob(t, pool, f.org, "failed", true, "continue_session", json.RawMessage(`{}`))
		maintenanceExec(t, pool, `INSERT INTO session_executors(org_id,session_id,job_id,job_type,host_node_id,owner_id,lock_token,status,created_at) VALUES($1,$2,$3,'continue_session','fixture-host','owner',$4,$5,now()-interval '80 days')`, f.org, f.session, id, uuid.New(), status)
		keep = append(keep, id.String())
	}
	crossOrg := insertMaintenanceJob(t, pool, f.org, "succeeded", true, "continue_session", json.RawMessage(`{}`))
	maintenanceExec(t, pool, `INSERT INTO session_executors(org_id,session_id,job_id,job_type,host_node_id,owner_id,lock_token,status) VALUES($1,$2,$3,'continue_session','fixture-host','owner',$4,'completed')`, foreign.org, foreign.session, crossOrg, uuid.New())
	keep = append(keep, crossOrg.String())
	for _, status := range []string{"pending", "running", "cancelled", "dead_letter"} {
		keep = append(keep, insertMaintenanceJob(t, pool, f.org, status, true, "maintenance", json.RawMessage(`{}`)).String())
	}
	keep = append(keep, insertMaintenanceJob(t, pool, f.org, "succeeded", false, "maintenance", json.RawMessage(`{}`)).String())
	for i, status := range []string{"running", "publishing"} {
		assessment := uuid.New()
		key := "retained:" + assessment.String()
		maintenanceExec(t, pool, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,result_origin,status,publication_key)
VALUES($1,$2,$3,'test/repo',$4,$5,$6,$7,$10,'base','main','head',1,'code','contract','intent','visual','request','gate','input','{}','full','initial_full','executed',$8,$9)`, assessment, f.org, f.repository, f.pr, f.metadata, f.session, f.policy, status, key, i+1)
		payload := json.RawMessage(`{"session_id":"` + f.session.String() + `","review_output_key":"` + key + `"}`)
		id := insertMaintenanceJob(t, pool, f.org, "failed", true, "run_code_review", payload)
		keep = append(keep, id.String())
		if status == "running" {
			thread := uuid.New()
			maintenanceExec(t, pool, `INSERT INTO session_threads VALUES($1,$2)`, thread, f.org)
			dispatchJob := insertMaintenanceJob(t, pool, f.org, "succeeded", true, "continue_session", json.RawMessage(`{}`))
			maintenanceExec(t, pool, `INSERT INTO code_review_recheck_dispatches(assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id,status) VALUES($1,$2,$3,$4,$5,$6,1,'digest',1,$7,'completed')`, assessment, f.org, f.repository, f.pr, f.session, thread, dispatchJob)
			keep = append(keep, dispatchJob.String())
		}
		// A matching payload in another org is not a protected full controller.
		insertMaintenanceJob(t, pool, foreign.org, "failed", true, "run_code_review", payload)
	}
	insertMaintenanceJob(t, pool, f.org, "succeeded", true, "maintenance", json.RawMessage(`{}`))
	insertMaintenanceJob(t, pool, f.org, "failed", true, "maintenance", json.RawMessage(`{}`))
	executorsBefore := maintenanceIDs(t, pool, "session_executors")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", 0), "zero job retention must preserve every job")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", -30), "negative job retention must preserve every job")
	require.Equal(t, int64(4), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", 30), "only unrelated succeeded and failed jobs should delete")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", 30), "repeated job retention should be idempotent")
	sort.Strings(keep)
	require.Equal(t, keep, maintenanceIDs(t, pool, "jobs"), "exact executor, review, noneligible and recent job identities should remain")
	require.Equal(t, executorsBefore, maintenanceIDs(t, pool, "session_executors"), "all active and terminal executor history should remain unchanged")
}

func TestRetentionPostgresProtectedRowsDoNotStarveBatches(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, function string }{{"webhook", "delete_expired_webhook_deliveries"}, {"job", "delete_expired_completed_jobs"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, _ := newMaintenancePostgres(t)
			org, integration, installation := seedMaintenanceInbound(t, pool)
			if tt.name == "webhook" {
				maintenanceExec(t, pool, `WITH parents AS (INSERT INTO webhook_deliveries(org_id,integration_id,provider,event_type,created_at) SELECT $1,$2,'slack','test',now()-interval '40 days' FROM generate_series(1,10001) RETURNING id)
INSERT INTO slack_inbound_events(org_id,slack_installation_id,slack_team_id,event_type,payload,webhook_delivery_id) SELECT $1,$3,'team','test','{}',id FROM parents`, org, integration, installation)
				maintenanceExec(t, pool, `INSERT INTO webhook_deliveries(org_id,integration_id,provider,event_type,created_at) SELECT $1,$2,'slack','test',now()-interval '40 days' FROM generate_series(1,10005)`, org, integration)
			} else {
				session := uuid.New()
				maintenanceExec(t, pool, `INSERT INTO sessions VALUES($1,$2);`, session, org)
				maintenanceExec(t, pool, `INSERT INTO nodes VALUES('batch-host')`)
				maintenanceExec(t, pool, `WITH parents AS (INSERT INTO jobs(org_id,queue,job_type,status,updated_at) SELECT $1,'maintenance','test','failed',now()-interval '40 days' FROM generate_series(1,10001) RETURNING id)
INSERT INTO session_executors(org_id,session_id,job_id,job_type,host_node_id,owner_id,lock_token,status) SELECT $1,$2,id,'test','batch-host','owner',gen_random_uuid(),'completed' FROM parents`, org, session)
				maintenanceExec(t, pool, `INSERT INTO jobs(org_id,queue,job_type,status,updated_at) SELECT $1,'maintenance','test','succeeded',now()-interval '40 days' FROM generate_series(1,10005)`, org)
			}
			table := "webhook_deliveries"
			if tt.name == "job" {
				table = "jobs"
			}
			var protected []string
			query := `SELECT id::text FROM webhook_deliveries WHERE id IN (SELECT webhook_delivery_id FROM slack_inbound_events) ORDER BY id`
			if tt.name == "job" {
				query = `SELECT id::text FROM jobs WHERE id IN (SELECT job_id FROM session_executors) ORDER BY id`
			}
			rows, err := pool.Query(context.Background(), query)
			require.NoError(t, err, "read exact protected batch identities")
			protected, err = pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect protected batch identities")
			if tt.name == "job" {
				require.Equal(t, int64(10000), maintenanceCleanup(t, pool, tt.function, 30), "corrected succeeded retention must stop after one 10000-row batch")
				require.Equal(t, int64(5), maintenanceCleanup(t, pool, tt.function, 30), "later cleanup call must finish the remaining eligible jobs")
			} else {
				require.Equal(t, int64(10005), maintenanceCleanup(t, pool, tt.function, 30), "existing webhook cleanup must advance past 10001 protected parents")
			}
			require.Equal(t, protected, maintenanceIDs(t, pool, table), "exact protected parent set should survive all batches")
			require.Equal(t, int64(0), maintenanceCleanup(t, pool, tt.function, 30), "exhausted batch cleanup must be idempotent")
		})
	}
}

func TestRetentionPostgresDownMigrations(t *testing.T) {
	t.Parallel()
	pool, schema := newMaintenancePostgres(t)
	org, _, _ := seedMaintenanceInbound(t, pool)
	succeeded := insertMaintenanceJob(t, pool, org, "succeeded", true, "test", json.RawMessage(`{}`))
	failed := insertMaintenanceJob(t, pool, org, "failed", true, "test", json.RawMessage(`{}`))
	maintenanceApplyFunctions(t, pool, schema, "000307_job_retention_succeeded_status.down.sql")
	require.Equal(t, int64(1), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", 30), "down307 should restore failed-only eligibility under valid job statuses")
	require.Equal(t, []string{succeeded.String()}, maintenanceIDs(t, pool, "jobs"), "down307 must retain the exact succeeded job")
	var source string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.proname='delete_expired_completed_jobs'`, schema).Scan(&source), "read down307 function body")
	require.Contains(t, source, "session_executors", "down307 must retain migration300 executor guards")
	maintenanceApplyFunctions(t, pool, schema, "000300_retention_foreign_key_guards.down.sql")
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.proname='delete_expired_completed_jobs'`, schema).Scan(&source), "read down300 function body")
	require.NotContains(t, source, "session_executors", "down300 should restore immediate pre300 executor semantics")
	require.Contains(t, source, "code_review_recheck_dispatches", "down300 must preserve migration296 recheck guard")
	require.Contains(t, source, "code_review_revision_assessments", "down300 must preserve migration296 staged assessment guard")
	require.NotEqual(t, failed, succeeded, "down migration fixture identities should be distinct")
}

func TestRetentionPostgresConcurrentReferenceFailsClosed(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	org, integration, installation := seedMaintenanceInbound(t, pool)
	parent := insertMaintenanceWebhook(t, pool, org, integration, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	childTx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin concurrent ledger insertion")
	defer func() { _ = childTx.Rollback(context.Background()) }()
	_, err = childTx.Exec(ctx, `INSERT INTO slack_inbound_events(org_id,slack_installation_id,slack_team_id,event_type,payload,webhook_delivery_id) VALUES($1,$2,'team','test','{}',$3)`, org, installation, parent)
	require.NoError(t, err, "uncommitted child should hold the real FK parent lock")
	cleanupConn, err := pool.Acquire(ctx)
	require.NoError(t, err, "reserve separate cleanup connection")
	defer cleanupConn.Release()
	cleanupPID := cleanupConn.Conn().PgConn().PID()
	result := make(chan error, 1)
	go func() {
		var deleted int64
		result <- cleanupConn.QueryRow(ctx, `SELECT delete_expired_webhook_deliveries(30)`).Scan(&deleted)
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, cleanupPID).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "cleanup must reach the deterministic parent-lock race before committing child")
	require.NoError(t, childTx.Commit(ctx), "commit referenced child after cleanup selection snapshot")
	err = <-result
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "concurrent reference must fail explicitly rather than discard history")
	require.Equal(t, "23503", pgErr.Code, "real FK should reject deletion after concurrent child commit")
	require.Equal(t, []string{parent.String()}, maintenanceIDs(t, pool, "webhook_deliveries"), "failed cleanup must preserve the exact referenced parent")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_webhook_deliveries", 30), "next snapshot should exclude newly committed reference")
}

func TestRetentionPostgresSucceededIndex(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	org, _, _ := seedMaintenanceInbound(t, pool)
	maintenanceExec(t, pool, `INSERT INTO jobs(org_id,queue,job_type,status,updated_at) SELECT $1,'maintenance','test','succeeded',now()-interval '40 days' FROM generate_series(1,1000)`, org)
	// Both files contain exactly one statement, so PostgreSQL executes each
	// concurrent index operation outside an implicit multi-statement txn.
	maintenanceExec(t, pool, maintenanceMigration(t, "000301_job_retention_succeeded_index.down.sql"))
	maintenanceExec(t, pool, maintenanceMigration(t, "000301_job_retention_succeeded_index.up.sql"))
	maintenanceExec(t, pool, `ANALYZE jobs; SET enable_seqscan=off`)
	rows, err := pool.Query(context.Background(), `EXPLAIN SELECT id FROM jobs WHERE status IN ('succeeded','failed') AND updated_at < now()-interval '30 days' ORDER BY updated_at,id LIMIT 10000 FOR UPDATE SKIP LOCKED`)
	require.NoError(t, err, "explain exact eligible status and age predicate")
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect succeeded retention plan")
	require.Contains(t, strings.Join(plan, "\n"), "idx_jobs_retention_succeeded", "new partial index must support succeeded and failed candidate selection")
	t.Logf("eligible candidate index plan:\n%s", strings.Join(plan, "\n"))
	maintenanceExec(t, pool, `RESET enable_seqscan`)
	maintenanceExec(t, pool, maintenanceMigration(t, "000301_job_retention_succeeded_index.down.sql"))
	var indexExists bool
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT to_regclass('idx_jobs_retention_succeeded') IS NOT NULL`).Scan(&indexExists), "read index after concurrent rollback")
	require.False(t, indexExists, "down301 should drop only the added succeeded retention index")
}

func TestRetentionPostgresReferenceIndexes(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	org, integration, installation := seedMaintenanceInbound(t, pool)
	session, repository := uuid.New(), uuid.New()
	maintenanceExec(t, pool, `INSERT INTO sessions VALUES($1,$2)`, session, org)
	maintenanceExec(t, pool, `INSERT INTO repositories VALUES($1,$2)`, repository, org)
	maintenanceExec(t, pool, `INSERT INTO nodes VALUES('index-host')`)
	maintenanceExec(t, pool, `CREATE INDEX idx_session_executors_job ON session_executors(org_id,job_id)`)
	maintenanceExec(t, pool, `WITH parents AS (INSERT INTO jobs(org_id,queue,job_type,status,updated_at) SELECT $1,'maintenance','test','failed',now()-interval '40 days' FROM generate_series(1,10001) RETURNING id)
INSERT INTO session_executors(org_id,session_id,job_id,job_type,host_node_id,owner_id,lock_token,status) SELECT $1,$2,id,'test','index-host','owner',gen_random_uuid(),'completed' FROM parents`, org, session)
	maintenanceExec(t, pool, `WITH parents AS (INSERT INTO webhook_deliveries(org_id,integration_id,provider,event_type,created_at) SELECT $1,$2,'slack','test',now()-interval '40 days' FROM generate_series(1,10001) RETURNING id)
INSERT INTO slack_inbound_events(org_id,slack_installation_id,slack_team_id,event_type,payload,webhook_delivery_id) SELECT $1,$3,'team','test','{}',id FROM parents`, org, integration, installation)
	maintenanceExec(t, pool, `WITH parents AS (INSERT INTO webhook_deliveries(org_id,integration_id,provider,event_type,created_at) SELECT $1,$2,'pagerduty','test',now()-interval '40 days' FROM generate_series(1,10001) RETURNING id)
INSERT INTO pagerduty_inbound_events(org_id,provider_event_id,event_type,payload,webhook_delivery_id) SELECT $1,id::text,'test','{}',id FROM parents`, org, integration)
	maintenanceExec(t, pool, `INSERT INTO session_preview_prewarm_runs(org_id,repository_id,session_id,mode,decision,status,job_id) SELECT $1,$2,$3,'cache','cache','succeeded',id FROM jobs WHERE org_id=$1`, org, repository, session)
	maintenanceExec(t, pool, `INSERT INTO preview_cache_prewarm_runs(org_id,repo_id,source,source_id,cache_scope_key,status,job_id) SELECT $1,$2,'fixture',id::text,id::text,'succeeded',id FROM jobs WHERE org_id=$1`, org, repository)
	for _, tt := range []struct{ migration, table, column, index string }{
		{"000302_session_executor_job_fk_index", "session_executors", "job_id", "idx_session_executors_retention_job"},
		{"000303_slack_webhook_delivery_fk_index", "slack_inbound_events", "webhook_delivery_id", "idx_slack_inbound_events_retention_delivery"},
		{"000304_pagerduty_webhook_delivery_fk_index", "pagerduty_inbound_events", "webhook_delivery_id", "idx_pagerduty_inbound_events_retention_delivery"},
		{"000305_session_preview_prewarm_job_fk_index", "session_preview_prewarm_runs", "job_id", "idx_session_preview_prewarm_runs_retention_job"},
		{"000306_preview_cache_prewarm_job_fk_index", "preview_cache_prewarm_runs", "job_id", "idx_preview_cache_prewarm_runs_retention_job"},
	} {
		// These operations deliberately share this fixture sequentially. Each
		// top-level test has its own schema and can run in parallel.
		maintenanceExec(t, pool, maintenanceMigration(t, tt.migration+".down.sql"))
		maintenanceExec(t, pool, `ANALYZE `+tt.table)
		var parent uuid.UUID
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT `+tt.column+` FROM `+tt.table+` LIMIT 1`).Scan(&parent), "read referenced parent for lookup-plan proof")
		query := `EXPLAIN (ANALYZE,BUFFERS) SELECT id FROM ` + tt.table + ` WHERE ` + tt.column + `=$1`
		rows, err := pool.Query(context.Background(), query, parent)
		require.NoError(t, err, "explain existing FK lookup without new ID index")
		before, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err, "collect prior FK lookup plan")
		maintenanceExec(t, pool, maintenanceMigration(t, tt.migration+".up.sql"))
		rows, err = pool.Query(context.Background(), query, parent)
		require.NoError(t, err, "explain FK lookup with exact ID index")
		after, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err, "collect indexed FK lookup plan")
		require.Contains(t, strings.Join(after, "\n"), tt.index, "exact referenced-ID lookup should use new concurrent index")
		t.Logf("%s before:\n%s\nafter:\n%s", tt.table, strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

func TestRetentionPostgresPrewarmHistoryOptionalJobLinks(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	completed := time.Now().UTC().Add(-time.Hour)
	f := seedMaintenanceReview(t, pool, &completed)
	keep := []string{}
	for i, tt := range []struct{ jobStatus, runStatus string }{
		{"succeeded", "succeeded"}, {"failed", "failed"}, {"pending", "queued"}, {"running", "running"},
	} {
		job := insertMaintenanceJob(t, pool, f.org, tt.jobStatus, true, "session_preview_prewarm", json.RawMessage(`{}`))
		if tt.jobStatus == "pending" || tt.jobStatus == "running" {
			keep = append(keep, job.String())
		}
		maintenanceExec(t, pool, `INSERT INTO session_preview_prewarm_runs(org_id,repository_id,session_id,workspace_revision,mode,decision,status,job_id,reason,explanation,capacity_snapshot,error) VALUES($1,$2,$3,$4,'cache','cache',$5,$6,'history','retained explanation','{"available":true}','retained error')`, f.org, f.repository, f.session, i, tt.runStatus, job)
		maintenanceExec(t, pool, `INSERT INTO preview_cache_prewarm_runs(org_id,repo_id,source,source_id,cache_scope_key,status,job_id,config_digest,error) VALUES($1,$2,'session',$3,$4,$5,$6,'digest','retained error')`, f.org, f.repository, f.session.String(), job.String(), tt.runStatus, job)
	}
	histories := map[string]json.RawMessage{}
	for _, table := range []string{"session_preview_prewarm_runs", "preview_cache_prewarm_runs"} {
		var before json.RawMessage
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(r)-'job_id' ORDER BY id) FROM `+table+` r`).Scan(&before), "read all durable prewarm history fields before retention")
		histories[table] = before
	}
	require.Equal(t, int64(2), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", 30), "only old terminal prewarm parent jobs should delete")
	for _, table := range []string{"session_preview_prewarm_runs", "preview_cache_prewarm_runs"} {
		var after json.RawMessage
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(r)-'job_id' ORDER BY id) FROM `+table+` r`).Scan(&after), "read all durable prewarm history fields after retention")
		require.JSONEq(t, string(histories[table]), string(after), "retention must preserve child identity, status and all history content")
		var terminalLinks, activeLinks int
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE status IN ('succeeded','failed') AND job_id IS NOT NULL),count(*) FILTER (WHERE status IN ('queued','running') AND job_id IS NOT NULL) FROM `+table).Scan(&terminalLinks, &activeLinks), "read optional terminal versus operational active job links")
		require.Equal(t, 0, terminalLinks, "terminal history should follow its existing ON DELETE SET NULL job FK contract")
		require.Equal(t, 2, activeLinks, "pending/running parent jobs must retain cancellation and active tracking links")
	}
	sort.Strings(keep)
	require.Equal(t, keep, maintenanceIDs(t, pool, "jobs"), "only exact pending and running parent job identities should remain")
}

func TestRetentionPostgresConcurrentBatchesSkipLockedRows(t *testing.T) {
	t.Parallel()
	pool, _ := newMaintenancePostgres(t)
	org, _, _ := seedMaintenanceInbound(t, pool)
	maintenanceExec(t, pool, `INSERT INTO jobs(org_id,queue,job_type,status,updated_at) SELECT $1,'maintenance','test','succeeded',now()-interval '40 days' FROM generate_series(1,20005)`, org)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstTx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin first concurrent retention batch")
	defer func() { _ = firstTx.Rollback(context.Background()) }()
	var first int64
	require.NoError(t, firstTx.QueryRow(ctx, `SELECT delete_expired_completed_jobs(30)`).Scan(&first), "first batch should claim exactly one bounded rowset")
	require.Equal(t, int64(10000), first, "first transaction should retain locks only on its bounded deleted rowset")
	// The first DELETE is deliberately uncommitted. A second connection must
	// skip its locked candidates and make progress before firstTx commits.
	var second int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT delete_expired_completed_jobs(30)`).Scan(&second), "concurrent batch must progress without waiting for the first transaction")
	require.Equal(t, int64(10000), second, "second caller should claim a distinct eligible batch")
	require.NoError(t, firstTx.Commit(ctx), "commit first independent retention batch")
	require.Equal(t, int64(5), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", 30), "later call should finish rows beyond both concurrent batch budgets")
	require.Equal(t, int64(0), maintenanceCleanup(t, pool, "delete_expired_completed_jobs", 30), "completed concurrent cleanup should be idempotent")
	require.Empty(t, maintenanceIDs(t, pool, "jobs"), "all exact unprotected eligible jobs should eventually be deleted")
}
