package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
)

const defaultExtensionQueueAgeThreshold = 2 * time.Minute
const runtimeStopPersistenceTimeout = 2 * time.Second

type runtimeConfig struct {
	SoftBudget               time.Duration
	NoProgressTimeout        time.Duration
	GracefulShutdownWindow   time.Duration
	CheckpointFinalizeWindow time.Duration
	ExtensionIncrement       time.Duration
	MaxAutomaticExtension    time.Duration
	AbsoluteRuntimeCeiling   time.Duration
	QueueAgeThreshold        time.Duration
}

func (o *Orchestrator) resolveRuntimeConfig(ctx context.Context, orgID uuid.UUID) runtimeConfig {
	cfg := runtimeConfig{
		SoftBudget:               DefaultSandboxTimeout,
		NoProgressTimeout:        time.Duration(models.DefaultNoProgressTimeoutSeconds) * time.Second,
		GracefulShutdownWindow:   time.Duration(models.DefaultGracefulShutdownWindowSeconds) * time.Second,
		CheckpointFinalizeWindow: time.Duration(models.DefaultCheckpointFinalizeWindowSeconds) * time.Second,
		ExtensionIncrement:       time.Duration(models.DefaultAutomaticExtensionSeconds) * time.Second,
		MaxAutomaticExtension:    time.Duration(models.DefaultMaxAutomaticExtensionSeconds) * time.Second,
		AbsoluteRuntimeCeiling:   time.Duration(models.DefaultAbsoluteRuntimeCeilingSeconds) * time.Second,
		QueueAgeThreshold:        defaultExtensionQueueAgeThreshold,
	}

	if o.orgs == nil {
		return cfg
	}

	org, err := o.orgs.GetByID(ctx, orgID)
	if err != nil {
		return cfg
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return cfg
	}

	cfg.SoftBudget = time.Duration(settings.MaxSessionDurationSeconds) * time.Second
	cfg.NoProgressTimeout = time.Duration(settings.RuntimeBudgets.NoProgressTimeoutSeconds) * time.Second
	cfg.GracefulShutdownWindow = time.Duration(settings.RuntimeBudgets.GracefulShutdownWindowSeconds) * time.Second
	cfg.CheckpointFinalizeWindow = time.Duration(settings.RuntimeBudgets.CheckpointFinalizationWindowSeconds) * time.Second
	cfg.ExtensionIncrement = time.Duration(settings.RuntimeBudgets.AutomaticExtensionSeconds) * time.Second
	cfg.MaxAutomaticExtension = time.Duration(settings.RuntimeBudgets.MaxAutomaticExtensionSeconds) * time.Second
	cfg.AbsoluteRuntimeCeiling = time.Duration(settings.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds) * time.Second
	return cfg
}

func checkpointCapabilityForAgent(agentType models.AgentType) models.CheckpointCapability {
	switch agentType {
	case models.AgentTypeCodex, models.AgentTypeClaudeCode, models.AgentTypeGeminiCLI:
		return models.CheckpointCapabilityFullResume
	case models.AgentTypeAmp, models.AgentTypePi:
		return models.CheckpointCapabilityFilesystemOnly
	default:
		return models.CheckpointCapabilityNoDurable
	}
}

func stopReasonToRuntime(reason StopReason) models.RuntimeStopReason {
	switch reason {
	case StopReasonUserCancel:
		return models.RuntimeStopReasonUserCancel
	case StopReasonSoftBudget:
		return models.RuntimeStopReasonSoftBudget
	case StopReasonNoProgress:
		return models.RuntimeStopReasonNoProgress
	case StopReasonAbsoluteCeiling:
		return models.RuntimeStopReasonAbsoluteCeiling
	default:
		return models.RuntimeStopReasonNone
	}
}

type runtimeProgressTracker struct {
	mu               sync.Mutex
	lastProgressAt   time.Time
	lastProgressType models.RuntimeProgressType
	lastStrength     models.RuntimeProgressStrength
	lastStrongAt     time.Time
	lastPersistedAt  time.Time
	activeTools      map[string]time.Time
}

func newRuntimeProgressTracker(now time.Time) *runtimeProgressTracker {
	return &runtimeProgressTracker{
		lastProgressAt:   now,
		lastProgressType: models.RuntimeProgressTypeAssistantOutput,
		lastStrength:     models.RuntimeProgressStrengthWeak,
		lastPersistedAt:  now,
	}
}

func (t *runtimeProgressTracker) Record(progressType models.RuntimeProgressType, strength models.RuntimeProgressStrength, observedAt time.Time, toolID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	t.lastProgressAt = observedAt
	t.lastProgressType = progressType
	t.lastStrength = strength
	if strength == models.RuntimeProgressStrengthStrong {
		t.lastStrongAt = observedAt
	}
	switch progressType {
	case models.RuntimeProgressTypeToolUse:
		if toolID != "" {
			if t.activeTools == nil {
				t.activeTools = make(map[string]time.Time)
			}
			t.activeTools[toolID] = observedAt
		}
	case models.RuntimeProgressTypeToolResult, models.RuntimeProgressTypeQuestionBlocked, models.RuntimeProgressTypeCheckpoint:
		if toolID != "" && t.activeTools != nil {
			delete(t.activeTools, toolID)
		} else {
			clear(t.activeTools)
		}
	}
}

func (t *runtimeProgressTracker) Snapshot() (time.Time, time.Time, models.RuntimeProgressType, models.RuntimeProgressStrength) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastProgressAt, t.lastStrongAt, t.lastProgressType, t.lastStrength
}

func (t *runtimeProgressTracker) ToolActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.activeTools) > 0
}

func (t *runtimeProgressTracker) ShouldPersist() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastProgressAt.IsZero() {
		return false
	}
	if t.lastPersistedAt.IsZero() || t.lastProgressAt.After(t.lastPersistedAt) {
		t.lastPersistedAt = t.lastProgressAt
		return true
	}
	return false
}

func runtimeProgressFromLog(entry LogEntry) (models.RuntimeProgressType, models.RuntimeProgressStrength, string, bool) {
	switch entry.Level {
	case "tool_use":
		if isTerminalCommandExecution(entry.Metadata) {
			return models.RuntimeProgressTypeToolResult, models.RuntimeProgressStrengthStrong, commandExecutionItemIDFromMetadata(entry.Metadata), true
		}
		return models.RuntimeProgressTypeToolUse, models.RuntimeProgressStrengthWeak, commandExecutionItemIDFromMetadata(entry.Metadata), true
	case "question":
		return models.RuntimeProgressTypeQuestionBlocked, models.RuntimeProgressStrengthStrong, "", true
	case "output":
		if entry.Metadata != nil {
			if typ, ok := entry.Metadata["type"].(string); ok && typ == "tool_result" {
				return models.RuntimeProgressTypeToolResult, models.RuntimeProgressStrengthStrong, commandExecutionItemIDFromMetadata(entry.Metadata), true
			}
		}
		return models.RuntimeProgressTypeAssistantOutput, models.RuntimeProgressStrengthWeak, "", true
	case "debug":
		if itemID, ok := commandExecutionStartItemID(entry.Message); ok {
			return models.RuntimeProgressTypeToolUse, models.RuntimeProgressStrengthWeak, itemID, true
		}
		return models.RuntimeProgressTypeAssistantReason, models.RuntimeProgressStrengthWeak, "", true
	default:
		return models.RuntimeProgressTypeNone, models.RuntimeProgressStrengthNone, "", false
	}
}

func commandExecutionItemIDFromMetadata(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	itemID, _ := metadata["item_id"].(string)
	return itemID
}

func isTerminalCommandExecution(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	tool, _ := metadata["tool"].(string)
	if tool != "command_execution" {
		return false
	}
	status, _ := metadata["status"].(string)
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func commandExecutionStartItemID(message string) (string, bool) {
	if message == "" {
		return "", false
	}
	var event struct {
		Type string `json:"type"`
		Item struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(message), &event); err != nil {
		return "", false
	}
	if event.Type != "item.started" || event.Item.Type != "command_execution" {
		return "", false
	}
	return event.Item.ID, true
}

type runtimeController struct {
	cfg           runtimeConfig
	sessions      SessionStore
	jobs          JobStore
	cancels       *CancelRegistry
	logger        zerolog.Logger
	orgID         uuid.UUID
	sessionID     uuid.UUID
	lockToken     uuid.UUID
	maxConcurrent int
	isDraining    func() bool
	tracker       *runtimeProgressTracker

	mu            sync.Mutex
	startedAt     time.Time
	softDeadline  time.Time
	hardDeadline  time.Time
	stopRequested StopReason
}

func newRuntimeController(
	cfg runtimeConfig,
	sessions SessionStore,
	jobs JobStore,
	cancels *CancelRegistry,
	logger zerolog.Logger,
	orgID, sessionID uuid.UUID,
	maxConcurrent int,
	isDraining func() bool,
	tracker *runtimeProgressTracker,
) *runtimeController {
	return &runtimeController{
		cfg:           cfg,
		sessions:      sessions,
		jobs:          jobs,
		cancels:       cancels,
		logger:        logger,
		orgID:         orgID,
		sessionID:     sessionID,
		maxConcurrent: maxConcurrent,
		isDraining:    isDraining,
		tracker:       tracker,
	}
}

func (c *runtimeController) Begin(ctx context.Context, startedAt time.Time, capability models.CheckpointCapability) error {
	c.mu.Lock()
	c.startedAt = startedAt
	c.softDeadline = startedAt.Add(c.cfg.SoftBudget)
	c.hardDeadline = startedAt.Add(c.cfg.AbsoluteRuntimeCeiling)
	c.mu.Unlock()

	if token, ok := jobctx.LockTokenFromContext(ctx); ok {
		c.lockToken = token
	}
	return c.sessions.BeginRuntime(ctx, c.orgID, c.sessionID, capability, c.softDeadline, c.hardDeadline, startedAt)
}

func (c *runtimeController) Run(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.tick(ctx, time.Now())
		}
	}
}

func (c *runtimeController) RequestStop(reason StopReason) {
	c.mu.Lock()
	if c.stopRequested != StopReasonNone {
		c.mu.Unlock()
		return
	}
	c.stopRequested = reason
	c.mu.Unlock()
	if c.cancels != nil {
		c.cancels.RequestStop(c.sessionID, reason, c.cfg.GracefulShutdownWindow)
	}
	runtimeReason := stopReasonToRuntime(reason)
	if runtimeReason != models.RuntimeStopReasonNone {
		stopAfter := time.Now().UTC().Add(c.cfg.GracefulShutdownWindow + c.cfg.CheckpointFinalizeWindow + defaultRuntimeStallAge)
		persistCtx, cancel := context.WithTimeout(context.Background(), runtimeStopPersistenceTimeout)
		defer cancel()
		if err := c.sessions.MarkRuntimeStopRequested(persistCtx, c.orgID, c.sessionID, runtimeReason, stopAfter); err != nil {
			c.logger.Warn().
				Err(err).
				Str("session_id", c.sessionID.String()).
				Str("stop_reason", string(runtimeReason)).
				Msg("failed to persist runtime stop request")
		}
	}
	c.logger.Info().
		Str("session_id", c.sessionID.String()).
		Str("stop_reason", string(runtimeReason)).
		Msg("runtime stop requested")
}

func (c *runtimeController) tick(ctx context.Context, now time.Time) {
	lastProgressAt, lastStrongAt, progressType, progressStrength := c.tracker.Snapshot()
	toolActive := c.tracker.ToolActive()
	if !lastProgressAt.IsZero() && c.tracker.ShouldPersist() {
		if err := c.sessions.RecordRuntimeProgress(ctx, c.orgID, c.sessionID, progressType, progressStrength, lastProgressAt); err != nil {
			c.logger.Debug().Err(err).Msg("failed to persist runtime progress")
		}
	}

	c.mu.Lock()
	stopRequested := c.stopRequested
	softDeadline := c.softDeadline
	hardDeadline := c.hardDeadline
	c.mu.Unlock()
	if stopRequested != StopReasonNone {
		return
	}

	if c.cfg.NoProgressTimeout > 0 && !toolActive && !lastProgressAt.IsZero() && now.Sub(lastProgressAt) >= c.cfg.NoProgressTimeout {
		c.RequestStop(StopReasonNoProgress)
		return
	}

	if now.After(hardDeadline) || now.Equal(hardDeadline) {
		c.RequestStop(StopReasonAbsoluteCeiling)
		return
	}

	if now.Before(softDeadline) {
		return
	}
	if c.shouldExtend(ctx, now, lastStrongAt) {
		if c.tryExtend(ctx, softDeadline) {
			return
		}
	}
	c.RequestStop(StopReasonSoftBudget)
}

func (c *runtimeController) shouldExtend(ctx context.Context, now, lastStrongAt time.Time) bool {
	if lastStrongAt.IsZero() {
		return false
	}
	if now.Sub(lastStrongAt) > c.cfg.ExtensionIncrement {
		return false
	}
	if c.isDraining != nil && c.isDraining() {
		return false
	}
	if c.maxConcurrent > 0 {
		running, err := c.sessions.CountRunningByOrg(ctx, c.orgID)
		if err == nil && running > c.maxConcurrent {
			return false
		}
	}
	if c.jobs != nil {
		age, ok, err := c.jobs.OldestPendingSessionJobAge(ctx)
		if err == nil && ok && age >= c.cfg.QueueAgeThreshold {
			return false
		}
	}
	return true
}

func (c *runtimeController) tryExtend(ctx context.Context, expectedSoftDeadline time.Time) bool {
	c.mu.Lock()
	currentSoftDeadline := c.softDeadline
	currentHardDeadline := c.hardDeadline
	startedAt := c.startedAt
	c.mu.Unlock()
	if !currentSoftDeadline.Equal(expectedSoftDeadline) {
		return true
	}

	maxSoftDeadline := currentHardDeadline
	if c.cfg.MaxAutomaticExtension > 0 {
		maxByPolicy := startedAt.Add(c.cfg.SoftBudget + c.cfg.MaxAutomaticExtension)
		if maxByPolicy.Before(maxSoftDeadline) {
			maxSoftDeadline = maxByPolicy
		}
	}
	newSoftDeadline := currentSoftDeadline.Add(c.cfg.ExtensionIncrement)
	if newSoftDeadline.After(maxSoftDeadline) {
		newSoftDeadline = maxSoftDeadline
	}
	if !newSoftDeadline.After(currentSoftDeadline) {
		return false
	}

	granted, err := c.sessions.GrantRuntimeExtension(ctx, c.orgID, c.sessionID, c.lockToken, currentSoftDeadline, newSoftDeadline, currentHardDeadline, int(newSoftDeadline.Sub(currentSoftDeadline).Seconds()))
	if err != nil || !granted {
		return false
	}
	c.mu.Lock()
	c.softDeadline = newSoftDeadline
	c.mu.Unlock()
	c.logger.Info().
		Str("session_id", c.sessionID.String()).
		Dur("new_soft_deadline_in", time.Until(newSoftDeadline)).
		Msg("granted runtime extension")
	return true
}
