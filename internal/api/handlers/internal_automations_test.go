package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

const internalAutomationSecret = "test-secret-32-chars-long-enough-auto"

type fakeInternalAutomationDelegate struct {
	createCalled bool
	runCalled    bool
	gotOrgID     uuid.UUID
	gotBody      map[string]any
}

func (f *fakeInternalAutomationDelegate) Create(w http.ResponseWriter, r *http.Request) {
	f.createCalled = true
	f.gotOrgID = middleware.OrgIDFromContext(r.Context())
	require.NoError(r.Context().Value(testingContextKey{}).(*testing.T), json.NewDecoder(r.Body).Decode(&f.gotBody), "forwarded create body should decode")
	writeJSON(w, http.StatusCreated, models.SingleResponse[map[string]any]{Data: map[string]any{"id": "automation-1", "name": f.gotBody["name"]}})
}

func (f *fakeInternalAutomationDelegate) Update(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]any]{Data: map[string]any{"id": chi.URLParam(r, "id")}})
}

func (f *fakeInternalAutomationDelegate) RunNow(w http.ResponseWriter, r *http.Request) {
	f.runCalled = true
	f.gotOrgID = middleware.OrgIDFromContext(r.Context())
	writeJSON(w, http.StatusAccepted, models.SingleResponse[map[string]any]{Data: map[string]any{"status": "queued"}})
}

func (f *fakeInternalAutomationDelegate) Pause(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]any]{Data: map[string]any{"enabled": false}})
}

func (f *fakeInternalAutomationDelegate) Resume(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]any]{Data: map[string]any{"enabled": true}})
}

type testingContextKey struct{}

type fakeInternalAutomationLookup struct {
	automation models.Automation
	err        error
}

func (f fakeInternalAutomationLookup) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Automation, error) {
	return f.automation, f.err
}

func newInternalAutomationToken(t *testing.T, orgID, repoID, sessionID uuid.UUID, origin models.SessionOrigin) string {
	t.Helper()
	token, err := auth.GenerateSessionThreadTokenWithClaims(internalAutomationSecret, orgID, repoID, sessionID, nil, []string{"automation:manage"}, string(origin), nil, time.Minute)
	require.NoError(t, err, "test token should be generated")
	return token
}

func newInternalAutomationRequest(t *testing.T, method, path, token string, body string, params map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), testingContextKey{}, t))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestInternalAutomationHandler_Create_ForwardsSameRepoAutomation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	delegate := &fakeInternalAutomationDelegate{}
	handler := NewInternalAutomationHandler(
		delegate,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalAutomationLookup{},
		internalAutomationSecret,
	)
	token := newInternalAutomationToken(t, orgID, repoID, sessionID, models.SessionOriginManual)
	req := newInternalAutomationRequest(t, http.MethodPost, "/api/v1/internal/automations", token,
		`{"name":"Nightly","goal":"Run cleanup","repository_id":"`+repoID.String()+`","schedule_type":"none","github_event_triggers":["pull_request_opened"]}`,
		nil,
	)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "same-repo automation create should be forwarded")
	require.True(t, delegate.createCalled, "delegate create should be called")
	require.Equal(t, orgID, delegate.gotOrgID, "handler should attach token org id to request context")
	require.Equal(t, "Nightly", delegate.gotBody["name"], "handler should forward the original payload")
}

func TestInternalAutomationHandler_RunRejectsCrossRepoAutomation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionRepoID := uuid.New()
	automationRepoID := uuid.New()
	sessionID := uuid.New()
	automationID := uuid.New()
	delegate := &fakeInternalAutomationDelegate{}
	handler := NewInternalAutomationHandler(
		delegate,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &sessionRepoID}},
		fakeInternalAutomationLookup{automation: models.Automation{ID: automationID, OrgID: orgID, RepositoryID: &automationRepoID}},
		internalAutomationSecret,
	)
	token := newInternalAutomationToken(t, orgID, sessionRepoID, sessionID, models.SessionOriginManual)
	req := newInternalAutomationRequest(t, http.MethodPost, "/api/v1/internal/automations/"+automationID.String()+"/run", token, `{}`, map[string]string{"id": automationID.String()})
	rr := httptest.NewRecorder()

	handler.RunNow(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "cross-repo automation run should be rejected")
	require.False(t, delegate.runCalled, "delegate run should not be called")
}

func TestInternalAutomationHandler_CreateRejectsPersonalIdentityScope(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	delegate := &fakeInternalAutomationDelegate{}
	handler := NewInternalAutomationHandler(
		delegate,
		fakeInternalSessionLookup{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}},
		fakeInternalAutomationLookup{},
		internalAutomationSecret,
	)
	token := newInternalAutomationToken(t, orgID, repoID, sessionID, models.SessionOriginManual)
	req := newInternalAutomationRequest(t, http.MethodPost, "/api/v1/internal/automations", token,
		`{"name":"Nightly","goal":"Run cleanup","repository_id":"`+repoID.String()+`","identity_scope":"personal","schedule_type":"none","github_event_triggers":["pull_request_opened"]}`,
		nil,
	)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "personal identity automation create should be rejected")
	require.False(t, delegate.createCalled, "delegate create should not be called")
}
