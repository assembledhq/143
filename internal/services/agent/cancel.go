package agent

import (
	"context"
	"errors"
	"fmt"
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
	cancel    CancellationSpec
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

// Register stores the sandbox, provider, and resolved cancellation behavior
// for a running session so CancelSession can interrupt the agent process.
func (r *CancelRegistry) Register(sessionID uuid.UUID, sandbox *Sandbox, provider SandboxProvider, ctxCancel context.CancelFunc, cancelSpec CancellationSpec) {
	if cancelSpec.Method == "" {
		cancelSpec = DefaultCancellationSpec
	}
	r.mu.Store(sessionID, &cancelEntry{
		sandbox:   sandbox,
		provider:  provider,
		ctxCancel: ctxCancel,
		cancel:    cancelSpec,
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

// CancelSession sends the agent's configured graceful interrupt to the coding
// agent running inside the sandbox, giving it a chance to save session state and exit gracefully. If the agent
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
	if err := r.sendInterrupt(killCtx, entry, entry.cancel); err != nil {
		if errors.Is(err, ErrUnsupportedInterruptMethod) && entry.cancel.Method != CancellationMethodCtrlC {
			r.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Str("requested_method", string(entry.cancel.Method)).
				Msg("provider does not support requested interrupt method, falling back to Ctrl+C")
			err = r.sendInterrupt(killCtx, entry, DefaultCancellationSpec)
		}
		if err != nil {
			r.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("failed to send graceful interrupt to agent process, falling back to context cancel")
			entry.ctxCancel()
			return
		}
	}

	r.logger.Info().
		Str("session_id", sessionID.String()).
		Msg("sent graceful interrupt to agent process in sandbox")

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
			Msg("agent did not exit after graceful interrupt, force-cancelling context")
		entry.ctxCancel()
	}
}

func (r *CancelRegistry) sendInterrupt(ctx context.Context, entry *cancelEntry, spec CancellationSpec) error {
	pidFile := InterruptPIDFilePath(entry.sandbox.HomeDir)
	req := InterruptRequest{
		Method:      spec.Method,
		PIDFilePath: pidFile,
		TTYFilePath: InterruptTTYFilePath(entry.sandbox.HomeDir),
	}
	if interruptor, ok := entry.provider.(SandboxInterruptor); ok {
		return interruptor.Interrupt(ctx, entry.sandbox, req)
	}
	if spec.Method != CancellationMethodCtrlC {
		return ErrUnsupportedInterruptMethod
	}
	cmd := BuildCtrlCInterruptCommand(pidFile)
	exitCode, err := entry.provider.Exec(ctx, entry.sandbox, cmd, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("interrupt command exited with code %d", exitCode)
	}
	return nil
}
