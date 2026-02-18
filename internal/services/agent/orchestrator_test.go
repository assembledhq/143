package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// --- Mock implementations ---

// mockSandboxProvider implements agent.SandboxProvider.
type mockSandboxProvider struct {
	createFn     func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error)
	cloneRepoFn  func(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error
	destroyFn    func(ctx context.Context, sb *agent.Sandbox) error
	destroyCalls int
	mu           sync.Mutex
}

func (m *mockSandboxProvider) Name() string { return "mock" }

func (m *mockSandboxProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	if m.createFn != nil {
		return m.createFn(ctx, cfg)
	}
	return &agent.Sandbox{ID: "sandbox-1", Provider: "mock", WorkDir: "/workspace"}, nil
}

func (m *mockSandboxProvider) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	if m.cloneRepoFn != nil {
		return m.cloneRepoFn(ctx, sb, repoURL, branch, token)
	}
	return nil
}

func (m *mockSandboxProvider) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}

func (m *mockSandboxProvider) ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	return nil, nil
}

func (m *mockSandboxProvider) WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
	return nil
}

func (m *mockSandboxProvider) Destroy(ctx context.Context, sb *agent.Sandbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.destroyCalls++
	if m.destroyFn != nil {
		return m.destroyFn(ctx, sb)
	}
	return nil
}

func (m *mockSandboxProvider) ConnectionInfo(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return &agent.SandboxConnectionInfo{}, nil
}

func (m *mockSandboxProvider) getDestroyCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.destroyCalls
}

// mockAgentAdapter implements agent.AgentAdapter.
type mockAgentAdapter struct {
	name      string
	executeFn func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error)
}

func (m *mockAgentAdapter) Name() string { return m.name }

func (m *mockAgentAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	return &agent.AgentPrompt{
		SystemPrompt: "test system prompt",
		UserPrompt:   "test user prompt",
		MaxTokens:    50000,
	}, nil
}

func (m *mockAgentAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, sandbox, prompt, logCh)
	}
	return &agent.AgentResult{
		Diff:            "--- a/file.go\n+++ b/file.go",
		Summary:         "Fixed the bug",
		ConfidenceScore: 0.9,
		ExitCode:        0,
	}, nil
}

// mockGitHubTokenProvider implements agent.GitHubTokenProvider.
type mockGitHubTokenProvider struct {
	token string
	err   error
}

func (m *mockGitHubTokenProvider) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.token, nil
}

// mockAgentRunStore implements agent.AgentRunStore.
type mockAgentRunStore struct {
	mu              sync.Mutex
	countRunning    int
	statusUpdates   []string
	resultUpdates   []resultUpdate
	countRunningErr error
}

type resultUpdate struct {
	status string
	result *models.AgentRunResult
}

func (m *mockAgentRunStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusUpdates = append(m.statusUpdates, status)
	return nil
}

func (m *mockAgentRunStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.AgentRunResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resultUpdates = append(m.resultUpdates, resultUpdate{status: status, result: result})
	return nil
}

func (m *mockAgentRunStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.countRunning, m.countRunningErr
}

func (m *mockAgentRunStore) getStatusUpdates() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.statusUpdates))
	copy(out, m.statusUpdates)
	return out
}

func (m *mockAgentRunStore) getResultUpdates() []resultUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]resultUpdate, len(m.resultUpdates))
	copy(out, m.resultUpdates)
	return out
}

// mockAgentRunLogStore implements agent.AgentRunLogStore.
type mockAgentRunLogStore struct {
	mu    sync.Mutex
	logs  []models.AgentRunLog
	count int
}

func (m *mockAgentRunLogStore) Create(ctx context.Context, log *models.AgentRunLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, *log)
	m.count++
	return nil
}

func (m *mockAgentRunLogStore) getCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// mockAgentRunQuestionStore implements agent.AgentRunQuestionStore.
type mockAgentRunQuestionStore struct {
	mu        sync.Mutex
	questions []models.AgentRunQuestion
}

func (m *mockAgentRunQuestionStore) Create(ctx context.Context, q *models.AgentRunQuestion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions = append(m.questions, *q)
	return nil
}

func (m *mockAgentRunQuestionStore) getQuestions() []models.AgentRunQuestion {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.AgentRunQuestion, len(m.questions))
	copy(out, m.questions)
	return out
}

// mockIssueStore implements agent.IssueStore.
type mockIssueStore struct {
	issue models.Issue
	err   error
}

func (m *mockIssueStore) GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
	if m.err != nil {
		return models.Issue{}, m.err
	}
	return m.issue, nil
}

// mockRepositoryStore implements agent.RepositoryStore.
type mockRepositoryStore struct {
	repo models.Repository
	err  error
}

func (m *mockRepositoryStore) GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error) {
	if m.err != nil {
		return models.Repository{}, m.err
	}
	return m.repo, nil
}

// mockJobStore implements agent.JobStore.
type mockJobStore struct {
	mu       sync.Mutex
	enqueued []string // job types
}

func (m *mockJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueued = append(m.enqueued, jobType)
	return uuid.New(), nil
}

func (m *mockJobStore) getEnqueued() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.enqueued))
	copy(out, m.enqueued)
	return out
}

// --- Helpers ---

func testOrg() uuid.UUID {
	return uuid.MustParse("00000000-0000-0000-0000-000000000001")
}

func testIssue(orgID uuid.UUID) models.Issue {
	repoID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	desc := "Test issue description"
	return models.Issue{
		ID:           uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		OrgID:        orgID,
		ExternalID:   "SENTRY-123",
		Source:       "sentry",
		RepositoryID: &repoID,
		Title:        "NullPointerException in handler",
		Description:  &desc,
		Status:       "open",
		Severity:     "high",
		Fingerprint:  "fp-123",
		CreatedAt:    time.Now(),
	}
}

func testRepo(orgID uuid.UUID) models.Repository {
	return models.Repository{
		ID:             uuid.MustParse("00000000-0000-0000-0000-000000000099"),
		OrgID:          orgID,
		FullName:       "acme/backend",
		DefaultBranch:  "main",
		CloneURL:       "https://github.com/acme/backend.git",
		InstallationID: 12345,
		Status:         "active",
		Settings:       json.RawMessage(`{}`),
	}
}

func testRun(orgID, issueID uuid.UUID) *models.AgentRun {
	return &models.AgentRun{
		ID:        uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		IssueID:   issueID,
		OrgID:     orgID,
		AgentType: "claude_code",
		Status:    "pending",
		TokenMode: "low",
	}
}

type testDeps struct {
	provider  *mockSandboxProvider
	adapter   *mockAgentAdapter
	agentRuns *mockAgentRunStore
	issues    *mockIssueStore
	repos     *mockRepositoryStore
	logs      *mockAgentRunLogStore
	questions *mockAgentRunQuestionStore
	jobs      *mockJobStore
	github    *mockGitHubTokenProvider
}

func defaultDeps() testDeps {
	orgID := testOrg()
	return testDeps{
		provider:  &mockSandboxProvider{},
		adapter:   &mockAgentAdapter{name: "claude_code"},
		agentRuns: &mockAgentRunStore{countRunning: 0},
		issues:    &mockIssueStore{issue: testIssue(orgID)},
		repos:     &mockRepositoryStore{repo: testRepo(orgID)},
		logs:      &mockAgentRunLogStore{},
		questions: &mockAgentRunQuestionStore{},
		jobs:      &mockJobStore{},
		github:    &mockGitHubTokenProvider{token: "ghp_test123"},
	}
}

func buildOrchestrator(d testDeps) *agent.Orchestrator {
	return agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:          d.provider,
		Adapters:          map[string]agent.AgentAdapter{d.adapter.Name(): d.adapter},
		AgentRuns:         d.agentRuns,
		AgentRunLogs:      d.logs,
		AgentRunQuestions: d.questions,
		Issues:            d.issues,
		Repositories:      d.repos,
		Jobs:              d.jobs,
		GitHub:            d.github,
		Logger:            zerolog.Nop(),
		MaxConcurrent:     3,
	})
}

// --- Tests ---

func TestRunAgent_SuccessfulRun(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "info", Message: "starting analysis"}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "tool_use", Message: "reading file"}
		return &agent.AgentResult{
			Diff:                "--- a/main.go\n+++ b/main.go",
			Summary:             "Fixed null pointer",
			ConfidenceScore:     0.9,
			ConfidenceReasoning: "Simple fix",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Status should have been set to "running".
	statuses := d.agentRuns.getStatusUpdates()
	require.Contains(t, statuses, "running")

	// Result should be "completed" with high confidence.
	results := d.agentRuns.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)
	require.NotNil(t, results[0].result.ConfidenceScore)
	require.InDelta(t, 0.9, *results[0].result.ConfidenceScore, 0.01)

	// Validate job should be enqueued.
	require.Contains(t, d.jobs.getEnqueued(), "validate")

	// Logs should be persisted.
	require.GreaterOrEqual(t, d.logs.getCount(), 2)

	// Sandbox should be destroyed.
	require.Equal(t, 1, d.provider.getDestroyCalls())
}

func TestRunAgent_FailedExecution(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return nil, errors.New("agent crashed: OOM killed")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "execute agent")

	// Run should be marked as failed.
	results := d.agentRuns.getResultUpdates()
	require.GreaterOrEqual(t, len(results), 1)
	// The last result update should be from failRun (before enqueue).
	// The first result update is from failRun, setting status to "failed".
	foundFailed := false
	for _, r := range results {
		if r.status == "failed" {
			foundFailed = true
		}
	}
	require.True(t, foundFailed)

	// analyze_failure job should be enqueued.
	require.Contains(t, d.jobs.getEnqueued(), "analyze_failure")

	// Sandbox should be destroyed.
	require.Equal(t, 1, d.provider.getDestroyCalls())
}

func TestRunAgent_LowConfidence(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Diff:            "--- a/fix.go\n+++ b/fix.go",
			Summary:         "Attempted fix but unsure",
			ConfidenceScore: 0.3,
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Result should be "needs_human_guidance".
	results := d.agentRuns.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "needs_human_guidance", results[0].status)

	// No validate job should be enqueued.
	for _, jt := range d.jobs.getEnqueued() {
		require.NotEqual(t, "validate", jt)
	}
}

func TestRunAgent_MediumConfidence(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Diff:            "--- a/fix.go\n+++ b/fix.go",
			Summary:         "Fix applied",
			ConfidenceScore: 0.65,
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Medium confidence (>= 0.5) proceeds as "completed".
	results := d.agentRuns.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)

	// Validate job should be enqueued.
	require.Contains(t, d.jobs.getEnqueued(), "validate")
}

func TestRunAgent_ConcurrencyLimit(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.agentRuns.countRunning = 3 // At the limit.

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "concurrency limit reached")

	// Status should NOT have been updated to "running".
	statuses := d.agentRuns.getStatusUpdates()
	for _, s := range statuses {
		require.NotEqual(t, "running", s)
	}

	// Sandbox should never have been created, so destroy shouldn't be called.
	require.Equal(t, 0, d.provider.getDestroyCalls())
}

func TestRunAgent_SandboxCleanupOnCreateFailure(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.createFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		return nil, errors.New("docker daemon not running")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "create sandbox")

	// Destroy should not be called since Create failed (no sandbox to destroy).
	require.Equal(t, 0, d.provider.getDestroyCalls())

	// Run should be marked as failed.
	results := d.agentRuns.getResultUpdates()
	foundFailed := false
	for _, r := range results {
		if r.status == "failed" {
			foundFailed = true
		}
	}
	require.True(t, foundFailed)
}

func TestRunAgent_SandboxCleanupOnCloneFailure(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.cloneRepoFn = func(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
		return errors.New("auth failed")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "clone repo")

	// Sandbox was created so Destroy must be called.
	require.Equal(t, 1, d.provider.getDestroyCalls())
}

func TestRunAgent_LogStreamingWithQuestion(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "info",
			Message:   "analyzing code",
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "question",
			Message:   "Should I refactor this function too?",
			Metadata: map[string]interface{}{
				"options":      []interface{}{"yes", "no"},
				"blocks_phase": "implementation",
			},
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "info",
			Message:   "completed",
		}
		return &agent.AgentResult{
			Diff:            "--- a/fix.go\n+++ b/fix.go",
			Summary:         "Fixed it",
			ConfidenceScore: 0.85,
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// All 3 log entries should be persisted.
	require.Equal(t, 3, d.logs.getCount())

	// 1 question should have been created.
	questions := d.questions.getQuestions()
	require.Len(t, questions, 1)
	require.Equal(t, "Should I refactor this function too?", questions[0].QuestionText)
	require.Equal(t, "pending", questions[0].Status)

	// Status should have been set to "awaiting_input" for the question.
	statuses := d.agentRuns.getStatusUpdates()
	require.Contains(t, statuses, "awaiting_input")
}

func TestRunAgent_UnknownAgentType(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = "unknown_agent"

	d := defaultDeps()
	orch := buildOrchestrator(d)

	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown agent type")

	// Should be marked as failed.
	results := d.agentRuns.getResultUpdates()
	foundFailed := false
	for _, r := range results {
		if r.status == "failed" {
			foundFailed = true
		}
	}
	require.True(t, foundFailed)
}

func TestRunAgent_ExactConfidenceThreshold(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Diff:            "--- a/fix.go\n+++ b/fix.go",
			Summary:         "Fix at exact threshold",
			ConfidenceScore: 0.5, // Exactly at the threshold.
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Score == 0.5 should proceed (>= threshold).
	results := d.agentRuns.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)
	require.Contains(t, d.jobs.getEnqueued(), "validate")
}

func TestRunAgent_IssueWithoutRepository(t *testing.T) {
	t.Parallel()

	orgID := testOrg()

	// Issue with no repository_id.
	issue := testIssue(orgID)
	issue.RepositoryID = nil

	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.issues.issue = issue

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Should complete without cloning.
	results := d.agentRuns.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)
}
