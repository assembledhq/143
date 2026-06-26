package demoseed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

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

func TestCurrentSeedPassesSafetyScan(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", ".143", "seed.sql"))
	require.NoError(t, err, "test should read the canonical demo seed")

	require.NoError(t, ScanSeedSafety(body), "canonical demo seed should pass safety scanning")
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
