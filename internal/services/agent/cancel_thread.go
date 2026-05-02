package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// threadCancelEntry holds everything needed to gracefully cancel an
// in-flight thread turn without disturbing sibling tabs in the same
// container.
type threadCancelEntry struct {
	sandbox     *Sandbox
	provider    SandboxProvider
	ctxCancel   context.CancelFunc
	processName string // agent process to SIGINT, e.g. "claude"
	once        sync.Once
}

// ThreadCancelRegistry maps thread IDs to their cancellable agent process.
// It mirrors CancelRegistry but is keyed by thread instead of session so that
// cancelling one tab leaves siblings running. The orchestrator registers a
// thread when it starts an agent run with thread-scoped options and
// deregisters when the run unwinds.
type ThreadCancelRegistry struct {
	mu        sync.Map // thread ID (uuid.UUID) → *threadCancelEntry
	cancelled sync.Map // thread ID (uuid.UUID) → bool
	logger    zerolog.Logger
}

// NewThreadCancelRegistry creates a new ThreadCancelRegistry.
func NewThreadCancelRegistry(logger zerolog.Logger) *ThreadCancelRegistry {
	return &ThreadCancelRegistry{logger: logger}
}

// Register stores the sandbox handle and process name for a thread. Process
// name is the in-container binary that should receive SIGINT (claude, codex,
// gemini, amp, pi). When the value is empty we fall back to the same
// "any-supported-agent" pkill pattern used for session-level cancels.
func (r *ThreadCancelRegistry) Register(threadID uuid.UUID, sandbox *Sandbox, provider SandboxProvider, processName string, ctxCancel context.CancelFunc) {
	if threadID == uuid.Nil {
		return
	}
	r.mu.Store(threadID, &threadCancelEntry{
		sandbox:     sandbox,
		provider:    provider,
		processName: processName,
		ctxCancel:   ctxCancel,
	})
}

// Deregister removes the entry. Call from a defer at the end of the agent
// run path so a crashed run does not leave a stale handle that targets a
// recycled container.
func (r *ThreadCancelRegistry) Deregister(threadID uuid.UUID) {
	r.mu.Delete(threadID)
	r.cancelled.Delete(threadID)
}

// WasCancelled reports whether CancelThread was called for this thread.
func (r *ThreadCancelRegistry) WasCancelled(threadID uuid.UUID) bool {
	val, ok := r.cancelled.Load(threadID)
	if !ok {
		return false
	}
	return val.(bool)
}

// CancelThread sends SIGINT to the in-container agent process associated
// with this thread and starts a fallback timer that force-cancels the run
// context if SIGINT does not unwind the run within the grace window.
//
// Returns true when the request was accepted, false when no entry exists for
// the thread (e.g. the run already finished). Safe to call multiple times.
func (r *ThreadCancelRegistry) CancelThread(threadID uuid.UUID) bool {
	val, ok := r.mu.Load(threadID)
	if !ok {
		return false
	}
	entry := val.(*threadCancelEntry)
	r.cancelled.Store(threadID, true)
	entry.once.Do(func() {
		go r.doCancel(threadID, entry, 30*time.Second)
	})
	return true
}

func (r *ThreadCancelRegistry) doCancel(threadID uuid.UUID, entry *threadCancelEntry, graceWindow time.Duration) {
	killCtx, killCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer killCancel()

	// We match by exact process name so we don't kill a sibling tab's agent
	// process running the same binary. When processName is empty we fall
	// back to the multi-binary pkill pattern; a session with one running
	// thread degrades cleanly to today's behavior.
	cmd := "pkill -INT -x 'claude|codex|gemini|amp|pi' 2>/dev/null; true"
	if entry.processName != "" {
		cmd = "pkill -INT -x " + shellQuote(entry.processName) + " 2>/dev/null; true"
	}

	if _, err := entry.provider.Exec(killCtx, entry.sandbox, cmd, io.Discard, io.Discard); err != nil {
		r.logger.Warn().Err(err).
			Str("thread_id", threadID.String()).
			Msg("failed to send SIGINT to thread agent process, falling back to context cancel")
		entry.ctxCancel()
		return
	}
	r.logger.Info().
		Str("thread_id", threadID.String()).
		Msg("sent SIGINT to thread agent process in sandbox")

	if graceWindow <= 0 {
		graceWindow = 30 * time.Second
	}
	timer := time.NewTimer(graceWindow)
	defer timer.Stop()
	<-timer.C
	if _, stillRunning := r.mu.Load(threadID); stillRunning {
		r.logger.Warn().Str("thread_id", threadID.String()).Msg("thread did not exit after SIGINT, force-cancelling context")
		entry.ctxCancel()
	}
}

// shellQuote single-quotes a value for safe inclusion in a shell command.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// agentProcessName maps an AgentType to the in-sandbox binary name. Used by
// the thread-scoped cancel registry to target SIGINT at exactly one tab's
// agent process when several tabs share the container. Returns empty when
// the agent's binary name is not on the canonical list, in which case the
// registry falls back to the multi-binary pkill pattern.
func agentProcessName(agentType models.AgentType) string {
	switch agentType {
	case models.AgentTypeCodex:
		return "codex"
	case models.AgentTypeClaudeCode:
		return "claude"
	case models.AgentTypeGeminiCLI:
		return "gemini"
	case models.AgentTypeAmp:
		return "amp"
	case models.AgentTypePi:
		return "pi"
	default:
		return ""
	}
}
