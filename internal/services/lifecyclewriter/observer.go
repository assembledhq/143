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
	ticker := time.NewTicker(observationInterval)
	return startSessionExecutor(store, buildSHA, logger, ticker.C, ticker.Stop, observationTimeout)
}

func startSessionExecutor(store Store, buildSHA string, logger zerolog.Logger, ticks <-chan time.Time, stopTicks func(), timeout time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	instanceID := uuid.New()
	logger = logger.With().Str("writer_instance_id", instanceID.String()).Logger()
	go func() {
		defer close(done)
		defer stopTicks()
		for {
			if ctx.Err() != nil {
				return
			}
			attemptCtx, attemptCancel := context.WithTimeout(ctx, timeout)
			err := store.Observe(attemptCtx, instanceID, models.LifecycleWriterRoleSessionExecutor, buildSHA)
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
