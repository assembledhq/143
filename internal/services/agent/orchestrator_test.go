package agent_test

import (
	"bytes"
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
	"github.com/assembledhq/143/internal/testutil"
)

// --- Mock implementations ---

// mockAgentAdapter implements agent.AgentAdapter.
type mockAgentAdapter struct {
	name      models.AgentType
	executeFn func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error)
}

func (m *mockAgentAdapter) Name() models.AgentType { return m.name }

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

type capturingAdapter struct {
	name     models.AgentType
	captured *agent.AgentInput
}

func (c *capturingAdapter) Name() models.AgentType { return c.name }

func (c *capturingAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	c.captured = input
	return &agent.AgentPrompt{
		SystemPrompt: "test system prompt",
		UserPrompt:   "test user prompt",
		MaxTokens:    50000,
	}, nil
}

func (c *capturingAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	return &agent.AgentResult{
		Summary:         "ok",
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

// mockCodexAuthProvider implements agent.CodexAuthProvider.
type mockCodexAuthProvider struct {
	cfg *models.OpenAIChatGPTConfig
	err error
}

func (m *mockCodexAuthProvider) GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.cfg, nil
}

type mockCredentialProvider struct {
	byProvider map[models.ProviderName]*models.DecryptedCredential
	err        error
}

func (m *mockCredentialProvider) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.byProvider == nil {
		return nil, nil
	}
	return m.byProvider[provider], nil
}

// mockSessionStore implements agent.SessionStore.
type mockSessionStore struct {
	mu              sync.Mutex
	countRunning    int
	statusUpdates   []string
	resultUpdates   []resultUpdate
	turnUpdates     []turnUpdate
	failureUpdates  []failureUpdate
	countRunningErr error
}

type failureUpdate struct {
	explanation  string
	category     string
	nextSteps    []string
	retryAdvised bool
}

type resultUpdate struct {
	status string
	result *models.SessionResult
}

type turnUpdate struct {
	turn           int
	result         *models.SessionResult
	agentSessionID string
	snapshotKey    string
}

func (m *mockSessionStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusUpdates = append(m.statusUpdates, status)
	return nil
}

func (m *mockSessionStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resultUpdates = append(m.resultUpdates, resultUpdate{status: status, result: result})
	return nil
}

func (m *mockSessionStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.countRunning, m.countRunningErr
}

func (m *mockSessionStore) UpdateTurnComplete(ctx context.Context, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turnUpdates = append(m.turnUpdates, turnUpdate{
		turn:           turn,
		result:         result,
		agentSessionID: agentSessionID,
		snapshotKey:    snapshotKey,
	})
	return nil
}

func (m *mockSessionStore) UpdateSnapshotInfo(ctx context.Context, orgID, sessionID uuid.UUID, agentSessionID, snapshotKey string) error {
	return nil
}

func (m *mockSessionStore) UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error {
	return nil
}

func (m *mockSessionStore) UpdateWorkingBranch(ctx context.Context, orgID, sessionID uuid.UUID, branch string) error {
	return nil
}

func (m *mockSessionStore) UpdateTitle(ctx context.Context, orgID, sessionID uuid.UUID, title string) error {
	return nil
}

func (m *mockSessionStore) UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failureUpdates = append(m.failureUpdates, failureUpdate{
		explanation:  explanation,
		category:     category,
		nextSteps:    nextSteps,
		retryAdvised: retryAdvised,
	})
	return nil
}

func (m *mockSessionStore) GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	return models.Session{}, nil
}

func (m *mockSessionStore) getStatusUpdates() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.statusUpdates))
	copy(out, m.statusUpdates)
	return out
}

func (m *mockSessionStore) getResultUpdates() []resultUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]resultUpdate, len(m.resultUpdates))
	copy(out, m.resultUpdates)
	return out
}

func (m *mockSessionStore) getTurnUpdates() []turnUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]turnUpdate, len(m.turnUpdates))
	copy(out, m.turnUpdates)
	return out
}

// mockSessionLogStore implements agent.SessionLogStore.
type mockSessionLogStore struct {
	mu    sync.Mutex
	logs  []models.SessionLog
	count int
}

func (m *mockSessionLogStore) Create(ctx context.Context, log *models.SessionLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, *log)
	m.count++
	return nil
}

func (m *mockSessionLogStore) getCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// mockSessionQuestionStore implements agent.SessionQuestionStore.
type mockSessionQuestionStore struct {
	mu        sync.Mutex
	questions []models.SessionQuestion
}

func (m *mockSessionQuestionStore) Create(ctx context.Context, q *models.SessionQuestion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions = append(m.questions, *q)
	return nil
}

func (m *mockSessionQuestionStore) getQuestions() []models.SessionQuestion {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.SessionQuestion, len(m.questions))
	copy(out, m.questions)
	return out
}

type mockSessionMessageStore struct {
	mu       sync.Mutex
	messages []models.SessionMessage
}

func (m *mockSessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg.ID == 0 {
		msg.ID = int64(len(m.messages) + 1)
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	m.messages = append(m.messages, *msg)
	return nil
}

func (m *mockSessionMessageStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.SessionMessage
	for _, msg := range m.messages {
		if msg.OrgID == orgID && msg.SessionID == sessionID {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *mockSessionMessageStore) getMessages() []models.SessionMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.SessionMessage, len(m.messages))
	copy(out, m.messages)
	return out
}

type mockSnapshotStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *mockSnapshotStore) Save(ctx context.Context, key string, reader io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	m.data[key] = payload
	return nil
}

func (m *mockSnapshotStore) Load(ctx context.Context, key string, writer io.Writer) error {
	m.mu.Lock()
	payload := append([]byte(nil), m.data[key]...)
	m.mu.Unlock()
	_, err := writer.Write(payload)
	return err
}

func (m *mockSnapshotStore) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// mockDecisionLogStore implements agent.DecisionLogStore.
type mockDecisionLogStore struct {
	mu       sync.Mutex
	outcomes []models.PMDecisionOutcome
}

func (m *mockDecisionLogStore) UpdateOutcome(ctx context.Context, orgID, planID, issueID uuid.UUID, outcome models.PMDecisionOutcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes = append(m.outcomes, outcome)
	return nil
}

type mockProjectTaskUpdater struct {
	mu       sync.Mutex
	statuses []string
}

func (m *mockProjectTaskUpdater) OnSessionComplete(ctx context.Context, run *models.Session, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses = append(m.statuses, status)
	return nil
}

func (m *mockProjectTaskUpdater) getStatuses() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.statuses))
	copy(out, m.statuses)
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

// mockOrgStore implements agent.OrgStore.
type mockOrgStore struct {
	org models.Organization
	err error
}

func (m *mockOrgStore) GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error) {
	if m.err != nil {
		return models.Organization{}, m.err
	}
	return m.org, nil
}

// mockJobStore implements agent.JobStore.
type mockJobStore struct {
	mu       sync.Mutex
	enqueued []string // job types
	payloads map[string]any
}

func (m *mockJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.payloads == nil {
		m.payloads = make(map[string]any)
	}
	m.enqueued = append(m.enqueued, jobType)
	m.payloads[jobType] = payload
	return uuid.New(), nil
}

func (m *mockJobStore) getEnqueued() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.enqueued))
	copy(out, m.enqueued)
	return out
}

func (m *mockJobStore) getPayload(jobType string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.payloads[jobType]
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
		Source:       models.IssueSourceSentry,
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

func testRun(orgID, issueID uuid.UUID) *models.Session {
	repoID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	return &models.Session{
		ID:           uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		IssueID:      issueID,
		OrgID:        orgID,
		AgentType:    models.AgentTypeClaudeCode,
		Status:       "pending",
		TokenMode:    "low",
		RepositoryID: &repoID,
	}
}

func strPtr(s string) *string {
	return &s
}

type testDeps struct {
	provider  *testutil.MockSandboxProvider
	adapter   *mockAgentAdapter
	sessions  *mockSessionStore
	projects  *mockProjectTaskUpdater
	issues    *mockIssueStore
	repos     *mockRepositoryStore
	logs      *mockSessionLogStore
	questions *mockSessionQuestionStore
	messages  *mockSessionMessageStore
	decisions *mockDecisionLogStore
	jobs      *mockJobStore
	github    *mockGitHubTokenProvider
	codexAuth agent.CodexAuthProvider
	creds     *mockCredentialProvider
	snapshots *mockSnapshotStore
	cancels   *agent.CancelRegistry
}

func defaultDeps() testDeps {
	orgID := testOrg()
	return testDeps{
		provider:  testutil.NewMockSandboxProvider(),
		adapter:   &mockAgentAdapter{name: models.AgentTypeClaudeCode},
		sessions:  &mockSessionStore{countRunning: 0},
		projects:  &mockProjectTaskUpdater{},
		issues:    &mockIssueStore{issue: testIssue(orgID)},
		repos:     &mockRepositoryStore{repo: testRepo(orgID)},
		logs:      &mockSessionLogStore{},
		questions: &mockSessionQuestionStore{},
		messages:  &mockSessionMessageStore{},
		decisions: &mockDecisionLogStore{},
		jobs:      &mockJobStore{},
		github:    &mockGitHubTokenProvider{token: "ghp_test123"},
		codexAuth: nil,
		creds:     &mockCredentialProvider{},
		snapshots: &mockSnapshotStore{},
	}
}

func buildOrchestrator(d testDeps) *agent.Orchestrator {
	return agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{d.adapter.Name(): d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		SessionMessages:  d.messages,
		DecisionLog:      d.decisions,
		ProjectTasks:     d.projects,
		Issues:           d.issues,
		Repositories:     d.repos,
		Jobs:             d.jobs,
		GitHub:           d.github,
		CodexAuth:        d.codexAuth,
		Credentials:      d.creds,
		Snapshots:        d.snapshots,
		Cancels:          d.cancels,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
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
	statuses := d.sessions.getStatusUpdates()
	require.Contains(t, statuses, "running")

	// Result should be "completed" with high confidence.
	results := d.sessions.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)
	require.NotNil(t, results[0].result.ConfidenceScore)
	require.InDelta(t, 0.9, *results[0].result.ConfidenceScore, 0.01)

	// Validate job should be enqueued.
	require.Contains(t, d.jobs.getEnqueued(), "validate")
	validatePayload, ok := d.jobs.getPayload("validate").(map[string]interface{})
	require.True(t, ok, "validate job payload should be a map")
	require.Equal(t, run.ID.String(), validatePayload["session_id"], "validate payload should include agent run ID")
	require.Equal(t, run.OrgID.String(), validatePayload["org_id"], "validate payload should include org ID")

	// Logs should be persisted.
	require.GreaterOrEqual(t, d.logs.getCount(), 2)

	// Sandbox should be destroyed.
	require.Equal(t, 1, d.provider.GetDestroyCalls())
}

func TestRunAgent_ExecuteErrorUpdatesProjectTask(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	projectTaskID := uuid.New()
	run := testRun(orgID, issue.ID)
	run.ProjectTaskID = &projectTaskID

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return nil, errors.New("tool execution failed")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should return an execution error")
	require.Equal(t, []string{"failed"}, d.projects.getStatuses(), "project task hook should be called with failed status on execute error")
}

func TestRunAgent_PopulatesPMContext(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issueID := uuid.New()
	runID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	pmApproach := "Check handlers/billing.go:42"
	pmReasoning := "High impact"

	run := &models.Session{
		ID:          runID,
		IssueID:     issueID,
		OrgID:       orgID,
		AgentType:   "claude_code",
		Status:      "pending",
		TokenMode:   "low",
		PMApproach:  &pmApproach,
		PMReasoning: &pmReasoning,
	}

	mockRuns := &mockSessionStore{}
	mockIssues := &mockIssueStore{issue: models.Issue{ID: issueID, OrgID: orgID, RepositoryID: &repoID, Title: "Issue"}}
	mockRepos := &mockRepositoryStore{repo: models.Repository{
		ID:             repoID,
		OrgID:          orgID,
		CloneURL:       "https://example.com/repo.git",
		DefaultBranch:  "main",
		InstallationID: 123,
	}}
	mockOrgs := &mockOrgStore{org: models.Organization{ID: orgID}}
	mockJobs := &mockJobStore{}
	mockLogs := &mockSessionLogStore{}
	mockQuestions := &mockSessionQuestionStore{}
	mockDecisions := &mockDecisionLogStore{}
	mockGH := &mockGitHubTokenProvider{token: "token"}
	sandboxProvider := testutil.NewMockSandboxProvider()

	capAdapter := &capturingAdapter{name: models.AgentTypeClaudeCode}

	orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         sandboxProvider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypeClaudeCode: capAdapter},
		Sessions:         mockRuns,
		SessionLogs:      mockLogs,
		SessionQuestions: mockQuestions,
		DecisionLog:      mockDecisions,
		Issues:           mockIssues,
		Repositories:     mockRepos,
		Orgs:             mockOrgs,
		Jobs:             mockJobs,
		GitHub:           mockGH,
		Logger:           zerolog.Nop(),
	})

	err := orchestrator.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed")
	require.NotNil(t, capAdapter.captured, "adapter should capture input")
	require.NotNil(t, capAdapter.captured.PMContext, "PMContext should be populated")
	require.Equal(t, pmApproach, capAdapter.captured.PMContext.Approach, "PMContext should include approach")
	require.Equal(t, pmReasoning, capAdapter.captured.PMContext.Reasoning, "PMContext should include reasoning")
	require.WithinDuration(t, now, time.Now(), time.Minute, "sanity check")
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
	results := d.sessions.getResultUpdates()
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
	analyzePayload, ok := d.jobs.getPayload("analyze_failure").(map[string]interface{})
	require.True(t, ok, "analyze_failure payload should be a map")
	require.Equal(t, run.ID.String(), analyzePayload["session_id"], "analyze_failure payload should include agent run ID")
	require.Equal(t, run.OrgID.String(), analyzePayload["org_id"], "analyze_failure payload should include org ID")

	// Sandbox should be destroyed.
	require.Equal(t, 1, d.provider.GetDestroyCalls())
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
	results := d.sessions.getResultUpdates()
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

	// Medium confidence (0.65 >= default aggressive auto_proceed 0.4) proceeds as completed.
	results := d.sessions.getResultUpdates()
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
	d.sessions.countRunning = 3 // At the limit.

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "concurrency limit reached")

	// Status should NOT have been updated to "running".
	statuses := d.sessions.getStatusUpdates()
	for _, s := range statuses {
		require.NotEqual(t, "running", s)
	}

	// Sandbox should never have been created, so destroy shouldn't be called.
	require.Equal(t, 0, d.provider.GetDestroyCalls())
}

func TestRunAgent_SandboxCleanupOnCreateFailure(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		return nil, errors.New("docker daemon not running")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "create sandbox")

	// Destroy should not be called since Create failed (no sandbox to destroy).
	require.Equal(t, 0, d.provider.GetDestroyCalls())

	// Run should be marked as failed.
	results := d.sessions.getResultUpdates()
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
	d.provider.CloneRepoFn = func(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
		return errors.New("auth failed")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "clone repo")

	// Sandbox was created so Destroy must be called.
	require.Equal(t, 1, d.provider.GetDestroyCalls())
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
	statuses := d.sessions.getStatusUpdates()
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
	results := d.sessions.getResultUpdates()
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
			ConfidenceScore: 0.4, // Exactly at the default aggressive auto_proceed threshold.
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Score == 0.4 should proceed (>= aggressive auto_proceed threshold).
	results := d.sessions.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)
	require.Contains(t, d.jobs.getEnqueued(), "validate")
}

func TestRunAgent_AgentCredentialsInjected(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()

	// Track the SandboxConfig passed to Create.
	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "env-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config: models.AnthropicConfig{
					APIKey: "sk-ant-test-key",
				},
			},
		},
	}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{d.adapter.Name(): d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Verify the sandbox was created with the credential-derived env vars.
	require.NotNil(t, capturedCfg.Env, "sandbox config should have env vars")
	require.Equal(t, "sk-ant-test-key", capturedCfg.Env["ANTHROPIC_API_KEY"])
}

func TestRunAgent_NoAgentEnvForUnknownType(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "no-env-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	// No credential configured for "claude_code".
	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{d.adapter.Name(): d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Sandbox should have no agent-specific env vars since "claude_code" has no credential.
	// HOME is always injected as a fallback, and GitHub integration vars are injected
	// when integration skills are available (independent of agent type).
	require.NotContains(t, capturedCfg.Env, "ANTHROPIC_API_KEY",
		"sandbox config should not have agent-specific env for unconfigured agent type")
	require.Equal(t, "/home/sandbox", capturedCfg.Env["HOME"],
		"HOME should always be set to the sandbox user's home dir")
}

func TestRunAgent_CodexUsesAuthJsonNotEnvVar(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	d.codexAuth = &mockCodexAuthProvider{
		cfg: &models.OpenAIChatGPTConfig{
			AccessToken:  "chatgpt-access-token",
			RefreshToken: "chatgpt-refresh-token",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		},
	}

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "codex-oauth", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "run should succeed when ChatGPT OAuth token exists")

	// CODEX_API_KEY must NOT be set — it causes Codex CLI to call
	// api.openai.com which requires api.responses.write scope.
	require.Empty(t, capturedCfg.Env["CODEX_API_KEY"], "CODEX_API_KEY should not be set as env var")

	// Instead, the token should be injected via auth.json.
	authData, ok := d.provider.Files["/home/sandbox/.codex/auth.json"]
	require.True(t, ok, "auth.json should be written to sandbox")
	var authJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(authData, &authJSON))
	require.Equal(t, "chatgpt", authJSON["auth_mode"])
	tokens, ok := authJSON["tokens"].(map[string]interface{})
	require.True(t, ok, "auth.json should have tokens object")
	require.Equal(t, "chatgpt-access-token", tokens["access_token"], "auth.json should contain the ChatGPT OAuth token")
}

func TestRunAgent_CodexOpenAIKeyAloneIsNotSufficient(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	d.codexAuth = nil // no ChatGPT OAuth
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderOpenAI: {
				Provider: models.ProviderOpenAI,
				Config:   models.OpenAIConfig{APIKey: "sk-openai-key", BaseURL: "https://api.openai.com/v1", APIType: "chat"},
			},
		},
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "run should fail when only OpenAI API key exists (no ChatGPT OAuth)")
	require.Contains(t, err.Error(), "no credentials", "error should mention missing credentials")
}

func TestRunAgent_CodexAuthWritesToSandboxWorkdir(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	d.codexAuth = &mockCodexAuthProvider{
		cfg: &models.OpenAIChatGPTConfig{
			AccessToken:  "test-access-token",
			RefreshToken: "test-refresh-token",
			ExpiresAt:    time.Date(2026, time.February, 23, 12, 0, 0, 0, time.UTC),
		},
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "run should succeed when codex auth token is present")

	require.Contains(
		t,
		d.provider.ExecCalls,
		"mkdir -p /home/sandbox/.codex",
		"codex auth setup should create the auth directory under the sandbox user's home",
	)

	authData, ok := d.provider.Files["/home/sandbox/.codex/auth.json"]
	require.True(t, ok, "codex auth injection should write auth.json under /home/sandbox/.codex")

	var authJSON map[string]interface{}
	unmarshalErr := json.Unmarshal(authData, &authJSON)
	require.NoError(t, unmarshalErr, "auth.json should contain valid JSON")
	require.Equal(t, "chatgpt", authJSON["auth_mode"], "auth.json should set auth_mode to chatgpt")
	tokens, ok := authJSON["tokens"].(map[string]interface{})
	require.True(t, ok, "auth.json should have a tokens object")
	require.Equal(t, "test-access-token", tokens["access_token"], "tokens should include access token")
	require.Equal(t, "", tokens["refresh_token"], "refresh_token should be empty so the CLI cannot consume it")
	require.NotEmpty(t, authJSON["last_refresh"], "auth.json should include last_refresh timestamp")
}

func TestRunAgent_CodexNoCredentialsFails(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	// No codexAuth provider and no CODEX_API_KEY in env.
	d.codexAuth = nil

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "run should fail when codex has no credentials")
	require.Contains(t, err.Error(), "no credentials", "error should mention missing credentials")

	// Verify the run was marked as failed via UpdateResult.
	d.sessions.mu.Lock()
	defer d.sessions.mu.Unlock()
	require.NotEmpty(t, d.sessions.resultUpdates, "run should have a result update")
	lastResult := d.sessions.resultUpdates[len(d.sessions.resultUpdates)-1]
	require.Equal(t, "failed", lastResult.status, "run should be marked as failed")
}

func TestRunAgent_CodexSandboxHasHomeEnv(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	d.codexAuth = &mockCodexAuthProvider{
		cfg: &models.OpenAIChatGPTConfig{
			AccessToken:  "test-token",
			RefreshToken: "test-refresh",
			ExpiresAt:    time.Date(2026, time.February, 23, 12, 0, 0, 0, time.UTC),
		},
	}

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "test-sandbox", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	orch := buildOrchestrator(d)
	_ = orch.RunAgent(context.Background(), run)

	require.Equal(t, "/home/sandbox", capturedCfg.Env["HOME"],
		"sandbox env should set HOME to the sandbox user's home so Codex CLI finds ~/.codex/auth.json")
}

func TestRunAgent_IssueWithoutRepository(t *testing.T) {
	t.Parallel()

	orgID := testOrg()

	// Issue with no repository_id.
	issue := testIssue(orgID)
	issue.RepositoryID = nil

	run := testRun(orgID, issue.ID)
	run.RepositoryID = nil // no repo on session either

	d := defaultDeps()
	d.issues.issue = issue

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	// Should complete without cloning.
	results := d.sessions.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)
}

func TestRunAgent_ManualSessionTransitionsToIdle(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.issues.issue = issue
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("snapshot-bytes"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Diff:                "--- a/main.go\n+++ b/main.go",
			Summary:             "Initial manual turn complete",
			ConfidenceScore:     0.92,
			ConfidenceReasoning: "straightforward fix",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "manual run should succeed")

	require.Empty(t, d.sessions.getResultUpdates(), "manual interactive turn should not finalize the run result yet")
	turnUpdates := d.sessions.getTurnUpdates()
	require.Len(t, turnUpdates, 1, "manual interactive run should persist an idle turn update")
	require.Equal(t, 1, turnUpdates[0].turn, "first manual turn should advance current_turn to 1")
	require.NotNil(t, turnUpdates[0].result, "turn update should persist the turn result")
	require.NotNil(t, turnUpdates[0].result.ResultSummary, "turn update should keep the assistant summary")
	require.Equal(t, "Initial manual turn complete", *turnUpdates[0].result.ResultSummary, "turn update should store the assistant summary")
	require.NotNil(t, turnUpdates[0].result.Diff, "turn update should store the latest diff")
	require.NotEmpty(t, turnUpdates[0].snapshotKey, "manual interactive run should store a snapshot key")

	messages := d.messages.getMessages()
	require.Len(t, messages, 1, "manual interactive run should create an assistant message")
	require.Equal(t, models.MessageRoleAssistant, messages[0].Role, "assistant reply should be stored in session_messages")
	require.Equal(t, 1, messages[0].TurnNumber, "assistant reply should be recorded for turn 1")

	require.NotContains(t, d.jobs.getEnqueued(), "validate", "manual interactive run should wait for explicit end before validation")
}

func TestContinueSession_PersistsTurnResultAndReturnsToIdle(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session.tar")

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please add regression coverage too.",
		},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.True(t, prompt.Continuation, "continue_session should execute the adapter in continuation mode")
		require.Equal(t, "Please add regression coverage too.", prompt.UserMessage, "continue_session should pass the latest user message")
		return &agent.AgentResult{
			Diff:                "--- a/main_test.go\n+++ b/main_test.go",
			Summary:             "Added the regression test",
			ConfidenceScore:     0.81,
			ConfidenceReasoning: "small follow-up",
			ExitCode:            0,
		}, nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.NoError(t, err, "continue_session should succeed")

	turnUpdates := d.sessions.getTurnUpdates()
	require.Len(t, turnUpdates, 1, "continue_session should persist the completed turn")
	require.Equal(t, 2, turnUpdates[0].turn, "continue_session should advance the turn counter")
	require.NotNil(t, turnUpdates[0].result, "continue_session should persist the latest result")
	require.NotNil(t, turnUpdates[0].result.ResultSummary, "continue_session should persist the latest summary")
	require.Equal(t, "Added the regression test", *turnUpdates[0].result.ResultSummary, "continue_session should store the latest assistant summary")
	require.NotNil(t, turnUpdates[0].result.Diff, "continue_session should persist the latest diff")
	require.Contains(t, d.sessions.getStatusUpdates(), "running", "continue_session should mark the session running while work is in progress")
	require.NotContains(t, d.jobs.getEnqueued(), "validate", "continue_session should stay interactive until the user ends the session")

	messages := d.messages.getMessages()
	require.Len(t, messages, 2, "continue_session should append an assistant reply")
	require.Equal(t, models.MessageRoleAssistant, messages[1].Role, "assistant reply should be stored for the continued turn")
	require.Equal(t, 2, messages[1].TurnNumber, "assistant reply should use the new turn number")
}

// TestContinueSession_SessionRepoSlug drives ContinueSession through the
// sessionRepoSlug fallback branches by capturing the WorkDir passed to
// provider.Create. Create is forced to fail so the test doesn't need a full
// snapshot/restore harness; the relevant code runs before Create is invoked.
func TestContinueSession_SessionRepoSlug(t *testing.T) {
	t.Parallel()

	const defaultWorkDir = "/workspace"
	const slugWorkDir = "/home/sandbox/backend"

	type tc struct {
		name         string
		prepSession  func(s *models.Session)
		prepDeps     func(d testDeps)
		wantWorkDir  string
		wantErrMatch string
	}

	cases := []tc{
		{
			name: "session without repo falls back to issue's repo",
			prepSession: func(s *models.Session) {
				s.RepositoryID = nil
			},
			prepDeps:     func(d testDeps) {},
			wantWorkDir:  slugWorkDir,
			wantErrMatch: "create sandbox",
		},
		{
			name: "session without repo and issue fetch fails falls back to default",
			prepSession: func(s *models.Session) {
				s.RepositoryID = nil
			},
			prepDeps: func(d testDeps) {
				d.issues.err = errors.New("boom")
			},
			wantWorkDir:  defaultWorkDir,
			wantErrMatch: "create sandbox",
		},
		{
			name: "session without repo and issue has no repo falls back to default",
			prepSession: func(s *models.Session) {
				s.RepositoryID = nil
			},
			prepDeps: func(d testDeps) {
				issue := d.issues.issue
				issue.RepositoryID = nil
				d.issues.issue = issue
			},
			wantWorkDir:  defaultWorkDir,
			wantErrMatch: "create sandbox",
		},
		{
			name:        "repo fetch fails falls back to default",
			prepSession: func(s *models.Session) {},
			prepDeps: func(d testDeps) {
				d.repos.err = errors.New("boom")
			},
			wantWorkDir:  defaultWorkDir,
			wantErrMatch: "create sandbox",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			session := testRun(orgID, issue.ID)
			session.Status = string(models.SessionStatusIdle)
			session.CurrentTurn = 1
			c.prepSession(session)

			d := defaultDeps()
			// mockIssueStore / mockRepositoryStore are value-holders; update in place.
			d.issues.issue = issue
			c.prepDeps(d)

			// Pre-populate a user message so ContinueSession reaches the Create call.
			d.messages.messages = []models.SessionMessage{{
				ID:         1,
				SessionID:  session.ID,
				OrgID:      orgID,
				TurnNumber: 2,
				Role:       models.MessageRoleUser,
				Content:    "continue please",
			}}

			var gotWorkDir string
			d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
				gotWorkDir = cfg.WorkDir
				return nil, errors.New("forced create failure to short-circuit test")
			}

			orch := buildOrchestrator(d)
			err := orch.ContinueSession(context.Background(), session)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.wantErrMatch)
			require.Equal(t, c.wantWorkDir, gotWorkDir, "sessionRepoSlug should drive WorkDir selection")
		})
	}
}

// ---------------------------------------------------------------------------
// WithSandboxProvider / SandboxProviderFromContext
// ---------------------------------------------------------------------------

func TestWithSandboxProvider_RoundTrip(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	ctx := agent.WithSandboxProvider(context.Background(), provider)

	got := agent.SandboxProviderFromContext(ctx)
	require.NotNil(t, got, "provider should be retrievable from context")
	require.Equal(t, provider, got)
}

func TestSandboxProviderFromContext_ReturnsNilWhenMissing(t *testing.T) {
	t.Parallel()

	got := agent.SandboxProviderFromContext(context.Background())
	require.Nil(t, got, "should return nil when no provider is set")
}

func TestRunAgent_InjectsSandboxProviderIntoContext(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		p := agent.SandboxProviderFromContext(ctx)
		require.NotNil(t, p, "RunAgent must inject the SandboxProvider into the adapter's context")
		return &agent.AgentResult{
			Summary:         "ok",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)
}

func TestContinueSession_InjectsSandboxProviderIntoContext(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session.tar")

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Follow up",
		},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("snap"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		p := agent.SandboxProviderFromContext(ctx)
		require.NotNil(t, p, "ContinueSession must inject the SandboxProvider into the adapter's context")
		return &agent.AgentResult{
			Summary:         "continued",
			ConfidenceScore: 0.85,
			ExitCode:        0,
		}, nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.NoError(t, err)
}

func TestRunAgent_CodexAuthInjectsTokenFromGetValidToken(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	d.codexAuth = &mockCodexAuthProvider{
		cfg: &models.OpenAIChatGPTConfig{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		},
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err)

	authData, ok := d.provider.Files["/home/sandbox/.codex/auth.json"]
	require.True(t, ok, "auth.json should be written to sandbox")
	var authJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(authData, &authJSON))
	tokens, ok := authJSON["tokens"].(map[string]interface{})
	require.True(t, ok, "auth.json should have tokens object")
	require.Equal(t, "access-token", tokens["access_token"], "auth.json should contain the token from GetValidToken")
}

func TestRunAgent_CancelReturnsToIdle(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	cancelReg := agent.NewCancelRegistry(zerolog.Nop())

	d := defaultDeps()
	d.cancels = cancelReg
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("cancel-snapshot"))), nil
	}
	// Make Exec fail so doCancel falls back to immediate context cancel
	// (avoids 30s timer wait in tests).
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 1, errors.New("exec not available in test")
	}

	// The adapter blocks until the cancel registry fires, then returns an error
	// simulating the agent being interrupted.
	adapterStarted := make(chan struct{})
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		close(adapterStarted)
		// Wait for the context to be cancelled (by the cancel registry fallback).
		<-ctx.Done()
		return &agent.AgentResult{
			Summary:        "partial work",
			ExitCode:       1,
			AgentSessionID: "agent-sess-123",
		}, ctx.Err()
	}

	done := make(chan error, 1)
	go func() {
		done <- buildOrchestrator(d).RunAgent(context.Background(), run)
	}()

	// Wait for the adapter to start executing, then trigger cancel.
	<-adapterStarted
	require.True(t, cancelReg.CancelSession(run.ID), "cancel should find registered session")

	err := <-done
	require.Error(t, err, "RunAgent should return an error when cancelled")
	require.Contains(t, err.Error(), "cancelled")

	// handleCancelledSession should have snapshot'd and returned to idle.
	turnUpdates := d.sessions.getTurnUpdates()
	require.Len(t, turnUpdates, 1, "cancelled session should have a turn update (returned to idle)")
	require.NotEmpty(t, turnUpdates[0].snapshotKey, "cancelled session should have a snapshot key")

	// Sandbox should be destroyed.
	require.Equal(t, 1, d.provider.GetDestroyCalls())
}

func TestRunAgent_CancelWithoutSnapshotMarksCancelled(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	agentSessID := "existing-agent-sess"
	run.AgentSessionID = &agentSessID

	cancelReg := agent.NewCancelRegistry(zerolog.Nop())

	d := defaultDeps()
	d.cancels = cancelReg
	// Make snapshot fail so handleCancelledSession hits the "no snapshot" path
	// and marks the session as cancelled instead of idle.
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return nil, errors.New("snapshot unavailable")
	}
	// Make Exec fail so doCancel falls back to immediate context cancel.
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 1, errors.New("exec not available in test")
	}

	adapterStarted := make(chan struct{})
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		close(adapterStarted)
		<-ctx.Done()
		// Return nil result to exercise the session.AgentSessionID fallback path.
		return nil, ctx.Err()
	}

	done := make(chan error, 1)
	go func() {
		done <- buildOrchestrator(d).RunAgent(context.Background(), run)
	}()

	<-adapterStarted
	require.True(t, cancelReg.CancelSession(run.ID))

	err := <-done
	require.Error(t, err)

	// Without snapshots, session should be marked cancelled (not idle).
	statuses := d.sessions.getStatusUpdates()
	require.Contains(t, statuses, "cancelled", "session without snapshot should be marked cancelled")

	// No turn updates since there's no snapshot.
	turnUpdates := d.sessions.getTurnUpdates()
	require.Empty(t, turnUpdates, "cancelled session without snapshot should not have turn updates")
}
