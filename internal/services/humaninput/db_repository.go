package humaninput

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type dbRepository struct {
	sessions  *db.SessionStore
	requests  *db.SessionHumanInputRequestStore
	questions *db.SessionQuestionStore
	messages  *db.SessionMessageStore
	threads   *db.SessionThreadStore
	jobs      *db.JobStore
}

func (r *dbRepository) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	return r.sessions.GetByID(ctx, orgID, sessionID)
}

func (r *dbRepository) ListRequests(ctx context.Context, orgID, sessionID uuid.UUID, filters RequestFilters) ([]models.HumanInputRequest, error) {
	return r.requests.ListBySession(ctx, orgID, sessionID, filters)
}

func (r *dbRepository) WithTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	tx, err := r.sessions.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin human input transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			zerolog.Ctx(ctx).Warn().Err(rollbackErr).Msg("failed to rollback human input transaction")
		}
	}()

	if err := fn(ctx, newDBTx(tx, r.jobs)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit human input transaction: %w", err)
	}
	committed = true
	return nil
}

func (r *dbRepository) NotifyJob(ctx context.Context, jobID uuid.UUID) {
	r.jobs.Notify(ctx, jobID)
}

type dbTx struct {
	tx        pgx.Tx
	sessions  *db.SessionStore
	requests  *db.SessionHumanInputRequestStore
	questions *db.SessionQuestionStore
	messages  *db.SessionMessageStore
	threads   *db.SessionThreadStore
	jobs      *db.JobStore
}

func newDBTx(tx pgx.Tx, jobs *db.JobStore) *dbTx {
	return &dbTx{
		tx:        tx,
		sessions:  db.NewSessionStore(tx),
		requests:  db.NewSessionHumanInputRequestStore(tx),
		questions: db.NewSessionQuestionStore(tx),
		messages:  db.NewSessionMessageStore(tx),
		threads:   db.NewSessionThreadStore(tx),
		jobs:      jobs,
	}
}

func (t *dbTx) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	return t.sessions.GetByID(ctx, orgID, sessionID)
}

func (t *dbTx) GetRequestForUpdate(ctx context.Context, orgID, sessionID, requestID uuid.UUID) (models.HumanInputRequest, error) {
	return t.requests.GetByIDForUpdate(ctx, orgID, sessionID, requestID)
}

func (t *dbTx) AnswerRequest(ctx context.Context, orgID, sessionID, requestID uuid.UUID, answerText *string, answerPayload json.RawMessage, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	return t.requests.AnswerPending(ctx, orgID, sessionID, requestID, answerText, answerPayload, answeredBy)
}

func (t *dbTx) CancelRequest(ctx context.Context, orgID, sessionID, requestID uuid.UUID, answerText *string, answerPayload json.RawMessage, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	return t.requests.CancelPending(ctx, orgID, sessionID, requestID, answerText, answerPayload, answeredBy)
}

func (t *dbTx) AnswerCompatibilityQuestion(ctx context.Context, orgID, sessionID uuid.UUID, questionText, answerText string, answeredBy uuid.UUID) error {
	_, err := t.questions.AnswerLatestPendingBySessionAndQuestion(ctx, orgID, sessionID, questionText, answerText, answeredBy)
	return err
}

func (t *dbTx) SkipCompatibilityQuestion(ctx context.Context, orgID, sessionID uuid.UUID, questionText string) error {
	return t.questions.SkipLatestPendingBySessionAndQuestion(ctx, orgID, sessionID, questionText)
}

func (t *dbTx) ClaimSessionForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	claimed, err := t.sessions.ClaimForResume(ctx, orgID, sessionID)
	if err == nil {
		return claimed, nil
	}
	return t.sessions.ClaimIdle(ctx, orgID, sessionID)
}

func (t *dbTx) ClaimThreadForResume(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	claimed, err := t.threads.ClaimForResumeInSession(ctx, orgID, sessionID, threadID, models.MaxRunningThreadsPerSession)
	if err == nil {
		return claimed, nil
	}
	if err == db.ErrThreadRunningLimitReached {
		return models.SessionThread{}, err
	}
	return t.threads.ClaimIdleForSession(ctx, orgID, sessionID, threadID, models.MaxRunningThreadsPerSession)
}

func (t *dbTx) CreateMessage(ctx context.Context, msg *models.SessionMessage) error {
	return t.messages.Create(ctx, msg)
}

func (t *dbTx) EnqueueContinue(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, target *models.Session, humanInputRequestID uuid.UUID) (uuid.UUID, error) {
	scopeID := sessionID
	payload := map[string]string{
		"session_id":                 sessionID.String(),
		"org_id":                     orgID.String(),
		"human_input_request_id":     humanInputRequestID.String(),
		"human_input_request_status": "ready",
	}
	if threadID != nil {
		scopeID = *threadID
		payload["thread_id"] = threadID.String()
	}
	dedupeKey := db.ContinueSessionDedupeKey(scopeID)
	return t.jobs.EnqueueInTxWithOpts(ctx, t.tx, orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "continue_session",
		Payload:      payload,
		Priority:     5,
		DedupeKey:    &dedupeKey,
		TargetNodeID: models.SessionWorkerTarget(target),
	})
}
