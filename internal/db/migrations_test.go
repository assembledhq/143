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
	"github.com/stretchr/testify/require"
)

func TestCopyCodingCredentialsMigrationFiltersUserCredentialProviders(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000111_copy_coding_credentials.up.sql")
	require.NoError(t, err, "test should read the coding credential copy migration")

	sql := string(body)
	allowedProviders := "('openai', 'anthropic', 'gemini', 'amp', 'pi', 'openrouter')"
	require.Contains(t, sql,
		"WHERE is_team_default = false\n  AND provider IN "+allowedProviders,
		"personal user credential copy should include only coding-agent providers")
	require.Contains(t, sql,
		"WHERE uc.is_team_default = true\n  AND uc.provider IN "+allowedProviders,
		"team-default user credential copy should include only coding-agent providers")
}

func TestMigrationsDeclareSessionsModelUsedColumn(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("../../migrations/*.up.sql")
	require.NoError(t, err, "test should list up migrations")

	re := regexp.MustCompile(`(?is)ALTER\s+TABLE\s+sessions\s+ADD\s+COLUMN(?:\s+IF\s+NOT\s+EXISTS)?\s+model_used\s+text\b`)
	for _, file := range files {
		body, readErr := os.ReadFile(file)
		require.NoError(t, readErr, "test should read migration file")
		if re.Match(body) {
			return
		}
	}

	require.Fail(t, "schema must add sessions.model_used because SessionStore.UpdateResult writes it")
}

func TestRemoveGeminiCLIMigrationKeepsHistoricalSessionsReadable(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000186_remove_gemini_cli_agent_type.up.sql")
	require.NoError(t, err, "test should read the Gemini CLI removal migration")

	sql := string(body)
	require.NotContains(t, sql, "VALIDATE CONSTRAINT chk_sessions_agent_type",
		"session agent_type constraint should stay NOT VALID so historical gemini_cli sessions remain readable")
	require.Contains(t, sql, "jsonb_set",
		"migration should normalize saved org default_agent_type values away from gemini_cli")
	require.Contains(t, sql, "agent_config,gemini_cli",
		"migration should remove saved gemini_cli agent_config entries")
	require.Contains(t, sql, "UPDATE automations",
		"migration should normalize saved automation config away from gemini_cli")
}

func TestUsersSecondaryEmailsMigrationIsExpandOnly(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000191_users_secondary_emails.up.sql")
	require.NoError(t, err, "test should read the users secondary emails migration")

	sql := string(body)
	require.Contains(t, sql, "ALTER TABLE users ADD COLUMN secondary_emails text[];",
		"migration should add secondary_emails as a nullable expand-only column")
	require.NotContains(t, sql, "UPDATE users",
		"migration should not backfill users in the schema migration")
	require.NotContains(t, sql, "SET DEFAULT",
		"migration should not set a default that can require table-wide validation")
	require.NotContains(t, sql, "SET NOT NULL",
		"migration should leave secondary_emails nullable and let queries coalesce null arrays")
}

func TestMigrationsAllowBuilderRole(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000130_builder_role_constraints.up.sql")
	require.NoError(t, err, "test should read builder role migration")

	sql := string(body)
	require.Contains(t, sql, "chk_users_role CHECK (role IN ('admin', 'member', 'builder', 'viewer')) NOT VALID",
		"users role constraint should allow seeded builder users")
	require.Contains(t, sql, "VALIDATE CONSTRAINT chk_users_role",
		"users role constraint should validate separately to reduce lock pressure")
	require.Contains(t, sql, "organization_memberships_role_check CHECK (role IN ('admin', 'member', 'builder', 'viewer')) NOT VALID",
		"membership role constraint should allow seeded builder memberships")
	require.Contains(t, sql, "VALIDATE CONSTRAINT organization_memberships_role_check",
		"membership role constraint should validate separately to reduce lock pressure")
}

// TestOrgJoinTokensRoleCheckPinsTypedEnum pins the org_join_tokens role
// CHECK constraint to the models.Role vocabulary, per the project standard
// for CHECK-constraint columns: if a new role is added to the enum, this
// test fails until a migration widens the constraint to match.
func TestOrgJoinTokensRoleCheckPinsTypedEnum(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000166_org_join_tokens.up.sql")
	require.NoError(t, err, "test should read the org join tokens migration")

	sql := string(body)
	require.Contains(t, sql, "chk_org_join_tokens_role CHECK (role IN ('admin', 'member', 'builder', 'viewer'))",
		"org_join_tokens role constraint must match models.ValidRoles; widen it in a new migration when the enum grows")
}

// TestCopyCodingCredentialsMigrationStampsTeamDefaultMarker locks the
// step-3 INSERT to write `team_default_origin_user_id = uc.user_id`. The
// down migration's orphan check and the dual-write mirror's cleanup both
// key on this column; if the up migration ever drops the marker, an
// otherwise-clean rollback would refuse with a confusing "rows without a
// legacy counterpart" error and the mirror would forever leak team-default
// rows. Pinning here keeps the cross-file invariant from drifting.
func TestCopyCodingCredentialsMigrationStampsTeamDefaultMarker(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000111_copy_coding_credentials.up.sql")
	require.NoError(t, err, "test should read the coding credential copy migration")

	sql := string(body)
	require.Contains(t, sql, "team_default_origin_user_id",
		"step-3 INSERT must populate the team_default_origin_user_id marker column")
	require.Contains(t, sql,
		"AND cc.team_default_origin_user_id = uc.user_id",
		"idempotency check must key on the marker column, not the label string")
}

// TestCopyCodingCredentialsDownMigrationRefusesOrphans locks the down
// migration's pre-check that aborts when coding_credentials contains rows
// with no legacy counterpart and no team-default marker — the only kind of
// row that could only have been created by /api/v1/coding-credentials after
// 000111 ran. A blanket DELETE that ignored those rows would silently drop
// live user data.
func TestCopyCodingCredentialsDownMigrationRefusesOrphans(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000111_copy_coding_credentials.down.sql")
	require.NoError(t, err, "test should read the coding credential copy down migration")

	sql := string(body)
	require.Contains(t, sql, "RAISE EXCEPTION",
		"down migration must abort on orphan rows rather than silently delete them")
	require.Contains(t, sql, "AND cc.team_default_origin_user_id IS NULL",
		"down migration must use the marker column to identify migration-minted rows")
	require.NotContains(t, sql, "label LIKE 'Team default (migrated from %)'",
		"down migration must not depend on string-matching the human-readable label")
}

// TestCodingCredentialsSchemaDeclaresTeamDefaultMarker pins the marker
// column and its CHECK constraint to the schema migration so the cross-file
// invariant (mirror + 000111 + down migration all read this column) cannot
// drift with a future schema rewrite that renames or drops it.
func TestCodingCredentialsSchemaDeclaresTeamDefaultMarker(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000110_coding_credentials.up.sql")
	require.NoError(t, err, "test should read the coding credential schema migration")

	sql := string(body)
	require.Contains(t, sql, "team_default_origin_user_id uuid",
		"schema must declare the team_default_origin_user_id marker column")
	require.Contains(t, sql, "chk_coding_credentials_team_default_marker",
		"schema must constrain the marker column to org-scoped rows")
}

func TestCodingCredentialsVersioningMigrationUsesInsertOnlyRuntimeState(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000167_coding_credentials_insert_only_versioning.up.sql")
	require.NoError(t, err, "test should read the coding credential insert-only versioning migration")

	sql := string(body)
	require.Contains(t, sql, "ADD COLUMN version_id uuid", "migration should add physical config version ids")
	require.Contains(t, sql, "ALTER COLUMN version_id SET DEFAULT gen_random_uuid()", "config version ids should default for future inserts")
	require.Contains(t, sql, "ADD COLUMN active boolean NOT NULL DEFAULT true", "migration should add active flag to config rows")
	require.Contains(t, sql, "CREATE TABLE coding_credential_runtime_state", "migration should create a separate runtime state table")
	require.Contains(t, sql, "credential_id uuid NOT NULL", "runtime state should key by stable credential id")
	require.Contains(t, sql, "active boolean NOT NULL DEFAULT true", "runtime state should use insert-only active rows")
	require.Contains(t, sql, "WHERE active = true", "migration should use active-row partial uniqueness")
	require.Contains(t, sql, "INSERT INTO coding_credential_runtime_state", "migration should backfill runtime state from existing credentials")
	require.Contains(t, sql, "coding_credential_runtime_state_guard", "migration should guard runtime rows and sync temporary legacy runtime columns")
	require.Contains(t, sql, "cc.user_id IS NOT DISTINCT FROM NEW.user_id", "runtime guard should enforce nullable user scope identity")
	require.Contains(t, sql, "cc.active = true", "runtime guard should require an active config row")
	require.Contains(t, sql, "ERRCODE = 'foreign_key_violation'", "runtime guard should reject orphaned runtime state")
}

func TestCodingCredentialsVersioningMigrationPostgresBehavior(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the Postgres migration behavior test")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	defer conn.Close(ctx)

	schema := "test_coding_credentials_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated schema")
	defer func() {
		_, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`, public`)
	require.NoError(t, err, "test should isolate migration objects to the test schema")
	if _, err = conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Skipf("pgcrypto extension is required for gen_random_uuid(): %v", err)
	}

	orgID := uuid.New()
	userID := uuid.New()
	orgCredID := uuid.New()
	userCredID := uuid.New()
	// DDL runs as one no-arg Exec (simple protocol allows multiple
	// statements); the parameterized seed INSERTs must run one at a time
	// because the extended protocol rejects multi-statement strings.
	_, err = conn.Exec(ctx, `
		CREATE TABLE organizations (id uuid PRIMARY KEY);
		CREATE TABLE users (id uuid PRIMARY KEY);
		CREATE TABLE coding_credentials (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL REFERENCES organizations(id),
			user_id uuid REFERENCES users(id) ON DELETE CASCADE,
			provider text NOT NULL,
			label text NOT NULL DEFAULT '',
			config bytea NOT NULL,
			priority integer NOT NULL DEFAULT 1,
			status text NOT NULL DEFAULT 'active',
			last_verified_at timestamptz,
			rate_limited_until timestamptz,
			rate_limited_observed_at timestamptz,
			rate_limit_message text,
			created_by uuid,
			team_default_origin_user_id uuid,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE UNIQUE INDEX coding_credentials_scope_provider_label_idx
			ON coding_credentials (org_id, user_id, provider, label) NULLS NOT DISTINCT;
		CREATE INDEX coding_credentials_resolver_idx
			ON coding_credentials (org_id, provider, user_id, priority, created_at)
			WHERE status = 'active';
		CREATE INDEX coding_credentials_user_idx
			ON coding_credentials (org_id, user_id, priority)
			WHERE user_id IS NOT NULL AND status != 'disabled';
		CREATE INDEX coding_credentials_org_idx
			ON coding_credentials (org_id, priority)
			WHERE user_id IS NULL AND status != 'disabled';
		CREATE INDEX coding_credentials_pending_auth_ttl_idx
			ON coding_credentials (created_at)
			WHERE status = 'pending_auth';
		CREATE INDEX idx_coding_credentials_rate_limited_until
			ON coding_credentials (rate_limited_until)
			WHERE rate_limited_until IS NOT NULL;
	`)
	require.NoError(t, err, "test should create the pre-migration coding credential shape")

	_, err = conn.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, orgID)
	require.NoError(t, err, "test should seed an organization")
	_, err = conn.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, userID)
	require.NoError(t, err, "test should seed a user")
	_, err = conn.Exec(ctx, `
		INSERT INTO coding_credentials (
			id, org_id, user_id, provider, label, config, priority, status, last_verified_at, rate_limited_until, rate_limit_message
		) VALUES
			($1, $3, NULL, 'openai', 'org-a', decode('76303a7b7d', 'hex'), 1, 'active', now(), now() + interval '1 hour', 'cool down'),
			($2, $3, $4, 'anthropic', 'user-a', decode('76303a7b7d', 'hex'), 2, 'invalid', NULL, NULL, NULL)`,
		orgCredID, userCredID, orgID, userID)
	require.NoError(t, err, "test should seed pre-migration coding credentials")

	body, err := os.ReadFile("../../migrations/000167_coding_credentials_insert_only_versioning.up.sql")
	require.NoError(t, err, "test should read the versioning migration")
	_, err = conn.Exec(ctx, string(body))
	require.NoError(t, err, "versioning migration should apply to the pre-migration schema")

	var activeConfigs, activeRuntime int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM coding_credentials WHERE active = true`).Scan(&activeConfigs)
	require.NoError(t, err, "test should count active config versions")
	require.Equal(t, 2, activeConfigs, "migration should leave exactly one active config per existing credential")
	err = conn.QueryRow(ctx, `SELECT count(*) FROM coding_credential_runtime_state WHERE active = true`).Scan(&activeRuntime)
	require.NoError(t, err, "test should count active runtime versions")
	require.Equal(t, 2, activeRuntime, "migration should backfill exactly one active runtime row per existing credential")

	var defaultExpr string
	err = conn.QueryRow(ctx, `
		SELECT pg_get_expr(d.adbin, d.adrelid)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = $1 AND c.relname = 'coding_credentials' AND a.attname = 'version_id'
	`, schema).Scan(&defaultExpr)
	require.NoError(t, err, "test should inspect version_id default")
	require.Contains(t, defaultExpr, "gen_random_uuid()", "version_id should default for future config versions")

	_, err = conn.Exec(ctx, `
		INSERT INTO coding_credentials (id, org_id, user_id, provider, label, config, priority, status, created_at, updated_at)
		VALUES ($1, $2, NULL, 'openai', 'duplicate-active-id', decode('76303a7b7d', 'hex'), 99, 'active', now(), now())
	`, orgCredID, orgID)
	require.Error(t, err, "duplicate active config versions should be rejected")

	_, err = conn.Exec(ctx, `
		INSERT INTO coding_credential_runtime_state (credential_id, org_id, user_id, status, active)
		VALUES ($1, $2, NULL, 'active', true)
	`, orgCredID, orgID)
	require.Error(t, err, "duplicate active runtime state rows should be rejected")

	_, err = conn.Exec(ctx, `
		INSERT INTO coding_credential_runtime_state (credential_id, org_id, user_id, status, active)
		VALUES ($1, $2, $3, 'active', false)
	`, orgCredID, orgID, userID)
	require.Error(t, err, "runtime state rows with the wrong nullable user scope should be rejected")

	_, err = conn.Exec(ctx, `
		INSERT INTO coding_credential_runtime_state (credential_id, org_id, user_id, status, active)
		VALUES ($1, $2, NULL, 'active', false)
	`, uuid.New(), orgID)
	require.Error(t, err, "orphan runtime state rows should be rejected")

	_, err = conn.Exec(ctx, `
		INSERT INTO coding_credential_runtime_state (credential_id, org_id, user_id, status, active)
		VALUES ($1, $2, NULL, 'invalid', false)
	`, orgCredID, orgID)
	require.NoError(t, err, "runtime trigger should allow scoped inactive history")
	var syncedStatus string
	err = conn.QueryRow(ctx, `SELECT status FROM coding_credentials WHERE id = $1 AND active = true`, orgCredID).Scan(&syncedStatus)
	require.NoError(t, err, "test should read trigger-synced status")
	require.Equal(t, "invalid", syncedStatus, "runtime trigger should sync legacy runtime columns on active config")

	// Simulate a pre-versioning writer racing the rolling deploy: it inserts a
	// config row with no runtime-state row, which the versioned read paths
	// cannot see. Boot-time reconciliation must heal it.
	strayID := uuid.New()
	_, err = conn.Exec(ctx, `
		INSERT INTO coding_credentials (id, org_id, user_id, provider, label, config, priority, status)
		VALUES ($1, $2, NULL, 'openai', 'deploy-window', decode('76303a7b7d', 'hex'), 3, 'active')`,
		strayID, orgID)
	require.NoError(t, err, "pre-versioning code should still be able to insert config rows")

	// The boot-time reconciler was removed with the credentials cleanup
	// migration (the legacy columns it copied are gone); the equivalent SQL is
	// inlined here because this test exercises the 000167-era schema where
	// those columns still exist.
	reconcileSQL := `INSERT INTO coding_credential_runtime_state (
			credential_id, org_id, user_id, status, last_verified_at,
			rate_limited_until, rate_limited_observed_at, rate_limit_message, active
		)
		SELECT cc.id, cc.org_id, cc.user_id, cc.status, cc.last_verified_at,
		       cc.rate_limited_until, cc.rate_limited_observed_at, cc.rate_limit_message, true
		FROM coding_credentials cc
		WHERE cc.active = true
		  AND NOT EXISTS (
			SELECT 1 FROM coding_credential_runtime_state rt
			WHERE rt.credential_id = cc.id AND rt.active = true
		  )
		ON CONFLICT (credential_id) WHERE active = true DO NOTHING`
	tag, err := conn.Exec(ctx, reconcileSQL)
	require.NoError(t, err, "reconciliation should heal config rows missing runtime state")
	require.Equal(t, int64(1), tag.RowsAffected(), "reconciliation should backfill exactly the orphaned credential")
	var strayStatus string
	err = conn.QueryRow(ctx,
		`SELECT status FROM coding_credential_runtime_state WHERE credential_id = $1 AND active = true`, strayID,
	).Scan(&strayStatus)
	require.NoError(t, err, "healed credential should have an active runtime row")
	require.Equal(t, "active", strayStatus, "healed runtime state should copy the legacy status column")

	tag, err = conn.Exec(ctx, reconcileSQL)
	require.NoError(t, err, "reconciliation should be idempotent")
	require.Zero(t, tag.RowsAffected(), "second reconciliation pass should be a no-op")

	downBody, err := os.ReadFile("../../migrations/000167_coding_credentials_insert_only_versioning.down.sql")
	require.NoError(t, err, "test should read the versioning down migration")
	_, err = conn.Exec(ctx, string(downBody))
	require.NoError(t, err, "versioning down migration should apply cleanly")

	var totalRows int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM coding_credentials`).Scan(&totalRows)
	require.NoError(t, err, "test should count post-rollback rows")
	require.Equal(t, 3, totalRows, "down migration should collapse versions back to one row per credential")

	var restoredStatus string
	var restoredRateLimit *time.Time
	err = conn.QueryRow(ctx,
		`SELECT status, rate_limited_until FROM coding_credentials WHERE id = $1`, orgCredID,
	).Scan(&restoredStatus, &restoredRateLimit)
	require.NoError(t, err, "test should read the rolled-back credential")
	require.Equal(t, "active", restoredStatus, "down migration should restore status from the active runtime row")
	require.NotNil(t, restoredRateLimit, "down migration should restore rate-limit state from the active runtime row")

	var pkColumns string
	err = conn.QueryRow(ctx, `
		SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		FROM pg_constraint c
		JOIN pg_class r ON r.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace
		CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE n.nspname = $1 AND r.relname = 'coding_credentials' AND c.contype = 'p'
	`, schema).Scan(&pkColumns)
	require.NoError(t, err, "test should inspect the rolled-back primary key")
	require.Equal(t, "id", pkColumns, "down migration should restore the primary key on id")

	t.Logf("validated migration behavior in schema %q", schema)
}

func TestAutomationsGoalLengthMigrationRaisesConstraint(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000116_automation_goal_length.down.sql")
	require.NoError(t, err, "test should read the automation goal length down migration")
	downSQL := string(body)
	require.Contains(t, downSQL, "DROP CONSTRAINT IF EXISTS chk_automations_goal_length",
		"down migration should remove the current goal-length check before restoring the old one")
	require.Contains(t, downSQL, "char_length(goal) BETWEEN 1 AND 4000",
		"down migration should restore the previous 4000-character cap")

	body, err = os.ReadFile("../../migrations/000116_automation_goal_length.up.sql")
	require.NoError(t, err, "test should read the automation goal length up migration")
	upSQL := string(body)
	require.Contains(t, upSQL, "DROP CONSTRAINT IF EXISTS chk_automations_goal_length",
		"up migration should replace the old goal-length check rather than stack another one")
	require.Contains(t, upSQL, "char_length(goal) BETWEEN 1 AND 8000",
		"up migration should raise the automation goal cap to 8000 characters")
}

func TestAutomationsGoalLengthExpandMigrationRaisesConstraint(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000118_automation_goal_length_expand.down.sql")
	require.NoError(t, err, "test should read the expanded automation goal length down migration")
	downSQL := string(body)
	require.Contains(t, downSQL, "DROP CONSTRAINT IF EXISTS chk_automations_goal_length",
		"down migration should remove the current goal-length check before restoring the previous one")
	require.Contains(t, downSQL, "char_length(goal) BETWEEN 1 AND 8000",
		"down migration should restore the previous 8000-character cap")

	body, err = os.ReadFile("../../migrations/000118_automation_goal_length_expand.up.sql")
	require.NoError(t, err, "test should read the expanded automation goal length up migration")
	upSQL := string(body)
	require.Contains(t, upSQL, "DROP CONSTRAINT IF EXISTS chk_automations_goal_length",
		"up migration should replace the old goal-length check rather than stack another one")
	require.Contains(t, upSQL, "char_length(goal) BETWEEN 1 AND 64000",
		"up migration should raise the automation goal cap to 64000 characters")
}

func TestReviewLoopMigrationDoesNotReferenceSessionMessagesByIDOnly(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("../../migrations/*_review_agent_loops.up.sql")
	require.NoError(t, err, "test should list review loop migrations")
	require.Len(t, files, 1, "test should find exactly one review loop migration")

	body, err := os.ReadFile(files[0])
	require.NoError(t, err, "test should read the review loop migration")

	sql := string(body)
	require.NotContains(t, sql, "REFERENCES session_messages(id)",
		"session_messages is partitioned with primary key (id, created_at), so review loop message pointers must not FK to id alone")
}

func TestSlackHumanInputPrivacyMigrationIsRetrySafe(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000192_slackbot_human_input_privacy.up.sql")
	require.NoError(t, err, "test should read the Slack human-input privacy migration")

	sql := string(body)
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS assigned_user_id",
		"Slack human-input privacy migration should tolerate retry after partially adding assigned_user_id")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS sensitivity",
		"Slack human-input privacy migration should tolerate retry after partially adding sensitivity")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS preferred_channel",
		"Slack human-input privacy migration should tolerate retry after partially adding preferred_channel")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_session_human_input_requests_assigned_pending",
		"Slack human-input privacy migration should tolerate retry after creating assigned-user index")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_slack_inbound_events_payload_retention",
		"Slack human-input privacy migration should tolerate retry after creating payload-retention index")
}

func TestSlackSessionClaimsMigrationDropsDependentIndexExplicitly(t *testing.T) {
	t.Parallel()

	upBody, err := os.ReadFile("../../migrations/000193_slack_session_claims.up.sql")
	require.NoError(t, err, "test should read the Slack session claims up migration")
	downBody, err := os.ReadFile("../../migrations/000193_slack_session_claims.down.sql")
	require.NoError(t, err, "test should read the Slack session claims down migration")

	require.Contains(t, string(upBody), "CREATE TABLE IF NOT EXISTS slack_session_claims",
		"Slack session claims up migration should tolerate retry after creating the claims table")
	require.Contains(t, string(upBody), "CREATE INDEX IF NOT EXISTS idx_slack_session_claims_org_user",
		"Slack session claims up migration should tolerate retry after creating the claims index")
	require.Contains(t, string(downBody), "DROP INDEX IF EXISTS idx_slack_session_claims_org_user",
		"Slack session claims down migration should drop the claims index explicitly before dropping the table")
}

func TestGitHubInstallationClaimsMigrationDeduplicatesInstallationsBeforeUpsert(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000126_github_installation_repo_claims.up.sql")
	require.NoError(t, err, "test should read the GitHub installation claims migration")

	sql := string(body)
	require.Contains(t, sql, "WITH installation_candidates AS",
		"migration should stage installation candidates before the upsert")
	require.Contains(t, sql, "ROW_NUMBER() OVER",
		"migration should rank candidates so each installation_id is upserted once")
	require.Contains(t, sql, "PARTITION BY installation_id",
		"migration should deduplicate by installation_id before ON CONFLICT DO UPDATE")
	require.Contains(t, sql, "MIN(created_at) OVER (PARTITION BY installation_id) AS first_seen_created_at",
		"migration should preserve the earliest integration timestamp for installation created_at")
	require.Contains(t, sql, "WHERE candidate_rank = 1",
		"migration should only upsert the selected candidate per installation")
}

func TestGitHubInstallationClaimsDownMigrationDropsChildLinksFirst(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000126_github_installation_repo_claims.down.sql")
	require.NoError(t, err, "test should read the GitHub installation claims down migration")

	sql := string(body)
	linkDrop := strings.Index(sql, "DROP TABLE IF EXISTS github_installation_org_links")
	installationDrop := strings.Index(sql, "DROP TABLE IF EXISTS github_installations")
	require.NotEqual(t, -1, linkDrop, "down migration should drop github_installation_org_links")
	require.NotEqual(t, -1, installationDrop, "down migration should drop github_installations")
	require.Less(t, linkDrop, installationDrop,
		"down migration should drop child org-link table before parent installations table")
}
