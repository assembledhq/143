package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/assembledhq/143/internal/demoseed"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	switch args[0] {
	case "check":
		return runCheck(ctx, args[1:], stdout, stderr)
	case "apply":
		return runApply(ctx, args[1:], stdout, stderr)
	case "prune":
		return runPrune(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "unknown subcommand")
		fmt.Fprintln(stderr)
		usage(stderr)
		return 2
	}
}

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	adminURL := fs.String("database-url", firstNonEmpty(os.Getenv("DEMO_SEED_CHECK_DATABASE_URL"), os.Getenv("DATABASE_URL"), demoseed.DefaultDatabaseURL), "writable Postgres URL used to create a temporary sibling database")
	seedPath := fs.String("seed", demoseed.DefaultSeedPath, "path to demo seed SQL file or fragment directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if demoseed.IsProbablyProductionURL(*adminURL) && !envTruthy(os.Getenv("DEMO_SEED_ALLOW_PRODUCTION_URL")) {
		fmt.Fprintln(stderr, "demo seed check refused a production-looking database URL; set DEMO_SEED_ALLOW_PRODUCTION_URL=true only for an intentional demo/check target")
		return 1
	}

	fmt.Fprintln(stdout, "Checking demo seed against a temporary migrated database...")
	if err := demoseed.Check(ctx, demoseed.CheckOptions{
		AdminDatabaseURL: *adminURL,
		SeedPath:         *seedPath,
	}); err != nil {
		fmt.Fprintf(stderr, "demo seed check failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Demo seed check passed.")
	return 0
}

func runApply(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	databaseURL := fs.String("database-url", os.Getenv("DEMO_SEED_DATABASE_URL"), "demo database URL to migrate and seed")
	seedPath := fs.String("seed", demoseed.DefaultSeedPath, "path to demo seed SQL file or fragment directory")
	skipMigrations := fs.Bool("skip-migrations", false, "apply seed without running migrations first")
	allowNonDemoOrgs := fs.Bool("allow-nondemo-orgs", envTruthy(os.Getenv("DEMO_SEED_ALLOW_NONDEMO_ORGS")), "allow seeding into a database that already contains non-demo organizations")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *databaseURL == "" {
		fmt.Fprintln(stderr, "DEMO_SEED_DATABASE_URL or --database-url is required for demo-seed apply")
		return 1
	}

	fmt.Fprintln(stdout, "Applying demo seed to target database...")
	if err := demoseed.Apply(ctx, demoseed.ApplyOptions{
		DatabaseURL:      *databaseURL,
		SeedPath:         *seedPath,
		SkipMigrations:   *skipMigrations,
		AllowNonDemoOrgs: *allowNonDemoOrgs,
		Env: demoseed.Environment(
			"ALLOW_DEMO_SEED_APPLY",
			"DEMO_MODE",
			"DEMO_SEED_ALLOW_PRODUCTION_URL",
		),
	}); err != nil {
		fmt.Fprintf(stderr, "demo seed apply failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Demo seed applied.")
	return 0
}

func runPrune(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	databaseURL := fs.String("database-url", os.Getenv("DEMO_SEED_DATABASE_URL"), "demo database URL to prune")
	maxAge := fs.Duration("max-age", 24*time.Hour, "delete volatile demo auth sessions older than this duration")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *databaseURL == "" {
		fmt.Fprintln(stderr, "DEMO_SEED_DATABASE_URL or --database-url is required for demo-seed prune")
		return 1
	}

	fmt.Fprintln(stdout, "Pruning volatile demo state...")
	pruned, err := demoseed.Prune(ctx, demoseed.PruneOptions{
		DatabaseURL: *databaseURL,
		MaxAge:      *maxAge,
		Env: demoseed.Environment(
			"ALLOW_DEMO_SEED_APPLY",
			"DEMO_MODE",
			"DEMO_SEED_ALLOW_PRODUCTION_URL",
		),
	})
	if err != nil {
		fmt.Fprintf(stderr, "demo seed prune failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Pruned %d volatile demo row(s).\n", pruned)
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: demo-seed [check|apply|prune]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  check  creates a temporary database, runs migrations, applies .143/seed twice, and verifies safety/idempotency")
	fmt.Fprintln(w, "  apply  migrates and applies .143/seed to an explicit demo database target")
	fmt.Fprintln(w, "  prune  deletes old volatile state from an explicit demo database target")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func envTruthy(value string) bool {
	switch value {
	case "1", "t", "true", "TRUE", "yes", "YES", "y", "Y":
		return true
	default:
		return false
	}
}
