package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
)

type fakeInternalSessionLookup struct {
	session models.Session
	err     error
}

func (f fakeInternalSessionLookup) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return f.session, f.err
}

type fakeInternalOrgLookup struct {
	org models.Organization
	err error
}

func (f fakeInternalOrgLookup) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	return f.org, f.err
}

type fakeInternalThreadService struct {
	threads              []models.SessionThread
	includeArchivedCalls []bool
	getThreadResult      models.SessionThread
	getThreadErr         error
	createInput          *thread.CreateThreadInput
	createResult         *models.SessionThread
	sendInput            *thread.SendMessageInput
	messageWindowResult  thread.MessageWindowResult
}

func (f *fakeInternalThreadService) CreateThread(_ context.Context, input thread.CreateThreadInput) (*models.SessionThread, error) {
	f.createInput = &input
	if f.createResult != nil {
		return f.createResult, nil
	}
	return &models.SessionThread{
		ID:                uuid.New(),
		SessionID:         input.SessionID,
		OrgID:             input.OrgID,
		AgentType:         models.AgentType(input.AgentType),
		Label:             input.Label,
		Status:            models.ThreadStatusIdle,
		CreatedBySource:   input.CreatedBySource,
		CreatedByThreadID: input.CreatedByThreadID,
		CreatedAt:         time.Now(),
	}, nil
}
func (f *fakeInternalThreadService) UpdateThread(context.Context, thread.UpdateThreadInput) (*models.SessionThread, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) ArchiveThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) ListThreads(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]models.SessionThread, error) {
	return f.threads, nil
}
func (f *fakeInternalThreadService) ListThreadsWithOptions(_ context.Context, _ uuid.UUID, _ uuid.UUID, opts thread.ListThreadsOptions) ([]models.SessionThread, error) {
	f.includeArchivedCalls = append(f.includeArchivedCalls, opts.IncludeArchived)
	return f.threads, nil
}
func (f *fakeInternalThreadService) GetThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
	return f.getThreadResult, f.getThreadErr
}
func (f *fakeInternalThreadService) SendMessage(_ context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
	f.sendInput = &input
	return &thread.SendMessageResult{
		Message: &models.SessionMessage{
			ID:        1,
			SessionID: input.SessionID,
			OrgID:     input.OrgID,
			ThreadID:  &input.ThreadID,
			Role:      models.MessageRoleUser,
			Content:   input.Message,
			Source:    input.MessageSource,
			CreatedAt: time.Now(),
		},
		ThreadStatus:  models.ThreadStatusPending,
		DeliveryState: models.ThreadInboxDeliveryStatePending,
	}, nil
}
func (f *fakeInternalThreadService) EndThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) GetMessages(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) ([]models.SessionMessage, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) GetMessageWindow(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, db.SessionMessageWindowOptions) (thread.MessageWindowResult, error) {
	return f.messageWindowResult, nil
}
func (f *fakeInternalThreadService) GetLogs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, db.SessionLogFilterOptions) ([]models.SessionLog, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) CancelThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) ListFileEvents(context.Context, uuid.UUID, uuid.UUID, *time.Time) ([]models.SessionThreadFileEvent, error) {
	return nil, nil
}
func (f *fakeInternalThreadService) ListRecoverableInboxEntries(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) ([]models.ThreadInboxEntry, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) RetryInboxEntry(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, bool) (models.ThreadInboxEntry, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) ForkThread(context.Context, thread.ForkInput) (thread.ForkResult, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) RevertThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID) (thread.ForkResult, error) {
	panic("not implemented")
}
func (f *fakeInternalThreadService) GetTranscriptWindow(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, db.SessionTranscriptWindowOptions) (thread.TranscriptWindowResult, error) {
	panic("not implemented")
}

func TestInternalSessionTabsHandler_List(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")

	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{threads: []models.SessionThread{{
			ID:        threadID,
			SessionID: sessionID,
			OrgID:     orgID,
			AgentType: models.AgentTypeCodex,
			Label:     "Codex",
			Status:    models.ThreadStatusRunning,
			CreatedAt: time.Now(),
		}}},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.List(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "List should allow default-on tab tools")
	require.Contains(t, rr.Body.String(), `"is_current":true`, "List should mark token source thread as current")
}

func TestInternalSessionTabsHandler_ListRejectsRepositoryMismatch(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	tokenRepoID := uuid.New()
	sessionRepoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, tokenRepoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &sessionRepoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.List(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code, "List should reject tokens scoped to a different repository")
	require.Contains(t, rr.Body.String(), "INVALID_SANDBOX_TOKEN", "repository mismatch should use the sandbox token error code")
}

func TestInternalSessionTabsHandler_ListForwardsIncludeArchived(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	threads := &fakeInternalThreadService{}
	handler := NewInternalSessionTabsHandler(
		threads,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs?include_archived=true", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.List(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "List should allow include_archived")
	require.Equal(t, []bool{true}, threads.includeArchivedCalls, "List should forward include_archived to the thread service")
}

func TestInternalSessionTabsHandler_ListRejectsInvalidIncludeArchived(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	threads := &fakeInternalThreadService{}
	handler := NewInternalSessionTabsHandler(
		threads,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs?include_archived=maybe", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.List(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "List should reject malformed include_archived values")
	require.Contains(t, rr.Body.String(), "INVALID_INCLUDE_ARCHIVED", "List should return a stable error code for invalid include_archived")
	require.Empty(t, threads.includeArchivedCalls, "List should not call the service for invalid include_archived")
}

func TestInternalSessionTabsHandler_ListRejectsSessionLookupError(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{},
		fakeInternalSessionLookup{err: fmt.Errorf("db timeout")},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.List(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code, "List should return 401 when session lookup fails")
	require.Contains(t, rr.Body.String(), "INVALID_SANDBOX_TOKEN", "session lookup failure should use the sandbox token error code")
}

func TestInternalSessionTabsHandler_ListDisabled(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")

	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{"coding_agent_tab_tools_enabled":false}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.List(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code, "List should reject disabled tab tools")
	require.Contains(t, rr.Body.String(), "TAB_TOOLS_DISABLED", "disabled response should use stable error code")
}

func TestInternalSessionTabsHandler_ListInvalidSettingsFailsClosed(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")

	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{"coding_agent_tab_tools_enabled":"yes"}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.List(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code, "List should fail closed when tab-tool settings cannot be parsed")
	require.Contains(t, rr.Body.String(), "TAB_TOOLS_DISABLED", "invalid settings should use the same closed-state error code")
}

func TestInternalSessionTabsHandler_CreateUsesOrgDefaultWithoutSourceThread(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	threads := &fakeInternalThreadService{}
	handler := NewInternalSessionTabsHandler(
		threads,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{"default_agent_type":"claude_code"}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/session-tabs", bytes.NewBufferString(`{"label":"Review"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, "Create should create a tab")
	require.NotNil(t, threads.createInput, "Create should call the thread service")
	require.Equal(t, "claude_code", threads.createInput.AgentType, "Create should default to the org default agent when no source thread is available")
}

func TestInternalSessionTabsHandler_GetRejectsThreadOutsideSession(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{getThreadErr: thread.ErrThreadNotFound},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs/"+threadID.String(), nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	chi.RouteContext(req.Context()).URLParams.Add("thread_id", threadID.String())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code, "Get should hide threads outside the token session")
	require.Contains(t, rr.Body.String(), "TAB_NOT_FOUND", "cross-session thread lookups should use the tab not found error")
}

func TestInternalSessionTabsHandler_CreateMarksAgentToolProvenance(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	sourceThreadID := uuid.New()
	token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &sourceThreadID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	threads := &fakeInternalThreadService{}
	handler := NewInternalSessionTabsHandler(
		threads,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/session-tabs", bytes.NewBufferString(`{"label":"Review","agent_type":"codex"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, "Create should create a tab")
	require.NotNil(t, threads.createInput, "Create should call the thread service")
	require.Equal(t, models.ThreadCreatedBySourceAgentTool, threads.createInput.CreatedBySource, "Create should mark agent tool provenance")
	require.Equal(t, sourceThreadID, *threads.createInput.CreatedByThreadID, "Create should preserve source thread provenance")
}

func TestInternalSessionTabsHandler_SendMessageMarksAgentToolSource(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	threads := &fakeInternalThreadService{}
	handler := NewInternalSessionTabsHandler(
		threads,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/session-tabs/"+threadID.String()+"/messages", bytes.NewBufferString(`{"message":"run tests","client_message_id":"agent-tool-test"}`))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	chi.RouteContext(req.Context()).URLParams.Add("thread_id", threadID.String())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.SendMessage(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "SendMessage should accept the message")
	require.NotNil(t, threads.sendInput, "SendMessage should call the thread service")
	require.Equal(t, models.SessionMessageSourceAgentTool, threads.sendInput.MessageSource, "SendMessage should mark message source")
	require.Equal(t, "agent-tool-test", threads.sendInput.ClientMessageID, "SendMessage should forward the idempotency key")
}

func TestInternalSessionTabsHandler_SendMessageResponseIncludesPendingCount(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	model := "gpt-5.3-codex"
	token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	threads := &fakeInternalThreadService{
		getThreadResult: models.SessionThread{
			ID:                  threadID,
			SessionID:           sessionID,
			OrgID:               orgID,
			AgentType:           models.AgentTypeCodex,
			ModelOverride:       &model,
			Status:              models.ThreadStatusPending,
			PendingMessageCount: 2,
		},
	}
	handler := NewInternalSessionTabsHandler(
		threads,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/session-tabs/"+threadID.String()+"/messages", bytes.NewBufferString(`{"message":"run tests"}`))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	chi.RouteContext(req.Context()).URLParams.Add("thread_id", threadID.String())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.SendMessage(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "SendMessage should accept the message")
	require.Contains(t, rr.Body.String(), `"pending_message_count":2`, "SendMessage should include the target thread pending count")
}

func TestInternalSessionTabsHandler_MessagesRejectsInvalidPosition(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs/"+threadID.String()+"/messages?position=oldest", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	chi.RouteContext(req.Context()).URLParams.Add("thread_id", threadID.String())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.Messages(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "Messages should reject unsupported positions")
	require.Contains(t, rr.Body.String(), "INVALID_POSITION", "Messages should return a stable error code for unsupported positions")
}

func TestInternalSessionTabsHandler_MessagesRejectsInvalidIncludeToolEvents(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs/"+threadID.String()+"/messages?include_tool_events=maybe", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	chi.RouteContext(req.Context()).URLParams.Add("thread_id", threadID.String())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.Messages(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, "Messages should reject malformed include_tool_events values")
	require.Contains(t, rr.Body.String(), "INVALID_INCLUDE_TOOL_EVENTS", "Messages should return a stable error code for invalid include_tool_events")
}

func TestInternalSessionTabsHandler_MessagesRejectsInvalidPaginationParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		query         string
		expectedError string
	}{
		{
			name:          "invalid limit",
			query:         "limit=abc",
			expectedError: "INVALID_LIMIT",
		},
		{
			name:          "invalid before cursor",
			query:         "before=not-a-cursor",
			expectedError: "INVALID_BEFORE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secret := "test-secret"
			orgID := uuid.New()
			repoID := uuid.New()
			sessionID := uuid.New()
			threadID := uuid.New()
			token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, time.Minute)
			require.NoError(t, err, "test should create sandbox token")
			handler := NewInternalSessionTabsHandler(
				&fakeInternalThreadService{},
				fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
				fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
				secret,
				zerolog.Nop(),
			)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs/"+threadID.String()+"/messages?"+tt.query, nil)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
			chi.RouteContext(req.Context()).URLParams.Add("thread_id", threadID.String())
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()

			handler.Messages(rr, req)
			require.Equal(t, http.StatusBadRequest, rr.Code, "Messages should reject malformed pagination parameters")
			require.Contains(t, rr.Body.String(), tt.expectedError, "Messages should return the expected pagination error code")
		})
	}
}

func TestInternalSessionTabsHandler_MessagesReturnsNewestFirstWithCursor(t *testing.T) {
	t.Parallel()

	secret := "test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	token, err := auth.GenerateSessionThreadToken(secret, orgID, repoID, sessionID, &threadID, time.Minute)
	require.NoError(t, err, "test should create sandbox token")
	handler := NewInternalSessionTabsHandler(
		&fakeInternalThreadService{messageWindowResult: thread.MessageWindowResult{Window: db.SessionMessageWindow{
			Messages: []models.SessionMessage{
				{ID: 1, OrgID: orgID, SessionID: sessionID, ThreadID: &threadID, Role: models.MessageRoleUser, Content: "older"},
				{ID: 2, OrgID: orgID, SessionID: sessionID, ThreadID: &threadID, Role: models.MessageRoleAssistant, Content: "newer"},
			},
			NextOlderCursor: "1",
		}}},
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}},
		secret,
		zerolog.Nop(),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/session-tabs/"+threadID.String()+"/messages?position=latest", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	chi.RouteContext(req.Context()).URLParams.Add("thread_id", threadID.String())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	handler.Messages(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "Messages should return a window")
	require.Less(t, strings.Index(rr.Body.String(), "newer"), strings.Index(rr.Body.String(), "older"), "Messages should return newest messages first")
	require.Contains(t, rr.Body.String(), `"next_cursor":"1"`, "Messages should preserve the next older cursor")
}

func TestBuildSessionTabToolAuditDetails(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	sourceThreadID := uuid.New()
	targetThreadID := uuid.New()
	model := "gpt-5.3-codex"
	scope := sandboxSessionTabScope{SessionID: sessionID, SourceThreadID: &sourceThreadID}

	details := buildSessionTabToolAuditDetails(scope, targetThreadID, models.AgentTypeCodex, &model, "session_tabs_send", 42)
	require.Equal(t, sessionID.String(), details["session_id"], "audit details should include the session id")
	require.Equal(t, sourceThreadID.String(), details["source_thread_id"], "audit details should include the source thread id")
	require.Equal(t, targetThreadID.String(), details["target_thread_id"], "audit details should include the target thread id")
	require.Equal(t, "codex", details["agent_type"], "audit details should include the agent type")
	require.Equal(t, model, details["model"], "audit details should include the model")
	require.Equal(t, "session_tabs_send", details["tool_name"], "audit details should include the tool name")
	require.Equal(t, 42, details["message_length"], "audit details should include message length")
}
