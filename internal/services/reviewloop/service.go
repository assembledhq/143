package reviewloop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

type Store interface {
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
	store   Store
	runtime Runtime
}

type Option func(*Service)

func NewService(store Store, runtime Runtime, opts ...Option) *Service {
	s := &Service{store: store, runtime: runtime}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type StartReviewLoopRequest struct {
	AgentType       models.AgentType
	Model           string
	MaxPasses       int
	FixMode         models.ReviewLoopFixMode
	Source          models.ReviewLoopSource
	AutomationRunID *uuid.UUID
	StartedByUserID *uuid.UUID
	ReviewRequired  bool
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
		OrgID:           orgID,
		SessionID:       sessionID,
		AutomationRunID: req.AutomationRunID,
		ThreadID:        &thread.ID,
		Status:          models.ReviewLoopStatusRunning,
		Source:          source,
		AgentType:       agentType,
		MaxPasses:       req.MaxPasses,
		FixMode:         fixMode,
		ReviewRequired:  req.ReviewRequired,
		StartedByUserID: req.StartedByUserID,
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
		_ = s.failLoop(ctx, orgID, *loop, fmt.Sprintf("failed to start review pass: %s", err))
		return nil, err
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
	summary := strings.TrimSpace(assistantSummary)
	switch pass.Status {
	case models.ReviewLoopPassStatusReviewing:
		msg, err := s.sendPlain(ctx, loop, prompts.ReviewLoopDecisionPrompt(), nil, withContinuationDedupeKey(reviewLoopContinuationDedupeKey(loop.ID, pass.ID, "decision")))
		if err != nil {
			_ = s.failLoop(ctx, orgID, loop, fmt.Sprintf("failed to request review decision: %s", err))
			return err
		}
		return s.store.MarkPassDeciding(ctx, orgID, pass.ID, summary, msg.ID)
	case models.ReviewLoopPassStatusDeciding:
		decision, err := parseDecision(summary)
		switch {
		case err == nil && decision == models.ReviewLoopDecisionClean:
			if loop.AutomationRunID != nil {
				return s.store.MarkPassCleanAndEnqueueOpenPR(ctx, orgID, loop.ID, pass.ID, decision, summary, automationOpenPRPayload(loop), automationOpenPRDedupeKey(loop.SessionID))
			}
			if err := s.store.MarkPassClean(ctx, orgID, loop.ID, pass.ID, decision, summary); err != nil {
				return err
			}
			return nil
		case err == nil && decision == models.ReviewLoopDecisionNeedsFix:
			return s.startLegacyFixPass(ctx, orgID, loop, pass, decision)
		case isMalformedDecision(summary):
			_ = s.failLoop(ctx, orgID, loop, ErrUnrecognizedDecision.Error())
			return ErrUnrecognizedDecision
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
		if loop.AutomationRunID != nil {
			return s.store.MarkPassNeedsHumanDecisionAndEnqueueOpenPR(ctx, orgID, loop.ID, pass.ID, decision, "Review pass limit reached with remaining issues.", automationOpenPRPayload(loop), automationOpenPRDedupeKey(loop.SessionID))
		}
		if err := s.store.MarkPassNeedsHumanDecision(ctx, orgID, loop.ID, pass.ID, decision, "Review pass limit reached with remaining issues."); err != nil {
			return err
		}
		return nil
	}
	msg, err := s.sendPlain(ctx, loop, prompts.ReviewLoopFixPrompt(prompts.ReviewLoopFixPromptData{FixMode: loop.FixMode}), nil, withContinuationDedupeKey(reviewLoopContinuationDedupeKey(loop.ID, pass.ID, "fix")))
	if err != nil {
		_ = s.failLoop(ctx, orgID, loop, fmt.Sprintf("failed to start fix pass: %s", err))
		return err
	}
	return s.store.MarkPassFixing(ctx, orgID, pass.ID, decision, msg.ID)
}

func (s *Service) completeFixAndStartNextReview(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop, pass models.SessionReviewLoopPass, summary string) error {
	if err := s.store.MarkPassFixComplete(ctx, orgID, pass.ID, summary); err != nil {
		return err
	}
	if pass.PassIndex >= loop.MaxPasses {
		terminalSummary := "Review pass limit reached after fixes; confirmation review is still needed."
		if loop.AutomationRunID != nil {
			return s.store.MarkPassNeedsHumanDecisionAndEnqueueOpenPR(ctx, orgID, loop.ID, pass.ID, models.ReviewLoopDecisionNeedsFix, terminalSummary, automationOpenPRPayload(loop), automationOpenPRDedupeKey(loop.SessionID))
		}
		if err := s.store.MarkPassNeedsHumanDecision(ctx, orgID, loop.ID, pass.ID, models.ReviewLoopDecisionNeedsFix, terminalSummary); err != nil {
			return err
		}
		return nil
	}
	next := &models.SessionReviewLoopPass{
		OrgID:     orgID,
		LoopID:    loop.ID,
		SessionID: loop.SessionID,
		PassIndex: pass.PassIndex + 1,
		Status:    models.ReviewLoopPassStatusReviewing,
	}
	if err := s.store.CreatePass(ctx, next); err != nil {
		return err
	}
	msg, err := s.sendReview(ctx, &loop, next, nil, withContinuationDedupeKey(reviewLoopContinuationDedupeKey(loop.ID, next.ID, "review")))
	if err != nil {
		_ = s.failLoop(ctx, orgID, loop, fmt.Sprintf("failed to start confirmation review: %s", err))
		return err
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
	return s.failLoop(ctx, orgID, loop, strings.TrimSpace(summary))
}

func (s *Service) failLoop(ctx context.Context, orgID uuid.UUID, loop models.SessionReviewLoop, summary string) error {
	if loop.AutomationRunID != nil {
		return s.store.MarkLoopFailedAndEnqueueOpenPR(ctx, orgID, loop.ID, summary, automationOpenPRPayload(loop), automationOpenPRDedupeKey(loop.SessionID))
	}
	return s.store.MarkLoopFailed(ctx, orgID, loop.ID, summary)
}

func automationOpenPRPayload(loop models.SessionReviewLoop) map[string]any {
	return map[string]any{
		"session_id": loop.SessionID.String(),
		"org_id":     loop.OrgID.String(),
	}
}

func automationOpenPRDedupeKey(sessionID uuid.UUID) string {
	return "open_pr:review_loop:" + sessionID.String()
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

func withCommands(commands ...models.SessionInputCommand) sendOption {
	return func(input *threadsvc.SendMessageInput) {
		input.Commands = commands
	}
}

func (s *Service) sendPlain(ctx context.Context, loop models.SessionReviewLoop, message string, userID *uuid.UUID, opts ...sendOption) (*models.SessionMessage, error) {
	if loop.ThreadID == nil {
		return nil, fmt.Errorf("review loop has no thread")
	}
	input := threadsvc.SendMessageInput{
		SessionID: loop.SessionID,
		OrgID:     loop.OrgID,
		ThreadID:  *loop.ThreadID,
		UserID:    userID,
		Message:   message,
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
	switch strings.TrimSpace(summary) {
	case string(models.ReviewLoopDecisionClean):
		return models.ReviewLoopDecisionClean, nil
	case string(models.ReviewLoopDecisionNeedsFix):
		return models.ReviewLoopDecisionNeedsFix, nil
	default:
		return "", ErrUnrecognizedDecision
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
