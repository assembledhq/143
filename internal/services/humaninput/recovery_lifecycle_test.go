package humaninput

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// Each fixture owns its transaction and failure injection; no shared state or
// process environment is needed to exercise provider-resume failure paths.
type recoveryRepository struct {
	*fakeRepository
	failStage string
	failure   error
	calls     []string
	t         *testing.T
}

func newRecoveryRepository(t *testing.T) *recoveryRepository {
	t.Helper()
	orgID, sessionID, requestID, threadID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	snapshot := "durable-checkpoint"
	return &recoveryRepository{t: t, fakeRepository: &fakeRepository{
		session:        models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusAwaitingInput, SandboxState: models.SandboxStateRunning, SnapshotKey: &snapshot, CurrentTurn: 2},
		request:        models.HumanInputRequest{ID: requestID, OrgID: orgID, SessionID: sessionID, ThreadID: &threadID, Kind: models.HumanInputRequestKindFreeText, Status: models.HumanInputRequestStatusPending, Body: "Which approach?"},
		claimedSession: models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, CurrentTurn: 2},
		claimedThread:  models.SessionThread{ID: threadID, OrgID: orgID, SessionID: sessionID, CurrentTurn: 8},
		jobID:          uuid.New(),
	}}
}

func (r *recoveryRepository) step(stage string) error {
	r.calls = append(r.calls, stage)
	if r.failStage == stage {
		return r.failure
	}
	return nil
}

func (r *recoveryRepository) WithTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	if err := r.step("begin"); err != nil {
		return err
	}
	if err := fn(ctx, &recoveryTx{fakeTx: &fakeTx{repo: r.fakeRepository}, owner: r}); err != nil {
		return err
	}
	if err := r.step("commit"); err != nil {
		return err
	}
	r.committed = true
	return nil
}

func (r *recoveryRepository) NotifyJob(ctx context.Context, jobID uuid.UUID) {
	r.calls = append(r.calls, "notify")
	require.True(r.t, r.committed, "resume notification must follow transaction commit")
	r.fakeRepository.NotifyJob(ctx, jobID)
}

type recoveryTx struct {
	*fakeTx
	owner *recoveryRepository
}

func (tx *recoveryTx) checkIdentity(orgID, sessionID uuid.UUID) {
	require.Equal(tx.owner.t, []uuid.UUID{tx.owner.session.OrgID, tx.owner.session.ID}, []uuid.UUID{orgID, sessionID}, "every resume operation should retain tenant and session identity")
}

func (tx *recoveryTx) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	tx.checkIdentity(orgID, sessionID)
	if err := tx.owner.step("session"); err != nil {
		return models.Session{}, err
	}
	return tx.fakeTx.GetSession(ctx, orgID, sessionID)
}

func (tx *recoveryTx) GetRequestForUpdate(ctx context.Context, orgID, sessionID, requestID uuid.UUID) (models.HumanInputRequest, error) {
	tx.checkIdentity(orgID, sessionID)
	require.Equal(tx.owner.t, tx.owner.request.ID, requestID, "resume should lock only the requested human input")
	if err := tx.owner.step("request"); err != nil {
		return models.HumanInputRequest{}, err
	}
	return tx.fakeTx.GetRequestForUpdate(ctx, orgID, sessionID, requestID)
}

func (tx *recoveryTx) AnswerRequest(ctx context.Context, orgID, sessionID, requestID uuid.UUID, text *string, payload json.RawMessage, userID uuid.UUID) (models.HumanInputRequest, error) {
	tx.checkIdentity(orgID, sessionID)
	if err := tx.owner.step("answer"); err != nil {
		return models.HumanInputRequest{}, err
	}
	return tx.fakeTx.AnswerRequest(ctx, orgID, sessionID, requestID, text, payload, userID)
}

func (tx *recoveryTx) AppendApprovedCapabilityGrant(ctx context.Context, orgID, sessionID uuid.UUID, item models.AgentCapabilitySnapshotItem) ([]models.AgentCapabilitySnapshotItem, error) {
	tx.checkIdentity(orgID, sessionID)
	require.Equal(tx.owner.t, &tx.owner.request.ID, item.HumanInputRequestID, "transactional grant must bind the exact locked and answered request")
	if err := tx.owner.step("grant"); err != nil {
		return nil, err
	}
	return tx.fakeTx.AppendApprovedCapabilityGrant(ctx, orgID, sessionID, item)
}

func (tx *recoveryTx) CancelRequest(ctx context.Context, orgID, sessionID, requestID uuid.UUID, text *string, payload json.RawMessage, userID uuid.UUID) (models.HumanInputRequest, error) {
	tx.checkIdentity(orgID, sessionID)
	if err := tx.owner.step("cancel"); err != nil {
		return models.HumanInputRequest{}, err
	}
	return tx.fakeTx.CancelRequest(ctx, orgID, sessionID, requestID, text, payload, userID)
}

func (tx *recoveryTx) AnswerCompatibilityQuestion(ctx context.Context, orgID, sessionID uuid.UUID, question, answer string, userID uuid.UUID) error {
	tx.checkIdentity(orgID, sessionID)
	if err := tx.owner.step("compatibility"); err != nil {
		return err
	}
	return tx.fakeTx.AnswerCompatibilityQuestion(ctx, orgID, sessionID, question, answer, userID)
}

func (tx *recoveryTx) SkipCompatibilityQuestion(ctx context.Context, orgID, sessionID uuid.UUID, question string) error {
	tx.checkIdentity(orgID, sessionID)
	if err := tx.owner.step("compatibility"); err != nil {
		return err
	}
	return tx.fakeTx.SkipCompatibilityQuestion(ctx, orgID, sessionID, question)
}

func (tx *recoveryTx) ClaimSessionForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	tx.checkIdentity(orgID, sessionID)
	if err := tx.owner.step("claim session"); err != nil {
		return models.Session{}, err
	}
	return tx.fakeTx.ClaimSessionForResume(ctx, orgID, sessionID)
}

func (tx *recoveryTx) ClaimThreadForResume(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	tx.checkIdentity(orgID, sessionID)
	require.Equal(tx.owner.t, tx.owner.claimedThread.ID, threadID, "resume should claim the originating thread")
	if err := tx.owner.step("claim thread"); err != nil {
		return models.SessionThread{}, err
	}
	return tx.fakeTx.ClaimThreadForResume(ctx, orgID, sessionID, threadID)
}

func (tx *recoveryTx) CreateMessage(ctx context.Context, message *models.SessionMessage) error {
	if err := tx.owner.step("message"); err != nil {
		return err
	}
	return tx.fakeTx.CreateMessage(ctx, message)
}

func (tx *recoveryTx) EnqueueContinue(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, target *models.Session, requestID uuid.UUID) (uuid.UUID, error) {
	tx.checkIdentity(orgID, sessionID)
	require.Equal(tx.owner.t, tx.owner.request.ThreadID, threadID, "job should resume the answered thread")
	require.Equal(tx.owner.t, &tx.owner.claimedSession, target, "job placement should use the claimed session")
	if err := tx.owner.step("enqueue"); err != nil {
		return uuid.Nil, err
	}
	return tx.fakeTx.EnqueueContinue(ctx, orgID, sessionID, threadID, target, requestID)
}

func TestHumanInputRecoveryStopsAtFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("transient database failure")
	tests := []struct {
		name  string
		stage string
		cause error
		want  error
	}{
		{name: "begin", stage: "begin", cause: failure, want: failure},
		{name: "session read", stage: "session", cause: failure, want: failure},
		{name: "missing session", stage: "session", cause: pgx.ErrNoRows, want: ErrNotFound},
		{name: "request read", stage: "request", cause: failure, want: failure},
		{name: "missing request", stage: "request", cause: pgx.ErrNoRows, want: ErrNotFound},
		{name: "request write", stage: "write", cause: failure, want: failure},
		{name: "request answered concurrently", stage: "write", cause: pgx.ErrNoRows, want: ErrNotPending},
		{name: "compatibility write", stage: "compatibility", cause: failure, want: failure},
		{name: "session claim", stage: "claim session", cause: failure, want: ErrNotResumable},
		{name: "thread claim", stage: "claim thread", cause: failure, want: ErrNotResumable},
		{name: "running thread ceiling", stage: "claim thread", cause: db.ErrThreadRunningLimitReached, want: ErrRunningLimit},
		{name: "message write", stage: "message", cause: failure, want: failure},
		{name: "enqueue", stage: "enqueue", cause: failure, want: failure},
		{name: "commit", stage: "commit", cause: failure, want: failure},
	}
	operations := []struct {
		name string
	}{
		{name: "answer"},
		{name: "cancel"},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					repo := newRecoveryRepository(t)
					repo.failStage, repo.failure = tt.stage, tt.cause
					if tt.stage == "write" {
						repo.failStage = operation.name
					}
					text := "Use the simpler approach."
					svc := New(repo)
					var result Result
					var err error
					if operation.name == "answer" {
						result, err = svc.Answer(context.Background(), AnswerInput{OrgID: repo.session.OrgID, SessionID: repo.session.ID, RequestID: repo.request.ID, UserID: uuid.New(), Answer: models.HumanInputAnswerInput{AnswerText: &text}})
					} else {
						result, err = svc.Cancel(context.Background(), CancelInput{OrgID: repo.session.OrgID, SessionID: repo.session.ID, RequestID: repo.request.ID, UserID: uuid.New()})
					}
					require.ErrorIs(t, err, tt.want, "failed provider resume should return the appropriate recovery error")
					require.Equal(t, Result{}, result, "failed transaction must not expose a partially successful result")
					require.False(t, repo.committed, "failed recovery must not commit")
					require.Equal(t, uuid.Nil, repo.notifiedJobID, "failed recovery must not wake a job")
					sequence := []string{"begin", "session", "request", operation.name, "compatibility", "claim session", "claim thread", "message", "enqueue", "commit"}
					for i, stage := range sequence {
						if stage == repo.failStage {
							require.Equal(t, sequence[:i+1], repo.calls, "recovery must stop before downstream writes after failure")
							break
						}
					}
				})
			}
		})
	}
}

func TestHumanInputResumeUsesThreadTurnAndCommitsBeforeNotify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		withThread   bool
		compatAbsent bool
		expectedTurn int
	}{
		{name: "session scoped response", expectedTurn: 3},
		{name: "thread scoped response", withThread: true, expectedTurn: 9},
		{name: "missing legacy question does not block provider", withThread: true, compatAbsent: true, expectedTurn: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newRecoveryRepository(t)
			if !tt.withThread {
				repo.request.ThreadID = nil
			}
			if tt.compatAbsent {
				repo.failStage, repo.failure = "compatibility", pgx.ErrNoRows
			}
			text, userID := "  Use transactions.  ", uuid.New()
			expectedRequest := repo.request
			expectedRequest.Status = models.HumanInputRequestStatusAnswered
			trimmed := "Use transactions."
			expectedRequest.AnswerText = &trimmed
			expectedRequest.AnswerPayload = json.RawMessage(`{"answer_text":"  Use transactions.  "}`)
			expectedRequest.AnsweredBy = &userID
			result, err := New(repo).Answer(context.Background(), AnswerInput{OrgID: repo.session.OrgID, SessionID: repo.session.ID, RequestID: repo.request.ID, UserID: userID, Answer: models.HumanInputAnswerInput{AnswerText: &text}})
			require.NoError(t, err, "durable human input should resume the correct scope")
			require.NotNil(t, result.Message, "successful resume should include its persisted message")
			expectedMessage := models.SessionMessage{ID: 42, OrgID: repo.session.OrgID, SessionID: repo.session.ID, ThreadID: repo.request.ThreadID, UserID: &userID, Role: models.MessageRoleUser, TurnNumber: tt.expectedTurn, Content: "Use transactions.", CreatedAt: result.Message.CreatedAt}
			expectedRequest.AnsweredAt = result.Request.AnsweredAt
			require.NotNil(t, expectedRequest.AnsweredAt, "answered request should retain its completion time")
			require.Equal(t, Result{Request: expectedRequest, Message: &expectedMessage, JobID: repo.jobID}, result, "successful result should include the exact answered request, thread message and resume job")
			require.Equal(t, repo.jobID, repo.notifiedJobID, "only the committed job should be notified")
			require.Equal(t, []string{"commit", "notify"}, repo.calls[len(repo.calls)-2:], "job notification must follow commit")
		})
	}
}

func TestHumanInputDBTransactionBoundaries(t *testing.T) {
	t.Parallel()
	failure := errors.New("transaction failure")
	tests := []struct {
		name  string
		stage string
	}{
		{name: "commit succeeds"},
		{name: "begin fails", stage: "begin"},
		{name: "callback fails and rolls back", stage: "callback"},
		{name: "commit fails and rolls back", stage: "commit"},
		{name: "rollback failure retains callback error", stage: "rollback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "human input transaction fixture should initialize")
			defer mock.Close()
			begin := mock.ExpectBegin()
			if tt.stage == "begin" {
				begin.WillReturnError(failure)
			} else if tt.stage == "callback" || tt.stage == "rollback" {
				rollback := mock.ExpectRollback()
				if tt.stage == "rollback" {
					rollback.WillReturnError(errors.New("rollback connection lost"))
				}
			} else {
				commit := mock.ExpectCommit()
				if tt.stage == "commit" {
					commit.WillReturnError(failure)
					mock.ExpectRollback()
				}
			}
			repo := &dbRepository{sessions: db.NewSessionStore(mock)}
			called := false
			err = repo.WithTx(context.Background(), func(_ context.Context, tx Tx) error {
				called = true
				require.NotNil(t, tx, "callback should receive stores bound to the opened transaction")
				if tt.stage == "callback" || tt.stage == "rollback" {
					return failure
				}
				return nil
			})
			if tt.stage == "" {
				require.NoError(t, err, "successful callback should commit without rollback")
			} else {
				require.ErrorIs(t, err, failure, "transaction boundaries should preserve the original failure")
			}
			require.Equal(t, tt.stage != "begin", called, "callback must run only after a successful begin")
			require.NoError(t, mock.ExpectationsWereMet(), "failed transactions should roll back and successful ones should commit once")
		})
	}
}

type recoveryCapabilityApprover struct {
	t        *testing.T
	identity []uuid.UUID
	calls    int
}

func (a *recoveryCapabilityApprover) BuildApprovedGrant(_ context.Context, orgID, sessionID, requestID uuid.UUID, capability models.AgentCapabilityID, access models.AgentCapabilityAccessLevel) (models.AgentCapabilitySnapshotItem, error) {
	a.calls++
	require.Equal(a.t, a.identity, []uuid.UUID{orgID, sessionID, requestID}, "approval must retain the original tenant and input request identity")
	require.Equal(a.t, models.AgentCapabilityRepoContext, capability, "approval should use the capability requested by the provider")
	require.Equal(a.t, models.AgentCapabilityAccessRead, access, "approval should not elevate the requested access level")
	return models.AgentCapabilitySnapshotItem{ID: capability, AccessLevel: access, HumanInputRequestID: &requestID}, nil
}

func TestHumanInputCapabilityApprovalRecovery(t *testing.T) {
	t.Parallel()
	failure := errors.New("capability grant unavailable")
	tests := []struct {
		name          string
		provider      string
		selected      string
		answerPayload string
		grantErr      error
		expectedCalls int
	}{
		{name: "selected approval applies the requested grant", selected: "approve", expectedCalls: 1},
		{name: "structured approval applies the requested grant", answerPayload: `{"decision":"approve"}`, expectedCalls: 1},
		{name: "already granted retry resumes safely", selected: "approve", grantErr: db.ErrCapabilityAlreadyGranted, expectedCalls: 1},
		{name: "grant failure prevents provider resume", selected: "approve", grantErr: failure, expectedCalls: 1},
		{name: "denial grants no capability", selected: "deny"},
		{name: "ordinary provider request grants no capability", selected: "approve", provider: `{"type":"tool_approval"}`},
		{name: "malformed provider metadata grants no capability", selected: "approve", provider: `{`},
		{name: "missing provider metadata grants no capability", selected: "approve", provider: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newRecoveryRepository(t)
			repo.request.Kind = models.HumanInputRequestKindToolApproval
			repo.request.Choices = []models.HumanInputChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}}
			repo.request.ProviderPayload = json.RawMessage(`{"type":"agent_capability_request","capability_id":"repo_context","access_level":"read"}`)
			if tt.provider != "" {
				repo.request.ProviderPayload = json.RawMessage(tt.provider)
			}
			input := models.HumanInputAnswerInput{AnswerPayload: json.RawMessage(tt.answerPayload)}
			if tt.selected != "" {
				input.SelectedChoiceIDs = []string{tt.selected}
			}
			approver := &recoveryCapabilityApprover{t: t, identity: []uuid.UUID{repo.session.OrgID, repo.session.ID, repo.request.ID}}
			repo.grantErr = tt.grantErr
			svc := New(repo)
			svc.SetCapabilityApprover(approver)
			result, err := svc.Answer(context.Background(), AnswerInput{OrgID: repo.session.OrgID, SessionID: repo.session.ID, RequestID: repo.request.ID, UserID: uuid.New(), Answer: input})
			if tt.grantErr == failure {
				require.ErrorIs(t, err, failure, "grant failure should reach recovery without falsely resuming the provider")
				require.Equal(t, Result{}, result, "failed approval must not return a partial resume")
				require.False(t, repo.committed, "failed approval must not commit the answer")
				require.Equal(t, uuid.Nil, repo.notifiedJobID, "failed approval must not notify a provider resume")
			} else {
				require.NoError(t, err, "valid approval or denial should continue provider recovery")
				require.Equal(t, repo.jobID, result.JobID, "completed approval should return the committed resume job")
			}
			require.Equal(t, tt.expectedCalls, approver.calls, "only an explicit capability approval should apply a grant")
		})
	}
}
