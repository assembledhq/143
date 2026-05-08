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
	updateStatusFn   func(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
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
	return models.Session{ID: sessionID, OrgID: orgID, Status: string(models.SessionStatusRunning)}, nil
}

func (m *mockSessionStoreForThread) ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	if m.claimForResumeFn != nil {
		return m.claimForResumeFn(ctx, orgID, sessionID)
	}
	return models.Session{}, fmt.Errorf("no rows")
}

func (m *mockSessionStoreForThread) UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error {
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

func (m *mockJobStore) EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	return m.Enqueue(ctx, orgID, opts.Queue, opts.JobType, opts.Payload, opts.Priority, opts.DedupeKey)
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
				var resp models.SingleResponse[models.SessionMessage]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, "please continue", resp.Data.Content, "should return the message content")
				require.Equal(t, models.MessageRoleUser, resp.Data.Role, "should set the message role to user")
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

			req := threadRequest(http.MethodGet, "/api/v1/sessions/"+tt.sessionIDParam+"/threads/"+tt.threadIDParam+"/logs", "", orgID, map[string]string{"id": tt.sessionIDParam, "tid": tt.threadIDParam})
			w := httptest.NewRecorder()

			handler.GetThreadLogs(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			if tt.expectedError != "" {
				var errResp models.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedError, errResp.Error.Code, "should return expected error code")
			} else {
				var resp models.ListResponse[models.SessionLog]
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response body should be valid JSON")
				require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of logs")
			}
		})
	}
}
