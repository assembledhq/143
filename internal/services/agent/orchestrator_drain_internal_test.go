package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// drainStubMessages is a minimal SessionMessageStore stub for drain tests.
// The orchestrator's drain only calls ListBySession.
type drainStubMessages struct {
	messages []models.SessionMessage
	err      error
}

func (s *drainStubMessages) Create(context.Context, *models.SessionMessage) error {
	return nil
}

func (s *drainStubMessages) ListBySession(context.Context, uuid.UUID, uuid.UUID) ([]models.SessionMessage, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.messages, nil
}

// drainStubSessions returns a configurable session row from GetByID. The
// drain only consults GetByID; the rest of the SessionStore surface is
// stubbed with zero values.
type drainStubSessions struct {
	session models.Session
	err     error
}

func (s *drainStubSessions) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	if s.err != nil {
		return models.Session{}, s.err
	}
	return s.session, nil
}

// All other SessionStore methods are no-op zero-returning stubs; the drain
// path never calls them.
func (s *drainStubSessions) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, models.SessionStatus) error {
	return nil
}
func (s *drainStubSessions) UpdateResult(context.Context, uuid.UUID, uuid.UUID, models.SessionStatus, *models.SessionResult) error {
	return nil
}
func (s *drainStubSessions) CountRunningByOrg(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}
func (s *drainStubSessions) UpdateTurnComplete(context.Context, uuid.UUID, uuid.UUID, int, *models.SessionResult, string, string) error {
	return nil
}
func (s *drainStubSessions) UpdateSnapshotInfo(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}
func (s *drainStubSessions) BeginRuntime(context.Context, uuid.UUID, uuid.UUID, models.CheckpointCapability, time.Time, time.Time, time.Time) error {
	return nil
}
func (s *drainStubSessions) RequestCancel(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (s *drainStubSessions) ConsumeCancelRequest(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}
func (s *drainStubSessions) RecordRuntimeProgress(context.Context, uuid.UUID, uuid.UUID, models.RuntimeProgressType, models.RuntimeProgressStrength, time.Time) error {
	return nil
}
func (s *drainStubSessions) MarkRuntimeStopRequested(context.Context, uuid.UUID, uuid.UUID, models.RuntimeStopReason, time.Time) error {
	return nil
}
func (s *drainStubSessions) GrantRuntimeExtension(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time, time.Time, time.Time, int) (bool, error) {
	return false, nil
}
func (s *drainStubSessions) PublishCheckpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string, models.CheckpointKind, models.CheckpointCapability, int64, time.Time, *string, models.RuntimeStopReason) (bool, error) {
	return false, nil
}
func (s *drainStubSessions) UpdateRecoveryState(context.Context, uuid.UUID, uuid.UUID, models.RecoveryState, *time.Time, *time.Time, bool) error {
	return nil
}
func (s *drainStubSessions) UpdateSandboxState(context.Context, uuid.UUID, uuid.UUID, models.SandboxState) error {
	return nil
}
func (s *drainStubSessions) MarkRunningWithSandboxState(context.Context, uuid.UUID, uuid.UUID, models.SandboxState) error {
	return nil
}
func (s *drainStubSessions) UpdateWorkingBranch(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (s *drainStubSessions) UpdateBaseCommitSHA(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (s *drainStubSessions) SetGitIdentity(context.Context, uuid.UUID, uuid.UUID, string, *uuid.UUID) error {
	return nil
}
func (s *drainStubSessions) UpdateFailure(context.Context, uuid.UUID, uuid.UUID, string, string, []string, bool) error {
	return nil
}
func (s *drainStubSessions) UpdateTitle(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (s *drainStubSessions) UpdateRevisionContext(context.Context, uuid.UUID, uuid.UUID, []byte) error {
	return nil
}
func (s *drainStubSessions) AcquireTurnHold(context.Context, uuid.UUID, uuid.UUID, string) (string, error) {
	return "", nil
}
func (s *drainStubSessions) SetWorkerNodeIDForContainer(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}
func (s *drainStubSessions) ReleaseTurnHold(context.Context, uuid.UUID, uuid.UUID) (bool, string, error) {
	return false, "", nil
}
func (s *drainStubSessions) FinalizeContainerDestroy(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return false, nil
}
func (s *drainStubSessions) ClearContainerID(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return false, nil
}
func (s *drainStubSessions) ContainerHoldState(context.Context, uuid.UUID, uuid.UUID, string) (bool, bool, error) {
	return false, false, nil
}

// drainStubJobs records every continue_session enqueue so tests can assert
// whether the drain fired and inspect the payload.
type drainStubJobs struct {
	enqueues []drainStubEnqueue
	err      error
}

type drainStubEnqueue struct {
	queue        string
	jobType      string
	payload      any
	dedupeKey    string
	targetNodeID string
}

func (j *drainStubJobs) Enqueue(_ context.Context, _ uuid.UUID, queue, jobType string, payload any, _ int, dedupeKey *string) (uuid.UUID, error) {
	if j.err != nil {
		return uuid.Nil, j.err
	}
	key := ""
	if dedupeKey != nil {
		key = *dedupeKey
	}
	j.enqueues = append(j.enqueues, drainStubEnqueue{
		queue:     queue,
		jobType:   jobType,
		payload:   payload,
		dedupeKey: key,
	})
	return uuid.New(), nil
}

func (j *drainStubJobs) EnqueueWithTarget(_ context.Context, _ uuid.UUID, queue, jobType string, payload any, _ int, dedupeKey *string, targetNodeID *string) (uuid.UUID, error) {
	if j.err != nil {
		return uuid.Nil, j.err
	}
	key := ""
	if dedupeKey != nil {
		key = *dedupeKey
	}
	target := ""
	if targetNodeID != nil {
		target = *targetNodeID
	}
	j.enqueues = append(j.enqueues, drainStubEnqueue{
		queue:        queue,
		jobType:      jobType,
		payload:      payload,
		dedupeKey:    key,
		targetNodeID: target,
	})
	return uuid.New(), nil
}

func (j *drainStubJobs) OldestPendingSessionJobAge(context.Context) (time.Duration, bool, error) {
	return 0, false, nil
}

// drainStubThreads records ClearPendingMessages calls so tests can assert
// the counter is reset whenever a queued thread message is drained.
type drainStubThreads struct {
	clearedThreadIDs []uuid.UUID
	clearErr         error
	nextQueuedThread models.SessionThread
	claimNextErr     error
	claimNextCalls   int
}

func (t *drainStubThreads) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, models.ThreadStatus) error {
	return nil
}
func (t *drainStubThreads) CompleteTurn(context.Context, uuid.UUID, uuid.UUID, int, string) error {
	return nil
}
func (t *drainStubThreads) UpdateResult(context.Context, uuid.UUID, uuid.UUID, models.ThreadStatus, *models.SessionResult) error {
	return nil
}
func (t *drainStubThreads) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
	return models.SessionThread{}, pgx.ErrNoRows
}
func (t *drainStubThreads) ClearPendingMessages(_ context.Context, _, threadID uuid.UUID) error {
	if t.clearErr != nil {
		return t.clearErr
	}
	t.clearedThreadIDs = append(t.clearedThreadIDs, threadID)
	return nil
}
func (t *drainStubThreads) ClaimNextQueuedForSession(context.Context, uuid.UUID, uuid.UUID, int) (models.SessionThread, error) {
	t.claimNextCalls++
	if t.claimNextErr != nil {
		return models.SessionThread{}, t.claimNextErr
	}
	if t.nextQueuedThread.ID == uuid.Nil {
		return models.SessionThread{}, pgx.ErrNoRows
	}
	return t.nextQueuedThread, nil
}

type drainStubHumanInputs struct {
	request        models.HumanInputRequest
	sessionAnswers []drainStubHumanInputAnswer
	threadAnswers  []drainStubHumanInputAnswer
}

type drainStubHumanInputAnswer struct {
	orgID      uuid.UUID
	sessionID  uuid.UUID
	threadID   uuid.UUID
	answerText string
	answeredBy uuid.UUID
}

func (h *drainStubHumanInputs) Create(context.Context, *models.HumanInputRequest) error {
	return nil
}

func (h *drainStubHumanInputs) GetByID(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.HumanInputRequest, error) {
	return h.request, nil
}

func (h *drainStubHumanInputs) AnswerLatestPendingFreeTextBySession(_ context.Context, orgID, sessionID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	h.sessionAnswers = append(h.sessionAnswers, drainStubHumanInputAnswer{
		orgID:      orgID,
		sessionID:  sessionID,
		answerText: answerText,
		answeredBy: answeredBy,
	})
	return h.request, nil
}

func (h *drainStubHumanInputs) AnswerLatestPendingFreeTextByThread(_ context.Context, orgID, sessionID, threadID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	h.threadAnswers = append(h.threadAnswers, drainStubHumanInputAnswer{
		orgID:      orgID,
		sessionID:  sessionID,
		threadID:   threadID,
		answerText: answerText,
		answeredBy: answeredBy,
	})
	return h.request, nil
}

func newDrainOrchestrator(messages *drainStubMessages, sessions *drainStubSessions, jobs *drainStubJobs, threads *drainStubThreads, humanInputs ...*drainStubHumanInputs) *Orchestrator {
	o := &Orchestrator{
		sessionMessages: messages,
		sessions:        sessions,
		jobs:            jobs,
		logger:          zerolog.Nop(),
	}
	if threads != nil {
		o.sessionThreads = threads
	}
	if len(humanInputs) > 0 {
		o.humanInputRequests = humanInputs[0]
	}
	return o
}

func TestDrainQueuedMessages_NoNewerMessages(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "idle"}}
	jobs := &drainStubJobs{}
	o := newDrainOrchestrator(messages, sessions, jobs, nil)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, nil, zerolog.Nop())

	require.Empty(t, jobs.enqueues, "drain must not enqueue when no message is newer than the processed one")
}

func TestDrainQueuedMessages_EnqueuesForSessionScope(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser},
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, Content: "queued"},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "idle"}}
	jobs := &drainStubJobs{}
	o := newDrainOrchestrator(messages, sessions, jobs, nil)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, nil, zerolog.Nop())

	require.Len(t, jobs.enqueues, 1, "drain must enqueue continue_session when a newer user message exists")
	require.Equal(t, "agent", jobs.enqueues[0].queue)
	require.Equal(t, "continue_session", jobs.enqueues[0].jobType)
	require.Equal(t, continueSessionDrainDedupeKey(sessionID, processed.ID), jobs.enqueues[0].dedupeKey,
		"drain must not reuse the active continue_session dedupe key while that job is still running")
	payload, ok := jobs.enqueues[0].payload.(map[string]string)
	require.True(t, ok, "payload should be string-keyed")
	require.Equal(t, sessionID.String(), payload["session_id"])
	_, hasThread := payload["thread_id"]
	require.False(t, hasThread, "session-scope drain must not include a thread_id")
}

func TestDrainQueuedMessagesAfterProcessedID_EnqueuesInitialRunQueuedPrompt(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, Content: "queued during initial run"},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "idle"}}
	jobs := &drainStubJobs{}
	o := newDrainOrchestrator(messages, sessions, jobs, nil)

	o.drainQueuedMessagesAfterProcessedID(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, 0, nil, zerolog.Nop())

	require.Len(t, jobs.enqueues, 1, "initial run drain should enqueue continue_session for a prompted message appended while run_agent was active")
	require.Equal(t, continueSessionDrainDedupeKey(sessionID, 0), jobs.enqueues[0].dedupeKey, "initial run drain should use a drain-specific dedupe key")
	payload, ok := jobs.enqueues[0].payload.(map[string]string)
	require.True(t, ok, "initial run drain payload should be string-keyed")
	require.Equal(t, sessionID.String(), payload["session_id"], "initial run drain payload should target the original session")
}

// TestDrainQueuedMessages_LinearPromptedRunningSessionContract pins the
// contract between the Linear-agent prompted handler and the orchestrator's
// post-turn drain. When a `prompted` event lands while a turn is still
// running, worker/handlers_linear_agent_prompted.go's
// appendPromptedMessageToRunningSession inserts a session_messages row with:
//
//   - Role = MessageRoleUser
//   - ThreadID = nil (session-scope, not thread-scope)
//   - TurnNumber = currentTurn + 1
//   - ID > processedMessageID (guaranteed by the sequence)
//
// and intentionally does NOT enqueue continue_session — it relies on this
// drain to pick the message up. If the drain ever changes its filter (role,
// thread scope, id ordering), the Linear-agent prompted handler will silently
// strand follow-up @143 prompts. This test exists to fail loudly in that case.
func TestDrainQueuedMessages_LinearPromptedRunningSessionContract(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	// processedMessageID = 10 represents the in-flight turn's most-recent
	// processed user message. The Linear-appended row has id=11 (sequence-
	// allocated after the running turn began).
	processedID := int64(10)
	linearAppended := models.SessionMessage{
		ID:        processedID + 1,
		OrgID:     orgID,
		SessionID: sessionID,
		Role:      models.MessageRoleUser,
		ThreadID:  nil,
		Content:   "follow-up @143 mention",
	}

	messages := &drainStubMessages{messages: []models.SessionMessage{linearAppended}}
	sessions := &drainStubSessions{session: models.Session{Status: models.SessionStatusIdle}}
	jobs := &drainStubJobs{}
	o := newDrainOrchestrator(messages, sessions, jobs, nil)

	o.drainQueuedMessagesAfterProcessedID(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processedID, nil, zerolog.Nop())

	require.Len(t, jobs.enqueues, 1, "drain must enqueue continue_session for a Linear-agent-appended running-session prompt; otherwise follow-up @143 mentions are stranded")
	require.Equal(t, "agent", jobs.enqueues[0].queue, "drain must enqueue on the agent queue so the worker picks it up")
	require.Equal(t, "continue_session", jobs.enqueues[0].jobType)
	require.Equal(t, continueSessionDrainDedupeKey(sessionID, processedID), jobs.enqueues[0].dedupeKey, "drain must use the drain-specific dedupe key — reusing the active continue_session key would collide with the still-running job")
}

func TestDrainQueuedMessages_ThreadScopeDrainsQueuedSiblingThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadA := uuid.New()
	threadB := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadA}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadA},
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadB, Content: "other thread"},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "idle"}}
	jobs := &drainStubJobs{}
	threads := &drainStubThreads{}
	o := newDrainOrchestrator(messages, sessions, jobs, threads)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, &threadA, zerolog.Nop())

	require.Equal(t, []uuid.UUID{threadB}, threads.clearedThreadIDs, "pending_message_count must be cleared for the queued sibling thread")
	require.Len(t, jobs.enqueues, 1, "thread-scope drain must enqueue continue_session for a queued sibling thread")
	payload := jobs.enqueues[0].payload.(map[string]string)
	require.Equal(t, threadB.String(), payload["thread_id"], "thread-scope drain must target the queued sibling thread")
}

func TestDrainQueuedMessages_ThreadScopeClearsAndEnqueues(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadA := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadA}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadA},
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadA, Content: "queued"},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "idle"}}
	jobs := &drainStubJobs{}
	threads := &drainStubThreads{}
	o := newDrainOrchestrator(messages, sessions, jobs, threads)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, &threadA, zerolog.Nop())

	require.Equal(t, []uuid.UUID{threadA}, threads.clearedThreadIDs, "pending_message_count must be cleared for the drained thread")
	require.Len(t, jobs.enqueues, 1, "thread-scope drain must enqueue continue_session")
	payload := jobs.enqueues[0].payload.(map[string]string)
	require.Equal(t, threadA.String(), payload["thread_id"], "thread-scope drain must propagate thread_id")
}

func TestDrainQueuedMessages_ClearsPendingOnlyAfterEnqueueSucceeds(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID},
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID, Content: "queued"},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: models.SessionStatusIdle}}
	jobs := &drainStubJobs{err: fmt.Errorf("queue unavailable")}
	threads := &drainStubThreads{}
	o := newDrainOrchestrator(messages, sessions, jobs, threads)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, &threadID, zerolog.Nop())

	require.Empty(t, threads.clearedThreadIDs, "pending_message_count must remain until the resume job is durably enqueued")
}

func TestDrainQueuedMessages_ThreadScopeCarriesQueuedMessageForHumanInputAnswer(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID},
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID, UserID: &userID, Content: "Use the existing table."},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "idle"}}
	jobs := &drainStubJobs{}
	threads := &drainStubThreads{}
	humanInputs := &drainStubHumanInputs{request: models.HumanInputRequest{ID: uuid.New()}}
	o := newDrainOrchestrator(messages, sessions, jobs, threads, humanInputs)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, &threadID, zerolog.Nop())

	require.Empty(t, humanInputs.threadAnswers, "drain should not answer human-input before the resume job is durably enqueued")
	require.Len(t, jobs.enqueues, 1, "thread-scope drain should enqueue continue_session")
	payload := jobs.enqueues[0].payload.(map[string]string)
	require.Equal(t, "6", payload["queued_message_id"], "thread-scope drain should carry the queued message id so the worker can answer human input after claiming the job")
}

func TestDrainQueuedMessages_SkipsNonResumableTerminalSessionStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser},
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, Content: "queued"},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "skipped"}}
	jobs := &drainStubJobs{}
	o := newDrainOrchestrator(messages, sessions, jobs, nil)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, nil, zerolog.Nop())

	require.Empty(t, jobs.enqueues, "drain must skip enqueue when the session has reached a non-resumable terminal status")
}

func TestDrainQueuedMessages_NilProcessedNoOp(t *testing.T) {
	t.Parallel()

	jobs := &drainStubJobs{}
	o := newDrainOrchestrator(&drainStubMessages{}, &drainStubSessions{}, jobs, nil)

	o.drainQueuedMessages(context.Background(), &models.Session{}, nil, nil, zerolog.Nop())

	require.Empty(t, jobs.enqueues, "drain must no-op when no message was processed")
}

func TestAdmitNextQueuedThread_SkipsTerminalSessionStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	sessions := &drainStubSessions{session: models.Session{
		ID:     sessionID,
		OrgID:  orgID,
		Status: models.SessionStatusFailed,
	}}
	jobs := &drainStubJobs{}
	threads := &drainStubThreads{nextQueuedThread: models.SessionThread{
		ID:        threadID,
		OrgID:     orgID,
		SessionID: sessionID,
		Status:    models.ThreadStatusRunning,
	}}
	o := newDrainOrchestrator(nil, sessions, jobs, threads)

	o.admitNextQueuedThread(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, zerolog.Nop())

	require.Zero(t, threads.claimNextCalls, "terminal sessions must not claim queued threads after a runtime closes")
	require.Empty(t, jobs.enqueues, "terminal sessions must not enqueue follow-up continue_session jobs")
}

func TestAdmitNextQueuedThread_EnqueuesForDrainableSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	workerNodeID := "worker-a"
	containerID := "container-a"
	sessions := &drainStubSessions{session: models.Session{
		ID:           sessionID,
		OrgID:        orgID,
		Status:       models.SessionStatusRunning,
		WorkerNodeID: &workerNodeID,
		ContainerID:  &containerID,
	}}
	jobs := &drainStubJobs{}
	threads := &drainStubThreads{nextQueuedThread: models.SessionThread{
		ID:        threadID,
		OrgID:     orgID,
		SessionID: sessionID,
		Status:    models.ThreadStatusRunning,
	}}
	o := newDrainOrchestrator(nil, sessions, jobs, threads)

	o.admitNextQueuedThread(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, zerolog.Nop())

	require.Equal(t, 1, threads.claimNextCalls, "drainable sessions should claim one queued thread when a runtime slot opens")
	require.Len(t, jobs.enqueues, 1, "drainable sessions should enqueue the claimed thread")
	require.Equal(t, "continue_session", jobs.enqueues[0].jobType, "admitted queued threads should resume through continue_session")
	require.Equal(t, continueSessionDedupeKey(threadID), jobs.enqueues[0].dedupeKey, "admitted queued threads should be deduped by thread")
	require.Equal(t, workerNodeID, jobs.enqueues[0].targetNodeID, "admitted queued threads should stay pinned to the session worker")
}
