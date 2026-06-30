package demoseed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
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

func TestValidateApplyEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         map[string]string
		databaseURL string
		expectErr   string
	}{
		{
			name:        "requires explicit apply opt in",
			env:         map[string]string{"DEMO_MODE": "true"},
			databaseURL: "postgres://user:pass@localhost/demo",
			expectErr:   "ALLOW_DEMO_SEED_APPLY=true",
		},
		{
			name:        "requires demo mode",
			env:         map[string]string{"ALLOW_DEMO_SEED_APPLY": "true"},
			databaseURL: "postgres://user:pass@localhost/demo",
			expectErr:   "DEMO_MODE=true",
		},
		{
			name:        "rejects production-looking target",
			env:         map[string]string{"ALLOW_DEMO_SEED_APPLY": "true", "DEMO_MODE": "true"},
			databaseURL: "postgres://user:pass@prod-db.internal/onefortythree",
			expectErr:   "production-looking",
		},
		{
			name: "allows production-looking target only with second override",
			env: map[string]string{
				"ALLOW_DEMO_SEED_APPLY":          "true",
				"DEMO_MODE":                      "true",
				"DEMO_SEED_ALLOW_PRODUCTION_URL": "true",
			},
			databaseURL: "postgres://user:pass@prod-db.internal/onefortythree",
		},
		{
			name:        "allows explicit demo target",
			env:         map[string]string{"ALLOW_DEMO_SEED_APPLY": "true", "DEMO_MODE": "true"},
			databaseURL: "postgres://user:pass@demo-db.internal/demo_workspace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateApplyEnvironment(tt.env, tt.databaseURL)
			if tt.expectErr != "" {
				require.Error(t, err, "ValidateApplyEnvironment should reject unsafe apply configuration")
				require.Contains(t, err.Error(), tt.expectErr, "ValidateApplyEnvironment should explain the rejected guardrail")
				return
			}
			require.NoError(t, err, "ValidateApplyEnvironment should allow guarded demo apply configuration")
		})
	}
}

func TestApplyPreflightsTargetBeforeMigrations(t *testing.T) {
	t.Parallel()

	var calls []string
	errUnsafeTarget := fmt.Errorf("unsafe target")
	err := apply(context.Background(), ApplyOptions{
		DatabaseURL: "postgres://user:pass@localhost/demo",
		Env:         map[string]string{"ALLOW_DEMO_SEED_APPLY": "true", "DEMO_MODE": "true"},
	}, applyDeps{
		readAndScanSeed: func(seedPath string) ([]byte, error) {
			calls = append(calls, "read")
			return []byte("seed sql"), nil
		},
		validateApplyEnvironment: func(env map[string]string, databaseURL string) error {
			calls = append(calls, "validate")
			return nil
		},
		connectPool: func(ctx context.Context, databaseURL string) (seedDB, error) {
			calls = append(calls, "connect")
			return fakeSeedDB{}, nil
		},
		ensureApplyTargetSafe: func(ctx context.Context, pool seedDB, allowNonDemoOrgs bool) error {
			calls = append(calls, "preflight")
			return errUnsafeTarget
		},
		runMigrations: func(databaseURL string) error {
			calls = append(calls, "migrate")
			return nil
		},
		applySeedSQL: func(ctx context.Context, pool seedDB, seedSQL []byte) error {
			calls = append(calls, "apply")
			return nil
		},
		assertDemoSeedState: func(ctx context.Context, pool seedDB) error {
			calls = append(calls, "assert")
			return nil
		},
	})

	require.ErrorIs(t, err, errUnsafeTarget, "Apply should return the target-safety preflight error")
	require.Equal(t, []string{"read", "validate", "connect", "preflight"}, calls, "Apply should preflight the target before migrations or seed writes")
}

func TestPruneDeletesOldDemoVolatileState(t *testing.T) {
	t.Parallel()

	db := &pruneSeedDB{auditRows: 5}
	rows, err := prune(context.Background(), PruneOptions{
		DatabaseURL: "postgres://user:pass@localhost/demo",
		MaxAge:      24 * time.Hour,
		Env:         map[string]string{"ALLOW_DEMO_SEED_APPLY": "true", "DEMO_MODE": "true"},
	}, func(ctx context.Context, databaseURL string) (seedDB, error) {
		require.Equal(t, "postgres://user:pass@localhost/demo", databaseURL, "Prune should connect to requested database")
		return db, nil
	})

	require.NoError(t, err, "Prune should delete volatile demo rows")
	require.Equal(t, int64(12), rows, "Prune should return total deleted row count")
	require.True(t, db.closed, "Prune should close the database connection")
	require.Contains(t, db.execSQL, "DELETE FROM auth_sessions", "Prune should delete old auth sessions")
	require.Equal(t, DemoOrgID, db.execArgs[0], "Prune should scope deletes to demo org")
	require.IsType(t, time.Time{}, db.execArgs[1], "Prune should pass a cutoff timestamp")
	require.Contains(t, db.querySQL, "delete_expired_audit_logs", "Prune should call audit log retention")
	require.Equal(t, DemoOrgID, db.queryArgs[0], "Prune should scope audit retention to demo org")
	require.Equal(t, 1, db.queryArgs[1], "Prune should retain at least one day of audit logs")
}

func TestEnsureApplyTargetSafe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr string
	}{
		{
			name: "allows unmigrated database",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT to_regclass('public.organizations') IS NOT NULL")).
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
			},
		},
		{
			name: "rejects non-demo organizations",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT to_regclass('public.organizations') IS NOT NULL")).
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM organizations WHERE id <> $1")).
					WithArgs(DemoOrgID).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
			},
			expectErr: "non-demo organization",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create pgx mock pool")
			defer mock.Close()
			tt.setupMock(mock)

			err = EnsureApplyTargetSafe(context.Background(), mock, false)
			if tt.expectErr != "" {
				require.Error(t, err, "EnsureApplyTargetSafe should reject unsafe targets")
				require.Contains(t, err.Error(), tt.expectErr, "EnsureApplyTargetSafe should explain target rejection")
			} else {
				require.NoError(t, err, "EnsureApplyTargetSafe should allow safe targets")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
			expectErr: "non-demo email",
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

type fakeSeedDB struct{}

func (fakeSeedDB) Close() {}

func (fakeSeedDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (fakeSeedDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeRow{}
}

type fakeRow struct{}

func (fakeRow) Scan(...any) error {
	return nil
}

type pruneAuditRow struct {
	rows int64
	err  error
}

func (r pruneAuditRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) == 0 {
		return nil
	}
	target, ok := dest[0].(*int64)
	if !ok {
		return fmt.Errorf("expected first scan target to be *int64")
	}
	*target = r.rows
	return nil
}

type pruneSeedDB struct {
	execSQL   string
	execArgs  []any
	querySQL  string
	queryArgs []any
	auditRows int64
	closed    bool
}

func (p *pruneSeedDB) Close() {
	p.closed = true
}

func (p *pruneSeedDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	p.execSQL = sql
	p.execArgs = args
	return pgconn.NewCommandTag("DELETE 7"), nil
}

func (p *pruneSeedDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	p.querySQL = sql
	p.queryArgs = args
	return pruneAuditRow{rows: p.auditRows}
}
