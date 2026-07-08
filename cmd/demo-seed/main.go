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
	seedPath := fs.String("seed", demoseed.DefaultSeedPath, "path to preview seed SQL file or fragment directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if demoseed.IsProbablyProductionURL(*adminURL) && !envTruthy(os.Getenv("DEMO_SEED_ALLOW_PRODUCTION_URL")) {
		fmt.Fprintln(stderr, "preview seed check refused a production-looking database URL; set DEMO_SEED_ALLOW_PRODUCTION_URL=true only for an intentional preview/check target")
		return 1
	}

	fmt.Fprintln(stdout, "Checking preview seed against a temporary migrated database...")
	if err := demoseed.Check(ctx, demoseed.CheckOptions{
		AdminDatabaseURL: *adminURL,
		SeedPath:         *seedPath,
	}); err != nil {
		fmt.Fprintf(stderr, "preview seed check failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Preview seed check passed.")
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: demo-seed check")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  check  creates a temporary database, runs migrations, applies .143/seed twice, and verifies safety/idempotency")
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
