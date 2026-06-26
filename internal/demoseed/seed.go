package demoseed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultDatabaseURL = "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable" // #nosec G101 -- dev-only default.
	DefaultSeedPath    = ".143/seed.sql"
	DemoOrgID          = "00000000-0000-4000-a000-000000000001"
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

	allowedSeedURLHosts = map[string]struct{}{
		"github.com": {},
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
		if _, ok := allowedSeedURLHosts[strings.ToLower(parsed.Hostname())]; !ok {
			return fmt.Errorf("unapproved URL host %q found in demo seed", parsed.Hostname())
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
	if opts.DatabaseURL == "" {
		return fmt.Errorf("DEMO_SEED_DATABASE_URL or --database-url is required for demo-seed apply")
	}
	seedSQL, err := readAndScanSeed(defaultString(opts.SeedPath, DefaultSeedPath))
	if err != nil {
		return err
	}
	if err := ValidateApplyEnvironment(opts.Env, opts.DatabaseURL); err != nil {
		return err
	}
	if !opts.SkipMigrations {
		if err := RunMigrations(opts.DatabaseURL); err != nil {
			return fmt.Errorf("run migrations before applying demo seed: %w", err)
		}
	}

	pool, err := connectPool(ctx, opts.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := EnsureApplyTargetSafe(ctx, pool, opts.AllowNonDemoOrgs); err != nil {
		return err
	}
	if err := ApplySeedSQL(ctx, pool, seedSQL); err != nil {
		return err
	}
	if err := AssertDemoSeedState(ctx, pool); err != nil {
		return err
	}
	return nil
}

func ApplySeedSQL(ctx context.Context, pool *pgxpool.Pool, seedSQL []byte) error {
	if _, err := pool.Exec(ctx, string(seedSQL)); err != nil {
		return fmt.Errorf("execute demo seed SQL: %w", err)
	}
	return nil
}

func EnsureApplyTargetSafe(ctx context.Context, pool *pgxpool.Pool, allowNonDemoOrgs bool) error {
	var nonDemoOrgs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizations WHERE id <> $1`, DemoOrgID).Scan(&nonDemoOrgs); err != nil {
		return fmt.Errorf("inspect existing organizations before demo seed apply: %w", err)
	}
	if nonDemoOrgs > 0 && !allowNonDemoOrgs {
		return fmt.Errorf("refusing to apply demo seed because target database contains %d non-demo organization(s); set DEMO_SEED_ALLOW_NONDEMO_ORGS=true only for an intentional mixed dev database", nonDemoOrgs)
	}
	return nil
}

func AssertDemoSeedState(ctx context.Context, pool *pgxpool.Pool) error {
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
			name:     "demo memberships exist",
			query:    `SELECT count(*) FROM organization_memberships WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid`,
			expected: 4,
		},
		{
			name:     "demo sessions exist",
			query:    `SELECT count(*) FROM sessions WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid AND id IN ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000302'::uuid)`,
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
			    '00000000-0000-4000-a000-000000000302'::uuid
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
			    '00000000-0000-4000-a000-000000000302'::uuid
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

func readAndScanSeed(seedPath string) ([]byte, error) {
	body, err := os.ReadFile(seedPath)
	if err != nil {
		return nil, fmt.Errorf("read demo seed %s: %w", seedPath, err)
	}
	if err := ScanSeedSafety(body); err != nil {
		return nil, err
	}
	return body, nil
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
