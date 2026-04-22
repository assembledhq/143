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

func (m *mockIssueStore) GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
	return models.Issue{}, nil
}

func (m *mockIssueStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.IssueFilters) ([]models.Issue, error) {
	return nil, nil
}

func (m *mockIssueStore) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error {
	m.updated = append(m.updated, issueID)
	return nil
}

type mockSessionStore struct {
	created          []*models.Session
	running          int
	lastResult       *models.SessionResult
	lastResultStatus string
}

func (m *mockSessionStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	return m.running, nil
}

func (m *mockSessionStore) Create(ctx context.Context, run *models.Session) error {
	m.created = append(m.created, run)
	return nil
}

func (m *mockSessionStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.SessionFilters) ([]models.Session, error) {
	return nil, nil
}

func (m *mockSessionStore) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.Session, error) {
	return nil, nil
}

func (m *mockSessionStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error {
	m.lastResult = result
	m.lastResultStatus = status
	return nil
}

func (m *mockSessionStore) UpdatePMPlanID(ctx context.Context, orgID, runID, planID uuid.UUID) error {
	return nil
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
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
		orgs:     &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		jobs:     &mockJobStore{},
		plans:    &mockPlanStore{},
		logger:   zerolog.Nop(),
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

	err = svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err, "executePlan should succeed")
	require.Equal(t, models.PMTaskStatusDelegated, plan.Tasks[0].Status, "first task should be delegated")
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[1].Status, "second task should be skipped by capacity")

	runStore := svc.sessions.(*mockSessionStore)
	require.Len(t, runStore.created, 1, "should create one agent run")
	require.NotNil(t, runStore.created[0].PMPlanID, "created run should link to PM plan")
	require.Equal(t, planID, *runStore.created[0].PMPlanID, "PM plan ID should be set")
	require.NotNil(t, runStore.created[0].Title, "session title should be set")
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
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
		orgs:     &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		jobs:     &mockJobStore{},
		plans:    &mockPlanStore{},
		logger:   zerolog.Nop(),
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

	err = svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err, "executePlan should succeed")
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[0].Status, "manual autonomy should skip delegation")

	runStore := svc.sessions.(*mockSessionStore)
	require.Len(t, runStore.created, 0, "should not create agent runs in manual mode")
}

type mockSessionLogStore struct {
	logs []*models.SessionLog
}

func (m *mockSessionLogStore) Create(ctx context.Context, log *models.SessionLog) error {
	m.logs = append(m.logs, log)
	return nil
}

func TestAnalyze_FailSessionRecordsError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessions := &mockSessionStore{}
	logStore := &mockSessionLogStore{}

	svc := &Service{
		sessions:    sessions,
		sessionLogs: logStore,
		orgs:        &failingOrgStore{}, // gatherContext will fail on GetByID
		repos:       &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active"}}},
		sandbox:     &mockSandbox{},
		adapters:    testAdapterMap(&mockAdapter{}),
		env:         testAgentEnv(),
		logger:      zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.Error(t, err, "Analyze should fail")
	require.Contains(t, err.Error(), "gather context")

	// Verify session was created, then marked failed with error via UpdateResult.
	require.Len(t, sessions.created, 1, "PM session should be created")
	require.Equal(t, "failed", sessions.lastResultStatus, "session should be marked failed")
	require.NotNil(t, sessions.lastResult, "UpdateResult should have been called")
	require.NotNil(t, sessions.lastResult.Error, "error message should be set on session")
	require.Contains(t, *sessions.lastResult.Error, "gather context", "error should describe the failure stage")
	require.Contains(t, *sessions.lastResult.Error, "org not found", "error should include the underlying cause")

	// Verify an error-level session log was written.
	require.Len(t, logStore.logs, 1, "should write one session log entry")
	require.Equal(t, "error", logStore.logs[0].Level, "log should be error level")
	require.Contains(t, logStore.logs[0].Message, "gather context", "log message should describe failure")
}
