package demoseed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplaceDatabaseName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		databaseURL string
		dbName      string
		expected    string
		expectErr   bool
	}{
		{
			name:        "replaces URL path and keeps query",
			databaseURL: "postgres://user:pass@localhost:5432/onefortythree?sslmode=disable",
			dbName:      "demo_seed_check",
			expected:    "postgres://user:pass@localhost:5432/demo_seed_check?sslmode=disable",
		},
		{
			name:        "supports postgresql scheme",
			databaseURL: "postgresql://user:pass@localhost/source",
			dbName:      "target",
			expected:    "postgresql://user:pass@localhost/target",
		},
		{
			name:        "rejects keyword DSN",
			databaseURL: "host=localhost user=onefortythree dbname=onefortythree",
			dbName:      "target",
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := ReplaceDatabaseName(tt.databaseURL, tt.dbName)
			if tt.expectErr {
				require.Error(t, err, "ReplaceDatabaseName should reject unsupported DSN formats")
				return
			}
			require.NoError(t, err, "ReplaceDatabaseName should rewrite a postgres URL")
			require.Equal(t, tt.expected, actual, "ReplaceDatabaseName should preserve non-database URL parts")
		})
	}
}

func TestIsProbablyProductionURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		databaseURL string
		expected    bool
	}{
		{name: "local dev is not production", databaseURL: "postgres://user:pass@localhost:5432/onefortythree?sslmode=disable", expected: false},
		{name: "demo host is not production", databaseURL: "postgres://user:pass@demo-db.internal/demo_workspace", expected: false},
		{name: "product host is not production", databaseURL: "postgres://user:pass@product-demo.internal/demo_workspace", expected: false},
		{name: "production host is production", databaseURL: "postgres://user:pass@prod-db.internal/onefortythree", expected: true},
		{name: "production database is production", databaseURL: "postgres://user:pass@localhost/onefortythree_production", expected: true},
		{name: "rds production-like host is production", databaseURL: "postgres://user:pass@143-production.abc.us-west-2.rds.amazonaws.com/onefortythree", expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, IsProbablyProductionURL(tt.databaseURL), "IsProbablyProductionURL should classify target risk")
		})
	}
}

func TestScanSeedSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		expectErr string
	}{
		{
			name: "allows safe demo data",
			body: "INSERT INTO users (email) VALUES ('preview-admin@143.dev');\n" +
				"INSERT INTO repositories (clone_url) VALUES ('https://github.com/assembledhq/143.git');",
		},
		{
			name: "allows approved demo PR URL",
			body: "INSERT INTO pull_requests (github_pr_url) VALUES ('https://github.com/assembledhq/143/pull/42');",
		},
		{
			name:      "rejects non-demo email",
			body:      "INSERT INTO users (email) VALUES ('alice@example.com');",
			expectErr: "non-preview email",
		},
		{
			name:      "rejects obvious GitHub token",
			body:      "INSERT INTO integrations (config) VALUES ('{\"token\":\"ghp_0123456789abcdef0123456789abcdef012345\"}');",
			expectErr: "token",
		},
		{
			name:      "rejects private key material",
			body:      "-----BEGIN PRIVATE KEY-----",
			expectErr: "private key",
		},
		{
			name:      "rejects URL credentials",
			body:      "INSERT INTO repositories (clone_url) VALUES ('https://user:pass@example.com/repo.git');",
			expectErr: "credentials",
		},
		{
			name:      "rejects unapproved external URL",
			body:      "INSERT INTO pull_requests (github_pr_url) VALUES ('https://linear.app/acme/issue/BUG-1/private');",
			expectErr: "unapproved URL host",
		},
		{
			name:      "rejects unapproved GitHub repository URL",
			body:      "INSERT INTO repositories (clone_url) VALUES ('https://github.com/customer/private-repo.git');",
			expectErr: "unapproved URL path",
		},
		{
			name:      "rejects unapproved GitHub path in approved repository",
			body:      "INSERT INTO pull_requests (github_pr_url) VALUES ('https://github.com/assembledhq/143/issues/1');",
			expectErr: "unapproved URL path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ScanSeedSafety([]byte(tt.body))
			if tt.expectErr != "" {
				require.Error(t, err, "ScanSeedSafety should reject unsafe demo seed content")
				require.Contains(t, err.Error(), tt.expectErr, "ScanSeedSafety should identify the unsafe content class")
				return
			}
			require.NoError(t, err, "ScanSeedSafety should allow safe demo seed content")
		})
	}
}

func TestReadAndScanSeedReadsDirectoryFragmentsInLexicalOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "20_second.sql"), []byte("SELECT 'second';\n"), 0o600), "test should write second seed fragment")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "10_first.sql"), []byte("SELECT 'first';"), 0o600), "test should write first seed fragment")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignored"), 0o600), "test should write non-SQL sibling")

	body, err := readAndScanSeed(dir)
	require.NoError(t, err, "readAndScanSeed should accept a seed fragment directory")
	require.Equal(t, "SELECT 'first';\n\nSELECT 'second';\n", string(body), "readAndScanSeed should concatenate SQL fragments in lexical order")
}

func TestCurrentSeedPassesSafetyScan(t *testing.T) {
	t.Parallel()

	body := readCurrentSeed(t)

	require.NoError(t, ScanSeedSafety(body), "canonical demo seed should pass safety scanning")
}

func TestCurrentSeedCoversRepresentativeProductTables(t *testing.T) {
	t.Parallel()

	seed := string(readCurrentSeed(t))

	requiredStatements := []string{
		"INSERT INTO issues",
		"INSERT INTO priority_scores",
		"INSERT INTO complexity_estimates",
		"INSERT INTO session_issue_links",
		"INSERT INTO session_turn_issue_snapshots",
		"INSERT INTO session_threads",
		"INSERT INTO session_thread_file_events",
		"INSERT INTO session_review_comments",
		"INSERT INTO validations",
		"INSERT INTO preview_groups",
		"INSERT INTO preview_targets",
		"INSERT INTO preview_runtimes",
		"INSERT INTO preview_logs",
		"INSERT INTO pull_request_health_current",
		"INSERT INTO pull_request_health_snapshots",
		"INSERT INTO repository_pr_templates",
		"INSERT INTO automations",
		"INSERT INTO automation_runs",
		"INSERT INTO automation_event_triggers",
		"INSERT INTO agent_capability_policies",
		"INSERT INTO automation_goal_improvements",
		"INSERT INTO pm_documents",
		"INSERT INTO pm_plans",
		"INSERT INTO pm_decision_log",
		"INSERT INTO project_tasks",
		"INSERT INTO project_task_dependencies",
		"INSERT INTO project_specs",
		"INSERT INTO project_attachments",
		"INSERT INTO project_cycles",
		"INSERT INTO project_source_issues",
		"INSERT INTO code_review_session_metadata",
		"INSERT INTO code_review_agent_results",
		"INSERT INTO code_review_findings",
		"INSERT INTO code_review_prompt_artifacts",
		"INSERT INTO usage_hourly",
		"INSERT INTO usage_hourly_execution",
		"INSERT INTO slack_installations",
		"INSERT INTO slack_channel_settings",
		"INSERT INTO pagerduty_integrations",
		"INSERT INTO pagerduty_incidents",
		"INSERT INTO linear_team_repo_mappings",
	}
	for _, statement := range requiredStatements {
		require.Contains(t, seed, statement, "canonical demo seed should include representative product data")
	}
}

func TestCurrentSeedUsesConvergentConflictHandlers(t *testing.T) {
	t.Parallel()

	seed := string(readCurrentSeed(t))

	prTemplateBlock := seedBlock(t, seed, "INSERT INTO repository_pr_templates", "INSERT INTO projects")
	require.Contains(t, prTemplateBlock, "ON CONFLICT (repository_id) DO UPDATE", "repository PR template seed should converge on the table's natural unique key")

	issueBlock := seedBlock(t, seed, "INSERT INTO issues", "INSERT INTO priority_scores")
	requiredIssueColumns := []string{
		"external_id = EXCLUDED.external_id",
		"source = EXCLUDED.source",
		"source_integration_id = EXCLUDED.source_integration_id",
		"repository_id = EXCLUDED.repository_id",
		"first_seen_at = EXCLUDED.first_seen_at",
		"fingerprint = EXCLUDED.fingerprint",
	}
	for _, columnAssignment := range requiredIssueColumns {
		require.Contains(t, issueBlock, columnAssignment, "issue seed conflict handler should converge canonical issue fields")
	}

	previewBlock := seedBlock(t, seed, "DELETE FROM preview_links", "-- A seeded \"ready\" preview instance")
	requiredPreviewCleanup := []string{
		"DELETE FROM preview_links",
		"id <> '00000000-0000-4000-a000-000000000432'::uuid",
		"DELETE FROM preview_targets",
		"id <> '00000000-0000-4000-a000-000000000431'::uuid",
		"DELETE FROM preview_groups",
		"id <> '00000000-0000-4000-a000-000000000430'::uuid",
	}
	for _, statement := range requiredPreviewCleanup {
		require.Contains(t, previewBlock, statement, "preview seed should remove matching natural-key rows before fixed-id inserts")
	}
}

func readCurrentSeed(t *testing.T) []byte {
	t.Helper()

	body, err := readAndScanSeed(filepath.Join("..", "..", DefaultSeedPath))
	require.NoError(t, err, "test should read the canonical demo seed")
	return body
}

func seedBlock(t *testing.T, seed, startMarker, endMarker string) string {
	t.Helper()

	start := strings.Index(seed, startMarker)
	require.NotEqual(t, -1, start, "seed should contain the requested block start")
	remainder := seed[start:]
	end := strings.Index(remainder, endMarker)
	require.NotEqual(t, -1, end, "seed should contain the requested block end")
	return remainder[:end]
}
