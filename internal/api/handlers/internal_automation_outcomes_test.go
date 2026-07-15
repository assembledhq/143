package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	automationservice "github.com/assembledhq/143/internal/services/automations"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakeAutomationOutcomeReporter struct {
	req automationservice.ReportOutcomeRequest
	err error
}

func (f *fakeAutomationOutcomeReporter) Report(_ context.Context, _ uuid.UUID, req automationservice.ReportOutcomeRequest) (models.AutomationRunOutcome, error) {
	f.req = req
	if f.err != nil {
		return models.AutomationRunOutcome{}, f.err
	}
	return models.AutomationRunOutcome{
		ID: uuid.New(), AutomationRunID: req.RunID, Decision: req.Decision,
	}, nil
}

func TestInternalAutomationOutcomeHandlerReport(t *testing.T) {
	t.Parallel()

	const secret = "automation-outcome-test-secret"
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	runID := uuid.New()
	threadID := uuid.New()
	baseSession := models.Session{
		ID: sessionID, OrgID: orgID, Origin: models.SessionOriginAutomation,
		RepositoryID: &repoID, AutomationRunID: &runID,
	}

	tests := []struct {
		name         string
		origin       models.SessionOrigin
		scopes       []string
		body         string
		serviceErr   error
		expectedCode int
	}{
		{
			name: "records a scoped outcome", origin: models.SessionOriginAutomation,
			scopes:       []string{"automation-run:report-outcome"},
			body:         `{"decision":"passed","reason":"No blocking issues.","head_sha":"abc123"}`,
			expectedCode: http.StatusOK,
		},
		{
			name: "rejects missing scope", origin: models.SessionOriginAutomation,
			scopes:       []string{"preview:read"},
			body:         `{"decision":"passed","reason":"No blocking issues."}`,
			expectedCode: http.StatusForbidden,
		},
		{
			name: "rejects wrong origin", origin: models.SessionOriginManual,
			scopes:       []string{"automation-run:report-outcome"},
			body:         `{"decision":"passed","reason":"No blocking issues."}`,
			expectedCode: http.StatusForbidden,
		},
		{
			name: "rejects invalid decision", origin: models.SessionOriginAutomation,
			scopes:       []string{"automation-run:report-outcome"},
			body:         `{"decision":"completed","reason":"Done."}`,
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "maps conflicting report", origin: models.SessionOriginAutomation,
			scopes:       []string{"automation-run:report-outcome"},
			body:         `{"decision":"passed","reason":"Done."}`,
			serviceErr:   automationservice.ErrOutcomeAlreadyReported,
			expectedCode: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token, err := auth.GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, &threadID, tt.scopes, string(tt.origin), nil, time.Minute)
			require.NoError(t, err, "test token should be generated")
			service := &fakeAutomationOutcomeReporter{err: tt.serviceErr}
			handler := NewInternalAutomationOutcomeHandler(
				service,
				mockInternalEvalSessionStore{session: baseSession},
				secret,
				zerolog.Nop(),
			)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/automation-runs/current/outcome", bytes.NewBufferString(tt.body))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			handler.Report(rr, req)
			require.Equal(t, tt.expectedCode, rr.Code, "handler should return the expected status")
			if tt.expectedCode != http.StatusOK {
				return
			}
			require.Equal(t, runID, service.req.RunID, "handler should derive the run from the authorized session")
			require.Equal(t, sessionID, service.req.SessionID, "handler should derive the reporting session from the token")
			require.Equal(t, models.AutomationOutcomeDecisionPassed, service.req.Decision, "handler should forward the typed decision")
			var response models.SingleResponse[map[string]string]
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "success response should be valid JSON")
			require.Equal(t, "recorded", response.Data["status"], "success response should confirm persistence")
		})
	}
}
