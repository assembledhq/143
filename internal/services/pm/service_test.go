package pm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type mockIssueStore struct {
	updated []uuid.UUID
}

func (m *mockIssueStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.IssueFilters) ([]models.Issue, error) {
	return nil, nil
}

func (m *mockIssueStore) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error {
	m.updated = append(m.updated, issueID)
	return nil
}

type mockAgentRunStore struct {
	created []*models.AgentRun
	running int
}

func (m *mockAgentRunStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	return m.running, nil
}

func (m *mockAgentRunStore) Create(ctx context.Context, run *models.AgentRun) error {
	m.created = append(m.created, run)
	return nil
}

func (m *mockAgentRunStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.AgentRunFilters) ([]models.AgentRun, error) {
	return nil, nil
}

func (m *mockAgentRunStore) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.AgentRun, error) {
	return nil, nil
}

type mockOrgStore struct {
	org models.Organization
}

func (m *mockOrgStore) GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error) {
	return m.org, nil
}

type mockJobStore struct {
	enqueued []string
}

func (m *mockJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	m.enqueued = append(m.enqueued, jobType)
	return uuid.New(), nil
}

type mockPlanStore struct {
	updated []*models.PMPlan
}

func (m *mockPlanStore) Create(ctx context.Context, plan *models.PMPlan) error {
	return nil
}

func (m *mockPlanStore) Update(ctx context.Context, plan *models.PMPlan) error {
	m.updated = append(m.updated, plan)
	return nil
}

func TestExecutePlan_DelegatesWithinCapacity(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue1 := uuid.New()
	issue2 := uuid.New()
	planID := uuid.New()

	settings := models.OrgSettings{
		MaxConcurrentRuns: 1,
		AutonomyLevel:     "auto_all",
		DefaultAgentType:  "codex",
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err, "should marshal settings")

	svc := &Service{
		issues:    &mockIssueStore{},
		agentRuns: &mockAgentRunStore{},
		orgs:      &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		jobs:      &mockJobStore{},
		plans:     &mockPlanStore{},
		logger:    zerolog.Nop(),
	}

	plan := &Plan{
		ID:    planID,
		OrgID: orgID,
		Tasks: []Task{
			{
				Rank:       1,
				IssueIDs:   []uuid.UUID{issue1},
				Approach:   "Approach 1",
				Reasoning:  "Reason 1",
				Complexity: models.PMTaskComplexitySimple,
				Confidence: models.PMTaskConfidenceHigh,
			},
			{
				Rank:       2,
				IssueIDs:   []uuid.UUID{issue2},
				Approach:   "Approach 2",
				Reasoning:  "Reason 2",
				Complexity: models.PMTaskComplexitySimple,
				Confidence: models.PMTaskConfidenceHigh,
			},
		},
	}

	err = svc.executePlan(context.Background(), orgID, plan)
	require.NoError(t, err, "executePlan should succeed")
	require.Equal(t, models.PMTaskStatusDelegated, plan.Tasks[0].Status, "first task should be delegated")
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[1].Status, "second task should be skipped by capacity")

	runStore := svc.agentRuns.(*mockAgentRunStore)
	require.Len(t, runStore.created, 1, "should create one agent run")
	require.NotNil(t, runStore.created[0].PMPlanID, "created run should link to PM plan")
	require.Equal(t, planID, *runStore.created[0].PMPlanID, "PM plan ID should be set")
	require.NotNil(t, runStore.created[0].PMApproach, "PM approach should be set")
	require.NotNil(t, runStore.created[0].PMReasoning, "PM reasoning should be set")
}

func TestExecutePlan_ManualAutonomySkipsDelegation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue1 := uuid.New()
	planID := uuid.New()

	settings := models.OrgSettings{
		MaxConcurrentRuns: 3,
		AutonomyLevel:     "manual",
		DefaultAgentType:  "codex",
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err, "should marshal settings")

	svc := &Service{
		issues:    &mockIssueStore{},
		agentRuns: &mockAgentRunStore{},
		orgs:      &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		jobs:      &mockJobStore{},
		plans:     &mockPlanStore{},
		logger:    zerolog.Nop(),
	}

	plan := &Plan{
		ID:    planID,
		OrgID: orgID,
		Tasks: []Task{
			{
				Rank:       1,
				IssueIDs:   []uuid.UUID{issue1},
				Approach:   "Approach 1",
				Reasoning:  "Reason 1",
				Complexity: models.PMTaskComplexitySimple,
				Confidence: models.PMTaskConfidenceHigh,
			},
		},
	}

	err = svc.executePlan(context.Background(), orgID, plan)
	require.NoError(t, err, "executePlan should succeed")
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[0].Status, "manual autonomy should skip delegation")

	runStore := svc.agentRuns.(*mockAgentRunStore)
	require.Len(t, runStore.created, 0, "should not create agent runs in manual mode")
}
