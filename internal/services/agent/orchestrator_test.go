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

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/storage"
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
		SystemPrompt:    "test system prompt",
		UserPrompt:      "test user prompt",
		MaxTokens:       50000,
		ReasoningEffort: input.ReasoningEffort,
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

// mockClaudeCodeAuthProvider implements agent.ClaudeCodeAuthProvider.
type mockClaudeCodeAuthProvider struct {
	sub       *models.AnthropicSubscription
	credID    *uuid.UUID
	hasSub    bool
	hasSubErr error
	tokenErr  error
}

func (m *mockClaudeCodeAuthProvider) HasActiveSubscription(ctx context.Context, orgID uuid.UUID) (bool, error) {
	return m.hasSub, m.hasSubErr
}

func (m *mockClaudeCodeAuthProvider) GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.AnthropicSubscription, *uuid.UUID, error) {
	if m.tokenErr != nil {
		return nil, nil, m.tokenErr
	}
	return m.sub, m.credID, nil
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

func (m *mockCredentialProvider) ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	cred, err := m.Get(ctx, orgID, provider)
	if err != nil || cred == nil {
		return nil, err
	}
	return []models.DecryptedCredential{*cred}, nil
}

// mockSessionStore implements agent.SessionStore.
type mockSessionStore struct {
	mu                     sync.Mutex
	countRunning           int
	statusUpdates          []string
	resultUpdates          []resultUpdate
	turnUpdates            []turnUpdate
	runtimeBegins          []runtimeBegin
	progressUpdates        []runtimeProgressUpdate
	extensionGrants        []runtimeExtensionGrant
	checkpoints            []checkpointUpdate
	recoveryStates         []recoveryStateUpdate
	baseCommitSHAs         []string
	failureUpdates         []failureUpdate
	workerOwnerships       []workerOwnershipUpdate
	revisionContextUpdates [][]byte
	countRunningErr        error
	beginRuntimeErr        error
	updateRevisionErr      error
	acquireHoldFn          func(proposedContainerID string) (string, error)
	acquireHoldErr         error
	setWorkerNodeErr       error
	releaseHoldFn          func() (bool, string, error)
	finalizeFn             func(expectedContainerID string) (bool, error)
	acquireHoldCalls       int
	releaseHoldCalls       int
	finalizeCalls          int
}

type failureUpdate struct {
	explanation  string
	category     string
	nextSteps    []string
	retryAdvised bool
}

type workerOwnershipUpdate struct {
	containerID  string
	workerNodeID string
}

type runtimeBegin struct {
	capability   models.CheckpointCapability
	softDeadline time.Time
	hardDeadline time.Time
	observedAt   time.Time
}

type runtimeProgressUpdate struct {
	progressType models.RuntimeProgressType
	strength     models.RuntimeProgressStrength
	observedAt   time.Time
}

type checkpointUpdate struct {
	agentSessionID string
	snapshotKey    string
	kind           models.CheckpointKind
	capability     models.CheckpointCapability
	sizeBytes      int64
	checkpointErr  *string
	stopReason     models.RuntimeStopReason
}

type runtimeExtensionGrant struct {
	expectedSoftDeadline time.Time
	newSoftDeadline      time.Time
	extensionSeconds     int
}

type recoveryStateUpdate struct {
	state            models.RecoveryState
	incrementAttempt bool
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

func (m *mockSessionStore) BeginRuntime(ctx context.Context, orgID, sessionID uuid.UUID, capability models.CheckpointCapability, softDeadline, hardDeadline, observedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtimeBegins = append(m.runtimeBegins, runtimeBegin{
		capability:   capability,
		softDeadline: softDeadline,
		hardDeadline: hardDeadline,
		observedAt:   observedAt,
	})
	return m.beginRuntimeErr
}

func (m *mockSessionStore) RecordRuntimeProgress(ctx context.Context, orgID, sessionID uuid.UUID, progressType models.RuntimeProgressType, strength models.RuntimeProgressStrength, observedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.progressUpdates = append(m.progressUpdates, runtimeProgressUpdate{
		progressType: progressType,
		strength:     strength,
		observedAt:   observedAt,
	})
	return nil
}

func (m *mockSessionStore) GrantRuntimeExtension(ctx context.Context, orgID, sessionID uuid.UUID, lockToken uuid.UUID, expectedSoftDeadline, newSoftDeadline, hardDeadline time.Time, extensionSeconds int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.extensionGrants = append(m.extensionGrants, runtimeExtensionGrant{
		expectedSoftDeadline: expectedSoftDeadline,
		newSoftDeadline:      newSoftDeadline,
		extensionSeconds:     extensionSeconds,
	})
	return true, nil
}

func (m *mockSessionStore) PublishCheckpoint(ctx context.Context, orgID, sessionID uuid.UUID, lockToken uuid.UUID, agentSessionID, snapshotKey string, kind models.CheckpointKind, capability models.CheckpointCapability, sizeBytes int64, checkpointedAt time.Time, checkpointErr *string, stopReason models.RuntimeStopReason) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkpoints = append(m.checkpoints, checkpointUpdate{
		agentSessionID: agentSessionID,
		snapshotKey:    snapshotKey,
		kind:           kind,
		capability:     capability,
		sizeBytes:      sizeBytes,
		checkpointErr:  checkpointErr,
		stopReason:     stopReason,
	})
	return true, nil
}

func (m *mockSessionStore) UpdateRecoveryState(ctx context.Context, orgID, sessionID uuid.UUID, state models.RecoveryState, queuedAt, startedAt *time.Time, incrementAttempt bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveryStates = append(m.recoveryStates, recoveryStateUpdate{
		state:            state,
		incrementAttempt: incrementAttempt,
	})
	return nil
}

func (m *mockSessionStore) UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error {
	return nil
}

func (m *mockSessionStore) UpdateWorkingBranch(ctx context.Context, orgID, sessionID uuid.UUID, branch string) error {
	return nil
}

func (m *mockSessionStore) UpdateBaseCommitSHA(ctx context.Context, orgID, sessionID uuid.UUID, baseCommitSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baseCommitSHAs = append(m.baseCommitSHAs, baseCommitSHA)
	return nil
}

func (m *mockSessionStore) UpdateTitle(ctx context.Context, orgID, sessionID uuid.UUID, title string) error {
	return nil
}

func (m *mockSessionStore) UpdateRevisionContext(ctx context.Context, orgID, sessionID uuid.UUID, revisionContext []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revisionContextUpdates = append(m.revisionContextUpdates, revisionContext)
	return m.updateRevisionErr
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

func (m *mockSessionStore) AcquireTurnHold(ctx context.Context, orgID, sessionID uuid.UUID, proposedContainerID string) (string, error) {
	m.mu.Lock()
	m.acquireHoldCalls++
	fn := m.acquireHoldFn
	err := m.acquireHoldErr
	m.mu.Unlock()
	if fn != nil {
		return fn(proposedContainerID)
	}
	if err != nil {
		return "", err
	}
	// Default: caller's proposal wins.
	return proposedContainerID, nil
}

func (m *mockSessionStore) SetWorkerNodeIDForContainer(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID, workerNodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workerOwnerships = append(m.workerOwnerships, workerOwnershipUpdate{
		containerID:  expectedContainerID,
		workerNodeID: workerNodeID,
	})
	return m.setWorkerNodeErr
}

func (m *mockSessionStore) ReleaseTurnHold(ctx context.Context, orgID, sessionID uuid.UUID) (bool, string, error) {
	m.mu.Lock()
	m.releaseHoldCalls++
	fn := m.releaseHoldFn
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	// Default: turn held it alone, destroy and clear.
	return true, "", nil
}

func (m *mockSessionStore) FinalizeContainerDestroy(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (bool, error) {
	m.mu.Lock()
	m.finalizeCalls++
	fn := m.finalizeFn
	m.mu.Unlock()
	if fn != nil {
		return fn(expectedContainerID)
	}
	// Default: CAS succeeds.
	return true, nil
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

func (m *mockSessionStore) getFailureUpdates() []failureUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]failureUpdate, len(m.failureUpdates))
	copy(out, m.failureUpdates)
	return out
}

func (m *mockSessionStore) getCheckpointUpdates() []checkpointUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]checkpointUpdate, len(m.checkpoints))
	copy(out, m.checkpoints)
	return out
}

func (m *mockSessionStore) getBaseCommitSHAs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.baseCommitSHAs))
	copy(out, m.baseCommitSHAs)
	return out
}

// mockSessionLogStore implements agent.SessionLogStore.
type mockSessionLogStore struct {
	mu                   sync.Mutex
	logs                 []models.SessionLog
	count                int
	markedTurnNumber     int
	markedMessage        string
	markedOrgID          uuid.UUID
	markedSessionID      uuid.UUID
	markDuplicateInvoked bool
	markDuplicateErr     error
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

func (m *mockSessionLogStore) MarkAssistantTranscriptDuplicate(ctx context.Context, orgID, sessionID uuid.UUID, turnNumber int, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markDuplicateInvoked = true
	m.markedOrgID = orgID
	m.markedSessionID = sessionID
	m.markedTurnNumber = turnNumber
	m.markedMessage = message
	return m.markDuplicateErr
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
	mu        sync.Mutex
	messages  []models.SessionMessage
	createErr error
}

func (m *mockSessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
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
	mu               sync.Mutex
	enqueued         []string // job types
	payloads         map[string]any
	oldestPendingAge time.Duration
	hasPendingAge    bool
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

func (m *mockJobStore) OldestPendingSessionJobAge(ctx context.Context) (time.Duration, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.oldestPendingAge, m.hasPendingAge, nil
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
		ID:             uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		PrimaryIssueID: &issueID,
		OrgID:          orgID,
		AgentType:      models.AgentTypeClaudeCode,
		Status:         "pending",
		TokenMode:      "low",
		RepositoryID:   &repoID,
	}
}

func strPtr(s string) *string {
	return &s
}

type testDeps struct {
	provider       *testutil.MockSandboxProvider
	adapter        *mockAgentAdapter
	sessions       *mockSessionStore
	projects       *mockProjectTaskUpdater
	issues         *mockIssueStore
	repos          *mockRepositoryStore
	logs           *mockSessionLogStore
	questions      *mockSessionQuestionStore
	messages       *mockSessionMessageStore
	decisions      *mockDecisionLogStore
	jobs           *mockJobStore
	github         *mockGitHubTokenProvider
	codexAuth      agent.CodexAuthProvider
	claudeCodeAuth agent.ClaudeCodeAuthProvider
	creds          *mockCredentialProvider
	snapshots      *mockSnapshotStore
	cancels        *agent.CancelRegistry
	nodeID         string
	orgs           *mockOrgStore
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
		creds: &mockCredentialProvider{
			byProvider: map[models.ProviderName]*models.DecryptedCredential{
				// Seed an Anthropic API key so ensureClaudeCodeAuth's fallback
				// path succeeds for tests that use the default Claude Code
				// agent type. Tests exercising credential-specific behavior
				// override d.creds directly.
				models.ProviderAnthropic: {
					Provider: models.ProviderAnthropic,
					Config:   models.AnthropicConfig{APIKey: "sk-ant-default-test"},
				},
			},
		},
		snapshots: &mockSnapshotStore{},
	}
}

func buildOrchestrator(d testDeps) *agent.Orchestrator {
	var orgStore agent.OrgStore
	if d.orgs != nil {
		orgStore = d.orgs
	}
	var snapshotStore storage.SnapshotStore
	if d.snapshots != nil {
		snapshotStore = d.snapshots
	}
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
		ClaudeCodeAuth:   d.claudeCodeAuth,
		Credentials:      d.creds,
		Snapshots:        snapshotStore,
		Cancels:          d.cancels,
		Orgs:             orgStore,
		NodeID:           d.nodeID,
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

func TestRunAgent_MarksFinalOutputLogAsTranscriptDuplicate(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	run := testRun(orgID, issue.ID)
	run.Origin = models.SessionOriginManual
	run.InteractionMode = models.SessionInteractionModeInteractive
	run.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd

	d := defaultDeps()
	d.issues.issue = issue
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: "Final answer"}
		return &agent.AgentResult{
			Summary:  "Final answer",
			ExitCode: 0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should complete successfully")
	require.True(t, d.logs.markDuplicateInvoked, "RunAgent should mark the final output log as a transcript duplicate")
	require.Equal(t, run.OrgID, d.logs.markedOrgID, "duplicate marker should use the session org")
	require.Equal(t, run.ID, d.logs.markedSessionID, "duplicate marker should use the session id")
	require.Equal(t, 1, d.logs.markedTurnNumber, "duplicate marker should tag the first turn")
	require.Equal(t, "Final answer", d.logs.markedMessage, "duplicate marker should target the final assistant summary")
}

func TestRunAgent_ContinuesWhenAssistantMessageCreateFails(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	run := testRun(orgID, issue.ID)
	run.Origin = models.SessionOriginManual
	run.InteractionMode = models.SessionInteractionModeInteractive
	run.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.createErr = errors.New("persist failed")
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:  "Final answer",
			ExitCode: 0,
		}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should continue when assistant message persistence fails for an interactive turn")
	require.False(t, d.logs.markDuplicateInvoked, "RunAgent should not attempt duplicate marking when assistant message persistence fails")
}

func TestRunAgent_LogsDuplicateMarkerFailureAndSucceeds(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	run := testRun(orgID, issue.ID)
	run.Origin = models.SessionOriginManual
	run.InteractionMode = models.SessionInteractionModeInteractive
	run.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd

	d := defaultDeps()
	d.issues.issue = issue
	d.logs.markDuplicateErr = errors.New("update failed")
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: "Final answer"}
		return &agent.AgentResult{
			Summary:  "Final answer",
			ExitCode: 0,
		}, nil
	}

	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
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
		ClaudeCodeAuth:   d.claudeCodeAuth,
		Credentials:      d.creds,
		Snapshots:        d.snapshots,
		Cancels:          d.cancels,
		Logger:           logger,
	})

	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should tolerate duplicate marker failures")
	require.Contains(t, buf.String(), "failed to mark assistant output log as transcript duplicate", "RunAgent should log the duplicate marker failure")
}

func TestRecoverSession_ResumesFromLatestDurableCheckpoint(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusRunning)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session.tar")
	session.AgentSessionID = strPtr("agent-session-1")

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue from the last checkpoint.",
		},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.True(t, prompt.Continuation, "recovery should resume in continuation mode")
		require.Equal(t, "agent-session-1", prompt.ResumeSessionID, "recovery should pass through the committed agent session id")
		return &agent.AgentResult{
			Summary:             "Recovered and continued the turn",
			ConfidenceScore:     0.88,
			ConfidenceReasoning: "checkpoint restore succeeded",
			ExitCode:            0,
		}, nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("checkpoint-bytes"),
	}

	orch := buildOrchestrator(d)
	err := orch.RecoverSession(context.Background(), session)
	require.NoError(t, err, "RecoverSession should resume from the committed checkpoint")

	turnUpdates := d.sessions.getTurnUpdates()
	require.Len(t, turnUpdates, 1, "recovery should complete the resumed turn")
	require.Equal(t, 2, turnUpdates[0].turn, "recovery should advance from the committed checkpoint turn")
	require.Contains(t, d.sessions.getStatusUpdates(), "running", "recovery should transition the session back to running during replay")
	require.Empty(t, d.sessions.getResultUpdates(), "recovery should stay on the interactive turn-complete path, not final result update")
}

func TestRecoverSession_RestartsWhenNoDurableCheckpointExists(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.Status = string(models.SessionStatusRunning)

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:             "Restarted cleanly",
			ConfidenceScore:     0.91,
			ConfidenceReasoning: "fresh restart",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RecoverSession(context.Background(), run)
	require.NoError(t, err, "RecoverSession should restart cleanly when no checkpoint exists")

	results := d.sessions.getResultUpdates()
	require.Len(t, results, 1, "restart should follow the normal run result path")
	require.Equal(t, "completed", results[0].status, "restart should complete the run from scratch")
	require.Contains(t, d.jobs.getEnqueued(), "validate", "restart should enqueue validation like a fresh run")
}

func TestRecoverSession_RestartsWithoutCountingOwnRunningSlot(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.Status = string(models.SessionStatusRunning)

	d := defaultDeps()
	d.sessions.countRunning = 1
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:             "Recovered by restarting cleanly",
			ConfidenceScore:     0.91,
			ConfidenceReasoning: "fresh restart after pre-checkpoint worker loss",
			ExitCode:            0,
		}, nil
	}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
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
		MaxConcurrent:    1,
	})

	err := orch.RecoverSession(context.Background(), run)
	require.NoError(t, err, "RecoverSession should ignore the recovering session's own running slot when restarting")

	results := d.sessions.getResultUpdates()
	require.Len(t, results, 1, "restart should still complete the run")
	require.Equal(t, "completed", results[0].status, "restart should complete successfully under a single-slot concurrency limit")
	require.Contains(t, d.jobs.getEnqueued(), "validate", "restart should enqueue validation like a fresh run")
}

func TestRecoverSession_PreservesRunningStatusWhenRuntimeInitFails(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusRunning)
	session.CurrentTurn = 1
	session.AgentSessionID = strPtr("agent-session-1")
	session.SnapshotKey = strPtr("snapshots/test/recover-init-failure.tar")

	d := defaultDeps()
	d.sessions.beginRuntimeErr = errors.New("runtime init write failed")
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue from the last checkpoint.",
		},
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("checkpoint-bytes"),
	}

	err := buildOrchestrator(d).RecoverSession(context.Background(), session)
	require.Error(t, err, "RecoverSession should surface runtime initialization errors")
	require.Contains(t, err.Error(), "begin runtime control", "RecoverSession should wrap the runtime initialization failure")

	statuses := d.sessions.getStatusUpdates()
	require.Equal(t, []string{"running", "running"}, statuses, "recovery should preserve the running status when runtime initialization fails")
}

// TestRunAgent_PreviewHoldsContainerSkipsDestroy covers the branch where
// ReleaseTurnHold reports destroyNow=false (a preview is holding the
// sandbox) so the deferred cleanup leaves the container alive
// (orchestrator.go:483-485).
func TestRunAgent_PreviewHoldsContainerSkipsDestroy(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.releaseHoldFn = func() (bool, string, error) {
		// Preview still holds — do NOT destroy.
		return false, "c-1", nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:             "ok",
			ConfidenceScore:     0.9,
			ConfidenceReasoning: "ok",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	require.NoError(t, orch.RunAgent(context.Background(), run))

	require.Equal(t, 1, d.sessions.acquireHoldCalls)
	require.Equal(t, 1, d.sessions.releaseHoldCalls)
	require.Equal(t, 0, d.sessions.finalizeCalls, "FinalizeContainerDestroy must not run while preview holds")
	require.Equal(t, 0, d.provider.GetDestroyCalls(), "sandbox must be left alive while preview holds")
}

// TestRunAgent_ReleaseHoldErrorFallsBackToDestroy covers the branch where
// ReleaseTurnHold errors and the orchestrator falls back to destroying to
// avoid a container leak (orchestrator.go:477-482).
func TestRunAgent_ReleaseHoldErrorFallsBackToDestroy(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.releaseHoldFn = func() (bool, string, error) {
		return false, "", errors.New("db down")
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:             "ok",
			ConfidenceScore:     0.9,
			ConfidenceReasoning: "ok",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	require.NoError(t, orch.RunAgent(context.Background(), run))

	// Even with a DB error on release, we still destroy to avoid a leak.
	require.Equal(t, 1, d.provider.GetDestroyCalls())
}

// TestRunAgent_AcquireHoldErrorFailsRun covers the branch where
// AcquireTurnHold errors: we must destroy the locally-created sandbox and
// fail the run, because the DB has no container_id reference so the
// reconciler can't clean it up.
func TestRunAgent_AcquireHoldErrorFailsRun(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.acquireHoldErr = errors.New("write failed")

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "acquire turn hold")

	require.Equal(t, 1, d.sessions.acquireHoldCalls)
	// Must destroy the sandbox we created so it does not leak.
	require.Equal(t, 1, d.provider.GetDestroyCalls())
	// Must not fall through to ReleaseTurnHold/Finalize — we never acquired.
	require.Equal(t, 0, d.sessions.releaseHoldCalls)
	require.Equal(t, 0, d.sessions.finalizeCalls)
}

// TestRunAgent_AcquireHoldLosesRaceFailsRun covers the branch where
// AcquireTurnHold succeeds but returns a different container_id (another
// holder published first). We must destroy our local sandbox and fail the
// run so the retry picks up the winning container via the reuse path.
func TestRunAgent_AcquireHoldLosesRaceFailsRun(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "winner-container", nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox race")

	require.Equal(t, 1, d.sessions.acquireHoldCalls)
	require.Equal(t, 1, d.provider.GetDestroyCalls(), "must destroy the losing sandbox")
	require.Equal(t, 0, d.sessions.releaseHoldCalls, "no release — we never held")
	require.Equal(t, 0, d.sessions.finalizeCalls)
}

func TestRunAgent_SetWorkerNodeIDFailureFailsRun(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.nodeID = "worker-a"
	d.sessions.setWorkerNodeErr = errors.New("persist failed")

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should fail when session worker ownership cannot be persisted")
	require.Contains(t, err.Error(), "persist session worker ownership", "RunAgent should surface the worker ownership persistence failure")
	require.Equal(t, 1, d.provider.GetDestroyCalls(), "RunAgent should destroy the sandbox when worker ownership persistence fails")
	require.Equal(t, 1, d.sessions.releaseHoldCalls, "RunAgent should release the turn hold when worker ownership persistence fails")
	require.Equal(t, 1, d.sessions.finalizeCalls, "RunAgent should finalize the container destroy when worker ownership persistence fails")
}

// TestRunAgent_FinalizeDestroyErrorSkipsDestroy covers the log-and-continue
// branch when FinalizeContainerDestroy errors: we skip destroy so we don't
// tear down a container that another holder may have just acquired.
func TestRunAgent_FinalizeDestroyErrorSkipsDestroy(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.finalizeFn = func(expectedContainerID string) (bool, error) {
		return false, errors.New("db write failed")
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:             "ok",
			ConfidenceScore:     0.9,
			ConfidenceReasoning: "ok",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	require.NoError(t, orch.RunAgent(context.Background(), run))

	require.Equal(t, 0, d.provider.GetDestroyCalls(), "must not destroy when finalize CAS errors")
	require.Equal(t, 1, d.sessions.finalizeCalls)
}

// TestRunAgent_FinalizeDestroyReturnsFalseSkipsDestroy covers the CAS-lost
// branch: a new holder acquired between ReleaseTurnHold and FinalizeContainerDestroy.
func TestRunAgent_FinalizeDestroyReturnsFalseSkipsDestroy(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.finalizeFn = func(expectedContainerID string) (bool, error) {
		// Simulate a preview grabbing the container after release.
		return false, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:             "ok",
			ConfidenceScore:     0.9,
			ConfidenceReasoning: "ok",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	require.NoError(t, orch.RunAgent(context.Background(), run))

	require.Equal(t, 0, d.provider.GetDestroyCalls(), "must not destroy when finalize CAS loses the race")
	require.Equal(t, 1, d.sessions.finalizeCalls)
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
		ID:             runID,
		PrimaryIssueID: &issueID,
		OrgID:          orgID,
		AgentType:      "claude_code",
		Status:         "pending",
		TokenMode:      "low",
		PMApproach:     &pmApproach,
		PMReasoning:    &pmReasoning,
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
		Credentials: &mockCredentialProvider{
			byProvider: map[models.ProviderName]*models.DecryptedCredential{
				models.ProviderAnthropic: {
					Provider: models.ProviderAnthropic,
					Config:   models.AnthropicConfig{APIKey: "sk-ant-pm-ctx-test"},
				},
			},
		},
		Logger: zerolog.Nop(),
	})

	err := orchestrator.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed")
	require.NotNil(t, capAdapter.captured, "adapter should capture input")
	require.NotNil(t, capAdapter.captured.PMContext, "PMContext should be populated")
	require.Equal(t, pmApproach, capAdapter.captured.PMContext.Approach, "PMContext should include approach")
	require.Equal(t, pmReasoning, capAdapter.captured.PMContext.Reasoning, "PMContext should include reasoning")
	require.WithinDuration(t, now, time.Now(), time.Minute, "sanity check")
}

func TestRunAgent_LegacySyntheticManualSessionUsesManualModeAndFallbackReferences(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repo := testRepo(orgID)
	issueID := uuid.New()
	description := "Investigate the session composer"
	rawData := []byte(`{"manual_session":true,"references":[{"kind":"file","token":"@internal/api/handlers/sessions.go","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"},{"kind":"directory","token":"@frontend/src/app/(dashboard)/sessions/new","path":"frontend/src/app/(dashboard)/sessions/new","display":"frontend/src/app/(dashboard)/sessions/new"}]}`)

	run := &models.Session{
		ID:             uuid.New(),
		PrimaryIssueID: &issueID,
		OrgID:          orgID,
		AgentType:      "claude_code",
		Status:         "pending",
		TokenMode:      "low",
		RepositoryID:   &repo.ID,
	}

	mockRuns := &mockSessionStore{}
	mockIssues := &mockIssueStore{issue: models.Issue{
		ID:           issueID,
		OrgID:        orgID,
		Source:       models.IssueSourceManual,
		RepositoryID: &repo.ID,
		Title:        "Manual session",
		Description:  &description,
		RawData:      rawData,
	}}
	mockRepos := &mockRepositoryStore{repo: repo}
	mockOrgs := &mockOrgStore{org: models.Organization{ID: orgID}}
	mockJobs := &mockJobStore{}
	mockLogs := &mockSessionLogStore{}
	mockQuestions := &mockSessionQuestionStore{}
	mockMessages := &mockSessionMessageStore{}
	mockDecisions := &mockDecisionLogStore{}
	mockGitHub := &mockGitHubTokenProvider{token: "ghp_test123"}
	mockSnapshots := &mockSnapshotStore{}
	sandboxProvider := testutil.NewMockSandboxProvider()
	sandboxProvider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("snapshot-bytes"))), nil
	}
	capAdapter := &capturingAdapter{name: models.AgentTypeClaudeCode}

	orchestrator := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         sandboxProvider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypeClaudeCode: capAdapter},
		Sessions:         mockRuns,
		SessionLogs:      mockLogs,
		SessionQuestions: mockQuestions,
		SessionMessages:  mockMessages,
		DecisionLog:      mockDecisions,
		Issues:           mockIssues,
		Repositories:     mockRepos,
		Orgs:             mockOrgs,
		Jobs:             mockJobs,
		GitHub:           mockGitHub,
		Credentials: &mockCredentialProvider{
			byProvider: map[models.ProviderName]*models.DecryptedCredential{
				models.ProviderAnthropic: {
					Provider: models.ProviderAnthropic,
					Config:   models.AnthropicConfig{APIKey: "sk-ant-manual-ref-test"},
				},
			},
		},
		Snapshots: mockSnapshots,
		Logger:    zerolog.Nop(),
	})

	err := orchestrator.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed for legacy synthetic manual sessions")
	require.NotNil(t, capAdapter.captured, "adapter should capture input")
	require.True(t, capAdapter.captured.Manual, "legacy synthetic manual sessions should still run in manual mode")
	require.Len(t, capAdapter.captured.References, 2, "legacy synthetic manual sessions should recover references from manual issue raw_data when message refs are absent")
	require.Equal(t, "internal/api/handlers/sessions.go", capAdapter.captured.References[0].Path, "file path should be preserved")
	require.Equal(t, models.SessionInputReferenceKindDirectory, capAdapter.captured.References[1].Kind, "directory references should remain typed")
	require.Len(t, mockRuns.turnUpdates, 1, "legacy synthetic manual sessions should complete an interactive turn and return to idle")
	require.Empty(t, mockRuns.resultUpdates, "legacy synthetic manual sessions should not be finalized as single-run sessions")
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

func TestRunAgent_CapturesAndPersistsBaseCommitSHA(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if cmd == "git rev-parse HEAD" {
			_, _ = io.WriteString(stdout, "abc123\n")
		}
		return 0, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed")
	require.Equal(t, []string{"abc123"}, d.sessions.getBaseCommitSHAs(), "RunAgent should persist the captured base commit sha")
	require.NotNil(t, run.BaseCommitSHA, "RunAgent should populate the in-memory session base commit sha")
	require.Equal(t, "abc123", *run.BaseCommitSHA, "RunAgent should store the captured base commit sha on the session")
}

func TestRunAgent_BaseCommitCaptureNonZeroExitDoesNotFailRun(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if cmd == "git rev-parse HEAD" {
			_, _ = io.WriteString(stderr, "fatal")
			return 1, nil
		}
		return 0, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should continue when base commit capture fails")
	require.Nil(t, run.BaseCommitSHA, "RunAgent should leave BaseCommitSHA unset when capture fails")
	require.Empty(t, d.sessions.getBaseCommitSHAs(), "RunAgent should not persist a base commit when capture fails")
}

func TestContinueSession_UsesBuildRunResultInUpdateTurnComplete(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	agentSessionID := "existing-agent-session"
	snapshotKey := "existing-snapshot"
	session.Status = string(models.SessionStatusIdle)
	session.AgentSessionID = &agentSessionID
	session.SnapshotKey = &snapshotKey
	session.CurrentTurn = 1

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{{
		ID:         1,
		SessionID:  session.ID,
		OrgID:      orgID,
		TurnNumber: 2,
		Role:       models.MessageRoleUser,
		Content:    "Please continue the work.",
	}}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.snapshots.data = map[string][]byte{
		snapshotKey: []byte("restored-snapshot"),
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Equal(t, "Please continue the work.", prompt.UserMessage, "ContinueSession should use the latest user message")
		return &agent.AgentResult{
			Summary:             "done",
			Diff:                "--- a/main.go\n+++ b/main.go\n",
			ConfidenceScore:     0.8,
			ConfidenceReasoning: "looks good",
			RiskFactors:         []string{"low"},
			TokenUsage:          agent.TokenUsage{InputTokens: 1, OutputTokens: 2, TotalCostUSD: 0.01},
			AgentSessionID:      "",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.NoError(t, err, "ContinueSession should succeed")

	updates := d.sessions.getTurnUpdates()
	require.Len(t, updates, 1, "ContinueSession should persist exactly one turn update")
	require.Equal(t, 2, updates[0].turn, "ContinueSession should increment the turn number")
	require.Equal(t, agentSessionID, updates[0].agentSessionID, "ContinueSession should reuse the existing agent session id when the adapter does not return one")
	require.NotEmpty(t, updates[0].snapshotKey, "ContinueSession should persist a snapshot key")
	require.NotNil(t, updates[0].result, "ContinueSession should build a session result for UpdateTurnComplete")
	require.NotNil(t, updates[0].result.Diff, "ContinueSession should pass the diff through to UpdateTurnComplete")
}

func TestContinueSession_RepairedSlashCommandsOnReusePath(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-abc"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)

	missingCommand := models.SessionInputCommand{
		Kind:      "command",
		AgentType: models.AgentTypeClaudeCode,
		Name:      "review",
		Token:     "/review",
		Display:   "/review",
		Arguments: "security",
		Source:    models.SessionInputCommandSourceBuiltin,
	}

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "follow-up",
			Commands:   models.SessionInputCommands{missingCommand},
		},
	}
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not be called on the reuse path")
		return nil, nil
	}
	d.provider.RestoreFn = func(context.Context, *agent.Sandbox, io.Reader) error {
		t.Fatalf("provider.Restore must not be called on the reuse path")
		return nil
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("snap"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Equal(
			t,
			agent.EnsureSlashCommandsInPrompt("follow-up", []models.SessionInputCommand{missingCommand}),
			prompt.UserMessage,
			"ContinueSession should repair slash commands before executing a reused session",
		)
		return &agent.AgentResult{
			Summary:             "done",
			ConfidenceScore:     0.9,
			ConfidenceReasoning: "ok",
			ExitCode:            0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.NoError(t, err, "ContinueSession should succeed on the reuse path when slash commands need repair")
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

func TestRunAgent_ClaudeSubscriptionInjectsCredentialsFile(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	credID := uuid.New()
	expiresAt := time.Now().Add(45 * time.Minute)
	d.claudeCodeAuth = &mockClaudeCodeAuthProvider{
		hasSub: true,
		credID: &credID,
		sub: &models.AnthropicSubscription{
			AccessToken:   "sub-access-1",
			RefreshToken:  "sub-refresh-1",
			ExpiresAt:     expiresAt,
			AccountType:   "claude_max",
			RateLimitTier: "default_claude_max_20x",
			Scopes:        []string{"user:profile", "user:inference", "user:sessions:claude_code"},
		},
	}

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "sub-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "run should succeed with a Claude subscription")

	// Keep the API key in env as a fallback if token refresh later fails.
	// The Claude Code CLI should still prefer the credentials file we wrote.
	require.Equal(t, "sk-ant-default-test", capturedCfg.Env["ANTHROPIC_API_KEY"],
		"Anthropic API key should remain available as a fallback when a subscription is present")

	// Credentials file was written to the expected path with the CLI's schema.
	credsPath := "/home/sandbox/.claude/.credentials.json"
	credsData, ok := d.provider.Files[credsPath]
	require.True(t, ok, ".credentials.json should be written under ~/.claude")

	var credsJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(credsData, &credsJSON))
	oauth, ok := credsJSON["claudeAiOauth"].(map[string]interface{})
	require.True(t, ok, "credentials file should have claudeAiOauth object")
	require.Equal(t, "sub-access-1", oauth["accessToken"])
	require.Equal(t, "sub-refresh-1", oauth["refreshToken"])
	require.Equal(t, "claude_max", oauth["subscriptionType"])
	require.Equal(t, "default_claude_max_20x", oauth["rateLimitTier"])
	require.EqualValues(t, expiresAt.UnixMilli(), oauth["expiresAt"], "expiresAt should be millis-since-epoch")
	scopes, ok := oauth["scopes"].([]interface{})
	require.True(t, ok, "scopes should be an array")
	require.Len(t, scopes, 3)

	// The credentials file must be pre-created at 0600 in the same Exec that
	// mkdirs ~/.claude, so the subsequent WriteFile (which uses `>`) inherits
	// the locked-down mode instead of briefly existing at the shell's default
	// umask. The CLI refuses a world-readable token file.
	require.Contains(t, d.provider.ExecCalls,
		"mkdir -p '/home/sandbox/.claude' && install -m 600 /dev/null '/home/sandbox/.claude/.credentials.json'",
		"should create ~/.claude and pre-create the credentials file with mode 0600 in a single command")
}

func TestRunAgent_ClaudeSubscriptionTokenFailureFallsBackToAPIKey(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.claudeCodeAuth = &mockClaudeCodeAuthProvider{
		hasSub:   true,
		tokenErr: errors.New("refresh failed"),
	}

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "fallback-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "run should fall back to the Anthropic API key when subscription token lookup fails")

	require.Equal(t, "sk-ant-default-test", capturedCfg.Env["ANTHROPIC_API_KEY"],
		"Anthropic API key should be injected into the sandbox as a fallback")
	_, wroteCredsFile := d.provider.Files["/home/sandbox/.claude/.credentials.json"]
	require.False(t, wroteCredsFile, "credentials file should not be written when subscription token lookup fails")
	require.Len(t, d.sessions.getFailureUpdates(), 0, "fallback to API key should not mark the run as failed")
}

func TestRunAgent_NoAgentEnvForUnknownType(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	// Override: no credential configured at all so we can assert on the
	// sandbox env and the fail-fast path.
	d.creds = &mockCredentialProvider{}

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "no-env-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
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
	// Without any Claude subscription or Anthropic API key, ensureClaudeCodeAuth
	// fails the run fast. The sandbox is still created (capturedCfg populated)
	// before the auth check runs.
	require.Error(t, err)
	require.Contains(t, err.Error(), "no credentials for claude code agent")

	// Sandbox config was captured before the auth check failed. Confirm no
	// ANTHROPIC_API_KEY was injected since no credential was configured.
	require.NotContains(t, capturedCfg.Env, "ANTHROPIC_API_KEY",
		"sandbox config should not have agent-specific env for unconfigured agent type")
	require.Equal(t, "/home/sandbox", capturedCfg.Env["HOME"],
		"HOME should always be set to the sandbox user's home dir")

	// The failure should be categorized as claude_code_auth.
	failures := d.sessions.getFailureUpdates()
	require.Len(t, failures, 1)
	require.Equal(t, string(agent.FailureCategoryClaudeCodeAuth), failures[0].category)
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
		"mkdir -p '/home/sandbox/.codex'",
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
	run.Origin = models.SessionOriginManual
	run.InteractionMode = models.SessionInteractionModeInteractive
	run.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd

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
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session.tar")
	session.ReasoningEffort = func() *models.ReasoningEffort {
		effort := models.ReasoningEffortHigh
		return &effort
	}()

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
		require.Equal(t, models.ReasoningEffortHigh, prompt.ReasoningEffort, "continue_session should preserve the stored reasoning effort on snapshot-backed turns")
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

func TestContinueSession_FreshResumeClaudeTokenFailureFallsBackToAPIKey(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = nil
	session.ReasoningEffort = func() *models.ReasoningEffort {
		effort := models.ReasoningEffortMedium
		return &effort
	}()

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please keep going without the old snapshot.",
		},
	}
	d.claudeCodeAuth = &mockClaudeCodeAuthProvider{
		hasSub:   true,
		tokenErr: errors.New("refresh failed"),
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.False(t, prompt.Continuation, "fresh resume should rebuild context instead of using continuation mode")
		require.Contains(t, prompt.UserPrompt, "Please keep going without the old snapshot.", "fresh resume should include the latest user message in the rebuilt prompt")
		require.Equal(t, models.ReasoningEffortMedium, prompt.ReasoningEffort, "fresh resume should preserve the stored reasoning effort when rebuilding the prompt")
		return &agent.AgentResult{
			Summary:         "continued from fallback auth",
			ConfidenceScore: 0.8,
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.NoError(t, err, "fresh resume should fall back to the Anthropic API key when Claude token refresh fails")
	require.Len(t, d.sessions.getFailureUpdates(), 0, "API-key fallback should not mark the resumed session as failed")
	_, wroteCredsFile := d.provider.Files["/home/sandbox/.claude/.credentials.json"]
	require.False(t, wroteCredsFile, "fresh resume should not write Claude subscription credentials when token refresh fails")
}

func TestRunAgent_OmitsReasoningEffortWhenUnset(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.ReasoningEffort = nil

	d := defaultDeps()
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Equal(t, models.ReasoningEffort(""), prompt.ReasoningEffort, "RunAgent should leave reasoning effort empty when no override is configured")
		return &agent.AgentResult{
			Summary:         "ok",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed without a reasoning override")
}

func TestContinueSession_OmitsReasoningEffortWhenUnset(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session-nil-effort.tar")
	session.ReasoningEffort = nil

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue.",
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
		require.Equal(t, models.ReasoningEffort(""), prompt.ReasoningEffort, "ContinueSession should leave reasoning effort empty when the stored session has no override")
		return &agent.AgentResult{
			Summary:         "continued",
			ConfidenceScore: 0.81,
			ExitCode:        0,
		}, nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session)
	require.NoError(t, err, "ContinueSession should succeed without a reasoning override")
}

func TestContinueSession_ClaudeTokenFailureRemovesStaleCredentialsBeforeAPIKeyFallback(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
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
			Content:    "Resume from the saved work.",
		},
	}
	d.claudeCodeAuth = &mockClaudeCodeAuthProvider{
		hasSub:   true,
		tokenErr: errors.New("refresh failed"),
	}
	d.provider.Files["/home/sandbox/.claude/.credentials.json"] = []byte(`{"stale":true}`)
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if cmd == "rm -f '/home/sandbox/.claude/.credentials.json'" {
			delete(d.provider.Files, "/home/sandbox/.claude/.credentials.json")
		}
		return 0, nil
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.True(t, prompt.Continuation, "snapshot-backed resume should still use continuation mode")
		return &agent.AgentResult{
			Summary:         "continued after deleting stale creds",
			ConfidenceScore: 0.84,
			ExitCode:        0,
		}, nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.NoError(t, err, "snapshot resume should fall back to API key after removing stale Claude credentials")
	require.Contains(t, d.provider.ExecCalls, "rm -f '/home/sandbox/.claude/.credentials.json'", "fallback should delete stale Claude credentials before relying on the API key")
	_, credsStillPresent := d.provider.Files["/home/sandbox/.claude/.credentials.json"]
	require.False(t, credsStillPresent, "stale Claude credentials should be removed before API-key fallback continues")
	require.Len(t, d.sessions.getFailureUpdates(), 0, "successful fallback should not record a Claude auth failure")
}

// errForcedCreateFailure is used by tests that short-circuit provider.Create so
// the test can inspect the SandboxConfig without running the full sandbox
// lifecycle. Named so grep picks it up and future refactors don't silently
// change behavior if Create's call site moves.
var errForcedCreateFailure = errors.New("forced create failure to short-circuit test")

// TestContinueSession_ClearsReviewContextAfterConsumption guarantees the
// one-shot review directive is wiped from sessions.revision_context before
// (or during) the turn so the next user message can't be silently swapped
// for /review again. Persists immediately on read; even if the agent run
// fails or is retried, the user retries by clicking the button — never by
// re-firing the same stale directive against an unrelated message.
func TestContinueSession_ClearsReviewContextAfterConsumption(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-abc"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)
	// Persist a review-only RevisionContext.
	reviewCtxJSON, err := json.Marshal(agent.RevisionContext{
		ReviewContext: &agent.SessionReviewContext{
			Mode:           models.SessionReviewModeDefault,
			RequestSummary: "user clicked Review",
		},
	})
	require.NoError(t, err)
	session.RevisionContext = reviewCtxJSON

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please review your changes.",
		},
	}
	// Capture the prompt the adapter sees so we can assert the review
	// context survived the threading even though it was cleared from the
	// persisted row.
	var promptSeen *agent.AgentPrompt
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		promptSeen = prompt
		return &agent.AgentResult{Summary: "done", ExitCode: 0}, nil
	}
	d.sessions.releaseHoldFn = func() (bool, string, error) { return false, existing, nil }

	orch := buildOrchestrator(d)
	require.NoError(t, orch.ContinueSession(context.Background(), session))

	require.NotNil(t, promptSeen, "adapter should have run")
	require.True(t, promptSeen.IsReview(), "the in-memory prompt must still see the review directive even after the row is cleared")

	d.sessions.mu.Lock()
	defer d.sessions.mu.Unlock()
	require.NotEmpty(t, d.sessions.revisionContextUpdates, "orchestrator must call UpdateRevisionContext to clear the consumed review directive")
	// Review-only context: the cleared write should be a nil/empty payload.
	last := d.sessions.revisionContextUpdates[len(d.sessions.revisionContextUpdates)-1]
	require.True(t, len(last) == 0, "review-only context should clear the row entirely (got %q)", string(last))
}

func TestContinueSession_AppendsNonReviewRevisionContextToUserMessage(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-append"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)

	revisionContextJSON, err := json.Marshal(agent.RevisionContext{
		FormattedFeedback: "Please address the code review notes.",
		CommentSummary:    "One unresolved comment remains.",
		PreviousDiff:      "--- a/foo\n+++ b/foo\n",
	})
	require.NoError(t, err, "json.Marshal should encode the non-review revision context")
	session.RevisionContext = revisionContextJSON

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue.",
		},
	}

	var promptSeen *agent.AgentPrompt
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		promptSeen = prompt
		return &agent.AgentResult{Summary: "done", ExitCode: 0}, nil
	}
	d.sessions.releaseHoldFn = func() (bool, string, error) { return false, existing, nil }

	err = buildOrchestrator(d).ContinueSession(context.Background(), session)
	require.NoError(t, err, "ContinueSession should preserve the normal continuation path for non-review revision metadata")
	require.NotNil(t, promptSeen, "ContinueSession should execute the adapter for resumable sessions")
	require.Contains(t, promptSeen.UserMessage, "## Revision context", "ContinueSession should append formatted revision context to the user's continuation message")
	require.Contains(t, promptSeen.UserMessage, "One unresolved comment remains.", "ContinueSession should carry the revision summary into the adapter prompt")
}

func TestContinueSession_IgnoresMalformedRevisionContext(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-invalid-revision"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)
	session.RevisionContext = json.RawMessage(`{"review_context":`)

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue.",
		},
	}

	var promptSeen *agent.AgentPrompt
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		promptSeen = prompt
		return &agent.AgentResult{Summary: "done", ExitCode: 0}, nil
	}
	d.sessions.releaseHoldFn = func() (bool, string, error) { return false, existing, nil }

	err := buildOrchestrator(d).ContinueSession(context.Background(), session)
	require.NoError(t, err, "ContinueSession should keep running even when the persisted revision context is malformed")
	require.NotNil(t, promptSeen, "ContinueSession should still invoke the adapter after discarding malformed revision context")
	require.Nil(t, promptSeen.RevisionContext, "ContinueSession should drop malformed revision context instead of propagating corrupt JSON into the adapter")
	require.NotContains(t, promptSeen.UserMessage, "## Revision context", "ContinueSession should not append revision framing when the revision context could not be parsed")
}

func TestContinueSession_UpdateRevisionContextErrorDoesNotBlockReviewTurn(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-update-failure"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)

	reviewCtxJSON, err := json.Marshal(agent.RevisionContext{
		ReviewContext: &agent.SessionReviewContext{
			Mode:           models.SessionReviewModeSecurity,
			RequestSummary: "user clicked Security review",
		},
	})
	require.NoError(t, err, "json.Marshal should encode the review revision context")
	session.RevisionContext = reviewCtxJSON

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please review your changes.",
		},
	}
	d.sessions.updateRevisionErr = errors.New("write failed")

	var promptSeen *agent.AgentPrompt
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		promptSeen = prompt
		return &agent.AgentResult{Summary: "done", ExitCode: 0}, nil
	}
	d.sessions.releaseHoldFn = func() (bool, string, error) { return false, existing, nil }

	err = buildOrchestrator(d).ContinueSession(context.Background(), session)
	require.NoError(t, err, "ContinueSession should not fail the review turn when clearing the consumed review context is best-effort")
	require.NotNil(t, promptSeen, "ContinueSession should still invoke the adapter when UpdateRevisionContext fails")
	require.True(t, promptSeen.IsReview(), "ContinueSession should preserve the in-memory review directive even if the persisted clear fails")
}

// TestContinueSession_ReusesExistingContainer covers the branch where
// session.ContainerID is populated (a preview hydrated the sandbox) so
// continueSessionTurn skips Create/Restore and attaches to it by ID
// (orchestrator.go:849-859). It also exercises the preview-holds-on
// release branch so the container is left alive (orchestrator.go:924-927).
func TestContinueSession_ReusesExistingContainer(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-abc"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)
	// No SnapshotKey: proves the reuse branch takes precedence over hydrate.

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "follow-up",
		},
	}
	// Fail the test if Create runs — reuse must skip it.
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not be called on the reuse path")
		return nil, nil
	}
	d.provider.RestoreFn = func(context.Context, *agent.Sandbox, io.Reader) error {
		t.Fatalf("provider.Restore must not be called on the reuse path")
		return nil
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("snap"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Equal(t, existing, sandbox.ID, "adapter must run against the reused container")
		return &agent.AgentResult{
			Summary:             "done",
			ConfidenceScore:     0.9,
			ConfidenceReasoning: "ok",
			ExitCode:            0,
		}, nil
	}
	// Preview still holds the container after this turn ends.
	d.sessions.releaseHoldFn = func() (bool, string, error) {
		return false, existing, nil
	}

	orch := buildOrchestrator(d)
	require.NoError(t, orch.ContinueSession(context.Background(), session))

	require.Equal(t, 0, d.provider.GetDestroyCalls(), "sandbox must stay alive while preview holds it")
	require.Equal(t, 0, d.sessions.finalizeCalls, "FinalizeContainerDestroy must not run while preview holds")
	require.GreaterOrEqual(t, d.sessions.acquireHoldCalls, 1)
	require.GreaterOrEqual(t, d.sessions.releaseHoldCalls, 1)
}

// TestContinueSession_AcquireHoldErrorFailsTurn covers the branch where
// AcquireTurnHold errors after fresh sandbox creation: we must destroy the
// local sandbox and fail the turn rather than leaking a container that
// has no container_id row reference for the reconciler.
func TestContinueSession_AcquireHoldErrorFailsTurn(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	// No ContainerID and no SnapshotKey — forces the Create path, which is
	// the leak-prone branch: on hold error we must tear the new container down.

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "retry me"},
	}
	d.sessions.acquireHoldErr = errors.New("db write failed")

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.Error(t, err)
	require.Contains(t, err.Error(), "acquire turn hold")

	require.Equal(t, 1, d.sessions.acquireHoldCalls)
	require.Equal(t, 1, d.provider.GetDestroyCalls(), "must destroy the fresh sandbox to avoid a leak")
	require.Equal(t, 0, d.sessions.releaseHoldCalls)
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusIdle), "must revert to idle on hold error")
}

func TestContinueSession_SetWorkerNodeIDFailureFailsTurn(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1

	d := defaultDeps()
	d.nodeID = "worker-a"
	d.sessions.setWorkerNodeErr = errors.New("persist failed")
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "retry me"},
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.Error(t, err, "ContinueSession should fail when session worker ownership cannot be persisted")
	require.Contains(t, err.Error(), "persist session worker ownership", "ContinueSession should surface the worker ownership persistence failure")
	require.Equal(t, 1, d.provider.GetDestroyCalls(), "ContinueSession should destroy the sandbox when worker ownership persistence fails")
	require.Equal(t, 1, d.sessions.releaseHoldCalls, "ContinueSession should release the turn hold when worker ownership persistence fails")
	require.Equal(t, 1, d.sessions.finalizeCalls, "ContinueSession should finalize the container destroy when worker ownership persistence fails")
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusIdle), "ContinueSession should revert the session to idle when worker ownership persistence fails")
}

// TestContinueSession_SessionRepoSlug exercises every branch of
// sessionRepoSlug through ContinueSession.
//   - "session with repo" must produce the slug-derived WorkDir and then fail
//     at provider.Create.
//   - "session without repo" must fall back to the default WorkDir and fail at
//     Create — the primary issue's repository is NOT consulted; sessions.repository_id
//     is the canonical source of truth after the Phase 2 decoupling.
//   - "repo fetch failure" must hard-fail (resolve workdir error) WITHOUT ever
//     calling Create — otherwise we'd risk running the agent in /workspace
//     while the snapshot tar restored files under /home/sandbox/<slug>.
func TestContinueSession_SessionRepoSlug(t *testing.T) {
	t.Parallel()

	const defaultWorkDir = "/workspace"
	const slugWorkDir = "/home/sandbox/backend"

	type tc struct {
		name         string
		prepSession  func(s *models.Session)
		prepDeps     func(d testDeps)
		wantWorkDir  string // "" means Create must not be invoked
		wantErrMatch string
	}

	cases := []tc{
		{
			name:         "session with repo uses slug-derived workdir",
			prepSession:  func(s *models.Session) {},
			prepDeps:     func(d testDeps) {},
			wantWorkDir:  slugWorkDir,
			wantErrMatch: "create sandbox",
		},
		{
			name: "session without repo uses default workdir",
			prepSession: func(s *models.Session) {
				s.RepositoryID = nil
			},
			prepDeps:     func(d testDeps) {},
			wantWorkDir:  defaultWorkDir,
			wantErrMatch: "create sandbox",
		},
		{
			name:        "repo fetch failure hard-fails resume",
			prepSession: func(s *models.Session) {},
			prepDeps: func(d testDeps) {
				d.repos.err = errors.New("db flaky")
			},
			wantWorkDir:  "",
			wantErrMatch: "resolve workdir",
		},
	}

	for _, c := range cases {
		c := c
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
			var createCalls int
			d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
				createCalls++
				gotWorkDir = cfg.WorkDir
				return nil, errForcedCreateFailure
			}

			orch := buildOrchestrator(d)
			err := orch.ContinueSession(context.Background(), session)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.wantErrMatch)
			if c.wantWorkDir == "" {
				require.Zero(t, createCalls, "resume must not create a sandbox when repo lookup fails")
				return
			}
			require.Equal(t, 1, createCalls, "Create should be called exactly once when workdir resolution succeeds")
			require.Equal(t, c.wantWorkDir, gotWorkDir, "sessionRepoSlug should drive WorkDir selection")
		})
	}
}

// TestContinueSession_ErrorMessageDeferredToDeadLetterHook locks in the
// behavior that when sandbox preparation fails, ContinueSession does NOT
// post the user-visible assistant message inline. Instead it registers a
// dead-letter hook that the worker runs only when it decides to stop
// retrying — so a mid-retry attempt stays silent, but a dead-lettered job
// (for any reason: FatalError, retryable timeout, retries exhausted)
// produces exactly one message. Exercised for both the sandbox-creation
// branch and the sibling workdir-resolution branch, plus the direct
// caller case (no registry on ctx).
func TestContinueSession_ErrorMessageDeferredToDeadLetterHook(t *testing.T) {
	t.Parallel()

	type failureMode int
	const (
		sandboxCreateFailure failureMode = iota
		workdirResolveFailure
	)

	cases := []struct {
		name              string
		failure           failureMode
		withRegistry      bool
		runHooks          bool
		wantMessageInline bool
		wantMessageAfter  bool
		wantErrMatch      string
	}{
		{
			name:              "sandbox-create: direct caller without registry posts nothing",
			failure:           sandboxCreateFailure,
			withRegistry:      false,
			wantMessageInline: false,
			wantMessageAfter:  false,
			wantErrMatch:      "create sandbox",
		},
		{
			name:              "sandbox-create: mid-retry (registry present, hooks not fired) stays silent",
			failure:           sandboxCreateFailure,
			withRegistry:      true,
			runHooks:          false,
			wantMessageInline: false,
			wantMessageAfter:  false,
			wantErrMatch:      "create sandbox",
		},
		{
			name:              "sandbox-create: dead-letter (hooks fired) posts exactly one message",
			failure:           sandboxCreateFailure,
			withRegistry:      true,
			runHooks:          true,
			wantMessageInline: false,
			wantMessageAfter:  true,
			wantErrMatch:      "create sandbox",
		},
		{
			name:              "workdir-resolve: mid-retry stays silent",
			failure:           workdirResolveFailure,
			withRegistry:      true,
			runHooks:          false,
			wantMessageInline: false,
			wantMessageAfter:  false,
			wantErrMatch:      "resolve workdir",
		},
		{
			name:              "workdir-resolve: dead-letter posts exactly one message",
			failure:           workdirResolveFailure,
			withRegistry:      true,
			runHooks:          true,
			wantMessageInline: false,
			wantMessageAfter:  true,
			wantErrMatch:      "resolve workdir",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			session := testRun(orgID, issue.ID)
			session.Status = string(models.SessionStatusIdle)
			session.CurrentTurn = 1

			d := defaultDeps()
			d.issues.issue = issue

			// Pre-populate a user message so ContinueSession reaches the
			// failure point we're exercising.
			d.messages.messages = []models.SessionMessage{{
				ID:         1,
				SessionID:  session.ID,
				OrgID:      orgID,
				TurnNumber: 2,
				Role:       models.MessageRoleUser,
				Content:    "continue please",
			}}

			switch c.failure {
			case sandboxCreateFailure:
				d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
					return nil, errForcedCreateFailure
				}
			case workdirResolveFailure:
				// Force sessionRepoSlug to fail at the repo lookup.
				d.repos.err = errors.New("db flaky")
			}

			ctx := context.Background()
			if c.withRegistry {
				ctx = jobctx.WithDeadLetterHooks(ctx)
			}

			orch := buildOrchestrator(d)
			err := orch.ContinueSession(ctx, session)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.wantErrMatch)

			countAssistantMessages := func() int {
				var n int
				for _, m := range d.messages.getMessages() {
					if m.Role == models.MessageRoleAssistant && m.SessionID == session.ID {
						n++
					}
				}
				return n
			}

			if c.wantMessageInline {
				require.Equal(t, 1, countAssistantMessages(), "inline assistant message expected before hook runs")
			} else {
				require.Zero(t, countAssistantMessages(), "no assistant message should be posted inline")
			}

			if c.runHooks {
				jobctx.RunDeadLetterHooks(ctx, err)
			}

			if c.wantMessageAfter {
				require.Equal(t, 1, countAssistantMessages(), "dead-letter hook should post exactly one assistant message")
			} else {
				require.Zero(t, countAssistantMessages(), "no assistant message should be posted when hook does not fire")
			}
		})
	}
}

// TestContinueSession_DeadLetterHookIdempotent ensures repeated invocations
// of the hook registry — which can happen if multiple worker dead-letter
// branches coordinate or tests drive it defensively — still produce only
// one user-visible message.
func TestContinueSession_DeadLetterHookIdempotent(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{{
		ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2,
		Role: models.MessageRoleUser, Content: "continue please",
	}}
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		return nil, errForcedCreateFailure
	}

	ctx := jobctx.WithDeadLetterHooks(context.Background())
	orch := buildOrchestrator(d)
	err := orch.ContinueSession(ctx, session)
	require.Error(t, err)

	jobctx.RunDeadLetterHooks(ctx, err)
	jobctx.RunDeadLetterHooks(ctx, err)

	var assistantMessages int
	for _, m := range d.messages.getMessages() {
		if m.Role == models.MessageRoleAssistant && m.SessionID == session.ID {
			assistantMessages++
		}
	}
	require.Equal(t, 1, assistantMessages, "repeated RunDeadLetterHooks must not produce duplicate messages")
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
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
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

// --- ResolveSessionTimeout ---

func TestResolveSessionTimeout_UsesOrgOverride(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	// 10 min override — inside [min, max] so ParseOrgSettings leaves it alone.
	overrideSeconds := 10 * 60
	settings, err := json.Marshal(map[string]any{
		"max_session_duration_seconds": overrideSeconds,
	})
	require.NoError(t, err)

	d := defaultDeps()
	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
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
		Orgs:             &mockOrgStore{org: models.Organization{ID: orgID, Settings: settings}},
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Snapshots:        d.snapshots,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	got := orch.ResolveSessionTimeout(context.Background(), orgID)
	require.Equal(t, 10*time.Minute, got)
}

func TestResolveSessionTimeout_ClampsBelowFloor(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	// 30 seconds — below MinMaxSessionDurationSeconds (120s). ParseOrgSettings
	// should clamp up to 2 minutes.
	settings, err := json.Marshal(map[string]any{
		"max_session_duration_seconds": 30,
	})
	require.NoError(t, err)

	d := defaultDeps()
	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
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
		Orgs:             &mockOrgStore{org: models.Organization{ID: orgID, Settings: settings}},
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Snapshots:        d.snapshots,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	got := orch.ResolveSessionTimeout(context.Background(), orgID)
	require.Equal(t, 2*time.Minute, got)
}

func TestResolveSessionTimeout_FallsBackWhenOrgStoreErrors(t *testing.T) {
	t.Parallel()

	orgID := testOrg()

	d := defaultDeps()
	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
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
		Orgs:             &mockOrgStore{err: errors.New("db down")},
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Snapshots:        d.snapshots,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	got := orch.ResolveSessionTimeout(context.Background(), orgID)
	require.Equal(t, agent.DefaultSandboxTimeout, got)
}

func TestResolveSessionTimeout_FallsBackWhenOrgStoreNil(t *testing.T) {
	t.Parallel()

	d := defaultDeps()
	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
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
		// Orgs intentionally nil.
		Jobs:          d.jobs,
		GitHub:        d.github,
		Credentials:   d.creds,
		Snapshots:     d.snapshots,
		Logger:        zerolog.Nop(),
		MaxConcurrent: 3,
	})

	got := orch.ResolveSessionTimeout(context.Background(), testOrg())
	require.Equal(t, agent.DefaultSandboxTimeout, got)
}

// --- DeadlineExceeded handling in RunAgent / ContinueSession ---

func TestRunAgent_DeadlineExceededClassifiesAsTimeout(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	startedAt := time.Now().Add(-10 * time.Minute)
	run.StartedAt = &startedAt

	d := defaultDeps()
	// Simulate the adapter returning after ctx has already expired — the
	// orchestrator should detect ctx.Err() == DeadlineExceeded and route
	// into the timeout branch.
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return nil, context.DeadlineExceeded
	}

	orch := buildOrchestrator(d)

	// Pass a context that's already past its deadline.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	err := orch.RunAgent(ctx, run)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSessionTimedOut)

	// failRun persists status via UpdateResult, not UpdateStatus.
	results := d.sessions.getResultUpdates()
	foundFailed := false
	for _, r := range results {
		if r.status == "failed" {
			foundFailed = true
			require.NotNil(t, r.result)
			require.NotNil(t, r.result.Error)
			require.Contains(t, *r.result.Error, "Session timed out")
		}
	}
	require.True(t, foundFailed, "session should have been marked failed via UpdateResult")

	// Classification should be set directly (no analyze_failure enqueue)
	// so the UI sees a timeout category without round-tripping through the
	// async classifier.
	require.NotContains(t, d.jobs.getEnqueued(), "analyze_failure",
		"timeout path classifies explicitly; analyze_failure should not be enqueued")
	failures := d.sessions.getFailureUpdates()
	require.Len(t, failures, 1)
	require.Equal(t, agent.FailureCategoryTimeout, failures[0].category)
	require.True(t, failures[0].retryAdvised)
}

func TestContinueSession_DeadlineExceededClassifiesAsTimeout(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session.tar")
	startedAt := time.Now().Add(-10 * time.Minute)
	session.StartedAt = &startedAt

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Keep going.",
		},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return nil, context.DeadlineExceeded
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	orch := buildOrchestrator(d)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	err := orch.ContinueSession(ctx, session)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSessionTimedOut)

	require.NotContains(t, d.jobs.getEnqueued(), "analyze_failure",
		"timeout path classifies explicitly; analyze_failure should not be enqueued")
	failures := d.sessions.getFailureUpdates()
	require.Len(t, failures, 1)
	require.Equal(t, agent.FailureCategoryTimeout, failures[0].category)
	require.True(t, failures[0].retryAdvised)
}

func TestRunAgent_GracefullyStopsAndPreservesCheckpointOnNoProgress(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	settings, err := json.Marshal(map[string]any{
		"max_session_duration_seconds": 30,
		"runtime_budgets": map[string]any{
			"no_progress_timeout_seconds":            1,
			"graceful_shutdown_window_seconds":       1,
			"checkpoint_finalization_window_seconds": 1,
			"automatic_extension_seconds":            2,
			"max_automatic_extension_seconds":        2,
			"absolute_runtime_ceiling_seconds":       5,
		},
	})
	require.NoError(t, err, "runtime settings JSON should marshal")

	cancelReg := agent.NewCancelRegistry(zerolog.Nop())

	d := defaultDeps()
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID, Settings: settings}}
	d.cancels = cancelReg
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("checkpoint-after-no-progress"))), nil
	}
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 1, errors.New("exec not available in test")
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		<-ctx.Done()
		return &agent.AgentResult{
			Summary:         "Interrupted cleanly",
			ConfidenceScore: 0.4,
			AgentSessionID:  "agent-checkpoint-1",
			ExitCode:        1,
		}, ctx.Err()
	}

	err = buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "policy stop should be handled internally after checkpoint publication")

	checkpoints := d.sessions.getCheckpointUpdates()
	require.NotEmpty(t, checkpoints, "policy stop should publish checkpoint metadata")
	require.Equal(t, models.CheckpointKindGracefulStop, checkpoints[len(checkpoints)-1].kind, "policy stop should publish a graceful-stop checkpoint")
	require.Equal(t, models.RuntimeStopReasonNoProgress, checkpoints[len(checkpoints)-1].stopReason, "policy stop should record the no-progress stop reason")
	require.NotEmpty(t, checkpoints[len(checkpoints)-1].snapshotKey, "policy stop should persist the checkpoint snapshot key")

	failures := d.sessions.getFailureUpdates()
	require.Len(t, failures, 1, "policy stop should persist a structured failure explanation")
	require.Equal(t, agent.FailureCategoryTimeout, failures[0].category, "policy stop should use the timeout family category")
	require.Contains(t, failures[0].explanation, "saved a resumable checkpoint", "policy stop should explain that the latest state was preserved")
}

func TestRunAgent_DoesNotPublishCheckpointWithoutSnapshotStore(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.snapshots = nil
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Diff:            "--- a/main.go\n+++ b/main.go",
			Summary:         "Fixed null pointer",
			ConfidenceScore: 0.9,
			AgentSessionID:  "agent-no-snapshot-store",
			ExitCode:        0,
		}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed without a snapshot store configured")
	require.Empty(t, d.sessions.getCheckpointUpdates(), "RunAgent should not publish checkpoint metadata when no snapshot was actually persisted")
}

func TestRunAgent_RevertsToPendingWhenRuntimeInitFails(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.beginRuntimeErr = errors.New("runtime init write failed")

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should surface runtime initialization errors")
	require.Contains(t, err.Error(), "begin runtime control", "RunAgent should wrap the runtime initialization failure")

	statuses := d.sessions.getStatusUpdates()
	require.Equal(t, []string{"running", "pending"}, statuses, "RunAgent should roll the session back out of running when runtime initialization fails")
}

// TestRunAgent_UserCancelTakesPrecedenceOverDeadline guards the ordering
// of branches in the RunAgent error handler: when a user cancel and a
// context deadline have both fired, the session must be classified as a
// user cancel (snapshotted back to idle) rather than a timeout failure.
// Without this ordering, a cancel that races the deadline could flip the
// session into a failed-with-timeout state instead of returning to idle.
func TestRunAgent_UserCancelTakesPrecedenceOverDeadline(t *testing.T) {
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
	// Make Exec fail so doCancel falls back to immediate ctx cancel rather
	// than waiting 30s for the SIGINT timer.
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 1, errors.New("exec not available in test")
	}

	// Adapter waits for the cancel registry to mark the session cancelled,
	// then returns DeadlineExceeded to simulate the race where the ctx
	// deadline has also fired by the time the adapter reports back.
	adapterStarted := make(chan struct{})
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		close(adapterStarted)
		<-ctx.Done()
		return nil, context.DeadlineExceeded
	}

	done := make(chan error, 1)
	go func() {
		// Pass a context with a near-future deadline so DeadlineExceeded is
		// a plausible outcome by the time we check ctx.Err() in RunAgent.
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		done <- buildOrchestrator(d).RunAgent(ctx, run)
	}()

	<-adapterStarted
	require.True(t, cancelReg.CancelSession(run.ID), "cancel should find registered session")

	err := <-done
	require.Error(t, err)
	require.Contains(t, err.Error(), "cancelled", "cancel must win over timeout classification")
	require.NotContains(t, err.Error(), "timed out", "timeout classification must lose to explicit user cancel")

	// No failed-status update should have been recorded.
	for _, r := range d.sessions.getResultUpdates() {
		require.NotEqual(t, "failed", r.status, "cancelled-with-expired-ctx should not mark session failed")
	}
	// No analyze_failure job should have been enqueued.
	require.NotContains(t, d.jobs.getEnqueued(), "analyze_failure", "cancel path must not enqueue failure analysis")
}

// --- Amp/Pi agent env resolution ---

// TestRunAgent_AmpCredentialEnv asserts that Amp sessions receive auth from
// the credential store and non-secret mode defaults from agent_config.
func TestRunAgent_AmpCredentialEnv(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	run := testRun(orgID, testIssue(orgID).ID)
	run.AgentType = models.AgentTypeAmp

	d := defaultDeps()
	d.adapter.name = models.AgentTypeAmp

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "amp-sb", Provider: "mock", WorkDir: "/workspace"}, nil
	}
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAmp: {Provider: models.ProviderAmp, Config: models.AmpConfig{APIKey: "amp_live_key"}},
		},
	}

	settings := models.OrgSettings{
		AgentConfig: models.AgentEnvConfig{
			"amp": {"AMP_MODE": models.AmpModeDeep},
		},
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	orgs := &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypeAmp: d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		Orgs:             orgs,
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	require.NoError(t, orch.RunAgent(context.Background(), run))
	require.Equal(t, "amp_live_key", capturedCfg.Env["AMP_API_KEY"],
		"AMP_API_KEY must flow from the Amp credential store into the sandbox env")
	require.Equal(t, models.AmpModeDeep, capturedCfg.Env["AMP_MODE"],
		"AMP_MODE from agent_config.amp should reach the sandbox")
}

// TestRunAgent_PiDedicatedCredentialEnv asserts Pi sessions use a dedicated Pi
// credential and only layer model defaults from agent_config.
func TestRunAgent_PiDedicatedCredentialEnv(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	run := testRun(orgID, testIssue(orgID).ID)
	run.AgentType = models.AgentTypePi

	d := defaultDeps()
	d.adapter.name = models.AgentTypePi

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "pi-sb", Provider: "mock", WorkDir: "/workspace"}, nil
	}

	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderPi: {Provider: models.ProviderPi, Config: models.PiConfig{APIKey: "pi-api-key"}},
		},
	}

	settings := models.OrgSettings{
		AgentConfig: models.AgentEnvConfig{
			"pi": {"PI_MODEL": models.PiModelGPT54},
		},
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	orgs := &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypePi: d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		Orgs:             orgs,
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	require.NoError(t, orch.RunAgent(context.Background(), run))

	require.Equal(t, "pi-api-key", capturedCfg.Env["PI_API_KEY"], "Pi should use its dedicated credential")
	require.Equal(t, models.PiModelGPT54, capturedCfg.Env["PI_MODEL"], "PI_MODEL should flow from agent_config.pi")
	require.NotContains(t, capturedCfg.Env, "ANTHROPIC_API_KEY", "Pi should not inherit Anthropic credentials")
	require.NotContains(t, capturedCfg.Env, "OPENAI_API_KEY", "Pi should not inherit OpenAI credentials")
	require.NotContains(t, capturedCfg.Env, "GEMINI_API_KEY", "Pi should not inherit Gemini credentials")
}

// TestRunAgent_AmpMissingAPIKeyFailsFast asserts that an Amp run without an
// AMP_API_KEY in the resolved env fails the run with a clear user-facing
// error before the sandbox is even created, rather than letting the Amp CLI
// blow up with "invalid api key" inside the container. This covers both the
// nil-orgs-store case and the case where the org simply hasn't configured
// any Amp credential yet.
func TestRunAgent_AmpMissingAPIKeyFailsFast(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	run := testRun(orgID, testIssue(orgID).ID)
	run.AgentType = models.AgentTypeAmp

	d := defaultDeps()
	d.adapter.name = models.AgentTypeAmp

	var sandboxCreated bool
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		sandboxCreated = true
		return &agent.Sandbox{ID: "amp-unreachable", Provider: "mock", WorkDir: "/workspace"}, nil
	}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypeAmp: d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		// Orgs intentionally omitted to simulate "no Amp/Pi default settings source".
		Jobs:          d.jobs,
		GitHub:        d.github,
		Credentials:   d.creds,
		Logger:        zerolog.Nop(),
		MaxConcurrent: 3,
	})

	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "Amp run without AMP_API_KEY must fail fast")
	require.Contains(t, err.Error(), "AMP_API_KEY",
		"error should name the missing credential so users know what to configure")
	require.False(t, sandboxCreated,
		"pre-flight auth check must fire before sandbox creation")
}

// TestRunAgent_PiModelOverrideReachesSandbox asserts that a per-run override
// still shapes Pi's model selection while auth comes from the dedicated Pi row.
func TestRunAgent_PiModelOverrideReachesSandbox(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	run := testRun(orgID, testIssue(orgID).ID)
	run.AgentType = models.AgentTypePi
	override := models.PiModelGPT54
	run.ModelOverride = &override

	d := defaultDeps()
	d.adapter.name = models.AgentTypePi

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "pi-override", Provider: "mock", WorkDir: "/workspace"}, nil
	}

	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderPi: {Provider: models.ProviderPi, Config: models.PiConfig{APIKey: "pi-key"}},
		},
	}
	settings := models.OrgSettings{
		AgentConfig: models.AgentEnvConfig{
			"pi": {"PI_MODEL": models.PiModelClaudeSonnet46},
		},
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err)
	orgs := &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypePi: d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		Orgs:             orgs,
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	require.NoError(t, orch.RunAgent(context.Background(), run))
	require.Equal(t, "pi-key", capturedCfg.Env["PI_API_KEY"], "Pi should keep using the dedicated Pi credential")
	require.Equal(t, models.PiModelGPT54, capturedCfg.Env["PI_MODEL"], "per-run model override should reach the sandbox env")
}

// TestRunAgent_PiMissingCredentialFailsFast asserts that a Pi run fails fast
// when no dedicated Pi credential is configured.
func TestRunAgent_PiMissingCredentialFailsFast(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	run := testRun(orgID, testIssue(orgID).ID)
	run.AgentType = models.AgentTypePi

	d := defaultDeps()
	d.adapter.name = models.AgentTypePi
	d.creds = &mockCredentialProvider{}

	var sandboxCreated bool
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		sandboxCreated = true
		return &agent.Sandbox{ID: "pi-unreachable", Provider: "mock", WorkDir: "/workspace"}, nil
	}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypePi: d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		// Orgs and credentials intentionally empty — no Pi credential anywhere.
		Jobs:          d.jobs,
		GitHub:        d.github,
		Credentials:   d.creds,
		Logger:        zerolog.Nop(),
		MaxConcurrent: 3,
	})

	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "Pi run without PI_API_KEY must fail fast")
	require.Contains(t, err.Error(), "PI_API_KEY",
		"error should name the missing Pi credential so users know what to configure")
	require.False(t, sandboxCreated,
		"pre-flight auth check must fire before sandbox creation")
}

// TestRunAgent_AmpAgentConfigCached asserts the orchestrator reads Amp
// agent_config through the OrgSettingsCache and that InvalidateOrg forces a
// fresh read.
//
// We test this behaviorally rather than by counting OrgStore.GetByID calls:
// the orchestrator hits GetByID from several unrelated paths (context limits,
// confidence thresholds, session-timeout resolution, …), so a strict
// before/after diff would couple the test to internal call patterns and
// break any time someone added another GetByID site for an unrelated reason.
//
// Instead, the cache is pre-populated with a marker value distinct from the
// org store's own value. If the orchestrator consults the cache, the captured
// sandbox env contains the cached marker; if it bypasses the cache, the
// captured env contains the org-store value. After InvalidateOrg, the
// captured env must flip to the org-store value, proving invalidation works.
func TestRunAgent_AmpAgentConfigCached(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)

	orgStoreSettings := models.OrgSettings{
		AgentConfig: models.AgentEnvConfig{
			"amp": {"AMP_MODE": models.AmpModeRush},
		},
	}
	settingsJSON, err := json.Marshal(orgStoreSettings)
	require.NoError(t, err)
	orgs := &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}}

	cache := agent.NewOrgSettingsCache(time.Minute)
	cache.Set(orgID, models.AgentEnvConfig{
		"amp": {"AMP_MODE": models.AmpModeDeep},
	})

	d := defaultDeps()
	d.adapter.name = models.AgentTypeAmp
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAmp: {Provider: models.ProviderAmp, Config: models.AmpConfig{APIKey: "amp-cache-test"}},
		},
	}

	var captured []map[string]string
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		envCopy := make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			envCopy[k] = v
		}
		captured = append(captured, envCopy)
		return &agent.Sandbox{ID: "amp-sb", Provider: "mock", WorkDir: "/workspace"}, nil
	}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypeAmp: d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		Orgs:             orgs,
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		OrgSettingsCache: cache,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	// Cache is warm: agent_config must come from the cache, not the org store.
	run1 := testRun(orgID, issue.ID)
	run1.AgentType = models.AgentTypeAmp
	require.NoError(t, orch.RunAgent(context.Background(), run1))
	require.Len(t, captured, 1)
	require.Equal(t, models.AmpModeDeep, captured[0]["AMP_MODE"],
		"warm cache must short-circuit the org store read for agent_config")

	// Invalidate and re-run: the orchestrator must re-read the org store and
	// the captured env must reflect the org store's value, proving the cache
	// is no longer satisfying the agent_config path.
	cache.InvalidateOrg(orgID)

	run2 := testRun(orgID, issue.ID)
	run2.AgentType = models.AgentTypeAmp
	require.NoError(t, orch.RunAgent(context.Background(), run2))
	require.Len(t, captured, 2)
	require.Equal(t, models.AmpModeRush, captured[1]["AMP_MODE"],
		"after InvalidateOrg the orchestrator must re-read agent_config from the org store")
}

// TestRunAgent_AmpAgentConfigCacheTTLExpires asserts that a naturally expired
// cache entry (no explicit Invalidate) is treated as a miss by the
// orchestrator, and that the orchestrator falls back to the org store.
//
// This complements TestRunAgent_AmpAgentConfigCached (which covers the
// explicit InvalidateOrg path) — without this case, a bug that made the
// orchestrator keep using stale cached entries past the TTL would slip
// through, since the invalidate path is a separate code branch (delete) from
// the time-based expiry path (Get returns miss).
func TestRunAgent_AmpAgentConfigCacheTTLExpires(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)

	orgStoreSettings := models.OrgSettings{
		AgentConfig: models.AgentEnvConfig{
			"amp": {"AMP_MODE": models.AmpModeRush},
		},
	}
	settingsJSON, err := json.Marshal(orgStoreSettings)
	require.NoError(t, err)
	orgs := &mockOrgStore{org: models.Organization{ID: orgID, Settings: settingsJSON}}

	cache := agent.NewOrgSettingsCache(time.Minute)
	base := time.Unix(1_700_000_000, 0)
	clock := base
	cache.SetClockForTest(func() time.Time { return clock })

	// Prime the cache with a value distinct from the org store so a cache hit
	// is observable in the captured sandbox env. The Set timestamp uses the
	// current (base) clock, so the entry expires at base + 1 minute.
	cache.Set(orgID, models.AgentEnvConfig{
		"amp": {"AMP_MODE": models.AmpModeDeep},
	})

	d := defaultDeps()
	d.adapter.name = models.AgentTypeAmp
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAmp: {Provider: models.ProviderAmp, Config: models.AmpConfig{APIKey: "amp-cache-test"}},
		},
	}

	var captured []map[string]string
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		envCopy := make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			envCopy[k] = v
		}
		captured = append(captured, envCopy)
		return &agent.Sandbox{ID: "amp-sb", Provider: "mock", WorkDir: "/workspace"}, nil
	}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         d.provider,
		Adapters:         map[models.AgentType]agent.AgentAdapter{models.AgentTypeAmp: d.adapter},
		Sessions:         d.sessions,
		SessionLogs:      d.logs,
		SessionQuestions: d.questions,
		DecisionLog:      d.decisions,
		Issues:           d.issues,
		Repositories:     d.repos,
		Orgs:             orgs,
		Jobs:             d.jobs,
		GitHub:           d.github,
		Credentials:      d.creds,
		OrgSettingsCache: cache,
		Logger:           zerolog.Nop(),
		MaxConcurrent:    3,
	})

	// First run: clock is at base, cache is still fresh — must serve the
	// cached value.
	run1 := testRun(orgID, issue.ID)
	run1.AgentType = models.AgentTypeAmp
	require.NoError(t, orch.RunAgent(context.Background(), run1))
	require.Len(t, captured, 1)
	require.Equal(t, models.AmpModeDeep, captured[0]["AMP_MODE"],
		"pre-expiry run must hit the cache")

	// Advance past the TTL without calling InvalidateOrg. The next read
	// must be a miss and the orchestrator must re-populate from the org
	// store (whose value is distinct so we can distinguish the sources).
	clock = base.Add(time.Minute + time.Second)

	run2 := testRun(orgID, issue.ID)
	run2.AgentType = models.AgentTypeAmp
	require.NoError(t, orch.RunAgent(context.Background(), run2))
	require.Len(t, captured, 2)
	require.Equal(t, models.AmpModeRush, captured[1]["AMP_MODE"],
		"post-TTL run must miss the cache and re-read from the org store")
}

// TestContinueSession_AmpMissingAPIKeyFailsFast asserts the ContinueSession
// auth pre-flight: when Amp is missing AMP_API_KEY the function must revert
// the session status back to idle, post an assistant message so the user
// sees a concrete reason in the UI, and return the auth error — all before
// creating a sandbox. Unlike RunAgent failures (which defer user-visible
// messages to the dead-letter hook for retryability), auth errors are
// terminal so the message goes out inline.
func TestContinueSession_AmpMissingAPIKeyFailsFast(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeAmp
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1

	d := defaultDeps()
	d.adapter.name = models.AgentTypeAmp
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{{
		ID:         1,
		SessionID:  session.ID,
		OrgID:      orgID,
		TurnNumber: 2,
		Role:       models.MessageRoleUser,
		Content:    "continue please",
	}}

	var sandboxCreated bool
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		sandboxCreated = true
		return &agent.Sandbox{ID: "amp-unreachable", Provider: "mock", WorkDir: "/workspace"}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session)
	require.Error(t, err, "ContinueSession must fail when AMP_API_KEY is missing")
	require.Contains(t, err.Error(), "AMP_API_KEY",
		"error should name the missing credential")
	require.False(t, sandboxCreated,
		"auth pre-flight must fire before sandbox creation in ContinueSession")

	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusIdle),
		"session status must be reverted to idle so the user can retry after fixing config")

	var assistantMessages []models.SessionMessage
	for _, m := range d.messages.getMessages() {
		if m.Role == models.MessageRoleAssistant && m.SessionID == session.ID {
			assistantMessages = append(assistantMessages, m)
		}
	}
	require.Len(t, assistantMessages, 1,
		"auth failure must post exactly one inline assistant message (not deferred to a hook)")
	require.Contains(t, assistantMessages[0].Content, "AMP_API_KEY",
		"assistant message should surface the actionable error text to the user")
	require.Equal(t, session.CurrentTurn+1, assistantMessages[0].TurnNumber,
		"assistant error message belongs on the attempted turn, not the prior one")
}
