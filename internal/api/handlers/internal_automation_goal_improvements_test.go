package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	automationservice "github.com/assembledhq/143/internal/services/automations"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakeAutomationGoalImprovementCompleter struct {
	gotReq automationservice.CompleteDeepGoalImprovementRequest
	err    error
}

func (f *fakeAutomationGoalImprovementCompleter) CompleteDeepFromAgent(_ context.Context, _ uuid.UUID, req automationservice.CompleteDeepGoalImprovementRequest) (models.AutomationGoalImprovement, error) {
	f.gotReq = req
	if f.err != nil {
		return models.AutomationGoalImprovement{}, f.err
	}
	return models.AutomationGoalImprovement{
		ID:     req.ImprovementID,
		Status: models.AutomationGoalImprovementStatusCompleted,
	}, nil
}

func newGoalImprovementHandler(t *testing.T, service automationGoalImprovementCompleter, secret string, orgID, repoID, sessionID uuid.UUID) *InternalAutomationGoalImprovementHandler {
	t.Helper()
	return NewInternalAutomationGoalImprovementHandler(
		service,
		mockInternalEvalSessionStore{session: models.Session{
			ID:           sessionID,
			OrgID:        orgID,
			Origin:       models.SessionOriginAutomationGoalImprovement,
			RepositoryID: &repoID,
		}},
		secret,
		zerolog.Nop(),
	)
}

func completeGoalImprovementRequest(t *testing.T, handler *InternalAutomationGoalImprovementHandler, token, improvementID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/automation-goal-improvements/"+improvementID+"/complete", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("improvement_id", improvementID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	handler.Complete(rr, req)
	return rr
}

func TestInternalAutomationGoalImprovementHandler_Complete(t *testing.T) {
	t.Parallel()

	const secret = "internal-automation-goal-improvement-test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	improvementID := uuid.New()

	validToken, err := auth.GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, nil, []string{"automation-goal-improvement:complete"}, string(models.SessionOriginAutomationGoalImprovement), nil, time.Minute)
	require.NoError(t, err)

	validBody := []byte(`{"improvement_id":"` + improvementID.String() + `","proposed_goal":"Run tests with clear evidence","rationale":"clearer","changes":["added evidence"],"evidence":["repository tests"],"risks":[],"confidence":"high"}`)

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()
		service := &fakeAutomationGoalImprovementCompleter{}
		handler := newGoalImprovementHandler(t, service, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, validToken, improvementID.String(), validBody)

		require.Equal(t, http.StatusOK, rr.Code, "Complete should accept a scoped token")
		require.Equal(t, improvementID, service.gotReq.ImprovementID, "Complete should forward the path improvement ID")
		require.Equal(t, sessionID, service.gotReq.SessionID, "Complete should forward the token session ID")
		require.Equal(t, "Run tests with clear evidence", service.gotReq.ProposedGoal)
		var response models.SingleResponse[map[string]string]
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response))
		require.Equal(t, "completed", response.Data["status"])
	})

	t.Run("missing token returns 401", func(t *testing.T) {
		t.Parallel()
		handler := newGoalImprovementHandler(t, &fakeAutomationGoalImprovementCompleter{}, secret, orgID, repoID, sessionID)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(validBody))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("improvement_id", improvementID.String())
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		handler.Complete(rr, req)
		require.Equal(t, http.StatusUnauthorized, rr.Code, "missing token should be rejected")
	})

	t.Run("wrong scope returns 403", func(t *testing.T) {
		t.Parallel()
		wrongScopeToken, err := auth.GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, nil, []string{"issue:create"}, string(models.SessionOriginAutomationGoalImprovement), nil, time.Minute)
		require.NoError(t, err)
		handler := newGoalImprovementHandler(t, &fakeAutomationGoalImprovementCompleter{}, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, wrongScopeToken, improvementID.String(), validBody)
		require.Equal(t, http.StatusForbidden, rr.Code, "token with wrong scope should be rejected")
	})

	t.Run("wrong origin returns 403", func(t *testing.T) {
		t.Parallel()
		wrongOriginToken, err := auth.GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, nil, []string{"automation-goal-improvement:complete"}, string(models.SessionOriginManual), nil, time.Minute)
		require.NoError(t, err)
		handler := newGoalImprovementHandler(t, &fakeAutomationGoalImprovementCompleter{}, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, wrongOriginToken, improvementID.String(), validBody)
		require.Equal(t, http.StatusForbidden, rr.Code, "token with wrong session origin should be rejected")
	})

	t.Run("body improvement_id mismatch returns 403", func(t *testing.T) {
		t.Parallel()
		otherID := uuid.New()
		mismatchBody := []byte(`{"improvement_id":"` + otherID.String() + `","proposed_goal":"Run tests","rationale":"ok","confidence":"high"}`)
		handler := newGoalImprovementHandler(t, &fakeAutomationGoalImprovementCompleter{}, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, validToken, improvementID.String(), mismatchBody)
		require.Equal(t, http.StatusForbidden, rr.Code, "improvement_id body mismatch with path should be rejected")
	})

	t.Run("invalid path UUID returns 400", func(t *testing.T) {
		t.Parallel()
		handler := newGoalImprovementHandler(t, &fakeAutomationGoalImprovementCompleter{}, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, validToken, "not-a-uuid", validBody)
		require.Equal(t, http.StatusBadRequest, rr.Code, "invalid path UUID should return 400")
	})

	t.Run("service ErrGoalImprovementNotRunning returns 409", func(t *testing.T) {
		t.Parallel()
		service := &fakeAutomationGoalImprovementCompleter{err: automationservice.ErrGoalImprovementNotRunning}
		handler := newGoalImprovementHandler(t, service, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, validToken, improvementID.String(), validBody)
		require.Equal(t, http.StatusConflict, rr.Code, "not-running error should return 409 not 400")
	})

	t.Run("service ErrGoalImprovementSessionMismatch returns 403", func(t *testing.T) {
		t.Parallel()
		service := &fakeAutomationGoalImprovementCompleter{err: automationservice.ErrGoalImprovementSessionMismatch}
		handler := newGoalImprovementHandler(t, service, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, validToken, improvementID.String(), validBody)
		require.Equal(t, http.StatusForbidden, rr.Code, "session mismatch should return 403 not 400")
	})

	t.Run("service ErrGoalImprovementProposalRejected returns 422", func(t *testing.T) {
		t.Parallel()
		service := &fakeAutomationGoalImprovementCompleter{err: fmt.Errorf("%w: goal not substantively improved", automationservice.ErrGoalImprovementProposalRejected)}
		handler := newGoalImprovementHandler(t, service, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, validToken, improvementID.String(), validBody)
		require.Equal(t, http.StatusUnprocessableEntity, rr.Code, "proposal rejection should return 422 not 400")
	})

	t.Run("unclassified service error returns 500", func(t *testing.T) {
		t.Parallel()
		service := &fakeAutomationGoalImprovementCompleter{err: errors.New("db connection failed")}
		handler := newGoalImprovementHandler(t, service, secret, orgID, repoID, sessionID)
		rr := completeGoalImprovementRequest(t, handler, validToken, improvementID.String(), validBody)
		require.Equal(t, http.StatusInternalServerError, rr.Code, "unexpected service error should return 500 not 400")
	})
}
