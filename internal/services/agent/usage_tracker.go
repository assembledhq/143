package agent

import (
	"context"
	"sync"
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

// ActiveContainer is an in-memory record of a sandbox known to be running on
// this process. The runtime sampler uses Snapshot() to find them.
type ActiveContainer struct {
	EventID       uuid.UUID
	Sandbox       *Sandbox
	StartedAt     time.Time
	CPULimit      float64
	MemoryLimitMB int
	DiskLimitMB   int
}

// UsageTracker records container lifecycle events for billing observability.
// It writes to both the database (for billing queries) and OTel metrics (for
// real-time dashboards and alerting via any backend). It also keeps an
// in-memory registry of active containers so the runtime sampler can fetch
// stats without round-tripping the DB on every tick.
type UsageTracker struct {
	store   ContainerUsageStore     // nil = DB tracking disabled
	metrics *metrics.BillingMetrics // nil = metrics disabled
	logger  zerolog.Logger

	mu     sync.RWMutex
	active map[string]ActiveContainer // keyed by sandbox container ID
}

// NewUsageTracker creates a UsageTracker. Pass nil store to disable DB tracking,
// nil metrics to disable metric emission.
func NewUsageTracker(store ContainerUsageStore, m *metrics.BillingMetrics, logger zerolog.Logger) *UsageTracker {
	return &UsageTracker{
		store:   store,
		metrics: m,
		logger:  logger,
		active:  make(map[string]ActiveContainer),
	}
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
			DiskLimitMB:   cfg.DiskLimitGB * 1024,
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

	// In-memory registry for the runtime sampler. Stored even when the DB
	// path is disabled so self-hosters without persistence still get live
	// resource metrics. When DB persistence is enabled, register only after
	// RecordStart succeeds; otherwise the stop path receives uuid.Nil and
	// cannot remove this entry by event ID.
	t.mu.Lock()
	t.active[sandbox.ID] = ActiveContainer{
		EventID:       eventID,
		Sandbox:       sandbox,
		StartedAt:     startedAt,
		CPULimit:      cfg.CPULimit,
		MemoryLimitMB: cfg.MemoryLimitMB,
		DiskLimitMB:   cfg.DiskLimitGB * 1024,
	}
	t.mu.Unlock()

	return eventID
}

// ContainerStopped records that a container was destroyed. Must be called
// exactly once per ContainerStarted call. sessionID is included in failure
// logs so billing write failures can be traced back to the owning session
// in Grafana. containerID must match the sandbox.ID passed to ContainerStarted
// so the in-memory sampler registry can drop the entry in O(1) without
// iterating; pass "" if the caller no longer has it (the registry entry
// will be reaped by Forget on the next failed Stats call).
func (t *UsageTracker) ContainerStopped(ctx context.Context, orgID, sessionID uuid.UUID, eventID uuid.UUID, containerID string, startedAt time.Time, exitReason string) {
	stoppedAt := time.Now()
	orgIDStr := orgID.String()
	durationSec := stoppedAt.Sub(startedAt).Seconds()
	durationMin := durationSec / 60.0

	// OTel metrics.
	if t.metrics != nil {
		t.metrics.RecordStop(ctx, orgIDStr, exitReason, durationSec, durationMin)
	}

	// Remove from the in-memory registry. The map is keyed by container ID
	// so this is an O(1) delete that's unambiguous even if RecordStart
	// failure ever stops short-circuiting the registry insert.
	if containerID != "" {
		t.mu.Lock()
		delete(t.active, containerID)
		t.mu.Unlock()
	}

	// DB persistence. Skip if eventID is Nil (start recording failed).
	if t.store != nil && eventID != uuid.Nil {
		if err := t.store.RecordStop(ctx, eventID, stoppedAt, exitReason); err != nil {
			ev := t.logger.Error().Err(err).
				Str("org_id", orgIDStr).
				Str("event_id", eventID.String())
			if sessionID != uuid.Nil {
				ev = ev.Str("session_id", sessionID.String())
			}
			ev.Msg("failed to record container stop for billing")
		}
	}
}

// Snapshot returns a copy of the currently active containers known to this
// process. Used by the runtime sampler to walk live sandboxes; safe to call
// concurrently with ContainerStarted/ContainerStopped.
//
// The Sandbox pointer inside each entry is shared (not deep-copied); callers
// must treat it as read-only. Mutating it would race with code paths that
// stash the sandbox elsewhere (orchestrator, preview manager).
func (t *UsageTracker) Snapshot() []ActiveContainer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]ActiveContainer, 0, len(t.active))
	for _, ac := range t.active {
		out = append(out, ac)
	}
	return out
}

// Forget removes a container from the in-memory registry without recording
// a stop event. Intended for the runtime sampler to evict ghost entries
// when Stats() reports a container is gone (e.g. a process crash skipped
// the normal ContainerStopped path). This only affects sampling — the
// billing DB row for the orphan is reconciled by ReconcileOrphanedContainers
// at next startup.
func (t *UsageTracker) Forget(containerID string) {
	if containerID == "" {
		return
	}
	t.mu.Lock()
	delete(t.active, containerID)
	t.mu.Unlock()
}
