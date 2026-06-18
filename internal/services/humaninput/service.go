package humaninput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RequestFilters = db.HumanInputRequestFilters

var (
	ErrNotFound          = errors.New("human input request not found")
	ErrInvalidAnswer     = errors.New("invalid human input answer")
	ErrNotPending        = errors.New("human input request is not pending")
	ErrNotResumable      = errors.New("session is not resumable")
	ErrSnapshotExpired   = errors.New("session snapshot expired")
	ErrCheckpointPending = errors.New("session human input checkpoint is not ready")
	ErrRunningLimit      = errors.New("session running thread limit reached")
)

type Repository interface {
	GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	ListRequests(ctx context.Context, orgID, sessionID uuid.UUID, filters RequestFilters) ([]models.HumanInputRequest, error)
	WithTx(ctx context.Context, fn func(context.Context, Tx) error) error
	NotifyJob(ctx context.Context, jobID uuid.UUID)
}

type Tx interface {
	GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	GetRequestForUpdate(ctx context.Context, orgID, sessionID, requestID uuid.UUID) (models.HumanInputRequest, error)
	AnswerRequest(ctx context.Context, orgID, sessionID, requestID uuid.UUID, answerText *string, answerPayload json.RawMessage, answeredBy uuid.UUID) (models.HumanInputRequest, error)
	CancelRequest(ctx context.Context, orgID, sessionID, requestID uuid.UUID, answerText *string, answerPayload json.RawMessage, answeredBy uuid.UUID) (models.HumanInputRequest, error)
	AnswerCompatibilityQuestion(ctx context.Context, orgID, sessionID uuid.UUID, questionText, answerText string, answeredBy uuid.UUID) error
	SkipCompatibilityQuestion(ctx context.Context, orgID, sessionID uuid.UUID, questionText string) error
	ClaimSessionForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	ClaimThreadForResume(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	CreateMessage(ctx context.Context, msg *models.SessionMessage) error
	EnqueueContinue(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, target *models.Session, humanInputRequestID uuid.UUID) (uuid.UUID, error)
}

type Service struct {
	repo               Repository
	capabilityApprover CapabilityApprover
}

func New(repo Repository) *Service {
	return &Service{repo: repo}
}

type CapabilityApprover interface {
	ApplyApprovedGrant(ctx context.Context, orgID, sessionID, requestID uuid.UUID, capability models.AgentCapabilityID, accessLevel models.AgentCapabilityAccessLevel) ([]models.AgentCapabilitySnapshotItem, error)
}

func (s *Service) SetCapabilityApprover(approver CapabilityApprover) {
	s.capabilityApprover = approver
}

func NewDBService(
	sessionStore *db.SessionStore,
	requestStore *db.SessionHumanInputRequestStore,
	questionStore *db.SessionQuestionStore,
	messageStore *db.SessionMessageStore,
	threadStore *db.SessionThreadStore,
	jobStore *db.JobStore,
) *Service {
	return New(&dbRepository{
		sessions:  sessionStore,
		requests:  requestStore,
		questions: questionStore,
		messages:  messageStore,
		threads:   threadStore,
		jobs:      jobStore,
	})
}

type AnswerInput struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	RequestID uuid.UUID
	UserID    uuid.UUID
	Answer    models.HumanInputAnswerInput
}

type CancelInput struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	RequestID uuid.UUID
	UserID    uuid.UUID
}

type Result struct {
	Request models.HumanInputRequest
	Message *models.SessionMessage
	JobID   uuid.UUID
}

func (s *Service) List(ctx context.Context, orgID, sessionID uuid.UUID, filters RequestFilters) ([]models.HumanInputRequest, error) {
	if _, err := s.repo.GetSession(ctx, orgID, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.repo.ListRequests(ctx, orgID, sessionID, filters)
}

func (s *Service) Answer(ctx context.Context, input AnswerInput) (Result, error) {
	var result Result
	if err := s.repo.WithTx(ctx, func(txCtx context.Context, tx Tx) error {
		session, request, err := s.lockValidPendingRequest(txCtx, tx, input.OrgID, input.SessionID, input.RequestID)
		if err != nil {
			return err
		}
		if err := request.ValidateAnswer(input.Answer); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidAnswer, err)
		}

		answerPayload, err := db.MarshalHumanInputAnswerPayload(input.Answer)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidAnswer, err)
		}
		answerText := AnswerText(request, input.Answer)
		answered, err := tx.AnswerRequest(txCtx, input.OrgID, input.SessionID, input.RequestID, answerText, answerPayload, input.UserID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotPending
			}
			return err
		}
		if err := s.applyApprovedCapabilityGrant(txCtx, request, input); err != nil {
			return err
		}

		content := MessageContent(request, input.Answer)
		if LegacyQuestionCompatible(request.Kind) {
			if err := tx.AnswerCompatibilityQuestion(txCtx, input.OrgID, input.SessionID, request.Body, content, input.UserID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}

		claimedSession, messageTurn, err := claimForProviderResume(txCtx, tx, input.OrgID, input.SessionID, request.ThreadID)
		if err != nil {
			return err
		}
		if messageTurn == 0 {
			messageTurn = session.CurrentTurn + 1
		}

		msg := &models.SessionMessage{
			SessionID:  input.SessionID,
			OrgID:      input.OrgID,
			ThreadID:   request.ThreadID,
			UserID:     &input.UserID,
			TurnNumber: messageTurn,
			Role:       models.MessageRoleUser,
			Content:    content,
		}
		if err := tx.CreateMessage(txCtx, msg); err != nil {
			return err
		}
		jobID, err := tx.EnqueueContinue(txCtx, input.OrgID, input.SessionID, request.ThreadID, &claimedSession, answered.ID)
		if err != nil {
			return err
		}

		result = Result{Request: answered, Message: msg, JobID: jobID}
		return nil
	}); err != nil {
		return Result{}, err
	}
	s.repo.NotifyJob(ctx, result.JobID)
	return result, nil
}

func (s *Service) applyApprovedCapabilityGrant(ctx context.Context, request models.HumanInputRequest, input AnswerInput) error {
	if s.capabilityApprover == nil || !humanInputAnswerApproved(input.Answer) {
		return nil
	}
	var payload struct {
		Type         string                            `json:"type"`
		CapabilityID models.AgentCapabilityID          `json:"capability_id"`
		AccessLevel  models.AgentCapabilityAccessLevel `json:"access_level"`
	}
	if len(request.ProviderPayload) == 0 {
		return nil
	}
	if err := json.Unmarshal(request.ProviderPayload, &payload); err != nil {
		return nil
	}
	if payload.Type != "agent_capability_request" {
		return nil
	}
	_, err := s.capabilityApprover.ApplyApprovedGrant(ctx, input.OrgID, input.SessionID, input.RequestID, payload.CapabilityID, payload.AccessLevel)
	if errors.Is(err, db.ErrCapabilityAlreadyGranted) {
		return nil
	}
	return err
}

func humanInputAnswerApproved(input models.HumanInputAnswerInput) bool {
	for _, id := range input.SelectedChoiceIDs {
		if id == "approve" {
			return true
		}
	}
	if len(input.AnswerPayload) == 0 {
		return false
	}
	var payload struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(input.AnswerPayload, &payload); err != nil {
		return false
	}
	return payload.Decision == "approve"
}

func (s *Service) Cancel(ctx context.Context, input CancelInput) (Result, error) {
	var result Result
	if err := s.repo.WithTx(ctx, func(txCtx context.Context, tx Tx) error {
		_, request, err := s.lockValidPendingRequest(txCtx, tx, input.OrgID, input.SessionID, input.RequestID)
		if err != nil {
			return err
		}

		answerTextValue := "Cancelled human input request."
		answerText := &answerTextValue
		answerPayload, err := json.Marshal(map[string]any{
			"decision":  "deny",
			"cancelled": true,
		})
		if err != nil {
			return err
		}
		cancelled, err := tx.CancelRequest(txCtx, input.OrgID, input.SessionID, input.RequestID, answerText, answerPayload, input.UserID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotPending
			}
			return err
		}

		if LegacyQuestionCompatible(request.Kind) {
			if err := tx.SkipCompatibilityQuestion(txCtx, input.OrgID, input.SessionID, request.Body); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}

		claimedSession, messageTurn, err := claimForProviderResume(txCtx, tx, input.OrgID, input.SessionID, request.ThreadID)
		if err != nil {
			return err
		}
		msg := &models.SessionMessage{
			SessionID:  input.SessionID,
			OrgID:      input.OrgID,
			ThreadID:   request.ThreadID,
			UserID:     &input.UserID,
			TurnNumber: messageTurn,
			Role:       models.MessageRoleUser,
			Content:    answerTextValue,
		}
		if err := tx.CreateMessage(txCtx, msg); err != nil {
			return err
		}
		jobID, err := tx.EnqueueContinue(txCtx, input.OrgID, input.SessionID, request.ThreadID, &claimedSession, cancelled.ID)
		if err != nil {
			return err
		}

		result = Result{Request: cancelled, Message: msg, JobID: jobID}
		return nil
	}); err != nil {
		return Result{}, err
	}
	s.repo.NotifyJob(ctx, result.JobID)
	return result, nil
}

func (s *Service) lockValidPendingRequest(ctx context.Context, tx Tx, orgID, sessionID, requestID uuid.UUID) (models.Session, models.HumanInputRequest, error) {
	session, err := tx.GetSession(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Session{}, models.HumanInputRequest{}, ErrNotFound
		}
		return models.Session{}, models.HumanInputRequest{}, err
	}
	if session.SandboxState == models.SandboxStateDestroyed {
		return models.Session{}, models.HumanInputRequest{}, ErrSnapshotExpired
	}
	request, err := tx.GetRequestForUpdate(ctx, orgID, sessionID, requestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Session{}, models.HumanInputRequest{}, ErrNotFound
		}
		return models.Session{}, models.HumanInputRequest{}, err
	}
	if request.Status != models.HumanInputRequestStatusPending {
		return models.Session{}, models.HumanInputRequest{}, ErrNotPending
	}
	if err := validateCheckpointReady(session); err != nil {
		return models.Session{}, models.HumanInputRequest{}, err
	}
	return session, request, nil
}

func validateCheckpointReady(session models.Session) error {
	if models.SessionStatus(session.Status) != models.SessionStatusAwaitingInput {
		return ErrCheckpointPending
	}
	if session.PendingSnapshotKey != nil && strings.TrimSpace(*session.PendingSnapshotKey) != "" {
		return ErrCheckpointPending
	}
	if session.SnapshotKey == nil || strings.TrimSpace(*session.SnapshotKey) == "" {
		return ErrCheckpointPending
	}
	return nil
}

func claimForProviderResume(ctx context.Context, tx Tx, orgID, sessionID uuid.UUID, threadID *uuid.UUID) (models.Session, int, error) {
	claimedSession, err := tx.ClaimSessionForResume(ctx, orgID, sessionID)
	if err != nil {
		return models.Session{}, 0, ErrNotResumable
	}
	messageTurn := claimedSession.CurrentTurn + 1
	if threadID == nil {
		return claimedSession, messageTurn, nil
	}
	thread, err := tx.ClaimThreadForResume(ctx, orgID, sessionID, *threadID)
	if err != nil {
		if errors.Is(err, db.ErrThreadRunningLimitReached) {
			return models.Session{}, 0, ErrRunningLimit
		}
		return models.Session{}, 0, ErrNotResumable
	}
	return claimedSession, thread.CurrentTurn + 1, nil
}

func LegacyQuestionCompatible(kind models.HumanInputRequestKind) bool {
	switch kind {
	case "", models.HumanInputRequestKindFreeText, models.HumanInputRequestKindSingleChoice, models.HumanInputRequestKindMultiChoice:
		return true
	default:
		return false
	}
}

func AnswerText(req models.HumanInputRequest, input models.HumanInputAnswerInput) *string {
	if input.AnswerText != nil && strings.TrimSpace(*input.AnswerText) != "" {
		trimmed := strings.TrimSpace(*input.AnswerText)
		return &trimmed
	}
	labels := selectedLabels(req, input.SelectedChoiceIDs)
	if len(labels) == 0 {
		return nil
	}
	joined := strings.Join(labels, ", ")
	return &joined
}

func MessageContent(req models.HumanInputRequest, input models.HumanInputAnswerInput) string {
	if answerText := AnswerText(req, input); answerText != nil {
		return *answerText
	}
	if len(input.AnswerPayload) > 0 {
		return "Answered human input request."
	}
	return strings.TrimSpace(req.Body)
}

func selectedLabels(req models.HumanInputRequest, selectedIDs []string) []string {
	if len(selectedIDs) == 0 {
		return nil
	}
	labelsByID := make(map[string]string, len(req.Choices))
	for _, choice := range req.Choices {
		labelsByID[choice.ID] = choice.Label
	}
	labels := make([]string, 0, len(selectedIDs))
	for _, id := range selectedIDs {
		if label := labelsByID[id]; label != "" {
			labels = append(labels, label)
		} else {
			labels = append(labels, id)
		}
	}
	return labels
}
