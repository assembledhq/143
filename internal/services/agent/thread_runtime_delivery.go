package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

const threadInboxDeliveryBatchSize = 50
const threadInboxHandleWriteTimeout = 5 * time.Second
const threadRuntimeStateUpdateTimeout = 5 * time.Second
const threadRuntimeLeaseDuration = 60 * time.Second
const threadInboxOwnerPollInterval = 2 * time.Second

type ThreadRuntimeStore interface {
	CreateStarting(ctx context.Context, orgID uuid.UUID, params db.CreateThreadRuntimeParams) (models.ThreadRuntime, error)
	GetActiveByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadRuntime, error)
	MarkLiveWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, runtimeHandleID string, leaseDuration time.Duration) (bool, error)
	HeartbeatWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, leaseDuration time.Duration) (bool, error)
	AdvanceDeliveryWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, deliveredSequence, ackedSequence int64) (bool, error)
	CommitInboxDeliveryWithLease(ctx context.Context, orgID, runtimeID, leaseToken, threadID uuid.UUID, ownerNodeID string, deliveredSequence, ackedSequence int64) (bool, error)
	MarkTerminalWithLease(ctx context.Context, orgID, runtimeID, leaseToken uuid.UUID, status models.ThreadRuntimeStatus, stopReason, lastError string) (bool, error)
}

type ThreadInboxStore interface {
	AppendForMessage(ctx context.Context, orgID uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error)
	ClaimDeliverableAfter(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, ownerNodeID string, afterSequence int64, limit int) ([]models.ThreadInboxEntry, error)
	MarkDeliveredForEntry(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, ownerNodeID string, entryID uuid.UUID, sequenceNo int64) (int64, error)
	MarkAckedForSeedMessages(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, messageIDs []int64) (int64, error)
	MarkDeliveringForMessages(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, ownerNodeID string, messageIDs []int64) (int64, error)
	MarkDeadLetter(ctx context.Context, orgID, threadID, entryID uuid.UUID, reason string) (models.ThreadInboxEntry, error)
	MarkDeliveryFailed(ctx context.Context, orgID, threadID, runtimeID, entryID uuid.UUID, reason string, maxAttempts int) (models.ThreadInboxEntry, error)
	CountPendingByThread(ctx context.Context, orgID, threadID uuid.UUID) (int, error)
	IsMessageAcked(ctx context.Context, orgID, threadID uuid.UUID, messageID int64) (bool, error)
}

type SessionSandboxHolderStore interface {
	CreateActive(ctx context.Context, orgID uuid.UUID, params db.CreateSessionSandboxHolderParams) (models.SessionSandboxHolder, error)
	ReleaseWithLease(ctx context.Context, orgID, sessionID uuid.UUID, kind models.SessionSandboxHolderKind, holderID, leaseToken uuid.UUID) (bool, error)
	HeartbeatWithLease(ctx context.Context, orgID, sessionID uuid.UUID, kind models.SessionSandboxHolderKind, holderID, leaseToken uuid.UUID, leaseDuration time.Duration) (bool, error)
	CountActiveThreadRuntimesBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error)
	CountActiveThreadRuntimesBySessionExcluding(ctx context.Context, orgID, sessionID, excludedHolderID uuid.UUID) (int, error)
}

type sessionStatusUpdater interface {
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error
	UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state models.SandboxState) error
	// MarkRunningWithSandboxState writes status=running and sandbox_state in a
	// single SQL statement so the row cannot end up half-updated when one
	// write succeeds and the other fails.
	MarkRunningWithSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, sandboxState models.SandboxState) error
}

type ThreadRuntimeOwnedElsewhereError struct {
	RuntimeID   uuid.UUID
	ThreadID    uuid.UUID
	OwnerNodeID string
}

func (e *ThreadRuntimeOwnedElsewhereError) Error() string {
	return fmt.Sprintf("thread runtime %s for thread %s is owned by worker %q", e.RuntimeID, e.ThreadID, e.OwnerNodeID)
}

var ErrThreadRuntimeLeaseLost = errors.New("thread runtime lease lost")
var ErrThreadRuntimeLiveInputUnsupported = errors.New("thread runtime live input unsupported")

// ThreadRuntimeInputFormatter is an optional adapter extension. Every adapter
// participates in live inbox delivery; implementing this interface only lets a
// specific adapter override the default content-and-newline encoding (see
// formatThreadInboxRuntimeInput). There is no opt-out.
type ThreadRuntimeInputFormatter interface {
	FormatThreadRuntimeInput(entry models.ThreadInboxEntry) ([]byte, error)
}

var ErrThreadRuntimeAlreadyActive = errors.New("thread runtime already active")

type threadRuntimeControl struct {
	runtime                   models.ThreadRuntime
	leaseDuration             time.Duration
	runtimes                  ThreadRuntimeStore
	holders                   SessionSandboxHolderStore
	inbox                     ThreadInboxStore
	seedMessageIDs            []int64
	heartbeatFailureStartedAt time.Time
	logger                    zerolog.Logger
}

func (o *Orchestrator) startThreadRuntimeControl(ctx context.Context, session *models.Session, threadID uuid.UUID, sandbox *Sandbox, seedMessageIDs []int64, log zerolog.Logger) (*threadRuntimeControl, error) {
	if o == nil || o.threadRuntimes == nil || session == nil || threadID == uuid.Nil || sandbox == nil {
		return nil, nil
	}
	leaseToken := uuid.New()
	model := ""
	if session.ModelOverride != nil {
		model = *session.ModelOverride
	}
	runtime, err := o.threadRuntimes.CreateStarting(ctx, session.OrgID, db.CreateThreadRuntimeParams{
		SessionID:                  session.ID,
		ThreadID:                   threadID,
		SandboxID:                  session.ID,
		ContainerID:                sandbox.ID,
		AgentType:                  session.AgentType,
		Model:                      model,
		OwnerNodeID:                o.nodeID,
		LeaseToken:                 leaseToken,
		LastDeliveredSequence:      0,
		LastAckedSequence:          0,
		BaseWorkspaceGeneration:    session.WorkspaceGeneration,
		CurrentWorkspaceGeneration: session.WorkspaceGeneration,
		LeaseDuration:              threadRuntimeLeaseDuration,
	})
	if err != nil {
		if errors.Is(err, db.ErrActiveThreadRuntimeExists) {
			return nil, fmt.Errorf("%w: %w", ErrThreadRuntimeAlreadyActive, err)
		}
		return nil, fmt.Errorf("create thread runtime: %w", err)
	}
	if runtime.LeaseToken == uuid.Nil {
		runtime.LeaseToken = leaseToken
	}
	control := &threadRuntimeControl{
		runtime:        runtime,
		leaseDuration:  threadRuntimeLeaseDuration,
		runtimes:       o.threadRuntimes,
		holders:        o.sandboxHolders,
		inbox:          o.threadInbox,
		seedMessageIDs: append([]int64(nil), seedMessageIDs...),
		logger:         log,
	}
	if o.sandboxHolders != nil {
		if _, err := o.sandboxHolders.CreateActive(ctx, session.OrgID, db.CreateSessionSandboxHolderParams{
			SessionID:     session.ID,
			ContainerID:   sandbox.ID,
			HolderKind:    models.SessionSandboxHolderKindThreadRuntime,
			HolderID:      runtime.ID,
			OwnerNodeID:   o.nodeID,
			LeaseToken:    runtime.LeaseToken,
			LeaseDuration: threadRuntimeLeaseDuration,
		}); err != nil {
			control.Close(context.Background(), models.ThreadRuntimeStatusFailed, "holder_create_failed", err.Error())
			return nil, fmt.Errorf("create thread runtime sandbox holder: %w", err)
		}
	}
	if o.threadInbox != nil && len(seedMessageIDs) > 0 {
		if _, err := o.threadInbox.MarkDeliveringForMessages(ctx, session.OrgID, threadID, runtime.ID, o.nodeID, seedMessageIDs); err != nil {
			control.Close(context.Background(), models.ThreadRuntimeStatusFailed, "seed_inbox_delivering_failed", err.Error())
			return nil, fmt.Errorf("mark seed thread inbox messages delivering: %w", err)
		}
	}
	return control, nil
}

func (c *threadRuntimeControl) Attacher(registry *ThreadCancelRegistry) InteractiveHandleAttacher {
	if c == nil {
		return nil
	}
	return newThreadRuntimeHandleAttacher(threadRuntimeHandleAttacherConfig{
		Registry:       registry,
		ThreadID:       c.runtime.ThreadID,
		RuntimeStore:   c.runtimes,
		OrgID:          c.runtime.OrgID,
		RuntimeID:      c.runtime.ID,
		LeaseToken:     c.runtime.LeaseToken,
		LeaseDuration:  c.leaseDuration,
		InboxStore:     c.inbox,
		SeedMessageIDs: c.seedMessageIDs,
		Logger:         c.logger,
	})
}

func (c *threadRuntimeControl) StartHeartbeat(ctx context.Context, interval time.Duration, cancelRun context.CancelFunc) func() {
	if c == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = c.leaseDuration / 3
		if interval <= 0 {
			interval = 10 * time.Second
		}
	}
	heartbeatCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if !c.heartbeatOnce(heartbeatCtx) {
					if cancelRun != nil {
						cancelRun()
					}
					return
				}
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func (c *threadRuntimeControl) StartInboxPoller(ctx context.Context, interval time.Duration, deliver func(context.Context) error) func() {
	if c == nil || deliver == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = threadInboxOwnerPollInterval
	}
	pollCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				if err := deliver(pollCtx); err != nil {
					c.logger.Warn().Err(err).
						Str("runtime_id", c.runtime.ID.String()).
						Str("thread_id", c.runtime.ThreadID.String()).
						Msg("thread inbox owner poll failed")
				}
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func (c *threadRuntimeControl) heartbeatOnce(ctx context.Context) bool {
	writeCtx, cancel := context.WithTimeout(ctx, threadRuntimeStateUpdateTimeout)
	defer cancel()
	if c.runtimes != nil {
		ok, err := c.runtimes.HeartbeatWithLease(writeCtx, c.runtime.OrgID, c.runtime.ID, c.runtime.LeaseToken, c.leaseDuration)
		if err != nil {
			return c.recordHeartbeatError(err, "failed to heartbeat thread runtime")
		}
		if !ok {
			c.logger.Warn().
				Str("runtime_id", c.runtime.ID.String()).
				Msg("thread runtime heartbeat was fenced by lease state")
			return false
		}
	}
	if c.holders != nil {
		ok, err := c.holders.HeartbeatWithLease(writeCtx, c.runtime.OrgID, c.runtime.SessionID, models.SessionSandboxHolderKindThreadRuntime, c.runtime.ID, c.runtime.LeaseToken, c.leaseDuration)
		if err != nil {
			return c.recordHeartbeatError(err, "failed to heartbeat thread runtime sandbox holder")
		}
		if !ok {
			c.logger.Warn().
				Str("runtime_id", c.runtime.ID.String()).
				Msg("thread runtime sandbox holder heartbeat was fenced by lease state")
			return false
		}
	}
	c.heartbeatFailureStartedAt = time.Time{}
	return true
}

func (c *threadRuntimeControl) recordHeartbeatError(err error, message string) bool {
	now := time.Now()
	if c.heartbeatFailureStartedAt.IsZero() {
		c.heartbeatFailureStartedAt = now
	}
	c.logger.Warn().Err(err).
		Str("runtime_id", c.runtime.ID.String()).
		Time("heartbeat_error_since", c.heartbeatFailureStartedAt).
		Msg(message)
	if c.leaseDuration <= 0 {
		return true
	}
	if now.Sub(c.heartbeatFailureStartedAt) < c.leaseDuration {
		return true
	}
	c.logger.Error().
		Err(err).
		Str("runtime_id", c.runtime.ID.String()).
		Dur("heartbeat_error_duration", now.Sub(c.heartbeatFailureStartedAt)).
		Msg("thread runtime heartbeat errors exceeded lease duration; stopping local runtime")
	return false
}

func (c *threadRuntimeControl) Close(ctx context.Context, status models.ThreadRuntimeStatus, stopReason, lastError string) {
	if c == nil {
		return
	}
	if c.runtimes != nil {
		ok, err := c.runtimes.MarkTerminalWithLease(ctx, c.runtime.OrgID, c.runtime.ID, c.runtime.LeaseToken, status, stopReason, lastError)
		if err != nil {
			c.logger.Warn().Err(err).
				Str("runtime_id", c.runtime.ID.String()).
				Msg("failed to mark thread runtime terminal")
		} else if !ok {
			c.logger.Warn().
				Str("runtime_id", c.runtime.ID.String()).
				Msg("thread runtime terminal mark was fenced by lease state")
		}
	}
	if c.holders != nil {
		ok, err := c.holders.ReleaseWithLease(ctx, c.runtime.OrgID, c.runtime.SessionID, models.SessionSandboxHolderKindThreadRuntime, c.runtime.ID, c.runtime.LeaseToken)
		if err != nil {
			c.logger.Warn().Err(err).
				Str("runtime_id", c.runtime.ID.String()).
				Msg("failed to release thread runtime sandbox holder")
		} else if !ok {
			c.logger.Warn().
				Str("runtime_id", c.runtime.ID.String()).
				Msg("thread runtime sandbox holder release was fenced by lease state")
		}
	}
}

// keepSessionRunningIfSiblingRuntimesActive is a one-way "stay running"
// nudge: when a thread runtime closes but other thread runtimes for the same
// session are still active, it re-stamps the session as running so a stale
// idle write from the closing thread's UpdateTurnComplete doesn't make the
// session look done while siblings are still mutating the shared workspace.
//
// It deliberately never writes idle/completed. When no sibling holders
// remain, the function returns without touching the row, because the caller
// has already chosen the correct terminal state through one of the normal
// completion paths (UpdateTurnComplete → idle on success, handleCancelledSession
// / handlePolicyStoppedSession / failRun on stop/fail). Writing idle here
// would race those paths and could either clobber a richer terminal state
// (e.g. completed/failed/cancelled) or revert a session whose last turn
// hasn't finished yet.
//
// Invariant: every caller must already have committed the closing runtime's
// terminal status via the normal completion paths before invoking this
// function — it only adjusts for "sibling still running", never for "I just
// finished".
func keepSessionRunningIfSiblingRuntimesActive(ctx context.Context, sessions sessionStatusUpdater, holders SessionSandboxHolderStore, orgID, sessionID uuid.UUID, log zerolog.Logger) {
	if sessions == nil || holders == nil {
		return
	}
	active, err := holders.CountActiveThreadRuntimesBySession(ctx, orgID, sessionID)
	if err != nil {
		log.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to count active sibling thread runtime holders after runtime close")
		return
	}
	if active == 0 {
		// No siblings left — defer to the closing turn's terminal status
		// write rather than touching the row here.
		return
	}
	// Use the combined update so status and sandbox_state move together. The
	// previous split (UpdateStatus then UpdateSandboxState) could leave the
	// row showing status='running' while sandbox_state had not been bumped,
	// or vice versa, if one write failed.
	if err := sessions.MarkRunningWithSandboxState(ctx, orgID, sessionID, models.SandboxStateRunning); err != nil {
		log.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Int("active_runtime_holders", active).
			Msg("failed to keep session running while sibling thread runtimes remain active")
	}
}

type threadRuntimeHandleAttacherConfig struct {
	Registry       *ThreadCancelRegistry
	ThreadID       uuid.UUID
	RuntimeStore   ThreadRuntimeStore
	InboxStore     ThreadInboxStore
	OrgID          uuid.UUID
	RuntimeID      uuid.UUID
	LeaseToken     uuid.UUID
	LeaseDuration  time.Duration
	SeedMessageIDs []int64
	Logger         zerolog.Logger
}

type threadRuntimeHandleAttacher struct {
	cfg threadRuntimeHandleAttacherConfig
}

func newThreadRuntimeHandleAttacher(cfg threadRuntimeHandleAttacherConfig) InteractiveHandleAttacher {
	if cfg.Registry == nil && cfg.RuntimeStore == nil {
		return nil
	}
	return &threadRuntimeHandleAttacher{cfg: cfg}
}

func (a *threadRuntimeHandleAttacher) Attach(handle InteractiveCommandHandle) {
	if a == nil || handle == nil {
		return
	}
	if a.cfg.Registry != nil {
		a.cfg.Registry.AttachHandle(a.cfg.ThreadID, handle)
	}
	if a.cfg.RuntimeStore == nil || a.cfg.RuntimeID == uuid.Nil || a.cfg.LeaseToken == uuid.Nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), threadRuntimeStateUpdateTimeout)
	defer cancel()
	ok, err := a.cfg.RuntimeStore.MarkLiveWithLease(ctx, a.cfg.OrgID, a.cfg.RuntimeID, a.cfg.LeaseToken, handle.ID(), a.cfg.LeaseDuration)
	if err != nil {
		a.warn().Err(err).
			Str("runtime_id", a.cfg.RuntimeID.String()).
			Str("thread_id", a.cfg.ThreadID.String()).
			Str("handle_id", handle.ID()).
			Msg("failed to mark thread runtime live after handle attach")
		if a.cfg.Registry != nil {
			a.cfg.Registry.DetachHandle(a.cfg.ThreadID)
		}
		return
	}
	if !ok {
		a.warn().
			Str("runtime_id", a.cfg.RuntimeID.String()).
			Str("thread_id", a.cfg.ThreadID.String()).
			Str("handle_id", handle.ID()).
			Msg("thread runtime live mark was fenced by lease state")
		if a.cfg.Registry != nil {
			a.cfg.Registry.DetachHandle(a.cfg.ThreadID)
		}
		return
	}
	if a.cfg.InboxStore != nil && len(a.cfg.SeedMessageIDs) > 0 {
		if _, err := a.cfg.InboxStore.MarkAckedForSeedMessages(ctx, a.cfg.OrgID, a.cfg.ThreadID, a.cfg.RuntimeID, a.cfg.SeedMessageIDs); err != nil {
			a.warn().Err(err).
				Str("runtime_id", a.cfg.RuntimeID.String()).
				Str("thread_id", a.cfg.ThreadID.String()).
				Msg("failed to ack seed thread inbox messages after live handle attach")
		}
	}
}

func (a *threadRuntimeHandleAttacher) Detach() {
	if a == nil || a.cfg.Registry == nil {
		return
	}
	a.cfg.Registry.DetachHandle(a.cfg.ThreadID)
}

func (a *threadRuntimeHandleAttacher) warn() *zerolog.Event {
	return a.cfg.Logger.Warn()
}

func (o *Orchestrator) DeliverThreadInbox(ctx context.Context, orgID, sessionID, threadID uuid.UUID) error {
	if o == nil || o.threadRuntimes == nil || o.threadInbox == nil || o.threadCancels == nil {
		return nil
	}

	// Pre-check ownership *before* acquiring the per-thread delivery lock.
	// The lock exists to serialize delivery attempts on the owning worker;
	// non-owning workers (stray notifications, mis-routed jobs) must not
	// populate o.threadDeliveryLocks because they have no Close() path to
	// clean it up — forgetThreadDeliveryLock only runs from
	// RunAgent/ContinueSession on the owner, so any lock created here on a
	// non-owner would leak for the life of the worker process. The fresh
	// runtime read used to drive delivery happens again below under the
	// lock so a concurrent commit on this same worker can't leave us
	// chasing stale delivery cursors.
	if runtime, err := o.threadRuntimes.GetActiveByThread(ctx, orgID, threadID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get active thread runtime: %w", err)
	} else if runtime.SessionID != sessionID {
		return fmt.Errorf("active thread runtime session mismatch: runtime session %s != payload session %s", runtime.SessionID, sessionID)
	} else if runtime.OwnerNodeID != "" && o.nodeID != "" && runtime.OwnerNodeID != o.nodeID {
		return &ThreadRuntimeOwnedElsewhereError{
			RuntimeID:   runtime.ID,
			ThreadID:    threadID,
			OwnerNodeID: runtime.OwnerNodeID,
		}
	}

	lock := o.threadDeliveryLock(threadID)
	lock.Lock()
	defer lock.Unlock()

	runtime, err := o.threadRuntimes.GetActiveByThread(ctx, orgID, threadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get active thread runtime: %w", err)
	}
	if runtime.SessionID != sessionID {
		return fmt.Errorf("active thread runtime session mismatch: runtime session %s != payload session %s", runtime.SessionID, sessionID)
	}
	if runtime.OwnerNodeID != "" && o.nodeID != "" && runtime.OwnerNodeID != o.nodeID {
		return &ThreadRuntimeOwnedElsewhereError{
			RuntimeID:   runtime.ID,
			ThreadID:    threadID,
			OwnerNodeID: runtime.OwnerNodeID,
		}
	}

	formatter := o.threadRuntimeInputFormatter(runtime.AgentType)

	entries, err := o.threadInbox.ClaimDeliverableAfter(ctx, orgID, threadID, runtime.ID, runtime.OwnerNodeID, runtime.LastDeliveredSequence, threadInboxDeliveryBatchSize)
	if err != nil {
		return fmt.Errorf("claim deliverable thread inbox entries: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}

	for _, entry := range entries {
		input, err := formatter.FormatThreadRuntimeInput(entry)
		if errors.Is(err, ErrThreadRuntimeLiveInputUnsupported) {
			if _, markErr := o.threadInbox.MarkDeadLetter(ctx, orgID, threadID, entry.ID, err.Error()); markErr != nil {
				return fmt.Errorf("mark unsupported thread inbox entry %d dead-letter: %w", entry.SequenceNo, markErr)
			}
			o.logger.Warn().
				Err(err).
				Str("runtime_id", runtime.ID.String()).
				Str("thread_id", threadID.String()).
				Str("agent_type", string(runtime.AgentType)).
				Int64("sequence_no", entry.SequenceNo).
				Msg("dead-lettered unsupported live-input thread inbox entry")
			continue
		}
		if err != nil {
			reason := err.Error()
			if _, markErr := o.threadInbox.MarkDeadLetter(ctx, orgID, threadID, entry.ID, reason); markErr != nil {
				return fmt.Errorf("mark thread inbox entry %d dead-letter after format failure: %w", entry.SequenceNo, markErr)
			}
			o.logger.Warn().
				Err(err).
				Str("runtime_id", runtime.ID.String()).
				Str("thread_id", threadID.String()).
				Int64("sequence_no", entry.SequenceNo).
				Msg("dead-lettered poison thread inbox entry")
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, threadInboxHandleWriteTimeout)
		err = o.threadCancels.DeliverInput(writeCtx, threadID, input)
		cancel()
		if errors.Is(err, ErrThreadHandleUnavailable) || errors.Is(err, ErrInputNotOpen) {
			o.logger.Info().
				Err(err).
				Str("runtime_id", runtime.ID.String()).
				Str("thread_id", threadID.String()).
				Int64("sequence_no", entry.SequenceNo).
				Msg("thread runtime handle not available for live inbox delivery; entry remains claimed for retry")
			return nil
		}
		if err != nil {
			if _, markErr := o.threadInbox.MarkDeliveryFailed(ctx, orgID, threadID, runtime.ID, entry.ID, err.Error(), db.DefaultThreadInboxMaxDeliveryAttempts); markErr != nil {
				return fmt.Errorf("mark thread inbox entry %d failed after handle write failure: %w", entry.SequenceNo, markErr)
			}
			return fmt.Errorf("deliver thread inbox entry %d to runtime handle: %w", entry.SequenceNo, err)
		}
		deliveredRows, err := o.threadInbox.MarkDeliveredForEntry(ctx, orgID, threadID, runtime.ID, runtime.OwnerNodeID, entry.ID, entry.SequenceNo)
		if err != nil {
			return fmt.Errorf("mark thread inbox entry %d delivered: %w", entry.SequenceNo, err)
		}
		if deliveredRows != 1 {
			return fmt.Errorf("mark thread inbox entry %d delivered: expected 1 row, updated %d", entry.SequenceNo, deliveredRows)
		}
		ok, err := o.threadRuntimes.CommitInboxDeliveryWithLease(ctx, orgID, runtime.ID, runtime.LeaseToken, threadID, runtime.OwnerNodeID, entry.SequenceNo, entry.SequenceNo)
		if err != nil {
			return fmt.Errorf("commit thread runtime inbox delivery cursor: %w", err)
		}
		if !ok {
			return fmt.Errorf("%w: %s", ErrThreadRuntimeLeaseLost, runtime.ID)
		}
	}
	return nil
}

// threadRuntimeInputFormatter returns the formatter to use for delivering
// inbox payloads to an adapter's live handle. Every adapter participates by
// default; an adapter may implement ThreadRuntimeInputFormatter to override
// the wire encoding. There is no opt-out — turn-bound CLIs whose handles do
// not accept stdin will surface ErrInputNotOpen from WriteInput, which the
// delivery loop already treats as "leave the entry claimed and try again".
func (o *Orchestrator) threadRuntimeInputFormatter(agentType models.AgentType) ThreadRuntimeInputFormatter {
	if o != nil && len(o.adapters) > 0 {
		if adapter := o.adapters[agentType]; adapter != nil {
			if formatter, ok := adapter.(ThreadRuntimeInputFormatter); ok {
				return formatter
			}
		}
	}
	return defaultThreadRuntimeInputFormatter{}
}

// defaultThreadRuntimeInputFormatter is the formatter used when an adapter
// has no provider-specific encoding. It delegates to the package-level
// formatThreadInboxRuntimeInput helper so the wire shape is identical to
// what an adapter would produce by hand.
type defaultThreadRuntimeInputFormatter struct{}

func (defaultThreadRuntimeInputFormatter) FormatThreadRuntimeInput(entry models.ThreadInboxEntry) ([]byte, error) {
	return formatThreadInboxRuntimeInput(entry)
}

func (o *Orchestrator) threadDeliveryLock(threadID uuid.UUID) *sync.Mutex {
	val, _ := o.threadDeliveryLocks.LoadOrStore(threadID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

func (o *Orchestrator) forgetThreadDeliveryLock(threadID uuid.UUID) {
	if o == nil || threadID == uuid.Nil {
		return
	}
	o.threadDeliveryLocks.Delete(threadID)
}

func formatThreadInboxRuntimeInput(entry models.ThreadInboxEntry) ([]byte, error) {
	switch entry.EntryType {
	case models.ThreadInboxEntryTypeUserMessage, models.ThreadInboxEntryTypeHumanInputAnswer:
	default:
		return nil, fmt.Errorf("unsupported thread inbox entry type for live delivery: %s", entry.EntryType)
	}
	var payload struct {
		Content string `json:"content"`
	}
	if len(entry.Payload) == 0 {
		return nil, fmt.Errorf("thread inbox entry %s has empty payload", entry.ID)
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode thread inbox entry %s payload: %w", entry.ID, err)
	}
	content := payload.Content
	if content == "" {
		return nil, fmt.Errorf("thread inbox entry %s has no live-delivery content", entry.ID)
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return []byte(content), nil
}

func threadRuntimeSeedMessageIDs(messages []models.SessionMessage) []int64 {
	ids := make([]int64, 0, len(messages))
	for _, msg := range messages {
		if msg.ID != 0 && msg.Role == models.MessageRoleUser {
			ids = append(ids, msg.ID)
		}
	}
	return ids
}
