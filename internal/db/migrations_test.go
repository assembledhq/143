package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
