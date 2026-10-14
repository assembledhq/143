package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	automationservice "github.com/assembledhq/143/internal/services/automations"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func goalImprovementHandlerRows(items ...models.AutomationGoalImprovement) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "automation_id", "repository_id", "mode", "status", "input_name", "input_goal", "input_config", "base_goal_hash", "evidence_snapshot", "proposed_goal", "proposal", "confidence", "warnings", "error_message", "analysis_session_id", "created_by", "applied_by", "applied_at", "created_at", "updated_at"})
	for _, i := range items {
		rows.AddRow(i.ID, i.OrgID, i.AutomationID, i.RepositoryID, i.Mode, i.Status, i.InputName, i.InputGoal, i.InputConfig, i.BaseGoalHash, i.EvidenceSnapshot, i.ProposedGoal, i.Proposal, i.Confidence, i.Warnings, i.ErrorMessage, i.AnalysisSessionID, i.CreatedBy, i.AppliedBy, i.AppliedAt, i.CreatedAt, i.UpdatedAt)
	}
	return rows
}
func goalImprovementHandlerRequest(org, user uuid.UUID, automation, improvement, query, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/?"+query, strings.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), org)
	if user != uuid.Nil {
		ctx = middleware.WithUser(ctx, &models.User{ID: user, OrgID: uuid.New()})
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", automation)
	route.URLParams.Add("improvement_id", improvement)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
}

func TestAutomationGoalImprovementReadAndCancel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, endpoint, query, code string
		invalid, unavailable, empty bool
		status                      int
		proposalStatus              models.AutomationGoalImprovementStatus
		loadErr, cancelErr          error
		limit                       int
	}{
		{name: "read completed proposal", endpoint: "get", status: 200, proposalStatus: models.AutomationGoalImprovementStatusCompleted},
		{name: "read foreign proposal", endpoint: "get", status: 404, code: "NOT_FOUND", loadErr: pgx.ErrNoRows},
		{name: "read invalid ID", endpoint: "get", invalid: true, status: 400, code: "INVALID_ID"},
		{name: "read unavailable", endpoint: "get", unavailable: true, status: 503, code: "GOAL_IMPROVEMENT_UNAVAILABLE"},
		{name: "list default limit", endpoint: "list", status: 200, limit: 10, proposalStatus: models.AutomationGoalImprovementStatusRunning},
		{name: "list clamped limit", endpoint: "list", query: "limit=100", status: 200, limit: 25, proposalStatus: models.AutomationGoalImprovementStatusCompleted},
		{name: "list empty", endpoint: "list", status: 200, limit: 10, empty: true},
		{name: "list read failure", endpoint: "list", status: 500, limit: 10, code: "LIST_FAILED", loadErr: errors.New("private DB failure")},
		{name: "list bad ID", endpoint: "list", invalid: true, status: 400, code: "INVALID_ID"},
		{name: "list malformed limit", endpoint: "list", query: "limit=no", status: 400, code: "INVALID_LIMIT"},
		{name: "list nonpositive limit", endpoint: "list", query: "limit=0", status: 400, code: "INVALID_LIMIT"},
		{name: "list unavailable", endpoint: "list", unavailable: true, status: 503, code: "GOAL_IMPROVEMENT_UNAVAILABLE"},
		{name: "cancel pending", endpoint: "cancel", status: 200, proposalStatus: models.AutomationGoalImprovementStatusPending},
		{name: "cancel running", endpoint: "cancel", status: 200, proposalStatus: models.AutomationGoalImprovementStatusRunning},
		{name: "cancel completed is conflict", endpoint: "cancel", status: 409, code: "IMPROVEMENT_NOT_CANCELABLE", proposalStatus: models.AutomationGoalImprovementStatusCompleted},
		{name: "cancel missing proposal", endpoint: "cancel", status: 404, code: "NOT_FOUND", loadErr: pgx.ErrNoRows},
		{name: "cancel failure", endpoint: "cancel", status: 500, code: "CANCEL_FAILED", proposalStatus: models.AutomationGoalImprovementStatusPending, cancelErr: errors.New("private DB failure")},
		{name: "cancel malformed ID", endpoint: "cancel", invalid: true, status: 400, code: "INVALID_ID"},
		{name: "cancel unavailable", endpoint: "cancel", unavailable: true, status: 503, code: "GOAL_IMPROVEMENT_UNAVAILABLE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, user, automation, id := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			proposal := models.AutomationGoalImprovement{ID: id, OrgID: org, AutomationID: &automation, Mode: models.AutomationGoalImprovementModeFast, Status: tt.proposalStatus, InputGoal: "Run tests", BaseGoalHash: automationservice.GoalHash("Run tests"), CreatedAt: now, UpdatedAt: now}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated improvement fixture")
			defer mock.Close()
			if !tt.invalid && !tt.unavailable && tt.code != "INVALID_LIMIT" {
				q := `SELECT .* FROM automation_goal_improvements WHERE id = @id AND org_id = @org_id`
				args := pgx.NamedArgs{"id": id, "org_id": org}
				rows := goalImprovementHandlerRows(proposal)
				if tt.endpoint == "list" {
					q = `SELECT .* FROM automation_goal_improvements WHERE org_id = @org_id AND automation_id = @automation_id ORDER BY created_at DESC LIMIT @limit`
					args = pgx.NamedArgs{"org_id": org, "automation_id": automation, "limit": tt.limit}
					if tt.empty {
						rows = goalImprovementHandlerRows()
					}
				}
				expect := mock.ExpectQuery(q).WithArgs(args)
				if tt.loadErr != nil {
					expect.WillReturnError(tt.loadErr)
				} else {
					expect.WillReturnRows(rows)
				}
				if tt.endpoint == "cancel" && tt.loadErr == nil && tt.proposalStatus != models.AutomationGoalImprovementStatusCompleted {
					mock.ExpectQuery(q).WithArgs(args).WillReturnRows(goalImprovementHandlerRows(proposal))
					cancel := mock.ExpectExec(`UPDATE automation_goal_improvements SET status = @status, error_message = @error_message, updated_at = now\(\) WHERE id = @id AND org_id = @org_id AND status IN \('pending', 'running'\)`).WithArgs(pgx.NamedArgs{"status": models.AutomationGoalImprovementStatusCanceled, "error_message": "proposal was canceled by the user", "id": id, "org_id": org})
					if tt.cancelErr != nil {
						cancel.WillReturnError(tt.cancelErr)
					} else {
						cancel.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					}
				}
			}
			h := NewAutomationHandler(nil, nil)
			if !tt.unavailable {
				h.SetGoalImprovementService(automationservice.NewGoalImprovementService(db.NewAutomationGoalImprovementStore(mock), nil, nil, nil, nil, nil, nil))
			}
			automationPath, improvementPath := automation.String(), id.String()
			if tt.invalid {
				automationPath = "bad"
				improvementPath = "bad"
			}
			req := goalImprovementHandlerRequest(org, user, automationPath, improvementPath, tt.query, "")
			rr := httptest.NewRecorder()
			switch tt.endpoint {
			case "get":
				h.GetGoalImprovement(rr, req)
			case "list":
				h.ListGoalImprovements(rr, req)
			case "cancel":
				h.CancelGoalImprovement(rr, req)
			}
			require.Equal(t, tt.status, rr.Code, "improvement endpoint should preserve validation, conflict, and dependency outcomes")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else if tt.endpoint == "list" {
				expected := []models.AutomationGoalImprovement{proposal}
				if tt.empty {
					expected = []models.AutomationGoalImprovement{}
				}
				var response models.ListResponse[models.AutomationGoalImprovement]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "proposal list should decode")
				require.Equal(t, expected, response.Data, "list should return exact tenant-scoped proposals")
			} else {
				if tt.endpoint == "cancel" {
					proposal.Status = models.AutomationGoalImprovementStatusCanceled
					message := "proposal was canceled by the user"
					proposal.ErrorMessage = &message
				}
				var response models.SingleResponse[models.AutomationGoalImprovement]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "proposal envelope should decode")
				require.Equal(t, proposal, response.Data, "proposal response should match durable state and cancellation reason")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all proposal reads and writes must use authenticated tenant")
		})
	}
}

func TestAutomationGoalImprovementErrorResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, code string
		cause      error
		apply      bool
		status     int
	}{
		{name: "LLM unavailable", cause: automationservice.ErrGoalImprovementLLMUnavailable, status: 503, code: "LLM_NOT_CONFIGURED"},
		{name: "duplicate deep analysis", cause: automationservice.ErrGoalImprovementAlreadyRunning, status: 409, code: "DEEP_IMPROVEMENT_RUNNING"},
		{name: "missing goal", cause: automationservice.ErrGoalRequired, status: 400, code: "MISSING_FIELD"},
		{name: "repository required", cause: automationservice.ErrGoalRequiresRepository, status: 400, code: "INVALID_GOAL_IMPROVEMENT_REQUEST"},
		{name: "unexpected provider failure", cause: errors.New("private DB failure"), status: 500, code: "GOAL_IMPROVEMENT_FAILED"},
		{name: "not completed", apply: true, cause: automationservice.ErrGoalImprovementNotCompleted, status: 409, code: "IMPROVEMENT_NOT_COMPLETED"},
		{name: "stale goal", apply: true, cause: automationservice.ErrGoalImprovementStaleGoal, status: 409, code: "STALE_GOAL"},
		{name: "missing proposed goal", apply: true, cause: automationservice.ErrGoalImprovementProposedGoalRequired, status: 400, code: "MISSING_FIELD"},
		{name: "unexpected apply failure", apply: true, cause: errors.New("private DB failure"), status: 500, code: "APPLY_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := NewAutomationHandler(nil, nil)
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.apply {
				h.writeApplyGoalImprovementError(rr, req, tt.cause)
			} else {
				h.writeGoalImprovementError(rr, req, tt.cause)
			}
			require.Equal(t, tt.status, rr.Code, "improvement errors should have actionable status codes")
			requireAPIClientError(t, rr, tt.code)
		})
	}
}
