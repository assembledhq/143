package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestAgentCapabilityPolicyStore_GetSessionDefaultPolicyScopesByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	policyID := uuid.New()
	store := NewAgentCapabilityPolicyStore(mock)

	mock.ExpectQuery("SELECT id, org_id, policy_type, automation_id, name, active, created_by, created_at FROM agent_capability_policies").
		WithArgs(anyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "policy_type", "automation_id", "name", "active", "created_by", "created_at"}).
			AddRow(policyID, orgID, models.AgentCapabilityPolicyTypeSessionDefault, nil, "", true, nil, time.Now()))
	mock.ExpectQuery("SELECT id, org_id, policy_id, capability_id, access_level, enabled, config, created_by, created_at FROM agent_capability_policy_grants").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "policy_id", "capability_id", "access_level", "enabled", "config", "created_by", "created_at"}).
			AddRow(uuid.New(), orgID, policyID, models.AgentCapabilityRepoContext, models.AgentCapabilityAccessRead, true, []byte(`{}`), nil, time.Now()))

	policy, err := store.GetSessionDefaultPolicy(context.Background(), orgID)
	require.NoError(t, err, "session default policy should load")
	require.Equal(t, orgID, policy.OrgID, "policy should belong to requested org")
	require.Equal(t, policyID, policy.ID, "policy id should match returned row")
	require.Len(t, policy.Grants, 1, "policy should include grants for the same org and policy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAgentCapabilityPolicyStore_UpdateSessionDefaultPolicyReplacesInTransaction(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	policyID := uuid.New()
	store := NewAgentCapabilityPolicyStore(mock)

	grants := []models.AgentCapabilityPolicyGrantInput{{
		CapabilityID: models.AgentCapabilitySessionHistory,
		AccessLevel:  models.AgentCapabilityAccessRead,
		Enabled:      true,
		Config:       json.RawMessage(`{"max_age_days":30}`),
	}}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE agent_capability_policies SET active = false").
		WithArgs(anyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO agent_capability_policies").
		WithArgs(anyArgs(4)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(policyID))
	mock.ExpectExec("INSERT INTO agent_capability_policy_grants").
		WithArgs(anyArgs(7)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	gotID, err := store.UpdateSessionDefaultPolicy(context.Background(), orgID, &userID, grants)
	require.NoError(t, err, "session default replacement should commit")
	require.Equal(t, policyID, gotID, "new policy id should be returned")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAgentCapabilityPolicyStore_GetAutomationPolicyJoinsAutomationByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	automationID := uuid.New()
	policyID := uuid.New()
	store := NewAgentCapabilityPolicyStore(mock)

	mock.ExpectQuery("JOIN automations a ON a.id = p.automation_id AND a.org_id = p.org_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "policy_type", "automation_id", "name", "active", "created_by", "created_at"}).
			AddRow(policyID, orgID, models.AgentCapabilityPolicyTypeAutomation, &automationID, "", true, nil, time.Now()))
	mock.ExpectQuery("SELECT id, org_id, policy_id, capability_id, access_level, enabled, config, created_by, created_at FROM agent_capability_policy_grants").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "policy_id", "capability_id", "access_level", "enabled", "config", "created_by", "created_at"}))

	_, err = store.GetAutomationPolicy(context.Background(), orgID, automationID)
	require.NoError(t, err, "automation policy should load through org-scoped automation join")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
