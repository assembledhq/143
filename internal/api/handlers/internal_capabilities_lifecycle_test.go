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

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type capabilityRequestCapture struct {
	requests []models.HumanInputRequest
	err      error
	id       uuid.UUID
}

func (f *capabilityRequestCapture) Create(_ context.Context, request *models.HumanInputRequest) error {
	f.requests = append(f.requests, *request)
	request.ID = f.id
	return f.err
}

func TestInternalCapabilityRequestLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, action, stage, body, code string
		status                          int
	}{
		{name: "effective snapshot preserves token fences", action: "effective", status: 200},
		{name: "request approval pauses exact session", status: 202},
		{name: "invalid JSON", body: "{", status: 400, code: "INVALID_BODY"},
		{name: "invalid capability", body: `{"capability_id":"unknown","access_level":"read"}`, status: 400, code: "INVALID_CAPABILITY"},
		{name: "request store failure", stage: "insert", status: 500, code: "CAPABILITY_REQUEST_FAILED"},
		{name: "pause failure remains visible", stage: "pause", status: 500, code: "UPDATE_SESSION_FAILED"},
		{name: "missing token", stage: "token", status: 401, code: "UNAUTHORIZED"},
		{name: "invalid signature", stage: "signature", status: 401, code: "UNAUTHORIZED"},
		{name: "repo-only token rejected", stage: "scope", status: 403, code: "SESSION_REQUIRED"},
		{name: "foreign session hidden", stage: "missing", status: 404, code: "NOT_FOUND"},
		{name: "repository mismatch", stage: "repo", status: 403, code: "REPO_MISMATCH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, repo, session, thread, requestID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			secret := "isolated-capability-signing-secret"
			token, err := auth.GenerateSessionThreadTokenWithClaims(secret, org, repo, session, &thread, []string{models.PerTargetToolAllowlistScope, "tool:preview_status"}, "", nil, time.Minute)
			require.NoError(t, err, "sign tenant, repository, session, thread and tool-scoped fixture token")
			if tt.stage == "token" {
				token = ""
			}
			if tt.stage == "signature" {
				token = "invalid"
			}
			if tt.stage == "scope" {
				token, err = auth.GenerateInternalToken(secret, org, repo, time.Minute)
				require.NoError(t, err, "sign repository-only token for scope rejection")
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated internal authorization store")
			defer mock.Close()
			row := retrySessionRow(session, org, models.SessionStatusRunning, nil, nil, models.SandboxStateRunning, nil, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
			for index, column := range sessionColumns {
				if column == "repository_id" {
					row[index] = &repo
				}
				if column == "workspace_generation" || column == "workspace_revision" {
					row[index] = int64(0)
				}
				if column == "workspace_revision_updated_at" {
					row[index] = time.Time{}
				}
				if column == "capability_snapshot" {
					row[index] = []models.AgentCapabilitySnapshotItem{}
				}
			}
			if tt.stage == "repo" {
				for index, column := range sessionColumns {
					if column == "repository_id" {
						row[index] = ptr(uuid.New())
					}
				}
			}
			if tt.stage != "token" && tt.stage != "signature" && tt.stage != "scope" {
				q := mock.ExpectQuery(`SELECT .* FROM sessions WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`).WithArgs(pgx.NamedArgs{"id": session, "org_id": org})
				if tt.stage == "missing" {
					q.WillReturnError(pgx.ErrNoRows)
				} else {
					q.WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(row...))
				}
			}
			writer := &capabilityRequestCapture{id: requestID}
			if tt.stage == "insert" {
				writer.err = errors.New("private request store error")
			}
			svc := agentcapabilities.NewService(nil)
			svc.SetHumanInputRequestWriter(writer)
			if tt.status == 202 || tt.stage == "pause" {
				q := mock.ExpectQuery(`UPDATE sessions SET status = @status, runtime_soft_deadline_at = NULL, runtime_hard_deadline_at = NULL, last_activity_at = now\(\) WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": session, "org_id": org, "status": string(models.SessionStatusAwaitingInput)})
				if tt.stage == "pause" {
					q.WillReturnError(errors.New("private pause error"))
				} else {
					for index, column := range sessionColumns {
						if column == "status" {
							row[index] = models.SessionStatusAwaitingInput
						}
					}
					q.WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(row...))
				}
			}
			body := tt.body
			if body == "" {
				body = `{"capability_id":"repo_context","access_level":"read","reason":" inspect source "}`
			}
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			h := NewInternalAgentCapabilitiesHandler(svc, db.NewSessionStore(mock), secret)
			rr := httptest.NewRecorder()
			if tt.action == "effective" {
				h.Effective(rr, r)
			} else {
				h.Request(rr, r)
			}
			require.Equal(t, tt.status, rr.Code, "internal capability endpoint must enforce signed identity before approval writes")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else if tt.action == "effective" {
				var response models.SingleResponse[internalCapabilityResponse]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "effective capability response must decode")
				require.Equal(t, internalCapabilityResponse{Snapshot: []models.AgentCapabilitySnapshotItem{}, Catalog: svc.Definitions(), SessionID: session, RepositoryID: repo, ToolAllowlist: []string{"preview_status"}}, response.Data, "effective response must preserve token tool allowlist and exact session identity")
			} else {
				var response models.SingleResponse[models.HumanInputRequest]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "accepted capability request must decode")
				expected := writer.requests[0]
				expected.ID = requestID
				require.Equal(t, expected, response.Data, "accepted response must retain every persisted approval field")
			}
			if tt.status == 202 || tt.stage == "pause" || tt.stage == "insert" {
				expected := models.HumanInputRequest{OrgID: org, SessionID: session, ThreadID: &thread, TurnNumber: 1, AgentType: models.AgentTypeClaudeCode, Kind: models.HumanInputRequestKindActionChoice, Status: models.HumanInputRequestStatusPending, Title: "Approve agent capability", Body: "The agent requested Repository context (read access).\n\nReason: inspect source", Sensitivity: models.HumanInputSensitivityTeam, PreferredChannel: models.HumanInputPreferredChannelWeb, Choices: []models.HumanInputChoice{{ID: "approve", Label: "Approve", Description: "Grant this capability for the current session only."}, {ID: "deny", Label: "Deny", Description: "Resume without this capability.", Destructive: true}}, ProviderPayload: json.RawMessage(`{"access_level":"read","capability_id":"repo_context","reason":"inspect source","type":"agent_capability_request"}`)}
				require.Equal(t, []models.HumanInputRequest{expected}, writer.requests, "approval creation must retain authenticated tenant and signed execution identity")
			} else {
				require.Empty(t, writer.requests, "authorization and validation failures must create no approval request")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "authorization and pause must bind the exact signed tenant and session")
		})
	}
}
