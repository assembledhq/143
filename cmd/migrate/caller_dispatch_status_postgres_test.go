package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

const callerDispatchStatusHighestVersion = uint(392)

func seedCallerDispatchStatusMigrationMetadata(t *testing.T, ctx context.Context, conn *pgx.Conn, f callerIntentMigrationFixture) uuid.UUID {
	t.Helper()
	integration, repository, pullRequest, policy, metadata := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, f.org}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,'synthetic/status-schema','https://example.invalid/status-schema.git',1)`, []any{repository, f.org, integration}},
		{`INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,$3,1,'https://example.invalid/pr/1','synthetic/status-schema','Synthetic status schema','synthetic-head','synthetic-base')`, []any{pullRequest, f.org, f.session}},
		{`INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy) VALUES($1,$2,1,'comment_only','{}','{}','{}','','')`, []any{policy, f.org}},
		{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'synthetic-base','synthetic-head','slash_command','running','synthetic-status-output')`, []any{metadata, f.org, f.session, repository, pullRequest, policy}},
	}
	for _, statement := range statements {
		_, err := conn.Exec(ctx, statement.query, statement.args...)
		require.NoError(t, err, "retain a complete scoped synthetic metadata row before testing real migration boundaries")
	}
	return metadata
}

func requireCallerDispatchStatusMigrationCatalog(t *testing.T, ctx context.Context, conn *pgx.Conn, version uint, validated, ready bool) {
	t.Helper()
	var old, new, valid bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_check' AND convalidated),
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_v2_check'),
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_v2_check' AND convalidated)`).Scan(&old, &new, &valid), "read the exact old/new allowance identities and actual PostgreSQL validation state")
	require.Equal(t, [3]bool{version < 390, version >= 390, version >= 390 && validated}, [3]bool{old, new, valid}, "only the appended allowance may replace the exact original validated CHECK")
	if version >= 390 {
		var schemaReady, statusReady, enabled bool
		require.NoError(t, conn.QueryRow(ctx, `SELECT schema_ready,dispatch_status_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&schemaReady, &statusReady, &enabled), "read all three actual interlocks at the committed appended boundary")
		require.Equal(t, [3]bool{true, ready, false}, [3]bool{schemaReady, statusReady, enabled}, "latest readiness must remain independent of explicit intent enablement")
	}
}

func TestCallerDispatchStatusMigrationRoundTripPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, migration.Migrate(383), "start from the actual clean pre-caller ledger rather than a mocked schema version")
	f := seedCallerIntentMigrationLegacy(t, ctx, conn)
	seedCallerDispatchStatusMigrationMetadata(t, ctx, conn, f)
	before := callerIntentMigrationRows(t, ctx, conn, f.org, true)
	var original389 json.RawMessage
	validated := false
	// These independently committed versions include standalone concurrent DDL;
	// their dependent boundary order cannot run in parallel or inside a test tx.
	for _, target := range []uint{389, 390, 391, 392, 391, 390, 389, 383, 389, 390, 391, 392} {
		require.NoError(t, migration.Migrate(target), "traverse the exact next actual driver boundary without Force or fake ledger writes")
		requireCallerIntentMigrationLedger(t, ctx, migration, conn, target, false)
		if target >= 391 {
			validated = true
		}
		if target < 390 {
			validated = false
		}
		requireCallerDispatchStatusMigrationCatalog(t, ctx, conn, target, validated, target == 392)
		require.True(t, callerIntentMigrationRowsEqual(before, callerIntentMigrationRows(t, ctx, conn, f.org, true)), "every legacy business row including complete metadata must survive each real up/down boundary exactly")
		if target == 389 {
			catalog := callerIntentMigrationCompleteCatalog(t, ctx, conn)
			if original389 == nil {
				original389 = catalog
			} else {
				require.Equal(t, original389, catalog, "clean appended rollback must restore the exact original columns, constraints, function definitions, triggers and indexes")
			}
		}
	}
}

//nolint:paralleltest // Subcases serialize full-chain database creation and dirty-ledger cleanup within the fixed three-client fixture.
func TestCallerDispatchStatusHighestRollbackLedgerPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pending bool
	}{
		{name: "retained classification without due work"},
		{name: "retained classification with due work", pending: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each case owns a real ledger and uses the helper's bounded three
			// connections; serialize database creation and dirty-ledger cleanup.
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			require.NoError(t, migration.Migrate(callerDispatchStatusHighestVersion), "install the genuine appended highest boundary before its first down")
			f := seedCallerIntentMigrationLegacy(t, ctx, conn)
			metadata := seedCallerDispatchStatusMigrationMetadata(t, ctx, conn, f)
			if tt.pending {
				seedCallerIntentMigrationPendingReviewer(t, ctx, conn, f)
			}
			_, err := conn.Exec(ctx, `UPDATE code_review_session_metadata SET status_code='code_reviewer_dispatch_unresolved' WHERE org_id=$1 AND id=$2`, f.org, metadata)
			require.NoError(t, err, "commit a genuine retained new-code row, independently of whether a due tuple remains")
			before, catalog := callerIntentMigrationRows(t, ctx, conn, f.org, false), callerIntentMigrationCompleteCatalog(t, ctx, conn)
			downErr := migration.Steps(-1)
			requireAdmissionLookupMigrationError(t, downErr, "55000")
			var migrationErr database.Error
			require.ErrorAs(t, downErr, &migrationErr, "the highest refusal must preserve the actual driver's failing statement")
			var original *pq.Error
			require.True(t, errors.As(migrationErr.OrigErr, &original), "retain the actual PostgreSQL guard cause, not a constructed test error")
			require.Equal(t, "retained_dispatch_unresolved_status_prevents_rollback", original.Message, "retained status must have a distinct highest-first refusal even when due work also exists")
			requireCallerIntentMigrationLedger(t, ctx, migration, conn, 391, true)
			require.True(t, callerIntentMigrationRowsEqual(before, callerIntentMigrationRows(t, ctx, conn, f.org, false)), "failed highest down must retain every complete business/control row without clearing classification")
			require.Equal(t, catalog, callerIntentMigrationCompleteCatalog(t, ctx, conn), "dirty refusal must happen before any lower DDL or readiness mutation")
			requireCallerIntentMigrationDirtyRefusals(t, ctx, migration, conn, 391, 392)
		})
	}
}

func TestCallerDispatchStatusMigrationCommandPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for the actual appended migration executable")
	}
	binary := filepath.Join(t.TempDir(), "migrate")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".")
	var output bytes.Buffer
	build.Stdout, build.Stderr = &output, &output
	require.True(t, build.Run() == nil, "build the actual migration executable without publishing private captured diagnostics")
	directory := callerIntentMigrationCommandSourceThrough(t, callerDispatchStatusHighestVersion)
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, migration.Migrate(383), "seed the real clean predecessor before invoking the actual up executable")
	f := seedCallerIntentMigrationLegacy(t, ctx, conn)
	metadata := seedCallerDispatchStatusMigrationMetadata(t, ctx, conn, f)
	before := callerIntentMigrationRows(t, ctx, conn, f.org, true)
	conn = runCallerIntentMigrationCommandWithRefusal(t, ctx, binary, directory, "up", false, conn, "", 0)
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, 392, false)
	requireCallerDispatchStatusMigrationCatalog(t, ctx, conn, 392, true, true)
	require.True(t, callerIntentMigrationRowsEqual(before, callerIntentMigrationRows(t, ctx, conn, f.org, true)), "the real binary must preserve all original complete business rows while installing the passive prerequisite")
	_, err := conn.Exec(ctx, `UPDATE code_review_session_metadata SET status_code='code_reviewer_dispatch_unresolved' WHERE org_id=$1 AND id=$2`, f.org, metadata)
	require.NoError(t, err, "retain actual new-code history without a due tuple before the actual command's first down")
	retained, catalog := callerIntentMigrationRows(t, ctx, conn, f.org, false), callerIntentMigrationCompleteCatalog(t, ctx, conn)
	conn = runCallerIntentMigrationCommandWithRefusal(t, ctx, binary, directory, "down", true, conn, "retained_dispatch_unresolved_status_prevents_rollback", 391)
	requireCallerIntentMigrationLedger(t, ctx, migration, conn, 391, true)
	require.True(t, callerIntentMigrationRowsEqual(retained, callerIntentMigrationRows(t, ctx, conn, f.org, false)), "actual CLI refusal must preserve the complete retained status and all control flags")
	require.Equal(t, catalog, callerIntentMigrationCompleteCatalog(t, ctx, conn), "actual command must stop before any supporting allowance or readiness object is changed")
	requireCallerIntentMigrationDirtyRefusals(t, ctx, migration, conn, 391, 392)
}
