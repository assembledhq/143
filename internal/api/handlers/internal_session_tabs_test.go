package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
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

type internalTabOutcomeService struct {
	ThreadService
	send     func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error)
	getCalls int
}

func (s *internalTabOutcomeService) SendMessage(ctx context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
	return s.send(ctx, input)
}

func (s *internalTabOutcomeService) GetThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
	s.getCalls++
	return models.SessionThread{}, nil
}

type internalTabAuditProbe struct {
	db.DBTX
	calls int
}

func (p *internalTabAuditProbe) QueryRow(context.Context, string, ...any) pgx.Row {
	p.calls++
	return internalTabUnexpectedAuditRow{}
}

type internalTabUnexpectedAuditRow struct{}

func (internalTabUnexpectedAuditRow) Scan(...any) error {
	return errors.New("unexpected tab message audit")
}

func internalTabSendRequest(t *testing.T, orgID, repoID, sessionID, laneID uuid.UUID, clientKey string) *http.Request {
	t.Helper()
	token, err := auth.GenerateSessionThreadToken("test-secret", orgID, repoID, sessionID, &laneID, time.Minute)
	require.NoError(t, err, "prepare token for the exact authorized tab scope")
	requestBody := map[string]string{"message": "  synthetic message intent  "}
	if clientKey != "" {
		requestBody["client_message_id"] = " " + clientKey + " "
	}
	body, err := json.Marshal(requestBody)
	require.NoError(t, err, "encode the original optional command identity")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/session-tabs/"+laneID.String()+"/messages", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	chi.RouteContext(req.Context()).URLParams.Add("thread_id", laneID.String())
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestInternalSessionTabsHandler_SendMessageUnknownPrecedesDomainErrors(t *testing.T) {
	t.Parallel()
	causes := []struct {
		name  string
		cause error
		join  bool
	}{
		{name: "not found", cause: thread.ErrThreadNotFound},
		{name: "wrapped guard missing lane", cause: fmt.Errorf("thread send exact lane missing: %w", pgx.ErrNoRows)},
		{name: "raw lock timeout", cause: &pgconn.PgError{Code: "55P03", Message: "synthetic private lock diagnostic"}},
		{name: "backpressure", cause: thread.ErrThreadInboxBackpressure},
		{name: "contention", cause: db.ErrLifecycleContention},
		{name: "normalization", cause: thread.ErrSendNormalizing},
		{name: "mode change", cause: db.ErrThreadSendModeChanged},
		{name: "joined not found first", cause: thread.ErrThreadNotFound, join: true},
		{name: "joined guard missing lane first", cause: fmt.Errorf("thread send exact lane missing: %w", pgx.ErrNoRows), join: true},
		{name: "joined contention first", cause: db.ErrLifecycleContention, join: true},
	}
	for _, cause := range causes {
		t.Run(cause.name, func(t *testing.T) {
			t.Parallel()
			keys := []struct{ name, key string }{{name: "provided key", key: "synthetic-tab-key"}, {name: "absent key"}}
			for _, key := range keys {
				t.Run(key.name, func(t *testing.T) {
					t.Parallel()
					orgID, repoID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
					kind, suffix := "thread_client_message", key.key
					if key.key == "" {
						kind, suffix = "thread_server_command", uuid.NewString()
					}
					ingressKey := orgID.String() + ":" + sessionID.String() + ":" + laneID.String() + ":" + suffix
					ingress, err := db.NewLifecycleIngress(kind, ingressKey, db.LifecycleDigest{1})
					require.NoError(t, err, "prepare the exact scoped identity retained by the service")
					var sendErr error = &thread.CommandOutcomeUnknownError{Ingress: ingress, Cause: cause.cause}
					if cause.join {
						sendErr = errors.Join(cause.cause, sendErr)
					}
					sendErr = fmt.Errorf("synthetic private diagnostic: %w", sendErr)
					calls := 0
					svc := &internalTabOutcomeService{send: func(_ context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
						calls++
						require.Equal(t, thread.SendMessageInput{OrgID: orgID, SessionID: sessionID, ThreadID: laneID, Message: "synthetic message intent", ClientMessageID: key.key, MessageSource: models.SessionMessageSourceAgentTool}, input, "internal adapter must retain authorized scope and original optional identity without minting a replacement")
						return nil, sendErr
					}}
					handler := NewInternalSessionTabsHandler(svc, fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}}, fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}}, "test-secret", zerolog.Nop())
					audit := &internalTabAuditProbe{}
					handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(audit), zerolog.Nop()))
					recorder := httptest.NewRecorder()
					handler.SendMessage(recorder, internalTabSendRequest(t, orgID, repoID, sessionID, laneID, key.key))
					message := "message acceptance is pending confirmation; retain the same command key and do not resubmit"
					if key.key == "" {
						message = "message acceptance is pending confirmation; do not resubmit the message"
					}
					expected := models.ErrorResponse{Error: models.ErrorDetail{Code: "COMMAND_OUTCOME_UNKNOWN", Message: message, Details: sessionMessageUnknownDetails{IngressKind: models.SessionMessageIngressKind(kind), IngressKey: ingressKey}}}
					encoded, err := json.Marshal(expected)
					require.NoError(t, err, "encode the complete safe unknown response")
					require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "uncertainty must dominate every wrapped or joined domain and contention error")
					require.Equal(t, string(encoded)+"\n", recorder.Body.String(), "unknown response must expose only safe original identity and explicit no-resubmit guidance")
					require.Equal(t, "", recorder.Header().Get("Retry-After"), "unknown outcome must not advertise a retry delay")
					require.Equal(t, 1, calls, "unknown outcome must never cause a second SendMessage call")
					require.Zero(t, svc.getCalls, "unknown outcome must not perform postcommit thread reads")
					require.Zero(t, audit.calls, "unknown outcome must not emit a successful-send audit")
				})
			}
		})
	}
}

func TestInternalSessionTabsHandler_SendMessageKnownErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		cause                     error
		status                    int
		code, message, retryAfter string
	}{
		{name: "contention", cause: db.ErrLifecycleContention, status: http.StatusConflict, code: "LIFECYCLE_CONTENDED", message: "message admission is busy", retryAfter: "1"},
		{name: "mode changed", cause: db.ErrThreadSendModeChanged, status: http.StatusConflict, code: "LIFECYCLE_CONTENDED", message: "message admission is busy", retryAfter: "1"},
		{name: "receipt observation needed", cause: db.ErrMessageAdmissionReplayObservationNeeded, status: http.StatusConflict, code: "LIFECYCLE_CONTENDED", message: "message admission is busy", retryAfter: "1"},
		{name: "normalization pending", cause: thread.ErrSendNormalizing, status: http.StatusConflict, code: "NORMALIZATION_PENDING", message: "this session is preparing its message protocol", retryAfter: "1"},
		{name: "legacy not found", cause: thread.ErrThreadNotFound, status: http.StatusNotFound, code: "TAB_NOT_FOUND", message: "tab not found"},
		{name: "wrapped guard missing lane", cause: fmt.Errorf("thread send exact lane missing: %w", pgx.ErrNoRows), status: http.StatusNotFound, code: "TAB_NOT_FOUND", message: "tab not found"},
		{name: "legacy backpressure", cause: thread.ErrThreadInboxBackpressure, status: http.StatusConflict, code: "THREAD_INBOX_BACKPRESSURE", message: "too many undelivered messages"},
		{name: "ordinary dependency failure", cause: errors.New("synthetic private failure"), status: http.StatusInternalServerError, code: "SEND_MESSAGE_FAILED", message: "failed to send message"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			keys := []struct{ name, key string }{{name: "provided key", key: "synthetic-tab-key"}, {name: "absent key"}}
			for _, key := range keys {
				t.Run(key.name, func(t *testing.T) {
					t.Parallel()
					orgID, repoID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
					calls := 0
					svc := &internalTabOutcomeService{send: func(_ context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
						calls++
						require.Equal(t, key.key, input.ClientMessageID, "known refusal must retain the original optional key")
						return nil, fmt.Errorf("synthetic private diagnostic: %w", tt.cause)
					}}
					handler := NewInternalSessionTabsHandler(svc, fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}}, fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}}, "test-secret", zerolog.Nop())
					audit := &internalTabAuditProbe{}
					handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(audit), zerolog.Nop()))
					recorder := httptest.NewRecorder()
					handler.SendMessage(recorder, internalTabSendRequest(t, orgID, repoID, sessionID, laneID, key.key))
					message := tt.message
					if tt.retryAfter != "" {
						message += "; retry the request"
						if key.key != "" {
							message = tt.message + "; retry with the same command key"
						}
					}
					expected, err := json.Marshal(models.ErrorResponse{Error: models.ErrorDetail{Code: tt.code, Message: message}})
					require.NoError(t, err, "encode the exact safe refusal response")
					require.Equal(t, tt.status, recorder.Code, "known refusal should preserve the specified retryable or legacy HTTP status")
					require.Equal(t, string(expected)+"\n", recorder.Body.String(), "known refusal must not leak diagnostic text or manufacture a command key")
					require.Equal(t, tt.retryAfter, recorder.Header().Get("Retry-After"), "only known retryable protocol refusals should carry a retry delay")
					require.Equal(t, 1, calls, "known refusal must leave retries to the caller")
					require.Zero(t, svc.getCalls, "known refusal must not perform postcommit thread reads")
					require.Zero(t, audit.calls, "known refusal must not emit a successful-send audit")
				})
			}
		})
	}
}

// This CPU fixture drives the real legacy scope guard through an empty exact
// lane discovery. It does not establish PostgreSQL locking or race behavior.
type internalTabMissingLaneGuardTx struct {
	*handlerSendFixtureTx
}

func (f *internalTabMissingLaneGuardTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if query != handlerSendLaneKeysSQL {
		return f.handlerSendFixtureTx.Query(ctx, query, args...)
	}
	require.Equal(f.t, 5, f.stage, "the real guard must acquire the parent before discovering the missing lane")
	require.Equal(f.t, []any{f.org, f.session}, args[:2], "missing-lane discovery must retain the exact authorized tenant and session")
	f.stage++
	f.guard.ExpectQuery("SELECT id FROM session_threads").WithArgs(args...).WillReturnRows(pgxmock.NewRows([]string{"id"}))
	return f.guard.Query(ctx, query, args...)
}

func TestInternalSessionTabsHandler_SendMessageMissingLaneGuardReturnsNotFound(t *testing.T) {
	t.Parallel()
	keys := []struct{ name, key string }{{name: "provided key", key: "synthetic-tab-key"}, {name: "absent key"}}
	for _, key := range keys {
		t.Run(key.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			guard, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated CPU SQL fixture for the actual legacy scope guard")
			t.Cleanup(guard.Close)
			guardTx := &internalTabMissingLaneGuardTx{handlerSendFixtureTx: &handlerSendFixtureTx{t: t, guard: guard}}
			calls := 0
			svc := &internalTabOutcomeService{send: func(ctx context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
				calls++
				require.Equal(t, thread.SendMessageInput{OrgID: orgID, SessionID: sessionID, ThreadID: laneID, Message: "synthetic message intent", ClientMessageID: key.key, MessageSource: models.SessionMessageSourceAgentTool}, input, "the exact authorized request must reach the real legacy guard without replacing its optional key")
				unitCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
				defer cancel()
				err := db.RequireLegacyThreadSendScopeInTx(unitCtx, guardTx, input.OrgID, input.SessionID, input.ThreadID)
				require.ErrorIs(t, err, pgx.ErrNoRows, "empty exact lane discovery must produce the actual wrapped missing-row cause")
				require.EqualError(t, err, "thread send exact lane missing: no rows in result set", "the regression must use the real guard error rather than a handler-only sentinel")
				return nil, err
			}}
			handler := NewInternalSessionTabsHandler(svc, fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}}, fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}}, "test-secret", zerolog.Nop())
			audit := &internalTabAuditProbe{}
			handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(audit), zerolog.Nop()))
			recorder := httptest.NewRecorder()
			handler.SendMessage(recorder, internalTabSendRequest(t, orgID, repoID, sessionID, laneID, key.key))
			expected, err := json.Marshal(models.ErrorResponse{Error: models.ErrorDetail{Code: "TAB_NOT_FOUND", Message: "tab not found"}})
			require.NoError(t, err, "encode the existing safe missing-tab response")
			require.Equal(t, 6, guardTx.stage, "the real guard must stop at missing exact lane discovery before lane locks or mutation")
			require.NoError(t, guard.ExpectationsWereMet(), "all real guard SQL responses must be consumed")
			require.Equal(t, http.StatusNotFound, recorder.Code, "a lane removed before the atomic legacy guard must remain a missing tab rather than a server failure")
			require.Equal(t, string(expected)+"\n", recorder.Body.String(), "the actual missing-lane guard must preserve the existing safe 404 body")
			require.Empty(t, recorder.Header().Get("Retry-After"), "a known missing lane must not advertise retry")
			require.Equal(t, 1, calls, "the handler must never resend after a missing-lane guard refusal")
			require.Zero(t, svc.getCalls, "a missing-lane guard refusal must not trigger post-error thread reads")
			require.Zero(t, audit.calls, "a missing-lane guard refusal must not emit a success audit")
		})
	}
}

func TestInternalSessionTabsHandler_SendMessageUnknownDetailsRespectScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, scope, kind, suffix, key string
		sentinel, noDiagnostic         bool
	}{
		{name: "foreign organization", scope: "org", kind: "thread_client_message", suffix: "synthetic-tab-key", key: "synthetic-tab-key"},
		{name: "foreign session", scope: "session", kind: "thread_client_message", suffix: "synthetic-tab-key", key: "synthetic-tab-key"},
		{name: "foreign lane", scope: "lane", kind: "thread_client_message", suffix: "synthetic-tab-key", key: "synthetic-tab-key"},
		{name: "different client identity", kind: "thread_client_message", suffix: "untrusted-key", key: "synthetic-tab-key"},
		{name: "unrecognized kind", kind: "provider_launch", suffix: "untrusted-key", key: "synthetic-tab-key"},
		{name: "invalid server identity", kind: "thread_server_command", suffix: "untrusted-key"},
		{name: "no diagnostic or caller identity", kind: "thread_server_command", suffix: "untrusted-key", noDiagnostic: true},
		{name: "sentinel retains caller identity", key: "synthetic-tab-key", sentinel: true},
		{name: "keyless sentinel invents nothing", sentinel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID, sessionID, laneID, diagnosticID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			keyOrg, keySession, keyLane := orgID, sessionID, laneID
			switch tt.scope {
			case "org":
				keyOrg = uuid.New()
			case "session":
				keySession = uuid.New()
			case "lane":
				keyLane = uuid.New()
			}
			if tt.noDiagnostic {
				diagnosticID = uuid.Nil
			}
			var sendErr error = thread.ErrCommandOutcomeUnknown
			var details any
			fallback := sessionMessageUnknownDetails{ClientMessageID: tt.key}
			if !tt.sentinel {
				key := keyOrg.String() + ":" + keySession.String() + ":" + keyLane.String() + ":" + tt.suffix
				ingress, err := db.NewLifecycleIngress(tt.kind, key, db.LifecycleDigest{1})
				require.NoError(t, err, "prepare untrusted retained identity without granting disclosure authority")
				sendErr = &thread.CommandOutcomeUnknownError{Ingress: ingress, DiagnosticID: diagnosticID, ClientMessageID: "untrusted-internal-client-key", Cause: errors.New("synthetic private cause")}
				if diagnosticID != uuid.Nil {
					fallback.DiagnosticID = &diagnosticID
				}
			}
			if fallback.DiagnosticID != nil || fallback.ClientMessageID != "" {
				details = fallback
			}
			calls := 0
			svc := &internalTabOutcomeService{send: func(_ context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
				calls++
				require.Equal(t, thread.SendMessageInput{OrgID: orgID, SessionID: sessionID, ThreadID: laneID, Message: "synthetic message intent", ClientMessageID: tt.key, MessageSource: models.SessionMessageSourceAgentTool}, input, "scope checks must use the current authorized request, not retained foreign receipt fields")
				return nil, sendErr
			}}
			handler := NewInternalSessionTabsHandler(svc, fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}}, fakeInternalOrgLookup{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{}`)}}, "test-secret", zerolog.Nop())
			audit := &internalTabAuditProbe{}
			handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(audit), zerolog.Nop()))
			recorder := httptest.NewRecorder()
			handler.SendMessage(recorder, internalTabSendRequest(t, orgID, repoID, sessionID, laneID, tt.key))
			message := "message acceptance is pending confirmation; do not resubmit the message"
			if tt.key != "" {
				message = "message acceptance is pending confirmation; retain the same command key and do not resubmit"
			}
			expected, err := json.Marshal(models.ErrorResponse{Error: models.ErrorDetail{Code: "COMMAND_OUTCOME_UNKNOWN", Message: message, Details: details}})
			require.NoError(t, err, "encode safe diagnostic correlation without untrusted receipt data")
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "unresolved identity remains unknown rather than becoming a new command")
			require.Equal(t, string(expected)+"\n", recorder.Body.String(), "unknown response must disclose no foreign scope, untrusted key, raw cause, or original message")
			require.Equal(t, "", recorder.Header().Get("Retry-After"), "diagnostic correlation does not authorize retry")
			require.Equal(t, 1, calls, "unknown fallback must not resend")
			require.Zero(t, svc.getCalls, "unknown fallback must not read thread state after the error")
			require.Zero(t, audit.calls, "unknown fallback must not emit a success audit")
		})
	}
}

type internalTabSessionLookupFunc func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error)

func (f internalTabSessionLookupFunc) GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	return f(ctx, orgID, sessionID)
}

type internalTabOrgLookupFunc func(context.Context, uuid.UUID) (models.Organization, error)

func (f internalTabOrgLookupFunc) GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error) {
	return f(ctx, orgID)
}

func TestInternalSessionTabsHandler_SendMessageRetainsAuthorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                               string
		sessionError, foreignRepo, orgError, toolsDisabled bool
		status                                             int
		code, message                                      string
	}{
		{name: "session lookup failure", sessionError: true, status: http.StatusUnauthorized, code: "INVALID_SANDBOX_TOKEN", message: "sandbox token is not authorized for this session"},
		{name: "different repository", foreignRepo: true, status: http.StatusUnauthorized, code: "INVALID_SANDBOX_TOKEN", message: "sandbox token is not authorized for this session"},
		{name: "organization lookup failure", orgError: true, status: http.StatusUnauthorized, code: "INVALID_SANDBOX_TOKEN", message: "sandbox token organization is unavailable"},
		{name: "tools disabled", toolsDisabled: true, status: http.StatusForbidden, code: "TAB_TOOLS_DISABLED", message: "agent tab tools are disabled for this organization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			sessionCalls, orgCalls, sendCalls := 0, 0, 0
			svc := &internalTabOutcomeService{send: func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error) {
				sendCalls++
				return nil, thread.ErrCommandOutcomeUnknown
			}}
			sessions := internalTabSessionLookupFunc(func(_ context.Context, actualOrgID, actualSessionID uuid.UUID) (models.Session, error) {
				sessionCalls++
				require.Equal(t, orgID, actualOrgID, "current session authorization must remain scoped by token organization")
				require.Equal(t, sessionID, actualSessionID, "current authorization must read the token's exact session")
				if tt.sessionError {
					return models.Session{}, errors.New("synthetic unavailable session")
				}
				currentRepoID := repoID
				if tt.foreignRepo {
					currentRepoID = uuid.New()
				}
				return models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &currentRepoID}, nil
			})
			orgs := internalTabOrgLookupFunc(func(_ context.Context, actualOrgID uuid.UUID) (models.Organization, error) {
				orgCalls++
				require.Equal(t, orgID, actualOrgID, "current organization lookup must retain token scope")
				if tt.orgError {
					return models.Organization{}, errors.New("synthetic unavailable organization")
				}
				settings := json.RawMessage(`{}`)
				if tt.toolsDisabled {
					settings = json.RawMessage(`{"coding_agent_tab_tools_enabled":false}`)
				}
				return models.Organization{ID: orgID, Settings: settings}, nil
			})
			handler := NewInternalSessionTabsHandler(svc, sessions, orgs, "test-secret", zerolog.Nop())
			audit := &internalTabAuditProbe{}
			handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(audit), zerolog.Nop()))
			recorder := httptest.NewRecorder()
			handler.SendMessage(recorder, internalTabSendRequest(t, orgID, repoID, sessionID, laneID, "synthetic-tab-key"))
			expected, err := json.Marshal(models.ErrorResponse{Error: models.ErrorDetail{Code: tt.code, Message: tt.message}})
			require.NoError(t, err, "encode existing authorization response")
			require.Equal(t, tt.status, recorder.Code, "outcome rendering must not bypass current authorization")
			require.Equal(t, string(expected)+"\n", recorder.Body.String(), "unauthorized request must expose only the existing safe refusal")
			require.Equal(t, 1, sessionCalls, "authorization must consult current session ownership once")
			expectedOrgCalls := 1
			if tt.sessionError || tt.foreignRepo {
				expectedOrgCalls = 0
			}
			require.Equal(t, expectedOrgCalls, orgCalls, "failed session scope must stop before organization or command processing")
			require.Zero(t, sendCalls, "unauthorized request must never enter SendMessage")
			require.Zero(t, svc.getCalls, "unauthorized request must not read target thread state")
			require.Zero(t, audit.calls, "unauthorized request must not emit a success audit")
		})
	}
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
