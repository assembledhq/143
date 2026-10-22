// Package postgres provides isolated, fully migrated PostgreSQL test databases.
// TEST_DATABASE_URL must name a disposable server and a role with CREATEDB.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog"
)

const cleanupTimeout = 30 * time.Second

var shared manager

// NewPool clones an isolated database from a template migrated once per process.
// A zero maxConns preserves pgxpool's default. Call Run from the package's
// TestMain so templates outlive all parallel tests and are removed afterward.
func NewPool(t *testing.T, ctx context.Context, migrationsDir string, maxConns int32) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for database fixtures")
	}
	pool, cleanup, err := shared.newPool(ctx, raw, migrationsDir, maxConns)
	if err != nil {
		t.Fatalf("create isolated migrated PostgreSQL fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("remove isolated PostgreSQL fixture: %v", err)
		}
	})
	return pool
}

// Run executes a test package and removes its shared templates after every test
// and cleanup has finished. Cleanup errors must fail an otherwise passing suite.
func Run(m *testing.M) int {
	code := m.Run()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := shared.close(ctx); err != nil {
		logger := zerolog.New(os.Stderr)
		logger.Error().Err(err).Msg("remove PostgreSQL test templates")
		if code == 0 {
			code = 1
		}
	}
	return code
}

type template struct {
	ready  chan struct{}
	name   string
	config *pgx.ConnConfig
	err    error
}

type manager struct {
	mu        sync.Mutex
	templates map[[sha256.Size]byte]*template
	closed    bool
}

func (m *manager) newPool(ctx context.Context, raw, migrationsDir string, maxConns int32) (*pgxpool.Pool, func() error, error) {
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		// Parse errors may include the original DSN, including its password.
		return nil, nil, errors.New("invalid TEST_DATABASE_URL")
	}
	if maxConns < 0 {
		return nil, nil, errors.New("fixture maxConns must not be negative")
	}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}
	source, err := filepath.Abs(migrationsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve fixture migrations: %w", err)
	}
	key, err := templateKey(config.ConnConfig, source)
	if err != nil {
		return nil, nil, err
	}
	base, err := m.getTemplate(ctx, key, config.ConnConfig, source)
	if err != nil {
		return nil, nil, err
	}
	name := databaseName("fixture")
	if err := withAdmin(ctx, config.ConnConfig, func(admin *pgx.Conn) error {
		_, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{base.name}.Sanitize())
		return err
	}); err != nil {
		// A connection error can arrive after CREATE DATABASE committed. The
		// random name belongs only to this attempt, so remove it in either case.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		return nil, nil, errors.Join(fmt.Errorf("clone fixture database: %w", err), dropDatabase(cleanupCtx, base.config, name))
	}
	config.ConnConfig.Database = name
	config.ConnConfig.RuntimeParams["search_path"] = "public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err == nil {
		err = pool.Ping(ctx)
	}
	cleanup := func() error {
		if pool != nil {
			pool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		return dropDatabase(cleanupCtx, base.config, name)
	}
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("open fixture pool: %w", err), cleanup())
	}
	return pool, cleanup, nil
}

func (m *manager) getTemplate(ctx context.Context, key [sha256.Size]byte, config *pgx.ConnConfig, source string) (*template, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("PostgreSQL fixture manager is closed")
	}
	if m.templates == nil {
		m.templates = make(map[[sha256.Size]byte]*template)
	}
	entry, found := m.templates[key]
	if !found {
		entry = &template{ready: make(chan struct{}), name: databaseName("template"), config: config.Copy()}
		m.templates[key] = entry
	}
	m.mu.Unlock()
	if !found {
		// Publish the result through ready, without calling testing.Fatal or
		// holding the registry lock while PostgreSQL applies migrations.
		entry.err = buildTemplate(ctx, entry, source)
		close(entry.ready)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.ready:
		if entry.err != nil {
			return nil, entry.err
		}
		return entry, nil
	}
}

func (m *manager) close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	entries := make([]*template, 0, len(m.templates))
	for _, entry := range m.templates {
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	var result error
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		case <-entry.ready:
		}
		// Failed initialization already attempted cleanup; retry DROP IF EXISTS
		// here too, so transient cleanup failures cannot silently leak a template.
		result = errors.Join(result, dropDatabase(ctx, entry.config, entry.name))
	}
	return result
}

func buildTemplate(ctx context.Context, entry *template, source string) (result error) {
	attempted := false
	defer func() {
		if result != nil && attempted {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			result = errors.Join(result, dropDatabase(cleanupCtx, entry.config, entry.name))
		}
	}()
	if err := withAdmin(ctx, entry.config, func(admin *pgx.Conn) error {
		attempted = true
		_, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{entry.name}.Sanitize()+" TEMPLATE template0")
		return err
	}); err != nil {
		return fmt.Errorf("create fixture template (TEST_DATABASE_URL role needs CREATEDB): %w", err)
	}
	config := entry.config.Copy()
	config.Database = entry.name
	config.RuntimeParams["search_path"] = "public"
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	client := stdlib.OpenDB(*config)
	driver, err := migratepostgres.WithInstance(client, &migratepostgres.Config{
		DatabaseName: entry.name, SchemaName: "public", StatementTimeout: 2 * time.Minute,
	})
	if err != nil {
		return errors.Join(fmt.Errorf("initialize fixture migration driver: %w", err), client.Close())
	}
	sourceURL := (&url.URL{Scheme: "file", Path: source}).String()
	migration, err := migrate.NewWithDatabaseInstance(sourceURL, "postgres", driver)
	if err != nil {
		return errors.Join(fmt.Errorf("open fixture migrations: %w", err), driver.Close())
	}
	upErr := migration.Up()
	sourceErr, databaseErr := migration.Close()
	if errors.Is(upErr, migrate.ErrNoChange) {
		upErr = nil
	}
	if err := errors.Join(upErr, sourceErr, databaseErr); err != nil {
		return fmt.Errorf("migrate fixture template: %w", err)
	}
	// CREATE DATABASE cannot copy a source with active connections. Closing the
	// migrator above and sealing the template keeps every clone deterministic.
	if err := withAdmin(ctx, entry.config, func(admin *pgx.Conn) error {
		_, err := admin.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{entry.name}.Sanitize()+" ALLOW_CONNECTIONS false")
		return err
	}); err != nil {
		return fmt.Errorf("seal fixture template: %w", err)
	}
	return nil
}

func databaseName(kind string) string {
	return "test_" + kind + "_" + uuid.NewString()
}

func withAdmin(ctx context.Context, config *pgx.ConnConfig, operation func(*pgx.Conn) error) (result error) {
	admin, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		result = errors.Join(result, admin.Close(closeCtx))
	}()
	return operation(admin)
}

func dropDatabase(ctx context.Context, config *pgx.ConnConfig, name string) error {
	return withAdmin(ctx, config, func(admin *pgx.Conn) error {
		_, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		return err
	})
}

func templateKey(config *pgx.ConnConfig, source string) ([sha256.Size]byte, error) {
	// JSON sorts runtime parameter keys. Include the normalized endpoint,
	// identity and database rather than URL spelling; the digest is never logged.
	type endpoint struct {
		Host string
		Port uint16
	}
	identity := struct {
		Endpoint endpoint
		Database string
		User     string
		Password string
		Runtime  map[string]string
		Fallback []endpoint
	}{Endpoint: endpoint{config.Host, config.Port}, Database: config.Database,
		User: config.User, Password: config.Password, Runtime: config.RuntimeParams}
	for _, fallback := range config.Fallbacks {
		identity.Fallback = append(identity.Fallback, endpoint{fallback.Host, fallback.Port})
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("fingerprint fixture database configuration: %w", err)
	}
	files, err := filepath.Glob(filepath.Join(source, "*.sql"))
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("list fixture migrations: %w", err)
	}
	if len(files) == 0 {
		return [sha256.Size]byte{}, errors.New("fixture migration directory contains no SQL migrations")
	}
	sort.Strings(files)
	hash := sha256.New()
	// Hash.Write cannot fail. Framing prevents ambiguous boundaries between
	// configuration, paths, names and migration contents.
	write := func(value []byte) {
		hash.Write([]byte(fmt.Sprintf("%d:", len(value))))
		hash.Write(value)
	}
	write(encoded)
	write([]byte(source))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return [sha256.Size]byte{}, fmt.Errorf("read fixture migration: %w", err)
		}
		write([]byte(filepath.Base(path)))
		write(content)
	}
	var key [sha256.Size]byte
	copy(key[:], hash.Sum(nil))
	return key, nil
}
