package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
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
func (s *drainStubSessions) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (s *drainStubSessions) UpdateResult(context.Context, uuid.UUID, uuid.UUID, string, *models.SessionResult) error {
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
func (s *drainStubSessions) GrantRuntimeExtension(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time, time.Time, time.Time, int) (bool, error) {
	return false, nil
}
func (s *drainStubSessions) PublishCheckpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string, models.CheckpointKind, models.CheckpointCapability, int64, time.Time, *string, models.RuntimeStopReason) (bool, error) {
	return false, nil
}
func (s *drainStubSessions) UpdateRecoveryState(context.Context, uuid.UUID, uuid.UUID, models.RecoveryState, *time.Time, *time.Time, bool) error {
	return nil
}
func (s *drainStubSessions) UpdateSandboxState(context.Context, uuid.UUID, uuid.UUID, string) error {
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
func (t *drainStubThreads) ClearPendingMessages(_ context.Context, _, threadID uuid.UUID) error {
	if t.clearErr != nil {
		return t.clearErr
	}
	t.clearedThreadIDs = append(t.clearedThreadIDs, threadID)
	return nil
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
	sessions := &drainStubSessions{session: models.Session{Status: string(models.SessionStatusIdle)}}
	jobs := &drainStubJobs{}
	o := newDrainOrchestrator(messages, sessions, jobs, nil)

	o.drainQueuedMessagesAfterProcessedID(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processedID, nil, zerolog.Nop())

	require.Len(t, jobs.enqueues, 1, "drain must enqueue continue_session for a Linear-agent-appended running-session prompt; otherwise follow-up @143 mentions are stranded")
	require.Equal(t, "agent", jobs.enqueues[0].queue, "drain must enqueue on the agent queue so the worker picks it up")
	require.Equal(t, "continue_session", jobs.enqueues[0].jobType)
	require.Equal(t, continueSessionDrainDedupeKey(sessionID, processedID), jobs.enqueues[0].dedupeKey, "drain must use the drain-specific dedupe key — reusing the active continue_session key would collide with the still-running job")
}

func TestDrainQueuedMessages_ThreadScopeIgnoresOtherThreads(t *testing.T) {
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

	require.Empty(t, jobs.enqueues, "drain must ignore newer messages on a different thread")
	require.Empty(t, threads.clearedThreadIDs, "no clear should fire when there is nothing to drain")
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

func TestDrainQueuedMessages_ThreadScopeAnswersPendingHumanInput(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()
	requestID := uuid.New()
	processed := &models.SessionMessage{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID}

	messages := &drainStubMessages{messages: []models.SessionMessage{
		{ID: 5, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID},
		{ID: 6, OrgID: orgID, SessionID: sessionID, Role: models.MessageRoleUser, ThreadID: &threadID, UserID: &userID, Content: "Use the existing table."},
	}}
	sessions := &drainStubSessions{session: models.Session{Status: "idle"}}
	jobs := &drainStubJobs{}
	threads := &drainStubThreads{}
	humanInputs := &drainStubHumanInputs{request: models.HumanInputRequest{
		ID:        requestID,
		OrgID:     orgID,
		SessionID: sessionID,
		ThreadID:  &threadID,
		Status:    models.HumanInputRequestStatusAnswered,
	}}
	o := newDrainOrchestrator(messages, sessions, jobs, threads, humanInputs)

	o.drainQueuedMessages(context.Background(), &models.Session{ID: sessionID, OrgID: orgID}, processed, &threadID, zerolog.Nop())

	require.Len(t, humanInputs.threadAnswers, 1, "thread-scope drain should answer the pending human-input request with the queued user message")
	require.Equal(t, threadID, humanInputs.threadAnswers[0].threadID, "thread-scope drain should answer the request for the drained thread")
	require.Equal(t, "Use the existing table.", humanInputs.threadAnswers[0].answerText, "thread-scope drain should persist the queued message text as the answer")
	require.Equal(t, userID, humanInputs.threadAnswers[0].answeredBy, "thread-scope drain should attribute the answer to the queued message user")
	require.Len(t, jobs.enqueues, 1, "thread-scope drain should enqueue continue_session")
	payload := jobs.enqueues[0].payload.(map[string]string)
	require.Equal(t, requestID.String(), payload["human_input_request_id"], "thread-scope drain should carry the answered request id into the resume job")
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
