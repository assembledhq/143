package pm

import (
	"context"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

var errNotFound = fmt.Errorf("not found")

// --- mock stores for project execution tests ---

type mockProjectStore struct {
	projects map[uuid.UUID]models.Project
	updated  []*models.Project
}

func newMockProjectStore(projects ...models.Project) *mockProjectStore {
	m := &mockProjectStore{projects: make(map[uuid.UUID]models.Project)}
	for _, p := range projects {
		m.projects[p.ID] = p
	}
	return m
}

func (m *mockProjectStore) ListByOrg(_ context.Context, _ uuid.UUID, _ db.ProjectFilters) ([]models.Project, error) {
	var out []models.Project
	for _, p := range m.projects {
		out = append(out, p)
	}
	return out, nil
}

func (m *mockProjectStore) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (models.Project, error) {
	p, ok := m.projects[id]
	if !ok {
		return models.Project{}, errNotFound
	}
	return p, nil
}

func (m *mockProjectStore) Update(_ context.Context, p *models.Project) error {
	m.updated = append(m.updated, p)
	m.projects[p.ID] = *p
	return nil
}

func (m *mockProjectStore) UpdateProgress(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (m *mockProjectStore) UpdateStatus(_ context.Context, _, id uuid.UUID, status models.ProjectStatus) error {
	p := m.projects[id]
	p.Status = models.ProjectStatus(status)
	m.projects[id] = p
	return nil
}

type mockProjectTaskStore struct {
	tasks         []*models.ProjectTask
	created       []*models.ProjectTask
	maxBatch      int
	countByStatus map[models.ProjectTaskStatus]int
}

func (m *mockProjectTaskStore) Create(_ context.Context, t *models.ProjectTask) error {
	t.ID = uuid.New()
	m.created = append(m.created, t)
	return nil
}

func (m *mockProjectTaskStore) GetByID(_ context.Context, _, taskID uuid.UUID) (models.ProjectTask, error) {
	for _, t := range m.tasks {
		if t.ID == taskID {
			return *t, nil
		}
	}
	return models.ProjectTask{}, errNotFound
}

func (m *mockProjectTaskStore) ListByProject(_ context.Context, _, _ uuid.UUID, filters db.ProjectTaskFilters) ([]models.ProjectTask, error) {
	var out []models.ProjectTask
	for _, t := range m.tasks {
		if filters.Status == "" || string(t.Status) == filters.Status {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (m *mockProjectTaskStore) Update(_ context.Context, t *models.ProjectTask) error {
	for i, existing := range m.tasks {
		if existing.ID == t.ID {
			m.tasks[i] = t
			return nil
		}
	}
	m.tasks = append(m.tasks, t)
	return nil
}

func (m *mockProjectTaskStore) CountByProjectAndStatus(_ context.Context, _, _ uuid.UUID, status models.ProjectTaskStatus) (int, error) {
	if m.countByStatus != nil {
		return m.countByStatus[status], nil
	}
	return 0, nil
}

func (m *mockProjectTaskStore) GetMaxBatchNumber(_ context.Context, _, _ uuid.UUID) (int, error) {
	return m.maxBatch, nil
}

type mockProjectCycleStore struct {
	cycles  []models.ProjectCycle
	created []*models.ProjectCycle
}

func (m *mockProjectCycleStore) Create(_ context.Context, c *models.ProjectCycle) error {
	m.created = append(m.created, c)
	return nil
}

func (m *mockProjectCycleStore) ListByProject(_ context.Context, _, _ uuid.UUID, _ int) ([]models.ProjectCycle, error) {
	return m.cycles, nil
}

// --- helper to build a Service with project stores ---

func newTestProjectService(ps *mockProjectStore, pts *mockProjectTaskStore, pcs *mockProjectCycleStore) *Service {
	svc := &Service{
		sessions:      &mockSessionStore{},
		jobs:          &mockJobStore{},
		projects:      ps,
		projectTasks:  pts,
		projectCycles: pcs,
		logger:        zerolog.Nop(),
	}
	return svc
}

// --- Tests ---

func TestSlugifyTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		title  string
		maxLen int
		want   string
	}{
		{name: "simple", title: "Fix login bug", maxLen: 40, want: "fix-login-bug"},
		{name: "special chars", title: "Add @user auth & token!", maxLen: 40, want: "add-user-auth-token"},
		{name: "multiple dashes", title: "Fix -- login -- issue", maxLen: 40, want: "fix-login-issue"},
		{name: "truncation", title: "This is a very long title that should be truncated", maxLen: 20, want: "this-is-a-very-long"},
		{name: "empty becomes task", title: "!@#$%", maxLen: 40, want: "task"},
		{name: "underscores to dashes", title: "my_cool_feature", maxLen: 40, want: "my-cool-feature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := slugifyTitle(tt.title, tt.maxLen)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateProjectBranchName(t *testing.T) {
	t.Parallel()
	projectID := uuid.MustParse("12345678-abcd-efab-cdef-1234567890ab")
	got := generateProjectBranchName(projectID, 2, 3, "Fix auth bug")
	require.Equal(t, "143/project-12345678/2-3-fix-auth-bug", got)
}

func TestTokenModeFromTaskComplexity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		complexity *string
		want       string
	}{
		{name: "nil defaults to low", complexity: nil, want: "low"},
		{name: "simple maps to low", complexity: strPtr("simple"), want: "low"},
		{name: "moderate maps to high", complexity: strPtr("moderate"), want: "high"},
		{name: "complex maps to high", complexity: strPtr("complex"), want: "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tokenModeFromTaskComplexity(tt.complexity)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCanDispatchForProject_Sequential(t *testing.T) {
	t.Parallel()

	pts := &mockProjectTaskStore{}
	svc := newTestProjectService(nil, pts, nil)

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeSequential,
	}

	t.Run("no active tasks allows 1", func(t *testing.T) { //nolint:paralleltest // subtests share mutable mock state
		pts.countByStatus = map[models.ProjectTaskStatus]int{}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 1, got)
	})

	t.Run("active tasks blocks dispatch", func(t *testing.T) { //nolint:paralleltest // subtests share mutable mock state
		pts.countByStatus = map[models.ProjectTaskStatus]int{models.ProjectTaskStatusRunning: 1}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 0, got)
	})
}

func TestCanDispatchForProject_Parallel(t *testing.T) {
	t.Parallel()

	pts := &mockProjectTaskStore{}
	svc := newTestProjectService(nil, pts, nil)

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeParallel,
		MaxConcurrent: 3,
	}

	t.Run("no active tasks returns max", func(t *testing.T) { //nolint:paralleltest // subtests share mutable mock state
		pts.countByStatus = map[models.ProjectTaskStatus]int{}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 3, got)
	})

	t.Run("some active tasks returns remaining", func(t *testing.T) { //nolint:paralleltest // subtests share mutable mock state
		pts.countByStatus = map[models.ProjectTaskStatus]int{models.ProjectTaskStatusRunning: 1, models.ProjectTaskStatusDelegated: 1}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 1, got)
	})

	t.Run("all slots used returns 0", func(t *testing.T) { //nolint:paralleltest // subtests share mutable mock state
		pts.countByStatus = map[models.ProjectTaskStatus]int{models.ProjectTaskStatusRunning: 3}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 0, got)
	})
}

func TestCanDispatchForProject_ZeroMaxConcurrent(t *testing.T) {
	t.Parallel()

	pts := &mockProjectTaskStore{countByStatus: map[models.ProjectTaskStatus]int{}}
	svc := newTestProjectService(nil, pts, nil)

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeParallel,
		MaxConcurrent: 0, // should default to 1
	}

	got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
	require.Equal(t, 1, got)
}

func TestExecuteProjectPlan_StatusRecommendationCompleted(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID: projectID, OrgID: orgID, Status: models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &mockProjectTaskStore{}
	pcs := &mockProjectCycleStore{}
	svc := newTestProjectService(ps, pts, pcs)

	pp := &ProjectPlan{
		ProjectID:            projectID,
		StatusRecommendation: "completed",
		CycleAnalysis:        "All done",
	}

	err := svc.executeProjectPlan(context.Background(), orgID, pp, models.OrgSettings{AutonomyLevel: "auto_all"}, uuid.New())
	require.NoError(t, err)

	// Project status should be updated to completed.
	require.Equal(t, models.ProjectStatusCompleted, ps.projects[projectID].Status)
	// A cycle should be recorded.
	require.Len(t, pcs.created, 1)
	// No tasks should be created.
	require.Len(t, pts.created, 0)
}

func TestExecuteProjectPlan_StatusRecommendationNeedsHumanReview(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID: projectID, OrgID: orgID, Status: models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &mockProjectTaskStore{}
	pcs := &mockProjectCycleStore{}
	svc := newTestProjectService(ps, pts, pcs)

	pp := &ProjectPlan{
		ProjectID:            projectID,
		StatusRecommendation: "needs_human_review",
		CycleAnalysis:        "Needs review",
	}

	err := svc.executeProjectPlan(context.Background(), orgID, pp, models.OrgSettings{AutonomyLevel: "auto_all"}, uuid.New())
	require.NoError(t, err)

	// Project should remain active — needs_human_review does not change status.
	require.Equal(t, models.ProjectStatusActive, ps.projects[projectID].Status)
	require.Len(t, pcs.created, 1)
}

func TestExecuteProjectPlan_CreatesTasksAndDispatches(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID: projectID, OrgID: orgID, Status: models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &mockProjectTaskStore{maxBatch: 1}
	pcs := &mockProjectCycleStore{}
	svc := newTestProjectService(ps, pts, pcs)

	pp := &ProjectPlan{
		ProjectID:     projectID,
		CycleAnalysis: "Creating tasks",
		NewTasks: []ProjectTaskSpec{
			{Title: "Task A", Description: "Do A", Approach: "approach-a", Reasoning: "reason-a", Complexity: "simple", Confidence: "high"},
			{Title: "Task B", Description: "Do B"},
		},
	}

	settings := models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "codex",
	}

	err := svc.executeProjectPlan(context.Background(), orgID, pp, settings, uuid.New())
	require.NoError(t, err)

	// Both tasks should be created.
	require.Len(t, pts.created, 2)
	require.Equal(t, "Task A", pts.created[0].Title)
	require.Equal(t, "Task B", pts.created[1].Title)
	require.Equal(t, 2, pts.created[0].BatchNumber, "batch number should be maxBatch+1")

	// A cycle should be recorded.
	require.Len(t, pcs.created, 1)
	require.Equal(t, 2, pcs.created[0].TasksCreatedThisCycle)
}

func TestExecuteProjectPlan_ManualAutonomyDoesNotDispatch(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID: projectID, OrgID: orgID, Status: models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &mockProjectTaskStore{maxBatch: 0}
	pcs := &mockProjectCycleStore{}
	svc := newTestProjectService(ps, pts, pcs)

	pp := &ProjectPlan{
		ProjectID:     projectID,
		CycleAnalysis: "Manual mode",
		NewTasks: []ProjectTaskSpec{
			{Title: "Task A"},
		},
	}

	settings := models.OrgSettings{AutonomyLevel: "manual"}
	err := svc.executeProjectPlan(context.Background(), orgID, pp, settings, uuid.New())
	require.NoError(t, err)

	// Tasks should still be created.
	require.Len(t, pts.created, 1)

	// But no agent runs should be created (manual mode).
	runStore := svc.sessions.(*mockSessionStore)
	require.Len(t, runStore.created, 0)
}

func TestExecuteProjectPlan_UpdatesLessonsLearned(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID: projectID, OrgID: orgID, Status: models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &mockProjectTaskStore{maxBatch: 0}
	pcs := &mockProjectCycleStore{}
	svc := newTestProjectService(ps, pts, pcs)

	pp := &ProjectPlan{
		ProjectID:      projectID,
		CycleAnalysis:  "Learned some lessons",
		LessonsLearned: []string{"lesson-1", "lesson-2"},
	}

	settings := models.OrgSettings{AutonomyLevel: "manual"}
	err := svc.executeProjectPlan(context.Background(), orgID, pp, settings, uuid.New())
	require.NoError(t, err)

	// Project should have lessons learned updated.
	require.Len(t, ps.updated, 1)
	require.Contains(t, ps.updated[0].LessonsLearned, "lesson-1")
	require.Contains(t, ps.updated[0].LessonsLearned, "lesson-2")
}

func TestExecuteProjectPlan_UpdatesCurrentPhase(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	ps := newMockProjectStore(models.Project{
		ID: projectID, OrgID: orgID, Status: models.ProjectStatusActive,
		ExecutionMode: models.ProjectExecModeSequential,
	})
	pts := &mockProjectTaskStore{maxBatch: 0}
	pcs := &mockProjectCycleStore{}
	svc := newTestProjectService(ps, pts, pcs)

	pp := &ProjectPlan{
		ProjectID:     projectID,
		CycleAnalysis: "Phase update",
		CurrentPhase:  "implementation",
	}

	settings := models.OrgSettings{AutonomyLevel: "manual"}
	err := svc.executeProjectPlan(context.Background(), orgID, pp, settings, uuid.New())
	require.NoError(t, err)

	p := ps.projects[projectID]
	require.NotNil(t, p.CurrentPhase)
	require.Equal(t, "implementation", *p.CurrentPhase)
}

func TestRecordProjectCycle_IncrementsNumber(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	planID := uuid.New()

	pcs := &mockProjectCycleStore{
		cycles: []models.ProjectCycle{{CycleNumber: 5}},
	}
	svc := newTestProjectService(nil, nil, pcs)

	pp := &ProjectPlan{
		ProjectID:     projectID,
		CycleAnalysis: "Analysis",
		ProgressPct:   42,
	}

	svc.recordProjectCycle(context.Background(), orgID, pp, planID, 3, 2)

	require.Len(t, pcs.created, 1)
	require.Equal(t, 6, pcs.created[0].CycleNumber)
	require.Equal(t, 3, pcs.created[0].TasksCreatedThisCycle)
	require.NotNil(t, pcs.created[0].ProgressPct)
	require.Equal(t, 42, *pcs.created[0].ProgressPct)
}

func TestDispatchProjectTasks_SkipsLowConfidenceUnlessAutoAll(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	lowConf := "low"

	pendingTask := &models.ProjectTask{
		ID:          uuid.New(),
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Low conf task",
		Status:      models.ProjectTaskStatusPending,
		Confidence:  &lowConf,
		BatchNumber: 1,
		SortOrder:   1,
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{pendingTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	svc := newTestProjectService(nil, pts, &mockProjectCycleStore{})

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeSequential,
	}

	// auto_safe should skip low-confidence tasks
	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, models.OrgSettings{
		AutonomyLevel:    "auto_safe",
		DefaultAgentType: "codex",
	}, uuid.New())
	require.Equal(t, 0, dispatched)

	// auto_all should dispatch them
	dispatched = svc.dispatchProjectTasks(context.Background(), orgID, project, models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "codex",
	}, uuid.New())
	require.Equal(t, 1, dispatched)
}

func TestCheckDependenciesStatus(t *testing.T) {
	t.Parallel()

	depA := uuid.New()
	depB := uuid.New()
	depC := uuid.New()

	tests := []struct {
		name      string
		dependsOn []uuid.UUID
		statuses  map[uuid.UUID]models.ProjectTaskStatus
		want      depStatus
	}{
		{
			name:      "no dependencies is ready",
			dependsOn: nil,
			statuses:  nil,
			want:      depStatusReady,
		},
		{
			name:      "all completed is ready",
			dependsOn: []uuid.UUID{depA, depB},
			statuses: map[uuid.UUID]models.ProjectTaskStatus{
				depA: models.ProjectTaskStatusCompleted,
				depB: models.ProjectTaskStatusCompleted,
			},
			want: depStatusReady,
		},
		{
			name:      "one pending is waiting",
			dependsOn: []uuid.UUID{depA, depB},
			statuses: map[uuid.UUID]models.ProjectTaskStatus{
				depA: models.ProjectTaskStatusCompleted,
				depB: models.ProjectTaskStatusRunning,
			},
			want: depStatusWaiting,
		},
		{
			name:      "one failed is blocked",
			dependsOn: []uuid.UUID{depA, depB},
			statuses: map[uuid.UUID]models.ProjectTaskStatus{
				depA: models.ProjectTaskStatusCompleted,
				depB: models.ProjectTaskStatusFailed,
			},
			want: depStatusBlocked,
		},
		{
			name:      "cancelled dep is blocked",
			dependsOn: []uuid.UUID{depA},
			statuses: map[uuid.UUID]models.ProjectTaskStatus{
				depA: models.ProjectTaskStatusCancelled,
			},
			want: depStatusBlocked,
		},
		{
			name:      "blocked dep propagates blocked (transitive)",
			dependsOn: []uuid.UUID{depA},
			statuses: map[uuid.UUID]models.ProjectTaskStatus{
				depA: models.ProjectTaskStatusBlocked,
			},
			want: depStatusBlocked,
		},
		{
			name:      "unknown dep is waiting",
			dependsOn: []uuid.UUID{depC},
			statuses:  map[uuid.UUID]models.ProjectTaskStatus{},
			want:      depStatusWaiting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := checkDependenciesStatus(tt.dependsOn, tt.statuses)
			require.Equal(t, tt.want, got, "checkDependenciesStatus should return expected status")
		})
	}
}

func TestCanDispatchForProject_DependencyGraph(t *testing.T) {
	t.Parallel()

	pts := &mockProjectTaskStore{countByStatus: map[models.ProjectTaskStatus]int{}}
	svc := newTestProjectService(nil, pts, nil)

	project := &models.Project{
		ID:            uuid.New(),
		ExecutionMode: models.ProjectExecModeDependencyGraph,
		MaxConcurrent: 3,
	}

	t.Run("no active tasks returns max concurrent", func(t *testing.T) { //nolint:paralleltest
		pts.countByStatus = map[models.ProjectTaskStatus]int{}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 3, got, "should allow max_concurrent when no active tasks")
	})

	t.Run("some active returns remaining", func(t *testing.T) { //nolint:paralleltest
		pts.countByStatus = map[models.ProjectTaskStatus]int{models.ProjectTaskStatusRunning: 2}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 1, got, "should return remaining slots")
	})

	t.Run("all slots used returns 0", func(t *testing.T) { //nolint:paralleltest
		pts.countByStatus = map[models.ProjectTaskStatus]int{models.ProjectTaskStatusRunning: 3}
		got := svc.canDispatchForProject(context.Background(), uuid.New(), project)
		require.Equal(t, 0, got, "should return 0 when all slots are used")
	})
}

func TestDispatchProjectTasks_DependencyGraph(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	depTask := &models.ProjectTask{
		ID:        uuid.New(),
		ProjectID: projectID,
		OrgID:     orgID,
		Title:     "Setup DB",
		Status:    models.ProjectTaskStatusCompleted,
	}

	readyTask := &models.ProjectTask{
		ID:        uuid.New(),
		ProjectID: projectID,
		OrgID:     orgID,
		Title:     "Build API",
		Status:    models.ProjectTaskStatusPending,
		DependsOn: []uuid.UUID{depTask.ID},
	}

	blockedTask := &models.ProjectTask{
		ID:        uuid.New(),
		ProjectID: projectID,
		OrgID:     orgID,
		Title:     "Deploy",
		Status:    models.ProjectTaskStatusPending,
		DependsOn: []uuid.UUID{uuid.New()}, // depends on unknown task — treated as waiting
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{depTask, readyTask, blockedTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	svc := newTestProjectService(nil, pts, &mockProjectCycleStore{})

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeDependencyGraph,
		MaxConcurrent: 5,
	}

	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "codex",
	}, uuid.New())

	require.Equal(t, 1, dispatched, "should only dispatch the task with satisfied dependencies")

	// Look up the task via the mock store — dispatchProjectTasks works on
	// copies returned by ListByProject, so the original readyTask pointer
	// isn't mutated; the mock's Update replaces the entry in pts.tasks.
	var found *models.ProjectTask
	for _, task := range pts.tasks {
		if task.ID == readyTask.ID {
			found = task
			break
		}
	}
	require.NotNil(t, found, "ready task should still be in store")
	require.Equal(t, models.ProjectTaskStatusDelegated, found.Status, "ready task should be delegated")
}

func TestDispatchProjectTasks_DependencyGraph_BlockedPath(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	failedDep := &models.ProjectTask{
		ID:        uuid.New(),
		ProjectID: projectID,
		OrgID:     orgID,
		Title:     "Broken Step",
		Status:    models.ProjectTaskStatusFailed,
	}

	blockedTask := &models.ProjectTask{
		ID:        uuid.New(),
		ProjectID: projectID,
		OrgID:     orgID,
		Title:     "Depends on Broken",
		Status:    models.ProjectTaskStatusPending,
		DependsOn: []uuid.UUID{failedDep.ID},
	}

	pts := &mockProjectTaskStore{
		tasks:         []*models.ProjectTask{failedDep, blockedTask},
		countByStatus: map[models.ProjectTaskStatus]int{},
	}
	svc := newTestProjectService(nil, pts, &mockProjectCycleStore{})

	project := &models.Project{
		ID:            projectID,
		ExecutionMode: models.ProjectExecModeDependencyGraph,
		MaxConcurrent: 5,
	}

	dispatched := svc.dispatchProjectTasks(context.Background(), orgID, project, models.OrgSettings{
		AutonomyLevel:    "auto_all",
		DefaultAgentType: "codex",
	}, uuid.New())

	require.Equal(t, 0, dispatched, "should not dispatch any tasks when dependency failed")

	// The task with a failed dependency should be marked as blocked.
	var found *models.ProjectTask
	for _, task := range pts.tasks {
		if task.ID == blockedTask.ID {
			found = task
			break
		}
	}
	require.NotNil(t, found, "blocked task should still be in store")
	require.Equal(t, models.ProjectTaskStatusBlocked, found.Status, "task with failed dependency should be marked blocked")
}
