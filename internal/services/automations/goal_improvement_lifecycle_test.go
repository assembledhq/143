package automations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type goalLifecycleLLM struct {
	response     string
	err          error
	system, user string
	calls        int
}

func (f *goalLifecycleLLM) Complete(_ context.Context, system, user string) (string, error) {
	f.system, f.user = system, user
	f.calls++
	return f.response, f.err
}
func goalLifecycleRows(v models.AutomationGoalImprovement) *pgxmock.Rows {
	return pgxmock.NewRows(goalImprovementRowsForServiceTest()).AddRow(v.ID, v.OrgID, v.AutomationID, v.RepositoryID, v.Mode, v.Status, v.InputName, v.InputGoal, v.InputConfig, v.BaseGoalHash, v.EvidenceSnapshot, v.ProposedGoal, v.Proposal, v.Confidence, v.Warnings, v.ErrorMessage, v.AnalysisSessionID, v.CreatedBy, v.AppliedBy, v.AppliedAt, v.CreatedAt, v.UpdatedAt)
}

func TestGoalImprovementFastLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stage, response, wantError string
		saved                            bool
	}{
		{name: "draft completed"}, {name: "saved completed", saved: true},
		{name: "invalid mode", stage: "mode", wantError: "invalid"},
		{name: "expiration failure", stage: "expire", wantError: "expire draft"},
		{name: "blank goal", stage: "goal", wantError: "goal is required"},
		{name: "LLM unavailable", stage: "missing_llm", wantError: "not configured"},
		{name: "malformed input config", stage: "config", wantError: "encode improvement input"},
		{name: "LLM timeout", stage: "llm", wantError: "improve automation goal"},
		{name: "invalid response", response: "not json", wantError: "parse automation goal"},
		{name: "missing goal", response: `{"rationale":"why"}`, wantError: "missing proposed_goal"},
		{name: "too long goal", stage: "length", wantError: "exceeds 12000"},
		{name: "missing rationale", response: `{"proposed_goal":"next"}`, wantError: "missing rationale"},
		{name: "insert failure", stage: "insert", wantError: "create automation goal"},
		{name: "saved load failure", stage: "load", saved: true, wantError: "load automation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated database mock")
			defer mock.Close()
			org, repo, user, automation, id := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			inputGoal := "Run tests"
			mode := models.AutomationGoalImprovementModeFast
			config := json.RawMessage(`{}`)
			if tt.stage == "mode" {
				mode = "unknown"
			}
			if tt.stage == "goal" {
				inputGoal = ""
			}
			if tt.stage == "config" {
				config = json.RawMessage(`{`)
			}
			response := tt.response
			if response == "" {
				response = `{"proposed_goal":"  Run tests and report failures  ","rationale":"Include verification","changes":["report"],"evidence":["goal"],"risks":[],"warnings":["Check test credentials"]}`
			}
			if tt.stage == "length" {
				b, e := json.Marshal(map[string]string{"proposed_goal": strings.Repeat("x", 12001), "rationale": "why"})
				require.NoError(t, e, "encode too-long response")
				response = string(b)
			}
			llm := &goalLifecycleLLM{response: response}
			if tt.stage == "llm" {
				llm.err = errors.New("provider timeout")
			}
			svc := NewGoalImprovementService(db.NewAutomationGoalImprovementStore(mock), db.NewAutomationStore(mock), nil, nil, nil, nil, llm)
			if tt.stage == "missing_llm" {
				svc.llm = nil
			}
			name := "Nightly"
			var automationPtr *uuid.UUID
			evidence := json.RawMessage(`{}`)
			if tt.saved {
				q := mock.ExpectQuery(`SELECT .+ FROM automations\s+WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`).WithArgs(automation, org)
				if tt.stage == "load" {
					q.WillReturnError(errors.New("lookup failed"))
				} else {
					a := models.Automation{ID: automation, OrgID: org, RepositoryID: &repo, Name: name, Goal: inputGoal, BaseBranch: "main", ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, GitHubEventFilters: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
					q.WillReturnRows(addAutomationRowForGoalImprovementServiceTest(pgxmock.NewRows(automationColumnsForGoalImprovementServiceTest()), a))
					config = savedAutomationInputConfig(a)
					automationPtr = &automation
					evidence = json.RawMessage(`{"warnings":["run history unavailable"]}`)
				}
			} else if tt.stage != "mode" {
				expiry := mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET status = @status, error_message = @error_message, updated_at = now\(\)\s+WHERE org_id = @org_id\s+AND automation_id IS NULL`).WithArgs(pgx.NamedArgs{"status": models.AutomationGoalImprovementStatusCanceled, "error_message": "draft proposal expired", "org_id": org, "before": pgxmock.AnyArg()})
				if tt.stage == "expire" {
					expiry.WillReturnError(errors.New("expired write failed"))
				} else {
					expiry.WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				}
			}
			expected := models.AutomationGoalImprovement{ID: id, OrgID: org, AutomationID: automationPtr, RepositoryID: &repo, Mode: mode, Status: models.AutomationGoalImprovementStatusCompleted, InputName: &name, InputGoal: inputGoal, InputConfig: config, BaseGoalHash: GoalHash(inputGoal), EvidenceSnapshot: evidence, ProposedGoal: stringPtrForGoalImprovementTest("Run tests and report failures"), Proposal: json.RawMessage(`{"rationale":"Include verification","changes":["report"],"evidence":["goal"],"risks":[]}`), Confidence: stringPtrForGoalImprovementTest("medium"), Warnings: json.RawMessage(`["Check test credentials"]`), CreatedBy: &user, CreatedAt: now, UpdatedAt: now}
			if tt.wantError == "" || tt.stage == "insert" {
				q := mock.ExpectQuery(`INSERT INTO automation_goal_improvements`).WithArgs(pgx.NamedArgs{"org_id": org, "automation_id": automationPtr, "repository_id": &repo, "mode": mode, "status": expected.Status, "input_name": &name, "input_goal": inputGoal, "input_config": config, "base_goal_hash": expected.BaseGoalHash, "evidence_snapshot": evidence, "proposed_goal": expected.ProposedGoal, "proposal": expected.Proposal, "confidence": expected.Confidence, "warnings": expected.Warnings, "error_message": expected.ErrorMessage, "analysis_session_id": expected.AnalysisSessionID, "created_by": &user})
				if tt.stage == "insert" {
					q.WillReturnError(errors.New("insert failed"))
				} else {
					q.WillReturnRows(goalLifecycleRows(expected))
				}
			}
			var got models.AutomationGoalImprovement
			if tt.saved {
				got, err = svc.ImproveSaved(context.Background(), org, SavedGoalImprovementRequest{Mode: mode, AutomationID: automation, CreatedBy: &user})
			} else {
				got, err = svc.ImproveDraft(context.Background(), org, DraftGoalImprovementRequest{Mode: mode, Name: " Nightly ", Goal: " " + inputGoal + " ", RepositoryID: &repo, Config: config, CreatedBy: &user})
			}
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError, "invalid or failed improvement must not appear completed")
			} else {
				require.NoError(t, err, "valid improvement should be persisted")
				require.Equal(t, expected, got, "proposal must retain exact org, evidence, actor and normalized goal")
				require.Equal(t, prompts.AutomationGoalFastImprovementPrompt(prompts.AutomationGoalFastImprovementPromptData{MaxGoalChars: 12000}), llm.system, "LLM must receive shared constrained prompt")
				var input fastGoalImprovementInput
				require.NoError(t, json.Unmarshal([]byte(llm.user), &input), "decode model input")
				require.Equal(t, fastGoalImprovementInput{Name: name, Goal: inputGoal, RepositoryID: repo.String(), Config: config, Evidence: evidence}, input, "model must receive exact saved or draft context")
			}
			if tt.stage == "mode" || tt.stage == "goal" || tt.stage == "expire" || tt.stage == "load" || tt.stage == "config" || tt.stage == "missing_llm" {
				require.Zero(t, llm.calls, "invalid state must not call the model")
			} else {
				require.Equal(t, 1, llm.calls, "eligible proposal should require exactly one completion")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all reads and writes must be org-scoped and failure must stop persistence")
		})
	}
}

func TestGoalImprovementDeepCompletionFences(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, stage, wantError string }{
		{name: "accepted proposal"}, {name: "reject unsafe proposal", stage: "reject", wantError: "judge rejected"},
		{name: "not deep", stage: "mode", wantError: "not a deep"},
		{name: "already completed", stage: "status", wantError: "not running"},
		{name: "wrong analysis session", stage: "session", wantError: "not attached"},
		{name: "no proposed goal", stage: "goal", wantError: "proposed_goal is required"},
		{name: "too long goal", stage: "length", wantError: "exceeds 12000"},
		{name: "no rationale", stage: "rationale", wantError: "rationale is required"},
		{name: "judge unavailable", stage: "no_llm", wantError: "not configured"},
		{name: "judge timeout", stage: "llm", wantError: "judge automation goal"},
		{name: "judge malformed response", stage: "parse", wantError: "parse automation goal proposal judge"},
		{name: "completion write failure", stage: "write", wantError: "complete automation goal"},
		{name: "rejection write failure", stage: "reject_write", wantError: "fail automation goal"},
		{name: "proposal missing", stage: "load", wantError: "load automation goal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated proposal store")
			defer mock.Close()
			org, id, session := uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			proposal := models.AutomationGoalImprovement{ID: id, OrgID: org, Mode: models.AutomationGoalImprovementModeDeep, Status: models.AutomationGoalImprovementStatusRunning, InputGoal: "Run tests", InputConfig: json.RawMessage(`{}`), BaseGoalHash: GoalHash("Run tests"), EvidenceSnapshot: json.RawMessage(`{}`), Proposal: json.RawMessage(`{}`), Warnings: json.RawMessage(`[]`), AnalysisSessionID: &session, CreatedAt: now, UpdatedAt: now}
			req := CompleteDeepGoalImprovementRequest{ImprovementID: id, SessionID: session, ProposedGoal: "  Run tests and report failures ", Rationale: " Verify outcome ", Changes: []string{"report"}, Evidence: []string{"test history"}, Risks: []string{}, Warnings: []string{"agent warning"}}
			switch tt.stage {
			case "mode":
				proposal.Mode = models.AutomationGoalImprovementModeFast
			case "status":
				proposal.Status = models.AutomationGoalImprovementStatusCompleted
			case "session":
				req.SessionID = uuid.New()
			case "goal":
				req.ProposedGoal = " "
			case "length":
				req.ProposedGoal = strings.Repeat("x", 12001)
			case "rationale":
				req.Rationale = " "
			}
			q := mock.ExpectQuery(`SELECT .+ FROM automation_goal_improvements\s+WHERE id = @id AND org_id = @org_id`).WithArgs(id, org)
			if tt.stage == "load" {
				q.WillReturnError(errors.New("lookup failed"))
			} else {
				q.WillReturnRows(goalLifecycleRows(proposal))
			}
			llm := &goalLifecycleLLM{response: "```json\n{\"accepted\":true,\"warnings\":[\"judge warning\"]}\n```"}
			if tt.stage == "llm" {
				llm.err = errors.New("provider timeout")
			}
			if tt.stage == "parse" {
				llm.response = "invalid"
			}
			svc := NewGoalImprovementService(db.NewAutomationGoalImprovementStore(mock), nil, nil, nil, nil, nil, llm)
			if tt.stage == "no_llm" {
				svc.llm = nil
			}
			expected := proposal
			expected.Status = models.AutomationGoalImprovementStatusCompleted
			expected.ProposedGoal = stringPtrForGoalImprovementTest("Run tests and report failures")
			expected.Proposal = json.RawMessage(`{"rationale":"Verify outcome","changes":["report"],"evidence":["test history"],"risks":[]}`)
			expected.Confidence = stringPtrForGoalImprovementTest("medium")
			expected.Warnings = json.RawMessage(`["agent warning","judge warning"]`)
			if tt.stage == "reject" || tt.stage == "reject_write" {
				llm.response = `{"accepted":false,"reason":" "}`
				e := mock.ExpectExec(`UPDATE automation_goal_improvements\s+SET status = @status, error_message = @error_message, updated_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status IN`).WithArgs(pgx.NamedArgs{"status": models.AutomationGoalImprovementStatusFailed, "error_message": "proposal judge rejected proposal", "id": id, "org_id": org})
				if tt.stage == "reject_write" {
					e.WillReturnError(errors.New("write failed"))
				} else {
					e.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				}
			} else if tt.wantError == "" || tt.stage == "write" {
				q := mock.ExpectQuery(`UPDATE automation_goal_improvements`).WithArgs(pgx.NamedArgs{"status": expected.Status, "proposed_goal": *expected.ProposedGoal, "proposal": expected.Proposal, "confidence": expected.Confidence, "warnings": expected.Warnings, "id": id, "org_id": org})
				if tt.stage == "write" {
					q.WillReturnError(errors.New("write failed"))
				} else {
					q.WillReturnRows(goalLifecycleRows(expected))
				}
			}
			got, err := svc.CompleteDeepFromAgent(context.Background(), org, req)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError, "invalid analysis must not complete the proposal")
			} else {
				require.NoError(t, err, "accepted analysis should complete")
				require.Equal(t, expected, got, "completion must combine warnings and normalize proposal exactly")
				require.JSONEq(t, `{"original_goal":"Run tests","proposed_goal":"Run tests and report failures","automation_config":{},"evidence_snapshot":{},"proposal_metadata":{"rationale":"Verify outcome","changes":["report"],"evidence":["test history"],"risks":[]},"base_goal_hash":"`+proposal.BaseGoalHash+`"}`, llm.user, "judge must see exact evidence and current goal")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "completion and rejection must preserve tenant and proposal fences")
		})
	}
}
