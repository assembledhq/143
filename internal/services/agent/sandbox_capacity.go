package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ErrSandboxCapacity is returned when a worker node cannot safely start one
// more sandbox container right now. Callers should treat it as transient.
var ErrSandboxCapacity = errors.New("sandbox capacity reached")

const (
	defaultSandboxCapacityCountTimeout   = 2 * time.Second
	defaultSandboxPressureCleanupTimeout = 5 * time.Second
)

// LiveSandboxCounter counts live sandbox containers on the local machine.
type LiveSandboxCounter interface {
	CountLiveSandboxes(ctx context.Context) (int, error)
}

// SandboxPressureCleaner performs a best-effort local cleanup pass when a
// worker host is full before admission gives up.
type SandboxPressureCleaner interface {
	ReapForCapacity(ctx context.Context, now time.Time) error
}

// SandboxCapacityGateConfig configures local sandbox admission control.
type SandboxCapacityGateConfig struct {
	Counter                LiveSandboxCounter
	PressureCleaner        SandboxPressureCleaner
	MaxActive              int
	CountTimeout           time.Duration
	PressureCleanupTimeout time.Duration
	NodeID                 string
	Logger                 zerolog.Logger
}

// SandboxCapacityRequest carries tracing fields for an admission attempt.
type SandboxCapacityRequest struct {
	Purpose   string
	SessionID string
	OrgID     string
}

// SandboxCapacitySnapshot is a best-effort view used in worker heartbeats.
type SandboxCapacitySnapshot struct {
	Live       int
	Reserved   int
	MaxActive  int
	CountError string
}

// SandboxCapacityGate gates new local sandbox creation against the current
// live Docker count plus in-flight reservations.
type SandboxCapacityGate struct {
	counter   LiveSandboxCounter
	cleaner   SandboxPressureCleaner
	maxActive int
	countTTL  time.Duration
	cleanTTL  time.Duration
	nodeID    string
	logger    zerolog.Logger

	mu       sync.Mutex
	reserved int
	// releaseGeneration invalidates live counts taken before a reservation release completes.
	releaseGeneration uint64
}

// NewSandboxCapacityGate constructs a local sandbox admission gate.
func NewSandboxCapacityGate(cfg SandboxCapacityGateConfig) *SandboxCapacityGate {
	countTTL := cfg.CountTimeout
	if countTTL <= 0 {
		countTTL = defaultSandboxCapacityCountTimeout
	}
	cleanTTL := cfg.PressureCleanupTimeout
	if cleanTTL <= 0 {
		cleanTTL = defaultSandboxPressureCleanupTimeout
	}
	return &SandboxCapacityGate{
		counter:   cfg.Counter,
		cleaner:   cfg.PressureCleaner,
		maxActive: cfg.MaxActive,
		countTTL:  countTTL,
		cleanTTL:  cleanTTL,
		nodeID:    cfg.NodeID,
		logger:    cfg.Logger,
	}
}

// MaxActive returns the configured local live sandbox cap.
func (g *SandboxCapacityGate) MaxActive() int {
	if g == nil {
		return 0
	}
	return g.maxActive
}

// SetPressureCleaner installs or replaces the cleanup hook used when a worker
// host is full. It is safe to call during startup before workers begin polling.
func (g *SandboxCapacityGate) SetPressureCleaner(cleaner SandboxPressureCleaner) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cleaner = cleaner
}

// ReservedCount returns the current in-flight reservation count.
func (g *SandboxCapacityGate) ReservedCount() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reserved
}

// Acquire reserves one sandbox slot if live+reserved is below MaxActive.
func (g *SandboxCapacityGate) Acquire(ctx context.Context, req SandboxCapacityRequest) (*SandboxCapacityReservation, error) {
	if g == nil {
		return nil, nil
	}
	if g.maxActive <= 0 {
		err := fmt.Errorf("%w: max_active_sandboxes is not configured", ErrSandboxCapacity)
		g.logCapacity(req, 0, g.ReservedCount()).Err(err).Msg("sandbox capacity unavailable")
		return nil, err
	}
	if g.counter == nil {
		err := fmt.Errorf("%w: live sandbox counter is not configured", ErrSandboxCapacity)
		g.logCapacity(req, 0, g.ReservedCount()).Err(err).Msg("sandbox capacity unavailable")
		return nil, err
	}

	cleanedForPressure := false
	for {
		g.mu.Lock()
		releaseGeneration := g.releaseGeneration
		g.mu.Unlock()

		live, err := g.countLiveSandboxes(ctx)
		if err != nil {
			wrapped := fmt.Errorf("%w: count live sandboxes: %w", ErrSandboxCapacity, err)
			g.logCapacity(req, 0, g.ReservedCount()).Err(err).Msg("failed to count live sandboxes; rejecting sandbox admission")
			return nil, wrapped
		}

		g.mu.Lock()
		if releaseGeneration != g.releaseGeneration {
			g.mu.Unlock()
			continue
		}

		total := live + g.reserved
		if total >= g.maxActive {
			reserved := g.reserved
			cleaner := g.cleaner
			g.mu.Unlock()
			if !cleanedForPressure && cleaner != nil {
				cleanedForPressure = true
				cleanCtx, cancel := context.WithTimeout(ctx, g.cleanTTL)
				cleanErr := cleaner.ReapForCapacity(cleanCtx, time.Now())
				cancel()
				if cleanErr != nil {
					g.logCapacity(req, live, reserved).Err(cleanErr).Msg("sandbox pressure cleanup failed before admission retry")
				} else {
					g.logCapacity(req, live, reserved).Msg("sandbox pressure cleanup completed before admission retry")
				}
				continue
			}
			err := fmt.Errorf("%w: %d/%d sandboxes active or reserved", ErrSandboxCapacity, total, g.maxActive)
			g.logCapacity(req, live, reserved).Msg("sandbox capacity reached; rejecting sandbox admission")
			return nil, err
		}
		g.reserved++
		reserved := g.reserved
		g.mu.Unlock()

		g.logCapacity(req, live, reserved).Msg("sandbox capacity reserved")
		return &SandboxCapacityReservation{gate: g}, nil
	}
}

// Snapshot returns a best-effort point-in-time capacity view for metadata.
func (g *SandboxCapacityGate) Snapshot(ctx context.Context) SandboxCapacitySnapshot {
	if g == nil {
		return SandboxCapacitySnapshot{}
	}

	g.mu.Lock()
	reserved := g.reserved
	g.mu.Unlock()

	snapshot := SandboxCapacitySnapshot{
		Reserved:  reserved,
		MaxActive: g.maxActive,
	}
	if g.counter == nil {
		snapshot.CountError = "live sandbox counter is not configured"
		return snapshot
	}
	live, err := g.countLiveSandboxes(ctx)
	if err != nil {
		snapshot.CountError = err.Error()
		return snapshot
	}
	snapshot.Live = live
	return snapshot
}

func (g *SandboxCapacityGate) countLiveSandboxes(ctx context.Context) (int, error) {
	countCtx, cancel := context.WithTimeout(ctx, g.countTTL)
	defer cancel()
	return g.counter.CountLiveSandboxes(countCtx)
}

func (g *SandboxCapacityGate) logCapacity(req SandboxCapacityRequest, live, reserved int) *zerolog.Event {
	return g.logger.Info().
		Str("node_id", g.nodeID).
		Int("live_sandboxes", live).
		Int("reserved_sandboxes", reserved).
		Int("max_active_sandboxes", g.maxActive).
		Str("purpose", req.Purpose).
		Str("session_id", req.SessionID).
		Str("org_id", req.OrgID)
}

// HasSpeculativeHeadroom returns true when the worker has at least minFree
// sandbox slots free after accounting for live containers and in-flight
// reservations. Speculative (prewarm) work should call this with minFree=2
// before attempting Acquire so that the last slot stays available for
// user-initiated work.
func (g *SandboxCapacityGate) HasSpeculativeHeadroom(ctx context.Context, minFree int) bool {
	if g == nil || g.maxActive <= 0 {
		return false
	}
	snapshot := g.Snapshot(ctx)
	if snapshot.CountError != "" {
		return false
	}
	return (snapshot.MaxActive - (snapshot.Live + snapshot.Reserved)) >= minFree
}

// SandboxCapacityReservation releases a previously acquired slot.
type SandboxCapacityReservation struct {
	gate *SandboxCapacityGate
	once sync.Once
}

// Release returns the reservation to the gate. It is safe to call repeatedly.
func (r *SandboxCapacityReservation) Release() {
	if r == nil || r.gate == nil {
		return
	}
	r.once.Do(func() {
		r.gate.mu.Lock()
		defer r.gate.mu.Unlock()
		if r.gate.reserved > 0 {
			r.gate.reserved--
		}
		r.gate.releaseGeneration++
	})
}
