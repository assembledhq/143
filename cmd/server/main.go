package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/assembledhq/143/internal/api"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/logging"
	"github.com/assembledhq/143/internal/worker"
)

func main() {
	cfg := config.Load()
	logger := logging.NewLogger(cfg.LogLevel)
	cfg.LogStatus(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	router := api.NewRouter(cfg, pool, logger)

	// Start worker if mode includes worker capability
	if cfg.Mode == "all" || cfg.Mode == "worker" {
		hostname, _ := os.Hostname()
		w := worker.New(pool, logger, hostname)
		stores := &worker.Stores{
			Issues:       db.NewIssueStore(pool),
			AgentRuns:    db.NewAgentRunStore(pool),
			Jobs:         db.NewJobStore(pool),
			Integrations: db.NewIntegrationStore(pool),
			Webhooks:     db.NewWebhookDeliveryStore(pool),
		}
		// Services will be nil until Phase 3 runtime dependencies are configured.
		// The existing handlers (ingest_webhook, prioritize, sync_sentry) don't need services.
		worker.RegisterHandlers(w, stores, nil, logger)
		go w.Start(ctx)
		logger.Info().Msg("worker started with registered handlers")
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info().Msg("shutting down server...")
		cancel() // stop worker
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("server shutdown failed")
		}
	}()

	logger.Info().Int("port", cfg.Port).Str("mode", cfg.Mode).Msg("starting server")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal().Err(err).Msg("server failed")
	}
}
