package agent

import (
	"context"
	"io"
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

// cancelEntry holds everything needed to gracefully cancel a running session.
type cancelEntry struct {
	sandbox   *Sandbox
	provider  SandboxProvider
	ctxCancel context.CancelFunc
	once      sync.Once // guards against multiple cancel goroutines
	mu        sync.Mutex
	reason    StopReason
}

// CancelRegistry tracks cancellable running sessions. The Orchestrator
// registers entries when starting agent runs, and the API layer calls
// CancelSession to send SIGINT to the agent process inside the container.
type CancelRegistry struct {
	mu        sync.Map // session ID (uuid.UUID) → *cancelEntry
	cancelled sync.Map // session ID (uuid.UUID) → bool — tracks which sessions had SIGINT sent
	logger    zerolog.Logger
}

// NewCancelRegistry creates a new CancelRegistry.
func NewCancelRegistry(logger zerolog.Logger) *CancelRegistry {
	return &CancelRegistry{logger: logger}
}

// Register stores the sandbox and provider for a running session so that
// CancelSession can send SIGINT to the agent process.
func (r *CancelRegistry) Register(sessionID uuid.UUID, sandbox *Sandbox, provider SandboxProvider, ctxCancel context.CancelFunc) {
	r.mu.Store(sessionID, &cancelEntry{
		sandbox:   sandbox,
		provider:  provider,
		ctxCancel: ctxCancel,
	})
}

// Deregister removes the cancel entry for the given session.
func (r *CancelRegistry) Deregister(sessionID uuid.UUID) {
	r.mu.Delete(sessionID)
	r.cancelled.Delete(sessionID)
}

// WasCancelled returns true if CancelSession was called for this session.
// The orchestrator uses this to decide whether to treat the result as a
// cancellation rather than normal completion.
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

// CancelSession sends SIGINT to the coding agent running inside the sandbox,
// giving it a chance to save session state and exit gracefully. If the agent
// doesn't exit within a timeout, the context is cancelled as a fallback.
// Returns true if the session was found and the cancel was initiated.
// Safe to call multiple times — only the first call spawns the cancel goroutine.
func (r *CancelRegistry) CancelSession(sessionID uuid.UUID) bool {
	return r.RequestStop(sessionID, StopReasonUserCancel, 30*time.Second)
}

// RequestStop initiates a graceful stop for the running session. The first
// caller sends SIGINT and starts the grace-period timer; later callers can
// upgrade the recorded reason to user_cancel without spawning a second timer.
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

	// Use sync.Once to ensure only one cancel goroutine runs, even if the
	// user clicks cancel multiple times rapidly.
	entry.once.Do(func() {
		go r.doCancel(sessionID, entry, graceWindow)
	})

	return true
}

// doCancel performs the actual SIGINT + fallback timeout logic.
func (r *CancelRegistry) doCancel(sessionID uuid.UUID, entry *cancelEntry, graceWindow time.Duration) {
	killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer killCancel()

	// Send SIGINT to the agent process inside the container.
	// The agent CLIs (claude, codex, gemini) all handle SIGINT gracefully:
	// they save conversation state, flush output, and exit cleanly.
	//
	// We use -x for exact process name matching (not -f which matches the
	// full command line and can self-match the pkill command or match file
	// paths containing these words). Each sandbox runs exactly one agent,
	// so matching by binary name is precise.
	cmd := "pkill -INT -x 'claude|codex|gemini|amp|pi' 2>/dev/null; true"

	// The exit code from Exec is intentionally ignored. pkill returns 1
	// when no matching process is found (agent already exited), which is
	// fine — the trailing "; true" ensures the shell exits 0 regardless.
	if _, err := entry.provider.Exec(killCtx, entry.sandbox, cmd, io.Discard, io.Discard); err != nil {
		r.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to send SIGINT to agent process, falling back to context cancel")
		entry.ctxCancel()
		return
	}

	r.logger.Info().
		Str("session_id", sessionID.String()).
		Msg("sent SIGINT to agent process in sandbox")

	// Give the agent time to wind down gracefully. If ExecStream hasn't
	// returned after the timeout, force-cancel the context as a fallback.
	if graceWindow <= 0 {
		graceWindow = 30 * time.Second
	}
	timer := time.NewTimer(graceWindow)
	defer timer.Stop()

	<-timer.C
	if _, stillRunning := r.mu.Load(sessionID); stillRunning {
		r.logger.Warn().
			Str("session_id", sessionID.String()).
			Msg("agent did not exit after SIGINT, force-cancelling context")
		entry.ctxCancel()
	}
}
