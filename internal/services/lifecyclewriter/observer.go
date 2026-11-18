// Package lifecyclewriter records passive process observations. These leases
// are not execution ownership, admission, readiness, or compatibility evidence.
package lifecyclewriter

import (
	"context"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	observationInterval = 30 * time.Second
	observationTimeout  = 5 * time.Second
)

// Store persists observations on the process's existing ordinary database pool.
type Store interface {
	Observe(context.Context, uuid.UUID, models.LifecycleWriterRole, string) error
}

// StartSessionExecutor starts an asynchronous, best-effort observer and returns
// its idempotent stop/join function. It deliberately does not inherit the run's
// signal context: terminal cleanup can keep writing after that context ends.
// Call stop after runtime cleanup and before closing the database pool. Neither
// stopping nor expiry certifies that a process or its transactions have drained.
func StartSessionExecutor(store Store, buildSHA string, logger zerolog.Logger) func() {
	return start(store, models.LifecycleWriterRoleSessionExecutor, buildSHA, logger)
}

// StartServer observes one configured server process, including one combined
// all-mode process rather than separate API/worker identities. The configured
// role does not attest that every capability initialized. Unsupported modes
// retain existing startup behavior: log and skip this optional observation.
// Stop/join only after coordinated shutdown and before closing the ordinary pool.
func StartServer(store Store, mode, buildSHA string, logger zerolog.Logger) func() {
	role := models.LifecycleWriterRole(mode)
	switch role {
	case models.LifecycleWriterRoleAPI, models.LifecycleWriterRoleWorker, models.LifecycleWriterRoleAll:
		return start(store, role, buildSHA, logger)
	default:
		logger.Warn().Str("mode", mode).Msg("passive lifecycle writer observation skipped for unsupported server mode")
		return func() {}
	}
}

func start(store Store, role models.LifecycleWriterRole, buildSHA string, logger zerolog.Logger) func() {
	ticker := time.NewTicker(observationInterval)
	return startObserver(store, role, buildSHA, logger, ticker.C, ticker.Stop, observationTimeout)
}

func startObserver(store Store, role models.LifecycleWriterRole, buildSHA string, logger zerolog.Logger, ticks <-chan time.Time, stopTicks func(), timeout time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	instanceID := uuid.New()
	logger = logger.With().Str("writer_instance_id", instanceID.String()).Str("writer_role", string(role)).Logger()
	go func() {
		defer close(done)
		defer stopTicks()
		for {
			if ctx.Err() != nil {
				return
			}
			attemptCtx, attemptCancel := context.WithTimeout(ctx, timeout)
			err := store.Observe(attemptCtx, instanceID, role, buildSHA)
			attemptCancel()
			if err != nil && ctx.Err() == nil {
				logger.Warn().Err(err).Msg("passive lifecycle writer observation failed; next interval will retry")
			}
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ticks:
				if !ok {
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
