package reviewloop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
)

func TestService_StartCreatesReviewThreadLoopPassAndMessage(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	threadID := uuid.New()
	messageID := int64(77)
	store := &fakeReviewLoopStore{}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeClaudeCode, Status: string(models.SessionStatusIdle), SandboxState: string(models.SandboxStateSnapshotted)},
		thread:  models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, AgentType: models.AgentTypeClaudeCode, Label: "Claude Review"},
		message: models.SessionMessage{ID: messageID, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses:       2,
		StartedByUserID: &userID,
		Source:          models.ReviewLoopSourceManual,
	})
	require.NoError(t, err, "Start should create the review loop")
	require.Equal(t, threadID, *loop.ThreadID, "Start should bind the loop to the review thread")
	require.Equal(t, models.AgentTypeClaudeCode, loop.AgentType, "Start should default to the session agent")
	require.Len(t, store.createdPasses, 1, "Start should create the first pass")
	require.Equal(t, models.ReviewLoopPassStatusReviewing, store.createdPasses[0].Status, "first pass should start in reviewing state")
	require.Equal(t, messageID, store.reviewMessageIDs[0], "Start should persist the review message id on the pass")
	require.Contains(t, threads.sent[0].Message, "/review", "Start should send the native review command")
	require.Len(t, threads.sent[0].Commands, 1, "Start should persist a structured slash command")
	require.Equal(t, "review", threads.sent[0].Commands[0].Name, "structured command should be /review")
}

func TestService_StartRejectsExistingRunningLoop(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoopBySession: models.SessionReviewLoop{
			ID:        uuid.New(),
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Status:    models.ReviewLoopStatusRunning,
			AgentType: models.AgentTypeCodex,
			MaxPasses: 2,
		},
	}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex, Status: string(models.SessionStatusIdle), SandboxState: string(models.SandboxStateSnapshotted)},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses: 2,
		Source:    models.ReviewLoopSourceManual,
	})

	require.ErrorIs(t, err, ErrReviewLoopAlreadyRunning, "Start should reject a second running loop for the same session")
	require.Nil(t, loop, "Start should not return a loop when another loop is running")
	require.Empty(t, store.createdLoops, "Start should not create another loop row")
	require.Empty(t, threads.created, "Start should not create an orphan review thread")
}

func TestService_OnThreadTurnCompleteDirtyThenClean(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:        loopID,
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Status:    models.ReviewLoopStatusRunning,
			AgentType: models.AgentTypeCodex,
			MaxPasses: 2,
		},
		latestPass: models.SessionReviewLoopPass{ID: passID, OrgID: orgID, LoopID: loopID, SessionID: sessionID, PassIndex: 1, Status: models.ReviewLoopPassStatusReviewing},
	}
	threads := &fakeThreadService{message: models.SessionMessage{ID: 10, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID}}
	svc := NewService(store, threads)

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "Found a missing regression test")
	require.NoError(t, err, "review completion should enqueue decision")
	require.Equal(t, "Found a missing regression test", store.markedReviewOutput, "review output should be stored natively")
	require.Contains(t, threads.sent[0].Message, "REVIEW_CLEAN", "decision prompt should ask for the clean sentinel")

	store.latestPass.Status = models.ReviewLoopPassStatusDeciding
	err = svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "NEEDS_FIX_PASS")
	require.NoError(t, err, "dirty decision should enqueue a fix pass")
	require.Equal(t, models.ReviewLoopDecisionNeedsFix, store.fixDecision, "dirty decision should be persisted")
	require.Contains(t, threads.sent[1].Message, "Fix the issues you identified", "fix prompt should reference the previous review")

	store.latestPass.Status = models.ReviewLoopPassStatusFixing
	err = svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "Added the regression test")
	require.NoError(t, err, "fix completion should enqueue the next review pass")
	require.Equal(t, "Added the regression test", store.fixSummary, "fix summary should be stored")
	require.Len(t, store.createdPasses, 1, "fix completion should create the confirmation pass")
	require.Equal(t, 2, store.createdPasses[0].PassIndex, "confirmation pass should increment pass_index")
	require.Contains(t, threads.sent[2].Message, "/review", "confirmation pass should run /review again")

	store.latestPass = store.createdPasses[0]
	store.latestPass.Status = models.ReviewLoopPassStatusDeciding
	err = svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "REVIEW_CLEAN")
	require.NoError(t, err, "clean decision should complete the loop")
	require.Equal(t, models.ReviewLoopDecisionClean, store.cleanDecision, "clean decision should be persisted")
	require.Equal(t, loopID, store.cleanLoopID, "clean decision should mark the loop clean")
}

func TestService_OnThreadTurnCompleteCleanAutomationLoopEnqueuesOpenPR(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "REVIEW_CLEAN")
	require.NoError(t, err, "clean automation review should complete without error")
	require.Equal(t, models.ReviewLoopDecisionClean, store.cleanDecision, "clean decision should be persisted")
	require.Equal(t, loopID, store.cleanLoopID, "clean decision should mark the loop clean")
	require.Equal(t, []string{"clean_open_pr"}, store.events, "clean automation review should durably queue PR creation with the terminal state")
}

func TestService_OnThreadTurnCompleteCleanAutomationLoopIsAtomicWithOpenPR(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
		terminalErr: errors.New("job enqueue failed"),
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "REVIEW_CLEAN")
	require.Error(t, err, "clean automation review should fail when terminal enqueue fails")
	require.ErrorContains(t, err, "job enqueue failed", "clean automation review should surface the enqueue failure")
	require.Equal(t, uuid.Nil, store.cleanLoopID, "clean automation review should not mark the loop clean without the open_pr job")
	require.Empty(t, store.events, "clean automation review should leave terminal state untouched when the atomic write fails")
}

func TestService_OnThreadTurnCompleteAutomationPassLimitEnqueuesOpenPRGate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "NEEDS_FIX_PASS")
	require.NoError(t, err, "pass-limit automation review should complete the terminal write")
	require.Equal(t, loopID, store.needsHumanLoopID, "pass-limit automation review should mark the loop for human decision")
	require.Equal(t, []string{"needs_human_open_pr"}, store.events, "pass-limit automation review should durably queue the PR gate with the terminal state")
}

type fakeReviewLoopStore struct {
	createdLoops         []models.SessionReviewLoop
	createdPasses        []models.SessionReviewLoopPass
	runningLoop          models.SessionReviewLoop
	runningLoopBySession models.SessionReviewLoop
	latestPass           models.SessionReviewLoopPass
	reviewMessageIDs     []int64
	markedReviewOutput   string
	fixDecision          models.ReviewLoopDecision
	cleanDecision        models.ReviewLoopDecision
	cleanLoopID          uuid.UUID
	needsHumanLoopID     uuid.UUID
	fixSummary           string
	terminalErr          error
	events               []string
}

func (f *fakeReviewLoopStore) CreateLoop(_ context.Context, loop *models.SessionReviewLoop) error {
	loop.ID = uuid.New()
	loop.StartedAt = time.Now().UTC()
	f.createdLoops = append(f.createdLoops, *loop)
	return nil
}

func (f *fakeReviewLoopStore) CreatePass(_ context.Context, pass *models.SessionReviewLoopPass) error {
	pass.ID = uuid.New()
	now := time.Now().UTC()
	pass.ReviewStartedAt = &now
	f.createdPasses = append(f.createdPasses, *pass)
	f.latestPass = *pass
	return nil
}

func (f *fakeReviewLoopStore) SetPassReviewMessage(_ context.Context, _ uuid.UUID, _ uuid.UUID, messageID int64) error {
	f.reviewMessageIDs = append(f.reviewMessageIDs, messageID)
	return nil
}

func (f *fakeReviewLoopStore) GetRunningLoopByThread(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.SessionReviewLoop, error) {
	return f.runningLoop, nil
}

func (f *fakeReviewLoopStore) GetRunningLoopBySession(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.SessionReviewLoop, error) {
	if f.runningLoopBySession.ID == uuid.Nil {
		return models.SessionReviewLoop{}, pgx.ErrNoRows
	}
	return f.runningLoopBySession, nil
}

func (f *fakeReviewLoopStore) GetLatestPass(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.SessionReviewLoopPass, error) {
	return f.latestPass, nil
}

func (f *fakeReviewLoopStore) MarkPassDeciding(_ context.Context, _ uuid.UUID, _ uuid.UUID, reviewOutput string, _ int64) error {
	f.markedReviewOutput = reviewOutput
	f.latestPass.Status = models.ReviewLoopPassStatusDeciding
	return nil
}

func (f *fakeReviewLoopStore) MarkPassFixing(_ context.Context, _ uuid.UUID, _ uuid.UUID, decision models.ReviewLoopDecision, _ int64) error {
	f.fixDecision = decision
	f.latestPass.Status = models.ReviewLoopPassStatusFixing
	return nil
}

func (f *fakeReviewLoopStore) MarkPassClean(_ context.Context, _ uuid.UUID, loopID, _ uuid.UUID, decision models.ReviewLoopDecision, _ string) error {
	f.cleanLoopID = loopID
	f.cleanDecision = decision
	f.events = append(f.events, "clean")
	return nil
}

func (f *fakeReviewLoopStore) MarkPassCleanAndEnqueueOpenPR(_ context.Context, _ uuid.UUID, loopID, _ uuid.UUID, decision models.ReviewLoopDecision, _ string, _ map[string]any, _ string) error {
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.cleanLoopID = loopID
	f.cleanDecision = decision
	f.events = append(f.events, "clean_open_pr")
	return nil
}

func (f *fakeReviewLoopStore) MarkPassFixComplete(_ context.Context, _ uuid.UUID, _ uuid.UUID, fixSummary string) error {
	f.fixSummary = fixSummary
	f.latestPass.Status = models.ReviewLoopPassStatusNeedsFix
	return nil
}

func (f *fakeReviewLoopStore) MarkLoopNeedsHumanDecision(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string) error {
	return nil
}

func (f *fakeReviewLoopStore) MarkLoopNeedsHumanDecisionAndEnqueueOpenPR(_ context.Context, _ uuid.UUID, loopID uuid.UUID, _ string, _ map[string]any, _ string) error {
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.needsHumanLoopID = loopID
	f.events = append(f.events, "needs_human_open_pr")
	return nil
}

func (f *fakeReviewLoopStore) MarkLoopFailed(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string) error {
	return nil
}

type fakeThreadService struct {
	session models.Session
	thread  models.SessionThread
	message models.SessionMessage
	sent    []threadsvc.SendMessageInput
	created []threadsvc.CreateThreadInput
}

func (f *fakeThreadService) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	return f.session, nil
}

func (f *fakeThreadService) CreateThread(ctx context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	f.created = append(f.created, input)
	return &f.thread, nil
}

func (f *fakeThreadService) SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	f.sent = append(f.sent, input)
	msg := f.message
	msg.ID += int64(len(f.sent) - 1)
	return &threadsvc.SendMessageResult{Message: &msg}, nil
}
