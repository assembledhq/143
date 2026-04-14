// Command backfill-usage populates the usage_hourly rollup table from
// historical container_usage_events and sessions.token_usage data.
//
// Usage:
//
//	DATABASE_URL=... go run cmd/backfill-usage/main.go [--days 90]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"

	internaldb "github.com/assembledhq/143/internal/db"
)

func main() {
	days := flag.Int("days", 90, "number of days to backfill")
	flag.Parse()

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		logger.Fatal().Msg("DATABASE_URL is required")
	}

	ctx := context.Background()
	pool, err := internaldb.NewPool(ctx, dbURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	rollupStore := internaldb.NewUsageRollupStore(pool)

	// Exclude the current in-progress hour — only backfill completed hours.
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.AddDate(0, 0, -*days)

	logger.Info().
		Time("start", start).
		Time("end", end).
		Int("days", *days).
		Msg("starting usage backfill")

	orgIDs, err := rollupStore.GetActiveOrgIDs(ctx, start, end)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to query orgs")
	}

	logger.Info().Int("orgs", len(orgIDs)).Msg("found orgs with usage data")

	totalHours := int(end.Sub(start).Hours())
	processed := 0

	for _, orgID := range orgIDs {
		logger.Info().Str("org_id", orgID.String()).Msg("backfilling org")

		if err := rollupStore.RollupRange(ctx, orgID, start, end); err != nil {
			logger.Error().Err(err).Str("org_id", orgID.String()).Msg("failed to backfill org")
			continue
		}

		processed++
		logger.Info().
			Str("org_id", orgID.String()).
			Int("hours", totalHours).
			Msg("org backfill complete")
	}

	fmt.Fprintf(os.Stderr, "Backfill complete: %d orgs, %d hours each\n", processed, totalHours)
}
