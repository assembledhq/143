package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
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

func (m *mockIssueStore) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status models.IssueStatus) error {
	m.updated = append(m.updated, issueID)
	return nil
}

type mockSessionStore struct {
	created          []*models.Session
	running          int
	lastResult       *models.SessionResult
	lastResultStatus models.SessionStatus
}

func (m *mockSessionStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	return m.running, nil
}

func (m *mockSessionStore) Create(ctx context.Context, run *models.Session) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	if run.PrimaryThreadID == nil {
		threadID := uuid.New()
		run.PrimaryThreadID = &threadID
	}
	m.created = append(m.created, run)
	return nil
}

func (m *mockSessionStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.SessionFilters) ([]models.Session, error) {
	return nil, nil
}

func (m *mockSessionStore) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.Session, error) {
	return nil, nil
}

func (m *mockSessionStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status models.SessionStatus, result *models.SessionResult) error {
	m.lastResult = result
	m.lastResultStatus = status
	return nil
}

func (m *mockSessionStore) UpdatePMPlanID(ctx context.Context, orgID, runID, planID uuid.UUID) error {
	return nil
}

type mockSessionMessageStore struct {
	created []*models.SessionMessage
}

func (m *mockSessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	m.created = append(m.created, msg)
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
	created []*models.PMPlan
	updated []*models.PMPlan
}

func (m *mockPlanStore) Create(ctx context.Context, plan *models.PMPlan) error {
	m.created = append(m.created, plan)
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
		issues:          &mockIssueStore{},
		sessions:        &mockSessionStore{},
		sessionMessages: &mockSessionMessageStore{},
		orgs:            &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		jobs:            &mockJobStore{},
		plans:           &mockPlanStore{},
		logger:          zerolog.Nop(),
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

	messageStore := svc.sessionMessages.(*mockSessionMessageStore)
	require.Len(t, messageStore.created, 1, "delegated PM sessions should include an initial user message")
	require.Equal(t, runStore.created[0].ID, messageStore.created[0].SessionID, "initial user message should belong to delegated session")
	require.Equal(t, orgID, messageStore.created[0].OrgID, "initial user message should be org-scoped")
	require.Equal(t, runStore.created[0].PrimaryThreadID, messageStore.created[0].ThreadID, "initial user message should be attached to the primary thread")
	require.Equal(t, 0, messageStore.created[0].TurnNumber, "initial user message should be turn zero")
	require.Equal(t, models.MessageRoleUser, messageStore.created[0].Role, "initial PM message should render as a user message")
	require.Equal(t, "Approach 1", messageStore.created[0].Content, "initial user message should use the PM approach")
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
	require.Equal(t, models.SessionStatusFailed, sessions.lastResultStatus, "session should be marked failed")
	require.NotNil(t, sessions.lastResult, "UpdateResult should have been called")
	require.NotNil(t, sessions.lastResult.Error, "error message should be set on session")
	require.Contains(t, *sessions.lastResult.Error, "gather context", "error should describe the failure stage")
	require.Contains(t, *sessions.lastResult.Error, "org not found", "error should include the underlying cause")

	// Verify an error-level session log was written.
	require.Len(t, logStore.logs, 1, "should write one session log entry")
	require.Equal(t, models.SessionLogLevelError, logStore.logs[0].Level, "log should be error level")
	require.Contains(t, logStore.logs[0].Message, "gather context", "log message should describe failure")
}

func TestAnalyze_DoesNotPersistUnavailableTokenUsage(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	plans := &mockPlanStore{}
	settingsJSON, err := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3, AutonomyLevel: "auto_all", DefaultAgentType: "codex"})
	require.NoError(t, err, "should marshal settings")

	issueID := uuid.New()
	planOutput := fmt.Sprintf(`<pm-plan>
{
  "analysis": "unavailable usage",
  "tasks": [{"rank":1,"issue_ids":["%s"],"title":"t","reasoning":"r","approach":"a","risk":"r","complexity":"simple","confidence":"high"}],
  "clusters": [],
  "skip": []
}
</pm-plan>`, issueID)

	unavailableUsage := agent.FinalizeTokenUsage(agent.TokenUsage{}, agent.TokenUsageHint{
		AgentType:      models.AgentTypeCodex,
		EffectiveModel: models.CodexModelGPT54,
		BillingMode:    agent.TokenBillingModeSubscription,
	})

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{executeResult: &agent.AgentResult{
			Summary:    planOutput,
			TokenUsage: unavailableUsage,
		}}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos:    &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", OrgID: orgID}}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		plans:    plans,
		jobs:     &mockJobStore{},
		logger:   zerolog.Nop(),
	}

	plan, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.NoError(t, err, "Analyze should succeed when token usage is unavailable")
	require.NotNil(t, plan, "Analyze should return a plan")
	require.Len(t, plans.created, 1, "Analyze should persist one PM plan")
	require.Nil(t, plans.created[0].TokenUsage, "PM plan should leave token usage nil when the provider reported no token payload")
}

func TestAnalyze_EnrichesTokenUsageHintOnPrompt(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	issueID := uuid.New()
	inner := &pmInnerAdapterMock{executeResult: &agent.AgentResult{
		Summary: fmt.Sprintf(`<pm-plan>
{
  "analysis": "usage hint",
  "tasks": [{"rank":1,"issue_ids":["%s"],"title":"t","reasoning":"r","approach":"a","risk":"r","complexity":"simple","confidence":"high"}],
  "clusters": [],
  "skip": []
}
</pm-plan>`, issueID),
	}}
	settingsJSON, err := json.Marshal(models.OrgSettings{
		MaxConcurrentRuns: 3,
		AutonomyLevel:     "auto_all",
		DefaultAgentType:  "codex",
		AgentConfig: models.AgentEnvConfig{
			string(models.AgentTypeCodex): {
				"OPENAI_MODEL": models.CodexModelGPT54,
			},
		},
	})
	require.NoError(t, err, "should marshal settings")

	svc := &Service{
		adapters: testAdapterMap(inner),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos:    &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", OrgID: orgID}}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		plans:    &mockPlanStore{},
		jobs:     &mockJobStore{},
		logger:   zerolog.Nop(),
	}

	_, err = svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)

	require.NoError(t, err, "Analyze should succeed")
	require.NotNil(t, inner.calledPrompt, "Analyze should pass a prompt to the inner agent")
	require.Equal(t, models.AgentTypeCodex, inner.calledPrompt.UsageHint.AgentType, "Analyze should preserve the selected agent type in UsageHint")
	require.Equal(t, models.CodexModelGPT54, inner.calledPrompt.UsageHint.EffectiveModel, "Analyze should propagate the effective model into UsageHint")
	require.Equal(t, agent.TokenBillingModeSubscription, inner.calledPrompt.UsageHint.BillingMode, "Analyze should record subscription billing when Codex runs via ChatGPT auth")
}
