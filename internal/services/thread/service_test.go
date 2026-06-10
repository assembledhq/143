package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// --- Mock stores ---

type mockThreadStore struct {
	createFn           func(ctx context.Context, t *models.SessionThread, max int) error
	getByIDFn          func(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error)
	listBySessionFn    func(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	archiveFn          func(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	claimIdleFn        func(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	claimForResumeFn   func(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	updateFn           func(ctx context.Context, t *models.SessionThread) error
	updateStatusFn     func(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error
	incrementPendingFn func(ctx context.Context, orgID, threadID uuid.UUID) error
	pendingCalls       []uuid.UUID
}

func (m *mockThreadStore) Create(ctx context.Context, t *models.SessionThread, max int) error {
	if m.createFn != nil {
		return m.createFn(ctx, t, max)
	}
	return nil
}

func (m *mockThreadStore) GetByID(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, threadID)
	}
	return models.SessionThread{}, fmt.Errorf("not found")
}

func (m *mockThreadStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error) {
	if m.listBySessionFn != nil {
		return m.listBySessionFn(ctx, orgID, sessionID)
	}
	return nil, nil
}

func (m *mockThreadStore) Archive(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	if m.archiveFn != nil {
		return m.archiveFn(ctx, orgID, sessionID, threadID)
	}
	return models.SessionThread{}, fmt.Errorf("not archived")
}

func (m *mockThreadStore) ClaimIdleForSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, _ int) (models.SessionThread, error) {
	if m.claimIdleFn != nil {
		return m.claimIdleFn(ctx, orgID, sessionID, threadID)
	}
	return models.SessionThread{}, fmt.Errorf("not idle")
}

func (m *mockThreadStore) ClaimForResumeInSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, _ int) (models.SessionThread, error) {
	if m.claimForResumeFn != nil {
		return m.claimForResumeFn(ctx, orgID, sessionID, threadID)
	}
	return models.SessionThread{}, fmt.Errorf("not resumable")
}

func (m *mockThreadStore) UpdateEditable(ctx context.Context, t *models.SessionThread) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, t)
	}
	return nil
}

func (m *mockThreadStore) UpdateStatus(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error {
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, orgID, threadID, status)
	}
	return nil
}

func (m *mockThreadStore) IncrementPendingMessages(ctx context.Context, orgID, threadID uuid.UUID) error {
	m.pendingCalls = append(m.pendingCalls, threadID)
	if m.incrementPendingFn != nil {
		return m.incrementPendingFn(ctx, orgID, threadID)
	}
	return nil
}

func (m *mockThreadStore) MarkCancelRequested(ctx context.Context, orgID, threadID uuid.UUID) error {
	return nil
}

type mockSessionStore struct {
	getByIDFn        func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	claimIdleFn      func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	claimForResumeFn func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	updateStatusFn   func(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error
	updateCalls      []models.SessionStatus
}

func (m *mockSessionStore) GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, sessionID)
	}
	return models.Session{}, fmt.Errorf("not found")
}

func (m *mockSessionStore) ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if m.claimIdleFn != nil {
		return m.claimIdleFn(ctx, orgID, sessionID)
	}
	return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}, nil
}

func (m *mockSessionStore) ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if m.claimForResumeFn != nil {
		return m.claimForResumeFn(ctx, orgID, sessionID)
	}
	return models.Session{}, fmt.Errorf("no rows")
}

func (m *mockSessionStore) UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error {
	m.updateCalls = append(m.updateCalls, status)
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, orgID, sessionID, status)
	}
	return nil
}

type mockMessageStore struct {
	createFn             func(ctx context.Context, msg *models.SessionMessage) error
	getByIDFn            func(ctx context.Context, orgID uuid.UUID, id int64) (models.SessionMessage, error)
	listByThreadFn       func(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error)
	listWindowByThreadFn func(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error)
}

func (m *mockMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	if m.createFn != nil {
		return m.createFn(ctx, msg)
	}
	return nil
}

func (m *mockMessageStore) GetByID(ctx context.Context, orgID uuid.UUID, id int64) (models.SessionMessage, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, id)
	}
	return models.SessionMessage{ID: id, OrgID: orgID}, nil
}

func (m *mockMessageStore) ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error) {
	if m.listByThreadFn != nil {
		return m.listByThreadFn(ctx, orgID, threadID)
	}
	return nil, nil
}

func (m *mockMessageStore) ListWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error) {
	if m.listWindowByThreadFn != nil {
		return m.listWindowByThreadFn(ctx, orgID, threadID, opts)
	}
	return db.SessionMessageWindow{}, nil
}

type mockLogStore struct {
	listByThreadFn      func(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error)
	listByThreadTurnsFn func(ctx context.Context, orgID, threadID uuid.UUID, turnNumbers []int) ([]models.SessionLog, error)
}

func (m *mockLogStore) ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error) {
	if m.listByThreadFn != nil {
		return m.listByThreadFn(ctx, orgID, threadID)
	}
	return nil, nil
}

func (m *mockLogStore) ListByThreadTurns(ctx context.Context, orgID, threadID uuid.UUID, turnNumbers []int) ([]models.SessionLog, error) {
	if m.listByThreadTurnsFn != nil {
		return m.listByThreadTurnsFn(ctx, orgID, threadID, turnNumbers)
	}
	return nil, nil
}

type mockJobStore struct {
	enqueueFn         func(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	enqueueWithOptsFn func(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error)
}

func (m *mockJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	if m.enqueueFn != nil {
		return m.enqueueFn(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
	}
	return uuid.New(), nil
}

func (m *mockJobStore) EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	if m.enqueueWithOptsFn != nil {
		return m.enqueueWithOptsFn(ctx, orgID, opts)
	}
	if m.enqueueFn != nil {
		return m.enqueueFn(ctx, orgID, opts.Queue, opts.JobType, opts.Payload, opts.Priority, opts.DedupeKey)
	}
	return uuid.New(), nil
}

type mockThreadInboxStore struct {
	appendFn                func(ctx context.Context, orgID uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error)
	getByClientMessageIDFn  func(ctx context.Context, orgID, threadID uuid.UUID, clientMessageID string) (models.ThreadInboxEntry, error)
	listSummariesBySession  func(ctx context.Context, orgID, sessionID uuid.UUID) (map[uuid.UUID]models.ThreadInboxDeliverySummary, error)
	getSummaryByThread      func(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadInboxDeliverySummary, error)
	listRecoverableFn       func(ctx context.Context, orgID, threadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error)
	retryRecoverableFn      func(ctx context.Context, orgID, threadID, entryID uuid.UUID, allowUnknown bool) (models.ThreadInboxEntry, error)
	countPendingByThreadFn  func(ctx context.Context, orgID, threadID uuid.UUID) (int, error)
	countPendingBySessionFn func(ctx context.Context, orgID, sessionID uuid.UUID) (int, error)
	appendCalls             []db.AppendThreadInboxEntryParams
	retryCalls              []uuid.UUID
	retryAllowUnknownCalls  []bool
}

func (m *mockThreadInboxStore) AppendForMessage(ctx context.Context, orgID uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	m.appendCalls = append(m.appendCalls, params)
	if m.appendFn != nil {
		return m.appendFn(ctx, orgID, params)
	}
	return models.ThreadInboxEntry{
		ID:            uuid.New(),
		OrgID:         orgID,
		SessionID:     params.SessionID,
		ThreadID:      params.ThreadID,
		MessageID:     params.MessageID,
		EntryType:     params.EntryType,
		DeliveryState: models.ThreadInboxDeliveryStatePending,
	}, nil
}

func (m *mockThreadInboxStore) GetByClientMessageID(ctx context.Context, orgID, threadID uuid.UUID, clientMessageID string) (models.ThreadInboxEntry, error) {
	if m.getByClientMessageIDFn != nil {
		return m.getByClientMessageIDFn(ctx, orgID, threadID, clientMessageID)
	}
	return models.ThreadInboxEntry{}, pgx.ErrNoRows
}

func (m *mockThreadInboxStore) ListDeliverySummariesBySession(ctx context.Context, orgID, sessionID uuid.UUID) (map[uuid.UUID]models.ThreadInboxDeliverySummary, error) {
	if m.listSummariesBySession != nil {
		return m.listSummariesBySession(ctx, orgID, sessionID)
	}
	return map[uuid.UUID]models.ThreadInboxDeliverySummary{}, nil
}

func (m *mockThreadInboxStore) GetDeliverySummaryByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadInboxDeliverySummary, error) {
	if m.getSummaryByThread != nil {
		return m.getSummaryByThread(ctx, orgID, threadID)
	}
	summary := models.ThreadInboxDeliverySummary{ThreadID: threadID}
	summary.Normalize()
	return summary, nil
}

func (m *mockThreadInboxStore) ListRecoverableByThread(ctx context.Context, orgID, threadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error) {
	if m.listRecoverableFn != nil {
		return m.listRecoverableFn(ctx, orgID, threadID, limit)
	}
	return []models.ThreadInboxEntry{}, nil
}

func (m *mockThreadInboxStore) RetryRecoverable(ctx context.Context, orgID, threadID, entryID uuid.UUID, allowUnknown bool) (models.ThreadInboxEntry, error) {
	m.retryCalls = append(m.retryCalls, entryID)
	m.retryAllowUnknownCalls = append(m.retryAllowUnknownCalls, allowUnknown)
	if m.retryRecoverableFn != nil {
		return m.retryRecoverableFn(ctx, orgID, threadID, entryID, allowUnknown)
	}
	return models.ThreadInboxEntry{
		ID:            entryID,
		OrgID:         orgID,
		ThreadID:      threadID,
		DeliveryState: models.ThreadInboxDeliveryStatePending,
	}, nil
}

type mockRuntimeOwnerStore struct {
	getActiveFn func(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadRuntime, error)
}

func (m *mockRuntimeOwnerStore) GetActiveByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadRuntime, error) {
	if m.getActiveFn != nil {
		return m.getActiveFn(ctx, orgID, threadID)
	}
	return models.ThreadRuntime{}, pgx.ErrNoRows
}

func (m *mockThreadInboxStore) CountPendingByThread(ctx context.Context, orgID, threadID uuid.UUID) (int, error) {
	if m.countPendingByThreadFn != nil {
		return m.countPendingByThreadFn(ctx, orgID, threadID)
	}
	return 0, nil
}

func (m *mockThreadInboxStore) CountPendingBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	if m.countPendingBySessionFn != nil {
		return m.countPendingBySessionFn(ctx, orgID, sessionID)
	}
	return 0, nil
}

type testDeps struct {
	threadStore  *mockThreadStore
	sessionStore *mockSessionStore
	messageStore *mockMessageStore
	logStore     *mockLogStore
	jobStore     *mockJobStore
	inboxStore   *mockThreadInboxStore
}

type mockOwnerLossRecovery struct {
	calls []ownerLossRecoveryCall
	err   error
}

type ownerLossRecoveryCall struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	threadID  *uuid.UUID
}

func (m *mockOwnerLossRecovery) RecoverLostOwner(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID) error {
	var copiedThreadID *uuid.UUID
	if threadID != nil {
		id := *threadID
		copiedThreadID = &id
	}
	m.calls = append(m.calls, ownerLossRecoveryCall{orgID: orgID, sessionID: sessionID, threadID: copiedThreadID})
	return m.err
}

func newTestService(t *testing.T) (*Service, *testDeps) {
	t.Helper()
	deps := &testDeps{
		threadStore:  &mockThreadStore{},
		sessionStore: &mockSessionStore{},
		messageStore: &mockMessageStore{},
		logStore:     &mockLogStore{},
		jobStore:     &mockJobStore{},
		inboxStore:   &mockThreadInboxStore{},
	}
	svc := NewService(deps.threadStore, deps.sessionStore, deps.messageStore, deps.logStore, deps.jobStore, zerolog.Nop())
	return svc, deps
}

var threadHumanInputRequestColumns = []string{
	"id", "org_id", "session_id", "thread_id", "turn_number", "agent_type",
	"provider_request_id", "request_kind", "status", "title", "body",
	"context", "blocks_phase", "choices", "response_schema", "provider_payload",
	"answer_text", "answer_payload", "answered_by", "answered_at", "expires_at", "created_at",
}

func threadHumanInputRequestRow(id, orgID, sessionID, threadID, userID uuid.UUID, answer string, now time.Time) []any {
	return []any{
		id, orgID, sessionID, &threadID, 3, models.AgentTypeClaudeCode,
		humanInputTestStringPtr("toolu_thread"), models.HumanInputRequestKindFreeText,
		models.HumanInputRequestStatusAnswered, "Claude needs input", "What should Claude do?",
		(*string)(nil), (*string)(nil), []byte("[]"), json.RawMessage(nil), json.RawMessage(`{"raw":true}`),
		&answer, json.RawMessage(`{"answer_text":"` + answer + `"}`), &userID, &now, (*time.Time)(nil), now,
	}
}

func humanInputTestStringPtr(s string) *string {
	return &s
}

func TestService_CreateThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		input     CreateThreadInput
		setupDeps func(deps *testDeps)
		expectErr error
	}{
		{
			name: "success with defaults",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, t *models.SessionThread, _ int) error {
					t.ID = threadID
					t.CreatedAt = now
					return nil
				}
			},
		},
		{
			name: "success with explicit agent type and instructions",
			input: CreateThreadInput{
				SessionID:    sessionID,
				OrgID:        orgID,
				AgentType:    "claude_code",
				Label:        "Frontend",
				Instructions: "focus on UI",
				FileScope:    []string{"src/"},
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, t *models.SessionThread, _ int) error {
					t.ID = threadID
					t.CreatedAt = now
					return nil
				}
			},
		},
		{
			name: "session not found",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrSessionNotFound,
		},
		{
			name: "success for completed session",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "completed", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, t *models.SessionThread, _ int) error {
					t.ID = threadID
					t.CreatedAt = now
					return nil
				}
			},
		},
		{
			name: "session in non-resumable terminal state",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "skipped", AgentType: models.AgentTypeClaudeCode}, nil
				}
			},
			expectErr: ErrSessionTerminal,
		},
		{
			name: "invalid agent type",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				AgentType: "nonexistent_agent",
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
			},
			expectErr: ErrInvalidAgentType,
		},
		{
			name: "thread limit reached",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, _ *models.SessionThread, _ int) error {
					return db.ErrThreadLimitReached
				}
			},
			expectErr: db.ErrThreadLimitReached,
		},
		{
			name: "creates idle blank thread without enqueueing work",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, thread *models.SessionThread, _ int) error {
					require.Equal(t, models.ThreadStatusIdle, thread.Status, "new tabs should start idle")
					thread.ID = threadID
					thread.CreatedAt = now
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					require.Fail(t, "creating a blank tab should not enqueue an agent job")
					return uuid.Nil, nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			result, err := svc.CreateThread(context.Background(), tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				require.Nil(t, result, "should not return a thread on error")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.NotNil(t, result, "should return a thread")
			require.Equal(t, threadID, result.ID, "should set the thread ID")
			require.Equal(t, tt.input.Label, result.Label, "should set the label")
			require.Equal(t, models.ThreadStatusIdle, result.Status, "new tab should wait for first user message")
		})
	}
}

func TestService_CreateThreadDoesNotEnqueueOnCreate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	svc, deps := newTestService(t)
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
	}
	deps.threadStore.createFn = func(_ context.Context, thread *models.SessionThread, _ int) error {
		thread.ID = uuid.New()
		thread.CreatedAt = time.Now()
		return nil
	}
	deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
		require.Fail(t, "CreateThread should not enqueue a job")
		return uuid.Nil, nil
	}

	result, err := svc.CreateThread(context.Background(), CreateThreadInput{
		SessionID: sessionID,
		OrgID:     orgID,
		Label:     "Reviewer",
	})

	require.NoError(t, err, "CreateThread should create a blank tab")
	require.Equal(t, models.ThreadStatusIdle, result.Status, "blank tab should be idle")
}

func TestService_UpdateThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name          string
		input         UpdateThreadInput
		setupDeps     func(deps *testDeps)
		expectErr     error
		expectedType  models.AgentType
		expectedLabel string
		expectedModel *string
	}{
		{
			name: "updates blank idle thread and clears inherited model override",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				model := models.ClaudeCodeModelSonnet46
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, gotThreadID uuid.UUID) (models.SessionThread, error) {
					require.Equal(t, threadID, gotThreadID, "UpdateThread should load the requested thread")
					return models.SessionThread{
						ID:            threadID,
						SessionID:     sessionID,
						OrgID:         orgID,
						AgentType:     models.AgentTypeClaudeCode,
						ModelOverride: &model,
						Label:         "Claude Code 2",
						Status:        models.ThreadStatusIdle,
						CurrentTurn:   0,
					}, nil
				}
				deps.threadStore.updateFn = func(_ context.Context, updated *models.SessionThread) error {
					require.Equal(t, models.AgentTypeCodex, updated.AgentType, "UpdateThread should persist the replacement agent")
					require.Nil(t, updated.ModelOverride, "UpdateThread should clear an incompatible inherited model override")
					require.Equal(t, "Codex 2", updated.Label, "UpdateThread should persist the replacement label")
					return nil
				}
			},
			expectedType:  models.AgentTypeCodex,
			expectedLabel: "Codex 2",
		},
		{
			name: "accepts an explicit model override for the replacement agent",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Model:     stringPtr(models.CodexModelGPT54Mini),
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						AgentType:   models.AgentTypeClaudeCode,
						Label:       "Claude Code 2",
						Status:      models.ThreadStatusIdle,
						CurrentTurn: 0,
					}, nil
				}
				deps.threadStore.updateFn = func(_ context.Context, updated *models.SessionThread) error {
					require.NotNil(t, updated.ModelOverride, "UpdateThread should persist the requested model override")
					require.Equal(t, models.CodexModelGPT54Mini, *updated.ModelOverride, "UpdateThread should persist the requested model override")
					return nil
				}
			},
			expectedType:  models.AgentTypeCodex,
			expectedLabel: "Codex 2",
			expectedModel: stringPtr(models.CodexModelGPT54Mini),
		},
		{
			name: "explicit empty model clears an existing override without switching agent",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Model:     stringPtr(""),
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				existing := models.CodexModelGPT54Mini
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeCodex}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:            threadID,
						SessionID:     sessionID,
						OrgID:         orgID,
						AgentType:     models.AgentTypeCodex,
						ModelOverride: &existing,
						Label:         "Codex 2",
						Status:        models.ThreadStatusIdle,
						CurrentTurn:   0,
					}, nil
				}
				deps.threadStore.updateFn = func(_ context.Context, updated *models.SessionThread) error {
					require.Nil(t, updated.ModelOverride, "explicit empty model should clear the override")
					return nil
				}
			},
			expectedType:  models.AgentTypeCodex,
			expectedLabel: "Codex 2",
		},
		{
			name: "omitted model preserves an existing override on a label-only patch",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Label:     "Codex 2 renamed",
			},
			setupDeps: func(deps *testDeps) {
				existing := models.CodexModelGPT54Mini
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeCodex}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:            threadID,
						SessionID:     sessionID,
						OrgID:         orgID,
						AgentType:     models.AgentTypeCodex,
						ModelOverride: &existing,
						Label:         "Codex 2",
						Status:        models.ThreadStatusIdle,
						CurrentTurn:   0,
					}, nil
				}
				deps.threadStore.updateFn = func(_ context.Context, updated *models.SessionThread) error {
					require.NotNil(t, updated.ModelOverride, "label-only patch should preserve the existing model override")
					require.Equal(t, models.CodexModelGPT54Mini, *updated.ModelOverride, "label-only patch should preserve the existing model override")
					return nil
				}
			},
			expectedType:  models.AgentTypeCodex,
			expectedLabel: "Codex 2 renamed",
			expectedModel: stringPtr(models.CodexModelGPT54Mini),
		},
		{
			name: "rejects threads that have already started turns",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						AgentType:   models.AgentTypeClaudeCode,
						Label:       "Claude Code 2",
						Status:      models.ThreadStatusIdle,
						CurrentTurn: 1,
					}, nil
				}
			},
			expectErr: ErrThreadNotEditable,
		},
		{
			name: "rejects non-idle threads",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						AgentType:   models.AgentTypeClaudeCode,
						Label:       "Claude Code 2",
						Status:      models.ThreadStatusRunning,
						CurrentTurn: 0,
					}, nil
				}
			},
			expectErr: ErrThreadNotEditable,
		},
		{
			name: "session not found",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrSessionNotFound,
		},
		{
			name: "session terminal",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "skipped", AgentType: models.AgentTypeClaudeCode}, nil
				}
			},
			expectErr: ErrSessionTerminal,
		},
		{
			name: "thread not found",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "thread in another session is hidden as not found",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   uuid.New(),
						OrgID:       orgID,
						AgentType:   models.AgentTypeClaudeCode,
						Label:       "Claude Code 2",
						Status:      models.ThreadStatusIdle,
						CurrentTurn: 0,
					}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "invalid agent type",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "pm_agent",
				Label:     "PM Agent 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						AgentType:   models.AgentTypeClaudeCode,
						Label:       "Claude Code 2",
						Status:      models.ThreadStatusIdle,
						CurrentTurn: 0,
					}, nil
				}
			},
			expectErr: ErrInvalidAgentType,
		},
		{
			name: "invalid model override",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Model:     stringPtr(models.ClaudeCodeModelSonnet46),
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						AgentType:   models.AgentTypeClaudeCode,
						Label:       "Claude Code 2",
						Status:      models.ThreadStatusIdle,
						CurrentTurn: 0,
					}, nil
				}
			},
			expectErr: ErrInvalidModel,
		},
		{
			name: "returns thread not editable when guarded update loses the race",
			input: UpdateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				AgentType: "codex",
				Model:     stringPtr(models.CodexModelGPT54Mini),
				Label:     "Codex 2",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						AgentType:   models.AgentTypeClaudeCode,
						Label:       "Claude Code 2",
						Status:      models.ThreadStatusIdle,
						CurrentTurn: 0,
					}, nil
				}
				deps.threadStore.updateFn = func(_ context.Context, _ *models.SessionThread) error {
					return pgx.ErrNoRows
				}
			},
			expectErr: ErrThreadNotEditable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			result, err := svc.UpdateThread(context.Background(), tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "UpdateThread should return the expected sentinel error")
				return
			}

			require.NoError(t, err, "UpdateThread should succeed for blank idle threads")
			require.Equal(t, tt.expectedType, result.AgentType, "UpdateThread should return the updated agent type")
			require.Equal(t, tt.expectedLabel, result.Label, "UpdateThread should return the updated label")
			require.Equal(t, tt.expectedModel, result.ModelOverride, "UpdateThread should return the updated model override")
		})
	}
}

func TestService_ArchiveThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name          string
		setupDeps     func(deps *testDeps)
		expectErr     error
		expectedLabel string
	}{
		{
			name: "archives a completed thread when another visible tab remains",
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "completed"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return []models.SessionThread{
						{ID: uuid.New(), SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning, Label: "Main tab"},
						{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted, Label: "Review"},
					}, nil
				}
				deps.threadStore.archiveFn = func(_ context.Context, gotOrgID, gotSessionID, gotThreadID uuid.UUID) (models.SessionThread, error) {
					require.Equal(t, orgID, gotOrgID, "ArchiveThread should archive within the requested org")
					require.Equal(t, sessionID, gotSessionID, "ArchiveThread should archive within the requested session")
					require.Equal(t, threadID, gotThreadID, "ArchiveThread should archive the requested thread")
					now := time.Now()
					return models.SessionThread{
						ID:         threadID,
						SessionID:  sessionID,
						OrgID:      orgID,
						Status:     models.ThreadStatusCompleted,
						Label:      "Review",
						ArchivedAt: &now,
					}, nil
				}
			},
			expectedLabel: "Review",
		},
		{
			name: "rejects archiving the last visible tab",
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "idle"}, nil
				}
				deps.threadStore.archiveFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, pgx.ErrNoRows
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted, Label: "Only tab"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return []models.SessionThread{
						{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted, Label: "Only tab"},
					}, nil
				}
			},
			expectErr: ErrCannotArchiveLastThread,
		},
		{
			name: "rejects archiving an active thread",
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running"}, nil
				}
				deps.threadStore.archiveFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, pgx.ErrNoRows
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning, Label: "Busy tab"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return []models.SessionThread{
						{ID: uuid.New(), SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted, Label: "Main tab"},
						{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning, Label: "Busy tab"},
					}, nil
				}
			},
			expectErr: ErrThreadActive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			result, err := svc.ArchiveThread(context.Background(), orgID, sessionID, threadID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "ArchiveThread should return the expected sentinel error")
				return
			}

			require.NoError(t, err, "ArchiveThread should succeed for inactive non-last tabs")
			require.Equal(t, tt.expectedLabel, result.Label, "ArchiveThread should return the archived thread")
			require.NotNil(t, result.ArchivedAt, "ArchiveThread should return the archived timestamp")
		})
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestService_ListThreads(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID1 := uuid.New()
	threadID2 := uuid.New()

	tests := []struct {
		name      string
		setupDeps func(deps *testDeps)
		expectErr error
		expectLen int
	}{
		{
			name: "success",
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return []models.SessionThread{{ID: threadID1}, {ID: threadID2}}, nil
				}
				deps.inboxStore.listSummariesBySession = func(_ context.Context, gotOrgID, gotSessionID uuid.UUID) (map[uuid.UUID]models.ThreadInboxDeliverySummary, error) {
					require.Equal(t, orgID, gotOrgID, "ListThreads should load delivery state within the requested org")
					require.Equal(t, sessionID, gotSessionID, "ListThreads should load delivery state for the requested session")
					summary := models.ThreadInboxDeliverySummary{ThreadID: threadID1, PendingCount: 2}
					summary.Normalize()
					return map[uuid.UUID]models.ThreadInboxDeliverySummary{threadID1: summary}, nil
				}
			},
			expectLen: 2,
		},
		{
			name: "session not found",
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrSessionNotFound,
		},
		{
			name: "list error",
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return nil, fmt.Errorf("db error")
				}
			},
			expectErr: nil, // wraps as "list threads:" not a sentinel
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)
			svc.SetThreadInboxStore(deps.inboxStore)

			threads, err := svc.ListThreads(context.Background(), orgID, sessionID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				return
			}
			if tt.name == "list error" {
				require.Error(t, err, "should return an error on db failure")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.Len(t, threads, tt.expectLen, "should return expected number of threads")
			if tt.name == "success" {
				require.NotNil(t, threads[0].InboxDelivery, "ListThreads should attach delivery state to threads when the inbox store is wired")
				require.Equal(t, models.ThreadInboxSummaryStatePending, threads[0].InboxDelivery.State, "ListThreads should expose pending delivery state")
				require.NotNil(t, threads[1].InboxDelivery, "ListThreads should attach an idle delivery state for threads without inbox rows")
				require.Equal(t, models.ThreadInboxSummaryStateIdle, threads[1].InboxDelivery.State, "ListThreads should expose idle delivery state when no inbox rows exist")
			}
		})
	}
}

func TestService_GetThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name      string
		setupDeps func(deps *testDeps)
		expectErr error
	}{
		{
			name: "success",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.inboxStore.getSummaryByThread = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.ThreadInboxDeliverySummary, error) {
					require.Equal(t, orgID, gotOrgID, "GetThread should load delivery state within the requested org")
					require.Equal(t, threadID, gotThreadID, "GetThread should load delivery state for the requested thread")
					summary := models.ThreadInboxDeliverySummary{ThreadID: threadID, DeliveredCount: 1}
					summary.Normalize()
					return summary, nil
				}
			},
		},
		{
			name: "thread not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "thread belongs to different session",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "archived thread is treated as not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					now := time.Now()
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, ArchivedAt: &now}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)
			svc.SetThreadInboxStore(deps.inboxStore)

			thread, err := svc.GetThread(context.Background(), orgID, sessionID, threadID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.Equal(t, threadID, thread.ID, "should return the correct thread")
			require.NotNil(t, thread.InboxDelivery, "GetThread should attach delivery state when the inbox store is wired")
			require.Equal(t, models.ThreadInboxSummaryStateDelivered, thread.InboxDelivery.State, "GetThread should expose live-delivered state")
		})
	}
}

func TestService_SendMessage(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name      string
		input     SendMessageInput
		setupDeps func(deps *testDeps)
		expectErr error
	}{
		{
			name: "success",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				UserID:    &userID,
				Message:   "continue",
				Images:    []string{"img1.png"},
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 42
					msg.CreatedAt = time.Now()
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, queue, jobType string, payload any, _ int, dedupeKey *string) (uuid.UUID, error) {
					require.Equal(t, "agent", queue, "thread messages should use the agent queue")
					require.Equal(t, "continue_session", jobType, "thread messages should reuse the continue-session worker")
					require.IsType(t, map[string]string{}, payload, "thread message payload should be string keyed")
					require.Equal(t, threadID.String(), payload.(map[string]string)["thread_id"], "thread id should be included for worker attribution")
					require.NotNil(t, dedupeKey, "continue-session enqueue should carry a dedupe key")
					require.Equal(t, db.ContinueSessionDedupeKey(threadID), *dedupeKey, "continue-session dedupe should be keyed by thread so a concurrent send to a sibling tab is not silently swallowed")
					return uuid.New(), nil
				}
			},
		},
		{
			name: "success with continuation dedupe override",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				UserID:    &userID,
				Message:   "internal follow-up",
				ContinuationDedupeKeyOverride: func() *string {
					key := "continue_session_review_loop:loop:pass:decision"
					return &key
				}(),
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 43
					msg.CreatedAt = time.Now()
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, queue, jobType string, payload any, _ int, dedupeKey *string) (uuid.UUID, error) {
					require.Equal(t, "agent", queue, "thread messages should use the agent queue")
					require.Equal(t, "continue_session", jobType, "thread messages should reuse the continue-session worker")
					require.IsType(t, map[string]string{}, payload, "thread message payload should be string keyed")
					require.Equal(t, threadID.String(), payload.(map[string]string)["thread_id"], "thread id should still be included for worker attribution")
					require.NotNil(t, dedupeKey, "override enqueue should carry a dedupe key")
					require.Equal(t, "continue_session_review_loop:loop:pass:decision", *dedupeKey, "internal follow-up should use the caller-provided dedupe key")
					return uuid.New(), nil
				}
			},
		},
		{
			name: "claims parent session before creating thread message",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				claimedSession := false
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					claimedSession = true
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, _ *models.SessionMessage) error {
					require.True(t, claimedSession, "SendMessage should claim the parent session before creating the message")
					return nil
				}
			},
		},
		{
			name: "enqueues concurrent continue_session when parent session is already running due to sibling",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				ownerNodeID := "worker-1"
				containerID := "sandbox-live"
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				// The parent session ClaimIdle fails because a sibling tab
				// already moved the session into running state. The thread claim
				// above means this tab is admitted under the sibling cap, so the
				// service should enqueue an independent thread-scoped job rather
				// than parking the message behind the active sibling.
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("session already running")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, ContainerID: &containerID, WorkerNodeID: &ownerNodeID}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					require.Equal(t, "hi", msg.Content, "concurrent sibling message should preserve content")
					require.Equal(t, 2, msg.TurnNumber, "concurrent sibling message should use the claimed thread's next turn")
					msg.ID = 99
					return nil
				}
				deps.threadStore.updateStatusFn = func(_ context.Context, _, tid uuid.UUID, status models.ThreadStatus) error {
					require.Failf(t, "concurrent sibling send must not release the admitted thread", "tid=%s status=%s", tid, status)
					return nil
				}
				deps.threadStore.incrementPendingFn = func(_ context.Context, _, tid uuid.UUID) error {
					require.Failf(t, "concurrent sibling send must not increment pending count", "tid=%s", tid)
					return nil
				}
				deps.jobStore.enqueueWithOptsFn = func(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
					require.Equal(t, "agent", opts.Queue, "concurrent sibling send should use the agent queue")
					require.Equal(t, "continue_session", opts.JobType, "concurrent sibling send should enqueue a continue_session job")
					require.NotNil(t, opts.DedupeKey, "concurrent sibling send should carry a dedupe key")
					require.Equal(t, db.ContinueSessionDedupeKey(threadID), *opts.DedupeKey, "concurrent sibling send should dedupe by thread")
					require.NotNil(t, opts.TargetNodeID, "concurrent sibling send should pin to the active sandbox owner")
					require.Equal(t, ownerNodeID, *opts.TargetNodeID, "concurrent sibling send should target the active sandbox owner")
					payload, ok := opts.Payload.(map[string]string)
					require.True(t, ok, "concurrent sibling payload should be string keyed")
					require.Equal(t, sessionID.String(), payload["session_id"], "concurrent sibling payload should carry session id")
					require.Equal(t, threadID.String(), payload["thread_id"], "concurrent sibling payload should carry thread id")
					return uuid.New(), nil
				}
			},
			expectErr: nil,
		},
		{
			name: "thread not found",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			// When the target thread is mid-turn, SendMessage queues the
			// message (creates the row + bumps pending_message_count) instead
			// of rejecting. The orchestrator drains the queue when the
			// in-flight turn completes.
			name: "thread busy queues message",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "queued",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 3, Status: models.ThreadStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					require.Equal(t, "queued", msg.Content)
					require.Equal(t, 5, msg.TurnNumber, "queued message belongs to the turn after the in-flight one")
					msg.ID = 7
					return nil
				}
				deps.threadStore.incrementPendingFn = func(_ context.Context, _, tid uuid.UUID) error {
					require.Equal(t, threadID, tid)
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					t.Fatalf("queue-only path must not enqueue a continue_session job")
					return uuid.UUID{}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					t.Fatalf("queue-only path must not re-claim the parent session")
					return models.Session{}, nil
				}
			},
		},
		{
			name: "thread busy queues message and proactively recovers lost owner",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "queued",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 3, Status: models.ThreadStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 7
					return nil
				}
			},
		},
		{
			name: "thread busy owner-loss recovery error does not roll back queued message",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "queued",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 3, Status: models.ThreadStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 8
					return nil
				}
			},
		},
		{
			// Resolving review comments on a queued send is rejected: the
			// resolution pass is keyed on the in-flight turn and we cannot
			// atomically commit it alongside a message that won't be
			// consumed until a later turn.
			name: "thread busy with comment resolution rejected",
			input: SendMessageInput{
				SessionID:               sessionID,
				OrgID:                   orgID,
				ThreadID:                threadID,
				Message:                 "addressed comments",
				ResolveReviewCommentIDs: []uuid.UUID{uuid.New()},
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
			expectErr: ErrReviewCommentsNotConfigured,
		},
		{
			name: "resume cap-saturated thread queues the message instead of rejecting",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "follow up after completion",
			},
			setupDeps: func(deps *testDeps) {
				// Resume path hits the same per-session running cap as the
				// idle claim path. The service should treat this identically
				// to the idle-cap case: queue the message against the still-
				// unclaimed thread instead of failing. Per-status coverage of
				// the resume happy path lives in
				// TestService_SendMessage_ResumesAcrossAllResumableStatuses.
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 3, Status: models.ThreadStatusCompleted}, nil
				}
				deps.threadStore.claimForResumeFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, db.ErrThreadRunningLimitReached
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 100
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					require.Fail(t, "queued resume message should not enqueue a continue_session")
					return uuid.Nil, nil
				}
			},
		},
		{
			name: "running limit reached queues the message instead of rejecting it",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				// Phase 2: when the DB-level CTE rejects the claim because
				// the per-session running cap is full, surface a
				// distinguishable error so the composer can offer to queue
				// the message instead of telling the user they failed.
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, db.ErrThreadRunningLimitReached
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 2, Status: models.ThreadStatusIdle}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 77
					require.NotNil(t, msg.ThreadID, "queued thread message should retain thread attribution")
					require.Equal(t, threadID, *msg.ThreadID, "queued thread message should target the requested thread")
					require.Equal(t, 3, msg.TurnNumber, "queued message should use the next turn number for that thread")
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					require.Fail(t, "queued thread message should not enqueue work until a running slot opens")
					return uuid.Nil, nil
				}
			},
		},
		{
			name: "session mismatch reverts to idle",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "message creation failure reverts to idle",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, _ *models.SessionMessage) error {
					return fmt.Errorf("db error")
				}
			},
		},
		{
			name: "message creation failure does not revert sibling-owned session to idle",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("session already running")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, _ *models.SessionMessage) error {
					return fmt.Errorf("db error")
				}
			},
		},
		{
			name: "enqueue failure reverts to idle",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 42
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					return uuid.Nil, fmt.Errorf("queue down")
				}
			},
			expectErr: ErrEnqueueFailed,
		},
		{
			name: "sibling-owned session enqueues thread-scoped job without reverting the session",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("session already running")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 42
					return nil
				}
				deps.threadStore.updateStatusFn = func(_ context.Context, _, _ uuid.UUID, status models.ThreadStatus) error {
					require.Failf(t, "sibling-owned send should keep the admitted thread running", "status=%s", status)
					return nil
				}
				deps.threadStore.incrementPendingFn = func(_ context.Context, _, tid uuid.UUID) error {
					require.Failf(t, "sibling-owned send should not increment pending messages", "tid=%s", tid)
					return nil
				}
				deps.jobStore.enqueueWithOptsFn = func(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
					require.Equal(t, "continue_session", opts.JobType, "sibling-owned send should enqueue a thread-scoped continue_session job")
					require.Nil(t, opts.TargetNodeID, "sibling-owned send should leave worker routing open when no owner node is recorded")
					return uuid.New(), nil
				}
			},
		},
		{
			name: "resumes a completed session via ClaimForResume",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				// Mirrors sessions.go:1953-1963. The original "failed to
				// create message" bug fired when a thread tab tried to send
				// to a completed session — ClaimIdle returned no rows and
				// the service had no fallback. With ClaimForResume wired,
				// the same flow now succeeds for any resumable status.
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 4, Status: models.ThreadStatusRunning}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows in result set")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusCompleted, CurrentTurn: 4}, nil
				}
				resumed := false
				deps.sessionStore.claimForResumeFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					resumed = true
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, CurrentTurn: 4}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					require.True(t, resumed, "ClaimForResume should fire before message create when ClaimIdle returns no rows")
					msg.ID = 7
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					return uuid.New(), nil
				}
			},
		},
		{
			name: "returns ErrSessionNotResumable when ClaimForResume returns no rows",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				// Race window: the session was 'completed' when GetByID
				// read it but transitioned to a non-resumable state by the
				// time ClaimForResume ran (e.g. another caller already
				// resumed it and a worker re-completed it). The handler
				// surfaces this as 409 NOT_RESUMABLE.
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusCompleted}, nil
				}
				deps.sessionStore.claimForResumeFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
				revertedThread := false
				deps.threadStore.updateStatusFn = func(_ context.Context, _, _ uuid.UUID, status models.ThreadStatus) error {
					if status == models.ThreadStatusIdle {
						revertedThread = true
					}
					return nil
				}
				t.Cleanup(func() {
					require.True(t, revertedThread, "thread must be reverted to idle when neither claim succeeds")
				})
			},
			expectErr: ErrSessionNotResumable,
		},
		{
			name: "returns ErrSessionSnapshotExpired when sandbox is destroyed",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				// Snapshots expire after 30 days. Mirrors sessions.go:1835:
				// surface a distinct sentinel so the handler can render
				// 410 Gone instead of 409, telling the user this session
				// can never be resumed (vs. a transient state issue).
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusCompleted, SandboxState: models.SandboxStateDestroyed}, nil
				}
				deps.sessionStore.claimForResumeFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					t.Errorf("ClaimForResume must not be called when the sandbox is destroyed")
					return models.Session{}, nil
				}
			},
			expectErr: ErrSessionSnapshotExpired,
		},
		{
			name: "resume race to running queues the message instead of rejecting it",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "continue after race",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				readCount := 0
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					readCount++
					if readCount == 1 {
						return models.SessionThread{
							ID:          threadID,
							SessionID:   sessionID,
							OrgID:       orgID,
							CurrentTurn: 4,
							Status:      models.ThreadStatusCompleted,
						}, nil
					}
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						CurrentTurn: 4,
						Status:      models.ThreadStatusRunning,
					}, nil
				}
				deps.threadStore.claimForResumeFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, pgx.ErrNoRows
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 88
					require.Equal(t, 6, msg.TurnNumber, "race-queued message should land behind the in-flight turn")
					return nil
				}
				deps.threadStore.incrementPendingFn = func(_ context.Context, _, tid uuid.UUID) error {
					require.Equal(t, threadID, tid, "race-queued send should increment pending messages on the requested thread")
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					require.Fail(t, "race-queued send should not enqueue a concurrent continue_session job")
					return uuid.Nil, nil
				}
			},
		},
		{
			name: "preserves original status on message create failure after resume",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				// After ClaimForResume moves a 'completed' session to
				// 'running' and the message create then fails, the revert
				// must put the session back to 'completed' (not 'idle').
				// Otherwise a transient DB error would silently re-arm a
				// finished session as a new task in the user's idle list.
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusCompleted}, nil
				}
				deps.sessionStore.claimForResumeFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, _ *models.SessionMessage) error {
					return fmt.Errorf("db error")
				}
				revertedToOriginal := false
				deps.sessionStore.updateStatusFn = func(_ context.Context, _, _ uuid.UUID, status models.SessionStatus) error {
					if status == models.SessionStatusCompleted {
						revertedToOriginal = true
					}
					return nil
				}
				t.Cleanup(func() {
					require.True(t, revertedToOriginal, "session must revert to its pre-claim status (completed) on send failure, not idle")
				})
			},
		},
		{
			name: "skips session revert when sibling tab is mid-turn",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				// Sibling-running case: ClaimIdle fails, GetByID returns
				// running, no claim is taken. If message create then fails,
				// reverting the session to idle would yank the running
				// sibling — so the revert must skip the session entirely
				// and only put the thread back to idle.
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, _ *models.SessionMessage) error {
					return fmt.Errorf("db error")
				}
				deps.sessionStore.updateStatusFn = func(_ context.Context, _, _ uuid.UUID, _ models.SessionStatus) error {
					t.Errorf("session UpdateStatus must not be called when sibling is mid-turn")
					return nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			ownerLoss := &mockOwnerLossRecovery{}
			switch tt.name {
			case "thread busy queues message and proactively recovers lost owner":
				svc.SetOwnerLossOrchestrator(ownerLoss)
			case "thread busy owner-loss recovery error does not roll back queued message":
				ownerLoss.err = errors.New("recovery temporarily unavailable")
				svc.SetOwnerLossOrchestrator(ownerLoss)
			}
			tt.setupDeps(deps)

			result, err := svc.SendMessage(context.Background(), tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				require.Nil(t, result, "should not return a result on error")
				if tt.name == "enqueue failure does not revert sibling-owned session to idle" || tt.name == "message creation failure does not revert sibling-owned session to idle" {
					require.Empty(t, deps.sessionStore.updateCalls, "SendMessage should leave the parent session running when a sibling thread already owns it")
				}
				return
			}
			switch tt.name {
			case "message creation failure reverts to idle",
				"preserves original status on message create failure after resume",
				"skips session revert when sibling tab is mid-turn":
				require.Error(t, err, "should return error on message creation failure")
				return
			}
			if tt.name == "message creation failure does not revert sibling-owned session to idle" {
				require.Error(t, err, "should return error on message creation failure even when sibling owns the parent session")
				require.Empty(t, deps.sessionStore.updateCalls, "SendMessage should leave the parent session running when a sibling thread already owns it")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.NotNil(t, result, "should return a result")
			require.NotNil(t, result.Message, "should return a message")
			require.Equal(t, models.MessageRoleUser, result.Message.Role, "should set role to user")
			if tt.name == "thread busy queues message and proactively recovers lost owner" ||
				tt.name == "thread busy owner-loss recovery error does not roll back queued message" {
				require.Len(t, ownerLoss.calls, 1, "queued send should trigger proactive owner-loss recovery")
				require.Equal(t, orgID, ownerLoss.calls[0].orgID, "owner-loss recovery should receive org id")
				require.Equal(t, sessionID, ownerLoss.calls[0].sessionID, "owner-loss recovery should receive session id")
				require.NotNil(t, ownerLoss.calls[0].threadID, "owner-loss recovery should receive thread id")
				require.Equal(t, threadID, *ownerLoss.calls[0].threadID, "owner-loss recovery should target queued thread")
			}
			if tt.name == "running limit reached queues the message instead of rejecting it" {
				require.Equal(t, []uuid.UUID{threadID}, deps.threadStore.pendingCalls, "queued send should increment the pending message count for that thread")
			}
		})
	}
}

func TestService_SendMessage_ProactiveOwnerLossRecoveryForSiblingQueue(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	tests := []struct {
		name  string
		setup func(*testDeps)
	}{
		{
			name: "running thread limit reached",
			setup: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, db.ErrThreadRunningLimitReached
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 2, Status: models.ThreadStatusIdle}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 77
					return nil
				}
			},
		},
		{
			name: "normal idle send does not trigger recovery",
			setup: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 7
					return nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			ownerLoss := &mockOwnerLossRecovery{}
			svc.SetOwnerLossOrchestrator(ownerLoss)
			tt.setup(deps)

			result, err := svc.SendMessage(context.Background(), SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hello",
			})
			require.NoError(t, err, "send should succeed")
			require.NotNil(t, result, "send should return a result")

			if tt.name == "normal idle send does not trigger recovery" {
				require.Empty(t, ownerLoss.calls, "normal enqueue path should not trigger proactive owner-loss recovery")
				return
			}
			require.Len(t, ownerLoss.calls, 1, "queue-only sibling path should trigger proactive owner-loss recovery")
			require.Equal(t, orgID, ownerLoss.calls[0].orgID, "owner-loss recovery should receive org id")
			require.Equal(t, sessionID, ownerLoss.calls[0].sessionID, "owner-loss recovery should receive session id")
			require.NotNil(t, ownerLoss.calls[0].threadID, "owner-loss recovery should receive thread id")
			require.Equal(t, threadID, *ownerLoss.calls[0].threadID, "owner-loss recovery should target queued thread")
		})
	}
}

func TestService_SendMessage_AppendsDurableInboxEntryWhenConfigured(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()

	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)

	deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 2, Status: models.ThreadStatusRunning}, nil
	}
	deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
		msg.ID = 123
		msg.CreatedAt = time.Now()
		return nil
	}
	deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
		require.Len(t, deps.inboxStore.appendCalls, 1, "SendMessage should append the durable inbox entry before enqueueing work")
		return uuid.New(), nil
	}

	result, err := svc.SendMessage(context.Background(), SendMessageInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		UserID:    &userID,
		Message:   "continue",
	})

	require.NoError(t, err, "SendMessage should not return an error")
	require.NotNil(t, result, "SendMessage should return a result")
	require.Len(t, deps.inboxStore.appendCalls, 1, "SendMessage should append exactly one durable inbox entry")
	require.Equal(t, sessionID, deps.inboxStore.appendCalls[0].SessionID, "inbox entry should carry the session id")
	require.Equal(t, threadID, deps.inboxStore.appendCalls[0].ThreadID, "inbox entry should carry the thread id")
	require.Equal(t, int64(123), deps.inboxStore.appendCalls[0].MessageID, "inbox entry should point at the persisted message")
	require.Equal(t, models.ThreadInboxEntryTypeUserMessage, deps.inboxStore.appendCalls[0].EntryType, "user sends should append user-message inbox entries")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(deps.inboxStore.appendCalls[0].Payload, &payload), "inbox payload should be valid JSON")
	require.Equal(t, "continue", payload["content"], "inbox payload should include message content for live delivery")
}

func TestService_SendMessage_AppliesInboxBackpressureWithoutClientMessageID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)
	deps.inboxStore.countPendingByThreadFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (int, error) {
		require.Equal(t, orgID, gotOrgID, "backpressure should count pending entries inside the org")
		require.Equal(t, threadID, gotThreadID, "backpressure should count pending entries for the target thread")
		return maxThreadInboxPending, nil
	}

	result, err := svc.SendMessage(context.Background(), SendMessageInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		Message:   "continue without idempotency key",
	})

	require.ErrorIs(t, err, ErrThreadInboxBackpressure, "SendMessage should enforce durable inbox backpressure even without a client message id")
	require.Nil(t, result, "SendMessage should not create a message once backpressure is exceeded")
	require.Empty(t, deps.inboxStore.appendCalls, "SendMessage should reject before appending more inbox entries")
}

func TestService_SendMessage_ReturnsExistingMessageForClientMessageID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	existingMessageID := int64(99)
	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)

	deps.inboxStore.getByClientMessageIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, clientMessageID string) (models.ThreadInboxEntry, error) {
		require.Equal(t, orgID, gotOrgID, "idempotency lookup should be org scoped")
		require.Equal(t, threadID, gotThreadID, "idempotency lookup should be thread scoped")
		require.Equal(t, "agent-tool-repeat", clientMessageID, "idempotency lookup should use the client message id")
		return models.ThreadInboxEntry{MessageID: existingMessageID}, nil
	}
	deps.messageStore.getByIDFn = func(_ context.Context, gotOrgID uuid.UUID, id int64) (models.SessionMessage, error) {
		require.Equal(t, orgID, gotOrgID, "existing message lookup should be org scoped")
		require.Equal(t, existingMessageID, id, "existing message lookup should use the inbox message id")
		return models.SessionMessage{ID: id, OrgID: gotOrgID, SessionID: sessionID, ThreadID: &threadID, Content: "already accepted"}, nil
	}
	deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{}, fmt.Errorf("claim should not be called for idempotent sends")
	}

	result, err := svc.SendMessage(context.Background(), SendMessageInput{
		SessionID:       sessionID,
		OrgID:           orgID,
		ThreadID:        threadID,
		ClientMessageID: "agent-tool-repeat",
		Message:         "run tests again",
	})
	require.NoError(t, err, "SendMessage should return the existing message without mutating state")
	require.NotNil(t, result, "SendMessage should return a result for idempotent sends")
	require.Equal(t, existingMessageID, result.Message.ID, "SendMessage should return the previously accepted message")
	require.Empty(t, deps.inboxStore.appendCalls, "SendMessage should not append a second inbox entry")
}

func TestService_SendMessage_RunningThreadEnqueuesLiveInboxDelivery(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()

	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)

	deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{}, fmt.Errorf("thread is already running")
	}
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{
			ID:          threadID,
			SessionID:   sessionID,
			OrgID:       orgID,
			CurrentTurn: 4,
			Status:      models.ThreadStatusRunning,
		}, nil
	}
	deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
		msg.ID = 456
		msg.CreatedAt = time.Now()
		return nil
	}
	var enqueued []db.EnqueueOpts
	deps.jobStore.enqueueWithOptsFn = func(_ context.Context, gotOrgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
		require.Equal(t, orgID, gotOrgID, "live delivery job should be scoped to the message org")
		enqueued = append(enqueued, opts)
		return uuid.New(), nil
	}

	result, err := svc.SendMessage(context.Background(), SendMessageInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		UserID:    &userID,
		Message:   "please keep going",
	})

	require.NoError(t, err, "SendMessage should accept same-thread input while the runtime is running")
	require.NotNil(t, result, "SendMessage should return the accepted message")
	require.Equal(t, []uuid.UUID{threadID}, deps.threadStore.pendingCalls, "running-thread send should increment pending messages")
	require.Len(t, deps.inboxStore.appendCalls, 1, "running-thread send should append durable inbox state")
	require.Len(t, enqueued, 1, "running-thread send should enqueue live inbox delivery")
	require.Equal(t, "agent", enqueued[0].Queue, "live delivery should use the agent queue")
	require.Equal(t, "deliver_thread_inbox", enqueued[0].JobType, "live delivery should use the thread inbox job type")
	require.Equal(t, 7, enqueued[0].Priority, "live delivery should be prioritized ahead of regular continuations")
	require.NotNil(t, enqueued[0].DedupeKey, "live delivery enqueue should dedupe repeated notifications for the same thread")
	require.Equal(t, "deliver_thread_inbox:"+threadID.String(), *enqueued[0].DedupeKey, "live delivery dedupe should be keyed by thread")
	payload, ok := enqueued[0].Payload.(map[string]string)
	require.True(t, ok, "live delivery payload should be a string map")
	require.Equal(t, sessionID.String(), payload["session_id"], "live delivery payload should carry the session id")
	require.Equal(t, threadID.String(), payload["thread_id"], "live delivery payload should carry the thread id")
	require.Equal(t, orgID.String(), payload["org_id"], "live delivery payload should carry the org id")
	require.Nil(t, enqueued[0].TargetNodeID, "live delivery job should let the runtime registry route to the current owner")
}

func TestService_SendMessage_LiveInboxDeliveryEnqueueFailureDoesNotRejectAcceptedMessage(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()

	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)

	deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{}, fmt.Errorf("thread is already running")
	}
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{
			ID:          threadID,
			SessionID:   sessionID,
			OrgID:       orgID,
			CurrentTurn: 1,
			Status:      models.ThreadStatusRunning,
		}, nil
	}
	deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
		msg.ID = 789
		msg.CreatedAt = time.Now()
		return nil
	}
	deps.jobStore.enqueueWithOptsFn = func(_ context.Context, _ uuid.UUID, _ db.EnqueueOpts) (uuid.UUID, error) {
		return uuid.Nil, fmt.Errorf("queue unavailable")
	}

	result, err := svc.SendMessage(context.Background(), SendMessageInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		UserID:    &userID,
		Message:   "this should still be accepted",
	})

	require.NoError(t, err, "SendMessage should not reject already-committed input when live-delivery notification fails")
	require.NotNil(t, result, "SendMessage should return the accepted message despite notification failure")
	require.Len(t, deps.inboxStore.appendCalls, 1, "SendMessage should keep durable inbox state for later recovery")
	require.Equal(t, []uuid.UUID{threadID}, deps.threadStore.pendingCalls, "SendMessage should still reflect queued input in pending counts")
}

func TestService_ListRecoverableInboxEntries(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()

	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)

	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "GetByID should be scoped to the org")
		require.Equal(t, threadID, gotThreadID, "GetByID should fetch the requested thread")
		return models.SessionThread{ID: threadID, OrgID: orgID, SessionID: sessionID, Status: models.ThreadStatusRunning}, nil
	}
	reason := "runtime lease expired after live delivery before ack"
	expected := []models.ThreadInboxEntry{{
		ID:            entryID,
		OrgID:         orgID,
		SessionID:     sessionID,
		ThreadID:      threadID,
		DeliveryState: models.ThreadInboxDeliveryStateUnknownDelivery,
		LastError:     &reason,
	}}
	deps.inboxStore.listRecoverableFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error) {
		require.Equal(t, orgID, gotOrgID, "recoverable lookup should be scoped to the org")
		require.Equal(t, threadID, gotThreadID, "recoverable lookup should be scoped to the thread")
		require.Greater(t, limit, 0, "recoverable lookup should use a bounded positive limit")
		return expected, nil
	}

	entries, err := svc.ListRecoverableInboxEntries(context.Background(), orgID, sessionID, threadID)

	require.NoError(t, err, "ListRecoverableInboxEntries should not return an error")
	require.Equal(t, expected, entries, "ListRecoverableInboxEntries should return recoverable entries for the requested thread")
}

func TestService_RetryInboxEntryReturnsEntryToPendingAndNotifiesDelivery(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()

	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)

	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "GetByID should be scoped to the org")
		require.Equal(t, threadID, gotThreadID, "GetByID should fetch the requested thread")
		return models.SessionThread{ID: threadID, OrgID: orgID, SessionID: sessionID, Status: models.ThreadStatusRunning}, nil
	}
	var enqueued []db.EnqueueOpts
	deps.jobStore.enqueueWithOptsFn = func(_ context.Context, gotOrgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
		require.Equal(t, orgID, gotOrgID, "retry notification should be scoped to the org")
		enqueued = append(enqueued, opts)
		return uuid.New(), nil
	}

	entry, err := svc.RetryInboxEntry(context.Background(), orgID, sessionID, threadID, entryID, false)

	require.NoError(t, err, "RetryInboxEntry should not return an error")
	require.Equal(t, models.ThreadInboxDeliveryStatePending, entry.DeliveryState, "RetryInboxEntry should return the entry to pending")
	require.Equal(t, []uuid.UUID{entryID}, deps.inboxStore.retryCalls, "RetryInboxEntry should retry the requested inbox entry")
	require.Equal(t, []bool{false}, deps.inboxStore.retryAllowUnknownCalls, "RetryInboxEntry should not replay unknown-delivery entries by default")
	require.Len(t, enqueued, 1, "RetryInboxEntry should notify live delivery after making the entry pending")
	require.Equal(t, "deliver_thread_inbox", enqueued[0].JobType, "RetryInboxEntry should reuse the live inbox delivery job")
	require.NotNil(t, enqueued[0].DedupeKey, "RetryInboxEntry should dedupe delivery notifications by thread")
	require.Equal(t, "deliver_thread_inbox:"+threadID.String(), *enqueued[0].DedupeKey, "RetryInboxEntry should key notification dedupe by thread")
}

func TestService_RetryInboxEntryEnqueuesContinuationWhenRuntimeIsGone(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()

	svc, deps := newTestService(t)
	svc.SetThreadInboxStore(deps.inboxStore)
	svc.SetThreadRuntimeStore(&mockRuntimeOwnerStore{})

	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, OrgID: orgID, SessionID: sessionID, Status: models.ThreadStatusRunning}, nil
	}
	var enqueued []db.EnqueueOpts
	deps.jobStore.enqueueWithOptsFn = func(_ context.Context, gotOrgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
		require.Equal(t, orgID, gotOrgID, "retry continuation should be scoped to the org")
		enqueued = append(enqueued, opts)
		return uuid.New(), nil
	}

	_, err := svc.RetryInboxEntry(context.Background(), orgID, sessionID, threadID, entryID, true)

	require.NoError(t, err, "RetryInboxEntry should not fail when no live runtime owns the tab")
	require.Equal(t, []bool{true}, deps.inboxStore.retryAllowUnknownCalls, "explicit replay consent should be forwarded to the store")
	require.Len(t, enqueued, 1, "RetryInboxEntry should enqueue a thread continuation when there is no active runtime")
	require.Equal(t, "continue_session", enqueued[0].JobType, "RetryInboxEntry should resume the tab when live delivery is impossible")
	payload, ok := enqueued[0].Payload.(map[string]string)
	require.True(t, ok, "continuation payload should be string keyed")
	require.Equal(t, threadID.String(), payload["thread_id"], "continuation payload should address the retried thread")
}

func TestService_SendMessage_AnswersThreadHumanInputRequest(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()
	requestID := uuid.New()
	now := time.Now()
	answerText := "Use the existing implementation"

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(31), now))
	mock.ExpectQuery("UPDATE session_human_input_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(threadHumanInputRequestColumns).
			AddRow(threadHumanInputRequestRow(requestID, orgID, sessionID, threadID, userID, answerText, now)...))
	mock.ExpectCommit()

	svc, deps := newTestService(t)
	deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{}, fmt.Errorf("no rows")
	}
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 2, Status: models.ThreadStatusAwaitingInput}, nil
	}
	deps.threadStore.claimForResumeFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 2, Status: models.ThreadStatusRunning}, nil
	}
	deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{}, fmt.Errorf("no rows")
	}
	deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusAwaitingInput, CurrentTurn: 5}, nil
	}
	deps.sessionStore.claimForResumeFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
		return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, CurrentTurn: 5}, nil
	}
	deps.jobStore.enqueueWithOptsFn = func(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
		require.Equal(t, "agent", opts.Queue, "thread human-input resume should use the agent queue")
		require.Equal(t, "continue_session", opts.JobType, "thread human-input resume should enqueue continue_session")
		require.NotNil(t, opts.DedupeKey, "thread human-input resume should use a dedupe key")
		require.Equal(t, db.ContinueSessionDedupeKey(threadID), *opts.DedupeKey, "thread human-input resume should dedupe by thread")
		payload, ok := opts.Payload.(map[string]string)
		require.True(t, ok, "thread human-input resume payload should be string keyed")
		require.Equal(t, sessionID.String(), payload["session_id"], "thread human-input resume should carry session id")
		require.Equal(t, threadID.String(), payload["thread_id"], "thread human-input resume should carry thread id")
		require.Equal(t, requestID.String(), payload["human_input_request_id"], "thread human-input resume should carry answered request id")
		return uuid.New(), nil
	}
	svc.SetReviewCommentResolver(mock, db.NewSessionReviewCommentStore(mock))
	svc.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(mock))

	result, err := svc.SendMessage(context.Background(), SendMessageInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		UserID:    &userID,
		Message:   answerText,
	})
	require.NoError(t, err, "thread composer should answer pending free-text human input")
	require.NotNil(t, result, "thread composer should return a result")
	require.NotNil(t, result.AnsweredHumanInput, "thread composer should return the answered human input request")
	require.Equal(t, requestID, result.AnsweredHumanInput.ID, "thread composer should report the answered human input request")
	require.Equal(t, models.HumanInputRequestStatusAnswered, result.AnsweredHumanInput.Status, "thread composer should mark the human input request answered")
	require.NoError(t, mock.ExpectationsWereMet(), "all transaction expectations should be met")
}

func TestService_QueueMessageWaitingForSlot_DoesNotAnswerHumanInputRequest(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	userID := uuid.New()
	svc, deps := newTestService(t)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{
			ID:          threadID,
			SessionID:   sessionID,
			OrgID:       orgID,
			CurrentTurn: 4,
			Status:      models.ThreadStatusAwaitingInput,
		}, nil
	}
	var created *models.SessionMessage
	deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
		created = msg
		return nil
	}
	svc.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(nil))

	result, err := svc.queueMessageWaitingForSlot(context.Background(), SendMessageInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		UserID:    &userID,
		Message:   "Use the existing implementation",
	})

	require.NoError(t, err, "queued awaiting_input messages should not need a human-input transaction")
	require.NotNil(t, result, "queue path should return a result")
	require.Nil(t, result.AnsweredHumanInput, "queue path should not mark human input answered before a resume job can carry the request id")
	require.NotNil(t, created, "queue path should still create the user message")
	require.Equal(t, []uuid.UUID{threadID}, deps.threadStore.pendingCalls, "queue path should increment pending message count")
}

// TestService_SendMessage_ResumesAcrossAllResumableStatuses pins the
// invariant that the resumable-status set is the source of truth for
// "thread accepts a follow-up message via ClaimForResumeInSession". A
// future change that narrows ResumableThreadStatuses (or adds a status to
// it without wiring the resume path) must not pass silently — this test
// exercises every status the model declares resumable and verifies the
// resume claim fires for each.
func TestService_SendMessage_ResumesAcrossAllResumableStatuses(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	// Drive the test off models.ResumableThreadStatuses directly so the
	// guard fails the moment that constant changes shape.
	for _, status := range models.ResumableThreadStatuses {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{}, fmt.Errorf("no rows")
			}
			deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 5, Status: status}, nil
			}
			resumeCalled := false
			deps.threadStore.claimForResumeFn = func(_ context.Context, _, sid, tid uuid.UUID) (models.SessionThread, error) {
				resumeCalled = true
				require.Equal(t, sessionID, sid, "should resume against the requested session")
				require.Equal(t, threadID, tid, "should resume the requested thread")
				return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 5, Status: models.ThreadStatusRunning}, nil
			}
			deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
				require.True(t, resumeCalled, "message create should run after the resume claim succeeded")
				msg.ID = 1
				require.Equal(t, 6, msg.TurnNumber, "resumed thread should advance to the next turn after CurrentTurn")
				return nil
			}
			deps.threadStore.incrementPendingFn = func(_ context.Context, _, _ uuid.UUID) error {
				t.Fatalf("resumed threads should run a new turn, not queue a pending message")
				return nil
			}

			result, err := svc.SendMessage(context.Background(), SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "follow up",
			})
			require.NoError(t, err, "resume path should accept a follow-up for status %q", status)
			require.NotNil(t, result, "resume path should return a result for status %q", status)
			require.NotNil(t, result.Message, "resume path should return a message for status %q", status)
			require.True(t, resumeCalled, "resume claim must fire for status %q", status)
		})
	}
}

// TestService_SendMessage_ResolvesReviewComments exercises the
// comment-resolution path end-to-end against a real tx, using pgxmock as
// the txStarter and a real db.SessionReviewCommentStore so the SQL
// invariants (INSERT message → SELECT comments → UPDATE comments → COMMIT)
// run inside a single transaction. Mirrors the session-level
// TestSessionHandler_SendMessage_ResolvesReviewComments coverage so the
// thread send path inherits the same atomic guarantee.
func TestService_SendMessage_ResolvesReviewComments(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	commentRowColumns := []string{
		"id", "session_id", "org_id", "user_id", "file_path",
		"line_number", "diff_side", "body", "resolved", "resolved_at", "resolved_by_pass",
		"pass_number", "created_at", "updated_at",
	}

	primeClaim := func(deps *testDeps) {
		deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
		}
		// GetByID is called once per resolution path to pick up CurrentTurn
		// for the resolution pass; CurrentTurn=2 → resolved_by_pass=2.
		deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
			return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, CurrentTurn: 2}, nil
		}
	}

	t.Run("rejects when resolver is not configured", func(t *testing.T) {
		t.Parallel()
		svc, deps := newTestService(t)
		primeClaim(deps)
		// No SetReviewCommentResolver — the service should fail-fast before
		// claiming any state.
		_, err := svc.SendMessage(context.Background(), SendMessageInput{
			SessionID:               sessionID,
			OrgID:                   orgID,
			ThreadID:                threadID,
			Message:                 "hi",
			ResolveReviewCommentIDs: []uuid.UUID{uuid.New()},
		})
		require.ErrorIs(t, err, ErrReviewCommentsNotConfigured, "missing plumbing should be a configuration error, not a 500")
	})

	t.Run("commits message and resolution in the same tx", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		commentID := uuid.New()
		commentUserID := uuid.New()

		// Tx-bracketed SQL: BEGIN → INSERT message → SELECT comments → UPDATE
		// comments → COMMIT. Any reordering breaks the atomic guarantee, so
		// pgxmock's default in-order matching is exactly the contract we want.
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(7), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(commentRowColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						42, "right", "fix this", false, (*time.Time)(nil), (*int)(nil),
						1, now, now),
			)
		resolvedAt := now
		resolvedByPass := 2
		mock.ExpectQuery("UPDATE session_review_comments SET resolved = true").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(commentRowColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						42, "right", "fix this", true, &resolvedAt, &resolvedByPass,
						1, now, now),
			)
		mock.ExpectCommit()

		svc, deps := newTestService(t)
		primeClaim(deps)
		svc.SetReviewCommentResolver(mock, db.NewSessionReviewCommentStore(mock))

		result, err := svc.SendMessage(context.Background(), SendMessageInput{
			SessionID:               sessionID,
			OrgID:                   orgID,
			ThreadID:                threadID,
			Message:                 "address review",
			ResolveReviewCommentIDs: []uuid.UUID{commentID},
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, int64(7), result.Message.ID)
		require.Len(t, result.ResolvedComments, 1, "the resolved comment should come back so the handler can audit it")
		require.Equal(t, commentID, result.ResolvedComments[0].ID)
		require.True(t, result.ResolvedComments[0].Resolved)
		require.Equal(t, 2, *result.ResolvedComments[0].ResolvedByPass, "pass should match session.CurrentTurn at send time")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("answers latest pending question when resuming awaiting_input", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		userID := uuid.New()
		questionID := uuid.New()

		// Tx-bracketed SQL for the awaiting_input resume path: BEGIN →
		// INSERT message → UPDATE the latest pending question to 'answered'
		// → COMMIT. Mirrors the session-level handler's tx shape so the
		// "follow-up message implicitly answers the open question"
		// invariant survives a partial failure.
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(11), now))
		answeredAt := now
		answerText := "yes go"
		mock.ExpectQuery("UPDATE session_questions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{
					"id", "session_id", "org_id", "question_text", "options", "context",
					"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
				}).AddRow(questionID, sessionID, orgID, "are you sure?", []string{"yes go", "abort"}, (*string)(nil),
					(*string)(nil), &answerText, &userID, &answeredAt, "answered", now),
			)
		mock.ExpectCommit()

		svc, deps := newTestService(t)
		// Resume from awaiting_input via the ClaimForResume fallback; this
		// is what sets revertStatus to awaiting_input and triggers the
		// question-answer branch inside createMessageInTx.
		deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
		}
		deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
			return models.Session{}, fmt.Errorf("no rows")
		}
		deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
			return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusAwaitingInput, CurrentTurn: 2}, nil
		}
		deps.sessionStore.claimForResumeFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
			return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, CurrentTurn: 2}, nil
		}
		deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
			return uuid.New(), nil
		}
		svc.SetReviewCommentResolver(mock, db.NewSessionReviewCommentStore(mock))
		svc.SetQuestionStore(db.NewSessionQuestionStore(mock))

		result, err := svc.SendMessage(context.Background(), SendMessageInput{
			SessionID: sessionID,
			OrgID:     orgID,
			ThreadID:  threadID,
			UserID:    &userID,
			Message:   "yes go",
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.AnsweredQuestion, "the answered question should come back so the handler can audit it")
		require.Equal(t, questionID, result.AnsweredQuestion.ID)
		require.Equal(t, models.SessionQuestionStatusAnswered, result.AnsweredQuestion.Status)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("answers latest pending question when awaiting_input thread queues at running limit", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock")
		defer mock.Close()

		userID := uuid.New()
		questionID := uuid.New()
		answerText := "ship it"
		answeredAt := now

		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(21), now))
		mock.ExpectQuery("UPDATE session_questions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{
					"id", "session_id", "org_id", "question_text", "options", "context",
					"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
				}).AddRow(questionID, sessionID, orgID, "continue?", []string{"ship it", "stop"}, (*string)(nil),
					(*string)(nil), &answerText, &userID, &answeredAt, "answered", now),
			)
		mock.ExpectCommit()

		svc, deps := newTestService(t)
		deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{}, fmt.Errorf("no rows")
		}
		deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 4, Status: models.ThreadStatusAwaitingInput}, nil
		}
		deps.threadStore.claimForResumeFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{}, db.ErrThreadRunningLimitReached
		}
		deps.threadStore.incrementPendingFn = func(_ context.Context, _, tid uuid.UUID) error {
			require.Equal(t, threadID, tid, "queued awaiting_input answer should increment the requested thread")
			return nil
		}
		deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
			require.Fail(t, "queued awaiting_input answer should wait for a running slot instead of enqueueing immediately")
			return uuid.Nil, nil
		}
		svc.SetReviewCommentResolver(mock, db.NewSessionReviewCommentStore(mock))
		svc.SetQuestionStore(db.NewSessionQuestionStore(mock))

		result, err := svc.SendMessage(context.Background(), SendMessageInput{
			SessionID: sessionID,
			OrgID:     orgID,
			ThreadID:  threadID,
			UserID:    &userID,
			Message:   answerText,
		})
		require.NoError(t, err, "queued awaiting_input answer should be accepted")
		require.NotNil(t, result, "queued awaiting_input answer should return a result")
		require.NotNil(t, result.AnsweredQuestion, "queued awaiting_input answer should return the answered question for audit")
		require.Equal(t, questionID, result.AnsweredQuestion.ID, "queued awaiting_input answer should report the answered question")
		require.Equal(t, models.SessionQuestionStatusAnswered, result.AnsweredQuestion.Status, "queued awaiting_input answer should mark the question answered")
		require.NoError(t, mock.ExpectationsWereMet(), "all transaction expectations should be met")
	})

	t.Run("answers latest pending human input when awaiting_input thread runs beside sibling", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock")
		defer mock.Close()

		userID := uuid.New()
		requestID := uuid.New()
		answerText := "continue"

		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(22), now))
		mock.ExpectQuery("UPDATE session_human_input_requests").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(threadHumanInputRequestColumns).
				AddRow(threadHumanInputRequestRow(requestID, orgID, sessionID, threadID, userID, answerText, now)...))
		mock.ExpectCommit()

		svc, deps := newTestService(t)
		ownerNodeID := "worker-1"
		containerID := "sandbox-live"
		deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{}, fmt.Errorf("no rows")
		}
		deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 2, Status: models.ThreadStatusAwaitingInput}, nil
		}
		deps.threadStore.claimForResumeFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
			return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 2, Status: models.ThreadStatusRunning}, nil
		}
		deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
			return models.Session{}, fmt.Errorf("session already running")
		}
		deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
			return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning, ContainerID: &containerID, WorkerNodeID: &ownerNodeID}, nil
		}
		deps.threadStore.updateStatusFn = func(_ context.Context, _, tid uuid.UUID, status models.ThreadStatus) error {
			require.Failf(t, "sibling awaiting_input answer should keep the admitted thread running", "tid=%s status=%s", tid, status)
			return nil
		}
		deps.threadStore.incrementPendingFn = func(_ context.Context, _, tid uuid.UUID) error {
			require.Failf(t, "sibling awaiting_input answer should not increment pending messages", "tid=%s", tid)
			return nil
		}
		deps.jobStore.enqueueWithOptsFn = func(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
			require.Equal(t, "continue_session", opts.JobType, "sibling awaiting_input answer should enqueue a thread-scoped continue_session")
			require.NotNil(t, opts.TargetNodeID, "sibling awaiting_input answer should pin to the active sandbox owner")
			require.Equal(t, ownerNodeID, *opts.TargetNodeID, "sibling awaiting_input answer should target the active sandbox owner")
			payload, ok := opts.Payload.(map[string]string)
			require.True(t, ok, "sibling awaiting_input payload should be string keyed")
			require.Equal(t, requestID.String(), payload["human_input_request_id"], "sibling awaiting_input payload should carry the answered request id")
			return uuid.New(), nil
		}
		svc.SetReviewCommentResolver(mock, db.NewSessionReviewCommentStore(mock))
		svc.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(mock))

		result, err := svc.SendMessage(context.Background(), SendMessageInput{
			SessionID: sessionID,
			OrgID:     orgID,
			ThreadID:  threadID,
			UserID:    &userID,
			Message:   answerText,
		})
		require.NoError(t, err, "sibling awaiting_input answer should be accepted")
		require.NotNil(t, result, "sibling awaiting_input answer should return a result")
		require.NotNil(t, result.AnsweredHumanInput, "sibling awaiting_input answer should return the answered human input request for audit")
		require.Equal(t, requestID, result.AnsweredHumanInput.ID, "sibling awaiting_input answer should report the answered request")
		require.Equal(t, models.HumanInputRequestStatusAnswered, result.AnsweredHumanInput.Status, "sibling awaiting_input answer should mark the request answered")
		require.NoError(t, mock.ExpectationsWereMet(), "all transaction expectations should be met")
	})

	t.Run("rolls back when a comment ID is not in the session", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		commentID := uuid.New()

		// BEGIN → INSERT message → SELECT (returns 0 rows → validation fails)
		// → ROLLBACK. The insert MUST be rolled back even though it succeeded
		// at the SQL level — that's the whole point of the atomic guarantee.
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(7), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(commentRowColumns)) // no rows match
		mock.ExpectRollback()

		svc, deps := newTestService(t)
		primeClaim(deps)
		svc.SetReviewCommentResolver(mock, db.NewSessionReviewCommentStore(mock))

		result, err := svc.SendMessage(context.Background(), SendMessageInput{
			SessionID:               sessionID,
			OrgID:                   orgID,
			ThreadID:                threadID,
			Message:                 "address review",
			ResolveReviewCommentIDs: []uuid.UUID{commentID},
		})
		require.Nil(t, result)
		require.Error(t, err)
		var notInSession *db.ErrReviewCommentsNotInSession
		require.True(t, errors.As(err, &notInSession), "validation error should surface unwrapped so the handler can render the missing IDs")
		require.Equal(t, []uuid.UUID{commentID}, notInSession.Missing)
		require.NoError(t, mock.ExpectationsWereMet(), "the message insert MUST be rolled back when validation fails")
	})
}

func TestService_EndThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name      string
		setupDeps func(deps *testDeps)
		expectErr error
	}{
		{
			name: "success from idle",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusIdle}, nil
				}
			},
		},
		{
			name: "success from pending",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusPending}, nil
				}
			},
		},
		{
			name: "success from running",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
		},
		{
			name: "thread not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "session mismatch",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID, Status: models.ThreadStatusIdle}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "already completed",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted}, nil
				}
			},
			expectErr: ErrThreadCannotBeEnded,
		},
		{
			name: "already failed",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusFailed}, nil
				}
			},
			expectErr: ErrThreadCannotBeEnded,
		},
		{
			name: "archived thread is treated as not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					now := time.Now()
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusIdle, ArchivedAt: &now}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			thread, err := svc.EndThread(context.Background(), orgID, sessionID, threadID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.Equal(t, models.ThreadStatusCompleted, thread.Status, "should set status to completed")
		})
	}
}

func TestService_GetMessages(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name      string
		setupDeps func(deps *testDeps)
		expectErr error
		expectLen int
	}{
		{
			name: "success",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.messageStore.listByThreadFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionMessage, error) {
					return []models.SessionMessage{{ID: 1}, {ID: 2}}, nil
				}
			},
			expectLen: 2,
		},
		{
			name: "thread not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "session mismatch",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "archived thread is treated as not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					now := time.Now()
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, ArchivedAt: &now}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			messages, err := svc.GetMessages(context.Background(), orgID, sessionID, threadID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.Len(t, messages, tt.expectLen, "should return expected number of messages")
		})
	}
}

func TestService_GetMessageWindow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name      string
		setupDeps func(deps *testDeps)
		expectErr error
		expected  MessageWindowResult
	}{
		{
			name: "success",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
					require.Equal(t, orgID, gotOrgID, "thread lookup should be scoped by org")
					require.Equal(t, threadID, gotThreadID, "thread lookup should use requested thread")
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted}, nil
				}
				deps.messageStore.listWindowByThreadFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error) {
					require.Equal(t, orgID, gotOrgID, "message window should be scoped by org")
					require.Equal(t, threadID, gotThreadID, "message window should use requested thread")
					require.Equal(t, int64(44), opts.BeforeID, "message window should pass cursor options")
					require.Equal(t, db.SessionMessageWindowPositionOlder, opts.Position, "message window should pass requested position")
					return db.SessionMessageWindow{
						Messages:                 []models.SessionMessage{{ID: 43}},
						NextOlderCursor:          "43",
						HasOlder:                 true,
						LatestAssistantMessageID: 43,
						LiveEdgeMessageID:        43,
					}, nil
				}
			},
			expected: MessageWindowResult{
				Window: db.SessionMessageWindow{
					Messages:                 []models.SessionMessage{{ID: 43}},
					NextOlderCursor:          "43",
					HasOlder:                 true,
					LatestAssistantMessageID: 43,
					LiveEdgeMessageID:        43,
				},
				ThreadStatus: models.ThreadStatusCompleted,
			},
		},
		{
			name: "thread not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "session mismatch",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			result, err := svc.GetMessageWindow(context.Background(), orgID, sessionID, threadID, db.SessionMessageWindowOptions{Position: db.SessionMessageWindowPositionOlder, BeforeID: 44, Limit: 10})
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				return
			}
			require.NoError(t, err, "message window should not return an error")
			require.Equal(t, tt.expected, result, "message window should return expected data and thread status")
		})
	}
}

func TestService_GetLogs(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name      string
		setupDeps func(deps *testDeps)
		expectErr error
		expectLen int
	}{
		{
			name: "success",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionLog, error) {
					return []models.SessionLog{{ID: 1}, {ID: 2}}, nil
				}
			},
			expectLen: 2,
		},
		{
			name: "success with turn filter",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadTurnsFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, turns []int) ([]models.SessionLog, error) {
					require.Equal(t, orgID, gotOrgID, "filtered log lookup should preserve org scope")
					require.Equal(t, threadID, gotThreadID, "filtered log lookup should preserve thread scope")
					require.Equal(t, []int{5, 6}, turns, "filtered log lookup should pass requested turns")
					return []models.SessionLog{{ID: 6}}, nil
				}
			},
			expectLen: 1,
		},
		{
			name: "thread not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "session mismatch",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: uuid.New(), OrgID: orgID}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "archived thread is treated as not found",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					now := time.Now()
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, ArchivedAt: &now}, nil
				}
			},
			expectErr: ErrThreadNotFound,
		},
		{
			name: "log store error",
			setupDeps: func(deps *testDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionLog, error) {
					return nil, fmt.Errorf("db error")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			opts := db.SessionLogFilterOptions{}
			if tt.name == "success with turn filter" {
				opts.TurnNumbers = []int{5, 6}
			}
			logs, err := svc.GetLogs(context.Background(), orgID, sessionID, threadID, opts)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				return
			}
			if tt.name == "log store error" {
				require.Error(t, err, "should return error on db failure")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.Len(t, logs, tt.expectLen, "should return expected number of logs")
		})
	}
}
