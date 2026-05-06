package pm

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// mockAdapter is a minimal stub for agent.AgentAdapter.
type mockAdapter struct{}

func (m *mockAdapter) Name() models.AgentType { return "mock" }
func (m *mockAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	return nil, nil
}
func (m *mockAdapter) Execute(ctx context.Context, sb *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return &agent.AgentResult{}, nil
}
func (m *mockAdapter) ResumeMode() agent.SessionResumeMode { return agent.ResumeUnsupported }

// mockSandbox is a minimal stub for agent.SandboxProvider.
type mockSandbox struct{}

func (m *mockSandbox) Name() string { return "mock" }
func (m *mockSandbox) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	return &agent.Sandbox{}, nil
}
func (m *mockSandbox) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	return nil
}
func (m *mockSandbox) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}
func (m *mockSandbox) ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	return nil, nil
}
func (m *mockSandbox) WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
	return nil
}
func (m *mockSandbox) Destroy(ctx context.Context, sb *agent.Sandbox) error { return nil }
func (m *mockSandbox) IsAlive(ctx context.Context, sb *agent.Sandbox) (bool, error) {
	return true, nil
}
func (m *mockSandbox) ConnectionInfo(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return nil, nil
}
func (m *mockSandbox) Snapshot(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (m *mockSandbox) Restore(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
	return nil
}
func (m *mockSandbox) ExecStream(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
	return 0, nil
}

func TestParseProjectPlan_ValidJSON(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	summary := `{
		"cycle_analysis": "Good progress on tests",
		"progress_pct": 42,
		"current_phase": "implementation",
		"status_recommendation": "",
		"lessons_learned": ["test first"],
		"new_tasks": [
			{
				"title": "Add caching",
				"description": "Implement Redis caching",
				"approach": "Use go-redis",
				"reasoning": "Performance",
				"complexity": "moderate",
				"confidence": "high"
			}
		],
		"skipped_tasks": [
			{"description": "Refactor auth", "reason": "Not in scope"}
		]
	}`

	pp, err := parseProjectPlan(summary, projectID)
	require.NoError(t, err, "parseProjectPlan should succeed for valid JSON")
	require.Equal(t, projectID, pp.ProjectID, "should set project ID")
	require.Equal(t, "Good progress on tests", pp.CycleAnalysis)
	require.Equal(t, 42, pp.ProgressPct)
	require.Equal(t, "implementation", pp.CurrentPhase)
	require.Len(t, pp.NewTasks, 1)
	require.Equal(t, "Add caching", pp.NewTasks[0].Title)
	require.Len(t, pp.SkippedTasks, 1)
	require.Len(t, pp.LessonsLearned, 1)
}

func TestParseProjectPlan_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := parseProjectPlan("{bad json", uuid.New())
	require.Error(t, err, "parseProjectPlan should fail for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal")
}

func TestParseProjectPlan_EmptyTasks(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	summary := `{"cycle_analysis": "All done", "progress_pct": 100, "new_tasks": [], "lessons_learned": []}`

	pp, err := parseProjectPlan(summary, projectID)
	require.NoError(t, err)
	require.Equal(t, projectID, pp.ProjectID)
	require.Equal(t, 100, pp.ProgressPct)
	require.Empty(t, pp.NewTasks)
}

func TestAnalyzeProject_ProjectNotFound(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	svc := &Service{
		adapters:      testAdapterMap(&mockAdapter{}),
		env:           testAgentEnv(),
		sandbox:       &mockSandbox{},
		projects:      newMockProjectStore(), // empty store
		projectTasks:  &mockProjectTaskStore{},
		projectCycles: &mockProjectCycleStore{},
		logger:        zerolog.Nop(),
	}
	err := svc.AnalyzeProject(context.Background(), uuid.New(), projectID)
	require.Error(t, err, "AnalyzeProject should fail when project not found")
	require.Contains(t, err.Error(), "get project")
}

func TestAnalyzeProject_ProjectNotActive(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	orgID := uuid.New()
	project := models.Project{
		ID:     projectID,
		OrgID:  orgID,
		Status: models.ProjectStatusDraft,
	}
	svc := &Service{
		adapters:      testAdapterMap(&mockAdapter{}),
		env:           testAgentEnv(),
		sandbox:       &mockSandbox{},
		projects:      newMockProjectStore(project),
		projectTasks:  &mockProjectTaskStore{},
		projectCycles: &mockProjectCycleStore{},
		logger:        zerolog.Nop(),
	}
	err := svc.AnalyzeProject(context.Background(), orgID, projectID)
	require.NoError(t, err, "AnalyzeProject should succeed but skip non-active project")
}

type successOrgStore struct{}

func (m *successOrgStore) GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error) {
	return models.Organization{ID: orgID}, nil
}

type failingRepoStore struct{}

func (m *failingRepoStore) ListByOrg(ctx context.Context, orgID uuid.UUID, _ db.RepositoryFilters) ([]models.Repository, error) {
	return nil, nil
}

func (m *failingRepoStore) GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error) {
	return models.Repository{}, fmt.Errorf("repo not found")
}

func TestAnalyzeProject_RepoNotFound(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()
	project := models.Project{
		ID:           projectID,
		OrgID:        orgID,
		RepositoryID: &repoID,
		Status:       models.ProjectStatusActive,
	}
	svc := &Service{
		adapters:      testAdapterMap(&mockAdapter{}),
		env:           testAgentEnv(),
		sandbox:       &mockSandbox{},
		projects:      newMockProjectStore(project),
		projectTasks:  &mockProjectTaskStore{},
		projectCycles: &mockProjectCycleStore{},
		orgs:          &successOrgStore{},
		repos:         &failingRepoStore{},
		logger:        zerolog.Nop(),
	}
	err := svc.AnalyzeProject(context.Background(), orgID, projectID)
	require.Error(t, err, "AnalyzeProject should fail when repo not found")
	require.Contains(t, err.Error(), "get repository")
}

type failingOrgStore struct{}

func (m *failingOrgStore) GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error) {
	return models.Organization{}, fmt.Errorf("org not found")
}

func TestAnalyzeProject_OrgNotFound(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	orgID := uuid.New()
	project := models.Project{
		ID:     projectID,
		OrgID:  orgID,
		Status: models.ProjectStatusActive,
	}
	svc := &Service{
		adapters:      testAdapterMap(&mockAdapter{}),
		env:           testAgentEnv(),
		sandbox:       &mockSandbox{},
		projects:      newMockProjectStore(project),
		projectTasks:  &mockProjectTaskStore{},
		projectCycles: &mockProjectCycleStore{},
		orgs:          &failingOrgStore{},
		logger:        zerolog.Nop(),
	}
	err := svc.AnalyzeProject(context.Background(), orgID, projectID)
	require.Error(t, err, "AnalyzeProject should fail when org not found")
	require.Contains(t, err.Error(), "get org")
}

func TestAnalyzeProject_NilAdapter(t *testing.T) {
	t.Parallel()

	svc := &Service{logger: zerolog.Nop()}
	err := svc.AnalyzeProject(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "AnalyzeProject should fail when adapter is nil")
	require.Contains(t, err.Error(), "not configured")
}

func TestAnalyzeProject_NilProjectStores(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: testAdapterMap(&mockAdapter{}),
		env:      testAgentEnv(),
		sandbox:  &mockSandbox{},
		logger:   zerolog.Nop(),
	}
	err := svc.AnalyzeProject(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "AnalyzeProject should fail when project stores are nil")
	require.Contains(t, err.Error(), "project stores not configured")
}

func TestSetProjectStores(t *testing.T) {
	t.Parallel()

	svc := &Service{logger: zerolog.Nop()}
	require.Nil(t, svc.projects, "projects should be nil before SetProjectStores")

	svc.SetProjectStores(&mockProjectStore{}, &mockProjectTaskStore{}, &mockProjectCycleStore{})
	require.NotNil(t, svc.projects, "projects should be set after SetProjectStores")
	require.NotNil(t, svc.projectTasks, "projectTasks should be set after SetProjectStores")
	require.NotNil(t, svc.projectCycles, "projectCycles should be set after SetProjectStores")
}

func TestBuildProjectCycleSystemPrompt(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	project := &ProjectSummary{
		ID:    projectID.String(),
		Title: "Test Project",
		Goal:  "Build something great",
	}

	prompt := buildProjectCycleSystemPrompt(project)
	require.Contains(t, prompt, "Test Project", "prompt should contain project title")
	require.Contains(t, prompt, "Build something great", "prompt should contain project goal")
	require.Contains(t, prompt, projectID.String(), "prompt should contain project ID")
	require.Contains(t, prompt, "cycle_analysis", "prompt should describe expected JSON output")
	require.Contains(t, prompt, "new_tasks", "prompt should mention new_tasks in schema")
}
