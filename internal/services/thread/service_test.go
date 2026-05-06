package thread

import (
	"context"
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
	createFn        func(ctx context.Context, t *models.SessionThread, max int) error
	getByIDFn       func(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error)
	listBySessionFn func(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	claimIdleFn     func(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	updateFn        func(ctx context.Context, t *models.SessionThread) error
	updateStatusFn  func(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error
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

func (m *mockThreadStore) ClaimIdleForSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, _ int) (models.SessionThread, error) {
	if m.claimIdleFn != nil {
		return m.claimIdleFn(ctx, orgID, sessionID, threadID)
	}
	return models.SessionThread{}, fmt.Errorf("not idle")
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
	return nil
}

func (m *mockThreadStore) MarkCancelRequested(ctx context.Context, orgID, threadID uuid.UUID) error {
	return nil
}

type mockSessionStore struct {
	getByIDFn      func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	claimIdleFn    func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	updateStatusFn func(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
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
	return models.Session{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning)}, nil
}

func (m *mockSessionStore) UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error {
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, orgID, sessionID, status)
	}
	return nil
}

type mockMessageStore struct {
	createFn       func(ctx context.Context, msg *models.SessionMessage) error
	listByThreadFn func(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error)
}

func (m *mockMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	if m.createFn != nil {
		return m.createFn(ctx, msg)
	}
	return nil
}

func (m *mockMessageStore) ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error) {
	if m.listByThreadFn != nil {
		return m.listByThreadFn(ctx, orgID, threadID)
	}
	return nil, nil
}

type mockLogStore struct {
	listByThreadFn func(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error)
}

func (m *mockLogStore) ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error) {
	if m.listByThreadFn != nil {
		return m.listByThreadFn(ctx, orgID, threadID)
	}
	return nil, nil
}

type mockJobStore struct {
	enqueueFn func(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

func (m *mockJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	if m.enqueueFn != nil {
		return m.enqueueFn(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
	}
	return uuid.New(), nil
}

type testDeps struct {
	threadStore  *mockThreadStore
	sessionStore *mockSessionStore
	messageStore *mockMessageStore
	logStore     *mockLogStore
	jobStore     *mockJobStore
}

func newTestService(t *testing.T) (*Service, *testDeps) {
	t.Helper()
	deps := &testDeps{
		threadStore:  &mockThreadStore{},
		sessionStore: &mockSessionStore{},
		messageStore: &mockMessageStore{},
		logStore:     &mockLogStore{},
		jobStore:     &mockJobStore{},
	}
	svc := NewService(deps.threadStore, deps.sessionStore, deps.messageStore, deps.logStore, deps.jobStore, zerolog.Nop())
	return svc, deps
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
			name: "session in terminal state",
			input: CreateThreadInput{
				SessionID: sessionID,
				OrgID:     orgID,
				Label:     "Backend",
			},
			setupDeps: func(deps *testDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "completed", AgentType: models.AgentTypeClaudeCode}, nil
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
				Model:     models.CodexModelGPT54Mini,
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
					return models.Session{ID: sessionID, OrgID: orgID, Status: "completed", AgentType: models.AgentTypeClaudeCode}, nil
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
				Model:     models.ClaudeCodeModelSonnet46,
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
				Model:     models.CodexModelGPT54Mini,
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

func stringPtr(value string) *string {
	return &value
}

func TestService_ListThreads(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

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
					return []models.SessionThread{{ID: uuid.New()}, {ID: uuid.New()}}, nil
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			thread, err := svc.GetThread(context.Background(), orgID, sessionID, threadID)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.Equal(t, threadID, thread.ID, "should return the correct thread")
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
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, queue, jobType string, payload any, _ int, _ *string) (uuid.UUID, error) {
					require.Equal(t, "agent", queue, "thread messages should use the agent queue")
					require.Equal(t, "continue_session", jobType, "thread messages should reuse the continue-session worker")
					require.IsType(t, map[string]string{}, payload, "thread message payload should be string keyed")
					require.Equal(t, threadID.String(), payload.(map[string]string)["thread_id"], "thread id should be included for worker attribution")
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
					return models.Session{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning)}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, _ *models.SessionMessage) error {
					require.True(t, claimedSession, "SendMessage should claim the parent session before creating the message")
					return nil
				}
			},
		},
		{
			name: "proceeds when parent session is already running due to sibling",
			input: SendMessageInput{
				SessionID: sessionID,
				OrgID:     orgID,
				ThreadID:  threadID,
				Message:   "hi",
			},
			setupDeps: func(deps *testDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, CurrentTurn: 1, Status: models.ThreadStatusRunning}, nil
				}
				// Phase 2: parent session ClaimIdle fails because a sibling
				// tab already moved the session into running state. The
				// service should treat this as a no-op and proceed instead
				// of failing the user's send.
				deps.sessionStore.claimIdleFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("session already running")
				}
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning)}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 99
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
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
			name: "thread not idle",
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
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning}, nil
				}
			},
			expectErr: ErrThreadNotIdle,
		},
		{
			name: "running limit reached",
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
			},
			expectErr: ErrRunningLimitReached,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, deps := newTestService(t)
			tt.setupDeps(deps)

			result, err := svc.SendMessage(context.Background(), tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "should return expected error")
				require.Nil(t, result, "should not return a result on error")
				return
			}
			if tt.name == "message creation failure reverts to idle" {
				require.Error(t, err, "should return error on message creation failure")
				return
			}
			require.NoError(t, err, "should not return an error")
			require.NotNil(t, result, "should return a result")
			require.NotNil(t, result.Message, "should return a message")
			require.Equal(t, models.MessageRoleUser, result.Message.Role, "should set role to user")
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
			return models.Session{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning), CurrentTurn: 2}, nil
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

			logs, err := svc.GetLogs(context.Background(), orgID, sessionID, threadID)
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
