package humaninput

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestService_AnswerSyncsCompatibilityQuestionAndEnqueuesResume(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	requestID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	choice := models.HumanInputChoice{ID: "react", Label: "React"}
	repo := &fakeRepository{
		session: models.Session{
			ID:           sessionID,
			OrgID:        orgID,
			Status:       string(models.SessionStatusAwaitingInput),
			CurrentTurn:  4,
			SandboxState: string(models.SandboxStateRunning),
		},
		request: models.HumanInputRequest{
			ID:        requestID,
			OrgID:     orgID,
			SessionID: sessionID,
			Kind:      models.HumanInputRequestKindSingleChoice,
			Status:    models.HumanInputRequestStatusPending,
			Title:     "Framework",
			Body:      "Which framework?",
			Choices:   []models.HumanInputChoice{choice},
			CreatedAt: time.Now(),
		},
		claimedSession: models.Session{
			ID:          sessionID,
			OrgID:       orgID,
			Status:      string(models.SessionStatusRunning),
			CurrentTurn: 4,
		},
		jobID: jobID,
	}

	svc := New(repo, zerolog.Nop())
	result, err := svc.Answer(context.Background(), AnswerInput{
		OrgID:     orgID,
		SessionID: sessionID,
		RequestID: requestID,
		UserID:    userID,
		Answer: models.HumanInputAnswerInput{
			SelectedChoiceIDs: []string{"react"},
		},
	})

	require.NoError(t, err, "Answer should accept a valid single-choice response")
	require.Equal(t, models.HumanInputRequestStatusAnswered, result.Request.Status, "Answer should mark the request answered")
	require.Equal(t, "React", *result.Request.AnswerText, "Answer should persist the selected choice label as answer text")
	require.Equal(t, "Which framework?", repo.compatibilityQuestionText, "Answer should target the matching compatibility question")
	require.Equal(t, "React", repo.compatibilityAnswerText, "Answer should close the compatibility question with the user-visible answer")
	require.Equal(t, requestID, repo.enqueuedHumanInputRequestID, "Answer should enqueue a provider resume with the answered request id")
	require.Equal(t, jobID, repo.notifiedJobID, "Answer should notify the queued resume job after commit")
	require.True(t, repo.committed, "Answer should commit the transaction")
}

func TestService_CancelDeniesProviderAndEnqueuesResume(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	requestID := uuid.New()
	userID := uuid.New()
	providerRequestID := "toolu_123"
	repo := &fakeRepository{
		session: models.Session{
			ID:           sessionID,
			OrgID:        orgID,
			Status:       string(models.SessionStatusAwaitingInput),
			CurrentTurn:  2,
			SandboxState: string(models.SandboxStateRunning),
		},
		request: models.HumanInputRequest{
			ID:                requestID,
			OrgID:             orgID,
			SessionID:         sessionID,
			ProviderRequestID: &providerRequestID,
			Kind:              models.HumanInputRequestKindToolApproval,
			Status:            models.HumanInputRequestStatusPending,
			Title:             "Approve Bash?",
			Body:              "Claude needs approval before it can continue.",
			Choices: []models.HumanInputChoice{
				{ID: "approve", Label: "Approve"},
				{ID: "deny", Label: "Deny"},
			},
			CreatedAt: time.Now(),
		},
		claimedSession: models.Session{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning), CurrentTurn: 2},
		jobID:          uuid.New(),
	}

	svc := New(repo, zerolog.Nop())
	result, err := svc.Cancel(context.Background(), CancelInput{
		OrgID:     orgID,
		SessionID: sessionID,
		RequestID: requestID,
		UserID:    userID,
	})

	require.NoError(t, err, "Cancel should convert a pending provider prompt into a denial resume")
	require.Equal(t, models.HumanInputRequestStatusCancelled, result.Request.Status, "Cancel should mark the request cancelled")
	require.Equal(t, requestID, repo.enqueuedHumanInputRequestID, "Cancel should enqueue a provider resume with the cancelled request id")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(repo.cancelPayload, &payload), "Cancel should persist a structured denial payload")
	require.Equal(t, "deny", payload["decision"], "Cancel should deny provider-side approval prompts")
	require.Equal(t, true, payload["cancelled"], "Cancel should preserve cancellation intent in the payload")
}

type fakeRepository struct {
	session        models.Session
	request        models.HumanInputRequest
	claimedSession models.Session
	claimedThread  models.SessionThread
	jobID          uuid.UUID

	listed bool

	answerPayload json.RawMessage
	cancelPayload json.RawMessage

	compatibilityQuestionText string
	compatibilityAnswerText   string
	skippedQuestionText       string

	enqueuedHumanInputRequestID uuid.UUID
	notifiedJobID               uuid.UUID
	committed                   bool
}

func (r *fakeRepository) GetSession(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return r.session, nil
}

func (r *fakeRepository) ListRequests(context.Context, uuid.UUID, uuid.UUID, RequestFilters) ([]models.HumanInputRequest, error) {
	r.listed = true
	return []models.HumanInputRequest{r.request}, nil
}

func (r *fakeRepository) WithTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	tx := &fakeTx{repo: r}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	r.committed = true
	return nil
}

func (r *fakeRepository) NotifyJob(_ context.Context, jobID uuid.UUID) {
	r.notifiedJobID = jobID
}

type fakeTx struct {
	repo *fakeRepository
}

func (tx *fakeTx) GetSession(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return tx.repo.session, nil
}

func (tx *fakeTx) GetRequestForUpdate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.HumanInputRequest, error) {
	return tx.repo.request, nil
}

func (tx *fakeTx) AnswerRequest(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, answerText *string, answerPayload json.RawMessage, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	tx.repo.answerPayload = append(json.RawMessage(nil), answerPayload...)
	req := tx.repo.request
	req.Status = models.HumanInputRequestStatusAnswered
	req.AnswerText = answerText
	req.AnswerPayload = answerPayload
	req.AnsweredBy = &answeredBy
	now := time.Now()
	req.AnsweredAt = &now
	tx.repo.request = req
	return req, nil
}

func (tx *fakeTx) CancelRequest(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, answerText *string, answerPayload json.RawMessage, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	tx.repo.cancelPayload = append(json.RawMessage(nil), answerPayload...)
	req := tx.repo.request
	req.Status = models.HumanInputRequestStatusCancelled
	req.AnswerText = answerText
	req.AnswerPayload = answerPayload
	req.AnsweredBy = &answeredBy
	now := time.Now()
	req.AnsweredAt = &now
	tx.repo.request = req
	return req, nil
}

func (tx *fakeTx) AnswerCompatibilityQuestion(_ context.Context, _ uuid.UUID, _ uuid.UUID, questionText, answerText string, _ uuid.UUID) error {
	tx.repo.compatibilityQuestionText = questionText
	tx.repo.compatibilityAnswerText = answerText
	return nil
}

func (tx *fakeTx) SkipCompatibilityQuestion(_ context.Context, _ uuid.UUID, _ uuid.UUID, questionText string) error {
	tx.repo.skippedQuestionText = questionText
	return nil
}

func (tx *fakeTx) ClaimSessionForResume(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return tx.repo.claimedSession, nil
}

func (tx *fakeTx) ClaimThreadForResume(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
	if tx.repo.claimedThread.ID == uuid.Nil {
		return models.SessionThread{}, pgx.ErrNoRows
	}
	return tx.repo.claimedThread, nil
}

func (tx *fakeTx) CreateMessage(_ context.Context, msg *models.SessionMessage) error {
	msg.ID = 42
	msg.CreatedAt = time.Now()
	return nil
}

func (tx *fakeTx) EnqueueContinue(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, _ *models.Session, humanInputRequestID uuid.UUID) (uuid.UUID, error) {
	tx.repo.enqueuedHumanInputRequestID = humanInputRequestID
	return tx.repo.jobID, nil
}
