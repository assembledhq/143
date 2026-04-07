package agent

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
)

// ContainerUsageStore defines the persistence operations for container usage.
type ContainerUsageStore interface {
	RecordStart(ctx context.Context, event *models.ContainerUsageEvent) error
	RecordStop(ctx context.Context, eventID uuid.UUID, stoppedAt time.Time, exitReason string) error
}

// UsageTracker records container lifecycle events for billing observability.
// It writes to both the database (for billing queries) and Prometheus (for
// real-time dashboards and alerting).
type UsageTracker struct {
	store  ContainerUsageStore // nil = tracking disabled
	logger zerolog.Logger
}

// NewUsageTracker creates a UsageTracker. Pass nil store to disable DB tracking
// (Prometheus metrics will still be emitted).
func NewUsageTracker(store ContainerUsageStore, logger zerolog.Logger) *UsageTracker {
	return &UsageTracker{store: store, logger: logger}
}

// ContainerStarted records that a container was created and started.
// Returns an event ID that must be passed to ContainerStopped.
func (t *UsageTracker) ContainerStarted(ctx context.Context, orgID, sessionID uuid.UUID, sandbox *Sandbox, cfg SandboxConfig) uuid.UUID {
	eventID := uuid.New()
	orgIDStr := orgID.String()

	// Prometheus metrics (always emitted, even if store is nil).
	metrics.ContainerStartsTotal.WithLabelValues(orgIDStr, sandbox.Provider, cfg.Image).Inc()
	metrics.ContainersActive.WithLabelValues(orgIDStr).Inc()
	metrics.ContainerCPUAllocated.WithLabelValues(orgIDStr).Observe(cfg.CPULimit)
	metrics.ContainerMemoryAllocatedMB.WithLabelValues(orgIDStr).Observe(float64(cfg.MemoryLimitMB))

	// DB persistence.
	if t.store != nil {
		event := &models.ContainerUsageEvent{
			ID:            eventID,
			OrgID:         orgID,
			SessionID:     sessionID,
			ContainerID:   sandbox.ID,
			Provider:      sandbox.Provider,
			CPULimit:      cfg.CPULimit,
			MemoryLimitMB: cfg.MemoryLimitMB,
			Image:         cfg.Image,
			StartedAt:     time.Now(),
		}
		if err := t.store.RecordStart(ctx, event); err != nil {
			t.logger.Error().Err(err).
				Str("session_id", sessionID.String()).
				Str("container_id", sandbox.ID).
				Msg("failed to record container start for billing")
		}
	}

	return eventID
}

// ContainerStopped records that a container was destroyed. Must be called
// exactly once per ContainerStarted call.
func (t *UsageTracker) ContainerStopped(ctx context.Context, orgID uuid.UUID, eventID uuid.UUID, startedAt time.Time, exitReason string) {
	stoppedAt := time.Now()
	orgIDStr := orgID.String()
	durationSec := stoppedAt.Sub(startedAt).Seconds()
	durationMin := durationSec / 60.0

	// Prometheus metrics.
	metrics.ContainersActive.WithLabelValues(orgIDStr).Dec()
	metrics.ContainerStopsTotal.WithLabelValues(orgIDStr, exitReason).Inc()
	metrics.ContainerDurationSeconds.WithLabelValues(orgIDStr, exitReason).Observe(durationSec)
	metrics.ContainerMinutesTotal.WithLabelValues(orgIDStr).Add(durationMin)

	// DB persistence.
	if t.store != nil {
		if err := t.store.RecordStop(ctx, eventID, stoppedAt, exitReason); err != nil {
			t.logger.Error().Err(err).
				Str("event_id", eventID.String()).
				Msg("failed to record container stop for billing")
		}
	}
}
