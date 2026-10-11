package handlers

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func expectCapabilityPolicyRead(mock pgxmock.PgxPoolIface, p models.AgentCapabilityPolicy, stage string) {
	query := `SELECT .+ FROM agent_capability_policies\s+WHERE org_id = @org_id\s+AND policy_type = 'session_default'\s+AND active = true`
	args := pgx.NamedArgs{"org_id": p.OrgID}
	if p.AutomationID != nil {
		query = `SELECT .+ FROM agent_capability_policies p\s+JOIN automations a ON a.id = p.automation_id AND a.org_id = p.org_id\s+WHERE p.org_id = @org_id\s+AND p.automation_id = @automation_id`
		args["automation_id"] = *p.AutomationID
	}
	q := mock.ExpectQuery(query).WithArgs(args)
	if stage == "missing" {
		q.WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "policy_type", "automation_id", "name", "active", "created_by", "created_at"}))
		return
	}
	if stage == "read" {
		q.WillReturnError(errors.New("private storage failure"))
		return
	}
	q.WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "policy_type", "automation_id", "name", "active", "created_by", "created_at"}).AddRow(p.ID, p.OrgID, p.PolicyType, p.AutomationID, p.Name, p.Active, p.CreatedBy, p.CreatedAt))
	grants := mock.ExpectQuery(`SELECT .+ FROM agent_capability_policy_grants\s+WHERE org_id = @org_id AND policy_id = @policy_id`).WithArgs(pgx.NamedArgs{"org_id": p.OrgID, "policy_id": p.ID})
	if stage == "grants" {
		grants.WillReturnError(errors.New("private storage failure"))
		return
	}
	rows := pgxmock.NewRows([]string{"id", "org_id", "policy_id", "capability_id", "access_level", "enabled", "config", "created_by", "created_at"})
	for _, g := range p.Grants {
		rows.AddRow(g.ID, g.OrgID, g.PolicyID, g.CapabilityID, g.AccessLevel, g.Enabled, g.Config, g.CreatedBy, g.CreatedAt)
	}
	grants.WillReturnRows(rows)
}

func TestAgentCapabilityPolicyLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stage, body string
		automatic, patch  bool
		status            int
		code              string
	}{
		{name: "read session policy", status: 200}, {name: "read automation policy", automatic: true, status: 200},
		{name: "no session policy", stage: "missing", status: 200}, {name: "no automation policy", stage: "missing", automatic: true, status: 200},
		{name: "policy read failure", stage: "read", status: 500, code: "GET_CAPABILITIES_FAILED"},
		{name: "grant read failure", stage: "grants", automatic: true, status: 500, code: "GET_CAPABILITIES_FAILED"},
		{name: "invalid automation ID", stage: "id", automatic: true, status: 400, code: "INVALID_ID"},
		{name: "replace session policy", patch: true, status: 200},
		{name: "replace automation policy", patch: true, automatic: true, status: 200},
		{name: "begin fails", patch: true, stage: "begin", status: 500, code: "UPDATE_CAPABILITIES_FAILED"},
		{name: "deactivate fails", patch: true, stage: "deactivate", status: 500, code: "UPDATE_CAPABILITIES_FAILED"},
		{name: "insert policy fails", patch: true, stage: "insert", status: 500, code: "UPDATE_CAPABILITIES_FAILED"},
		{name: "insert grant fails", patch: true, stage: "grant", status: 500, code: "UPDATE_CAPABILITIES_FAILED"},
		{name: "commit fails", patch: true, stage: "commit", status: 500, code: "UPDATE_CAPABILITIES_FAILED"},
		{name: "ownership fails", patch: true, automatic: true, stage: "ownership", status: 500, code: "UPDATE_CAPABILITIES_FAILED"},
		{name: "other tenant automation hidden", patch: true, automatic: true, stage: "absent", status: 404, code: "NOT_FOUND"},
		{name: "patch ID invalid", patch: true, automatic: true, stage: "id", status: 400, code: "INVALID_ID"},
		{name: "invalid request", patch: true, body: `{`, status: 400, code: "INVALID_REQUEST"},
		{name: "unknown grant rejected", patch: true, body: `{"capabilities":[{"capability_id":"unknown","access_level":"read","enabled":true}]}`, status: 400, code: "INVALID_CAPABILITY"},
		{name: "duplicate grants rejected", patch: true, body: `{"capabilities":[{"capability_id":"repo_context","access_level":"read","enabled":true},{"capability_id":"repo_context","access_level":"read","enabled":true}]}`, status: 400, code: "INVALID_CAPABILITY"},
		{name: "session cannot grant automation actions", patch: true, body: `{"capabilities":[{"capability_id":"automation_actions","access_level":"write","enabled":true}]}`, status: 400, code: "AUTOMATION_ONLY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated policy transaction fixture")
			defer mock.Close()
			org, user, automation, policyID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			p := models.AgentCapabilityPolicy{ID: policyID, OrgID: org, PolicyType: models.AgentCapabilityPolicyTypeSessionDefault, Name: "Session defaults", Active: true, CreatedBy: &user, CreatedAt: now}
			if tt.automatic {
				p.AutomationID = &automation
				p.PolicyType = models.AgentCapabilityPolicyTypeAutomation
				p.Name = "Automation policy"
			}
			p.Grants = []models.AgentCapabilityGrant{{ID: uuid.New(), OrgID: org, PolicyID: policyID, CapabilityID: models.AgentCapabilityRepoContext, AccessLevel: models.AgentCapabilityAccessRead, Enabled: true, Config: json.RawMessage(`{}`), CreatedBy: &user, CreatedAt: now}}
			body := tt.body
			if body == "" {
				body = `{"capabilities":[{"capability_id":"repo_context","access_level":"read","enabled":true}]}`
			}
			validWrite := tt.patch && tt.body == "" && tt.stage != "id"
			if validWrite {
				owned := true
				if tt.automatic {
					q := mock.ExpectQuery(`SELECT EXISTS \(\s+SELECT 1 FROM automations\s+WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`).WithArgs(pgx.NamedArgs{"id": automation, "org_id": org})
					if tt.stage == "ownership" {
						q.WillReturnError(errors.New("private storage failure"))
						owned = false
					} else {
						owned = tt.stage != "absent"
						q.WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(owned))
					}
				}
				if owned {
					begin := mock.ExpectBegin()
					if tt.stage == "begin" {
						begin.WillReturnError(errors.New("private storage failure"))
					} else {
						query := `UPDATE agent_capability_policies SET active = false\s+WHERE org_id = @org_id AND policy_type = @policy_type AND active = true`
						args := pgx.NamedArgs{"org_id": org, "policy_type": string(p.PolicyType)}
						if tt.automatic {
							query = `UPDATE agent_capability_policies SET active = false\s+WHERE org_id = @org_id AND automation_id = @automation_id AND policy_type = @policy_type AND active = true`
							args["automation_id"] = automation
						}
						e := mock.ExpectExec(query).WithArgs(args)
						if tt.stage == "deactivate" {
							e.WillReturnError(errors.New("private storage failure"))
						} else {
							q := mock.ExpectQuery(`INSERT INTO agent_capability_policies`).WithArgs(pgx.NamedArgs{"org_id": org, "policy_type": p.PolicyType, "automation_id": p.AutomationID, "created_by": &user})
							e.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
							if tt.stage == "insert" {
								q.WillReturnError(errors.New("private storage failure"))
							} else {
								q.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(policyID))
								e := mock.ExpectExec(`INSERT INTO agent_capability_policy_grants`).WithArgs(pgx.NamedArgs{"org_id": org, "policy_id": policyID, "capability_id": models.AgentCapabilityRepoContext, "access_level": models.AgentCapabilityAccessRead, "enabled": true, "config": json.RawMessage(`{}`), "created_by": &user})
								if tt.stage == "grant" {
									e.WillReturnError(errors.New("private storage failure"))
								} else {
									e.WillReturnResult(pgxmock.NewResult("INSERT", 1))
									c := mock.ExpectCommit()
									if tt.stage == "commit" {
										c.WillReturnError(errors.New("private storage failure"))
									}
								}
							}
						}
						mock.ExpectRollback()
					}
				}
			}
			if (!tt.patch && tt.stage != "id") || (validWrite && tt.stage == "") {
				expectCapabilityPolicyRead(mock, p, tt.stage)
			}
			route := automation.String()
			if tt.stage == "id" {
				route = "invalid"
			}
			r := apiClientLifecycleRequest(org, user, route, "", body)
			w := httptest.NewRecorder()
			store := db.NewAgentCapabilityPolicyStore(mock)
			h := NewAgentCapabilitiesHandler(store, agentcapabilities.NewService(store))
			if tt.patch {
				if tt.automatic {
					h.PatchAutomationPolicy(w, r)
				} else {
					h.PatchSessionDefault(w, r)
				}
			} else {
				if tt.automatic {
					h.GetAutomationPolicy(w, r)
				} else {
					h.GetSessionDefault(w, r)
				}
			}
			require.Equal(t, tt.status, w.Code, "policy lifecycle must preserve ownership, validation and transaction outcomes")
			if tt.code != "" {
				requireAPIClientError(t, w, tt.code)
			} else {
				expected := capabilityPolicyResponse{Policy: &p, Capabilities: p.Grants}
				if tt.stage == "missing" {
					expected = capabilityPolicyResponse{Capabilities: nil}
				}
				var response models.SingleResponse[capabilityPolicyResponse]
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response), "decode exact policy response")
				require.Equal(t, expected, response.Data, "response must preserve exact active policy and grants")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "policy writes must retain prior version, org ownership and atomic commit or rollback")
		})
	}
}
