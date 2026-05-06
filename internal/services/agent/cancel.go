package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// StopReason differentiates explicit user cancellation from policy-driven
// graceful stops such as soft-budget expiry or no-progress shutdown.
type StopReason string

const (
	StopReasonNone            StopReason = ""
	StopReasonUserCancel      StopReason = "user_cancel"
	StopReasonSoftBudget      StopReason = "soft_budget"
	StopReasonNoProgress      StopReason = "no_progress"
	StopReasonAbsoluteCeiling StopReason = "absolute_ceiling"
)

// cancelEntry holds the cancellation state for a single running session.
//
// The entry is created by the orchestrator at session start and carries only
// a context cancel function plus the adapter's preferred graceful-stop spec.
// The adapter's runtime helper later attaches a live InteractiveCommandHandle
// — until that point, RequestStop falls through to ctxCancel because there
// is nothing more specific to interrupt.
type cancelEntry struct {
	ctxCancel context.CancelFunc
	cancel    CancellationSpec

	mu     sync.Mutex
	handle InteractiveCommandHandle
	reason StopReason
	once   sync.Once
}

// CancelRegistry tracks cancellable running sessions. The Orchestrator
// registers an entry when it spawns an adapter run; the adapter's runtime
// helper attaches the live InteractiveCommandHandle once it has one. The API
// layer calls CancelSession to deliver the agent's configured graceful
// interrupt and, on grace expiry, force-close the handle.
type CancelRegistry struct {
	mu        sync.Map // session ID (uuid.UUID) → *cancelEntry
	cancelled sync.Map // session ID (uuid.UUID) → bool
	logger    zerolog.Logger
}

// NewCancelRegistry creates a new CancelRegistry.
func NewCancelRegistry(logger zerolog.Logger) *CancelRegistry {
	return &CancelRegistry{logger: logger}
}

// Register stores the per-session cancellation state. The handle is attached
// later via AttachHandle once the adapter starts the live command.
func (r *CancelRegistry) Register(sessionID uuid.UUID, ctxCancel context.CancelFunc, cancelSpec CancellationSpec) {
	if cancelSpec.Method == "" {
		cancelSpec = DefaultCancellationSpec
	}
	r.mu.Store(sessionID, &cancelEntry{
		ctxCancel: ctxCancel,
		cancel:    cancelSpec,
	})
}

// Deregister removes the cancel entry for the given session.
func (r *CancelRegistry) Deregister(sessionID uuid.UUID) {
	r.mu.Delete(sessionID)
	r.cancelled.Delete(sessionID)
}

// AttachHandle binds a live interactive command handle to the session entry.
// Adapters call this through the InteractiveHandleAttacher installed in the
// context. Replacing an existing handle is allowed (multi-turn sessions
// recreate the handle each turn).
func (r *CancelRegistry) AttachHandle(sessionID uuid.UUID, handle InteractiveCommandHandle) {
	val, ok := r.mu.Load(sessionID)
	if !ok {
		return
	}
	entry := val.(*cancelEntry)
	entry.mu.Lock()
	entry.handle = handle
	entry.mu.Unlock()
}

// DetachHandle clears the live handle without removing the cancel entry. Used
// by the runtime helper when a turn ends but the session lives on (e.g.
// follow-up turn).
func (r *CancelRegistry) DetachHandle(sessionID uuid.UUID) {
	val, ok := r.mu.Load(sessionID)
	if !ok {
		return
	}
	entry := val.(*cancelEntry)
	entry.mu.Lock()
	entry.handle = nil
	entry.mu.Unlock()
}

// HandleAttacher returns an InteractiveHandleAttacher bound to this session.
// The orchestrator installs it in the context before invoking adapter.Execute.
func (r *CancelRegistry) HandleAttacher(sessionID uuid.UUID) InteractiveHandleAttacher {
	return &registryHandleAttacher{registry: r, sessionID: sessionID}
}

type registryHandleAttacher struct {
	registry  *CancelRegistry
	sessionID uuid.UUID
}

func (a *registryHandleAttacher) Attach(handle InteractiveCommandHandle) {
	a.registry.AttachHandle(a.sessionID, handle)
}

func (a *registryHandleAttacher) Detach() {
	a.registry.DetachHandle(a.sessionID)
}

// WasCancelled returns true if CancelSession was called for this session.
func (r *CancelRegistry) WasCancelled(sessionID uuid.UUID) bool {
	return r.StopReason(sessionID) == StopReasonUserCancel
}

// StopReason returns the most recent graceful-stop reason recorded for the
// session. Empty means no stop was initiated.
func (r *CancelRegistry) StopReason(sessionID uuid.UUID) StopReason {
	val, ok := r.mu.Load(sessionID)
	if !ok {
		return StopReasonNone
	}
	entry := val.(*cancelEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.reason
}

// CancelSession sends the agent's configured graceful interrupt and falls
// back to context cancellation if the agent does not exit within a default
// 30 second grace window. Returns true when the session was found.
func (r *CancelRegistry) CancelSession(sessionID uuid.UUID) bool {
	return r.RequestStop(sessionID, StopReasonUserCancel, 30*time.Second)
}

// RequestStop initiates a graceful stop for the running session. The first
// caller delivers the interrupt and starts the grace timer; later callers
// can upgrade the recorded reason to user_cancel without spawning a second
// timer.
func (r *CancelRegistry) RequestStop(sessionID uuid.UUID, reason StopReason, graceWindow time.Duration) bool {
	val, ok := r.mu.Load(sessionID)
	if !ok {
		return false
	}
	entry := val.(*cancelEntry)
	if reason == StopReasonUserCancel {
		r.cancelled.Store(sessionID, true)
	}
	entry.mu.Lock()
	if entry.reason == StopReasonNone || reason == StopReasonUserCancel {
		entry.reason = reason
	}
	entry.mu.Unlock()

	entry.once.Do(func() {
		go r.doCancel(sessionID, entry, graceWindow)
	})
	return true
}

// doCancel performs the interrupt + grace + force-stop escalation.
//
// The escalation ladder is:
//
//  1. handle.Interrupt(spec) — preferred path; the handle delivers the
//     adapter-specific graceful stop through whichever transport it owns.
//  2. handle.Interrupt(default) — fallback if the requested method is
//     explicitly unsupported by this transport.
//  3. ctxCancel() — last-resort transport-level cancellation when no handle
//     is attached or when interrupt delivery fails outright.
//  4. After graceWindow, if the entry is still registered, ctxCancel() and
//     handle.Kill(...) force-close the underlying transport.
func (r *CancelRegistry) doCancel(sessionID uuid.UUID, entry *cancelEntry, graceWindow time.Duration) {
	interruptCtx, interruptCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer interruptCancel()

	entry.mu.Lock()
	handle := entry.handle
	spec := entry.cancel
	entry.mu.Unlock()

	// We deliberately keep using `handle` past Detach. The runInteractiveCommand
	// caller defers Close() then Detach(), so a Detach racing with this method
	// implies Close() already ran or is about to. Every handle method below is
	// idempotent and Close()-safe (sync.Once-guarded transport teardown,
	// Interrupt returns ErrUnsupportedInterruptMethod or a write error on a
	// closed conn, Kill is best-effort), so the worst case is one logged
	// "failed to deliver graceful interrupt" line — never a panic or a stuck
	// goroutine.
	if handle == nil {
		r.logger.Info().
			Str("session_id", sessionID.String()).
			Msg("no live handle attached, falling back to context cancel")
		entry.ctxCancel()
		return
	}

	if err := handle.Interrupt(interruptCtx, spec); err != nil {
		if errors.Is(err, ErrUnsupportedInterruptMethod) && spec.Method != CancellationMethodCtrlC {
			r.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Str("requested_method", string(spec.Method)).
				Msg("handle does not support requested interrupt method, falling back to Ctrl+C")
			err = handle.Interrupt(interruptCtx, DefaultCancellationSpec)
		}
		if err != nil {
			r.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("failed to deliver graceful interrupt, falling back to context cancel")
			entry.ctxCancel()
			return
		}
	}

	r.logger.Info().
		Str("session_id", sessionID.String()).
		Msg("delivered graceful interrupt to running agent")

	if graceWindow <= 0 {
		graceWindow = 30 * time.Second
	}
	timer := time.NewTimer(graceWindow)
	defer timer.Stop()

	<-timer.C
	if _, stillRunning := r.mu.Load(sessionID); !stillRunning {
		return
	}
	r.logger.Warn().
		Str("session_id", sessionID.String()).
		Msg("agent did not exit after graceful interrupt, force-stopping handle and cancelling context")
	killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer killCancel()
	if err := handle.Kill(killCtx); err != nil {
		r.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to force-stop interactive handle")
	}
	entry.ctxCancel()
}
