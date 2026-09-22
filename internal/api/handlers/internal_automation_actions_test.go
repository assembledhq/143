package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/assembledhq/143/internal/api/middleware"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/automationactions"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type testAutomationActionService struct {
	calls   int
	actor   models.AutomationActionActor
	request *models.AutomationActionRequest
	result  models.AutomationActionResult
	err     error
}

func (s *testAutomationActionService) Execute(_ context.Context, a models.AutomationActionActor, _ models.AutomationActionKey, r *models.AutomationActionRequest) (models.AutomationActionResult, error) {
	s.calls++
	s.actor, s.request = a, r
	return s.result, s.err
}
func (s *testAutomationActionService) Status(_ context.Context, a models.AutomationActionActor, _ string) (models.AutomationActionResult, error) {
	s.calls++
	s.actor = a
	return s.result, s.err
}
func TestInternalAutomationActionAuthorizationAndBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mode, token, body string
		want                    int
		calls                   int
	}{
		{"request", "request", "scoped", `{"operation_key":"daily","action_key":"notify","kind":"slack_notification","text":"Report"}`, 200, 1},
		{"resume", "resume", "scoped", `{}`, 200, 1}, {"status", "status", "scoped", "", 200, 1},
		{"no token", "request", "", `{}`, 401, 0}, {"old token", "request", "old", `{}`, 403, 0}, {"wrong scope", "request", "status", `{}`, 403, 0},
		{"destination override", "request", "scoped", `{"slack_channel_id":"C0123456789"}`, 400, 0}, {"resume content", "resume", "scoped", `{"head_sha":"a"}`, 400, 0}, {"trailing json", "request", "scoped", `{} {}`, 400, 0}, {"oversized", "request", "scoped", `{"reasoning":"` + strings.Repeat("a", 65<<10) + `"}`, 400, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actor := models.AutomationActionActor{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), RunID: uuid.New(), AttemptToken: uuid.New(), JobID: uuid.New()}
			const secret = "review-actions-test-secret"
			scopes := []string{models.ToolScope("automation:execute-action"), models.ToolScope("automation:action-status")}
			if tt.token == "status" {
				scopes = scopes[1:]
			}
			var token string
			var err error
			if tt.token == "old" {
				token, err = auth.GenerateSessionToken(secret, actor.OrgID, actor.RepositoryID, actor.SessionID, time.Minute)
			} else if tt.token != "" {
				token, err = auth.GenerateAutomationActionToken(secret, actor, scopes, "automation", time.Minute)
			}
			require.NoError(t, err, "mint test token")
			svc := &testAutomationActionService{result: models.AutomationActionResult{Status: models.AutomationActionNotStarted, Actions: []models.AutomationAction{}}}
			handler := NewInternalAutomationActionHandler(fakeInternalSessionLookup{session: models.Session{RepositoryID: &actor.RepositoryID}}, svc, secret)
			req := httptest.NewRequest("POST", "/api/v1/internal/automation/actions", strings.NewReader(tt.body))
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rr := httptest.NewRecorder()
			switch tt.mode {
			case "request":
				handler.Request(rr, req)
			case "resume":
				handler.Resume(rr, req)
			case "status":
				handler.Status(rr, req)
			}
			require.Equal(t, tt.want, rr.Code, "enforce token scope and strict body before service")
			require.Equal(t, tt.calls, svc.calls, "invalid calls cannot reach provider service")
			if tt.calls > 0 {
				require.Equal(t, actor, svc.actor, "identity must come entirely from signed token")
				if tt.mode == "resume" {
					require.Nil(t, svc.request, "resume must use stored content")
				}
			}
		})
	}
}
func TestInternalAutomationActionFailureReceipts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		want    int
		code    string
		partial bool
	}{
		{"revoked", db.ErrAutomationActionUnauthorized, 403, "CAPABILITY_REQUIRED", false},
		{"step limit", db.ErrAutomationActionLimit, 400, "INVALID_ACTION_REQUEST", false},
		{"budget", automationactions.ErrBudgetExhausted, 503, "BUDGET_EXHAUSTED", false},
		{"pending budget", automationactions.ErrBudgetExhausted, 200, "BUDGET_EXHAUSTED", true}, {"stale", automationactions.ErrStaleHead, 409, "STALE_HEAD", false}, {"config", automationactions.ErrIntegrationNotReady, 424, "INTEGRATION_NOT_READY", false}, {"partial then stale", automationactions.ErrStaleHead, 200, "STALE_HEAD", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := models.AutomationActionResult{}
			if tt.partial {
				result = models.AutomationActionResultFor([]models.AutomationAction{{Kind: models.AutomationActionLabel, Status: models.AutomationActionSucceeded}}, time.Now())
			}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/", nil)
			(&InternalAutomationActionHandler{}).respond(rr, req, result, tt.err)
			require.Equal(t, tt.want, rr.Code, "surface admission error or completed receipts")
			var body map[string]any
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "response must be JSON")
			if tt.partial {
				data := body["data"].(map[string]any)
				require.Equal(t, tt.code, data["error_code"], "report why further sends stopped")
				require.Equal(t, "delivered", data["status"], "preserve completed action evidence")
			} else {
				require.Equal(t, tt.code, body["error"].(map[string]any)["code"], "map admission failure")
			}
		})
	}
}

func TestAutomationActionGrantRequiresAdmin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role          string
		enabled, want bool
	}{{"admin", true, true}, {"member", true, false}, {"", true, false}, {"member", false, true}}
	for _, tt := range tests {
		t.Run(tt.role+fmt.Sprint(tt.enabled), func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("PATCH", "/", nil)
			req = req.WithContext(middleware.WithActiveRole(req.Context(), tt.role))
			rr := httptest.NewRecorder()
			got := authorizeAutomationActionGrants(rr, req, []models.AgentCapabilityPolicyGrantInput{{CapabilityID: models.AgentCapabilityAutomationActions, Enabled: tt.enabled}})
			require.Equal(t, tt.want, got, "only admins can opt into writes, while members can revoke")
		})
	}
}

func TestAutomationActionsCannotBecomeSessionDefaults(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("PATCH", "/api/v1/agent-capabilities/session-default", strings.NewReader(`{"capabilities":[{"capability_id":"automation_actions","enabled":true,"access_level":"write"}]}`))
	rr := httptest.NewRecorder()
	(&AgentCapabilitiesHandler{}).PatchSessionDefault(rr, req)
	require.Equal(t, 400, rr.Code, "reject automation-only authority before touching session-default storage")
	require.Contains(t, rr.Body.String(), "AUTOMATION_ONLY", "explain where the capability must be configured")
}
