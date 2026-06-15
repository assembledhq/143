package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// --- Mock stores implementing the thread service interfaces ---

type mockThreadStore struct {
	createFn         func(ctx context.Context, t *models.SessionThread, max int) error
	getByIDFn        func(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error)
	listBySessionFn  func(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	archiveFn        func(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	claimIdleFn      func(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	claimForResumeFn func(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	updateFn         func(ctx context.Context, t *models.SessionThread) error
	updateStatusFn   func(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error
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
	return nil
}

func (m *mockThreadStore) MarkCancelRequested(ctx context.Context, orgID, threadID uuid.UUID) error {
	return nil
}

type mockSessionStoreForThread struct {
	getByIDFn        func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	claimIdleFn      func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	claimForResumeFn func(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	updateStatusFn   func(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error
}

func (m *mockSessionStoreForThread) GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, sessionID)
	}
	return models.Session{}, fmt.Errorf("not found")
}

func (m *mockSessionStoreForThread) ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if m.claimIdleFn != nil {
		return m.claimIdleFn(ctx, orgID, sessionID)
	}
	return models.Session{ID: sessionID, OrgID: orgID, Status: models.SessionStatusRunning}, nil
}

func (m *mockSessionStoreForThread) ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if m.claimForResumeFn != nil {
		return m.claimForResumeFn(ctx, orgID, sessionID)
	}
	return models.Session{}, fmt.Errorf("no rows")
}

func (m *mockSessionStoreForThread) UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error {
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
	listByThreadFn            func(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error)
	listByThreadTurnsFn       func(ctx context.Context, orgID, threadID uuid.UUID, turnNumbers []int) ([]models.SessionLog, error)
	listByThreadLatestTurnsFn func(ctx context.Context, orgID, threadID uuid.UUID, latestTurns int) ([]models.SessionLog, error)
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

func (m *mockLogStore) ListByThreadLatestTurns(ctx context.Context, orgID, threadID uuid.UUID, latestTurns int) ([]models.SessionLog, error) {
	if m.listByThreadLatestTurnsFn != nil {
		return m.listByThreadLatestTurnsFn(ctx, orgID, threadID, latestTurns)
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

func (m *mockJobStore) EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	return m.Enqueue(ctx, orgID, opts.Queue, opts.JobType, opts.Payload, opts.Priority, opts.DedupeKey)
}

type mockThreadInboxStoreForHandler struct {
	listRecoverableFn  func(ctx context.Context, orgID, threadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error)
	retryRecoverableFn func(ctx context.Context, orgID, threadID, entryID uuid.UUID, allowUnknown bool) (models.ThreadInboxEntry, error)
}

func (m *mockThreadInboxStoreForHandler) AppendForMessage(context.Context, uuid.UUID, db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	return models.ThreadInboxEntry{}, nil
}

func (m *mockThreadInboxStoreForHandler) GetByClientMessageID(context.Context, uuid.UUID, uuid.UUID, string) (models.ThreadInboxEntry, error) {
	return models.ThreadInboxEntry{}, pgx.ErrNoRows
}

func (m *mockThreadInboxStoreForHandler) ListDeliverySummariesBySession(context.Context, uuid.UUID, uuid.UUID) (map[uuid.UUID]models.ThreadInboxDeliverySummary, error) {
	return map[uuid.UUID]models.ThreadInboxDeliverySummary{}, nil
}

func (m *mockThreadInboxStoreForHandler) GetDeliverySummaryByThread(context.Context, uuid.UUID, uuid.UUID) (models.ThreadInboxDeliverySummary, error) {
	return models.ThreadInboxDeliverySummary{}, nil
}

func (m *mockThreadInboxStoreForHandler) ListRecoverableByThread(ctx context.Context, orgID, threadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error) {
	if m.listRecoverableFn != nil {
		return m.listRecoverableFn(ctx, orgID, threadID, limit)
	}
	return []models.ThreadInboxEntry{}, nil
}

func (m *mockThreadInboxStoreForHandler) RetryRecoverable(ctx context.Context, orgID, threadID, entryID uuid.UUID, allowUnknown bool) (models.ThreadInboxEntry, error) {
	if m.retryRecoverableFn != nil {
		return m.retryRecoverableFn(ctx, orgID, threadID, entryID, allowUnknown)
	}
	return models.ThreadInboxEntry{ID: entryID, OrgID: orgID, ThreadID: threadID, DeliveryState: models.ThreadInboxDeliveryStatePending}, nil
}

func (m *mockThreadInboxStoreForHandler) CountPendingByThread(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}

func (m *mockThreadInboxStoreForHandler) CountPendingBySession(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}

// --- Helper to build the handler ---

type threadTestDeps struct {
	threadStore  *mockThreadStore
	sessionStore *mockSessionStoreForThread
	messageStore *mockMessageStore
	logStore     *mockLogStore
	jobStore     *mockJobStore
}

func newThreadHandler(t *testing.T) (*SessionThreadHandler, *threadTestDeps) {
	t.Helper()
	deps := &threadTestDeps{
		threadStore:  &mockThreadStore{},
		sessionStore: &mockSessionStoreForThread{},
		messageStore: &mockMessageStore{},
		logStore:     &mockLogStore{},
		jobStore:     &mockJobStore{},
	}
	svc := thread.NewService(
		deps.threadStore,
		deps.sessionStore,
		deps.messageStore,
		deps.logStore,
		deps.jobStore,
		zerolog.Nop(),
	)
	return NewSessionThreadHandler(svc), deps
}

func newThreadHandlerWithInbox(t *testing.T, inbox thread.ThreadInboxStore) (*SessionThreadHandler, *threadTestDeps) {
	t.Helper()
	deps := &threadTestDeps{
		threadStore:  &mockThreadStore{},
		sessionStore: &mockSessionStoreForThread{},
		messageStore: &mockMessageStore{},
		logStore:     &mockLogStore{},
		jobStore:     &mockJobStore{},
	}
	svc := thread.NewService(
		deps.threadStore,
		deps.sessionStore,
		deps.messageStore,
		deps.logStore,
		deps.jobStore,
		zerolog.Nop(),
	)
	svc.SetThreadInboxStore(inbox)
	return NewSessionThreadHandler(svc), deps
}

// threadRequest builds a request with orgID in context and chi URL params set.
func threadRequest(method, url string, body string, orgID uuid.UUID, params map[string]string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, url, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, url, nil)
	}
	ctx := middleware.WithOrgID(r.Context(), orgID)

	// Set chi URL params.
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return r.WithContext(ctx)
}

// --- Tests ---

func TestSessionThreadHandler_CreateThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	tests := []struct {
		name           string
		sessionIDParam string
		body           string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "success",
			sessionIDParam: sessionID.String(),
			body:           `{"label":"Backend API","agent_type":"claude_code"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, t *models.SessionThread, _ int) error {
					t.ID = threadID
					t.CreatedAt = now
					return nil
				}
			},
			expectedCode: http.StatusCreated,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "not-a-uuid",
			body:           `{"label":"Backend"}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "missing label",
			sessionIDParam: sessionID.String(),
			body:           `{"agent_type":"claude_code"}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "MISSING_LABEL",
		},
		{
			name:           "whitespace-only label",
			sessionIDParam: sessionID.String(),
			body:           `{"label":"   ","agent_type":"claude_code"}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "MISSING_LABEL",
		},
		{
			name:           "session not found",
			sessionIDParam: sessionID.String(),
			body:           `{"label":"Backend"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
		{
			name:           "thread limit reached",
			sessionIDParam: sessionID.String(),
			body:           `{"label":"Backend"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, _ *models.SessionThread, _ int) error {
					return db.ErrThreadLimitReached
				}
			},
			expectedCode:  http.StatusConflict,
			expectedError: "THREAD_LIMIT",
		},
		{
			name:           "success for completed session",
			sessionIDParam: sessionID.String(),
			body:           `{"label":"Backend API"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "completed", AgentType: models.AgentTypeClaudeCode}, nil
				}
				deps.threadStore.createFn = func(_ context.Context, t *models.SessionThread, _ int) error {
					t.ID = threadID
					t.CreatedAt = now
					return nil
				}
			},
			expectedCode: http.StatusCreated,
		},
		{
			name:           "session in non-resumable terminal state",
			sessionIDParam: sessionID.String(),
			body:           `{"label":"Backend"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "skipped", AgentType: models.AgentTypeClaudeCode}, nil
				}
			},
			expectedCode:  http.StatusConflict,
			expectedError: "SESSION_TERMINAL",
		},
		{
			name:           "invalid request body",
			sessionIDParam: sessionID.String(),
			body:           `{invalid-json`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_BODY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodPost, "/api/v1/sessions/"+tt.sessionIDParam+"/threads", tt.body, orgID, map[string]string{"id": tt.sessionIDParam})
			w := httptest.NewRecorder()

			handler.CreateThread(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.SingleResponse[models.SessionThread]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, threadID, resp.Data.ID, "should return the created thread ID")
				require.Equal(t, "Backend API", resp.Data.Label, "should return the correct label")
			}
		})
	}
}

func TestSessionThreadHandler_UpdateThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		body           string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "success",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"agent_type":"codex","label":"Codex 2"}`,
			setupDeps: func(deps *threadTestDeps) {
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
					require.Equal(t, models.AgentTypeCodex, updated.AgentType, "UpdateThread should persist the requested agent type")
					require.Equal(t, "Codex 2", updated.Label, "UpdateThread should persist the requested label")
					return nil
				}
			},
			expectedCode: http.StatusOK,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "not-a-uuid",
			threadIDParam:  threadID.String(),
			body:           `{"agent_type":"codex","label":"Codex 2"}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid thread ID",
			sessionIDParam: sessionID.String(),
			threadIDParam:  "not-a-uuid",
			body:           `{"agent_type":"codex","label":"Codex 2"}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid body",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{invalid-json`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_BODY",
		},
		{
			name:           "empty label rejected",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"agent_type":"codex","label":"   "}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "MISSING_LABEL",
		},
		{
			name:           "thread not editable",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"agent_type":"codex","label":"Codex 2"}`,
			setupDeps: func(deps *threadTestDeps) {
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
			expectedCode:  http.StatusConflict,
			expectedError: "THREAD_NOT_EDITABLE",
		},
		{
			name:           "invalid agent type",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"agent_type":"pm_agent","label":"PM Agent 2"}`,
			setupDeps: func(deps *threadTestDeps) {
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
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_AGENT_TYPE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodPatch, "/api/v1/sessions/"+tt.sessionIDParam+"/threads/"+tt.threadIDParam, tt.body, orgID, map[string]string{
				"id":  tt.sessionIDParam,
				"tid": tt.threadIDParam,
			})
			w := httptest.NewRecorder()

			handler.UpdateThread(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "UpdateThread should return the expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "UpdateThread should return the expected error code")
				return
			}

			var resp models.SingleResponse[models.SessionThread]
			err := json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, models.AgentTypeCodex, resp.Data.AgentType, "UpdateThread should return the updated agent type")
			require.Equal(t, "Codex 2", resp.Data.Label, "UpdateThread should return the updated label")
		})
	}
}

func TestSessionThreadHandler_ArchiveThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "success",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "completed"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return []models.SessionThread{
						{ID: uuid.New(), SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusRunning, Label: "Main tab"},
						{ID: threadID, SessionID: sessionID, OrgID: orgID, Status: models.ThreadStatusCompleted, Label: "Review"},
					}, nil
				}
				deps.threadStore.archiveFn = func(_ context.Context, _, _, gotThreadID uuid.UUID) (models.SessionThread, error) {
					require.Equal(t, threadID, gotThreadID, "ArchiveThread should archive the requested thread")
					archivedAt := time.Now()
					return models.SessionThread{
						ID:         threadID,
						SessionID:  sessionID,
						OrgID:      orgID,
						Label:      "Review",
						Status:     models.ThreadStatusCompleted,
						ArchivedAt: &archivedAt,
					}, nil
				}
			},
			expectedCode: http.StatusOK,
		},
		{
			name:           "invalid session id",
			sessionIDParam: "not-a-uuid",
			threadIDParam:  threadID.String(),
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "cannot archive last thread",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
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
			expectedCode:  http.StatusConflict,
			expectedError: "THREAD_LAST_VISIBLE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodPost, "/api/v1/sessions/"+tt.sessionIDParam+"/threads/"+tt.threadIDParam+"/archive", "", orgID, map[string]string{
				"id":  tt.sessionIDParam,
				"tid": tt.threadIDParam,
			})
			w := httptest.NewRecorder()

			handler.ArchiveThread(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "ArchiveThread should return the expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "ArchiveThread should return the expected error code")
				return
			}

			var resp models.SingleResponse[models.SessionThread]
			err := json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, "Review", resp.Data.Label, "ArchiveThread should return the archived thread")
			require.NotNil(t, resp.Data.ArchivedAt, "ArchiveThread should return the archived timestamp")
		})
	}
}

// TestSessionThreadHandler_UpdateThread_ModelWireEncoding pins the contract
// the handler exposes for the optional `model` field: absence keeps, JSON
// null clears, empty string clears, and a value sets. JSON null and "field
// absent" decode to the same `*string` nil, so the handler distinguishes
// them via json.RawMessage; this test would have caught the bug where
// `{"model": null}` silently kept the existing override.
func TestSessionThreadHandler_UpdateThread_ModelWireEncoding(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name             string
		body             string
		existingOverride *string
		assertModel      func(t *testing.T, model *string)
	}{
		{
			name:             "field absent keeps existing override",
			body:             `{"label":"Codex 2"}`,
			existingOverride: stringPtrTest(models.CodexModelGPT54Mini),
			assertModel: func(t *testing.T, model *string) {
				require.NotNil(t, model, "absent model field should keep the existing override")
				require.Equal(t, models.CodexModelGPT54Mini, *model)
			},
		},
		{
			name:             "json null clears the override",
			body:             `{"label":"Codex 2","model":null}`,
			existingOverride: stringPtrTest(models.CodexModelGPT54Mini),
			assertModel: func(t *testing.T, model *string) {
				require.Nil(t, model, "explicit JSON null should clear the override")
			},
		},
		{
			name:             "empty string clears the override",
			body:             `{"label":"Codex 2","model":""}`,
			existingOverride: stringPtrTest(models.CodexModelGPT54Mini),
			assertModel: func(t *testing.T, model *string) {
				require.Nil(t, model, "explicit empty string should clear the override")
			},
		},
		{
			name:             "value sets the override",
			body:             `{"label":"Codex 2","model":"` + models.CodexModelGPT54 + `"}`,
			existingOverride: nil,
			assertModel: func(t *testing.T, model *string) {
				require.NotNil(t, model, "value should set the override")
				require.Equal(t, models.CodexModelGPT54, *model)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
				return models.Session{ID: sessionID, OrgID: orgID, Status: "running", AgentType: models.AgentTypeCodex}, nil
			}
			deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{
					ID:            threadID,
					SessionID:     sessionID,
					OrgID:         orgID,
					AgentType:     models.AgentTypeCodex,
					ModelOverride: tt.existingOverride,
					Label:         "Codex 2",
					Status:        models.ThreadStatusIdle,
					CurrentTurn:   0,
				}, nil
			}
			var persistedModel *string
			deps.threadStore.updateFn = func(_ context.Context, updated *models.SessionThread) error {
				persistedModel = updated.ModelOverride
				return nil
			}

			req := threadRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String(), tt.body, orgID, map[string]string{
				"id":  sessionID.String(),
				"tid": threadID.String(),
			})
			w := httptest.NewRecorder()
			handler.UpdateThread(w, req)
			require.Equal(t, http.StatusOK, w.Code, "UpdateThread should return 200; body: %s", w.Body.String())
			tt.assertModel(t, persistedModel)
		})
	}
}

func stringPtrTest(value string) *string {
	return &value
}

func TestSessionThreadHandler_ListThreads(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID1 := uuid.New()
	threadID2 := uuid.New()
	now := time.Now()

	tests := []struct {
		name           string
		sessionIDParam string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedLen    int
		expectedError  string
	}{
		{
			name:           "success with threads",
			sessionIDParam: sessionID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return []models.SessionThread{
						{ID: threadID1, SessionID: sessionID, OrgID: orgID, Label: "Backend", Status: models.ThreadStatusRunning, CreatedAt: now},
						{ID: threadID2, SessionID: sessionID, OrgID: orgID, Label: "Frontend", Status: models.ThreadStatusIdle, CreatedAt: now},
					}, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name:           "success with empty list",
			sessionIDParam: sessionID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{ID: sessionID, OrgID: orgID, Status: "running"}, nil
				}
				deps.threadStore.listBySessionFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
					return nil, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "bad-id",
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "session not found",
			sessionIDParam: sessionID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.sessionStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
					return models.Session{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodGet, "/api/v1/sessions/"+tt.sessionIDParam+"/threads", "", orgID, map[string]string{"id": tt.sessionIDParam})
			w := httptest.NewRecorder()

			handler.ListThreads(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.ListResponse[models.SessionThread]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of threads")
			}
		})
	}
}

func TestSessionThreadHandler_GetThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "success",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:        threadID,
						SessionID: sessionID,
						OrgID:     orgID,
						Label:     "Backend",
						Status:    models.ThreadStatusRunning,
						CreatedAt: now,
					}, nil
				}
			},
			expectedCode: http.StatusOK,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "bad-id",
			threadIDParam:  threadID.String(),
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid thread ID",
			sessionIDParam: sessionID.String(),
			threadIDParam:  "bad-id",
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "thread not found",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
		{
			name:           "thread belongs to different session",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				otherSessionID := uuid.New()
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:        threadID,
						SessionID: otherSessionID,
						OrgID:     orgID,
						Label:     "Backend",
						Status:    models.ThreadStatusRunning,
					}, nil
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodGet, "/api/v1/sessions/"+tt.sessionIDParam+"/threads/"+tt.threadIDParam, "", orgID, map[string]string{"id": tt.sessionIDParam, "tid": tt.threadIDParam})
			w := httptest.NewRecorder()

			handler.GetThread(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.SingleResponse[models.SessionThread]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, threadID, resp.Data.ID, "should return the correct thread")
				require.Equal(t, "Backend", resp.Data.Label, "should return the correct label")
			}
		})
	}
}

func TestSessionThreadHandler_ListRecoverableInboxEntries(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	reason := "runtime lease expired after live delivery before ack"
	inbox := &mockThreadInboxStoreForHandler{
		listRecoverableFn: func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error) {
			require.Equal(t, orgID, gotOrgID, "handler should pass the request org")
			require.Equal(t, threadID, gotThreadID, "handler should list entries for the requested thread")
			require.Greater(t, limit, 0, "handler should use a bounded recoverable-entry limit")
			return []models.ThreadInboxEntry{{
				ID:            entryID,
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				DeliveryState: models.ThreadInboxDeliveryStateUnknownDelivery,
				LastError:     &reason,
			}}, nil
		},
	}
	handler, deps := newThreadHandlerWithInbox(t, inbox)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, OrgID: orgID, SessionID: sessionID}, nil
	}

	req := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/inbox/recoverable", "", orgID, map[string]string{
		"id":  sessionID.String(),
		"tid": threadID.String(),
	})
	rr := httptest.NewRecorder()

	handler.ListRecoverableInboxEntries(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ListRecoverableInboxEntries should return HTTP 200")
	var resp models.ListResponse[models.ThreadInboxEntry]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response body should be valid JSON")
	require.Equal(t, entryID, resp.Data[0].ID, "handler should return recoverable inbox entries")
	require.Equal(t, models.ThreadInboxDeliveryStateUnknownDelivery, resp.Data[0].DeliveryState, "handler should preserve recovery state")
}

func TestSessionThreadHandler_RetryInboxEntry(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	inbox := &mockThreadInboxStoreForHandler{
		retryRecoverableFn: func(_ context.Context, gotOrgID, gotThreadID, gotEntryID uuid.UUID, allowUnknown bool) (models.ThreadInboxEntry, error) {
			require.Equal(t, orgID, gotOrgID, "handler should pass the request org")
			require.Equal(t, threadID, gotThreadID, "handler should retry entries for the requested thread")
			require.Equal(t, entryID, gotEntryID, "handler should retry the requested entry")
			require.True(t, allowUnknown, "handler should pass explicit unknown-delivery replay consent from the request body")
			return models.ThreadInboxEntry{
				ID:            entryID,
				OrgID:         orgID,
				SessionID:     sessionID,
				ThreadID:      threadID,
				DeliveryState: models.ThreadInboxDeliveryStatePending,
			}, nil
		},
	}
	handler, deps := newThreadHandlerWithInbox(t, inbox)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: threadID, OrgID: orgID, SessionID: sessionID}, nil
	}

	req := threadRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/inbox/"+entryID.String()+"/retry", `{"replay_unknown_delivery":true}`, orgID, map[string]string{
		"id":       sessionID.String(),
		"tid":      threadID.String(),
		"entry_id": entryID.String(),
	})
	rr := httptest.NewRecorder()

	handler.RetryInboxEntry(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "RetryInboxEntry should return HTTP 200")
	var resp models.SingleResponse[models.ThreadInboxEntry]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response body should be valid JSON")
	require.Equal(t, entryID, resp.Data.ID, "handler should return the retried inbox entry")
	require.Equal(t, models.ThreadInboxDeliveryStatePending, resp.Data.DeliveryState, "handler should return the entry to pending")
}

func TestSessionThreadHandler_SendThreadMessage(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		body           string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "success",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":"please continue"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						Status:      models.ThreadStatusRunning,
						CurrentTurn: 1,
					}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 42
					msg.CreatedAt = time.Now()
					return nil
				}
			},
			expectedCode: http.StatusCreated,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "bad-id",
			threadIDParam:  threadID.String(),
			body:           `{"message":"hi"}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid thread ID",
			sessionIDParam: sessionID.String(),
			threadIDParam:  "bad-id",
			body:           `{"message":"hi"}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "missing message",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":""}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "MISSING_MESSAGE",
		},
		{
			name:           "whitespace-only message",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":"   "}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "MISSING_MESSAGE",
		},
		{
			name:           "thread not found",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":"continue"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
		{
			// Sending to a thread that is mid-turn now succeeds: the message
			// is queued (created + pending_message_count incremented) and
			// will be picked up when the in-flight turn completes. The
			// composer never sees a NOT_IDLE bounce.
			name:           "thread busy queues without enqueue",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":"please continue"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						Status:      models.ThreadStatusRunning,
						CurrentTurn: 1,
					}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 42
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					t.Fatalf("queue-only path must not enqueue")
					return uuid.UUID{}, nil
				}
			},
			expectedCode: http.StatusCreated,
		},
		{
			name:           "enqueue failure",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":"continue"}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						Status:      models.ThreadStatusRunning,
						CurrentTurn: 1,
					}, nil
				}
				deps.messageStore.createFn = func(_ context.Context, msg *models.SessionMessage) error {
					msg.ID = 42
					return nil
				}
				deps.jobStore.enqueueFn = func(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
					return uuid.Nil, fmt.Errorf("enqueue failed")
				}
			},
			expectedCode:  http.StatusInternalServerError,
			expectedError: "ENQUEUE_FAILED",
		},
		{
			name:           "invalid request body",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{bad-json`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_BODY",
		},
		{
			// Malformed comment IDs are caught at the wire-level parser
			// before any service work happens — same shape session-level
			// SendMessage uses, so the two surfaces stay consistent.
			name:           "malformed comment ID rejected by parser",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":"hi","resolve_review_comment_ids":["not-a-uuid"]}`,
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_REVIEW_COMMENT_ID",
		},
		{
			// When the service is wired without a resolver but the client
			// sends comment IDs, surface a 400 with REVIEW_COMMENTS_NOT_CONFIGURED
			// so the client can disable the feature flag rather than retrying.
			// The default newThreadHandler does not call SetReviewCommentResolver.
			name:           "resolve IDs rejected when resolver not wired",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			body:           `{"message":"hi","resolve_review_comment_ids":["00000000-0000-4000-8000-000000000000"]}`,
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.claimIdleFn = func(_ context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:          threadID,
						SessionID:   sessionID,
						OrgID:       orgID,
						Status:      models.ThreadStatusRunning,
						CurrentTurn: 1,
					}, nil
				}
			},
			expectedCode:  http.StatusBadRequest,
			expectedError: "REVIEW_COMMENTS_NOT_CONFIGURED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodPost, "/api/v1/sessions/"+tt.sessionIDParam+"/threads/"+tt.threadIDParam+"/messages", tt.body, orgID, map[string]string{"id": tt.sessionIDParam, "tid": tt.threadIDParam})
			w := httptest.NewRecorder()

			handler.SendThreadMessage(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.SingleResponse[models.SendThreadMessageResponse]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, "please continue", resp.Data.Message.Content, "should return the message content")
				require.Equal(t, models.MessageRoleUser, resp.Data.Message.Role, "should set the message role to user")
				require.Equal(t, models.ThreadInboxDeliveryState(""), resp.Data.DeliveryState, "should expose an empty delivery state when the durable inbox is not wired in this test")
			}
		})
	}
}

func TestSessionThreadHandler_EndThread(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "success",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:        threadID,
						SessionID: sessionID,
						OrgID:     orgID,
						Label:     "Backend",
						Status:    models.ThreadStatusIdle,
						CreatedAt: now,
					}, nil
				}
				deps.threadStore.updateStatusFn = func(_ context.Context, _, _ uuid.UUID, _ models.ThreadStatus) error {
					return nil
				}
			},
			expectedCode: http.StatusOK,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "bad-id",
			threadIDParam:  threadID.String(),
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid thread ID",
			sessionIDParam: sessionID.String(),
			threadIDParam:  "bad-id",
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "thread not found",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
		{
			name:           "thread already completed - invalid status",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:        threadID,
						SessionID: sessionID,
						OrgID:     orgID,
						Label:     "Backend",
						Status:    models.ThreadStatusCompleted,
					}, nil
				}
			},
			expectedCode:  http.StatusConflict,
			expectedError: "INVALID_STATUS",
		},
		{
			name:           "thread belongs to different session",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				otherSessionID := uuid.New()
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:        threadID,
						SessionID: otherSessionID,
						OrgID:     orgID,
						Label:     "Backend",
						Status:    models.ThreadStatusIdle,
					}, nil
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
		{
			name:           "end running thread succeeds",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:        threadID,
						SessionID: sessionID,
						OrgID:     orgID,
						Label:     "Backend",
						Status:    models.ThreadStatusRunning,
						CreatedAt: now,
					}, nil
				}
				deps.threadStore.updateStatusFn = func(_ context.Context, _, _ uuid.UUID, _ models.ThreadStatus) error {
					return nil
				}
			},
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodPost, "/api/v1/sessions/"+tt.sessionIDParam+"/threads/"+tt.threadIDParam+"/end", "", orgID, map[string]string{"id": tt.sessionIDParam, "tid": tt.threadIDParam})
			w := httptest.NewRecorder()

			handler.EndThread(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.SingleResponse[models.SessionThread]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, threadID, resp.Data.ID, "should return the ended thread")
				require.Equal(t, models.ThreadStatusCompleted, resp.Data.Status, "should set status to completed")
			}
		})
	}
}

func TestSessionThreadHandler_GetThreadMessages(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedLen    int
		expectedError  string
	}{
		{
			name:           "success with messages",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.messageStore.listByThreadFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionMessage, error) {
					return []models.SessionMessage{
						{ID: 1, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Role: models.MessageRoleUser, Content: "hello", CreatedAt: now},
						{ID: 2, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Role: models.MessageRoleAssistant, Content: "hi", CreatedAt: now},
					}, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name:           "success with empty messages",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.messageStore.listByThreadFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionMessage, error) {
					return nil, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "bad-id",
			threadIDParam:  threadID.String(),
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid thread ID",
			sessionIDParam: sessionID.String(),
			threadIDParam:  "bad-id",
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "thread not found",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			req := threadRequest(http.MethodGet, "/api/v1/sessions/"+tt.sessionIDParam+"/threads/"+tt.threadIDParam+"/messages", "", orgID, map[string]string{"id": tt.sessionIDParam, "tid": tt.threadIDParam})
			w := httptest.NewRecorder()

			handler.GetThreadMessages(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.ListResponse[models.SessionMessage]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of messages")
			}
		})
	}
}

func TestSessionThreadHandler_GetThreadMessagesWindow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	handler, deps := newThreadHandler(t)
	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "thread lookup should be scoped to the request org")
		require.Equal(t, threadID, gotThreadID, "thread lookup should use the route thread")
		return models.SessionThread{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.ThreadStatusIdle,
		}, nil
	}
	deps.messageStore.listWindowByThreadFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error) {
		require.Equal(t, orgID, gotOrgID, "message window should be scoped to the request org")
		require.Equal(t, threadID, gotThreadID, "message window should use the route thread")
		require.Equal(t, int64(30), opts.BeforeID, "message window should pass the before cursor")
		require.Equal(t, 25, opts.Limit, "message window should pass the requested limit")
		return db.SessionMessageWindow{
			Messages: []models.SessionMessage{
				{ID: 21, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Role: models.MessageRoleAssistant, Content: "latest", CreatedAt: now},
			},
			NextOlderCursor:          "21",
			HasOlder:                 true,
			LatestAssistantMessageID: 21,
			LiveEdgeMessageID:        21,
			Position:                 db.SessionMessageWindowPositionLatest,
		}, nil
	}

	req := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/messages?before=30&limit=25", "", orgID, map[string]string{"id": sessionID.String(), "tid": threadID.String()})
	w := httptest.NewRecorder()

	handler.GetThreadMessages(w, req)

	require.Equal(t, http.StatusOK, w.Code, "window request should return success")
	var resp models.ThreadMessageWindowResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "response should include one message")
	require.Equal(t, int64(21), resp.Data[0].ID, "response should include the requested message id")
	require.Equal(t, models.MessageRoleAssistant, resp.Data[0].Role, "response should preserve message role")
	require.Equal(t, "latest", resp.Data[0].Content, "response should preserve message content")
	require.Equal(t, models.ThreadMessageWindowMeta{
		NextOlderCursor:          "21",
		HasOlder:                 true,
		HasNewer:                 false,
		LatestAssistantMessageID: 21,
		LiveEdgeMessageID:        21,
		WindowPosition:           "latest",
		ThreadStatus:             string(models.ThreadStatusIdle),
	}, resp.Meta, "response should include cursor and anchor metadata")
}

func TestSessionThreadHandler_GetThreadMessagesWindow_After(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	handler, deps := newThreadHandler(t)
	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "thread lookup should be scoped to the request org")
		require.Equal(t, threadID, gotThreadID, "thread lookup should use the route thread")
		return models.SessionThread{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.ThreadStatusIdle,
		}, nil
	}
	deps.messageStore.listWindowByThreadFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error) {
		require.Equal(t, orgID, gotOrgID, "message window should be scoped to the request org")
		require.Equal(t, threadID, gotThreadID, "message window should use the route thread")
		require.Equal(t, int64(30), opts.AfterID, "message window should pass the after cursor")
		require.Equal(t, db.SessionMessageWindowPositionNewer, opts.Position, "message window should request a newer page")
		require.Equal(t, 25, opts.Limit, "message window should pass the requested limit")
		return db.SessionMessageWindow{
			Messages: []models.SessionMessage{
				{ID: 31, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Role: models.MessageRoleAssistant, Content: "newer", CreatedAt: now},
			},
			NextNewerCursor:          "31",
			HasNewer:                 true,
			LatestAssistantMessageID: 40,
			LiveEdgeMessageID:        40,
			Position:                 db.SessionMessageWindowPositionNewer,
		}, nil
	}

	req := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/messages?after=30&limit=25", "", orgID, map[string]string{"id": sessionID.String(), "tid": threadID.String()})
	w := httptest.NewRecorder()

	handler.GetThreadMessages(w, req)

	require.Equal(t, http.StatusOK, w.Code, "newer window request should return success")
	var resp models.ThreadMessageWindowResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, int64(31), resp.Data[0].ID, "response should include the newer message")
	require.Equal(t, models.ThreadMessageWindowMeta{
		NextNewerCursor:          "31",
		HasOlder:                 false,
		HasNewer:                 true,
		LatestAssistantMessageID: 40,
		LiveEdgeMessageID:        40,
		WindowPosition:           "newer",
		ThreadStatus:             string(models.ThreadStatusIdle),
	}, resp.Meta, "response should include newer cursor metadata")
}

func TestSessionThreadHandler_GetThreadMessagesWindow_AroundAnchor(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	handler, deps := newThreadHandler(t)
	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "thread lookup should be scoped to the request org")
		require.Equal(t, threadID, gotThreadID, "thread lookup should use the route thread")
		return models.SessionThread{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.ThreadStatusIdle,
		}, nil
	}
	deps.messageStore.listWindowByThreadFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error) {
		require.Equal(t, orgID, gotOrgID, "message window should be scoped to the request org")
		require.Equal(t, threadID, gotThreadID, "message window should use the route thread")
		require.Equal(t, db.SessionMessageWindowPositionAround, opts.Position, "message window should request an anchor-centered window")
		require.Equal(t, int64(456), opts.AnchorMessageID, "message window should pass the anchor message id")
		require.Equal(t, 25, opts.Limit, "message window should pass the requested limit")
		return db.SessionMessageWindow{
			Messages: []models.SessionMessage{
				{ID: 455, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Role: models.MessageRoleUser, Content: "older", CreatedAt: now.Add(-time.Minute)},
				{ID: 456, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Role: models.MessageRoleAssistant, Content: "anchor", CreatedAt: now},
				{ID: 457, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Role: models.MessageRoleUser, Content: "newer", CreatedAt: now.Add(time.Minute)},
			},
			NextOlderCursor:          "455",
			HasOlder:                 true,
			NextNewerCursor:          "457",
			HasNewer:                 true,
			AnchorMessageID:          456,
			AnchorFound:              true,
			LatestAssistantMessageID: 456,
			LiveEdgeMessageID:        500,
			Position:                 db.SessionMessageWindowPositionAround,
		}, nil
	}

	req := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/messages?position=around&anchor_message_id=456&limit=25", "", orgID, map[string]string{"id": sessionID.String(), "tid": threadID.String()})
	w := httptest.NewRecorder()

	handler.GetThreadMessages(w, req)

	require.Equal(t, http.StatusOK, w.Code, "around window request should return success")
	var resp models.ThreadMessageWindowResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, []int64{455, 456, 457}, []int64{resp.Data[0].ID, resp.Data[1].ID, resp.Data[2].ID}, "response should preserve chronological anchor window")
	require.Equal(t, models.ThreadMessageWindowMeta{
		NextOlderCursor:          "455",
		HasOlder:                 true,
		NextNewerCursor:          "457",
		HasNewer:                 true,
		LatestAssistantMessageID: 456,
		LiveEdgeMessageID:        500,
		AnchorMessageID:          456,
		AnchorFound:              true,
		WindowPosition:           "around",
		ThreadStatus:             string(models.ThreadStatusIdle),
	}, resp.Meta, "response should include anchor and bidirectional cursor metadata")
}

func TestSessionThreadHandler_GetThreadMessagesWindow_InvalidCursorCombinations(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	handler, deps := newThreadHandler(t)
	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.ThreadStatusIdle,
		}, nil
	}

	cases := []struct {
		name          string
		query         string
		expectedCode  int
		expectedError string
	}{
		{
			name:          "position=latest with before cursor",
			query:         "position=latest&before=10",
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_CURSOR",
		},
		{
			name:          "position=latest with after cursor",
			query:         "position=latest&after=10",
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_CURSOR",
		},
		{
			name:          "before and after combined",
			query:         "before=5&after=10",
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_CURSOR",
		},
		{
			name:          "around without anchor",
			query:         "position=around",
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_CURSOR",
		},
		{
			name:          "around with before cursor",
			query:         "position=around&anchor_message_id=5&before=3",
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_CURSOR",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := threadRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/messages?"+tc.query, "", orgID, map[string]string{"id": sessionID.String(), "tid": threadID.String()})
			w := httptest.NewRecorder()
			handler.GetThreadMessages(w, req)
			require.Equal(t, tc.expectedCode, w.Code, "invalid cursor combination should return error")
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "error response should be valid JSON")
			require.Equal(t, tc.expectedError, body.Error.Code, "error code should match")
		})
	}
}

func TestSessionThreadHandler_GetThreadLogs(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		setupDeps      func(deps *threadTestDeps)
		expectedCode   int
		expectedLen    int
		expectedError  string
	}{
		{
			name:           "success with logs",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionLog, error) {
					return []models.SessionLog{
						{ID: 1, SessionID: sessionID, ThreadID: &threadID, Level: "info", Message: "started", Timestamp: now},
						{ID: 2, SessionID: sessionID, ThreadID: &threadID, Level: "info", Message: "completed", Timestamp: now},
					}, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name:           "success with empty logs",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadFn = func(_ context.Context, _, _ uuid.UUID) ([]models.SessionLog, error) {
					return nil, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name:           "success with turn filter",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadTurnsFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, turnNumbers []int) ([]models.SessionLog, error) {
					require.Equal(t, orgID, gotOrgID, "log lookup should be scoped to the request organization")
					require.Equal(t, threadID, gotThreadID, "log lookup should target the requested thread")
					require.Equal(t, []int{5, 6, 7}, turnNumbers, "log lookup should pass normalized loaded turn numbers")
					return []models.SessionLog{
						{ID: 1, SessionID: sessionID, ThreadID: &threadID, Level: "info", Message: "loaded turn", TurnNumber: 7, Timestamp: now},
					}, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name:           "success with latest turns window",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadLatestTurnsFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, latestTurns int) ([]models.SessionLog, error) {
					require.Equal(t, orgID, gotOrgID, "log lookup should be scoped to the request organization")
					require.Equal(t, threadID, gotThreadID, "log lookup should target the requested thread")
					require.Equal(t, 50, latestTurns, "log lookup should pass the requested latest-turns window")
					return []models.SessionLog{
						{ID: 3, SessionID: sessionID, ThreadID: &threadID, Level: "info", Message: "latest turn", TurnNumber: 12, Timestamp: now},
					}, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name:           "latest turns clamped to the server cap",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
				}
				deps.logStore.listByThreadLatestTurnsFn = func(_ context.Context, _, _ uuid.UUID, latestTurns int) ([]models.SessionLog, error) {
					require.Equal(t, maxLatestTurns, latestTurns, "oversized latest_turns should be clamped, not honored")
					return []models.SessionLog{}, nil
				}
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
		{
			name:           "invalid session ID",
			sessionIDParam: "bad-id",
			threadIDParam:  threadID.String(),
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid thread ID",
			sessionIDParam: sessionID.String(),
			threadIDParam:  "bad-id",
			setupDeps:      func(deps *threadTestDeps) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "thread not found",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, deps := newThreadHandler(t)
			tt.setupDeps(deps)

			target := "/api/v1/sessions/" + tt.sessionIDParam + "/threads/" + tt.threadIDParam + "/logs"
			if tt.name == "success with turn filter" {
				target += "?turn_numbers=7,6,7,5,bad"
			}
			if tt.name == "success with latest turns window" {
				target += "?latest_turns=50"
			}
			if tt.name == "latest turns clamped to the server cap" {
				target += "?latest_turns=99999"
			}
			req := threadRequest(http.MethodGet, target, "", orgID, map[string]string{"id": tt.sessionIDParam, "tid": tt.threadIDParam})
			w := httptest.NewRecorder()

			handler.GetThreadLogs(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.ListResponse[models.SessionLogResponse]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of logs")
				if len(resp.Data) > 0 {
					require.Equal(t, len([]byte(resp.Data[0].Message)), resp.Data[0].MessageBytes, "thread log response should include message byte length for short logs")
					require.False(t, resp.Data[0].MessageTruncated, "short thread log should not be truncated")
				}
			}
		})
	}
}

func TestSessionThreadHandler_GetThreadLogs_TruncatesLongLog(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()
	longOutput := strings.Repeat("o", models.SessionLogPreviewBytes+37)

	handler, deps := newThreadHandler(t)
	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "thread lookup should be scoped to the request organization")
		require.Equal(t, threadID, gotThreadID, "thread lookup should target the requested thread")
		return models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID}, nil
	}
	deps.logStore.listByThreadFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) ([]models.SessionLog, error) {
		require.Equal(t, orgID, gotOrgID, "log lookup should be scoped to the request organization")
		require.Equal(t, threadID, gotThreadID, "log lookup should target the requested thread")
		return []models.SessionLog{
			{ID: 77, SessionID: sessionID, ThreadID: &threadID, Level: "output", Message: longOutput, Timestamp: now},
		}, nil
	}

	req := threadRequest(
		http.MethodGet,
		"/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/logs",
		"",
		orgID,
		map[string]string{"id": sessionID.String(), "tid": threadID.String()},
	)
	w := httptest.NewRecorder()

	handler.GetThreadLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return OK for thread logs")

	var resp models.ListResponse[models.SessionLogResponse]
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Len(t, resp.Data, 1, "should return one log response")
	require.Equal(t, models.SessionLogPreviewBytes, len(resp.Data[0].Message), "long log message should be previewed")
	require.Equal(t, len([]byte(longOutput)), resp.Data[0].MessageBytes, "response should report original byte length")
	require.Equal(t, len(longOutput), resp.Data[0].MessageChars, "response should report original character length")
	require.True(t, resp.Data[0].MessageTruncated, "long log should be marked truncated")
}

func TestParseTurnNumbersRejectsOverflow(t *testing.T) {
	t.Parallel()

	turnNumbers := parseTurnNumbers("1,2147483647,2147483648,999999999999999999999999999999")

	require.Equal(t, []int{1, 2147483647}, turnNumbers, "turn parsing should reject values that cannot fit in the database integer column")
}

func TestParseLatestTurns(t *testing.T) {
	t.Parallel()

	require.Equal(t, 0, parseLatestTurns(""), "empty input should disable the latest-turns window")
	require.Equal(t, 0, parseLatestTurns("bad"), "non-numeric input should disable the latest-turns window")
	require.Equal(t, 0, parseLatestTurns("-5"), "negative input should disable the latest-turns window")
	require.Equal(t, 0, parseLatestTurns("0"), "zero should disable the latest-turns window")
	require.Equal(t, 50, parseLatestTurns(" 50 "), "valid input should parse with surrounding whitespace")
	require.Equal(t, maxLatestTurns, parseLatestTurns("99999"), "oversized input should clamp to the server cap")
}

// --- mockTranscriptStore ---

type mockTranscriptStore struct {
	listWindowFn func(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionTranscriptWindowOptions) (db.SessionTranscriptWindow, error)
	searchFn     func(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionTranscriptSearchOptions) ([]db.SessionTranscriptSearchMatch, error)
}

func (m *mockTranscriptStore) ListThreadWindow(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionTranscriptWindowOptions) (db.SessionTranscriptWindow, error) {
	if m.listWindowFn != nil {
		return m.listWindowFn(ctx, orgID, threadID, opts)
	}
	return db.SessionTranscriptWindow{}, nil
}

func (m *mockTranscriptStore) SearchThread(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionTranscriptSearchOptions) ([]db.SessionTranscriptSearchMatch, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, orgID, threadID, opts)
	}
	return []db.SessionTranscriptSearchMatch{}, nil
}

// newThreadHandlerWithTranscript creates a handler with the given transcript store wired in.
func newThreadHandlerWithTranscript(t *testing.T, transcriptStore thread.TranscriptStore) (*SessionThreadHandler, *threadTestDeps) {
	t.Helper()
	deps := &threadTestDeps{
		threadStore:  &mockThreadStore{},
		sessionStore: &mockSessionStoreForThread{},
		messageStore: &mockMessageStore{},
		logStore:     &mockLogStore{},
		jobStore:     &mockJobStore{},
	}
	svc := thread.NewService(
		deps.threadStore,
		deps.sessionStore,
		deps.messageStore,
		deps.logStore,
		deps.jobStore,
		zerolog.Nop(),
	)
	svc.SetTranscriptStore(transcriptStore)
	return NewSessionThreadHandler(svc), deps
}

func TestSessionThreadHandler_GetThreadTranscript(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	tests := []struct {
		name           string
		sessionIDParam string
		threadIDParam  string
		query          string
		setupDeps      func(deps *threadTestDeps, ts *mockTranscriptStore)
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "invalid session uuid",
			sessionIDParam: "not-uuid",
			threadIDParam:  threadID.String(),
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid thread uuid",
			sessionIDParam: sessionID.String(),
			threadIDParam:  "not-uuid",
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ID",
		},
		{
			name:           "invalid position",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			query:          "?position=sideways",
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_POSITION",
		},
		{
			name:           "invalid cursor",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			query:          "?before=!!!notbase64",
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_CURSOR",
		},
		{
			name:           "around without anchor",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			query:          "?position=around",
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ANCHOR",
		},
		{
			name:           "invalid anchor_message_id",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			query:          "?position=around&anchor_message_id=notanumber",
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_ANCHOR",
		},
		{
			name:           "invalid limit_turns",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			query:          "?limit_turns=notanumber",
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_LIMIT",
		},
		{
			name:           "invalid include",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			query:          "?include=messages,unknown",
			setupDeps:      func(deps *threadTestDeps, ts *mockTranscriptStore) {},
			expectedCode:   http.StatusBadRequest,
			expectedError:  "INVALID_INCLUDE",
		},
		{
			name:           "thread not found",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps, ts *mockTranscriptStore) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{}, fmt.Errorf("no rows")
				}
			},
			expectedCode:  http.StatusNotFound,
			expectedError: "NOT_FOUND",
		},
		{
			name:           "success latest",
			sessionIDParam: sessionID.String(),
			threadIDParam:  threadID.String(),
			setupDeps: func(deps *threadTestDeps, ts *mockTranscriptStore) {
				deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
					return models.SessionThread{
						ID:        threadID,
						SessionID: sessionID,
						OrgID:     orgID,
						Status:    models.ThreadStatusIdle,
					}, nil
				}
				ts.listWindowFn = func(_ context.Context, _, _ uuid.UUID, _ db.SessionTranscriptWindowOptions) (db.SessionTranscriptWindow, error) {
					return db.SessionTranscriptWindow{}, nil
				}
			},
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ts := &mockTranscriptStore{}
			handler, deps := newThreadHandlerWithTranscript(t, ts)
			tt.setupDeps(deps, ts)

			url := "/api/v1/sessions/" + tt.sessionIDParam + "/threads/" + tt.threadIDParam + "/transcript" + tt.query
			req := threadRequest(http.MethodGet, url, "", orgID, map[string]string{
				"id":  tt.sessionIDParam,
				"tid": tt.threadIDParam,
			})
			w := httptest.NewRecorder()

			handler.GetThreadTranscript(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "GetThreadTranscript should return the expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "GetThreadTranscript should return the expected error code")
			}
		})
	}
}

func TestSessionThreadHandler_GetThreadTranscript_ParsesInclude(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	ts := &mockTranscriptStore{}
	handler, deps := newThreadHandlerWithTranscript(t, ts)
	deps.threadStore.getByIDFn = func(_ context.Context, _, _ uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.ThreadStatusIdle,
		}, nil
	}
	ts.listWindowFn = func(_ context.Context, _, _ uuid.UUID, opts db.SessionTranscriptWindowOptions) (db.SessionTranscriptWindow, error) {
		require.True(t, opts.Include.Messages, "include=messages should include messages")
		require.True(t, opts.Include.Tools, "include=tools should include tool entries")
		require.False(t, opts.Include.HumanInputs, "omitted human_inputs should be excluded")
		require.False(t, opts.Include.System, "omitted system should be excluded")
		return db.SessionTranscriptWindow{Position: models.TranscriptWindowPositionLatest}, nil
	}

	req := threadRequest(
		http.MethodGet,
		"/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/transcript?include=messages,tools",
		"",
		orgID,
		map[string]string{"id": sessionID.String(), "tid": threadID.String()},
	)
	w := httptest.NewRecorder()

	handler.GetThreadTranscript(w, req)
	require.Equal(t, http.StatusOK, w.Code, "GetThreadTranscript should accept valid include filters")
}

func TestSessionThreadHandler_GetThreadTranscript_ResponseContract(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	msgID := int64(41)
	logID := int64(42)
	olderCursor := "older-cursor"
	newerCursor := "newer-cursor"

	ts := &mockTranscriptStore{}
	handler, deps := newThreadHandlerWithTranscript(t, ts)
	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "GetThreadTranscript should scope the thread lookup by org")
		require.Equal(t, threadID, gotThreadID, "GetThreadTranscript should load the requested thread")
		return models.SessionThread{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.ThreadStatusIdle,
		}, nil
	}
	ts.listWindowFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, opts db.SessionTranscriptWindowOptions) (db.SessionTranscriptWindow, error) {
		require.Equal(t, orgID, gotOrgID, "transcript store should receive the request org")
		require.Equal(t, threadID, gotThreadID, "transcript store should receive the request thread")
		require.Equal(t, models.TranscriptWindowPositionAround, opts.Position, "transcript store should receive around position")
		require.Equal(t, "msg_41", opts.AnchorEntryID, "transcript store should receive the anchor entry id")
		return db.SessionTranscriptWindow{
			Rows: []db.SessionTranscriptRawRow{
				{
					EntryKindHint: models.TranscriptEntryKindMessage,
					TurnNumber:    7,
					EntryTime:     now,
					SourceRank:    1,
					SourceID:      msgID,
					Message: &models.SessionMessage{
						ID:         msgID,
						SessionID:  sessionID,
						OrgID:      orgID,
						ThreadID:   &threadID,
						TurnNumber: 7,
						Role:       models.MessageRoleAssistant,
						Content:    "Finished the work.",
						CreatedAt:  now,
					},
				},
				{
					EntryKindHint: models.TranscriptEntryKindToolResult,
					TurnNumber:    7,
					EntryTime:     now.Add(time.Second),
					SourceRank:    2,
					SourceID:      logID,
					Log: &models.SessionLog{
						ID:         logID,
						SessionID:  sessionID,
						OrgID:      orgID,
						ThreadID:   &threadID,
						TurnNumber: 7,
						Level:      models.SessionLogLevelOutput,
						Message:    "ok",
						Metadata:   json.RawMessage(`{"type":"tool_result","tool_name":"exec_command"}`),
						Timestamp:  now.Add(time.Second),
					},
				},
			},
			Position:                 models.TranscriptWindowPositionAround,
			HasOlder:                 true,
			HasNewer:                 true,
			OlderCursor:              olderCursor,
			NewerCursor:              newerCursor,
			AnchorEntryID:            "msg_41",
			AnchorFound:              true,
			LatestAssistantEntryID:   "msg_41",
			LatestAssistantMessageID: msgID,
			LiveEdgeEntryID:          "tres_42",
			LiveEdgeMessageID:        0,
		}, nil
	}

	req := threadRequest(
		http.MethodGet,
		"/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/transcript?position=around&anchor_entry_id=msg_41",
		"",
		orgID,
		map[string]string{"id": sessionID.String(), "tid": threadID.String()},
	)
	w := httptest.NewRecorder()

	handler.GetThreadTranscript(w, req)
	require.Equal(t, http.StatusOK, w.Code, "GetThreadTranscript should return a successful response")

	var resp models.SessionTranscriptWindowResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "GetThreadTranscript response should be valid JSON")
	require.Equal(t, models.TranscriptWindowPositionAround, resp.Meta.Position, "response should include the resolved window position")
	require.True(t, resp.Meta.AnchorFound, "response should report that the requested anchor was found")
	require.Equal(t, "msg_41", resp.Meta.AnchorEntryID, "response should echo the resolved anchor entry id")
	require.Equal(t, "msg_41", resp.Meta.LatestAssistantEntryID, "response should include the latest assistant entry id")
	require.Equal(t, msgID, resp.Meta.LatestAssistantMessageID, "response should include the latest assistant message id")
	require.Equal(t, "tres_42", resp.Meta.LiveEdgeEntryID, "response should include the live edge entry id")
	require.Equal(t, int64(0), resp.Meta.LiveEdgeMessageID, "non-message live edges should not report a message id")
	require.Equal(t, olderCursor, resp.Meta.NextOlderCursor, "response should include the older cursor")
	require.Equal(t, newerCursor, resp.Meta.NextNewerCursor, "response should include the newer cursor")
	require.Equal(t, models.ThreadStatusIdle, resp.Meta.ThreadStatus, "response should include the thread status")
	require.Len(t, resp.Data, 1, "response should group rows into one turn")
	require.Len(t, resp.Data[0].Entries, 2, "response should include both transcript entries")
	require.Equal(t, "msg_41", resp.Data[0].Entries[0].ID, "message entry should use the stable message entry id")
	require.Equal(t, "tres_42", resp.Data[0].Entries[1].ID, "tool result entry should use the stable tool-result entry id")
	require.Equal(t, models.TranscriptEntryKindToolResult, resp.Data[0].Entries[1].Kind, "tool result logs should render as tool_result entries")
}

func TestSessionThreadHandler_SearchThreadTranscript_ResponseContract(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	ts := &mockTranscriptStore{}
	handler, deps := newThreadHandlerWithTranscript(t, ts)
	deps.threadStore.getByIDFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID) (models.SessionThread, error) {
		require.Equal(t, orgID, gotOrgID, "SearchThreadTranscript should scope the thread lookup by org")
		require.Equal(t, threadID, gotThreadID, "SearchThreadTranscript should load the requested thread")
		return models.SessionThread{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.ThreadStatusIdle,
		}, nil
	}
	ts.searchFn = func(_ context.Context, gotOrgID, gotThreadID uuid.UUID, opts db.SessionTranscriptSearchOptions) ([]db.SessionTranscriptSearchMatch, error) {
		require.Equal(t, orgID, gotOrgID, "transcript search store should receive the request org")
		require.Equal(t, threadID, gotThreadID, "transcript search store should receive the request thread")
		require.Equal(t, "focused tests", opts.Query, "transcript search should pass the query text")
		require.Equal(t, 5, opts.Limit, "transcript search should pass the requested limit")
		require.True(t, opts.Include.Messages, "include should allow searching messages")
		require.True(t, opts.Include.Tools, "include should allow searching tools")
		require.False(t, opts.Include.HumanInputs, "omitted include kinds should be false")
		return []db.SessionTranscriptSearchMatch{
			{
				EntryID:    "msg_41",
				Kind:       models.TranscriptEntryKindMessage,
				TurnNumber: 7,
				CreatedAt:  now,
				Snippet:    "Run focused tests",
				MessageID:  41,
				Role:       models.MessageRoleUser,
			},
		}, nil
	}

	req := threadRequest(
		http.MethodGet,
		"/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/transcript/search?q=focused+tests&limit=5&include=messages,tools",
		"",
		orgID,
		map[string]string{"id": sessionID.String(), "tid": threadID.String()},
	)
	w := httptest.NewRecorder()

	handler.SearchThreadTranscript(w, req)
	require.Equal(t, http.StatusOK, w.Code, "SearchThreadTranscript should return a successful response")

	var resp models.SessionTranscriptSearchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "SearchThreadTranscript response should be valid JSON")
	require.Equal(t, "focused tests", resp.Meta.Query, "response meta should echo the search query")
	require.Equal(t, 5, resp.Meta.Limit, "response meta should report the effective limit")
	require.Equal(t, []models.SessionTranscriptSearchMatch{
		{
			EntryID:    "msg_41",
			Kind:       models.TranscriptEntryKindMessage,
			TurnNumber: 7,
			CreatedAt:  now,
			Snippet:    "Run focused tests",
			MessageID:  41,
			Role:       models.MessageRoleUser,
		},
	}, resp.Data, "response should return the expected search matches")
}
