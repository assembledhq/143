package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	defaultSandboxGCGracePeriod         = 30 * time.Minute
	defaultSandboxGCPressureGracePeriod = 2 * time.Minute
	defaultSandboxGCPressureMaxDestroy  = 2
	defaultSandboxGCHardMaxAge          = 24 * time.Hour
)

// ManagedSandboxContainer is the provider-neutral subset of Docker container
// metadata the worker-local GC needs to reconcile host state with DB state.
type ManagedSandboxContainer struct {
	ID        string
	SessionID string
	OrgID     string
	Purpose   string
	CreatedAt time.Time
}

// SandboxGCProvider is implemented by providers that can enumerate their
// managed sandbox containers on the local host.
type SandboxGCProvider interface {
	ListManagedSandboxes(ctx context.Context) ([]ManagedSandboxContainer, error)
	Destroy(ctx context.Context, sb *Sandbox) error
}

// SandboxReferenceStore is the DB surface the host-local GC needs. The
// reference list is intentionally cross-org: Docker containers are host-local
// resources and the GC must decide whether any session row still owns a
// container before removing it.
type SandboxReferenceStore interface {
	ListReferencedContainerIDs(ctx context.Context) ([]string, error)
	FinalizeContainerDestroy(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (cleared bool, err error)
}

// SandboxUsageCloser lets the GC close billing rows for containers it destroys
// outside the ordinary orchestrator defer path.
type SandboxUsageCloser interface {
	CloseOpenByContainerID(ctx context.Context, containerID string, stoppedAt time.Time, exitReason string) (int64, error)
}

type SandboxGCConfig struct {
	Interval                time.Duration
	UnreferencedGracePeriod time.Duration
	PressureGracePeriod     time.Duration
	PressureMaxDestroy      int
	HardMaxAge              time.Duration
}

type SandboxGC struct {
	provider      SandboxGCProvider
	store         SandboxReferenceStore
	usage         SandboxUsageCloser
	cfg           SandboxGCConfig
	startupCutoff time.Time
	logger        zerolog.Logger
}

func NewSandboxGC(provider SandboxGCProvider, store SandboxReferenceStore, usage SandboxUsageCloser, cfg SandboxGCConfig, logger zerolog.Logger) *SandboxGC {
	if cfg.UnreferencedGracePeriod <= 0 {
		cfg.UnreferencedGracePeriod = defaultSandboxGCGracePeriod
	}
	if cfg.PressureGracePeriod <= 0 {
		cfg.PressureGracePeriod = defaultSandboxGCPressureGracePeriod
	}
	if cfg.PressureMaxDestroy <= 0 {
		cfg.PressureMaxDestroy = defaultSandboxGCPressureMaxDestroy
	}
	if cfg.HardMaxAge <= 0 {
		cfg.HardMaxAge = defaultSandboxGCHardMaxAge
	}
	return &SandboxGC{
		provider:      provider,
		store:         store,
		usage:         usage,
		cfg:           cfg,
		startupCutoff: time.Now().UTC(),
		logger:        logger,
	}
}

func (g *SandboxGC) Run(ctx context.Context) {
	if g == nil || g.provider == nil || g.store == nil || g.cfg.Interval <= 0 {
		return
	}

	g.logger.Info().
		Dur("interval", g.cfg.Interval).
		Dur("unreferenced_grace_period", g.cfg.UnreferencedGracePeriod).
		Dur("hard_max_age", g.cfg.HardMaxAge).
		Msg("sandbox GC started")

	g.reapAndLog(ctx, time.Now())
	ticker := time.NewTicker(g.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			g.logger.Info().Msg("sandbox GC stopped")
			return
		case now := <-ticker.C:
			g.reapAndLog(ctx, now)
		}
	}
}

func (g *SandboxGC) reapAndLog(ctx context.Context, now time.Time) {
	if err := g.ReapOnce(ctx, now); err != nil {
		g.logger.Warn().Err(err).Msg("sandbox GC pass failed")
	}
}

func (g *SandboxGC) ReapOnce(ctx context.Context, now time.Time) error {
	return g.reapOnce(ctx, now, g.cfg.UnreferencedGracePeriod, time.Time{}, "sandbox_gc_unreferenced", 0)
}

// ReapStartup performs a Docker-first cleanup pass before workers accept new
// jobs. DB-unreferenced containers that predate this process cannot belong to
// an in-flight turn in this process, so they do not need the normal grace.
func (g *SandboxGC) ReapStartup(ctx context.Context, now time.Time) error {
	return g.reapOnce(ctx, now, 0, g.startupCutoff, "sandbox_gc_startup_unreferenced", 0)
}

// ReapForCapacity runs a pressure cleanup pass when local sandbox admission is
// full. It uses a short grace so leaked Docker-only containers do not outlive
// the worker job retry window.
func (g *SandboxGC) ReapForCapacity(ctx context.Context, now time.Time) error {
	return g.reapOnce(ctx, now, g.cfg.PressureGracePeriod, time.Time{}, "sandbox_gc_capacity_unreferenced", g.cfg.PressureMaxDestroy)
}

func (g *SandboxGC) reapOnce(ctx context.Context, now time.Time, unreferencedGrace time.Duration, unreferencedCreatedBefore time.Time, unreferencedReason string, maxDestroyAttempts int) error {
	if g == nil || g.provider == nil || g.store == nil {
		return nil
	}

	referenced, err := g.store.ListReferencedContainerIDs(ctx)
	if err != nil {
		return fmt.Errorf("list referenced container ids: %w", err)
	}
	refSet := make(map[string]struct{}, len(referenced))
	for _, id := range referenced {
		if id != "" {
			refSet[id] = struct{}{}
		}
	}

	containers, err := g.provider.ListManagedSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("list managed sandbox containers: %w", err)
	}

	var destroyedUnreferenced, destroyedExpired, skippedReferenced, destroyAttempts int
	for _, c := range containers {
		if c.ID == "" {
			continue
		}
		age := sandboxContainerAge(now, c.CreatedAt)
		if _, ok := refSet[c.ID]; !ok {
			if !unreferencedCreatedBefore.IsZero() && c.CreatedAt.After(unreferencedCreatedBefore) {
				continue
			}
			if age < unreferencedGrace {
				continue
			}
			if maxDestroyAttempts > 0 && destroyAttempts >= maxDestroyAttempts {
				break
			}
			destroyAttempts++
			if g.destroyContainer(ctx, c, now, unreferencedReason) {
				destroyedUnreferenced++
			}
			continue
		}

		if age < g.cfg.HardMaxAge {
			skippedReferenced++
			continue
		}
		if maxDestroyAttempts > 0 && destroyAttempts >= maxDestroyAttempts {
			break
		}
		orgID, sessionID, parseErr := parseManagedSandboxIDs(c)
		if parseErr != nil {
			g.logger.Warn().Err(parseErr).
				Str("container_id", c.ID).
				Msg("sandbox GC: hard-expired referenced container is missing valid ownership labels; leaving it alive")
			continue
		}
		cleared, err := g.store.FinalizeContainerDestroy(ctx, orgID, sessionID, c.ID)
		if err != nil {
			g.logger.Warn().Err(err).
				Str("container_id", c.ID).
				Str("session_id", c.SessionID).
				Msg("sandbox GC: failed to finalize hard-expired container; leaving it alive")
			continue
		}
		if !cleared {
			g.logger.Info().
				Str("container_id", c.ID).
				Str("session_id", c.SessionID).
				Msg("sandbox GC: hard-expired container is still held; leaving it alive")
			continue
		}
		destroyAttempts++
		if g.destroyContainer(ctx, c, now, "sandbox_gc_expired") {
			destroyedExpired++
		}
	}

	if destroyedUnreferenced > 0 || destroyedExpired > 0 {
		g.logger.Info().
			Int("destroyed_unreferenced", destroyedUnreferenced).
			Int("destroyed_expired", destroyedExpired).
			Int("skipped_referenced", skippedReferenced).
			Msg("sandbox GC pass complete")
	}
	return nil
}

func sandboxContainerAge(now time.Time, createdAt time.Time) time.Duration {
	if createdAt.IsZero() {
		return 0
	}
	if now.Before(createdAt) {
		return 0
	}
	return now.Sub(createdAt)
}

func parseManagedSandboxIDs(c ManagedSandboxContainer) (uuid.UUID, uuid.UUID, error) {
	orgID, err := uuid.Parse(c.OrgID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("parse org id: %w", err)
	}
	sessionID, err := uuid.Parse(c.SessionID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("parse session id: %w", err)
	}
	return orgID, sessionID, nil
}

func (g *SandboxGC) destroyContainer(ctx context.Context, c ManagedSandboxContainer, now time.Time, reason string) bool {
	if err := g.provider.Destroy(ctx, &Sandbox{
		ID:        c.ID,
		Provider:  "docker",
		SessionID: c.SessionID,
		OrgID:     c.OrgID,
		Purpose:   c.Purpose,
	}); err != nil {
		g.logger.Warn().Err(err).
			Str("container_id", c.ID).
			Str("session_id", c.SessionID).
			Str("reason", reason).
			Msg("sandbox GC: failed to destroy container")
		return false
	}
	if g.usage != nil {
		if _, err := g.usage.CloseOpenByContainerID(ctx, c.ID, now, reason); err != nil {
			g.logger.Warn().Err(err).
				Str("container_id", c.ID).
				Str("session_id", c.SessionID).
				Str("reason", reason).
				Msg("sandbox GC: failed to close open usage rows after destroy")
		}
	}
	return true
}
