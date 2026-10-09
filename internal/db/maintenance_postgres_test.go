package db

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The tables under test come from their real migrations, including job status
// checks and incoming single-column FKs. Only unrelated parent tables are
// reduced to the identity columns needed by those constraints.
func newMaintenancePostgres(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable PostgreSQL for maintenance regression proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	schema := "maintenance_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated maintenance schema")
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "drop isolated maintenance schema")
		require.NoError(t, admin.Close(ctx), "close maintenance administration connection")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse disposable maintenance DSN")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "20000"
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "open maintenance pool")
	t.Cleanup(pool.Close)
	maintenanceExec(t, pool, `
CREATE TABLE organizations(id uuid PRIMARY KEY);
CREATE TABLE integrations(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id));
CREATE TABLE slack_installations(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id));
CREATE TABLE pagerduty_integrations(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id));
CREATE TABLE nodes(id text PRIMARY KEY);
CREATE TABLE preview_instances(id uuid PRIMARY KEY);
CREATE TABLE preview_groups(id uuid PRIMARY KEY);
CREATE TABLE repositories(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id),UNIQUE(org_id,id));
CREATE TABLE pull_requests(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id),status text NOT NULL DEFAULT 'open',merged_at timestamptz,UNIQUE(org_id,id));
CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id),UNIQUE(org_id,id));
CREATE TABLE code_review_policies(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id),UNIQUE(org_id,id));
CREATE TABLE session_threads(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id),UNIQUE(org_id,id));
CREATE TABLE review_comments(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id),pull_request_id uuid NOT NULL REFERENCES pull_requests(id),reviewer_type text NOT NULL);
`)
	for _, tt := range []struct{ migration, table string }{
		{"000001_init.up.sql", "jobs"},
		{"000002_core_domain.up.sql", "webhook_deliveries"},
		{"000137_session_executors.up.sql", "session_executors"},
		{"000158_slackbot_product_surface.up.sql", "slack_inbound_events"},
		{"000177_preview_cache_prewarm_runs.up.sql", "preview_cache_prewarm_runs"},
		{"000208_session_preview_prewarm_policy.up.sql", "session_preview_prewarm_runs"},
		{"000214_pagerduty_integration.up.sql", "pagerduty_inbound_events"},
		{"000221_code_reviewer_bot.up.sql", "code_review_session_metadata"},
		{"000283_code_review_decision_outcomes.up.sql", "code_review_pull_request_lifecycle_observations"},
		{"000283_code_review_decision_outcomes.up.sql", "code_review_decision_outcomes"},
		{"000283_code_review_decision_outcomes.up.sql", "code_review_human_review_observations"},
	} {
		maintenanceExec(t, pool, maintenanceTableDDL(t, tt.migration, tt.table))
	}
	checks := maintenanceMigration(t, "000035_check_constraints.up.sql")
	start := strings.Index(checks, "ALTER TABLE jobs")
	end := strings.Index(checks[start:], "ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_status;")
	require.GreaterOrEqual(t, start, 0, "real job-status constraint must exist")
	require.GreaterOrEqual(t, end, 0, "real job-status validation must exist")
	maintenanceExec(t, pool, checks[start:start+end]+"ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_status;")
	maintenanceExec(t, pool, maintenanceMigration(t, "000195_slack_webhook_delivery_ledger.up.sql"))
	maintenanceExec(t, pool, `ALTER TABLE code_review_session_metadata ADD COLUMN risk_reason_details jsonb NOT NULL DEFAULT '[]'::jsonb;
CREATE UNIQUE INDEX metadata_identity ON code_review_session_metadata(org_id,id,session_id,repository_id,pull_request_id,policy_id);
CREATE UNIQUE INDEX jobs_org_id ON jobs(org_id,id);`)
	maintenanceExec(t, pool, maintenanceTableDDL(t, "000295_code_review_assessments.up.sql", "code_review_revision_assessments"))
	maintenanceExec(t, pool, maintenanceTableDDL(t, "000296_code_review_recheck_runtime.up.sql", "code_review_recheck_dispatches"))
	// Function search_path is public in production. Scope this fixture to its
	// isolated schema while preserving migration order and actual SQL bodies.
	maintenanceApplyFunctions(t, pool, schema, "000300_retention_foreign_key_guards.up.sql")
	for _, name := range []string{"000301_job_retention_succeeded_index.up.sql", "000302_session_executor_job_fk_index.up.sql", "000303_slack_webhook_delivery_fk_index.up.sql", "000304_pagerduty_webhook_delivery_fk_index.up.sql", "000305_session_preview_prewarm_job_fk_index.up.sql", "000306_preview_cache_prewarm_job_fk_index.up.sql", "000307_webhook_retention_ordered_index.up.sql"} {
		maintenanceExec(t, pool, maintenanceMigration(t, name))
	}
	maintenanceApplyFunctions(t, pool, schema, "000309_resume_bounded_retention.up.sql")
	return pool, schema
}

func maintenanceMigration(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
	require.NoError(t, err, "read actual maintenance migration %s", name)
	return string(body)
}

func maintenanceTableDDL(t *testing.T, migration, table string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)CREATE TABLE ` + regexp.QuoteMeta(table) + ` \(.*?\n\);`)
	ddl := pattern.FindString(maintenanceMigration(t, migration))
	require.NotEmpty(t, ddl, "actual migration must contain table %s", table)
	return ddl
}

func maintenanceExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), query, args...)
	require.NoError(t, err, "maintenance fixture statement should execute")
}

func maintenanceApplyFunctions(t *testing.T, pool *pgxpool.Pool, schema, migration string) {
	t.Helper()
	body := strings.ReplaceAll(maintenanceMigration(t, migration), "SET search_path = public", "SET search_path = "+schema)
	maintenanceExec(t, pool, body)
}

func maintenanceIDs(t *testing.T, pool *pgxpool.Pool, table string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id::text FROM `+table+` ORDER BY id`)
	require.NoError(t, err, "read remaining maintenance identities")
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect remaining maintenance identities")
	return ids
}

func maintenanceCleanup(t *testing.T, pool *pgxpool.Pool, function string, days int) int64 {
	t.Helper()
	var deleted int64
	err := pool.QueryRow(context.Background(), `SELECT `+function+`($1)`, days).Scan(&deleted)
	require.NoError(t, err, "cleanup should preserve FK integrity and return its count")
	return deleted
}

type maintenanceTenant struct{ org, repository, pr, policy, session, metadata uuid.UUID }

func seedMaintenanceReview(t *testing.T, pool *pgxpool.Pool, completedAt *time.Time) maintenanceTenant {
	t.Helper()
	f := maintenanceTenant{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	maintenanceExec(t, pool, `INSERT INTO organizations VALUES($1)`, f.org)
	maintenanceExec(t, pool, `INSERT INTO repositories VALUES($1,$2)`, f.repository, f.org)
	maintenanceExec(t, pool, `INSERT INTO pull_requests(id,org_id) VALUES($1,$2)`, f.pr, f.org)
	maintenanceExec(t, pool, `INSERT INTO code_review_policies VALUES($1,$2)`, f.policy, f.org)
	maintenanceExec(t, pool, `INSERT INTO sessions VALUES($1,$2)`, f.session, f.org)
	maintenanceExec(t, pool, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,decision,review_output_key,completed_at,created_at,risk_reason_details)
VALUES($1,$2,$3,$4,$5,$6,'base','head','app_reviewer','completed','approved',$3::uuid::text,$7,now()-interval '3 days','[{"code":"z"},{"code":"a"},{"code":"z"},{"code":""},{}]')`, f.metadata, f.org, f.session, f.repository, f.pr, f.policy, completedAt)
	return f
}
