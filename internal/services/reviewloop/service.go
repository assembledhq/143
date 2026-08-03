package reviewloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
)

var (
	ErrInvalidPassCount         = errors.New("max_passes must be between 1 and 5")
	ErrInvalidFixMode           = errors.New("fix_mode must be minimal or exhaustive")
	ErrUnsupportedReviewAgent   = errors.New("agent does not support native review")
	ErrSessionSnapshotExpired   = errors.New("session sandbox snapshot has expired")
	ErrNoRunningReviewLoop      = errors.New("no running review loop for thread")
	ErrReviewLoopAlreadyRunning = errors.New("review loop already running for session")
	ErrUnrecognizedDecision     = errors.New("review loop decision was not REVIEW_CLEAN or NEEDS_FIX_PASS")
)

const MaxReviewPasses = 5

const strandedPublicationReviewSummary = "Publication review became idle without a live continuation job; restarting review from the current workspace."

type Store interface {
	GetPrimaryChangesetID(ctx context.Context, orgID, sessionID uuid.UUID) (uuid.UUID, error)
	CreateLoopWithInitialPass(ctx context.Context, loop *models.SessionReviewLoop, pass *models.SessionReviewLoopPass) error
	CreatePass(ctx context.Context, pass *models.SessionReviewLoopPass) error
	SetPassReviewMessage(ctx context.Context, orgID, passID uuid.UUID, messageID int64) error
	GetRunningLoopBySession(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionReviewLoop, error)
	GetRunningLoopByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionReviewLoop, error)
	GetLatestPass(ctx context.Context, orgID, loopID uuid.UUID) (models.SessionReviewLoopPass, error)
	MarkPassDeciding(ctx context.Context, orgID, passID uuid.UUID, reviewOutput string, decisionMessageID int64) error
	MarkPassFixing(ctx context.Context, orgID, passID uuid.UUID, decision models.ReviewLoopDecision, fixMessageID int64) error
	MarkPassClean(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string) error
	MarkPassCleanAndEnqueueOpenPR(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string, payload map[string]any, dedupeKey string) error
	MarkPassFixComplete(ctx context.Context, orgID, passID uuid.UUID, fixSummary string) error
	MarkPassNeedsHumanDecision(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string) error
	MarkPassNeedsHumanDecisionAndEnqueueOpenPR(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string, payload map[string]any, dedupeKey string) error
	MarkLoopFailed(ctx context.Context, orgID, loopID uuid.UUID, summary string) error
	MarkLoopFailedAndEnqueueOpenPR(ctx context.Context, orgID, loopID uuid.UUID, summary string, payload map[string]any, dedupeKey string) error
}

type Runtime interface {
	GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	CreateThread(ctx context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error)
	SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error)
}

type RuntimeAdapter struct {
	Sessions interface {
		GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	}
	Threads interface {
		CreateThread(ctx context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error)
		SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error)
	}
}

func (a RuntimeAdapter) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	return a.Sessions.GetByID(ctx, orgID, sessionID)
}

func (a RuntimeAdapter) CreateThread(ctx context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	return a.Threads.CreateThread(ctx, input)
}

func (a RuntimeAdapter) SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	return a.Threads.SendMessage(ctx, input)
}

type Service struct {
	store             Store
	runtime           Runtime
	evidenceRefresher PublicationEvidenceRefresher
	metrics           reviewLoopMetrics
}

type reviewLoopMetrics interface {
	RecordLoop(ctx context.Context, outcome string, passes int)
	RecordFix(ctx context.Context)
}

type otelReviewLoopMetrics struct{}

func (otelReviewLoopMetrics) RecordLoop(ctx context.Context, outcome string, passes int) {
	metrics.RecordPrePRReviewLoop(ctx, outcome, passes)
}

func (otelReviewLoopMetrics) RecordFix(ctx context.Context) {
	metrics.RecordPrePRReviewFix(ctx)
}

type PublicationEvidenceRefresher interface {
	RefreshPublicationEvidence(ctx context.Context, loop models.SessionReviewLoop) (workspaceRevision int64, desiredHeadSHA string, err error)
}

type strandedPublicationReviewStore interface {
	ListStrandedPublicationLoops(ctx context.Context, orgID uuid.UUID, inactiveBefore time.Time, limit int) ([]db.StrandedPublicationReviewLoop, error)
	RestartStrandedPublicationLoop(ctx context.Context, orgID, loopID uuid.UUID, inactiveBefore time.Time, summary string) (bool, error)
}

func NewService(store Store, runtime Runtime) *Service {
	return &Service{store: store, runtime: runtime, metrics: otelReviewLoopMetrics{}}
}

func (s *Service) SetPublicationEvidenceRefresher(refresher PublicationEvidenceRefresher) {
	s.evidenceRefresher = refresher
}

// ReconcileStrandedPublicationLoops restarts publication reviews that have no
// live agent work left to advance them. Restarting from the durable workspace
// is safer than replaying an assistant summary because the failed transition
// may have stopped before or after applying workspace edits.
func (s *Service) ReconcileStrandedPublicationLoops(
	ctx context.Context,
	orgID uuid.UUID,
	inactiveBefore time.Time,
	limit int,
) (int, error) {
	recoveryStore, ok := s.store.(strandedPublicationReviewStore)
	if !ok {
		return 0, errors.New("publication review recovery store is unavailable")
	}
	candidates, err := recoveryStore.ListStrandedPublicationLoops(ctx, orgID, inactiveBefore, limit)
	if err != nil {
		return 0, err
	}
	restarted := 0
	var reconcileErr error
	for _, candidate := range candidates {
		ok, err := recoveryStore.RestartStrandedPublicationLoop(
			ctx, orgID, candidate.LoopID, inactiveBefore, strandedPublicationReviewSummary,
		)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("restart review loop %s: %w", candidate.LoopID, err))
			continue
		}
		if ok {
			restarted++
		}
	}
	return restarted, reconcileErr
}

type StartReviewLoopRequest struct {
	AgentType         models.AgentType
	Model             string
	MaxPasses         int
	FixMode           models.ReviewLoopFixMode
	Source            models.ReviewLoopSource
	AutomationRunID   *uuid.UUID
	StartedByUserID   *uuid.UUID
	ReviewRequired    bool
	ChangesetID       *uuid.UUID
	WorkspaceRevision *int64
	DesiredHeadSHA    *string
}

func (s *Service) Start(ctx context.Context, orgID, sessionID uuid.UUID, req StartReviewLoopRequest) (*models.SessionReviewLoop, error) {
	if req.MaxPasses < 1 || req.MaxPasses > MaxReviewPasses {
		return nil, ErrInvalidPassCount
	}
	source := req.Source
	if source == "" {
		source = models.ReviewLoopSourceManual
	}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	if source == models.ReviewLoopSourcePublication {
		if req.ChangesetID == nil || req.WorkspaceRevision == nil || req.DesiredHeadSHA == nil || strings.TrimSpace(*req.DesiredHeadSHA) == "" {
			return nil, errors.New("publication review requires changeset, workspace revision, and desired head SHA")
		}
	}
	fixMode := req.FixMode
	if fixMode == "" {
		fixMode = models.ReviewLoopFixModeMinimal
	}
	if err := fixMode.Validate(); err != nil {
		return nil, ErrInvalidFixMode
	}
	session, err := s.runtime.GetSession(ctx, orgID, sessionID)
	if err != nil {
		return nil, err
	}
	if session.SandboxState == models.SandboxStateDestroyed {
		return nil, ErrSessionSnapshotExpired
	}
	if session.SnapshotKey == nil || strings.TrimSpace(*session.SnapshotKey) == "" {
		return nil, ErrSessionSnapshotExpired
	}
	agentType := req.AgentType
	if agentType == "" {
		agentType = session.AgentType
	}
	if err := agentType.Validate(); err != nil {
		return nil, err
	}
	if !models.AgentSupportsNativeReview(agentType) {
		return nil, ErrUnsupportedReviewAgent
	}
	if req.Model != "" {
		if err := models.ValidateModelForAgentType(agentType, req.Model); err != nil {
			return nil, err
		}
	}
	if _, err := s.store.GetRunningLoopBySession(ctx, orgID, sessionID); err == nil {
		return nil, ErrReviewLoopAlreadyRunning
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	threadLabel := reviewThreadLabel(agentType)
	thread, err := s.runtime.CreateThread(ctx, threadsvc.CreateThreadInput{
		SessionID: sessionID,
		OrgID:     orgID,
		AgentType: string(agentType),
		Model:     req.Model,
		Label:     threadLabel,
	})
	if err != nil {
		return nil, err
	}
	loop := &models.SessionReviewLoop{
		OrgID:             orgID,
		SessionID:         sessionID,
		AutomationRunID:   req.AutomationRunID,
		ThreadID:          &thread.ID,
		Status:            models.ReviewLoopStatusRunning,
		Source:            source,
		ChangesetID:       req.ChangesetID,
		WorkspaceRevision: req.WorkspaceRevision,
		DesiredHeadSHA:    req.DesiredHeadSHA,
		AgentType:         agentType,
		MaxPasses:         req.MaxPasses,
		FixMode:           fixMode,
		ReviewRequired:    req.ReviewRequired,
		StartedByUserID:   req.StartedByUserID,
	}
	if session.SnapshotKey != nil && *session.SnapshotKey != "" {
		loop.LoopStartCheckpointKey = session.SnapshotKey
		loop.LatestCheckpointKey = session.SnapshotKey
	}
	pass := &models.SessionReviewLoopPass{
		OrgID:     orgID,
		SessionID: sessionID,
		PassIndex: 1,
		Status:    models.ReviewLoopPassStatusReviewing,
	}
	if err := s.store.CreateLoopWithInitialPass(ctx, loop, pass); err != nil {
		if isRunningLoopConflict(err) {
			return nil, ErrReviewLoopAlreadyRunning
		}
		return nil, err
	}
	msg, err := s.sendReview(ctx, loop, pass, req.StartedByUserID)
	if err != nil {
		return nil, errors.Join(err, s.failLoop(ctx, orgID, *loop, pass.PassIndex, fmt.Sprintf("failed to start review pass: %s", err)))
	}
	if err := s.store.SetPassReviewMessage(ctx, orgID, pass.ID, msg.ID); err != nil {
		return nil, err
	}
	return loop, nil
}

func isRunningLoopConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		strings.Contains(pgErr.ConstraintName, "session_review_loops_one_running_per_session")
}

func (s *Service) OnThreadTurnComplete(ctx context.Context, orgID, threadID uuid.UUID, assistantSummary string) error {
	return s.OnThreadTurnCompleteWithChange(ctx, orgID, threadID, assistantSummary, false)
}

// OnThreadTurnCompleteWithChange advances a review loop while retaining the
// per-turn mutation signal. A final review pass that writes files is not clean
// evidence and must stop for human attention rather than publishing it.
func (s *Service) OnThreadTurnCompleteWithChange(ctx context.Context, orgID, threadID uuid.UUID, assistantSummary string, workspaceChanged bool) error {
	loop, err := s.store.GetRunningLoopByThread(ctx, orgID, threadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoRunningReviewLoop
		}
		return err
	}
	pass, err := s.store.GetLatestPass(ctx, orgID, loop.ID)
	if err != nil {
		return err
	}
	if loop.Source == models.ReviewLoopSourcePublication && workspaceChanged {
		s.metrics.RecordFix(ctx)
	}
	summary := strings.TrimSpace(assistantSummary)
	if workspaceChanged && pass.PassIndex >= loop.MaxPasses &&
		(pass.Status == models.ReviewLoopPassStatusReviewing || pass.Status == models.ReviewLoopPassStatusDeciding) {
		return s.markPassNeedsHumanDecision(
			ctx, orgID, loop.ID, pass.ID, models.ReviewLoopDecisionNeedsFix,
			"The final review pass changed the workspace; those changes have not been independently reviewed.", loop, pass.PassIndex,
		)
	}
	switch pass.Status {
	case models.ReviewLoopPassStatusReviewing:
		if workspaceChanged {
			return s.completeFixAndStartNextReview(ctx, orgID, loop, pass, summary)
		}
		decision, err := parseDecision(summary)
		if err == nil && decision == models.ReviewLoopDecisionClean {
			return s.markPassClean(ctx, orgID, loop, pass, decision, summary)
		}
		msg, err := s.sendPlain(ctx, loop, prompts.ReviewLoopDecisionPrompt(), nil, withContinuationDedupeKey(reviewLoopContinuationDedupeKey(loop.ID, pass.ID, "decision")))
		if err != nil {
			return errors.Join(err, s.failLoop(ctx, orgID, loop, pass.PassIndex, fmt.Sprintf("failed to request review decision: %s", err)))
		}
		return s.store.MarkPassDeciding(ctx, orgID, pass.ID, summary, msg.ID)
	case models.ReviewLoopPassStatusDeciding:
		if workspaceChanged {
			return s.completeFixAndStartNextReview(ctx, orgID, loop, pass, summary)
		}
		decision, err := parseDecision(summary)
		switch {
		case err == nil && decision == models.ReviewLoopDecisionClean:
			return s.markPassClean(ctx, orgID, loop, pass, decision, summary)
		case err == nil && decision == models.ReviewLoopDecisionNeedsFix:
			return s.startLegacyFixPass(ctx, orgID, loop, pass, decision)
		case summary == "":
			return errors.Join(ErrUnrecognizedDecision, s.failLoop(ctx, orgID, loop, pass.PassIndex, ErrUnrecognizedDecision.Error()))
		case isMalformedDecision(summary):
			return errors.Join(ErrUnrecognizedDecision, s.failLoop(ctx, orgID, loop, pass.PassIndex, ErrUnrecognizedDecision.Error()))
		default:
			return s.completeFixAndStartNextReview(ctx, orgID, loop, pass, summary)
		}
	case models.ReviewLoopPassStatusFixing:
		return s.completeFixAndStartNextReview(ctx, orgID, loop, pass, summary)
	default:
		return nil
	}
}

func (s *Service) startLegacyFixPass(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop, pass models.SessionReviewLoopPass, decision models.ReviewLoopDecision) error {
	if pass.PassIndex >= loop.MaxPasses {
		if err := s.markPassNeedsHumanDecision(ctx, orgID, loop.ID, pass.ID, decision, "Review pass limit reached with remaining issues.", loop, pass.PassIndex); err != nil {
			return err
		}
		return nil
	}
	msg, err := s.sendPlain(ctx, loop, prompts.ReviewLoopFixPrompt(prompts.ReviewLoopFixPromptData{FixMode: loop.FixMode}), nil, withContinuationDedupeKey(reviewLoopContinuationDedupeKey(loop.ID, pass.ID, "fix")))
	if err != nil {
		return errors.Join(err, s.failLoop(ctx, orgID, loop, pass.PassIndex, fmt.Sprintf("failed to start fix pass: %s", err)))
	}
	return s.store.MarkPassFixing(ctx, orgID, pass.ID, decision, msg.ID)
}

func (s *Service) completeFixAndStartNextReview(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop, pass models.SessionReviewLoopPass, summary string) error {
	if err := s.store.MarkPassFixComplete(ctx, orgID, pass.ID, summary); err != nil {
		return err
	}
	if pass.PassIndex >= loop.MaxPasses {
		terminalSummary := "Review pass limit reached after fixes; confirmation review is still needed."
		if err := s.markPassNeedsHumanDecision(ctx, orgID, loop.ID, pass.ID, models.ReviewLoopDecisionNeedsFix, terminalSummary, loop, pass.PassIndex); err != nil {
			return err
		}
		return nil
	}
	if loop.Source == models.ReviewLoopSourcePublication {
		workspaceRevision, desiredHeadSHA, err := s.refreshPublicationEvidence(ctx, orgID, loop, pass.PassIndex)
		if err != nil {
			return err
		}
		loop.WorkspaceRevision = &workspaceRevision
		loop.DesiredHeadSHA = &desiredHeadSHA
	}
	next := &models.SessionReviewLoopPass{
		OrgID:     orgID,
		LoopID:    loop.ID,
		SessionID: loop.SessionID,
		PassIndex: pass.PassIndex + 1,
		Status:    models.ReviewLoopPassStatusReviewing,
	}
	if err := s.store.CreatePass(ctx, next); err != nil {
		return errors.Join(err, s.failLoop(ctx, orgID, loop, pass.PassIndex, fmt.Sprintf("failed to create confirmation review pass: %s", err)))
	}
	msg, err := s.sendReview(ctx, &loop, next, nil, withContinuationDedupeKey(reviewLoopContinuationDedupeKey(loop.ID, next.ID, "review")))
	if err != nil {
		return errors.Join(err, s.failLoop(ctx, orgID, loop, next.PassIndex, fmt.Sprintf("failed to start confirmation review: %s", err)))
	}
	return s.store.SetPassReviewMessage(ctx, orgID, next.ID, msg.ID)
}

func (s *Service) OnThreadTurnFailed(ctx context.Context, orgID, threadID uuid.UUID, summary string) error {
	loop, err := s.store.GetRunningLoopByThread(ctx, orgID, threadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoRunningReviewLoop
		}
		return err
	}
	pass, passErr := s.store.GetLatestPass(ctx, orgID, loop.ID)
	if passErr != nil {
		return errors.Join(passErr, s.failLoop(ctx, orgID, loop, max(loop.CompletedPasses, 1), strings.TrimSpace(summary)))
	}
	return s.failLoop(ctx, orgID, loop, pass.PassIndex, strings.TrimSpace(summary))
}

func (s *Service) failLoop(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop, passCount int, summary string) error {
	var markErr error
	if isLegacyAutomationLoop(loop) {
		payload, dedupeKey, targetErr := s.automationOpenPRTarget(ctx, orgID, loop)
		if targetErr != nil {
			return targetErr
		}
		markErr = s.store.MarkLoopFailedAndEnqueueOpenPR(ctx, orgID, loop.ID, summary, payload, dedupeKey)
	} else {
		markErr = s.store.MarkLoopFailed(ctx, orgID, loop.ID, summary)
	}
	if markErr == nil && loop.Source == models.ReviewLoopSourcePublication {
		s.metrics.RecordLoop(ctx, "failed", max(passCount, 1))
	}
	return markErr
}

func (s *Service) markPassClean(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop, pass models.SessionReviewLoopPass, decision models.ReviewLoopDecision, summary string) error {
	var markErr error
	if isLegacyAutomationLoop(loop) {
		payload, dedupeKey, err := s.automationOpenPRTarget(ctx, orgID, loop)
		if err != nil {
			return err
		}
		markErr = s.store.MarkPassCleanAndEnqueueOpenPR(ctx, orgID, loop.ID, pass.ID, decision, summary, payload, dedupeKey)
	} else if loop.Source == models.ReviewLoopSourcePublication {
		if _, _, err := s.refreshPublicationEvidence(ctx, orgID, loop, pass.PassIndex); err != nil {
			return err
		}
		markErr = s.store.MarkPassClean(ctx, orgID, loop.ID, pass.ID, decision, summary)
	} else {
		markErr = s.store.MarkPassClean(ctx, orgID, loop.ID, pass.ID, decision, summary)
	}
	if markErr == nil && loop.Source == models.ReviewLoopSourcePublication {
		s.metrics.RecordLoop(ctx, "clean", pass.PassIndex)
	}
	return markErr
}

func (s *Service) refreshPublicationEvidence(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop, passCount int) (int64, string, error) {
	if s.evidenceRefresher == nil {
		failure := errors.New("publication review evidence refresher is unavailable")
		return 0, "", errors.Join(failure, s.failLoop(ctx, orgID, loop, passCount, failure.Error()))
	}
	workspaceRevision, desiredHeadSHA, err := s.evidenceRefresher.RefreshPublicationEvidence(ctx, loop)
	if err != nil {
		return 0, "", errors.Join(err, s.failLoop(ctx, orgID, loop, passCount, fmt.Sprintf("failed to refresh publication review evidence: %s", err)))
	}
	evidenceStore, ok := s.store.(interface {
		RefreshPublicationEvidence(context.Context, uuid.UUID, uuid.UUID, int64, string) error
	})
	if !ok {
		failure := errors.New("publication review evidence store is unavailable")
		return 0, "", errors.Join(failure, s.failLoop(ctx, orgID, loop, passCount, failure.Error()))
	}
	if err := evidenceStore.RefreshPublicationEvidence(ctx, orgID, loop.ID, workspaceRevision, desiredHeadSHA); err != nil {
		return 0, "", errors.Join(err, s.failLoop(ctx, orgID, loop, passCount, fmt.Sprintf("failed to persist refreshed publication review evidence: %s", err)))
	}
	return workspaceRevision, desiredHeadSHA, nil
}

// passCount is the index of the pass that stopped for a human, not the loop's
// configured ceiling: a replayed or restarted loop can stop past MaxPasses, and
// the outcome telemetry has to describe the work that actually ran.
func (s *Service) markPassNeedsHumanDecision(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string, loop models.SessionReviewLoop, passCount int) error {
	var markErr error
	if isLegacyAutomationLoop(loop) {
		payload, dedupeKey, targetErr := s.automationOpenPRTarget(ctx, orgID, loop)
		if targetErr != nil {
			return targetErr
		}
		markErr = s.store.MarkPassNeedsHumanDecisionAndEnqueueOpenPR(ctx, orgID, loopID, passID, decision, summary, payload, dedupeKey)
	} else {
		markErr = s.store.MarkPassNeedsHumanDecision(ctx, orgID, loopID, passID, decision, summary)
	}
	if markErr == nil && loop.Source == models.ReviewLoopSourcePublication {
		s.metrics.RecordLoop(ctx, "needs_human", max(passCount, 1))
	}
	return markErr
}

func isLegacyAutomationLoop(loop models.SessionReviewLoop) bool {
	return loop.AutomationRunID != nil && loop.Source != models.ReviewLoopSourcePublication
}

func (s *Service) automationOpenPRTarget(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop) (map[string]any, string, error) {
	changesetID, err := s.store.GetPrimaryChangesetID(ctx, orgID, loop.SessionID)
	if err != nil {
		return nil, "", fmt.Errorf("resolve automation review primary changeset: %w", err)
	}
	return automationOpenPRPayload(loop, changesetID), automationOpenPRDedupeKey(changesetID), nil
}

func automationOpenPRPayload(loop models.SessionReviewLoop, changesetID uuid.UUID) map[string]any {
	return map[string]any{
		"session_id":         loop.SessionID.String(),
		"changeset_id":       changesetID.String(),
		"org_id":             loop.OrgID.String(),
		"publication_source": string(models.SessionPublicationSourceAutomation),
		"publication_queue":  string(models.SessionPublicationJobQueueDefault),
	}
}

func automationOpenPRDedupeKey(changesetID uuid.UUID) string {
	return db.OpenPRDedupeKey(changesetID)
}

type sendOption func(*threadsvc.SendMessageInput)

func withContinuationDedupeKey(key string) sendOption {
	return func(input *threadsvc.SendMessageInput) {
		input.ContinuationDedupeKeyOverride = &key
	}
}

func reviewLoopContinuationDedupeKey(loopID, passID uuid.UUID, phase string) string {
	return fmt.Sprintf("continue_session_review_loop:%s:%s:%s", loopID.String(), passID.String(), phase)
}

func (s *Service) sendReview(ctx context.Context, loop *models.SessionReviewLoop, pass *models.SessionReviewLoopPass, userID *uuid.UUID, opts ...sendOption) (*models.SessionMessage, error) {
	reviewPrompt := prompts.ReviewLoopReviewPrompt(prompts.ReviewLoopReviewPromptData{
		AgentType: loop.AgentType,
		FixMode:   loop.FixMode,
	})
	if !agentHasBuiltinReviewCommand(loop.AgentType) {
		return s.sendPlain(ctx, *loop, naturalLanguageReviewPrompt(reviewPrompt), userID, opts...)
	}
	arguments := strings.TrimPrefix(reviewPrompt, "/review")
	arguments = strings.TrimSpace(arguments)
	command := models.SessionInputCommand{
		Kind:      "command",
		AgentType: loop.AgentType,
		Name:      "review",
		Token:     "/review",
		Display:   "/review",
		Arguments: arguments,
		Source:    models.SessionInputCommandSourceBuiltin,
	}
	return s.sendPlain(ctx, *loop, reviewPrompt, userID, append(opts, withCommands(command))...)
}

func agentHasBuiltinReviewCommand(agentType models.AgentType) bool {
	for _, command := range models.SlashCommandsForAgent(agentType) {
		if command.Name == "review" {
			return true
		}
	}
	return false
}

func naturalLanguageReviewPrompt(reviewPrompt string) string {
	trimmed := strings.TrimSpace(reviewPrompt)
	arguments := strings.TrimSpace(strings.TrimPrefix(trimmed, "/review"))
	if arguments == trimmed {
		return trimmed
	}
	if arguments == "" {
		return "Review the current workspace diff."
	}
	return "Review " + arguments
}

func withCommands(commands ...models.SessionInputCommand) sendOption {
	return func(input *threadsvc.SendMessageInput) {
		input.Commands = commands
	}
}

func (s *Service) sendPlain(ctx context.Context, loop models.SessionReviewLoop, message string, userID *uuid.UUID, opts ...sendOption) (*models.SessionMessage, error) {
	if loop.ThreadID == nil {
		return nil, fmt.Errorf("review loop has no thread")
	}
	var targetChangesetID *uuid.UUID
	if loop.ChangesetID != nil {
		primaryChangesetID, err := s.store.GetPrimaryChangesetID(ctx, loop.OrgID, loop.SessionID)
		if err != nil {
			return nil, fmt.Errorf("resolve primary changeset for review message: %w", err)
		}
		// The primary changeset is represented by the session workspace and has
		// no materialized worktree. Only stack children need an explicit target.
		if *loop.ChangesetID != primaryChangesetID {
			targetChangesetID = loop.ChangesetID
		}
	}
	input := threadsvc.SendMessageInput{
		SessionID:   loop.SessionID,
		OrgID:       loop.OrgID,
		ThreadID:    *loop.ThreadID,
		ChangesetID: targetChangesetID,
		UserID:      userID,
		Message:     message,
	}
	for _, opt := range opts {
		opt(&input)
	}
	result, err := s.runtime.SendMessage(ctx, input)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Message == nil {
		return nil, fmt.Errorf("review loop message was not created")
	}
	return result.Message, nil
}

func parseDecision(summary string) (models.ReviewLoopDecision, error) {
	hasClean := containsDecisionSentinel(summary, models.ReviewLoopDecisionClean)
	hasNeedsFix := containsDecisionSentinel(summary, models.ReviewLoopDecisionNeedsFix)
	switch {
	case hasClean && !hasNeedsFix:
		return models.ReviewLoopDecisionClean, nil
	case hasNeedsFix && !hasClean:
		return models.ReviewLoopDecisionNeedsFix, nil
	default:
		return "", ErrUnrecognizedDecision
	}
}

func containsDecisionSentinel(summary string, decision models.ReviewLoopDecision) bool {
	sentinel := string(decision)
	lines := strings.Split(strings.ReplaceAll(summary, "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == sentinel {
			return true
		}
		if i == 0 && strings.HasPrefix(trimmed, sentinel) && isDecisionDirectiveBoundary(trimmed, len(sentinel)) {
			return true
		}
	}
	return false
}

func isDecisionDirectiveBoundary(line string, sentinelLen int) bool {
	if len(line) == sentinelLen {
		return true
	}
	switch line[sentinelLen] {
	case ':', '-', ' ', '\t':
		return true
	default:
		return false
	}
}

func isMalformedDecision(summary string) bool {
	trimmed := strings.TrimSpace(summary)
	return strings.HasPrefix(trimmed, string(models.ReviewLoopDecisionClean)) ||
		strings.HasPrefix(trimmed, string(models.ReviewLoopDecisionNeedsFix))
}

func reviewThreadLabel(agentType models.AgentType) string {
	switch agentType {
	case models.AgentTypeClaudeCode:
		return "Claude Review"
	case models.AgentTypeCodex:
		return "Codex Review"
	default:
		return "Review"
	}
}
