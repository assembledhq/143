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
// It writes to both the database (for billing queries) and OTel metrics (for
// real-time dashboards and alerting via any backend).
type UsageTracker struct {
	store   ContainerUsageStore    // nil = DB tracking disabled
	metrics *metrics.BillingMetrics // nil = metrics disabled
	logger  zerolog.Logger
}

// NewUsageTracker creates a UsageTracker. Pass nil store to disable DB tracking,
// nil metrics to disable metric emission.
func NewUsageTracker(store ContainerUsageStore, m *metrics.BillingMetrics, logger zerolog.Logger) *UsageTracker {
	return &UsageTracker{store: store, metrics: m, logger: logger}
}

// ContainerStarted records that a container was created and started.
// The caller must pass the startedAt timestamp so the same value is used for
// both the DB record and OTel duration computation, avoiding time skew.
// Returns an event ID that must be passed to ContainerStopped.
func (t *UsageTracker) ContainerStarted(ctx context.Context, orgID, sessionID uuid.UUID, sandbox *Sandbox, cfg SandboxConfig, startedAt time.Time) uuid.UUID {
	eventID := uuid.New()
	orgIDStr := orgID.String()

	// OTel metrics.
	if t.metrics != nil {
		t.metrics.RecordStart(ctx, orgIDStr, sandbox.Provider, cfg.CPULimit, cfg.MemoryLimitMB)
	}

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
			StartedAt:     startedAt,
		}
		if err := t.store.RecordStart(ctx, event); err != nil {
			t.logger.Error().Err(err).
				Str("session_id", sessionID.String()).
				Str("container_id", sandbox.ID).
				Msg("failed to record container start for billing")
			// Return uuid.Nil so ContainerStopped knows to skip the DB write.
			return uuid.Nil
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

	// OTel metrics.
	if t.metrics != nil {
		t.metrics.RecordStop(ctx, orgIDStr, exitReason, durationSec, durationMin)
	}

	// DB persistence. Skip if eventID is Nil (start recording failed).
	if t.store != nil && eventID != uuid.Nil {
		if err := t.store.RecordStop(ctx, eventID, stoppedAt, exitReason); err != nil {
			t.logger.Error().Err(err).
				Str("event_id", eventID.String()).
				Msg("failed to record container stop for billing")
		}
	}
}
