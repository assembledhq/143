// lifecycle-census emits a bounded passive observation, not a rollout decision.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/assembledhq/143/internal/services/lifecyclecensus"
	"github.com/rs/zerolog"
)

type collectFunc func(context.Context, string) (lifecyclecensus.Report, error)

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, lifecyclecensus.Collect)
}

// run keeps configuration, diagnostics and report output separate. In
// particular, database errors can contain credentials and must never be logged.
// A failed output write may already have emitted part of the report.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, collect collectFunc) int {
	logger := zerolog.New(stderr)
	if len(args) != 0 {
		logger.Error().Str("code", "invalid_arguments").Msg("lifecycle-census does not accept arguments")
		return 2
	}

	databaseURL := getenv("DATABASE_URL")
	if strings.TrimSpace(databaseURL) == "" {
		logger.Error().Str("code", "invalid_configuration").Msg("DATABASE_URL must be explicitly set")
		return 2
	}
	if err := ctx.Err(); err != nil {
		logCollectionError(logger, err)
		return 1
	}

	report, err := collect(ctx, databaseURL)
	if err != nil {
		logCollectionError(logger, err)
		return 1
	}
	if err := ctx.Err(); err != nil {
		logCollectionError(logger, err)
		return 1
	}

	// Marshal before writing so encoding failures cannot produce an apparent
	// success report. Struct field order and the collector's sorted groups make
	// this deterministic for identical observations.
	payload, err := json.Marshal(report)
	if err != nil {
		logger.Error().Str("code", "encoding_failed").Msg("could not encode census report")
		return 1
	}
	payload = append(payload, '\n')
	written, err := stdout.Write(payload)
	if err != nil || written != len(payload) {
		logger.Error().Str("code", "output_failed").Msg("could not write census report")
		return 1
	}
	// Success means collection and report delivery succeeded. It says nothing
	// about writer completeness, compatibility, drain or rollout readiness.
	return 0
}

func logCollectionError(logger zerolog.Logger, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		logger.Error().Str("code", "collection_canceled").Msg("census collection was canceled")
	case errors.Is(err, context.DeadlineExceeded):
		logger.Error().Str("code", "collection_timed_out").Msg("census collection exceeded its time budget")
	default:
		logger.Error().Str("code", "collection_failed").Msg("could not collect census observations")
	}
}
