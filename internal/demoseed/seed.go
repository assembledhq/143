package demoseed

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultDatabaseURL = "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable" // #nosec G101 -- dev-only default.
	DefaultSeedPath    = ".143/seed"
	DemoOrgID          = "00000000-0000-4000-a000-000000000001"
	DemoOrgName        = "143 Dogfood"
	DemoAdminEmail     = "preview-admin@143.dev"
	DemoMemberEmail    = "preview-member@143.dev"
	DemoBuilderEmail   = "preview-builder@143.dev"
	DemoViewerEmail    = "preview-viewer@143.dev"
	PrimarySessionID   = "00000000-0000-4000-a000-000000000300"
	PreviewGroupID     = "00000000-0000-4000-a000-000000000430"
	PreviewTargetID    = "00000000-0000-4000-a000-000000000431"
	DemoPullRequestID  = "00000000-0000-4000-a000-000000000501"
	DemoPullRequestURL = "https://github.com/assembledhq/143/pull/42"
	DemoRepository     = "assembledhq/143"
	DemoPRNumber       = 42
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	urlPattern   = regexp.MustCompile(`https?://[^\s'")<>]+`)

	forbiddenSeedPatterns = []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{name: "token", pattern: regexp.MustCompile(`(?i)\bgh[pousr]_[A-Za-z0-9_]{20,}\b`)},
		{name: "token", pattern: regexp.MustCompile(`(?i)\bxox[baprs]-[A-Za-z0-9-]{20,}\b`)},
		{name: "token", pattern: regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]{20,}\b`)},
		{name: "AWS access key", pattern: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
		{name: "private key", pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
		{name: "production env reference", pattern: regexp.MustCompile(`(?i)\.env\.production|ENCRYPTION_MASTER_KEY|GITHUB_APP_PRIVATE_KEY|SLACK_(BOT|SIGNING)_TOKEN|ANTHROPIC_API_KEY|OPENAI_API_KEY`)},
	}

	allowedSeedGitHubPaths = map[string]struct{}{
		"assembledhq/143":                 {},
		"assembledhq/143.git":             {},
		"assembledhq/143/pull/42":         {},
		"assembledhq/example-service":     {},
		"assembledhq/example-service.git": {},
	}
)

type CheckOptions struct {
	AdminDatabaseURL string
	SeedPath         string
}

type ApplyOptions struct {
	DatabaseURL      string
	SeedPath         string
	SkipMigrations   bool
	AllowNonDemoOrgs bool
	Env              map[string]string
}

type PruneOptions struct {
	DatabaseURL string
	MaxAge      time.Duration
	Env         map[string]string
}

type seedDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Close()
}

type applyDeps struct {
	readAndScanSeed          func(seedPath string) ([]byte, error)
	validateApplyEnvironment func(env map[string]string, databaseURL string) error
	runMigrations            func(databaseURL string) error
	connectPool              func(ctx context.Context, databaseURL string) (seedDB, error)
	ensureApplyTargetSafe    func(ctx context.Context, pool seedDB, allowNonDemoOrgs bool) error
	applySeedSQL             func(ctx context.Context, pool seedDB, seedSQL []byte) error
	assertDemoSeedState      func(ctx context.Context, pool seedDB) error
}

func ReplaceDatabaseName(databaseURL, dbName string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("database URL must use postgres:// or postgresql://")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("database URL must include a host")
	}
	if dbName == "" {
		return "", fmt.Errorf("database name is required")
	}
	parsed.Path = "/" + dbName
	return parsed.String(), nil
}

func IsProbablyProductionURL(databaseURL string) bool {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return false
	}
	target := strings.ToLower(parsed.Hostname() + " " + strings.TrimPrefix(parsed.Path, "/"))
	return regexp.MustCompile(`(^|[^a-z0-9])(prod|production)([^a-z0-9]|$)`).MatchString(target)
}

func ValidateApplyEnvironment(env map[string]string, databaseURL string) error {
	if !truthy(env["ALLOW_DEMO_SEED_APPLY"]) {
		return fmt.Errorf("refusing to apply demo seed without ALLOW_DEMO_SEED_APPLY=true")
	}
	if !truthy(env["DEMO_MODE"]) {
		return fmt.Errorf("refusing to apply demo seed without DEMO_MODE=true")
	}
	if IsProbablyProductionURL(databaseURL) && !truthy(env["DEMO_SEED_ALLOW_PRODUCTION_URL"]) {
		return fmt.Errorf("refusing to apply demo seed to production-looking database URL; set DEMO_SEED_ALLOW_PRODUCTION_URL=true only for an intentional demo target")
	}
	return nil
}

func ScanSeedSafety(body []byte) error {
	text := string(body)
	for _, candidate := range urlPattern.FindAllString(text, -1) {
		parsed, err := url.Parse(candidate)
		if err != nil {
			return fmt.Errorf("parse seed URL %q: %w", candidate, err)
		}
		if parsed.User != nil {
			return fmt.Errorf("URL %q contains credentials", candidate)
		}
		if err := validateSeedURL(parsed); err != nil {
			return err
		}
	}
	for _, match := range emailPattern.FindAllString(text, -1) {
		if !strings.HasSuffix(strings.ToLower(match), "@143.dev") {
			return fmt.Errorf("non-demo email %q found in demo seed", match)
		}
	}
	for _, forbidden := range forbiddenSeedPatterns {
		if forbidden.pattern.Match(body) {
			return fmt.Errorf("%s-looking content found in demo seed", forbidden.name)
		}
	}
	return nil
}

func validateSeedURL(parsed *url.URL) error {
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com":
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("unapproved URL path %q found in demo seed", parsed.Path)
		}
		path := strings.ToLower(strings.Trim(parsed.Path, "/"))
		if _, ok := allowedSeedGitHubPaths[path]; !ok {
			return fmt.Errorf("unapproved URL path %q found in demo seed", parsed.Path)
		}
		return nil
	default:
		return fmt.Errorf("unapproved URL host %q found in demo seed", parsed.Hostname())
	}
}

func Check(ctx context.Context, opts CheckOptions) error {
	seedPath := defaultString(opts.SeedPath, DefaultSeedPath)
	seedSQL, err := readAndScanSeed(seedPath)
	if err != nil {
		return err
	}

	adminURL := defaultString(opts.AdminDatabaseURL, DefaultDatabaseURL)
	tempName, err := tempDatabaseName()
	if err != nil {
		return err
	}
	tempURL, err := ReplaceDatabaseName(adminURL, tempName)
	if err != nil {
		return err
	}

	adminPool, err := connectPool(ctx, adminURL)
	if err != nil {
		postgresURL, replaceErr := ReplaceDatabaseName(adminURL, "postgres")
		if replaceErr != nil {
			return fmt.Errorf("connect admin database: %w", err)
		}
		adminPool, err = connectPool(ctx, postgresURL)
		if err != nil {
			return fmt.Errorf("connect admin database: %w", err)
		}
	}
	defer adminPool.Close()

	quotedTempName := pgx.Identifier{tempName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+quotedTempName); err != nil {
		return fmt.Errorf("create temporary demo seed database %q: %w", tempName, err)
	}
	defer func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, dropErr := adminPool.Exec(dropCtx, "DROP DATABASE IF EXISTS "+quotedTempName); dropErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to drop temporary demo seed database %s: %v\n", tempName, dropErr)
		}
	}()

	if err := RunMigrations(tempURL); err != nil {
		return fmt.Errorf("run migrations against temporary demo seed database: %w", err)
	}
	pool, err := connectPool(ctx, tempURL)
	if err != nil {
		return fmt.Errorf("connect temporary demo seed database: %w", err)
	}
	defer pool.Close()

	if err := ApplySeedSQL(ctx, pool, seedSQL); err != nil {
		return fmt.Errorf("apply demo seed first pass: %w", err)
	}
	if err := ApplySeedSQL(ctx, pool, seedSQL); err != nil {
		return fmt.Errorf("apply demo seed second pass: %w", err)
	}
	if err := AssertDemoSeedState(ctx, pool); err != nil {
		return err
	}

	return nil
}

func Apply(ctx context.Context, opts ApplyOptions) error {
	return apply(ctx, opts, defaultApplyDeps())
}

func Prune(ctx context.Context, opts PruneOptions) (int64, error) {
	return prune(ctx, opts, func(ctx context.Context, databaseURL string) (seedDB, error) {
		return connectPool(ctx, databaseURL)
	})
}

func prune(ctx context.Context, opts PruneOptions, connect func(context.Context, string) (seedDB, error)) (int64, error) {
	if opts.DatabaseURL == "" {
		return 0, fmt.Errorf("DEMO_SEED_DATABASE_URL or --database-url is required for demo-seed prune")
	}
	if opts.MaxAge <= 0 {
		return 0, fmt.Errorf("--max-age must be greater than zero")
	}
	if err := ValidateApplyEnvironment(opts.Env, opts.DatabaseURL); err != nil {
		return 0, err
	}
	pool, err := connect(ctx, opts.DatabaseURL)
	if err != nil {
		return 0, err
	}
	defer pool.Close()

	cutoff := time.Now().Add(-opts.MaxAge)
	tag, err := pool.Exec(ctx, `
		DELETE FROM auth_sessions
		WHERE org_id = $1::uuid
		  AND created_at < $2`, DemoOrgID, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune old demo auth sessions: %w", err)
	}

	var auditRows int64
	if err := pool.QueryRow(ctx, `SELECT delete_expired_audit_logs($1::uuid, $2)`, DemoOrgID, retentionDaysForMaxAge(opts.MaxAge)).Scan(&auditRows); err != nil {
		return 0, fmt.Errorf("prune old demo audit logs: %w", err)
	}

	return tag.RowsAffected() + auditRows, nil
}

func retentionDaysForMaxAge(maxAge time.Duration) int {
	retentionDays := int(maxAge / (24 * time.Hour))
	if maxAge%(24*time.Hour) != 0 {
		retentionDays++
	}
	if retentionDays < 1 {
		retentionDays = 1
	}
	return retentionDays
}

func apply(ctx context.Context, opts ApplyOptions, deps applyDeps) error {
	if opts.DatabaseURL == "" {
		return fmt.Errorf("DEMO_SEED_DATABASE_URL or --database-url is required for demo-seed apply")
	}
	seedSQL, err := deps.readAndScanSeed(defaultString(opts.SeedPath, DefaultSeedPath))
	if err != nil {
		return err
	}
	if err := deps.validateApplyEnvironment(opts.Env, opts.DatabaseURL); err != nil {
		return err
	}

	pool, err := deps.connectPool(ctx, opts.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := deps.ensureApplyTargetSafe(ctx, pool, opts.AllowNonDemoOrgs); err != nil {
		return err
	}
	if !opts.SkipMigrations {
		if err := deps.runMigrations(opts.DatabaseURL); err != nil {
			return fmt.Errorf("run migrations before applying demo seed: %w", err)
		}
	}
	if err := deps.applySeedSQL(ctx, pool, seedSQL); err != nil {
		return err
	}
	if err := deps.assertDemoSeedState(ctx, pool); err != nil {
		return err
	}
	return nil
}

func defaultApplyDeps() applyDeps {
	return applyDeps{
		readAndScanSeed:          readAndScanSeed,
		validateApplyEnvironment: ValidateApplyEnvironment,
		runMigrations:            RunMigrations,
		connectPool: func(ctx context.Context, databaseURL string) (seedDB, error) {
			return connectPool(ctx, databaseURL)
		},
		ensureApplyTargetSafe: EnsureApplyTargetSafe,
		applySeedSQL:          ApplySeedSQL,
		assertDemoSeedState:   AssertDemoSeedState,
	}
}

func ApplySeedSQL(ctx context.Context, pool seedDB, seedSQL []byte) error {
	if _, err := pool.Exec(ctx, string(seedSQL)); err != nil {
		return fmt.Errorf("execute demo seed SQL: %w", err)
	}
	return nil
}

func EnsureApplyTargetSafe(ctx context.Context, pool seedDB, allowNonDemoOrgs bool) error {
	var organizationsExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.organizations') IS NOT NULL`).Scan(&organizationsExists); err != nil {
		return fmt.Errorf("inspect organizations table before demo seed apply: %w", err)
	}
	if !organizationsExists {
		return nil
	}

	var nonDemoOrgs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizations WHERE id <> $1`, DemoOrgID).Scan(&nonDemoOrgs); err != nil {
		return fmt.Errorf("inspect existing organizations before demo seed apply: %w", err)
	}
	if nonDemoOrgs > 0 && !allowNonDemoOrgs {
		return fmt.Errorf("refusing to apply demo seed because target database contains %d non-demo organization(s); set DEMO_SEED_ALLOW_NONDEMO_ORGS=true only for an intentional mixed dev database", nonDemoOrgs)
	}
	return nil
}

func AssertDemoSeedState(ctx context.Context, pool seedDB) error {
	assertions := []struct {
		name     string
		query    string
		expected int
	}{
		{
			name:     "demo org exists",
			query:    `SELECT count(*) FROM organizations WHERE id = '00000000-0000-4000-a000-000000000001'::uuid`,
			expected: 1,
		},
		{
			name:     "demo users exist",
			query:    `SELECT count(*) FROM users WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND email IN ('preview-admin@143.dev', 'preview-member@143.dev', 'preview-builder@143.dev', 'preview-viewer@143.dev')`,
			expected: 4,
		},
		{
			name:     "demo users are passwordless",
			query:    `SELECT count(*) FROM users WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND email IN ('preview-admin@143.dev', 'preview-member@143.dev', 'preview-builder@143.dev', 'preview-viewer@143.dev') AND password_hash IS NULL`,
			expected: 4,
		},
		{
			name:     "demo memberships exist",
			query:    `SELECT count(*) FROM organization_memberships WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid`,
			expected: 4,
		},
		{
			name:     "demo sessions exist",
			query:    `SELECT count(*) FROM sessions WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000302'::uuid, '00000000-0000-4000-a000-000000000303'::uuid, '00000000-0000-4000-a000-000000000304'::uuid)`,
			expected: 5,
		},
		{
			name:     "demo issues exist",
			query:    `SELECT count(*) FROM issues WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000600'::uuid, '00000000-0000-4000-a000-000000000601'::uuid, '00000000-0000-4000-a000-000000000602'::uuid, '00000000-0000-4000-a000-000000000603'::uuid, '00000000-0000-4000-a000-000000000604'::uuid)`,
			expected: 5,
		},
		{
			name:     "demo issue priority scores exist",
			query:    `SELECT count(*) FROM priority_scores WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND issue_id IN ('00000000-0000-4000-a000-000000000600'::uuid, '00000000-0000-4000-a000-000000000601'::uuid, '00000000-0000-4000-a000-000000000602'::uuid, '00000000-0000-4000-a000-000000000603'::uuid, '00000000-0000-4000-a000-000000000604'::uuid)`,
			expected: 5,
		},
		{
			name:     "demo issue complexity estimates exist",
			query:    `SELECT count(*) FROM complexity_estimates WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND issue_id IN ('00000000-0000-4000-a000-000000000600'::uuid, '00000000-0000-4000-a000-000000000601'::uuid, '00000000-0000-4000-a000-000000000602'::uuid, '00000000-0000-4000-a000-000000000603'::uuid, '00000000-0000-4000-a000-000000000604'::uuid)`,
			expected: 5,
		},
		{
			name:     "demo session issue links exist",
			query:    `SELECT count(*) FROM session_issue_links WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND session_id IN ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000302'::uuid, '00000000-0000-4000-a000-000000000303'::uuid, '00000000-0000-4000-a000-000000000304'::uuid)`,
			expected: 5,
		},
		{
			name:     "demo session threads exist",
			query:    `SELECT count(*) FROM session_threads WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000700'::uuid, '00000000-0000-4000-a000-000000000701'::uuid, '00000000-0000-4000-a000-000000000702'::uuid, '00000000-0000-4000-a000-000000000703'::uuid, '00000000-0000-4000-a000-000000000704'::uuid)`,
			expected: 5,
		},
		{
			name:     "demo session thread file events exist",
			query:    `SELECT count(*) FROM session_thread_file_events WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND session_id IN ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000302'::uuid, '00000000-0000-4000-a000-000000000303'::uuid, '00000000-0000-4000-a000-000000000304'::uuid)`,
			expected: 5,
		},
		{
			name:     "demo review comments exist",
			query:    `SELECT count(*) FROM session_review_comments WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000730'::uuid, '00000000-0000-4000-a000-000000000731'::uuid)`,
			expected: 2,
		},
		{
			name:     "demo validation rows exist",
			query:    `SELECT count(*) FROM validations WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000750'::uuid, '00000000-0000-4000-a000-000000000751'::uuid)`,
			expected: 2,
		},
		{
			name:     "demo session questions exist",
			query:    `SELECT count(*) FROM session_questions WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000740'::uuid, '00000000-0000-4000-a000-000000000741'::uuid)`,
			expected: 2,
		},
		{
			name:     "demo session issue snapshots exist",
			query:    `SELECT count(*) FROM session_turn_issue_snapshots WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000640'::uuid, '00000000-0000-4000-a000-000000000641'::uuid, '00000000-0000-4000-a000-000000000642'::uuid)`,
			expected: 3,
		},
		{
			name:     "demo PR exists",
			query:    `SELECT count(*) FROM pull_requests WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id = '00000000-0000-4000-a000-000000000501'::uuid`,
			expected: 1,
		},
		{
			name:     "demo preview uses sentinel runtime",
			query:    `SELECT count(*) FROM preview_instances WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id = '00000000-0000-4000-a000-000000000400'::uuid AND provider = 'seeded' AND worker_node_id = 'seeded'`,
			expected: 1,
		},
		{
			name:     "demo preview target exists",
			query:    `SELECT count(*) FROM preview_targets WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id = '00000000-0000-4000-a000-000000000431'::uuid AND preview_group_id = '00000000-0000-4000-a000-000000000430'::uuid`,
			expected: 1,
		},
		{
			name:     "demo preview group points at current target",
			query:    `SELECT count(*) FROM preview_groups WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id = '00000000-0000-4000-a000-000000000430'::uuid AND current_target_id = '00000000-0000-4000-a000-000000000431'::uuid`,
			expected: 1,
		},
		{
			name:     "demo preview runtime exists",
			query:    `SELECT count(*) FROM preview_runtimes WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id = '00000000-0000-4000-a000-000000000433'::uuid`,
			expected: 1,
		},
		{
			name:     "demo preview logs exist",
			query:    `SELECT count(*) FROM preview_logs WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000435'::uuid, '00000000-0000-4000-a000-000000000436'::uuid)`,
			expected: 2,
		},
		{
			name:     "demo PR health current exists",
			query:    `SELECT count(*) FROM pull_request_health_current WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND pull_request_id = '00000000-0000-4000-a000-000000000501'::uuid`,
			expected: 1,
		},
		{
			name:     "demo PR health snapshot exists",
			query:    `SELECT count(*) FROM pull_request_health_snapshots WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND pull_request_id = '00000000-0000-4000-a000-000000000501'::uuid AND version = 1`,
			expected: 1,
		},
		{
			name:     "demo repository PR template exists",
			query:    `SELECT count(*) FROM repository_pr_templates WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id = '00000000-0000-4000-a000-000000000120'::uuid`,
			expected: 1,
		},
	}
	for _, assertion := range assertions {
		var actual int
		if err := pool.QueryRow(ctx, assertion.query).Scan(&actual); err != nil {
			return fmt.Errorf("assert demo seed state %q: %w", assertion.name, err)
		}
		if actual != assertion.expected {
			return fmt.Errorf("assert demo seed state %q: got %d, want %d", assertion.name, actual, assertion.expected)
		}
	}

	var duplicateMessages int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT session_id, role, turn_number, content
			FROM session_messages
			WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
			  AND session_id IN (
			    '00000000-0000-4000-a000-000000000300'::uuid,
			    '00000000-0000-4000-a000-000000000301'::uuid,
			    '00000000-0000-4000-a000-000000000302'::uuid,
			    '00000000-0000-4000-a000-000000000303'::uuid,
			    '00000000-0000-4000-a000-000000000304'::uuid
			  )
			GROUP BY session_id, role, turn_number, content
			HAVING count(*) > 1
		) duplicates
	`).Scan(&duplicateMessages); err != nil {
		return fmt.Errorf("assert demo seed message idempotency: %w", err)
	}
	if duplicateMessages != 0 {
		return fmt.Errorf("assert demo seed message idempotency: found %d duplicate message group(s)", duplicateMessages)
	}

	var duplicateLogs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT session_id, level, message, turn_number
			FROM session_logs
			WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
			  AND session_id IN (
			    '00000000-0000-4000-a000-000000000300'::uuid,
			    '00000000-0000-4000-a000-000000000301'::uuid,
			    '00000000-0000-4000-a000-000000000302'::uuid,
			    '00000000-0000-4000-a000-000000000303'::uuid,
			    '00000000-0000-4000-a000-000000000304'::uuid
			  )
			GROUP BY session_id, level, message, turn_number
			HAVING count(*) > 1
		) duplicates
	`).Scan(&duplicateLogs); err != nil {
		return fmt.Errorf("assert demo seed log idempotency: %w", err)
	}
	if duplicateLogs != 0 {
		return fmt.Errorf("assert demo seed log idempotency: found %d duplicate log group(s)", duplicateLogs)
	}

	var diffPresent bool
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(length(diff) > 0, false)
		FROM sessions
		WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
		  AND id = '00000000-0000-4000-a000-000000000300'::uuid
	`).Scan(&diffPresent); err != nil {
		return fmt.Errorf("assert demo session diff: %w", err)
	}
	if !diffPresent {
		return fmt.Errorf("assert demo session diff: session 00000000-0000-4000-a000-000000000300 has no diff")
	}

	return nil
}

func RunMigrations(databaseURL string) (err error) {
	source, err := ResolveMigrationSource()
	if err != nil {
		return err
	}
	m, err := migrate.New(source, databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		sourceErr, dbErr := m.Close()
		if err == nil && sourceErr != nil {
			err = sourceErr
		}
		if err == nil && dbErr != nil {
			err = dbErr
		}
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func ResolveMigrationSource() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return "file://" + candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if execPath, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(execPath), "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return "file://" + candidate, nil
		}
	}
	return "", fmt.Errorf("could not locate migrations directory")
}

func Environment(keys ...string) map[string]string {
	env := make(map[string]string, len(keys))
	for _, key := range keys {
		env[key] = os.Getenv(key)
	}
	return env
}

// ReadAndScanSeed reads a seed SQL file or ordered fragment directory and
// rejects content that is unsafe for public/demo data.
func ReadAndScanSeed(seedPath string) ([]byte, error) {
	return readAndScanSeed(seedPath)
}

func readAndScanSeed(seedPath string) ([]byte, error) {
	body, err := readSeedBody(seedPath)
	if err != nil {
		return nil, err
	}
	if err := ScanSeedSafety(body); err != nil {
		return nil, err
	}
	return body, nil
}

func readSeedBody(seedPath string) ([]byte, error) {
	info, err := os.Stat(seedPath)
	if err != nil {
		return nil, fmt.Errorf("read demo seed %s: %w", seedPath, err)
	}
	if !info.IsDir() {
		body, err := os.ReadFile(seedPath) // #nosec G304 -- seedPath is an operator-supplied local seed file for developer/demo tooling; contents are scanned before execution.
		if err != nil {
			return nil, fmt.Errorf("read demo seed %s: %w", seedPath, err)
		}
		return body, nil
	}

	entries, err := os.ReadDir(seedPath)
	if err != nil {
		return nil, fmt.Errorf("read demo seed directory %s: %w", seedPath, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("read demo seed directory %s: no .sql seed fragments found", seedPath)
	}

	var seed strings.Builder
	for i, name := range names {
		if i > 0 {
			seed.WriteString("\n")
		}
		fragmentPath := filepath.Join(seedPath, name)
		fragment, err := os.ReadFile(fragmentPath) // #nosec G304 -- fragment names come from os.ReadDir(seedPath); contents are scanned before execution.
		if err != nil {
			return nil, fmt.Errorf("read demo seed fragment %s: %w", fragmentPath, err)
		}
		seed.Write(fragment)
		if !bytes.HasSuffix(fragment, []byte("\n")) {
			seed.WriteString("\n")
		}
	}
	return []byte(seed.String()), nil
}

func connectPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func tempDatabaseName() (string, error) {
	var randomBytes [4]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("generate temporary database suffix: %w", err)
	}
	return fmt.Sprintf("oft_demo_seed_check_%d_%s", os.Getpid(), hex.EncodeToString(randomBytes[:])), nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "yes", "y":
		return true
	default:
		return false
	}
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
