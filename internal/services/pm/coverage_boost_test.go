package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// testAdapterMap wraps a single inner adapter as the adapter for the default
// agent type used by PM tests. Tests that don't set OrgSettings.DefaultAgentType
// fall through to models.DefaultDefaultAgentType, so registering under that key
// is enough to satisfy pickAdapter for the common case.
func testAdapterMap(a agent.AgentAdapter) map[models.AgentType]agent.AgentAdapter {
	return map[models.AgentType]agent.AgentAdapter{
		models.DefaultDefaultAgentType: a,
	}
}

type testCodexAuthProvider struct{}

func (testCodexAuthProvider) GetValidToken(_ context.Context, _ uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	return &models.OpenAIChatGPTConfig{
		AccessToken: "test-access-token",
		IDToken:     "test-id-token",
	}, nil
}

// testAgentEnv returns an AgentEnv with inert upstream dependencies wired in.
// Resolve still returns an empty env for most tests, CheckAuth stays a no-op
// for non-Amp/Pi agents, and Codex auth injection succeeds so tests that rely
// on the platform default agent type do not fail before reaching their intended
// assertion point.
func testAgentEnv() *agent.AgentEnv {
	return agent.NewAgentEnv(agent.AgentEnvDeps{
		CodexAuth: testCodexAuthProvider{},
		Provider:  &pmSandboxMock{},
		Logger:    zerolog.Nop(),
	})
}

// --- buildProjectSummaries and buildProjectSummary ---

func TestBuildProjectSummaries_Success(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	scope := "entire auth module"
	criteria := "all tests pass"
	phase := "implementation"

	ps := newMockProjectStore(models.Project{
		ID:                 projectID,
		OrgID:              orgID,
		Title:              "Auth overhaul",
		Goal:               "improve security",
		Scope:              &scope,
		CompletionCriteria: &criteria,
		Status:             models.ProjectStatusActive,
		Priority:           1,
		ExecutionMode:      models.ProjectExecModeParallel,
		MaxConcurrent:      3,
		CurrentPhase:       &phase,
		TotalTasks:         10,
		CompletedTasks:     5,
		FailedTasks:        1,
		LessonsLearned:     []string{"lesson-1"},
		ApproachHistory:    []models.ApproachRecord{{TaskTitle: "t1", Approach: "a1", Outcome: "ok"}},
	})

	approach := "fix auth"
	outcome := "worked"
	complexity := "moderate"
	confidence := "high"

	pts := &mockProjectTaskStore{
		tasks: []*models.ProjectTask{
			{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "Pending task", Status: models.ProjectTaskStatusPending, BatchNumber: 1, Approach: &approach, Complexity: &complexity, Confidence: &confidence},
			{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "Running task", Status: models.ProjectTaskStatusRunning, BatchNumber: 1},
			{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "Delegated task", Status: models.ProjectTaskStatusDelegated, BatchNumber: 1},
			{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "Completed task", Status: models.ProjectTaskStatusCompleted, BatchNumber: 1, OutcomeNotes: &outcome},
			{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "Failed task", Status: models.ProjectTaskStatusFailed, BatchNumber: 1},
		},
	}

	pcs := &mockProjectCycleStore{
		cycles: []models.ProjectCycle{
			{CycleNumber: 3, Analysis: "cycle 3 analysis", TasksCreatedThisCycle: 2, TasksCompletedThisCycle: 1, TasksFailedThisCycle: 0, CreatedAt: time.Now()},
		},
	}

	svc := &Service{
		projects:      ps,
		projectTasks:  pts,
		projectCycles: pcs,
		logger:        zerolog.Nop(),
	}

	summaries, err := svc.buildProjectSummaries(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, summaries, 1)

	s := summaries[0]
	require.Equal(t, projectID.String(), s.ID)
	require.Equal(t, "Auth overhaul", s.Title)
	require.Equal(t, "improve security", s.Goal)
	require.Equal(t, "entire auth module", s.Scope)
	require.Equal(t, "all tests pass", s.CompletionCriteria)
	require.Equal(t, "implementation", s.CurrentPhase)
	require.Equal(t, 1, s.Priority)
	require.Equal(t, "active", s.Status)
	require.Equal(t, "parallel", s.ExecutionMode)
	require.Equal(t, 3, s.MaxConcurrent)
	require.Equal(t, 10, s.TotalTasks)
	require.Equal(t, 5, s.CompletedTasks)
	require.Equal(t, 1, s.FailedTasks)
	require.Equal(t, 50, s.ProgressPct)
	require.Len(t, s.LessonsLearned, 1)
	require.Len(t, s.ApproachHistory, 1)

	// Check task categorization.
	require.Len(t, s.PendingTasks, 1)
	require.Equal(t, "Pending task", s.PendingTasks[0].Title)
	require.Equal(t, "fix auth", s.PendingTasks[0].Approach)
	require.Equal(t, "moderate", s.PendingTasks[0].Complexity)
	require.Equal(t, "high", s.PendingTasks[0].Confidence)

	require.Len(t, s.RunningTasks, 2) // running + delegated
	require.Len(t, s.RecentlyCompleted, 1)
	require.Equal(t, "worked", s.RecentlyCompleted[0].OutcomeNotes)
	require.Len(t, s.RecentlyFailed, 1)

	// Check cycles.
	require.Len(t, s.RecentCycles, 1)
	require.Equal(t, 3, s.RecentCycles[0].CycleNumber)
}

func TestBuildProjectSummaries_ProjectWithZeroTasks(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID:            projectID,
		OrgID:         orgID,
		Title:         "Empty project",
		Goal:          "nothing yet",
		Status:        models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
		TotalTasks:    0,
	})
	pts := &mockProjectTaskStore{}
	pcs := &mockProjectCycleStore{}

	svc := &Service{
		projects:      ps,
		projectTasks:  pts,
		projectCycles: pcs,
		logger:        zerolog.Nop(),
	}

	summaries, err := svc.buildProjectSummaries(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	require.Equal(t, 0, summaries[0].ProgressPct, "should not divide by zero")
}

func TestBuildProjectSummaries_TaskListError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID:            projectID,
		OrgID:         orgID,
		Title:         "Project",
		Status:        models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &errProjectTaskStore{err: fmt.Errorf("db connection lost")}
	pcs := &mockProjectCycleStore{}

	svc := &Service{
		projects:      ps,
		projectTasks:  pts,
		projectCycles: pcs,
		logger:        zerolog.Nop(),
	}

	// buildProjectSummaries logs errors per-project but returns what it can.
	summaries, err := svc.buildProjectSummaries(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, summaries, 0, "should skip projects with errors")
}

type errProjectTaskStore struct {
	err error
}

func (m *errProjectTaskStore) Create(_ context.Context, _ *models.ProjectTask) error {
	return m.err
}
func (m *errProjectTaskStore) GetByID(_ context.Context, _, _ uuid.UUID) (models.ProjectTask, error) {
	return models.ProjectTask{}, m.err
}
func (m *errProjectTaskStore) ListByProject(_ context.Context, _, _ uuid.UUID, _ db.ProjectTaskFilters) ([]models.ProjectTask, error) {
	return nil, m.err
}
func (m *errProjectTaskStore) Update(_ context.Context, _ *models.ProjectTask) error { return m.err }
func (m *errProjectTaskStore) CountByProjectAndStatus(_ context.Context, _, _ uuid.UUID, _ models.ProjectTaskStatus) (int, error) {
	return 0, m.err
}
func (m *errProjectTaskStore) GetMaxBatchNumber(_ context.Context, _, _ uuid.UUID) (int, error) {
	return 0, m.err
}

// --- parsePlan additional coverage ---

func TestParsePlan_NoTags(t *testing.T) {
	t.Parallel()

	_, err := parsePlan("no tags here")
	require.Error(t, err)
	require.Contains(t, err.Error(), "tags not found")
}

func TestParsePlan_EmptyContent(t *testing.T) {
	t.Parallel()

	_, err := parsePlan("<pm-plan>   </pm-plan>")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")
}

func TestParsePlan_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := parsePlan("<pm-plan>{not valid json}</pm-plan>")
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse pm plan json")
}

func TestParsePlan_WithProjectPlansAndSlotAllocation(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	issueID := uuid.New()

	output := fmt.Sprintf(`<pm-plan>
{
  "analysis": "Test with projects",
  "tasks": [
    {
      "rank": 1,
      "issue_ids": ["%s"],
      "title": "Fix bug",
      "reasoning": "High impact",
      "approach": "Fix it",
      "risk": "Low",
      "complexity": "simple",
      "confidence": "high"
    }
  ],
  "clusters": [],
  "skip": [],
  "project_plans": [
    {
      "project_id": "%s",
      "cycle_analysis": "Going well",
      "progress_pct": 75,
      "current_phase": "testing",
      "new_tasks": [{"title": "Write tests", "description": "Add coverage"}]
    }
  ],
  "slot_allocation": {
    "reactive": 2,
    "projects": {"%s": 3},
    "reasoning": "More project work needed"
  }
}
</pm-plan>`, issueID, projectID, projectID)

	plan, err := parsePlan(output)
	require.NoError(t, err)
	require.Len(t, plan.ProjectPlans, 1)
	require.Equal(t, projectID, plan.ProjectPlans[0].ProjectID)
	require.Equal(t, 75, plan.ProjectPlans[0].ProgressPct)
	require.NotNil(t, plan.SlotAllocation)
	require.Equal(t, 2, plan.SlotAllocation.Reactive)
	require.Equal(t, 3, plan.SlotAllocation.Projects[projectID.String()])
}

func TestParsePlan_EndBeforeStart(t *testing.T) {
	t.Parallel()

	_, err := parsePlan("</pm-plan> some text <pm-plan>")
	require.Error(t, err)
	require.Contains(t, err.Error(), "tags not found")
}

// --- executePlan additional paths ---

func TestExecutePlan_EmptyIssueIDsSkipped(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	planID := uuid.New()

	settings := models.OrgSettings{
		MaxConcurrentRuns: 5,
		AutonomyLevel:     "auto_all",
		DefaultAgentType:  "codex",
	}

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
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
				IssueIDs:   []uuid.UUID{}, // empty
				Approach:   "Approach",
				Reasoning:  "Reason",
				Complexity: models.PMTaskComplexitySimple,
				Confidence: models.PMTaskConfidenceHigh,
			},
		},
	}

	err := svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err)
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[0].Status, "tasks with empty issue IDs should be skipped")
}

func TestExecutePlan_LowConfidenceSkippedUnlessAutoAll(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := uuid.New()
	planID := uuid.New()

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
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
				IssueIDs:   []uuid.UUID{issue},
				Approach:   "Approach",
				Reasoning:  "Reason",
				Complexity: models.PMTaskComplexitySimple,
				Confidence: models.PMTaskConfidenceLow,
			},
		},
	}

	// auto_safe should skip low-confidence
	settings := models.OrgSettings{
		MaxConcurrentRuns: 5,
		AutonomyLevel:     "auto_safe",
		DefaultAgentType:  "codex",
	}
	err := svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err)
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[0].Status)

	// auto_all should delegate
	plan.Tasks[0].Status = ""
	settings.AutonomyLevel = "auto_all"
	err = svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err)
	require.Equal(t, models.PMTaskStatusDelegated, plan.Tasks[0].Status)
}

func TestExecutePlan_DefaultAgentType(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := uuid.New()
	planID := uuid.New()

	sessions := &mockSessionStore{}
	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: sessions,
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
				IssueIDs:   []uuid.UUID{issue},
				Approach:   "Approach",
				Reasoning:  "Reason",
				Complexity: models.PMTaskComplexityModerate,
				Confidence: models.PMTaskConfidenceHigh,
			},
		},
	}

	// Empty default agent type should fall back to DefaultDefaultAgentType.
	settings := models.OrgSettings{
		MaxConcurrentRuns: 5,
		AutonomyLevel:     "auto_all",
		DefaultAgentType:  "",
	}
	err := svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err)
	require.Len(t, sessions.created, 1)
	require.Equal(t, models.DefaultDefaultAgentType, sessions.created[0].AgentType)
	require.Equal(t, models.SessionTokenModeHigh, sessions.created[0].TokenMode, "moderate complexity should use high token mode")
}

func TestExecutePlan_DefaultMaxConcurrentRuns(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := uuid.New()
	planID := uuid.New()

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
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
				IssueIDs:   []uuid.UUID{issue},
				Approach:   "Approach",
				Reasoning:  "Reason",
				Complexity: models.PMTaskComplexitySimple,
				Confidence: models.PMTaskConfidenceHigh,
			},
		},
	}

	// Zero MaxConcurrentRuns should use the default.
	settings := models.OrgSettings{
		MaxConcurrentRuns: 0,
		AutonomyLevel:     "auto_all",
		DefaultAgentType:  "codex",
	}
	err := svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err)
	require.Equal(t, models.PMTaskStatusDelegated, plan.Tasks[0].Status)
}

func TestExecutePlan_MultipleIssuesTriaged(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue1 := uuid.New()
	issue2 := uuid.New()
	planID := uuid.New()

	issueStore := &mockIssueStore{}
	svc := &Service{
		issues:   issueStore,
		sessions: &mockSessionStore{},
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
				IssueIDs:   []uuid.UUID{issue1, issue2},
				Approach:   "Approach",
				Reasoning:  "Reason",
				Complexity: models.PMTaskComplexitySimple,
				Confidence: models.PMTaskConfidenceHigh,
			},
		},
	}

	settings := models.OrgSettings{
		MaxConcurrentRuns: 5,
		AutonomyLevel:     "auto_all",
		DefaultAgentType:  "codex",
	}
	err := svc.executePlan(context.Background(), orgID, plan, settings, nil)
	require.NoError(t, err)
	require.Len(t, issueStore.updated, 2, "both issues should be marked as triaged")
}

// --- gatherContext: repo settings merge ---

func TestGatherContext_WithRepoSettings(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	settings := models.OrgSettings{
		MaxConcurrentRuns: 3,
		ProductContext: &models.ProductContext{
			Philosophy: "org philosophy",
		},
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)

	repoSettings := models.RepoSettings{
		PM: &models.RepoPMSettings{
			ProductContext: &models.ProductContext{
				Philosophy: "repo philosophy",
			},
		},
	}
	repoSettingsJSON, err := json.Marshal(repoSettings)
	require.NoError(t, err)

	repo := &models.Repository{
		ID:       uuid.New(),
		OrgID:    orgID,
		Settings: repoSettingsJSON,
	}

	svc := &Service{
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		logger:   zerolog.Nop(),
	}

	bundle, err := svc.gatherContext(context.Background(), orgID, repo)
	require.NoError(t, err)
	require.NotNil(t, bundle.productContext)
	require.Equal(t, "repo philosophy", bundle.productContext.Philosophy, "repo settings should override org settings")
}

func TestGatherContext_SessionErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	tests := []struct {
		name     string
		sessions *gatherSessionStoreMock
		errPart  string
	}{
		{
			name: "pending runs error",
			sessions: &gatherSessionStoreMock{
				byStatus: map[string][]models.Session{},
				errByKey: map[string]error{"pending": fmt.Errorf("pending fail")},
			},
			errPart: "pending fail",
		},
		{
			name: "running runs error",
			sessions: &gatherSessionStoreMock{
				byStatus: map[string][]models.Session{},
				errByKey: map[string]error{"running": fmt.Errorf("running fail")},
			},
			errPart: "running fail",
		},
		{
			name: "recent runs error",
			sessions: &gatherSessionStoreMock{
				byStatus: map[string][]models.Session{},
				errByKey: map[string]error{"recent": fmt.Errorf("recent fail")},
			},
			errPart: "recent fail",
		},
		{
			name: "count running error",
			sessions: &gatherSessionStoreMock{
				byStatus: map[string][]models.Session{},
				errByKey: map[string]error{"count_running": fmt.Errorf("count fail")},
			},
			errPart: "count fail",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{
				issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
				sessions: tt.sessions,
				orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
				logger:   zerolog.Nop(),
			}

			_, err := svc.gatherContext(context.Background(), orgID, nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.errPart)
		})
	}
}

func TestGatherContext_TriagedIssueError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		issues: &gatherIssueStoreMock{
			byStatus: map[string][]models.Issue{},
			errByKey: map[string]error{"triaged": fmt.Errorf("triaged fail")},
		},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		logger:   zerolog.Nop(),
	}

	_, err := svc.gatherContext(context.Background(), orgID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "triaged fail")
}

func TestGatherContext_PRStoreError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		issues:       &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions:     &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		orgs:         &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		pullRequests: &gatherPRStoreMock{err: fmt.Errorf("pr store fail")},
		logger:       zerolog.Nop(),
	}

	_, err := svc.gatherContext(context.Background(), orgID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pr store fail")
}

func TestGatherContext_DecisionLogError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		issues:      &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions:    &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		orgs:        &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		decisionLog: &gatherDecisionStoreMock{err: fmt.Errorf("decision log fail")},
		logger:      zerolog.Nop(),
	}

	_, err := svc.gatherContext(context.Background(), orgID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decision log fail")
}

func TestGatherContext_WithProjectSummaries(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	ps := newMockProjectStore(models.Project{
		ID:            projectID,
		OrgID:         orgID,
		Title:         "Test project",
		Goal:          "test goal",
		Status:        models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &mockProjectTaskStore{}
	pcs := &mockProjectCycleStore{}

	svc := &Service{
		issues:        &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions:      &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		orgs:          &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		projects:      ps,
		projectTasks:  pts,
		projectCycles: pcs,
		logger:        zerolog.Nop(),
	}

	bundle, err := svc.gatherContext(context.Background(), orgID, nil)
	require.NoError(t, err)
	require.Len(t, bundle.pmContext.ActiveProjects, 1)
	require.Equal(t, "Test project", bundle.pmContext.ActiveProjects[0].Title)
}

// --- canDispatchForProject: dependency_graph (default) case ---

func TestCanDispatchForProject_DependencyGraphMode(t *testing.T) {
	t.Parallel()

	pts := &mockProjectTaskStore{countByStatus: map[models.ProjectTaskStatus]int{}}
	svc := newTestProjectService(nil, pts, nil)

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeDependencyGraph,
	}

	t.Run("no active tasks allows 1", func(t *testing.T) { //nolint:paralleltest // subtests share mutable mock state
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 1, got)
	})

	t.Run("active tasks blocks dispatch", func(t *testing.T) { //nolint:paralleltest // subtests share mutable mock state
		pts.countByStatus = map[models.ProjectTaskStatus]int{models.ProjectTaskStatusRunning: 1}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 0, got)
	})
}

func TestCanDispatchForProject_CountErrors(t *testing.T) {
	t.Parallel()

	pts := &errProjectTaskStore{err: fmt.Errorf("count error")}
	svc := &Service{
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeSequential,
	}

	got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
	require.Equal(t, 0, got, "should return 0 when count errors")
}

// --- dispatchProjectTasks: project agent type override ---

func TestDispatchProjectTasks_UsesProjectAgentType(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	agentType := "custom-agent"

	pendingTask := &models.ProjectTask{
		ID:          uuid.New(),
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Task with project agent type",
		Status:      models.ProjectTaskStatusPending,
		BatchNumber: 1,
		SortOrder:   1,
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{pendingTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	sessions := &mockSessionStore{}
	svc := &Service{
		sessions:     sessions,
		jobs:         &mockJobStore{},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential,
		AgentType:     &agentType,
	}

	settings := models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "default-agent",
	}

	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, settings, uuid.New())
	require.Equal(t, 1, dispatched)
	require.Len(t, sessions.created, 1)
	require.Equal(t, models.AgentType("custom-agent"), sessions.created[0].AgentType, "should use project's agent type over default")
}

func TestDispatchProjectTasks_FallbackAgentType(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	pendingTask := &models.ProjectTask{
		ID:          uuid.New(),
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Task",
		Status:      models.ProjectTaskStatusPending,
		BatchNumber: 1,
		SortOrder:   1,
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{pendingTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	sessions := &mockSessionStore{}
	svc := &Service{
		sessions:     sessions,
		jobs:         &mockJobStore{},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential,
	}

	// Both project and settings agent types are empty; should fallback.
	settings := models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "",
	}

	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, settings, uuid.New())
	require.Equal(t, 1, dispatched)
	require.Equal(t, models.DefaultDefaultAgentType, sessions.created[0].AgentType)
}

func TestDispatchProjectTasks_RespectsSlotLimit(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	tasks := []*models.ProjectTask{
		{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "T1", Status: models.ProjectTaskStatusPending, BatchNumber: 1, SortOrder: 1},
		{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "T2", Status: models.ProjectTaskStatusPending, BatchNumber: 1, SortOrder: 2},
		{ID: uuid.New(), ProjectID: projectID, OrgID: orgID, Title: "T3", Status: models.ProjectTaskStatusPending, BatchNumber: 1, SortOrder: 3},
	}

	pts := &mockProjectTaskStore{
		tasks:         tasks,
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	svc := &Service{
		sessions:     &mockSessionStore{},
		jobs:         &mockJobStore{},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential, // only 1 slot
	}

	settings := models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "codex",
	}

	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, settings, uuid.New())
	require.Equal(t, 1, dispatched, "sequential mode should only dispatch 1")
}

// --- recordProjectCycle: no previous cycles ---

func TestRecordProjectCycle_NoPreviousCycles(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	planID := uuid.New()

	pcs := &mockProjectCycleStore{
		cycles: []models.ProjectCycle{}, // no previous cycles
	}
	svc := newTestProjectService(nil, nil, pcs)

	pp := &ProjectPlan{
		ProjectID:     projectID,
		CycleAnalysis: "First cycle",
		ProgressPct:   0,
	}

	svc.recordProjectCycle(context.Background(), orgID, pp, planID, 1, 1)

	require.Len(t, pcs.created, 1)
	require.Equal(t, 1, pcs.created[0].CycleNumber, "first cycle should be 1")
	require.Nil(t, pcs.created[0].ProgressPct, "zero progress should produce nil progress")
}

// --- summarizeIssue with nil description ---

func TestSummarizeIssue_NilDescription(t *testing.T) {
	t.Parallel()

	issue := models.Issue{
		ID:          uuid.New(),
		Source:      models.IssueSource("github"),
		Title:       "Issue without description",
		Description: nil,
		Severity:    "low",
		FirstSeenAt: time.Now(),
		LastSeenAt:  time.Now(),
	}

	summary := summarizeIssue(issue, 500)
	require.Equal(t, "", summary.Description)
}

// --- Analyze validation paths ---

func TestAnalyze_NilAdapterOrSandbox(t *testing.T) {
	t.Parallel()

	svc := &Service{sandbox: nil, env: nil}
	_, err := svc.Analyze(context.Background(), uuid.New(), models.PMTriggerCron, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not configured")
}

func TestAnalyze_InvalidTrigger(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
	}
	_, err := svc.Analyze(context.Background(), uuid.New(), models.PMTrigger("invalid"), nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid trigger")
}

// --- mock repoStore for Analyze tests ---

type mockRepoStore struct {
	repos []models.Repository
	err   error
}

func (m *mockRepoStore) ListByOrg(_ context.Context, _ uuid.UUID, _ db.RepositoryFilters) ([]models.Repository, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.repos, nil
}

func (m *mockRepoStore) GetByID(_ context.Context, _, _ uuid.UUID) (models.Repository, error) {
	if m.err != nil {
		return models.Repository{}, m.err
	}
	if len(m.repos) > 0 {
		return m.repos[0], nil
	}
	return models.Repository{}, fmt.Errorf("not found")
}

func TestAnalyze_NoRepositories(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos:    &mockRepoStore{repos: []models.Repository{}},
	}
	_, err := svc.Analyze(context.Background(), uuid.New(), models.PMTriggerCron, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no repositories")
}

func TestAnalyze_RepoNotFound(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	otherRepoID := uuid.New()

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: otherRepoID, Status: "active"},
		}},
	}
	_, err := svc.Analyze(context.Background(), uuid.New(), models.PMTriggerCron, &repoID, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in org")
}

func TestAnalyze_RepoListError(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos:    &mockRepoStore{err: fmt.Errorf("db error")},
	}
	_, err := svc.Analyze(context.Background(), uuid.New(), models.PMTriggerCron, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list repositories")
}

func TestAnalyze_SelectsActiveRepo(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	inactiveRepoID := uuid.New()
	activeRepoID := uuid.New()

	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	// Adapter that returns a valid plan to exercise more code.
	issueID := uuid.New()
	planOutput := fmt.Sprintf(`<pm-plan>
{
  "analysis": "test",
  "tasks": [{"rank":1,"issue_ids":["%s"],"title":"t","reasoning":"r","approach":"a","risk":"r","complexity":"simple","confidence":"high"}],
  "clusters": [],
  "skip": []
}
</pm-plan>`, issueID)

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{executeResult: &agent.AgentResult{Summary: planOutput}}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: inactiveRepoID, Status: "inactive", OrgID: orgID},
			{ID: activeRepoID, Status: "active", OrgID: orgID},
		}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		plans:    &mockPlanStore{},
		jobs:     &mockJobStore{},
		logger:   zerolog.Nop(),
	}

	plan, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.Equal(t, "test", plan.Analysis)
}

// --- OnSessionComplete: task lookup error ---

func TestProjectHooks_OnSessionComplete_TaskLookupError(t *testing.T) {
	t.Parallel()

	taskID := uuid.New()
	pts := &errProjectTaskStore{err: fmt.Errorf("task not found")}
	hooks := NewProjectHooks(pts, nil, zerolog.Nop())

	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		ProjectTaskID: &taskID,
	}

	err := hooks.OnSessionComplete(context.Background(), run, "completed")
	require.Error(t, err)
	require.Contains(t, err.Error(), "get project task")
}

// --- executeProjectPlan: project not found ---

func TestExecuteProjectPlan_ProjectNotFound(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore() // no projects
	svc := newTestProjectService(ps, &mockProjectTaskStore{}, &mockProjectCycleStore{})

	pp := &ProjectPlan{
		ProjectID:     projectID,
		CycleAnalysis: "test",
	}

	err := svc.executeProjectPlan(context.Background(), orgID, pp, models.OrgSettings{}, uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get project")
}

// --- dispatchProjectTasks: task with approach and reasoning ---

func TestDispatchProjectTasks_TaskWithApproachAndReasoning(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	approach := "my approach"
	reasoning := "my reasoning"
	complexity := "complex"

	pendingTask := &models.ProjectTask{
		ID:          uuid.New(),
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Task with details",
		Status:      models.ProjectTaskStatusPending,
		BatchNumber: 2,
		SortOrder:   3,
		Approach:    &approach,
		Reasoning:   &reasoning,
		Complexity:  &complexity,
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{pendingTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	sessions := &mockSessionStore{}
	svc := &Service{
		sessions:     sessions,
		jobs:         &mockJobStore{},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential,
	}

	settings := models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "codex",
	}

	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, settings, uuid.New())
	require.Equal(t, 1, dispatched)
	require.Len(t, sessions.created, 1)
	require.NotNil(t, sessions.created[0].Title, "session title should be set")
	require.Equal(t, "my approach", *sessions.created[0].PMApproach)
	require.Equal(t, "my reasoning", *sessions.created[0].PMReasoning)
	require.Equal(t, models.SessionTokenModeHigh, sessions.created[0].TokenMode, "complex should map to high")
}

// --- dispatchProjectTasks: model override ---

func TestDispatchProjectTasks_ModelOverride(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	modelOverride := "gpt-4o"

	pendingTask := &models.ProjectTask{
		ID:          uuid.New(),
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Task",
		Status:      models.ProjectTaskStatusPending,
		BatchNumber: 1,
		SortOrder:   1,
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{pendingTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	sessions := &mockSessionStore{}
	svc := &Service{
		sessions:     sessions,
		jobs:         &mockJobStore{},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential,
		ModelOverride: &modelOverride,
	}

	settings := models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "codex",
	}

	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, settings, uuid.New())
	require.Equal(t, 1, dispatched)
	require.Equal(t, &modelOverride, sessions.created[0].ModelOverride)
}

// --- executePlan error paths ---

type errSessionStore struct {
	mockSessionStore
	createErr error
	countErr  error
}

func (m *errSessionStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	if m.countErr != nil {
		return 0, m.countErr
	}
	return m.mockSessionStore.CountRunningByOrg(ctx, orgID)
}

func (m *errSessionStore) Create(ctx context.Context, run *models.Session) error {
	if m.createErr != nil {
		return m.createErr
	}
	return m.mockSessionStore.Create(ctx, run)
}

func TestExecutePlan_CountRunningError(t *testing.T) {
	t.Parallel()

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &errSessionStore{countErr: fmt.Errorf("count error")},
		jobs:     &mockJobStore{},
		plans:    &mockPlanStore{},
		logger:   zerolog.Nop(),
	}

	plan := &Plan{ID: uuid.New(), OrgID: uuid.New()}
	err := svc.executePlan(context.Background(), uuid.New(), plan, models.OrgSettings{MaxConcurrentRuns: 5, AutonomyLevel: "auto_all"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "count error")
}

func TestExecutePlan_SessionCreateError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := uuid.New()

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &errSessionStore{createErr: fmt.Errorf("create error")},
		jobs:     &mockJobStore{},
		plans:    &mockPlanStore{},
		logger:   zerolog.Nop(),
	}

	plan := &Plan{
		ID:    uuid.New(),
		OrgID: orgID,
		Tasks: []Task{
			{Rank: 1, IssueIDs: []uuid.UUID{issue}, Complexity: models.PMTaskComplexitySimple, Confidence: models.PMTaskConfidenceHigh},
		},
	}

	err := svc.executePlan(context.Background(), orgID, plan, models.OrgSettings{MaxConcurrentRuns: 5, AutonomyLevel: "auto_all", DefaultAgentType: "codex"}, nil)
	require.NoError(t, err, "session create error should not fail executePlan")
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[0].Status)
}

type errJobStore struct {
	err error
}

func (m *errJobStore) Enqueue(_ context.Context, _ uuid.UUID, _, _ string, _ any, _ int, _ *string) (uuid.UUID, error) {
	return uuid.Nil, m.err
}

func TestExecutePlan_EnqueueError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := uuid.New()

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
		jobs:     &errJobStore{err: fmt.Errorf("enqueue error")},
		plans:    &mockPlanStore{},
		logger:   zerolog.Nop(),
	}

	plan := &Plan{
		ID:    uuid.New(),
		OrgID: orgID,
		Tasks: []Task{
			{Rank: 1, IssueIDs: []uuid.UUID{issue}, Complexity: models.PMTaskComplexitySimple, Confidence: models.PMTaskConfidenceHigh},
		},
	}

	err := svc.executePlan(context.Background(), orgID, plan, models.OrgSettings{MaxConcurrentRuns: 5, AutonomyLevel: "auto_all", DefaultAgentType: "codex"}, nil)
	require.NoError(t, err, "enqueue error should not fail executePlan")
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[0].Status)
}

func TestExecutePlan_RunningExceedsMax(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := uuid.New()

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{running: 10}, // running > max
		jobs:     &mockJobStore{},
		plans:    &mockPlanStore{},
		logger:   zerolog.Nop(),
	}

	plan := &Plan{
		ID:    uuid.New(),
		OrgID: orgID,
		Tasks: []Task{
			{Rank: 1, IssueIDs: []uuid.UUID{issue}, Complexity: models.PMTaskComplexitySimple, Confidence: models.PMTaskConfidenceHigh},
		},
	}

	err := svc.executePlan(context.Background(), orgID, plan, models.OrgSettings{MaxConcurrentRuns: 3, AutonomyLevel: "auto_all", DefaultAgentType: "codex"}, nil)
	require.NoError(t, err)
	require.Equal(t, models.PMTaskStatusSkippedCapacity, plan.Tasks[0].Status, "should skip when running exceeds max")
}

// --- dispatchProjectTasks error paths ---

type errSessionCreateStore struct {
	mockSessionStore
	createErr error
}

func (m *errSessionCreateStore) Create(_ context.Context, _ *models.Session) error {
	return m.createErr
}

func TestDispatchProjectTasks_SessionCreateError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	pendingTask := &models.ProjectTask{
		ID:          uuid.New(),
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Task",
		Status:      models.ProjectTaskStatusPending,
		BatchNumber: 1,
		SortOrder:   1,
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{pendingTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	svc := &Service{
		sessions:     &errSessionCreateStore{createErr: fmt.Errorf("session create fail")},
		jobs:         &mockJobStore{},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential,
	}

	settings := models.OrgSettings{AutonomyLevel: "auto_all", DefaultAgentType: "codex"}
	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, settings, uuid.New())
	require.Equal(t, 0, dispatched)
}

func TestDispatchProjectTasks_EnqueueError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	pendingTask := &models.ProjectTask{
		ID:          uuid.New(),
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Task",
		Status:      models.ProjectTaskStatusPending,
		BatchNumber: 1,
		SortOrder:   1,
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{pendingTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	svc := &Service{
		sessions:     &mockSessionStore{},
		jobs:         &errJobStore{err: fmt.Errorf("enqueue fail")},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential,
	}

	settings := models.OrgSettings{AutonomyLevel: "auto_all", DefaultAgentType: "codex"}
	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, settings, uuid.New())
	require.Equal(t, 0, dispatched)
}

// --- Analyze with specific repo ID ---

func TestAnalyze_WithSpecificRepoID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()

	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	issueID := uuid.New()
	planOutput := fmt.Sprintf(`<pm-plan>
{
  "analysis": "specific repo",
  "tasks": [{"rank":1,"issue_ids":["%s"],"title":"t","reasoning":"r","approach":"a","risk":"r","complexity":"simple","confidence":"high"}],
  "clusters": [],
  "skip": []
}
</pm-plan>`, issueID)

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{executeResult: &agent.AgentResult{Summary: planOutput}}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: repoID, Status: "active", OrgID: orgID},
		}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		plans:    &mockPlanStore{},
		jobs:     &mockJobStore{},
		logger:   zerolog.Nop(),
	}

	plan, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, &repoID, nil)
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.Equal(t, "specific repo", plan.Analysis)
}

// --- Analyze with decision log entries ---

func TestAnalyze_WritesDecisionLog(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()

	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3, AutonomyLevel: "auto_all", DefaultAgentType: "codex"})

	issueID := uuid.New()
	planOutput := fmt.Sprintf(`<pm-plan>
{
  "analysis": "with decision log",
  "tasks": [{"rank":1,"issue_ids":["%s"],"title":"t","reasoning":"r","approach":"a","risk":"r","complexity":"simple","confidence":"high"}],
  "clusters": [],
  "skip": []
}
</pm-plan>`, issueID)

	decisionLog := &gatherDecisionStoreMock{}

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{executeResult: &agent.AgentResult{Summary: planOutput}}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: repoID, Status: "active", OrgID: orgID},
		}},
		orgs:        &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:      &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions:    &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		plans:       &mockPlanStore{},
		jobs:        &mockJobStore{},
		decisionLog: decisionLog,
		logger:      zerolog.Nop(),
	}

	plan, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.Equal(t, models.PMPlanStatusCompleted, plan.Status)
	require.NotNil(t, plan.CompletedAt)
}

// --- Analyze with product context ---

func TestAnalyze_WithProductContext(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()

	settings := models.OrgSettings{
		MaxConcurrentRuns: 3,
		ProductContext: &models.ProductContext{
			Philosophy: "ship fast",
			Direction:  "growth",
			FocusAreas: []string{"auth"},
			AvoidAreas: []string{"billing"},
		},
	}
	settingsJSON, _ := json.Marshal(settings)

	issueID := uuid.New()
	planOutput := fmt.Sprintf(`<pm-plan>
{
  "analysis": "with product context",
  "tasks": [{"rank":1,"issue_ids":["%s"],"title":"t","reasoning":"r","approach":"a","risk":"r","complexity":"simple","confidence":"high"}],
  "clusters": [],
  "skip": []
}
</pm-plan>`, issueID)

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{executeResult: &agent.AgentResult{Summary: planOutput}}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: repoID, Status: "active", OrgID: orgID},
		}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		plans:    &mockPlanStore{},
		jobs:     &mockJobStore{},
		logger:   zerolog.Nop(),
	}

	plan, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, plan)
}

// --- Analyze with parse error ---

func TestAnalyze_ParsePlanError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()

	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{executeResult: &agent.AgentResult{Summary: "no plan tags"}}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: repoID, Status: "active", OrgID: orgID},
		}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		logger:   zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse plan")
}

// --- Analyze with execute error ---

func TestAnalyze_ExecuteError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()

	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{executeErr: fmt.Errorf("execution failed")}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: repoID, Status: "active", OrgID: orgID},
		}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		logger:   zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pm agent execution")
}

// --- Analyze with GitHub token ---

type mockGitHubTokenProvider struct {
	token string
	err   error
}

func (m *mockGitHubTokenProvider) GetInstallationToken(_ context.Context, _ int64) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.token, nil
}

func TestAnalyze_GitHubTokenError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()

	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		adapters: testAdapterMap(&pmInnerAdapterMock{}),
		env:      testAgentEnv(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: repoID, Status: "active", OrgID: orgID, InstallationID: 12345},
		}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		github:   &mockGitHubTokenProvider{err: fmt.Errorf("token error")},
		logger:   zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "get installation token")
}

// --- canDispatchForProject: delegated count error ---

type errDelegatedCountStore struct {
	mockProjectTaskStore
	delegatedErr bool
}

func (m *errDelegatedCountStore) CountByProjectAndStatus(_ context.Context, _, _ uuid.UUID, status models.ProjectTaskStatus) (int, error) {
	if m.delegatedErr && status == models.ProjectTaskStatusDelegated {
		return 0, fmt.Errorf("delegated count error")
	}
	if m.countByStatus != nil {
		return m.countByStatus[status], nil
	}
	return 0, nil
}

func TestCanDispatchForProject_DelegatedCountError(t *testing.T) {
	t.Parallel()

	pts := &errDelegatedCountStore{
		delegatedErr: true,
		mockProjectTaskStore: mockProjectTaskStore{
			countByStatus: map[models.ProjectTaskStatus]int{},
		},
	}
	svc := &Service{
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeSequential,
	}

	got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
	require.Equal(t, 0, got, "should return 0 when delegated count errors")
}

// --- canDispatchForProject: parallel with negative remaining ---

func TestCanDispatchForProject_ParallelOverCapacity(t *testing.T) {
	t.Parallel()

	pts := &mockProjectTaskStore{
		countByStatus: map[models.ProjectTaskStatus]int{
			models.ProjectTaskStatusRunning:   5,
			models.ProjectTaskStatusDelegated: 5,
		},
	}
	svc := newTestProjectService(nil, pts, nil)

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeParallel,
		MaxConcurrent: 3, // active (10) > max (3)
	}

	got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
	require.Equal(t, 0, got, "should return 0 when active exceeds max")
}

// --- dispatchProjectTasks: list pending error ---

type errListProjectTaskStore struct {
	mockProjectTaskStore
	listErr bool
}

func (m *errListProjectTaskStore) ListByProject(_ context.Context, _, _ uuid.UUID, _ db.ProjectTaskFilters) ([]models.ProjectTask, error) {
	if m.listErr {
		return nil, fmt.Errorf("list pending error")
	}
	return m.mockProjectTaskStore.ListByProject(context.Background(), uuid.Nil, uuid.Nil, db.ProjectTaskFilters{})
}

func TestDispatchProjectTasks_ListPendingError(t *testing.T) {
	t.Parallel()

	pts := &errListProjectTaskStore{
		listErr: true,
		mockProjectTaskStore: mockProjectTaskStore{
			countByStatus: map[models.ProjectTaskStatus]int{},
		},
	}
	svc := &Service{
		sessions:     &mockSessionStore{},
		jobs:         &mockJobStore{},
		projectTasks: pts,
		logger:       zerolog.Nop(),
	}

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeSequential,
	}

	settings := models.OrgSettings{AutonomyLevel: "auto_all", DefaultAgentType: "codex"}
	dispatched := svc.dispatchProjectTasks(context.Background(), uuid.New(), project, settings, uuid.New())
	require.Equal(t, 0, dispatched)
}
