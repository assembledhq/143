package main

// Release-channel schema gates. The canary deploy pipeline owns migrations;
// the stable plane never migrates. Three pieces implement the compatibility
// contract from docs/design/118-canary-stable-release-channels.md:
//
//  1. `migrate up` refuses to apply a pending destructive migration until the
//     currently deployed stable release is new enough (its migration set must
//     reach the migration's annotated floor). The deploy pipeline passes that
//     as STABLE_MAX_MIGRATION; when unset (local dev, CI) the gate is off.
//  2. After a successful `up`, applied destructive floors are persisted to
//     schema_compat_floors so later stable preflights can enforce them
//     without parsing annotations from newer checkouts.
//  3. `migrate verify` is the stable preflight: schema must be at least this
//     checkout's max migration, not dirty, and this checkout must satisfy
//     every recorded destructive floor.
//
// Destructive migrations carry an annotation in the .up.sql file:
//
//	-- lint:destructive-ok-after schema="000240" reason="stable >= 000240 no longer reads issues.legacy_state"
//
// meaning: apply only when the stable ref's own migration set includes
// 000240 (i.e. stable code was built after the expand step landed).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	migrationFileRE       = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)
	destructiveOkAfterRE  = regexp.MustCompile(`--[^\n]*lint:destructive-ok-after\s+schema="(\d+)"\s+reason="[^"]+"`)
	undefinedTableSQLCode = "42P01"
)

// migrationDirFromSource converts the file:// source URL resolveMigrationSource
// returns back into a filesystem path.
func migrationDirFromSource(source string) string {
	return strings.TrimPrefix(source, "file://")
}

// maxLocalMigrationVersion returns the highest migration number present in
// this checkout's migrations directory — the same notion of "expected schema
// version" the worker deploy preflight uses.
func maxLocalMigrationVersion(dir string) (uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read migrations dir: %w", err)
	}
	var maxVersion uint64
	for _, entry := range entries {
		match := migrationFileRE.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseUint(match[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse migration version from %q: %w", entry.Name(), err)
		}
		if version > maxVersion {
			maxVersion = version
		}
	}
	if maxVersion == 0 {
		return 0, fmt.Errorf("no *.up.sql migrations found in %s", dir)
	}
	return maxVersion, nil
}

// parseDestructiveFloors scans every .up.sql migration for the
// lint:destructive-ok-after annotation and returns migration version →
// required stable floor.
func parseDestructiveFloors(dir string) (map[uint64]uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	floors := make(map[uint64]uint64)
	for _, entry := range entries {
		match := migrationFileRE.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseUint(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version from %q: %w", entry.Name(), err)
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name())) // #nosec G304 -- migrations dir is deploy-controlled
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		annotation := destructiveOkAfterRE.FindSubmatch(content)
		if annotation == nil {
			continue
		}
		floor, err := strconv.ParseUint(string(annotation[1]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse destructive floor in %q: %w", entry.Name(), err)
		}
		floors[version] = floor
	}
	return floors, nil
}

// checkDestructiveGate enforces rule (1): with STABLE_MAX_MIGRATION set, any
// pending destructive migration whose floor exceeds the deployed stable ref's
// migration set blocks the run. Returns the blocking versions for the error
// message, sorted for determinism.
func checkDestructiveGate(floors map[uint64]uint64, currentDBVersion, stableMax uint64) []uint64 {
	var blocked []uint64
	for version, floor := range floors {
		if version <= currentDBVersion {
			continue // already applied; recorded in schema_compat_floors instead
		}
		if floor > stableMax {
			blocked = append(blocked, version)
		}
	}
	sort.Slice(blocked, func(i, j int) bool { return blocked[i] < blocked[j] })
	return blocked
}

func runDestructiveGate(dir string, currentDBVersion uint64) error {
	stableMaxRaw := strings.TrimSpace(os.Getenv("STABLE_MAX_MIGRATION"))
	if stableMaxRaw == "" {
		return nil // gate off outside the canary deploy pipeline
	}
	stableMax, err := strconv.ParseUint(stableMaxRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("STABLE_MAX_MIGRATION must be a migration number, got %q: %w", stableMaxRaw, err)
	}
	floors, err := parseDestructiveFloors(dir)
	if err != nil {
		return err
	}
	if blocked := checkDestructiveGate(floors, currentDBVersion, stableMax); len(blocked) > 0 {
		return fmt.Errorf(
			"destructive migrations %v are gated: their lint:destructive-ok-after floors exceed the deployed stable release's migration set (%06d); promote stable first, then redeploy",
			blocked, stableMax)
	}
	return nil
}

// recordAppliedFloors persists rule (2): after a successful up, every applied
// destructive migration's floor lands in schema_compat_floors. Idempotent via
// ON CONFLICT DO NOTHING; tolerates the table not existing yet (a checkout
// older than 000245 applying forward).
func recordAppliedFloors(ctx context.Context, conn *pgx.Conn, floors map[uint64]uint64, appliedThrough uint64) error {
	for version, floor := range floors {
		if version > appliedThrough {
			continue
		}
		_, err := conn.Exec(ctx, `
			INSERT INTO schema_compat_floors (migration_version, stable_floor)
			VALUES ($1, $2)
			ON CONFLICT (migration_version) DO NOTHING`, int64(version), int64(floor)) // #nosec G115 -- migration numbers are far below int64 max
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == undefinedTableSQLCode {
				fmt.Fprintln(os.Stderr, "WARNING: schema_compat_floors does not exist; skipping floor recording (schema predates 000245)")
				return nil
			}
			return fmt.Errorf("record destructive floor for %06d: %w", version, err)
		}
	}
	return nil
}

// runVerify implements `migrate verify`, the stable-plane preflight (3).
func runVerify(ctx context.Context, dbURL, dir string) error {
	localMax, err := maxLocalMigrationVersion(dir)
	if err != nil {
		return err
	}

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect for verify: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var dbVersion int64
	var dirty bool
	if err := conn.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&dbVersion, &dirty); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	if dirty {
		return fmt.Errorf("schema is dirty at version %d; repair before deploying", dbVersion)
	}
	if dbVersion < 0 || uint64(dbVersion) < localMax {
		return fmt.Errorf("schema version %d is older than this checkout's migration set (%06d); the canary pipeline owns migrations and must run first", dbVersion, localMax)
	}

	var maxFloor int64
	err = conn.QueryRow(ctx, `SELECT COALESCE(MAX(stable_floor), 0) FROM schema_compat_floors`).Scan(&maxFloor)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == undefinedTableSQLCode {
			maxFloor = 0 // schema predates 000245; no floors can exist
		} else {
			return fmt.Errorf("read schema_compat_floors: %w", err)
		}
	}
	if maxFloor > 0 && localMax < uint64(maxFloor) {
		return fmt.Errorf(
			"this checkout's migration set (%06d) is below the destructive-compatibility floor (%06d): an applied destructive migration removed schema this ref may still depend on; deploy a newer release",
			localMax, maxFloor)
	}

	fmt.Printf("Schema verify OK: db=%d local=%06d floor=%06d dirty=false\n", dbVersion, localMax, maxFloor)
	return nil
}

// recordFloorsAfterUp connects and persists applied destructive floors; used
// by `migrate up` after a successful run.
func recordFloorsAfterUp(ctx context.Context, dbURL, dir string, appliedThrough uint64) error {
	floors, err := parseDestructiveFloors(dir)
	if err != nil {
		return err
	}
	if len(floors) == 0 {
		return nil
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect for floor recording: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	return recordAppliedFloors(ctx, conn, floors, appliedThrough)
}
