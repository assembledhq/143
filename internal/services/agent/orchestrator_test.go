package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/assembledhq/143/internal/services/workspace"
	"github.com/assembledhq/143/internal/testutil"
)

// --- Mock implementations ---

// mockAgentAdapter implements agent.AgentAdapter.
type mockAgentAdapter struct {
	name       models.AgentType
	resumeMode agent.SessionResumeMode
	prepareFn  func(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error)
	executeFn  func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error)
}

func (m *mockAgentAdapter) Name() models.AgentType { return m.name }

func (m *mockAgentAdapter) ResumeMode() agent.SessionResumeMode { return m.resumeMode }

func (m *mockAgentAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if m.prepareFn != nil {
		return m.prepareFn(ctx, input)
	}
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

func (c *capturingAdapter) ResumeMode() agent.SessionResumeMode { return agent.ResumeBySessionID }

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
	cfg        *models.OpenAIChatGPTConfig
	err        error
	refreshCfg *models.OpenAIChatGPTConfig
	refreshErr error
	refreshIDs []uuid.UUID
}

func (m *mockCodexAuthProvider) GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.cfg, nil
}

func (m *mockCodexAuthProvider) RefreshTokenByID(_ context.Context, _ models.Scope, credID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	m.refreshIDs = append(m.refreshIDs, credID)
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.refreshCfg, nil
}

type mockCodingCredentialProvider struct {
	resolvable      map[models.ProviderName][]models.DecryptedCodingCredential
	err             error
	requiredUserID  *uuid.UUID
	mu              sync.Mutex
	rateLimitedIDs  map[uuid.UUID]models.CodingCredentialRateLimit
	authRejectedIDs map[uuid.UUID]bool
}

func (m *mockCodingCredentialProvider) ListResolvable(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.requiredUserID != nil {
		if userID == nil || *userID != *m.requiredUserID {
			return nil, nil
		}
	}
	if m.resolvable == nil {
		return nil, nil
	}
	return m.resolvable[provider], nil
}

func (m *mockCodingCredentialProvider) PickRunnableMulti(ctx context.Context, orgIDScope models.Scope, providers []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, provider := range providers {
		for _, cred := range m.resolvable[provider] {
			if cred.Status != models.CodingCredentialStatusActive {
				continue
			}
			if m.authRejectedIDs != nil && m.authRejectedIDs[cred.ID] {
				continue
			}
			if limit, ok := m.rateLimitedIDs[cred.ID]; ok && limit.Until.After(time.Now()) {
				continue
			}
			picked := cred
			return &picked, nil
		}
	}
	return nil, errors.New("all eligible coding credentials are currently shed")
}

func (m *mockCodingCredentialProvider) MarkRateLimitedForScope(_ context.Context, _ models.Scope, id uuid.UUID, limit models.CodingCredentialRateLimit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rateLimitedIDs == nil {
		m.rateLimitedIDs = make(map[uuid.UUID]models.CodingCredentialRateLimit)
	}
	m.rateLimitedIDs[id] = limit
	for provider, creds := range m.resolvable {
		for i := range creds {
			if creds[i].ID == id {
				until := limit.Until
				observedAt := time.Now()
				message := limit.Message
				creds[i].RateLimitedUntil = &until
				creds[i].RateLimitedObservedAt = &observedAt
				creds[i].RateLimitMessage = &message
			}
		}
		m.resolvable[provider] = creds
	}
	return nil
}

func (m *mockCodingCredentialProvider) MarkAuthRejectedForScope(_ context.Context, _ models.Scope, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.authRejectedIDs == nil {
		m.authRejectedIDs = make(map[uuid.UUID]bool)
	}
	m.authRejectedIDs[id] = true
	return nil
}

func (m *mockCodingCredentialProvider) MarkRateLimited(id uuid.UUID) {
	_ = m.MarkRateLimitedForScope(context.Background(), models.Scope{}, id, models.CodingCredentialRateLimit{Until: time.Now().Add(time.Minute)})
}

func (m *mockCodingCredentialProvider) MarkAuthRejected(id uuid.UUID) {
	_ = m.MarkAuthRejectedForScope(context.Background(), models.Scope{}, id)
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

// withDefaultStatus applies Status="active" when the test fixture didn't set
// one. The legacy fallback resolver filters by Status, and the orchestrator
// tests pre-date that filter — every fixture would otherwise need to repeat
// `Status: "active"` for behavior that's already production reality.
func (m *mockCredentialProvider) withDefaultStatus(cred *models.DecryptedCredential) *models.DecryptedCredential {
	if cred == nil || cred.Status != "" {
		return cred
	}
	copy := *cred
	copy.Status = models.CodingCredentialStatusActive
	return &copy
}

func (m *mockCredentialProvider) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.byProvider == nil {
		return nil, nil
	}
	return m.withDefaultStatus(m.byProvider[provider]), nil
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
	workspaceUpdates       []workspaceUpdate
	turnUpdates            []turnUpdate
	runtimeBegins          []runtimeBegin
	progressUpdates        []runtimeProgressUpdate
	extensionGrants        []runtimeExtensionGrant
	checkpoints            []checkpointUpdate
	recoveryStates         []recoveryStateUpdate
	workingBranches        []string
	baseCommitSHAs         []string
	failureUpdates         []failureUpdate
	workerOwnerships       []workerOwnershipUpdate
	revisionContextUpdates [][]byte
	updateWorkingBranchErr error
	countRunningErr        error
	beginRuntimeErr        error
	publishCheckpointErr   error
	publishCheckpointOK    *bool
	updateSnapshotInfoErr  error
	updateRevisionErr      error
	pendingCancel          bool
	consumeCancelCalls     int
	eventHook              func(string)
	acquireHoldFn          func(proposedContainerID string) (string, error)
	acquireHoldErr         error
	setWorkerNodeErr       error
	releaseHoldFn          func() (bool, string, error)
	finalizeFn             func(expectedContainerID string) (bool, error)
	clearContainerIDFn     func(expectedContainerID string) (bool, error)
	containerHoldStateFn   func(expectedContainerID string) (bool, bool, error)
	acquireHoldCalls       int
	releaseHoldCalls       int
	finalizeCalls          int
	clearContainerIDCalls  int
	containerStateCalls    int

	// Programmable response for the rehydrate-pass query. Each call returns
	// the next page; the field is used only by orchestrator-wrapper tests in
	// sandbox_auth_rehydrate_test.go. Adding the method here means every
	// orchestrator built with mockSessionStore satisfies
	// ContainerHoldingSessionLister, so the wrapper's success path is
	// reachable; the cast-fail path is exercised by a separate stub that
	// deliberately omits the method.
	containerHoldingPages [][]models.Session
	containerHoldingErr   error
	containerHoldingCalls int

	// getByIDFn lets individual tests stub the session row that drain and
	// other helpers query for status. Defaults to an empty Session when nil.
	getByIDFn func(orgID, sessionID uuid.UUID) (models.Session, error)
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

type workspaceUpdate struct {
	snapshotKey string
	result      *models.SessionResult
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
	if m.eventHook != nil {
		m.eventHook("session_result:" + status)
	}
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
	return m.updateSnapshotInfoErr
}

func (m *mockSessionStore) UpdateWorkspaceSnapshot(ctx context.Context, orgID, sessionID uuid.UUID, snapshotKey string, result *models.SessionResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceUpdates = append(m.workspaceUpdates, workspaceUpdate{
		snapshotKey: snapshotKey,
		result:      result,
	})
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

func (m *mockSessionStore) RequestCancel(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (m *mockSessionStore) ConsumeCancelRequest(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consumeCancelCalls++
	if !m.pendingCancel {
		return false, nil
	}
	m.pendingCancel = false
	return true, nil
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
	if m.publishCheckpointErr != nil {
		return false, m.publishCheckpointErr
	}
	published := true
	if m.publishCheckpointOK != nil {
		published = *m.publishCheckpointOK
	}
	if !published {
		return false, nil
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateWorkingBranchErr != nil {
		return m.updateWorkingBranchErr
	}
	m.workingBranches = append(m.workingBranches, branch)
	return nil
}

func (m *mockSessionStore) UpdateBaseCommitSHA(ctx context.Context, orgID, sessionID uuid.UUID, baseCommitSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baseCommitSHAs = append(m.baseCommitSHAs, baseCommitSHA)
	return nil
}

func (m *mockSessionStore) getWorkingBranches() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.workingBranches...)
}

func (m *mockSessionStore) SetGitIdentity(ctx context.Context, orgID, sessionID uuid.UUID, source string, userID *uuid.UUID) error {
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
	if m.eventHook != nil {
		m.eventHook("session_failure")
	}
	m.failureUpdates = append(m.failureUpdates, failureUpdate{
		explanation:  explanation,
		category:     category,
		nextSteps:    nextSteps,
		retryAdvised: retryAdvised,
	})
	return nil
}

func (m *mockSessionStore) GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getByIDFn != nil {
		return m.getByIDFn(orgID, sessionID)
	}
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

func (m *mockSessionStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (bool, error) {
	m.mu.Lock()
	m.clearContainerIDCalls++
	fn := m.clearContainerIDFn
	m.mu.Unlock()
	if fn != nil {
		return fn(expectedContainerID)
	}
	// Default: CAS succeeds.
	return true, nil
}

func (m *mockSessionStore) ContainerHoldState(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (bool, bool, error) {
	m.mu.Lock()
	m.containerStateCalls++
	fn := m.containerHoldStateFn
	m.mu.Unlock()
	if fn != nil {
		return fn(expectedContainerID)
	}
	// Default: the live winner is another turn holder.
	return true, false, nil
}

// ListContainerHoldingSessions returns the next pre-canned page from
// containerHoldingPages. Used only by sandbox_auth_rehydrate_test.go to
// exercise the orchestrator wrapper's success path; adding the method here
// (rather than in a dedicated stub) means the wrapper's interface assertion
// succeeds for every orchestrator built with mockSessionStore.
func (m *mockSessionStore) ListContainerHoldingSessions(_ context.Context, _ string, _ uuid.UUID, _ int) ([]models.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.containerHoldingErr != nil {
		return nil, m.containerHoldingErr
	}
	idx := m.containerHoldingCalls
	m.containerHoldingCalls++
	if idx >= len(m.containerHoldingPages) {
		return nil, nil
	}
	return m.containerHoldingPages[idx], nil
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

func (m *mockSessionStore) getWorkspaceUpdates() []workspaceUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]workspaceUpdate, len(m.workspaceUpdates))
	copy(out, m.workspaceUpdates)
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
	markedThreadID       *uuid.UUID
	markedTurnNumber     int
	markedMessage        string
	markedOrgID          uuid.UUID
	markedSessionID      uuid.UUID
	markDuplicateInvoked bool
	markDuplicateErr     error
	onCreate             func(models.SessionLog)
}

func (m *mockSessionLogStore) Create(ctx context.Context, log *models.SessionLog) error {
	m.mu.Lock()
	m.logs = append(m.logs, *log)
	m.count++
	onCreate := m.onCreate
	snapshot := *log
	m.mu.Unlock()
	if onCreate != nil {
		onCreate(snapshot)
	}
	return nil
}

func (m *mockSessionLogStore) getCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

func (m *mockSessionLogStore) getLogs() []models.SessionLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.SessionLog, len(m.logs))
	copy(out, m.logs)
	return out
}

func (m *mockSessionLogStore) MarkAssistantTranscriptDuplicate(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, turnNumber int, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markDuplicateInvoked = true
	m.markedOrgID = orgID
	m.markedSessionID = sessionID
	if threadID != nil {
		copied := *threadID
		m.markedThreadID = &copied
	}
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

func (m *mockSessionQuestionStore) AnswerLatestPendingBySessionAndQuestion(_ context.Context, orgID, sessionID uuid.UUID, questionText, answerText string, answeredBy uuid.UUID) (models.SessionQuestion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.questions) - 1; i >= 0; i-- {
		q := m.questions[i]
		if q.OrgID == orgID && q.SessionID == sessionID && q.Status == "pending" && q.QuestionText == questionText {
			m.questions[i].Status = "answered"
			m.questions[i].AnswerText = &answerText
			m.questions[i].AnsweredBy = &answeredBy
			return m.questions[i], nil
		}
	}
	return models.SessionQuestion{}, pgx.ErrNoRows
}

func (m *mockSessionQuestionStore) getQuestions() []models.SessionQuestion {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.SessionQuestion, len(m.questions))
	copy(out, m.questions)
	return out
}

// mockSessionHumanInputRequestStore implements agent.SessionHumanInputRequestStore.
type mockSessionHumanInputRequestStore struct {
	mu       sync.Mutex
	requests []models.HumanInputRequest
}

func (m *mockSessionHumanInputRequestStore) Create(_ context.Context, req *models.HumanInputRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.ID == uuid.Nil {
		req.ID = uuid.New()
	}
	m.requests = append(m.requests, *req)
	return nil
}

func (m *mockSessionHumanInputRequestStore) GetByID(_ context.Context, orgID, sessionID, id uuid.UUID) (models.HumanInputRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.requests {
		if req.OrgID == orgID && req.SessionID == sessionID && req.ID == id {
			return req, nil
		}
	}
	return models.HumanInputRequest{}, pgx.ErrNoRows
}

func (m *mockSessionHumanInputRequestStore) AnswerLatestPendingFreeTextBySession(_ context.Context, orgID, sessionID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	return m.answerLatestPendingFreeText(orgID, sessionID, nil, answerText, answeredBy)
}

func (m *mockSessionHumanInputRequestStore) AnswerLatestPendingFreeTextByThread(_ context.Context, orgID, sessionID, threadID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	return m.answerLatestPendingFreeText(orgID, sessionID, &threadID, answerText, answeredBy)
}

func (m *mockSessionHumanInputRequestStore) answerLatestPendingFreeText(orgID, sessionID uuid.UUID, threadID *uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.requests) - 1; i >= 0; i-- {
		req := m.requests[i]
		if req.OrgID != orgID || req.SessionID != sessionID || req.Status != models.HumanInputRequestStatusPending || req.Kind != models.HumanInputRequestKindFreeText {
			continue
		}
		if threadID != nil && (req.ThreadID == nil || *req.ThreadID != *threadID) {
			continue
		}
		answer := strings.TrimSpace(answerText)
		now := time.Now()
		m.requests[i].Status = models.HumanInputRequestStatusAnswered
		m.requests[i].AnswerText = &answer
		m.requests[i].AnsweredBy = &answeredBy
		m.requests[i].AnsweredAt = &now
		return m.requests[i], nil
	}
	return models.HumanInputRequest{}, pgx.ErrNoRows
}

func (m *mockSessionHumanInputRequestStore) getRequests() []models.HumanInputRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.HumanInputRequest, len(m.requests))
	copy(out, m.requests)
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

type mockUploadStore struct {
	mu      sync.Mutex
	files   map[string]mockUploadFile
	opened  []string
	openErr error
}

type mockUploadFile struct {
	body        []byte
	contentType string
}

func (m *mockUploadStore) Save(context.Context, string, io.Reader, string) (string, error) {
	return "", errors.New("not implemented")
}

func (m *mockUploadStore) URL(key string) string {
	return "/api/v1/uploads/files/" + key
}

func (m *mockUploadStore) Serve(http.ResponseWriter, *http.Request, string) {
}

func (m *mockUploadStore) Open(_ context.Context, key string) (io.ReadCloser, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.opened = append(m.opened, key)
	if m.openErr != nil {
		return nil, "", m.openErr
	}
	file, ok := m.files[key]
	if !ok {
		return nil, "", os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(file.body)), file.contentType, nil
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

type mockFileReader struct {
	listDirFn         func(ctx context.Context, containerID, workDir, dirPath string) ([]sandbox.FileEntry, error)
	readFileFn        func(ctx context.Context, containerID, workDir, filePath string) (string, bool, error)
	readFileContextFn func(ctx context.Context, containerID, workDir, filePath string, line, above, below int) (sandbox.FileContextResult, error)
}

func (m *mockFileReader) ListDir(ctx context.Context, containerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
	if m.listDirFn != nil {
		return m.listDirFn(ctx, containerID, workDir, dirPath)
	}
	return nil, errors.New("list dir not stubbed")
}

func (m *mockFileReader) ReadFile(ctx context.Context, containerID, workDir, filePath string) (string, bool, error) {
	if m.readFileFn != nil {
		return m.readFileFn(ctx, containerID, workDir, filePath)
	}
	return "", false, errors.New("read file not stubbed")
}

func (m *mockFileReader) ReadFileContext(ctx context.Context, containerID, workDir, filePath string, line, above, below int) (sandbox.FileContextResult, error) {
	if m.readFileContextFn != nil {
		return m.readFileContextFn(ctx, containerID, workDir, filePath, line, above, below)
	}
	return sandbox.FileContextResult{}, errors.New("read file context not stubbed")
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
	mu            sync.Mutex
	issue         models.Issue
	err           error
	statusUpdates []string
}

func (m *mockIssueStore) GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
	if m.err != nil {
		return models.Issue{}, m.err
	}
	return m.issue, nil
}

func (m *mockIssueStore) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.statusUpdates = append(m.statusUpdates, status)
	return nil
}

func (m *mockIssueStore) getStatusUpdates() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.statusUpdates))
	copy(out, m.statusUpdates)
	return out
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
	targets          map[string]*string // jobType -> last-seen TargetNodeID
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

func (m *mockJobStore) EnqueueWithTarget(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string, targetNodeID *string) (uuid.UUID, error) {
	m.mu.Lock()
	if m.targets == nil {
		m.targets = make(map[string]*string)
	}
	m.targets[jobType] = targetNodeID
	m.mu.Unlock()
	return m.Enqueue(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
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
	provider         *testutil.MockSandboxProvider
	adapter          *mockAgentAdapter
	sessions         *mockSessionStore
	sessionThreads   *mockSessionThreadStore
	projects         *mockProjectTaskUpdater
	issues           *mockIssueStore
	repos            *mockRepositoryStore
	logs             *mockSessionLogStore
	questions        *mockSessionQuestionStore
	humanInputs      *mockSessionHumanInputRequestStore
	messages         *mockSessionMessageStore
	decisions        *mockDecisionLogStore
	jobs             *mockJobStore
	github           *mockGitHubTokenProvider
	codexAuth        agent.CodexAuthProvider
	claudeCodeAuth   agent.ClaudeCodeAuthProvider
	creds            *mockCredentialProvider
	codingCreds      agent.CodingCredentialProvider
	snapshots        *mockSnapshotStore
	uploads          *mockUploadStore
	fileReader       sandbox.FileReader
	mentionIndexes   *workspace.MentionIndexCache
	cancels          *agent.CancelRegistry
	nodeID           string
	orgs             *mockOrgStore
	identityResolver *identity.Resolver
	sandboxAuth      agent.SandboxAuthServer
	sandboxCapacity  *agent.SandboxCapacityGate
	users            agent.UserLookup
	logger           *zerolog.Logger
}

// mockSessionThreadStore captures thread-status writes the orchestrator
// makes during failure-path bookkeeping. Methods are no-ops on the happy
// path; tests that exercise the worker-ownership / sandbox-failure cleanup
// blocks use updateStatusCalls to assert thread.status was reset.
type mockSessionThreadStore struct {
	mu                sync.Mutex
	updateStatusCalls []struct {
		threadID uuid.UUID
		status   models.ThreadStatus
	}
	completeTurnCalls []struct {
		threadID uuid.UUID
		turn     int
	}
}

func (m *mockSessionThreadStore) UpdateStatus(_ context.Context, _, threadID uuid.UUID, status models.ThreadStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStatusCalls = append(m.updateStatusCalls, struct {
		threadID uuid.UUID
		status   models.ThreadStatus
	}{threadID: threadID, status: status})
	return nil
}

func (m *mockSessionThreadStore) CompleteTurn(_ context.Context, _, threadID uuid.UUID, turn int, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completeTurnCalls = append(m.completeTurnCalls, struct {
		threadID uuid.UUID
		turn     int
	}{threadID: threadID, turn: turn})
	return nil
}

func (m *mockSessionThreadStore) UpdateResult(_ context.Context, _, threadID uuid.UUID, status models.ThreadStatus, _ *models.SessionResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStatusCalls = append(m.updateStatusCalls, struct {
		threadID uuid.UUID
		status   models.ThreadStatus
	}{threadID: threadID, status: status})
	return nil
}

func (m *mockSessionThreadStore) ClearPendingMessages(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (m *mockSessionThreadStore) statuses() []models.ThreadStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.ThreadStatus, 0, len(m.updateStatusCalls))
	for _, c := range m.updateStatusCalls {
		out = append(out, c.status)
	}
	return out
}

func (m *mockSessionThreadStore) completedTurns() []struct {
	threadID uuid.UUID
	turn     int
} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]struct {
		threadID uuid.UUID
		turn     int
	}, len(m.completeTurnCalls))
	copy(out, m.completeTurnCalls)
	return out
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
		humanInputs: &mockSessionHumanInputRequestStore{
			requests: make([]models.HumanInputRequest, 0),
		},
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
	logger := zerolog.Nop()
	if d.logger != nil {
		logger = *d.logger
	}
	var sessionThreads agent.SessionThreadStore
	if d.sessionThreads != nil {
		sessionThreads = d.sessionThreads
	}
	return agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:           d.provider,
		Adapters:           map[models.AgentType]agent.AgentAdapter{d.adapter.Name(): d.adapter},
		Sessions:           d.sessions,
		SessionThreads:     sessionThreads,
		SessionLogs:        d.logs,
		SessionQuestions:   d.questions,
		HumanInputRequests: d.humanInputs,
		SessionMessages:    d.messages,
		DecisionLog:        d.decisions,
		ProjectTasks:       d.projects,
		Issues:             d.issues,
		Repositories:       d.repos,
		Jobs:               d.jobs,
		GitHub:             d.github,
		CodexAuth:          d.codexAuth,
		ClaudeCodeAuth:     d.claudeCodeAuth,
		Credentials:        d.creds,
		CodingCredentials:  d.codingCreds,
		Snapshots:          snapshotStore,
		Uploads:            d.uploads,
		FileReader:         d.fileReader,
		MentionIndexes:     d.mentionIndexes,
		Cancels:            d.cancels,
		Orgs:               orgStore,
		IdentityResolver:   d.identityResolver,
		SandboxAuth:        d.sandboxAuth,
		SandboxCapacity:    d.sandboxCapacity,
		Users:              d.users,
		NodeID:             d.nodeID,
		Logger:             logger,
		MaxConcurrent:      3,
	})
}

func findLogEvent(t *testing.T, logs *bytes.Buffer, message string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event), "RunAgent should emit JSON logs")
		if event["message"] == message {
			return event
		}
	}
	return nil
}

func indexOfEvent(events []string, target string) int {
	for i, event := range events {
		if event == target {
			return i
		}
	}
	return -1
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
	require.Contains(t, d.issues.getStatusUpdates(), "in_progress", "RunAgent should mark the primary issue in progress when execution starts")

	// Result should be "completed" with high confidence.
	results := d.sessions.getResultUpdates()
	require.Len(t, results, 1)
	require.Equal(t, "completed", results[0].status)
	require.NotNil(t, results[0].result.ConfidenceScore)
	require.InDelta(t, 0.9, *results[0].result.ConfidenceScore, 0.01)

	// open_pr job should be enqueued.
	require.Contains(t, d.jobs.getEnqueued(), "open_pr")
	openPRPayload, ok := d.jobs.getPayload("open_pr").(map[string]interface{})
	require.True(t, ok, "open_pr job payload should be a map")
	require.Equal(t, run.ID.String(), openPRPayload["session_id"], "open_pr payload should include agent run ID")
	require.Equal(t, run.OrgID.String(), openPRPayload["org_id"], "open_pr payload should include org ID")

	// Logs should be persisted.
	require.GreaterOrEqual(t, d.logs.getCount(), 2)

	// Sandbox should be destroyed.
	require.Equal(t, 1, d.provider.GetDestroyCalls())
}

func TestRunAgent_MaterializesUploadedAttachments(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.Origin = models.SessionOriginManual
	attachmentURL := "/api/v1/uploads/files/" + orgID.String() + "/2026-05/screenshot.png"
	attachmentBody := []byte("png-data")

	d := defaultDeps()
	d.uploads = &mockUploadStore{
		files: map[string]mockUploadFile{
			orgID.String() + "/2026-05/screenshot.png": {
				body:        attachmentBody,
				contentType: "image/png",
			},
		},
	}
	d.messages.messages = []models.SessionMessage{{
		ID:          1,
		SessionID:   run.ID,
		OrgID:       orgID,
		TurnNumber:  0,
		Role:        models.MessageRoleUser,
		Content:     "This is the error.",
		Attachments: []string{attachmentURL},
	}}

	var capturedAttachments []agent.AgentAttachment
	d.adapter.prepareFn = func(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
		capturedAttachments = append([]agent.AgentAttachment(nil), input.Attachments...)
		return &agent.AgentPrompt{
			SystemPrompt: "system",
			UserPrompt:   "attachments:\n" + input.Attachments[0].LocalPath,
			MaxTokens:    50000,
		}, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Contains(t, prompt.UserPrompt, "/home/sandbox/.143/attachments/turn-1/attachment-1-screenshot.png", "RunAgent should include sandbox-local attachment paths in the prompt")
		return &agent.AgentResult{Summary: "done", ConfidenceScore: 0.9, ExitCode: 0}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)

	require.NoError(t, err, "RunAgent should succeed with uploaded attachments")
	require.Len(t, capturedAttachments, 1, "RunAgent should pass one materialized attachment to the adapter")
	require.Equal(t, attachmentURL, capturedAttachments[0].OriginalURL, "RunAgent should preserve the original attachment URL")
	require.Equal(t, "image/png", capturedAttachments[0].ContentType, "RunAgent should preserve the upload content type")
	require.Equal(t, attachmentBody, d.provider.Files[capturedAttachments[0].LocalPath], "RunAgent should copy uploaded bytes into the sandbox")
}

func TestRunAgent_WarnsAndContinuesWhenUploadedAttachmentCannotBeRead(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	attachmentURL := "/api/v1/uploads/files/" + orgID.String() + "/2026-05/missing.png"

	d := defaultDeps()
	d.uploads = &mockUploadStore{openErr: errors.New("s3 unavailable")}
	d.messages.messages = []models.SessionMessage{{
		ID:          1,
		SessionID:   run.ID,
		OrgID:       orgID,
		TurnNumber:  0,
		Role:        models.MessageRoleUser,
		Content:     "This is the error.",
		Attachments: []string{attachmentURL},
	}}

	var capturedAttachments []agent.AgentAttachment
	d.adapter.prepareFn = func(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
		capturedAttachments = append([]agent.AgentAttachment(nil), input.Attachments...)
		return &agent.AgentPrompt{SystemPrompt: "system", UserPrompt: input.Attachments[0].Error, MaxTokens: 50000}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)

	require.NoError(t, err, "RunAgent should continue when an uploaded attachment cannot be read")
	require.Len(t, capturedAttachments, 1, "RunAgent should pass an unresolved attachment warning to the adapter")
	require.Contains(t, capturedAttachments[0].Error, "s3 unavailable", "warning should include the read failure")
}

func TestRunAgent_DoesNotFetchExternalAttachmentURLs(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.uploads = &mockUploadStore{}
	d.messages.messages = []models.SessionMessage{{
		ID:          1,
		SessionID:   run.ID,
		OrgID:       orgID,
		TurnNumber:  0,
		Role:        models.MessageRoleUser,
		Content:     "Please inspect the linked screenshot.",
		Attachments: []string{"https://example.com/api/v1/uploads/files/" + orgID.String() + "/2026-05/screenshot.png"},
	}}

	var capturedAttachments []agent.AgentAttachment
	d.adapter.prepareFn = func(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
		capturedAttachments = append([]agent.AgentAttachment(nil), input.Attachments...)
		return &agent.AgentPrompt{SystemPrompt: "system", UserPrompt: input.Attachments[0].OriginalURL, MaxTokens: 50000}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)

	require.NoError(t, err, "RunAgent should continue when an attachment is an external URL")
	require.Len(t, capturedAttachments, 1, "RunAgent should pass external attachment context to the adapter")
	require.Equal(t, "https://example.com/api/v1/uploads/files/"+orgID.String()+"/2026-05/screenshot.png", capturedAttachments[0].OriginalURL, "RunAgent should preserve the external URL")
	require.Contains(t, capturedAttachments[0].Error, "external attachments are not fetched", "external attachment should be marked as unfetched")
	require.Empty(t, d.uploads.opened, "RunAgent should not fetch external URLs through upload storage")
}

func TestRunAgent_SandboxCapacityRejectsBeforeCreate(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.InteractionMode = models.SessionInteractionModeInteractive

	d := defaultDeps()
	d.sandboxCapacity = agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &fakeLiveSandboxCounter{count: 1},
		MaxActive: 1,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not be called when live sandbox capacity is full")
		return nil, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "RunAgent should surface the live sandbox capacity sentinel")
	require.Empty(t, d.sessions.getStatusUpdates(), "RunAgent should not mark the session running when capacity is unavailable")
}

func TestRunAgent_SuccessLogIncludesPlatformHealthFields(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	d := defaultDeps()
	d.logger = &logger

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should complete successfully")

	completion := findLogEvent(t, &logs, "agent run finished")
	require.NotNil(t, completion, "RunAgent should emit agent run finished log")
	require.Equal(t, string(models.AgentTypeClaudeCode), completion["agent_type"], "completion log should include agent type")
	require.Equal(t, "completed", completion["outcome"], "completion log should include platform outcome")
	durationMS, ok := completion["duration_ms"].(float64)
	require.True(t, ok, "completion log should include numeric duration_ms")
	require.GreaterOrEqual(t, durationMS, float64(0), "completion duration should be non-negative")
}

func TestRunAgent_InteractiveSuccessLogIncludesPlatformHealthFields(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	run := testRun(orgID, issue.ID)
	run.Origin = models.SessionOriginManual
	run.InteractionMode = models.SessionInteractionModeInteractive
	run.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	d := defaultDeps()
	d.logger = &logger
	d.issues.issue = issue
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("snapshot-bytes"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:         "Initial manual turn complete",
			ConfidenceScore: 0.92,
			ExitCode:        0,
		}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "interactive RunAgent should complete successfully")

	completion := findLogEvent(t, &logs, "agent run finished")
	require.NotNil(t, completion, "interactive RunAgent should emit agent run finished log")
	require.Equal(t, string(models.AgentTypeClaudeCode), completion["agent_type"], "interactive completion log should include agent type")
	require.Equal(t, "idle", completion["outcome"], "interactive completion log should include idle outcome")
	require.Equal(t, "idle", completion["status"], "interactive completion log should include idle status")
	durationMS, ok := completion["duration_ms"].(float64)
	require.True(t, ok, "interactive completion log should include numeric duration_ms")
	require.GreaterOrEqual(t, durationMS, float64(0), "interactive completion duration should be non-negative")
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
	logs := d.logs.getLogs()
	require.Len(t, logs, 1, "RunAgent should persist the streamed final output log")
	require.Equal(t, 1, logs[0].TurnNumber, "initial run logs should use the same first-turn number as the assistant transcript")
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
	require.Contains(t, d.jobs.getEnqueued(), "open_pr", "restart should enqueue PR creation like a fresh run")
}

func TestRecoverSession_FailsAfterRepeatedNoCheckpointRecovery(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.Status = string(models.SessionStatusRunning)
	run.RecoveryAttemptCount = 3
	threadID := uuid.New()
	run.PrimaryThreadID = &threadID

	d := defaultDeps()
	d.sessionThreads = &mockSessionThreadStore{}
	d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatal("RecoverSession must not restart the agent again after the no-checkpoint recovery budget is exhausted")
		return nil, nil
	}

	err := buildOrchestrator(d).RecoverSession(context.Background(), run)
	require.Error(t, err, "RecoverSession should stop retrying no-checkpoint restarts after the recovery budget is exhausted")
	require.ErrorIs(t, err, agent.ErrRecoveryAttemptsExhausted, "exhausted recovery should return a typed sentinel for worker handling")

	results := d.sessions.getResultUpdates()
	require.Len(t, results, 1, "exhausted recovery should mark the session failed")
	require.Equal(t, "failed", results[0].status, "exhausted recovery should be terminal")

	failures := d.sessions.getFailureUpdates()
	require.Len(t, failures, 1, "exhausted recovery should record structured failure metadata")
	require.Equal(t, agent.FailureCategoryRecovery, failures[0].category, "recovery failures should be classified distinctly from agent/tool failures")

	require.Empty(t, d.sessions.getStatusUpdates(), "exhausted recovery should not transition back to running")
	require.Empty(t, d.sessions.getTurnUpdates(), "exhausted recovery should not advance the turn")
	require.Empty(t, d.sessions.getCheckpointUpdates(), "exhausted recovery should not publish checkpoint metadata")
	require.Equal(t, []models.ThreadStatus{models.ThreadStatusFailed}, d.sessionThreads.statuses(), "exhausted recovery should fail the active thread so the UI does not stay stuck")
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
	require.Contains(t, d.jobs.getEnqueued(), "open_pr", "restart should enqueue PR creation like a fresh run")
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

// TestRunAgent_AcquireHoldLosesRaceSelfHeals covers the branch where
// AcquireTurnHold succeeds but returns a different container_id and the
// winning container is alive (real concurrent duplicate). The loser must
// destroy its local sandbox and return ErrSandboxRaceLoser WITHOUT touching
// the session row — the winner owns the row and will publish the
// authoritative result. The worker handler converts ErrSandboxRaceLoser into
// a FatalError so the duplicate job dead-letters silently.
func TestRunAgent_AcquireHoldLosesRaceSelfHeals(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "winner-container", nil
	}
	// Default IsAlive=true; explicit here for readability — the alive branch
	// is what distinguishes "real winner" from "stale orphan".
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return true, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSandboxRaceLoser, "loser must surface ErrSandboxRaceLoser so the worker can dead-letter")
	require.Contains(t, err.Error(), "sandbox race")

	require.Equal(t, 1, d.sessions.acquireHoldCalls)
	require.Equal(t, 1, d.provider.GetDestroyCalls(), "must destroy the losing sandbox")
	require.Equal(t, 0, d.sessions.releaseHoldCalls, "no release — we never held")
	require.Equal(t, 0, d.sessions.finalizeCalls)
	require.Equal(t, 0, d.sessions.clearContainerIDCalls, "alive winner — must NOT clear container_id (would kill the active turn)")
	for _, ru := range d.sessions.resultUpdates {
		require.NotEqual(t, "failed", ru.status, "loser must not mark the session failed — winner owns the row")
	}
}

// TestRunAgent_AcquireHoldLosesRaceClearsStaleOrphan covers the recovery
// branch where the "winning" container_id is actually a stale orphan from a
// crashed prior worker. The orchestrator must CAS-clear the row via
// ClearContainerID and return ErrStaleSandboxIDCleared so the worker
// requeues against a clean row.
func TestRunAgent_AcquireHoldLosesRaceClearsStaleOrphan(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "stale-orphan-container", nil
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return false, nil
	}
	var clearedID string
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		clearedID = expected
		return true, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared, "stale orphan path must surface ErrStaleSandboxIDCleared so the worker requeues")
	require.NotErrorIs(t, err, agent.ErrSandboxRaceLoser, "stale orphan must not be misdiagnosed as a real race loss")

	require.Equal(t, 1, d.sessions.clearContainerIDCalls, "must CAS-clear the stale orphan")
	require.Equal(t, "stale-orphan-container", clearedID, "must clear the exact ID returned by AcquireTurnHold")
	require.Equal(t, 1, d.provider.GetDestroyCalls(), "must still destroy the losing sandbox")
	require.Contains(t, d.sessions.statusUpdates, string(models.SessionStatusPending), "stale-orphan path must revert the session to pending so the retry re-enters the fresh run path")
	for _, ru := range d.sessions.resultUpdates {
		require.NotEqual(t, "failed", ru.status, "stale-orphan path must not mark the session failed")
	}
}

// TestRunAgent_AcquireHoldLosesRaceFallsBackOnIsAliveError verifies the
// conservative fallback: when the IsAlive probe errors (e.g. transient
// docker hiccup), we must NOT clear the row (could kill an active turn).
// Instead, fall through to ErrSandboxRaceLoser; the startup reconciler is
// the safety net for true orphans.
func TestRunAgent_AcquireHoldLosesRaceFallsBackOnIsAliveError(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "winner-container", nil
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return false, errors.New("docker daemon hiccup")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSandboxRaceLoser, "transient IsAlive error must NOT trigger the orphan-clear path")
	require.Equal(t, 0, d.sessions.clearContainerIDCalls, "must not clear container_id when liveness is unknown")
}

// TestRunAgent_AcquireHoldLosesRaceSkipsClearWhenCASLost covers the
// TOCTOU-safe behavior: ClearContainerID returning cleared=false (a new
// holder acquired between the IsAlive probe and the CAS) must fall through
// to ErrSandboxRaceLoser rather than ErrStaleSandboxIDCleared. Otherwise we
// would request a retry against a row that's actually busy.
func TestRunAgent_AcquireHoldLosesRaceSkipsClearWhenCASLost(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "winner-container", nil
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return false, nil
	}
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		return false, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSandboxRaceLoser, "CAS-lost clear must dead-letter, not retry")
	require.Equal(t, 1, d.sessions.clearContainerIDCalls, "must have attempted the clear")
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

// fakeSandboxAuthServer records calls to Listen and exposes the close func it
// hands back so tests can assert that closeAuthSocket fires on every exit
// path through RunAgent.
type fakeSandboxAuthServer struct {
	listenCalls     int
	closeCalls      int
	listenErr       error
	closeSessionIDs []uuid.UUID
}

func (f *fakeSandboxAuthServer) Listen(_ context.Context, _ uuid.UUID, _ *models.Session, _ *models.Repository, _ models.OrgSettings) (string, error) {
	f.listenCalls++
	if f.listenErr != nil {
		return "", f.listenErr
	}
	return "/tmp/fake.sock", nil
}

func (f *fakeSandboxAuthServer) Close(sessionID uuid.UUID) {
	f.closeCalls++
	f.closeSessionIDs = append(f.closeSessionIDs, sessionID)
}

// fakeUserStore is a no-op UserLookup used to satisfy the orchestrator's
// dependency without producing a Co-authored-by trailer for tests that don't
// care.
type fakeUserStore struct{}

func (fakeUserStore) GetByID(_ context.Context, _, _ uuid.UUID) (models.User, error) {
	return models.User{}, errors.New("not configured")
}

// TestRunAgent_AuthSocketClosedWhenCreateFails verifies that the per-session
// GitHub credential socket is torn down even when sandbox creation fails
// after the socket was opened. Without the defer guard, a Listen-then-fail
// sequence would leak one socket per failed session.
func TestRunAgent_AuthSocketClosedWhenCreateFails(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.TriggeredByUserID = func() *uuid.UUID { id := uuid.New(); return &id }()

	d := defaultDeps()
	// Force IntegrationSkills to be non-empty so the AuthSocket code path
	// fires. The orchestrator gates the socket on `IntegrationSkills != ""`
	// so the agent has CLI tools to authenticate against GitHub for.
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}

	// Identity resolver hands back an app token without making any HTTP
	// calls — its installation source is the existing mock GitHub provider
	// which short-circuits to a pre-staged token.
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}

	// Make sandbox creation fail *after* the socket has been opened.
	createErr := errors.New("create failed")
	d.provider.CreateFn = func(_ context.Context, _ agent.SandboxConfig) (*agent.Sandbox, error) {
		return nil, createErr
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should propagate the sandbox-create failure")
	require.ErrorIs(t, err, createErr, "RunAgent should wrap the underlying create error")

	require.Equal(t, 1, authStub.listenCalls, "sandbox auth socket must be opened before container create")
	require.Equal(t, 1, authStub.closeCalls, "sandbox auth socket must be closed when container create fails")
}

// TestRunAgent_AuthSocketClosedOnAcquireHoldError verifies the close fires on
// the AcquireTurnHold-error branch: the listener was opened by
// prepareSandboxGitHubAuth before container create, and AcquireTurnHold
// runs *after* the create succeeds. Without an explicit close on this
// branch, every AcquireTurnHold failure would leak a per-session
// listener until the orchestrator process exited.
func TestRunAgent_AuthSocketClosedOnAcquireHoldError(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.TriggeredByUserID = func() *uuid.UUID { id := uuid.New(); return &id }()

	d := defaultDeps()
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}
	d.sessions.acquireHoldErr = errors.New("acquire failed")

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should propagate the acquire-hold failure")
	require.Contains(t, err.Error(), "acquire turn hold", "RunAgent should surface the acquire-hold failure")

	require.Equal(t, 1, authStub.listenCalls, "auth socket must be opened before container create")
	require.Equal(t, 1, authStub.closeCalls, "auth socket must be closed when acquire-hold fails after socket was opened")
}

// TestRunAgent_AuthSocketClosedOnHydrateRaceLoss verifies the close fires on
// the losing-hydrate-race branch: AcquireTurnHold succeeds but reports a
// different container_id (another holder published first), so we destroy
// our local sandbox. The listener we opened before container create must
// also be torn down — the winning container is owned by the other holder
// and has its own listener.
func TestRunAgent_AuthSocketClosedOnHydrateRaceLoss(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.TriggeredByUserID = func() *uuid.UUID { id := uuid.New(); return &id }()

	d := defaultDeps()
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "winner-container", nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should return an error when AcquireTurnHold reports a different container_id")
	require.ErrorIs(t, err, agent.ErrSandboxRaceLoser, "RunAgent should surface ErrSandboxRaceLoser on the lost hydrate race")
	require.Contains(t, err.Error(), "sandbox race")

	require.Equal(t, 1, authStub.listenCalls, "auth socket must be opened before the race is detected")
	require.Equal(t, 1, authStub.closeCalls, "auth socket must be closed when the local sandbox is destroyed after losing the hydrate race")
}

func TestRunAgent_PreviewHeldContainerKeepsSandboxAuthSocketOpen(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}
	d.sessions.releaseHoldFn = func() (bool, string, error) {
		return false, "test-sandbox", nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:         "ok",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed while preview keeps the container alive")
	require.Equal(t, 1, authStub.listenCalls, "RunAgent should open a sandbox auth socket when integration skills are available")
	require.Equal(t, 0, authStub.closeCalls, "RunAgent must keep the auth socket open while preview holds the container for reuse")
}

func TestContinueSession_FreshResumeWiresSandboxAuth(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = nil

	d := defaultDeps()
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue and update the PR.",
		},
	}
	var createdCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		createdCfg = cfg
		return &agent.Sandbox{ID: "resume-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:         "continued",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should succeed on a fresh resume")
	require.Equal(t, 1, authStub.listenCalls, "ContinueSession should open a sandbox auth socket for fresh sandboxes")
	require.Equal(t, "/tmp/fake.sock", createdCfg.AuthSocketPath, "ContinueSession should bind-mount the per-session auth socket into fresh containers")
	require.Equal(t, sandboxauth.SandboxSocketPath, createdCfg.Env[sandboxauth.SocketEnvVar], "ContinueSession should expose the in-sandbox auth socket path")
	require.Equal(t, "143 Agent", createdCfg.Env[sandboxauth.GitNameEnvVar], "ContinueSession should set the git author name for the sandbox")
	require.Equal(t, "noreply@143.dev", createdCfg.Env[sandboxauth.GitEmailEnvVar], "ContinueSession should set the git author email for the sandbox")
	require.Contains(t, createdCfg.Env["PATH"], "/home/sandbox/.local/bin", "ContinueSession should prepend the gh wrapper directory to PATH")
	require.Contains(t, d.provider.ExecCalls, "143-tools git-bootstrap --workdir=/home/sandbox/backend", "ContinueSession should rerun git-bootstrap after cloning the fresh workspace")
	require.Equal(t, 1, authStub.closeCalls, "ContinueSession should close the sandbox auth socket when the fresh container is destroyed at turn end")
}

func TestContinueSession_MaterializesUploadedAttachmentInResumeMessage(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	snapshotKey := "snapshots/session.tar"
	agentSessionID := "agent-session-1"
	session.SnapshotKey = &snapshotKey
	session.AgentSessionID = &agentSessionID
	attachmentURL := "/api/v1/uploads/files/" + orgID.String() + "/2026-05/error.png"
	attachmentBody := []byte("png-data")

	d := defaultDeps()
	d.uploads = &mockUploadStore{
		files: map[string]mockUploadFile{
			orgID.String() + "/2026-05/error.png": {
				body:        attachmentBody,
				contentType: "image/png",
			},
		},
	}
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 1,
			Role:       models.MessageRoleAssistant,
			Content:    "previous response",
		},
		{
			ID:          2,
			SessionID:   session.ID,
			OrgID:       orgID,
			TurnNumber:  2,
			Role:        models.MessageRoleUser,
			Content:     "This is the error.",
			Attachments: []string{attachmentURL},
		},
	}
	d.adapter.resumeMode = agent.ResumeBySessionID
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.True(t, prompt.Continuation, "ContinueSession should use resume mode when an agent session id exists")
		require.Contains(t, prompt.UserMessage, "/home/sandbox/.143/attachments/turn-2/attachment-1-error.png", "ContinueSession should include sandbox-local attachment paths in the resume message")
		return &agent.AgentResult{Summary: "continued", ConfidenceScore: 0.9, ExitCode: 0}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)

	require.NoError(t, err, "ContinueSession should succeed with uploaded attachments")
	require.Equal(t, attachmentBody, d.provider.Files["/home/sandbox/.143/attachments/turn-2/attachment-1-error.png"], "ContinueSession should copy uploaded bytes into the sandbox")
}

func TestContinueSession_AllowsAttachmentOnlyFollowUp(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	snapshotKey := "snapshots/session.tar"
	agentSessionID := "agent-session-1"
	session.SnapshotKey = &snapshotKey
	session.AgentSessionID = &agentSessionID
	attachmentURL := "/api/v1/uploads/files/" + orgID.String() + "/2026-05/follow-up.png"
	attachmentBody := []byte("png-data")

	d := defaultDeps()
	d.uploads = &mockUploadStore{
		files: map[string]mockUploadFile{
			orgID.String() + "/2026-05/follow-up.png": {
				body:        attachmentBody,
				contentType: "image/png",
			},
		},
	}
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 1,
			Role:       models.MessageRoleAssistant,
			Content:    "previous response",
		},
		{
			ID:          2,
			SessionID:   session.ID,
			OrgID:       orgID,
			TurnNumber:  2,
			Role:        models.MessageRoleUser,
			Content:     "",
			Attachments: []string{attachmentURL},
		},
	}
	d.adapter.resumeMode = agent.ResumeBySessionID
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.True(t, prompt.Continuation, "ContinueSession should use resume mode when an agent session id exists")
		require.Contains(t, prompt.UserMessage, "## Attached files", "attachment-only follow-up should still include an attachment section")
		require.Contains(t, prompt.UserMessage, "/home/sandbox/.143/attachments/turn-2/attachment-1-follow-up.png", "attachment-only follow-up should include sandbox-local attachment paths")
		return &agent.AgentResult{Summary: "continued", ConfidenceScore: 0.9, ExitCode: 0}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)

	require.NoError(t, err, "ContinueSession should succeed when a follow-up has attachments but no text")
	require.Equal(t, attachmentBody, d.provider.Files["/home/sandbox/.143/attachments/turn-2/attachment-1-follow-up.png"], "ContinueSession should copy attachment-only follow-up bytes into the sandbox")
}

func TestContinueSession_MaterializesAttachmentsFromMultiplePendingMessages(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	snapshotKey := "snapshots/session.tar"
	agentSessionID := "agent-session-1"
	session.SnapshotKey = &snapshotKey
	session.AgentSessionID = &agentSessionID
	firstURL := "/api/v1/uploads/files/" + orgID.String() + "/2026-05/first.png"
	secondURL := "/api/v1/uploads/files/" + orgID.String() + "/2026-05/second.png"

	d := defaultDeps()
	d.uploads = &mockUploadStore{
		files: map[string]mockUploadFile{
			orgID.String() + "/2026-05/first.png":  {body: []byte("first"), contentType: "image/png"},
			orgID.String() + "/2026-05/second.png": {body: []byte("second"), contentType: "image/png"},
		},
	}
	d.messages.messages = []models.SessionMessage{
		{
			ID:          2,
			SessionID:   session.ID,
			OrgID:       orgID,
			TurnNumber:  2,
			Role:        models.MessageRoleUser,
			Content:     "First queued message.",
			Attachments: []string{firstURL},
		},
		{
			ID:          3,
			SessionID:   session.ID,
			OrgID:       orgID,
			TurnNumber:  2,
			Role:        models.MessageRoleUser,
			Content:     "Second queued message.",
			Attachments: []string{secondURL},
		},
	}
	d.adapter.resumeMode = agent.ResumeBySessionID
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Contains(t, prompt.UserMessage, "attachment-1-first.png", "ContinueSession should include the first pending message attachment")
		require.Contains(t, prompt.UserMessage, "attachment-2-second.png", "ContinueSession should include the second pending message attachment")
		return &agent.AgentResult{Summary: "continued", ConfidenceScore: 0.9, ExitCode: 0}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)

	require.NoError(t, err, "ContinueSession should succeed with attachments from multiple pending messages")
}

func TestContinueSession_RateLimitRetriesWithFallbackCredential(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeAmp
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = nil

	firstCredID := uuid.New()
	secondCredID := uuid.New()
	d := defaultDeps()
	d.adapter = &mockAgentAdapter{name: models.AgentTypeAmp}
	d.codingCreds = &mockCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAmp: {
				{
					ID:        firstCredID,
					OrgID:     orgID,
					Provider:  models.ProviderAmp,
					Config:    models.AmpConfig{APIKey: "amp-first"},
					Priority:  1,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
				{
					ID:        secondCredID,
					OrgID:     orgID,
					Provider:  models.ProviderAmp,
					Config:    models.AmpConfig{APIKey: "amp-fallback"},
					Priority:  2,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	d.sandboxAuth = &fakeSandboxAuthServer{}
	d.users = fakeUserStore{}
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
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		return &agent.Sandbox{ID: "resume-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	var seenKeys []string
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		seenKeys = append(seenKeys, sandbox.Env["AMP_API_KEY"])
		if len(seenKeys) == 1 {
			return &agent.AgentResult{ExitCode: 1, Error: "rate limit exceeded retry-after=60"}, errors.New("rate limit exceeded")
		}
		return &agent.AgentResult{
			Summary:         "continued with fallback",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should retry with a fallback credential after a rate-limit result")
	require.Equal(t, []string{"amp-first", "amp-fallback"}, seenKeys, "ContinueSession should refresh agent credentials before retrying")
	require.Len(t, d.sessions.turnUpdates, 1, "ContinueSession should persist a single successful turn after fallback retry")
}

func TestContinueSession_RateLimitFallbackExhaustionCreatesBlockedMessage(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeAmp
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = nil

	firstCredID := uuid.New()
	secondCredID := uuid.New()
	d := defaultDeps()
	d.adapter = &mockAgentAdapter{name: models.AgentTypeAmp}
	d.codingCreds = &mockCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAmp: {
				{
					ID:        firstCredID,
					OrgID:     orgID,
					Provider:  models.ProviderAmp,
					Config:    models.AmpConfig{APIKey: "amp-first"},
					Priority:  1,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
				{
					ID:        secondCredID,
					OrgID:     orgID,
					Provider:  models.ProviderAmp,
					Config:    models.AmpConfig{APIKey: "amp-fallback"},
					Priority:  2,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	d.sandboxAuth = &fakeSandboxAuthServer{}
	d.users = fakeUserStore{}
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
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		return &agent.Sandbox{ID: "resume-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	var seenKeys []string
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		seenKeys = append(seenKeys, sandbox.Env["AMP_API_KEY"])
		return &agent.AgentResult{ExitCode: 1, Error: "rate limit exceeded retry-after=60"}, errors.New("rate limit exceeded")
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
	require.Error(t, err, "ContinueSession should fail when every fallback credential is rate limited")
	require.Contains(t, err.Error(), "all Amp auths are rate limited", "ContinueSession should return the clear blocked-auth message")
	require.Equal(t, []string{"amp-first", "amp-fallback"}, seenKeys, "ContinueSession should try the next credential before blocking")

	messages := d.messages.getMessages()
	require.Len(t, messages, 2, "ContinueSession should append one assistant message for the blocked continue")
	require.Equal(t, models.MessageRoleAssistant, messages[1].Role, "blocked continue message should be assistant-authored")
	require.Contains(t, messages[1].Content, "all Amp auths are rate limited", "blocked continue message should explain the rate-limit exhaustion")
	require.Empty(t, d.sessions.turnUpdates, "ContinueSession should not persist a successful turn after all credentials are rate limited")
}

func TestContinueSession_FreshResumeLegacyGitHubAuthStillBootstrapsBranchGuard(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = nil

	d := defaultDeps()
	d.identityResolver = nil
	d.sandboxAuth = nil
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue and update the PR.",
		},
	}
	var createdCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		createdCfg = cfg
		return &agent.Sandbox{ID: "resume-sandbox-legacy", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			Summary:         "continued",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should succeed on a fresh legacy-auth resume")
	require.Empty(t, createdCfg.AuthSocketPath, "legacy github auth should not mount the sandbox auth socket")
	require.Equal(t, "ghp_test123", createdCfg.Env["GITHUB_TOKEN"], "legacy github auth should still expose the fallback token")
	require.Equal(t, "143 Agent", createdCfg.Env[sandboxauth.GitNameEnvVar], "legacy github auth should populate the git author name for bootstrap")
	require.Equal(t, "noreply@143.dev", createdCfg.Env[sandboxauth.GitEmailEnvVar], "legacy github auth should populate the git author email for bootstrap")
	require.Contains(t, d.provider.ExecCalls, "143-tools git-bootstrap --workdir=/home/sandbox/backend", "legacy github auth should still rerun git-bootstrap on fresh resume so the branch guard is installed")
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

func TestRunAgent_UsesRawTaskPromptStyleForAutomation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	repoID := uuid.New()
	goal := "Review recently merged PRs and prepare a follow-up fix if an obvious regression appears."

	run := &models.Session{
		ID:              runID,
		OrgID:           orgID,
		AgentType:       "claude_code",
		Status:          "pending",
		TokenMode:       "low",
		RepositoryID:    &repoID,
		AutomationRunID: func() *uuid.UUID { id := uuid.New(); return &id }(),
		PMApproach:      &goal,
	}

	mockRuns := &mockSessionStore{}
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
		Repositories:     mockRepos,
		Orgs:             mockOrgs,
		Jobs:             mockJobs,
		GitHub:           mockGH,
		Credentials: &mockCredentialProvider{
			byProvider: map[models.ProviderName]*models.DecryptedCredential{
				models.ProviderAnthropic: {
					Provider: models.ProviderAnthropic,
					Config:   models.AnthropicConfig{APIKey: "sk-ant-automation-test"},
				},
			},
		},
		Logger: zerolog.Nop(),
	})

	err := orchestrator.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed for automation sessions without a linked issue")
	require.NotNil(t, capAdapter.captured, "adapter should capture input")
	require.Equal(t, agent.PromptStyleRawTask, capAdapter.captured.PromptStyle, "automation sessions should use the raw-task prompt style")
	require.Equal(t, goal, capAdapter.captured.UserMessage, "automation sessions should pass the stored goal through as the raw task text")
	require.Nil(t, capAdapter.captured.PMContext, "automation sessions should not wrap the goal into PM analysis context")
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

func TestRunAgent_FailedExecutionDrainsInitialQueuedPrompt(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.getByIDFn = func(orgID, sessionID uuid.UUID) (models.Session, error) {
		return models.Session{
			ID:     sessionID,
			OrgID:  orgID,
			Status: string(models.SessionStatusFailed),
		}, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		err := d.messages.Create(ctx, &models.SessionMessage{
			SessionID: run.ID,
			OrgID:     run.OrgID,
			Role:      models.MessageRoleUser,
			Content:   "queued while initial run was active",
		})
		require.NoError(t, err, "test setup should append the prompted user message")
		return nil, errors.New("agent crashed after prompt")
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should return the execution error")
	require.Contains(t, err.Error(), "execute agent", "RunAgent should wrap the adapter execution error")

	enqueued := d.jobs.getEnqueued()
	require.Contains(t, enqueued, "analyze_failure", "failed run should still enqueue failure analysis")
	require.Contains(t, enqueued, "continue_session", "failed initial run should drain queued prompted messages")
	payload, ok := d.jobs.getPayload("continue_session").(map[string]string)
	require.True(t, ok, "continue_session payload should be string-keyed")
	require.Equal(t, run.ID.String(), payload["session_id"], "continue_session payload should target the failed initial session")
}

func TestRunAgent_FailureLogIncludesPlatformHealthFields(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	d := defaultDeps()
	d.logger = &logger
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return nil, errors.New("agent crashed: OOM killed")
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should fail when the adapter fails")

	failure := findLogEvent(t, &logs, "agent run failed")
	require.NotNil(t, failure, "RunAgent should emit agent run failed log")
	require.Equal(t, string(models.AgentTypeClaudeCode), failure["agent_type"], "failure log should include agent type")
	require.Equal(t, "failed", failure["outcome"], "failure log should include platform outcome")
	durationMS, ok := failure["duration_ms"].(float64)
	require.True(t, ok, "failure log should include numeric duration_ms")
	require.GreaterOrEqual(t, durationMS, float64(0), "failure duration should be non-negative")
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

	// No open_pr job should be enqueued.
	for _, jt := range d.jobs.getEnqueued() {
		require.NotEqual(t, "open_pr", jt)
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

	// open_pr job should be enqueued.
	require.Contains(t, d.jobs.getEnqueued(), "open_pr")
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

func TestRunAgent_PendingCancelIsDeliveredAfterSetup(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.cancels = agent.NewCancelRegistry(zerolog.Nop())
	d.sessions.pendingCancel = true
	var cloneCtxErr error
	d.provider.CloneRepoFn = func(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
		select {
		case <-ctx.Done():
			cloneCtxErr = ctx.Err()
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			cloneCtxErr = ctx.Err()
		}
		return nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		select {
		case <-ctx.Done():
			require.ErrorIs(t, ctx.Err(), context.Canceled, "pending cancel should be delivered at the execution boundary")
			return nil, ctx.Err()
		case <-time.After(time.Second):
			t.Fatal("pending cancel should be delivered at the execution boundary")
			return nil, context.Canceled
		}
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)

	require.Error(t, err, "RunAgent should return the cancellation error")
	require.ErrorIs(t, err, context.Canceled, "RunAgent should report cancellation")
	require.NoError(t, cloneCtxErr, "pending cancel should not cancel setup before repository clone completes")
	require.Equal(t, 1, d.sessions.consumeCancelCalls, "RunAgent should consume the pending cancel request once")
	require.Len(t, d.sessions.getTurnUpdates(), 1, "cancelled interactive run should return to idle through turn completion after snapshot")
	results := d.sessions.getResultUpdates()
	require.Empty(t, results, "pending cancel during setup should not mark the session failed")
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

func TestRunAgent_PersistsDiffHeadCommitSHAOnResult(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	headCalls := 0
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if cmd == "git rev-parse HEAD" {
			headCalls++
			if headCalls == 1 {
				_, _ = io.WriteString(stdout, "base123\n")
			} else {
				_, _ = io.WriteString(stdout, "result456\n")
			}
		}
		return 0, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed")

	results := d.sessions.getResultUpdates()
	require.NotEmpty(t, results, "RunAgent should persist a final result update")
	last := results[len(results)-1]
	require.NotNil(t, last.result.DiffHeadCommitSHA, "RunAgent should persist the session HEAD SHA alongside the diff result")
	require.Equal(t, "result456", *last.result.DiffHeadCommitSHA, "RunAgent should store the latest session HEAD SHA, not the initial base commit SHA")
}

func TestRunAgent_PersistsDiffWorkspaceDirtyOnResult(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	headCalls := 0
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		switch cmd {
		case "git rev-parse HEAD":
			headCalls++
			if headCalls == 1 {
				_, _ = io.WriteString(stdout, "base123\n")
			} else {
				_, _ = io.WriteString(stdout, "result456\n")
			}
		case "git status --porcelain --untracked-files=all -- .":
			_, _ = io.WriteString(stdout, " M app.go\n")
		}
		return 0, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed")

	results := d.sessions.getResultUpdates()
	require.NotEmpty(t, results, "RunAgent should persist a final result update")
	last := results[len(results)-1]
	require.True(t, last.result.DiffWorkspaceDirty, "RunAgent should persist whether the session workspace still had uncommitted changes")
}

func TestRunAgent_UsesDesignatedWorkingBranchForSandboxAndSession(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	var createdCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		createdCfg = cfg
		return &agent.Sandbox{ID: "sandbox-1", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if cmd == "git rev-parse HEAD" {
			_, _ = io.WriteString(stdout, "abc123\n")
		}
		return 0, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed")
	require.NotNil(t, run.WorkingBranch, "RunAgent should populate the in-memory working branch")
	require.Equal(t, *run.WorkingBranch, createdCfg.Env[sandboxauth.WorkingBranchEnvVar], "RunAgent should inject the designated working branch into the sandbox env")
	require.Equal(t, []string{*run.WorkingBranch}, d.sessions.getWorkingBranches(), "RunAgent should persist the designated working branch")
}

func TestRunAgent_FailsWhenWorkingBranchCreateReturnsExecError(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "git checkout -b ") {
			_, _ = io.WriteString(stderr, "checkout failed")
			return 7, errors.New("exec failed")
		}
		return 0, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should fail when creating the working branch returns an execution error")
	require.Contains(t, err.Error(), "create working branch", "RunAgent should surface the working-branch creation failure")
	require.Empty(t, d.sessions.getWorkingBranches(), "RunAgent should not persist a working branch when checkout fails")
}

func TestRunAgent_FailsWhenWorkingBranchCreateReturnsNonZeroExit(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "git checkout -b ") {
			_, _ = io.WriteString(stderr, "branch already exists")
			return 17, nil
		}
		return 0, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should fail when creating the working branch exits non-zero")
	require.Contains(t, err.Error(), "exit=17 stderr=branch already exists", "RunAgent should surface the non-zero working-branch checkout output")
	require.Empty(t, d.sessions.getWorkingBranches(), "RunAgent should not persist a working branch when checkout exits non-zero")
}

func TestRunAgent_WorkingBranchPersistErrorIsBestEffort(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.sessions.updateWorkingBranchErr = errors.New("db unavailable")
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if cmd == "git rev-parse HEAD" {
			_, _ = io.WriteString(stdout, "abc123\n")
		}
		return 0, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should continue when persisting the working branch fails")
	require.NotNil(t, run.WorkingBranch, "RunAgent should still populate the in-memory working branch when persistence fails")
	require.Empty(t, d.sessions.getWorkingBranches(), "RunAgent should treat working-branch persistence as best-effort")
}

func TestRunAgent_LegacyGitHubAuthStillBootstrapsBranchGuard(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.identityResolver = nil
	d.sandboxAuth = nil
	var createdCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		createdCfg = cfg
		return &agent.Sandbox{ID: "sandbox-legacy", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if cmd == "git rev-parse HEAD" {
			_, _ = io.WriteString(stdout, "abc123\n")
		}
		return 0, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "RunAgent should succeed on the legacy github auth path")
	require.Empty(t, createdCfg.AuthSocketPath, "legacy github auth should not mount the sandbox auth socket")
	require.Equal(t, "ghp_test123", createdCfg.Env["GITHUB_TOKEN"], "legacy github auth should still expose the fallback token")
	require.Equal(t, "143 Agent", createdCfg.Env[sandboxauth.GitNameEnvVar], "legacy github auth should populate the git author name for bootstrap")
	require.Equal(t, "noreply@143.dev", createdCfg.Env[sandboxauth.GitEmailEnvVar], "legacy github auth should populate the git author email for bootstrap")
	require.Contains(t, d.provider.ExecCalls, "143-tools git-bootstrap --workdir=/home/sandbox/backend", "legacy github auth should still run git-bootstrap so the branch guard is installed")
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

func TestContinueSession_GatesOnPendingSnapshotKey(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusPRCreated)
	session.SnapshotKey = strPtr("snapshots/test/old-pre-pr.tar.zst")
	session.PendingSnapshotKey = strPtr("snapshots/test/post-pr.tar.zst")

	d := defaultDeps()
	d.issues.issue = issue
	// Snapshot data deliberately empty so any attempt to actually hydrate
	// would fail loudly — proving the gate fires before hydrate.
	d.snapshots.data = nil

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.ErrorIs(t, err, agent.ErrSnapshotPending, "ContinueSession should bail with ErrSnapshotPending when PendingSnapshotKey is set")
	require.Empty(t, d.sessions.getStatusUpdates(), "ContinueSession must not mutate session state before the gate fires")
}

func TestContinueSession_SandboxCapacityRejectsFreshResumeBeforeCreate(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)

	d := defaultDeps()
	d.sandboxCapacity = agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &fakeLiveSandboxCounter{count: 1},
		MaxActive: 1,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not be called when live sandbox capacity is full")
		return nil, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)

	require.ErrorIs(t, err, agent.ErrSandboxCapacity, "ContinueSession should surface the live sandbox capacity sentinel")
	require.Empty(t, d.sessions.getStatusUpdates(), "ContinueSession should not mark the session running when capacity is unavailable")
}

func TestRevertThread_UpdatesWorkspaceSnapshot(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	sessionID := uuid.New()
	threadID := uuid.New()
	snapshotKey := "snapshots/test/session.tar.zst"
	baseCommitSHA := "base-sha"
	threadDiff := "diff --git a/foo.txt b/foo.txt\n--- a/foo.txt\n+++ b/foo.txt\n@@ -1 +1 @@\n-old\n+new\n"

	sessions := &mockSessionStore{}
	snapshots := &mockSnapshotStore{
		data: map[string][]byte{
			snapshotKey: []byte("prior-snapshot"),
		},
	}
	provider := testutil.NewMockSandboxProvider()
	provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("post-revert-snapshot"))), nil
	}
	var appliedReversePatch bool
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		switch {
		case cmd == "git rev-parse --is-inside-work-tree":
			_, _ = io.WriteString(stdout, "true\n")
			return 0, nil
		case strings.HasPrefix(cmd, "git apply -R "):
			appliedReversePatch = true
			return 0, nil
		case cmd == "git diff --find-renames --binary base-sha -- .":
			_, _ = io.WriteString(stdout, "diff --git a/foo.txt b/foo.txt\n")
			return 0, nil
		case cmd == "git ls-files --others --exclude-standard -- .":
			return 0, nil
		case cmd == "git rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head-sha\n")
			return 0, nil
		case cmd == "git status --porcelain --untracked-files=all -- .":
			_, _ = io.WriteString(stdout, " M foo.txt\n")
			return 0, nil
		default:
			return 0, nil
		}
	}

	orch := agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider:         provider,
		Sessions:         sessions,
		SessionLogs:      &mockSessionLogStore{},
		SessionQuestions: &mockSessionQuestionStore{},
		SessionMessages:  &mockSessionMessageStore{},
		Snapshots:        snapshots,
		Logger:           zerolog.Nop(),
	})

	session := &models.Session{
		ID:            sessionID,
		OrgID:         orgID,
		Status:        string(models.SessionStatusIdle),
		SnapshotKey:   &snapshotKey,
		BaseCommitSHA: &baseCommitSHA,
	}
	thread := &models.SessionThread{
		ID:        threadID,
		SessionID: sessionID,
		OrgID:     orgID,
		Diff:      &threadDiff,
	}

	err := orch.RevertThread(context.Background(), session, thread)
	require.NoError(t, err, "RevertThread should apply the reverse patch and persist the updated workspace state")
	require.True(t, appliedReversePatch, "RevertThread should run git apply -R inside the sandbox")

	updates := sessions.getWorkspaceUpdates()
	require.Len(t, updates, 1, "RevertThread should persist one workspace snapshot update")
	require.Equal(t, "snapshots/"+orgID.String()+"/"+sessionID.String()+"/workspace.tar.zst", updates[0].snapshotKey, "RevertThread should refresh the session snapshot key")
	require.NotNil(t, updates[0].result.Diff, "RevertThread should persist the refreshed session diff")
	require.Equal(t, "diff --git a/foo.txt b/foo.txt\n", *updates[0].result.Diff, "RevertThread should store the post-revert diff")
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
	err := orch.ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should succeed")

	updates := d.sessions.getTurnUpdates()
	require.Len(t, updates, 1, "ContinueSession should persist exactly one turn update")
	require.Equal(t, 2, updates[0].turn, "ContinueSession should increment the turn number")
	require.Equal(t, agentSessionID, updates[0].agentSessionID, "ContinueSession should reuse the existing agent session id when the adapter does not return one")
	require.NotEmpty(t, updates[0].snapshotKey, "ContinueSession should persist a snapshot key")
	require.NotNil(t, updates[0].result, "ContinueSession should build a session result for UpdateTurnComplete")
	require.NotNil(t, updates[0].result.Diff, "ContinueSession should pass the diff through to UpdateTurnComplete")
}

func TestContinueSession_EmbedsHistoryWhenResumeBySessionIDAdapterHasNoCapturedSessionID(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeClaudeCode
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	// Snapshot present (Path A) but no captured agent session id — exactly
	// the case where the adapter would otherwise lose conversation context.
	snapshotKey := "existing-snapshot"
	session.SnapshotKey = &snapshotKey
	session.AgentSessionID = nil
	priorDiff := "diff --git a/main.go b/main.go\n+fixed already\n"
	session.Diff = &priorDiff

	d := defaultDeps()
	d.adapter = &mockAgentAdapter{
		name:       models.AgentTypeClaudeCode,
		resumeMode: agent.ResumeBySessionID,
	}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 1,
			Role:       models.MessageRoleUser,
			Content:    "Please review my changes.",
		},
		{
			ID:         2,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 1,
			Role:       models.MessageRoleAssistant,
			Content:    "I found two issues: a missing nil check and an unbounded loop.",
		},
		{
			ID:         3,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "please fix both of these issues",
		},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.snapshots.data = map[string][]byte{snapshotKey: []byte("restored-snapshot")}

	var promptSeen *agent.AgentPrompt
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		promptSeen = prompt
		return &agent.AgentResult{Summary: "fixed", ConfidenceScore: 0.9, ExitCode: 0}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should succeed via the embedded-history fallback")
	require.NotNil(t, promptSeen, "adapter must have been invoked")
	require.False(t, promptSeen.Continuation, "fallback must run a fresh exec, not a Continuation turn")
	require.Empty(t, promptSeen.ResumeSessionID, "fallback must not set a ResumeSessionID — there is none")
	require.Contains(t, promptSeen.UserPrompt, "Previous conversation history", "fallback must embed conversation history into the prompt")
	require.Contains(t, promptSeen.UserPrompt, "Please review my changes.", "fallback must include the prior user turn")
	require.Contains(t, promptSeen.UserPrompt, "missing nil check", "fallback must include the prior assistant turn so 'fix both issues' is interpretable")
	require.Contains(t, promptSeen.UserPrompt, "please fix both of these issues", "fallback must include the new user message as the trailing instruction")
	require.NotContains(t, promptSeen.UserPrompt, "starting from a fresh clone", "snapshot fallback must not claim the restored workspace is a fresh clone")
	require.NotContains(t, promptSeen.UserPrompt, "Please re-apply these changes", "snapshot fallback must not ask the agent to re-apply a diff that is already present in the restored snapshot")
	require.NotContains(t, promptSeen.UserPrompt, priorDiff, "snapshot fallback must not include the prior diff as work to re-apply")
}

func TestContinueSession_FallsBackToFreshClaudeExecWhenSnapshotResumeStateIsStale(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeClaudeCode
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	snapshotKey := "existing-snapshot"
	session.SnapshotKey = &snapshotKey
	session.AgentSessionID = strPtr("stale-claude-session")

	d := defaultDeps()
	d.adapter = &mockAgentAdapter{
		name:       models.AgentTypeClaudeCode,
		resumeMode: agent.ResumeBySessionID,
	}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 1,
			Role:       models.MessageRoleUser,
			Content:    "Please review my changes.",
		},
		{
			ID:         2,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 1,
			Role:       models.MessageRoleAssistant,
			Content:    "I found two issues: a missing nil check and an unbounded loop.",
		},
		{
			ID:         3,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "please fix both of these issues",
		},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.snapshots.data = map[string][]byte{snapshotKey: []byte("restored-snapshot")}

	var prompts []*agent.AgentPrompt
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			return &agent.AgentResult{
				ExitCode: 1,
				Error:    "claude CLI exited with code 1",
			}, nil
		}
		return &agent.AgentResult{
			Summary:         "fixed",
			ConfidenceScore: 0.9,
			ExitCode:        0,
			AgentSessionID:  "fresh-claude-session",
		}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should recover from a stale Claude resume id by retrying with reconstructed context")
	require.Len(t, prompts, 2, "the Claude adapter should be executed once for the stale resume and once for the fresh-exec fallback")
	require.True(t, prompts[0].Continuation, "the first attempt should use deterministic Claude resume")
	require.Equal(t, "stale-claude-session", prompts[0].ResumeSessionID, "the first attempt should use the persisted Claude session id")
	require.False(t, prompts[1].Continuation, "the fallback should run a fresh exec against the restored workspace")
	require.Empty(t, prompts[1].ResumeSessionID, "the fallback must not retry the same stale Claude session id")
	require.Contains(t, prompts[1].UserPrompt, "Previous conversation history", "the fallback should reconstruct prior conversation context")
	require.Contains(t, prompts[1].UserPrompt, "Please review my changes.", "the fallback should include the earlier user turn")
	require.Contains(t, prompts[1].UserPrompt, "missing nil check", "the fallback should include the earlier assistant summary")
	require.Contains(t, prompts[1].UserPrompt, "please fix both of these issues", "the fallback should end with the new user message")

	updates := d.sessions.getTurnUpdates()
	require.Len(t, updates, 1, "ContinueSession should still persist a single completed turn")
	require.Equal(t, "fresh-claude-session", updates[0].agentSessionID, "successful fallback should advance the stored Claude session id")
	require.NotEmpty(t, updates[0].snapshotKey, "successful fallback should refresh the snapshot")

	messages := d.messages.getMessages()
	var assistantMessages []models.SessionMessage
	for _, msg := range messages {
		if msg.Role == models.MessageRoleAssistant {
			assistantMessages = append(assistantMessages, msg)
		}
	}
	require.Len(t, assistantMessages, 2, "only the original assistant message and the successful fallback reply should exist")
	require.Equal(t, "fixed", assistantMessages[len(assistantMessages)-1].Content, "the session timeline should record only the successful fallback output")
}

func TestContinueSession_FallsBackToFreshClaudeExecWhenSnapshotResumeStateIsStale_ScopesHistoryToRequestedThread(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	mainThreadID := uuid.New()
	codexThreadID := uuid.New()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeClaudeCode
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 2
	snapshotKey := "existing-snapshot"
	session.SnapshotKey = &snapshotKey
	session.AgentSessionID = strPtr("stale-claude-session")

	d := defaultDeps()
	d.adapter = &mockAgentAdapter{
		name:       models.AgentTypeClaudeCode,
		resumeMode: agent.ResumeBySessionID,
	}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 10, SessionID: session.ID, OrgID: orgID, ThreadID: &mainThreadID, TurnNumber: 1, Role: models.MessageRoleUser, Content: "main tab question"},
		{ID: 11, SessionID: session.ID, OrgID: orgID, ThreadID: &mainThreadID, TurnNumber: 1, Role: models.MessageRoleAssistant, Content: "main tab answer"},
		{ID: 20, SessionID: session.ID, OrgID: orgID, ThreadID: &codexThreadID, TurnNumber: 1, Role: models.MessageRoleUser, Content: "codex tab prior question"},
		{ID: 21, SessionID: session.ID, OrgID: orgID, ThreadID: &codexThreadID, TurnNumber: 1, Role: models.MessageRoleAssistant, Content: "codex tab prior answer"},
		{ID: 22, SessionID: session.ID, OrgID: orgID, ThreadID: &codexThreadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "please fix the failing test"},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.snapshots.data = map[string][]byte{snapshotKey: []byte("restored-snapshot")}

	var prompts []*agent.AgentPrompt
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			return &agent.AgentResult{
				ExitCode: 1,
				Error:    "claude CLI exited with code 1",
			}, nil
		}
		return &agent.AgentResult{
			Summary:         "fixed",
			ConfidenceScore: 0.9,
			ExitCode:        0,
			AgentSessionID:  "fresh-claude-session",
		}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &codexThreadID})
	require.NoError(t, err, "ContinueSession should recover from a stale Claude resume id on the requested thread")
	require.Len(t, prompts, 2, "the Claude adapter should be executed once for the stale resume and once for the fresh-exec fallback")
	require.Contains(t, prompts[1].UserPrompt, "codex tab prior question", "the fallback should include earlier history from the requested thread")
	require.Contains(t, prompts[1].UserPrompt, "codex tab prior answer", "the fallback should include the requested thread's assistant history")
	require.Contains(t, prompts[1].UserPrompt, "please fix the failing test", "the fallback should end with the new message from the requested thread")
	require.NotContains(t, prompts[1].UserPrompt, "main tab question", "the fallback should not include sibling-thread user history")
	require.NotContains(t, prompts[1].UserPrompt, "main tab answer", "the fallback should not include sibling-thread assistant history")
}

func TestContinueSession_UsesThreadExecutionOptions(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	threadID := uuid.New()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeClaudeCode
	session.ModelOverride = strPtr("claude-sonnet-4-6")
	session.AgentSessionID = strPtr("parent-claude-session")
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session.tar")

	threadModel := "gemini-2.5-pro"
	threadAgentSessionID := ""
	var createdCfg agent.SandboxConfig
	var promptSeen *agent.AgentPrompt

	d := defaultDeps()
	d.adapter = &mockAgentAdapter{
		name: models.AgentTypeGeminiCLI,
		executeFn: func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
			promptSeen = prompt
			return &agent.AgentResult{
				Diff:            "--- a/file.go\n+++ b/file.go",
				Summary:         "Thread result",
				ConfidenceScore: 0.9,
				AgentSessionID:  "thread-gemini-session",
				ExitCode:        0,
			}, nil
		},
	}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{{
		ID:         1,
		SessionID:  session.ID,
		OrgID:      orgID,
		ThreadID:   &threadID,
		TurnNumber: 2,
		Role:       models.MessageRoleUser,
		Content:    "Continue in the Gemini tab.",
	}}
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		createdCfg = cfg
		return &agent.Sandbox{ID: "gemini-thread-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{
		AgentType:            models.AgentTypeGeminiCLI,
		ModelOverride:        &threadModel,
		ThreadAgentSessionID: nil,
		ResultAgentSessionID: &threadAgentSessionID,
		ThreadID:             &threadID,
	})
	require.NoError(t, err, "ContinueSession should execute with the thread-selected adapter")
	require.Equal(t, threadModel, createdCfg.Env["GEMINI_MODEL"], "ContinueSession should apply the thread model to the thread agent env")
	require.NotContains(t, createdCfg.Env, "ANTHROPIC_MODEL", "ContinueSession should not apply the parent session model when a thread override is provided")
	require.NotNil(t, promptSeen, "ContinueSession should execute the thread adapter")
	require.False(t, promptSeen.Continuation, "first turn in a blank thread should start a fresh agent transcript")
	require.Empty(t, promptSeen.ResumeSessionID, "blank thread should not resume the parent session's agent session id")
	require.Equal(t, "thread-gemini-session", threadAgentSessionID, "ContinueSession should report the thread agent session id to the worker")
	turnUpdates := d.sessions.getTurnUpdates()
	require.Len(t, turnUpdates, 1, "ContinueSession should still complete the shared session turn")
	require.Equal(t, "parent-claude-session", turnUpdates[0].agentSessionID, "thread execution should not overwrite the parent session agent_session_id")
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
	err := orch.ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should succeed on the reuse path when slash commands need repair")
}

// TestContinueSession_ReusePathClearsStaleOrphanWhenContainerDead verifies the
// defensive liveness probe in the reuse path. When session.container_id points
// at a container that no longer exists (worker rollover / docker eviction
// destroyed it without going through FinalizeContainerDestroy), the
// continue_session must NOT proceed to attach to the dead container — that
// path historically misclassified the resulting Docker error as a Codex auth
// expiry. Instead, ClearContainerID is called and ErrStaleSandboxIDCleared is
// returned so the worker retries against a clean row.
//
// The probe is gated on session.WorkerNodeID matching this worker's node id;
// see TestContinueSession_ReusePathBailsOutOnCrossNodeClaim for the wrong-node
// path that the gate guards against.
func TestContinueSession_ReusePathClearsStaleOrphanWhenContainerDead(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	stale := "stale-container-8d0c678c"
	session.ContainerID = &stale
	thisNode := "worker-this-node"
	session.WorkerNodeID = &thisNode
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.nodeID = thisNode
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
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		require.Equal(t, stale, sb.ID, "IsAlive must probe the recorded container_id")
		return false, nil
	}
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		require.Equal(t, stale, expected, "ClearContainerID must guard the CAS on the recorded id")
		return true, nil
	}
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not run when liveness probe rejects the recorded container_id; the worker will retry")
		return nil, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatalf("adapter.Execute must not run when the session bailed out for a stale-orphan retry")
		return nil, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared,
		"ContinueSession reuse path with dead recorded container_id must return ErrStaleSandboxIDCleared so the worker retries")
	require.Equal(t, 1, d.sessions.clearContainerIDCalls, "ClearContainerID must be called exactly once")
}

// TestContinueSession_ReusePathBailsOutOnCrossNodeClaim verifies that when a
// continue_session job is claimed by the wrong worker (session.worker_node_id
// names a sibling node, e.g. before node-affinity job routing has rolled out
// or as a defense-in-depth catch for bugs that bypass it), the orchestrator
// returns ErrSandboxOnDifferentNode WITHOUT probing IsAlive or clearing
// container_id. Probing on the wrong daemon false-reports dead, and clearing
// would orphan the live container on its real host — both wrong moves.
func TestContinueSession_ReusePathBailsOutOnCrossNodeClaim(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	containerID := "preview-container-on-other-host"
	session.ContainerID = &containerID
	otherNode := "worker-host-c"
	session.WorkerNodeID = &otherNode
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.nodeID = "worker-host-7" // we are NOT the recorded owner
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
	d.provider.IsAliveFn = func(context.Context, *agent.Sandbox) (bool, error) {
		t.Fatalf("IsAlive must NOT be called on cross-node claim — probing the wrong daemon false-reports dead")
		return false, nil
	}
	d.sessions.clearContainerIDFn = func(string) (bool, error) {
		t.Fatalf("ClearContainerID must NOT be called on cross-node claim — would orphan the live container on its real host")
		return false, nil
	}
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not run on cross-node claim — wrong worker")
		return nil, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatalf("adapter.Execute must not run on cross-node claim")
		return nil, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSandboxOnDifferentNode,
		"cross-node continue_session claim must return ErrSandboxOnDifferentNode so the worker re-enqueues for the correct node")
	require.Equal(t, 0, d.sessions.clearContainerIDCalls, "container_id must be untouched on cross-node bail-out")
}

func TestContinueSession_ReusePathClearsContainerForDeadTargetNodeRecovery(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	containerID := "container-on-dead-worker"
	session.ContainerID = &containerID
	deadNode := "worker-dead"
	session.WorkerNodeID = &deadNode
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.nodeID = "worker-recovery"
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
	d.provider.IsAliveFn = func(context.Context, *agent.Sandbox) (bool, error) {
		t.Fatalf("IsAlive must not probe the recovery worker's daemon for a dead target node container")
		return false, nil
	}
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		require.Equal(t, containerID, expected, "dead-node recovery should CAS-clear the recorded container id")
		return true, nil
	}
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not run until the retry observes the cleared container_id")
		return nil, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatalf("adapter.Execute must not run until the retry observes the cleared container_id")
		return nil, nil
	}

	orch := buildOrchestrator(d)
	ctx := jobctx.WithDeadTargetNode(context.Background(), deadNode)
	err := orch.ContinueSession(ctx, session, nil)
	require.Error(t, err, "dead-node recovery should stop the current attempt after clearing the stale container")
	require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared,
		"dead-node recovery should retry so the next attempt hydrates on the recovery worker")
	require.Equal(t, 1, d.sessions.clearContainerIDCalls, "dead-node recovery should clear the recorded container exactly once")
}

// TestContinueSession_ReusePathRetriesWhenIsAliveProbeErrors locks in the
// behavior that an inconclusive IsAlive probe (docker daemon hiccup, probe
// timeout against an unreachable container) must NOT fall through to docker
// exec on a possibly-stale container_id. Doing so historically surfaced a
// user-visible "No such container" failure when the recorded id had already
// been destroyed out-of-band — see the preview-launch-fail race that
// motivated this change.
//
// We don't clear container_id on a probe error (would orphan a healthy live
// container if the daemon merely hiccuped), but we do bail with
// ErrStaleSandboxIDCleared so the worker re-enqueues without consuming an
// attempt; the next attempt re-fetches the session row and re-probes.
// Bounded by maxRetryableDuration so a permanently broken daemon still
// dead-letters through the normal retryable-timeout path.
func TestContinueSession_ReusePathRetriesWhenIsAliveProbeErrors(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-abc"
	session.ContainerID = &existing
	thisNode := "worker-this-node"
	session.WorkerNodeID = &thisNode
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.nodeID = thisNode
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
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return false, errors.New("docker connection refused")
	}
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		t.Fatalf("ClearContainerID must NOT be called when IsAlive errored — clearing on an inconclusive probe would orphan a healthy live container if docker just hiccuped")
		return false, nil
	}
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not run when the reuse path bailed for retry")
		return nil, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatalf("adapter.Execute must not run when the reuse path bailed for retry — falling through would surface 'No such container' if the recorded id had been destroyed out-of-band")
		return nil, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared,
		"inconclusive IsAlive probe must signal retry rather than fall through to docker exec on a possibly-stale container_id")
	require.Equal(t, 0, d.sessions.clearContainerIDCalls, "ClearContainerID must not be invoked on probe-error path")
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusPending),
		"reuse-path abandon must revert session status to pending so the next worker claim re-enters cleanly")
}

// TestContinueSession_ReusePathRetriesWhenClearContainerIDErrors covers the
// case where ClearContainerID itself returns a DB error. With the recorded
// container known dead (IsAlive returned false) and the clear unable to
// land, the session row's container_id is in an indeterminate state — the
// safe move is to bail rather than proceed to docker exec on a known-dead
// container, which would always surface a user-visible "No such container"
// failure.
func TestContinueSession_ReusePathRetriesWhenClearContainerIDErrors(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	stale := "stale-container-clear-err"
	session.ContainerID = &stale
	thisNode := "worker-this-node"
	session.WorkerNodeID = &thisNode
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.nodeID = thisNode
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2,
			Role: models.MessageRoleUser, Content: "follow-up",
		},
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		require.Equal(t, stale, sb.ID, "IsAlive must probe the recorded container_id")
		return false, nil
	}
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		require.Equal(t, stale, expected, "ClearContainerID must guard the CAS on the recorded id")
		return false, errors.New("postgres connection reset by peer")
	}
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not run when the reuse path bailed for retry")
		return nil, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatalf("adapter.Execute must not run after a clear DB error — would surface 'No such container' on a known-dead container")
		return nil, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared,
		"a ClearContainerID DB error during the reuse-path liveness check must signal retry rather than fall through to docker exec on a known-dead container")
	require.Equal(t, 1, d.sessions.clearContainerIDCalls, "ClearContainerID must have been attempted exactly once")
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusPending),
		"reuse-path abandon must revert session status to pending so the next worker claim re-enters cleanly")
}

// TestContinueSession_ReusePathRetriesWhenClearContainerIDCASLost covers the
// race that produced the original incident: the IsAlive probe sees the
// container as gone, but a peer (typically a preview's
// FinalizeContainerDestroy on a launch-failed instance) has already cleared
// container_id between our probe and clear, so the CAS loses (cleared=false).
// The in-memory session.ContainerID is now stale, so reusing it would
// attach to a no-longer-current container and surface a user-visible
// "No such container" docker exec failure. Bail for retry instead so the
// next attempt re-fetches a fresh session row.
func TestContinueSession_ReusePathRetriesWhenClearContainerIDCASLost(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	stale := "stale-container-cas-lost"
	session.ContainerID = &stale
	thisNode := "worker-this-node"
	session.WorkerNodeID = &thisNode
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.nodeID = thisNode
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2,
			Role: models.MessageRoleUser, Content: "follow-up",
		},
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		require.Equal(t, stale, sb.ID, "IsAlive must probe the recorded container_id")
		return false, nil
	}
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		require.Equal(t, stale, expected, "ClearContainerID must guard the CAS on the recorded id")
		return false, nil
	}
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not run when the reuse path bailed for retry")
		return nil, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatalf("adapter.Execute must not run after a CAS-lost clear — would surface 'No such container' on a stale id")
		return nil, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared,
		"CAS-lost ClearContainerID must signal retry so the next attempt re-fetches the now-active session row instead of attaching to the in-memory stale id")
	require.Equal(t, 1, d.sessions.clearContainerIDCalls, "ClearContainerID must have been attempted exactly once")
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusPending),
		"reuse-path abandon must revert session status to pending so the next worker claim re-enters cleanly")
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
			RequiresHumanInput: true,
			AgentSessionID:     "agent-question-1",
			ExitCode:           0,
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

	// Status should have been set to "awaiting_input" after the pause checkpoint.
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
	require.Contains(t, d.jobs.getEnqueued(), "open_pr")
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

func TestRunAgent_ClaudeUnifiedAPIKeyIsNotOverriddenBySubscription(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.claudeCodeAuth = &mockClaudeCodeAuthProvider{
		hasSub: true,
		sub: &models.AnthropicSubscription{
			AccessToken:  "org-sub-access",
			RefreshToken: "org-sub-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	d.codingCreds = &mockCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropic: {
				{
					ID:       uuid.New(),
					OrgID:    orgID,
					Provider: models.ProviderAnthropic,
					Priority: 1,
					Status:   models.CodingCredentialStatusActive,
					Config:   models.AnthropicConfig{APIKey: "sk-unified-ant-key"},
				},
			},
			models.ProviderAnthropicSubscription: {
				{
					ID:       uuid.New(),
					OrgID:    orgID,
					Provider: models.ProviderAnthropicSubscription,
					Priority: 2,
					Status:   models.CodingCredentialStatusActive,
					Config: models.AnthropicSubscriptionConfig{
						AccessToken:  "lower-priority-sub",
						RefreshToken: "lower-priority-refresh",
						ExpiresAt:    time.Now().Add(time.Hour),
					},
				},
			},
		},
	}

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "claude-api-key", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "run should succeed with the selected unified Anthropic API key")
	require.Equal(t, "sk-unified-ant-key", capturedCfg.Env["ANTHROPIC_API_KEY"], "sandbox env should carry the selected Anthropic API key")
	require.NotContains(t, d.provider.Files, "/home/sandbox/.claude/.credentials.json", "lower-priority subscription auth should not override the selected unified API key")
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

func TestRunAgent_CodexLegacyOpenAIKeyFallbackDoesNotRequireOAuth(t *testing.T) {
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

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "codex-legacy-api-key", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "run should succeed when the legacy OpenAI API-key fallback resolved an API key")
	require.Equal(t, "sk-openai-key", capturedCfg.Env["OPENAI_API_KEY"], "sandbox env should carry the legacy OpenAI API key")
	require.NotContains(t, d.provider.Files, "/home/sandbox/.codex/auth.json", "Codex OAuth auth.json should not be required when the legacy fallback resolved an API key")
}

func TestRunAgent_CodexUnifiedOpenAIKeyDoesNotRequireOAuth(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	d.codexAuth = nil
	d.codingCreds = &mockCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderOpenAI: {
				{
					ID:       uuid.New(),
					OrgID:    orgID,
					Provider: models.ProviderOpenAI,
					Priority: 1,
					Status:   models.CodingCredentialStatusActive,
					Config:   models.OpenAIConfig{APIKey: "sk-unified-openai-key"},
				},
			},
		},
	}

	var capturedCfg agent.SandboxConfig
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		capturedCfg = cfg
		return &agent.Sandbox{ID: "codex-api-key", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	orch := buildOrchestrator(d)
	err := orch.RunAgent(context.Background(), run)
	require.NoError(t, err, "run should succeed when the unified resolver selected an OpenAI API key")
	require.Equal(t, "sk-unified-openai-key", capturedCfg.Env["OPENAI_API_KEY"], "sandbox env should carry the selected OpenAI API key")
	require.NotContains(t, d.provider.Files, "/home/sandbox/.codex/auth.json", "Codex OAuth auth.json should not be required when the selected unified credential is an API key")
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

	require.NotContains(t, d.jobs.getEnqueued(), "open_pr", "manual interactive run should wait for explicit end before PR creation")
}

func TestContinueSession_PersistsTurnResultAndReturnsToIdle(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	threadID := uuid.New()
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
			ThreadID:   &threadID,
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
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: "Added the regression test"}
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
	err := orch.ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &threadID})
	require.NoError(t, err, "continue_session should succeed")

	turnUpdates := d.sessions.getTurnUpdates()
	require.Len(t, turnUpdates, 1, "continue_session should persist the completed turn")
	require.Equal(t, 2, turnUpdates[0].turn, "continue_session should advance the turn counter")
	require.NotNil(t, turnUpdates[0].result, "continue_session should persist the latest result")
	require.NotNil(t, turnUpdates[0].result.ResultSummary, "continue_session should persist the latest summary")
	require.Equal(t, "Added the regression test", *turnUpdates[0].result.ResultSummary, "continue_session should store the latest assistant summary")
	require.NotNil(t, turnUpdates[0].result.Diff, "continue_session should persist the latest diff")
	require.Contains(t, d.sessions.getStatusUpdates(), "running", "continue_session should mark the session running while work is in progress")
	require.NotContains(t, d.jobs.getEnqueued(), "open_pr", "continue_session should stay interactive until the user ends the session")

	messages := d.messages.getMessages()
	require.Len(t, messages, 2, "continue_session should append an assistant reply")
	require.Equal(t, models.MessageRoleAssistant, messages[1].Role, "assistant reply should be stored for the continued turn")
	require.Equal(t, 2, messages[1].TurnNumber, "assistant reply should use the new turn number")
	require.NotNil(t, messages[1].ThreadID, "assistant reply should preserve the thread id of the triggering message")
	require.Equal(t, threadID, *messages[1].ThreadID, "assistant reply should keep the triggering thread id")
	require.NotEmpty(t, d.logs.logs, "continue_session should persist streamed output logs")
	require.NotNil(t, d.logs.logs[0].ThreadID, "persisted output logs should preserve the thread id")
	require.Equal(t, threadID, *d.logs.logs[0].ThreadID, "persisted output logs should keep the triggering thread id")
	require.NotNil(t, d.logs.markedThreadID, "duplicate marker should preserve the thread id")
	require.Equal(t, threadID, *d.logs.markedThreadID, "duplicate marker should use the triggering thread id")
}

func TestContinueSession_CancelReturnsPayloadThreadToIdle(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	threadID := uuid.New()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/session.tar")
	session.PrimaryThreadID = nil

	cancelReg := agent.NewCancelRegistry(zerolog.Nop())

	d := defaultDeps()
	d.cancels = cancelReg
	d.sessionThreads = &mockSessionThreadStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			ThreadID:   &threadID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "Please continue this stopped turn.",
		},
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("cancelled-continue-snapshot"))), nil
	}
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "git ") {
			return 0, nil
		}
		return 1, errors.New("exec not available in test")
	}

	adapterStarted := make(chan struct{})
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		close(adapterStarted)
		<-ctx.Done()
		return &agent.AgentResult{
			Summary:        "partial continued work",
			ExitCode:       1,
			AgentSessionID: "agent-continued-cancel",
		}, ctx.Err()
	}

	done := make(chan error, 1)
	go func() {
		done <- buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &threadID})
	}()

	<-adapterStarted
	require.True(t, cancelReg.CancelSession(session.ID), "cancel should find the continued session")

	err := <-done
	require.Error(t, err, "ContinueSession should return the cancellation error")
	require.Contains(t, err.Error(), "cancelled", "ContinueSession should classify the result as user cancelled")

	turnUpdates := d.sessions.getTurnUpdates()
	require.Len(t, turnUpdates, 1, "cancelled continue_session should return the session to idle with a checkpoint")
	require.NotEmpty(t, turnUpdates[0].snapshotKey, "cancelled continue_session should persist a snapshot")

	completedTurns := d.sessionThreads.completedTurns()
	require.Len(t, completedTurns, 1, "cancelled continue_session should return the triggering thread to idle")
	require.Equal(t, threadID, completedTurns[0].threadID, "cancelled continue_session should use the payload thread id when the session row has no PrimaryThreadID")
	require.Equal(t, 2, completedTurns[0].turn, "cancelled continue_session should advance the triggering thread turn")
}

// TestContinueSession_RoutesToRequestedThreadAcrossSiblingTurns is the
// regression test for the multi-tab dispatch bug: a sibling thread further
// along in turns (Main on turn 9 with assistant replies through turn 11) used
// to "win" the orchestrator's latest-user-message lookup over a brand-new
// message on a younger thread (Codex 2 turn 2), causing the new message to be
// orphaned and the wrong thread to be re-run. Locks the contract that when
// the worker plumbs opts.ThreadID through, ContinueSession executes for that
// thread and writes the assistant reply against it — even when sibling
// threads have higher turn_numbers in the (turn_number, id)-ordered slice
// returned by ListBySession.
func TestContinueSession_RoutesToRequestedThreadAcrossSiblingTurns(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	mainThreadID := uuid.New()  // Main: many turns, last user already answered
	codexThreadID := uuid.New() // Codex 2: brand-new tab, user just sent
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginManual
	session.InteractionMode = models.SessionInteractionModeInteractive
	session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 11
	session.SnapshotKey = strPtr("snapshots/test/session.tar")

	d := defaultDeps()
	d.issues.issue = issue
	// Insertion order matches the ordering the real DB returns from
	// ListBySession (ORDER BY turn_number ASC, id ASC). Each thread keeps its
	// own turn counter, so the (turn=2, id=1052) row from Codex 2 sorts before
	// the (turn=9, id=1046) row from Main even though Codex 2's message is
	// chronologically newer. Pre-fix, latestUserMessage walked from the end
	// of this slice and returned the Main turn-9 row — orphaning the Codex 2
	// message and re-running an already-answered Main turn.
	d.messages.messages = []models.SessionMessage{
		{ID: 1052, SessionID: session.ID, OrgID: orgID, ThreadID: &codexThreadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "please fix the tests"},
		{ID: 1046, SessionID: session.ID, OrgID: orgID, ThreadID: &mainThreadID, TurnNumber: 9, Role: models.MessageRoleUser, Content: "main turn 9 (already answered)"},
		{ID: 1048, SessionID: session.ID, OrgID: orgID, ThreadID: &mainThreadID, TurnNumber: 9, Role: models.MessageRoleAssistant, Content: "main turn 9 reply"},
		{ID: 1069, SessionID: session.ID, OrgID: orgID, ThreadID: &mainThreadID, TurnNumber: 11, Role: models.MessageRoleAssistant, Content: "main turn 11 reply"},
	}
	d.provider.RestoreFn = func(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Equal(t, "please fix the tests", prompt.UserMessage,
			"continue_session for Codex 2 must run with Codex 2's user message, not the higher-turn Main message that sorts last in the (turn_number, id) ordering")
		return &agent.AgentResult{
			Summary:         "Codex 2 reply",
			ConfidenceScore: 0.7,
			ExitCode:        0,
		}, nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{
		ThreadID: &codexThreadID,
	})
	require.NoError(t, err, "ContinueSession should succeed for the Codex 2 thread")

	messages := d.messages.getMessages()
	require.Len(t, messages, 5, "an assistant reply should be appended to the timeline")
	reply := messages[4]
	require.Equal(t, models.MessageRoleAssistant, reply.Role)
	require.Equal(t, "Codex 2 reply", reply.Content, "assistant reply content should come from the Codex 2 turn")
	require.NotNil(t, reply.ThreadID, "assistant reply must be attributed to a thread, not session-level")
	require.Equal(t, codexThreadID, *reply.ThreadID,
		"assistant reply must be tagged with the Codex 2 thread, not Main — pre-fix this was the load-bearing assertion that failed in prod")
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
	err := orch.ContinueSession(context.Background(), session, nil)
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

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
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
	err := orch.ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "snapshot resume should fall back to API key after removing stale Claude credentials")
	require.Contains(t, d.provider.ExecCalls, "rm -f '/home/sandbox/.claude/.credentials.json'", "fallback should delete stale Claude credentials before relying on the API key")
	_, credsStillPresent := d.provider.Files["/home/sandbox/.claude/.credentials.json"]
	require.False(t, credsStillPresent, "stale Claude credentials should be removed before API-key fallback continues")
	require.Len(t, d.sessions.getFailureUpdates(), 0, "successful fallback should not record a Claude auth failure")
}

func TestContinueSession_ClaudeSnapshotRestoresTopLevelConfigFromBackup(t *testing.T) {
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
	session.SnapshotKey = strPtr("snapshots/test/session-with-claude-backup.tar")
	session.AgentSessionID = strPtr("claude-session-abc")

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
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("next-snapshot"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.True(t, prompt.Continuation, "snapshot-backed Claude resume should use continuation mode when an agent session id exists")
		return &agent.AgentResult{
			Summary:         "continued",
			ConfidenceScore: 0.82,
			ExitCode:        0,
		}, nil
	}
	d.snapshots.data = map[string][]byte{
		*session.SnapshotKey: []byte("restored-snapshot"),
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "snapshot resume should succeed after restoring Claude config from backup")

	var restoreCmd string
	for _, cmd := range d.provider.ExecCalls {
		if strings.Contains(cmd, ".claude.json.backup.*") {
			restoreCmd = cmd
			break
		}
	}
	require.NotEmpty(t, restoreCmd, "snapshot-backed Claude resume should attempt to restore ~/.claude.json from the newest backup")
	require.Contains(t, restoreCmd, "cp \"$latest\" '/home/sandbox/.claude.json'", "restore command should copy Claude's newest backup to the top-level config path")
}

// errForcedCreateFailure is used by tests that short-circuit provider.Create so
// the test can inspect the SandboxConfig without running the full sandbox
// lifecycle. Named so grep picks it up and future refactors don't silently
// change behavior if Create's call site moves.
var errForcedCreateFailure = errors.New("forced create failure to short-circuit test")

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

	err = buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
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

	err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
	require.NoError(t, err, "ContinueSession should keep running even when the persisted revision context is malformed")
	require.NotNil(t, promptSeen, "ContinueSession should still invoke the adapter after discarding malformed revision context")
	require.Nil(t, promptSeen.RevisionContext, "ContinueSession should drop malformed revision context instead of propagating corrupt JSON into the adapter")
	require.NotContains(t, promptSeen.UserMessage, "## Revision context", "ContinueSession should not append revision framing when the revision context could not be parsed")
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
	counter := &fakeLiveSandboxCounter{count: 99}
	d.sandboxCapacity = agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   counter,
		MaxActive: 1,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
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
	require.NoError(t, orch.ContinueSession(context.Background(), session, nil))

	require.Equal(t, 0, d.provider.GetDestroyCalls(), "sandbox must stay alive while preview holds it")
	require.Equal(t, int64(0), counter.calls.Load(), "reused-container continuations should not consume live sandbox capacity")
	require.Equal(t, 0, d.sessions.finalizeCalls, "FinalizeContainerDestroy must not run while preview holds")
	require.GreaterOrEqual(t, d.sessions.acquireHoldCalls, 1)
	require.GreaterOrEqual(t, d.sessions.releaseHoldCalls, 1)
}

// TestContinueSession_RestoresDiffMetadataOntoSandboxMetadata is the
// regression test for the "Changes tab goes blank after PR push / resolve
// conflicts" and "Changes tab inflates with target-branch commits after
// merging main" bugs. ContinueSession previously left sandbox.Metadata
// empty in every setup branch (reuse / hydrate / fresh-clone), so
// sessiondiff.Collect fell back to plain `git diff` and returned an empty
// string for any clean working tree (post-push, post-merge). That empty
// diff overwrote the authoritative session diff in the DB, blanking the
// Changes tab even though the PR itself was healthy. With the fix, the
// orchestrator copies session.BaseCommitSHA AND the resolved target branch
// back onto sandbox.Metadata after every setup branch, so the diff
// collector has both the immutable base SHA (fallback) and the target
// branch (for the merge-base-style diff that excludes commits brought in
// by integrating the target branch back into the working branch). We
// exercise the reuse path here because it's the simplest setup that goes
// through the post-switch metadata restore.
func TestContinueSession_RestoresDiffMetadataOntoSandboxMetadata(t *testing.T) {
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
	existing := "preview-container-base-sha"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)

	const expectedBaseSHA = "feedfacecafe1234"
	baseSHA := expectedBaseSHA
	session.BaseCommitSHA = &baseSHA

	d := defaultDeps()
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{
			ID:         1,
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 2,
			Role:       models.MessageRoleUser,
			Content:    "follow-up after PR push",
		},
	}

	var observedBaseSHA, observedTargetBranch string
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.NotNil(t, sandbox.Metadata, "ContinueSession must populate sandbox.Metadata before the agent runs")
		observedBaseSHA = sandbox.Metadata[agent.SandboxMetadataBaseCommitSHA]
		observedTargetBranch = sandbox.Metadata[agent.SandboxMetadataTargetBranch]
		return &agent.AgentResult{
			Summary:         "done",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("snap"))), nil
	}

	orch := buildOrchestrator(d)
	require.NoError(t, orch.ContinueSession(context.Background(), session, nil))

	require.Equal(t, expectedBaseSHA, observedBaseSHA,
		"ContinueSession must restore session.BaseCommitSHA onto sandbox.Metadata so sessiondiff.Collect can run `git diff <base> -- .` instead of falling back to plain `git diff`")
	require.Equal(t, "main", observedTargetBranch,
		"ContinueSession must stamp the resolved target branch onto sandbox.Metadata so sessiondiff.Collect can compute a merge-base diff against origin/<branch> instead of inflating the diff with target-branch changes after a merge")
}

// TestContinueSession_ReusedContainerReopensAuthListener locks in the
// regression: when a preview is holding the container alive across turns,
// ContinueSession must (re)open the per-session credential listener even
// though it skips container creation. Without this, an orchestrator restart
// between turns would leave the alive container with no live socket to dial,
// and the agent's next git push would fail. The directory bind-mount carries
// the recreated socket through to the running container.
func TestContinueSession_ReusedContainerReopensAuthListener(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	existing := "preview-container-xyz"
	session.ContainerID = &existing
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}
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
	d.provider.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not be called on the reuse path")
		return nil, nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		require.Equal(t, existing, sandbox.ID, "adapter must run against the reused container")
		return &agent.AgentResult{
			Summary:         "done",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}
	d.sessions.releaseHoldFn = func() (bool, string, error) {
		return false, existing, nil
	}

	orch := buildOrchestrator(d)
	require.NoError(t, orch.ContinueSession(context.Background(), session, nil))

	require.Equal(t, 1, authStub.listenCalls,
		"ContinueSession on a reused container must reopen the per-session auth listener so post-restart resumes can still dial it")
	require.Equal(t, 0, authStub.closeCalls,
		"the listener must stay open while preview keeps the reused container alive")
	require.Equal(t, 0, d.provider.GetDestroyCalls(),
		"reused container must not be destroyed while preview holds it")
	// git-bootstrap configures the in-container git config and credential
	// helper; on a reused container it was already run by the original
	// RunAgent, so we must not re-run it (the socket the helper points at
	// gets recreated transparently via the directory bind-mount).
	for _, cmd := range d.provider.ExecCalls {
		require.NotContains(t, cmd, "143-tools git-bootstrap",
			"git-bootstrap must not re-run on reused containers; original RunAgent already wired git config")
	}
}

// TestContinueSession_AuthSocketClosedOnAcquireHoldError verifies that
// ContinueSession closes the per-session credential listener on the
// AcquireTurnHold-error branch: prepareSandboxGitHubAuth ran before
// container create, so the listener is live by the time AcquireTurnHold
// errors. Without an explicit close, the listener (and its socket file)
// would outlive the failed turn.
func TestContinueSession_AuthSocketClosedOnAcquireHoldError(t *testing.T) {
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
	// No ContainerID, no SnapshotKey: forces the fresh-Create path that
	// goes through prepareSandboxGitHubAuth → Listen before AcquireTurnHold.

	d := defaultDeps()
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "retry me"},
	}
	d.sessions.acquireHoldErr = errors.New("db write failed")

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err, "ContinueSession should propagate the acquire-hold failure")
	require.Contains(t, err.Error(), "acquire turn hold", "ContinueSession should surface the acquire-hold failure")

	require.Equal(t, 1, authStub.listenCalls, "auth socket must be opened before acquire-hold")
	require.Equal(t, 1, authStub.closeCalls, "auth socket must be closed when acquire-hold fails after the listener was opened")
}

// TestContinueSession_AcquireHoldLosesRaceSelfHeals is the ContinueSession
// parity for TestRunAgent_AcquireHoldLosesRaceSelfHeals: when AcquireTurnHold
// reports a different (alive) container_id, ContinueSession must surface
// ErrSandboxRaceLoser without touching the session row, leaving the winner's
// terminal write authoritative.
func TestContinueSession_AcquireHoldLosesRaceSelfHeals(t *testing.T) {
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
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	d.users = fakeUserStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "follow up"},
	}
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "winner-container", nil
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return true, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSandboxRaceLoser, "ContinueSession loser must surface ErrSandboxRaceLoser")
	require.Contains(t, err.Error(), "sandbox race")

	require.Equal(t, 0, d.sessions.clearContainerIDCalls, "alive winner — must not clear container_id")
	for _, ru := range d.sessions.resultUpdates {
		require.NotEqual(t, "failed", ru.status, "ContinueSession loser must not mark the session failed — winner owns the row")
	}
	for _, status := range d.sessions.statusUpdates {
		require.NotEqual(t, string(models.SessionStatusIdle), status, "loser must not flip the session back to idle — winner is mid-turn")
	}
}

// TestContinueSession_AcquireHoldLosesRaceClearsStaleOrphan covers the
// ContinueSession recovery branch: stale orphan container_id from a crashed
// prior worker must be CAS-cleared so the worker requeues.
func TestContinueSession_AcquireHoldLosesRaceClearsStaleOrphan(t *testing.T) {
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
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	d.users = fakeUserStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "follow up"},
	}
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "stale-orphan-container", nil
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return false, nil
	}
	var clearedID string
	d.sessions.clearContainerIDFn = func(expected string) (bool, error) {
		clearedID = expected
		return true, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared, "ContinueSession stale-orphan path must surface ErrStaleSandboxIDCleared")
	require.Equal(t, 1, d.sessions.clearContainerIDCalls)
	require.Equal(t, "stale-orphan-container", clearedID)
}

// TestContinueSession_AcquireHoldLosesRaceToPreviewRetries covers the case
// where a preview hydrate published container_id first. The container is
// alive, but no agent turn owns it yet; the continuation must not dead-letter
// silently as a duplicate job because there is no winning agent job to finish
// the user's turn. Instead it reverts the session to idle and returns a
// retryable preview-race sentinel so the next job attempt can attach to the
// preview container through the normal reuse path.
func TestContinueSession_AcquireHoldLosesRaceToPreviewRetries(t *testing.T) {
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
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	d.users = fakeUserStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "follow up"},
	}
	d.sessions.acquireHoldFn = func(proposed string) (string, error) {
		return "preview-container", nil
	}
	d.provider.IsAliveFn = func(ctx context.Context, sb *agent.Sandbox) (bool, error) {
		return true, nil
	}
	d.sessions.containerHoldStateFn = func(expected string) (bool, bool, error) {
		require.Equal(t, "preview-container", expected, "holder-state probe should inspect the winning container")
		return false, true, nil
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err, "ContinueSession should return the preview race for the worker to retry")
	require.ErrorIs(t, err, agent.ErrSandboxPreviewRace, "preview-held containers should be retried, not dead-lettered as duplicate agent jobs")
	require.NotErrorIs(t, err, agent.ErrSandboxRaceLoser, "preview-held containers should not be classified as duplicate agent jobs")
	require.Contains(t, d.sessions.statusUpdates, string(models.SessionStatusIdle), "preview race should revert session status so retry re-enters cleanly")
	require.Equal(t, 0, d.sessions.clearContainerIDCalls, "alive preview container must not be cleared")
	for _, ru := range d.sessions.resultUpdates {
		require.NotEqual(t, "failed", ru.status, "preview race should not mark the session failed")
	}
}

// TestContinueSession_AuthSocketClosedOnHydrateFailure verifies that the
// hydrate-failure branch in ContinueSession also tears down the listener.
// prepareSandboxGitHubAuth opens the listener before HydrateSandboxFromSnapshot
// runs, so a hydrate failure must close it explicitly — there's no
// container yet to attach a deferred close to.
func TestContinueSession_AuthSocketClosedOnHydrateFailure(t *testing.T) {
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
	// SnapshotKey forces the hydrate path, which fails when the snapshot
	// store can't fulfil the restore (the orchestrator's mock provider
	// returns an error from Restore by default unless we wire one up).
	snapshotKey := "snap-key"
	session.SnapshotKey = &snapshotKey

	d := defaultDeps()
	d.creds = &mockCredentialProvider{
		byProvider: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderAnthropic: {
				Provider: models.ProviderAnthropic,
				Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
			},
			models.ProviderSentry: {
				Provider: models.ProviderSentry,
				Config:   models.SentryConfig{AccessToken: "sentry-test"},
			},
		},
	}
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.users = fakeUserStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "retry me"},
	}
	hydrateErr := errors.New("hydrate failed")
	d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, _ io.Reader) error {
		return hydrateErr
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err, "ContinueSession should propagate the hydrate failure")
	require.Contains(t, err.Error(), "hydrate sandbox", "ContinueSession should surface the hydrate failure")

	require.Equal(t, 1, authStub.listenCalls, "auth socket must be opened before hydrate")
	require.Equal(t, 1, authStub.closeCalls, "auth socket must be closed when hydrate fails after the listener was opened")
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
	err := orch.ContinueSession(context.Background(), session, nil)
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
	err := orch.ContinueSession(context.Background(), session, nil)
	require.Error(t, err, "ContinueSession should fail when session worker ownership cannot be persisted")
	require.Contains(t, err.Error(), "persist session worker ownership", "ContinueSession should surface the worker ownership persistence failure")
	require.Equal(t, 1, d.provider.GetDestroyCalls(), "ContinueSession should destroy the sandbox when worker ownership persistence fails")
	require.Equal(t, 1, d.sessions.releaseHoldCalls, "ContinueSession should release the turn hold when worker ownership persistence fails")
	require.Equal(t, 1, d.sessions.finalizeCalls, "ContinueSession should finalize the container destroy when worker ownership persistence fails")
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusIdle), "ContinueSession should revert the session to idle when worker ownership persistence fails")
}

// TestContinueSession_SetWorkerNodeIDFailureResetsThread covers the orphan
// fix: when the worker-ownership CAS in SetWorkerNodeIDForContainer fails
// mid-turn (the production scenario behind the "Session is not active" +
// "Agent is working..." UI orphan), the orchestrator must reset BOTH the
// session.status AND the active thread.status back to idle. The handler's
// own thread reset is best-effort with a potentially-cancelled ctx; this
// orchestrator-level reset is the load-bearing one.
func TestContinueSession_SetWorkerNodeIDFailureResetsThread(t *testing.T) {
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

	threadID := uuid.New()

	d := defaultDeps()
	d.nodeID = "worker-a"
	d.sessions.setWorkerNodeErr = errors.New("persist failed")
	d.sessionThreads = &mockSessionThreadStore{}
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{
		{ID: 1, SessionID: session.ID, OrgID: orgID, ThreadID: &threadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "retry me"},
	}

	orch := buildOrchestrator(d)
	err := orch.ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{
		AgentType: models.AgentTypeClaudeCode,
		ThreadID:  &threadID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "persist session worker ownership")

	// Session-level revert (existing behavior).
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusIdle),
		"session.status must be reverted to idle")

	// New behavior: thread-level revert. Without this, the thread row stays
	// 'running' and the UI shows the dual-state orphan we hit in prod.
	statuses := d.sessionThreads.statuses()
	require.Contains(t, statuses, models.ThreadStatusIdle,
		"thread.status must be reverted to idle so the UI doesn't get stuck on 'Agent is working...'")
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
			err := orch.ContinueSession(context.Background(), session, nil)
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
			err := orch.ContinueSession(ctx, session, nil)
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
	err := orch.ContinueSession(ctx, session, nil)
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

// TestContinueSession_CodexAuthInjectInfraFailureDeferredToDeadLetter locks
// in the deferred-failure behavior for transient codex auth injection
// errors (the bug that motivated this change). When InjectCodexAuthForUser
// fails with a non-auth-invalid error — typically a docker exec/file-write
// error against a container that's been destroyed out-of-band — the
// failure must NOT churn session state on every retry: each attempt
// previously emitted a Linear "failed" milestone and flipped session
// status to "failed", so two retries followed by a successful attempt
// left Linear thinking the session had failed twice and the UI flashing
// failed banners that snapped back when the third attempt succeeded.
//
// The new contract: during retries (registry present, hooks not fired),
// the session row is left untouched (status stays "running" from the
// orchestrator's prelude, no Linear ping, no inline assistant message).
// On dead-letter (registry present, hooks fired), the full failure
// bookkeeping fires exactly once.
func TestContinueSession_CodexAuthInjectInfraFailureDeferredToDeadLetter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                      string
		withRegistry              bool
		runHooks                  bool
		wantInlineLinearMilestone bool
		wantInlineFailedResult    bool
		wantInlineAssistantMsg    bool
		wantPostHookLinearFail    bool
		wantPostHookFailed        bool
		wantPostHookAssistantMsg  bool
	}{
		{
			name:         "direct caller without registry: failure not deferred (existing inline-fail semantics for tests/RecoverSession-without-registry)",
			withRegistry: false,
			// Without a registry, registerSandboxInfraFailure no-ops the
			// hook (mirroring registerSandboxFailureMessage). The caller
			// is expected to handle the returned error directly. We
			// verify nothing is written either inline or post-hook so
			// the missing-registry semantics are explicit.
			wantInlineLinearMilestone: false,
			wantInlineFailedResult:    false,
			wantInlineAssistantMsg:    false,
			wantPostHookLinearFail:    false,
			wantPostHookFailed:        false,
			wantPostHookAssistantMsg:  false,
		},
		{
			name:                      "mid-retry (registry present, hooks not yet fired): zero session-state churn",
			withRegistry:              true,
			runHooks:                  false,
			wantInlineLinearMilestone: false,
			wantInlineFailedResult:    false,
			wantInlineAssistantMsg:    false,
			wantPostHookLinearFail:    false,
			wantPostHookFailed:        false,
			wantPostHookAssistantMsg:  false,
		},
		{
			name:                      "dead-letter (hooks fired): exactly one Linear failed ping, one failed result, one assistant message",
			withRegistry:              true,
			runHooks:                  true,
			wantInlineLinearMilestone: false,
			wantInlineFailedResult:    false,
			wantInlineAssistantMsg:    false,
			wantPostHookLinearFail:    true,
			wantPostHookFailed:        true,
			wantPostHookAssistantMsg:  true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			session := testRun(orgID, issue.ID)
			session.AgentType = models.AgentTypeCodex
			session.Status = string(models.SessionStatusIdle)
			session.CurrentTurn = 1
			// Reuse-path setup: container exists on this node and is alive,
			// so the orchestrator skips the IsAlive bail and proceeds to
			// auth injection. That's the codepath where the bug bit.
			containerID := "alive-container-on-this-node"
			session.ContainerID = &containerID
			thisNode := "worker-this-node"
			session.WorkerNodeID = &thisNode
			session.SandboxState = string(models.SandboxStateRunning)

			d := defaultDeps()
			d.nodeID = thisNode
			d.adapter.name = models.AgentTypeCodex
			d.issues.issue = issue
			d.messages.messages = []models.SessionMessage{{
				ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2,
				Role: models.MessageRoleUser, Content: "follow-up",
			}}
			// IsAlive returns true so the reuse path attaches and reaches
			// the codex auth injection.
			d.provider.IsAliveFn = func(context.Context, *agent.Sandbox) (bool, error) {
				return true, nil
			}
			// Codex token resolves successfully — the failure we're
			// exercising is the post-token sandbox-side write, not auth
			// invalidity.
			d.codexAuth = &mockCodexAuthProvider{
				cfg: &models.OpenAIChatGPTConfig{
					AccessToken:  "valid-access",
					RefreshToken: "valid-refresh",
					ExpiresAt:    time.Now().Add(time.Hour),
				},
			}
			// Force the docker mkdir invocation in writeCodexAuth to
			// fail the way docker reports a destroyed container. This
			// is exactly the error that surfaced in the original
			// incident, so the test pins the regression at the same
			// shape.
			d.provider.ExecFn = func(_ context.Context, _ *agent.Sandbox, cmd string, _, _ io.Writer) (int, error) {
				if strings.Contains(cmd, ".codex") {
					return 0, errors.New("Error response from daemon: No such container: alive-container-on-this-node")
				}
				return 0, nil
			}

			ctx := context.Background()
			if c.withRegistry {
				ctx = jobctx.WithDeadLetterHooks(ctx)
			}

			orch := buildOrchestrator(d)
			err := orch.ContinueSession(ctx, session, nil)
			require.Error(t, err)
			require.Contains(t, err.Error(), "codex auth injection",
				"caller-visible error must still wrap the codex auth injection prefix so worker logs and the session.last_error stay shaped the same as before")

			countLinearFailed := func() int {
				// linear.EnqueueMilestone packs the milestone name into
				// the payload's "event" field. Filter on that so the
				// assertion is meaningful even if a future change to
				// ContinueSession also enqueues other milestone events
				// (e.g. "started") on the same path.
				payload, ok := d.jobs.getPayload("linear_milestone").(map[string]any)
				if !ok {
					return 0
				}
				if event, _ := payload["event"].(string); event == string(linear.MilestoneFailed) {
					return 1
				}
				return 0
			}
			countFailedResult := func() int {
				var n int
				for _, r := range d.sessions.getResultUpdates() {
					if r.status == "failed" {
						n++
					}
				}
				return n
			}
			countAssistant := func() int {
				var n int
				for _, m := range d.messages.getMessages() {
					if m.Role == models.MessageRoleAssistant && m.SessionID == session.ID {
						n++
					}
				}
				return n
			}

			// Inline assertions (before any hook fires).
			if c.wantInlineLinearMilestone {
				require.Equal(t, 1, countLinearFailed(), "inline linear milestone expected before hook runs")
			} else {
				require.Zero(t, countLinearFailed(), "no Linear failed milestone should be enqueued inline — would emit a false 'failed' ping on every retry")
			}
			if c.wantInlineFailedResult {
				require.Equal(t, 1, countFailedResult(), "inline failed result expected before hook runs")
			} else {
				require.Zero(t, countFailedResult(), "no failed result should be persisted inline — would flicker session.status mid-retry")
			}
			if c.wantInlineAssistantMsg {
				require.Equal(t, 1, countAssistant(), "inline assistant message expected before hook runs")
			} else {
				require.Zero(t, countAssistant(), "no assistant message should be posted inline")
			}

			if c.runHooks {
				jobctx.RunDeadLetterHooks(ctx, err)
			}

			// Post-hook assertions.
			if c.wantPostHookLinearFail {
				require.Equal(t, 1, countLinearFailed(), "dead-letter hook should enqueue exactly one linear_milestone:failed")
			} else {
				require.Zero(t, countLinearFailed(), "no Linear failed milestone should be enqueued when hook does not fire")
			}
			if c.wantPostHookFailed {
				require.GreaterOrEqual(t, countFailedResult(), 1, "dead-letter hook should mark the session failed")
			} else {
				require.Zero(t, countFailedResult(), "session should not be marked failed when hook does not fire")
			}
			if c.wantPostHookAssistantMsg {
				require.Equal(t, 1, countAssistant(), "dead-letter hook should post exactly one assistant message")
			} else {
				require.Zero(t, countAssistant(), "no assistant message should be posted when hook does not fire")
			}
		})
	}
}

// TestContinueSession_CodexAuthInvalidStillFailsInline ensures the
// permanent-auth-failure branch (refresh token revoked / no usable
// credential) is unaffected by the deferred-infra-failure refactor. Auth
// invalidity is not retryable, so retries would loop forever — the
// failure must mark the session failed inline so the worker dead-letters
// promptly and the user gets the re-authenticate CTA on first hit.
func TestContinueSession_CodexAuthInvalidStillFailsInline(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeCodex
	session.Status = string(models.SessionStatusIdle)
	session.CurrentTurn = 1
	containerID := "alive-container-on-this-node"
	session.ContainerID = &containerID
	thisNode := "worker-this-node"
	session.WorkerNodeID = &thisNode
	session.SandboxState = string(models.SandboxStateRunning)

	d := defaultDeps()
	d.nodeID = thisNode
	d.adapter.name = models.AgentTypeCodex
	d.issues.issue = issue
	d.messages.messages = []models.SessionMessage{{
		ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2,
		Role: models.MessageRoleUser, Content: "follow-up",
	}}
	d.provider.IsAliveFn = func(context.Context, *agent.Sandbox) (bool, error) {
		return true, nil
	}
	// Refresh-token revoked: the codex auth provider returns an
	// ErrCodexAuthInvalid-tagged error so InjectCodexAuthForUser
	// surfaces auth invalidity, not transient infra.
	d.codexAuth = &mockCodexAuthProvider{
		err: agent.ErrCodexAuthInvalid,
	}

	ctx := jobctx.WithDeadLetterHooks(context.Background())
	orch := buildOrchestrator(d)
	err := orch.ContinueSession(ctx, session, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "codex auth injection")

	// Inline assertions: the session must be marked failed BEFORE any
	// dead-letter hook fires. Auth invalidity is permanent — retrying
	// won't help — so we want the worker to dead-letter on the next
	// poll rather than burning attempts.
	failedResults := 0
	for _, r := range d.sessions.getResultUpdates() {
		if r.status == "failed" {
			failedResults++
		}
	}
	require.GreaterOrEqual(t, failedResults, 1, "ErrCodexAuthInvalid must mark the session failed inline so the user gets the re-authenticate CTA without waiting for the retry budget to exhaust")

	failedFailures := 0
	for _, f := range d.sessions.getFailureUpdates() {
		if f.category == agent.FailureCategoryCodexAuth {
			failedFailures++
		}
	}
	require.GreaterOrEqual(t, failedFailures, 1, "auth-invalid path must record the codex_auth_expired category inline")
}

// TestRunAgent_CodexAuthInjectInfraFailureDeferredToDeadLetter mirrors
// TestContinueSession_CodexAuthInjectInfraFailureDeferredToDeadLetter for
// the RunAgent call site (ensureCodexAuth at orchestrator.go:1908). Both
// call sites share ensureCodexAuth, so the deferred behavior must hold
// regardless of which orchestration entry point hit the failure.
func TestRunAgent_CodexAuthInjectInfraFailureDeferredToDeadLetter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                     string
		runHooks                 bool
		wantInlineLinearFail     bool
		wantInlineFailedResult   bool
		wantPostHookLinearFail   bool
		wantPostHookFailedResult bool
	}{
		{
			name:                     "mid-retry: zero session-state churn",
			runHooks:                 false,
			wantInlineLinearFail:     false,
			wantInlineFailedResult:   false,
			wantPostHookLinearFail:   false,
			wantPostHookFailedResult: false,
		},
		{
			name:                     "dead-letter: failed result + Linear failed enqueued exactly once",
			runHooks:                 true,
			wantInlineLinearFail:     false,
			wantInlineFailedResult:   false,
			wantPostHookLinearFail:   true,
			wantPostHookFailedResult: true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			run := testRun(orgID, issue.ID)
			run.AgentType = models.AgentTypeCodex

			d := defaultDeps()
			d.adapter.name = models.AgentTypeCodex
			d.issues.issue = issue
			d.codexAuth = &mockCodexAuthProvider{
				cfg: &models.OpenAIChatGPTConfig{
					AccessToken:  "valid-access",
					RefreshToken: "valid-refresh",
					ExpiresAt:    time.Now().Add(time.Hour),
				},
			}
			// Sandbox creation succeeds; the failure we exercise is
			// the post-clone codex auth injection's docker exec.
			d.provider.CreateFn = func(_ context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
				return &agent.Sandbox{ID: "sbx-1", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
			}
			d.provider.ExecFn = func(_ context.Context, _ *agent.Sandbox, cmd string, _, _ io.Writer) (int, error) {
				if strings.Contains(cmd, ".codex") {
					return 0, errors.New("Error response from daemon: No such container: sbx-1")
				}
				return 0, nil
			}

			ctx := jobctx.WithDeadLetterHooks(context.Background())
			orch := buildOrchestrator(d)
			err := orch.RunAgent(ctx, run)
			require.Error(t, err)
			require.Contains(t, err.Error(), "codex auth injection")

			countLinearFailed := func() int {
				payload, ok := d.jobs.getPayload("linear_milestone").(map[string]any)
				if !ok {
					return 0
				}
				if event, _ := payload["event"].(string); event == string(linear.MilestoneFailed) {
					return 1
				}
				return 0
			}
			countFailedResult := func() int {
				var n int
				for _, r := range d.sessions.getResultUpdates() {
					if r.status == "failed" {
						n++
					}
				}
				return n
			}

			if c.wantInlineLinearFail {
				require.Equal(t, 1, countLinearFailed(), "inline linear_milestone:failed expected before hook runs")
			} else {
				require.Zero(t, countLinearFailed(), "no inline linear_milestone:failed should be enqueued during retry — would emit a false 'failed' ping on every attempt")
			}
			if c.wantInlineFailedResult {
				require.Equal(t, 1, countFailedResult(), "inline failed result expected before hook runs")
			} else {
				require.Zero(t, countFailedResult(), "no inline failed result should be persisted during retry — would flicker session.status mid-flight")
			}

			if c.runHooks {
				jobctx.RunDeadLetterHooks(ctx, err)
			}

			if c.wantPostHookLinearFail {
				require.Equal(t, 1, countLinearFailed(), "dead-letter hook should enqueue exactly one linear_milestone:failed")
			} else {
				require.Zero(t, countLinearFailed(), "no linear_milestone:failed should be enqueued when hook does not fire")
			}
			if c.wantPostHookFailedResult {
				require.GreaterOrEqual(t, countFailedResult(), 1, "dead-letter hook should mark the session failed")
			} else {
				require.Zero(t, countFailedResult(), "session should not be marked failed when hook does not fire")
			}
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
	err := orch.ContinueSession(context.Background(), session, nil)
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

func TestRunAgent_CodexTokenExpiredRetryKeepsTriggeredUserScope(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	userID := uuid.New()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	run.AgentType = models.AgentTypeCodex
	run.TriggeredByUserID = &userID

	d := defaultDeps()
	d.adapter.name = models.AgentTypeCodex
	d.codexAuth = nil
	d.codingCreds = &mockCodingCredentialProvider{
		requiredUserID: &userID,
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderOpenAISubscription: {
				{
					ID:       uuid.New(),
					OrgID:    orgID,
					UserID:   &userID,
					Provider: models.ProviderOpenAISubscription,
					Priority: 1,
					Status:   models.CodingCredentialStatusActive,
					Config: models.OpenAISubscriptionConfig{
						AccessToken:  "personal-access",
						RefreshToken: "personal-refresh",
						ExpiresAt:    time.Now().Add(time.Hour),
					},
				},
			},
		},
	}

	executeCalls := 0
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		executeCalls++
		if executeCalls == 1 {
			return &agent.AgentResult{
				Error:           "codex CLI exited with code 1: auth error code: token_expired",
				ExitCode:        1,
				ConfidenceScore: 0.1,
			}, nil
		}
		return &agent.AgentResult{
			Summary:         "retry succeeded",
			ConfidenceScore: 0.9,
			ExitCode:        0,
		}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)

	require.NoError(t, err, "RunAgent should recover from token_expired when the personal subscription is still resolvable")
	require.Equal(t, 2, executeCalls, "RunAgent should execute a retry using the triggering user's credential scope")
	authData, ok := d.provider.Files["/home/sandbox/.codex/auth.json"]
	require.True(t, ok, "retry should keep writing Codex auth.json from the personal subscription")
	var authJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(authData, &authJSON), "auth.json should remain valid after retry injection")
	tokens, ok := authJSON["tokens"].(map[string]interface{})
	require.True(t, ok, "auth.json should contain tokens after retry injection")
	require.Equal(t, "personal-access", tokens["access_token"], "retry injection should use the triggering user's personal subscription")
}

func TestRunAgent_CancelReturnsToIdle(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	cancelReg := agent.NewCancelRegistry(zerolog.Nop())

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	d := defaultDeps()
	d.logger = &logger
	d.cancels = cancelReg
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("cancel-snapshot"))), nil
	}
	// Make Exec fail so doCancel falls back to immediate context cancel
	// (avoids 30s timer wait in tests).
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "git ") {
			return 0, nil
		}
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

	completion := findLogEvent(t, &logs, "agent run finished")
	require.NotNil(t, completion, "cancelled RunAgent should emit agent run finished log")
	require.Equal(t, "cancelled", completion["outcome"], "cancelled completion log should include cancelled outcome")
	require.Equal(t, string(models.RuntimeStopReasonUserCancel), completion["stop_reason"], "cancelled completion log should include user cancel stop reason")
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
		if strings.HasPrefix(cmd, "git ") {
			return 0, nil
		}
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

func TestRunAgent_TimeoutLogIncludesPlatformHealthFields(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	startedAt := time.Now().Add(-10 * time.Minute)
	run.StartedAt = &startedAt

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	d := defaultDeps()
	d.logger = &logger
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return nil, context.DeadlineExceeded
	}

	orch := buildOrchestrator(d)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	err := orch.RunAgent(ctx, run)
	require.Error(t, err)
	require.ErrorIs(t, err, agent.ErrSessionTimedOut)

	timeout := findLogEvent(t, &logs, "session exceeded configured timeout")
	require.NotNil(t, timeout, "timeout RunAgent should emit the canonical timeout log line")
	require.Equal(t, string(models.AgentTypeClaudeCode), timeout["agent_type"], "timeout log should include agent type")
	require.Equal(t, "timeout", timeout["outcome"], "timeout log should include platform outcome")
	durationMS, ok := timeout["duration_ms"].(float64)
	require.True(t, ok, "timeout log should include numeric duration_ms")
	require.GreaterOrEqual(t, durationMS, float64(0), "timeout duration should be non-negative")

	// The canonical timeout log is the only failure event emitted on this
	// path. Emitting a second "agent run failed" log would double-count
	// timeouts in dashboards/alerts that key off either message.
	require.Nil(t, findLogEvent(t, &logs, "agent run failed"), "timeout path should not also emit agent run failed log")
}

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

	err := orch.ContinueSession(ctx, session, nil)
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

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	d := defaultDeps()
	d.logger = &logger
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID, Settings: settings}}
	d.cancels = cancelReg
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("checkpoint-after-no-progress"))), nil
	}
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "git ") {
			return 0, nil
		}
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

	completion := findLogEvent(t, &logs, "agent run finished")
	require.NotNil(t, completion, "policy-stopped RunAgent should emit agent run finished log")
	require.Equal(t, "runtime_policy_stopped", completion["outcome"], "policy stop completion log should include policy outcome")
	require.Equal(t, string(models.RuntimeStopReasonNoProgress), completion["stop_reason"], "policy stop completion log should include runtime stop reason")
}

func TestRunAgent_PolicyStopPersistsTerminalStateBeforeMentionWarmup(t *testing.T) {
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

	var (
		eventMu sync.Mutex
		events  []string
	)
	recordEvent := func(event string) {
		eventMu.Lock()
		defer eventMu.Unlock()
		events = append(events, event)
	}

	cancelReg := agent.NewCancelRegistry(zerolog.Nop())
	d := defaultDeps()
	d.cancels = cancelReg
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID, Settings: settings}}
	d.sessions.eventHook = recordEvent
	d.mentionIndexes = workspace.NewMentionIndexCache(workspace.MentionIndexCacheConfig{})
	d.fileReader = &mockFileReader{
		listDirFn: func(ctx context.Context, containerID, workDir, dirPath string) ([]sandbox.FileEntry, error) {
			recordEvent("mention_warm")
			return nil, errors.New("mention warm should not block terminal status")
		},
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("checkpoint-before-warm"))), nil
	}
	d.provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "git ") {
			return 0, nil
		}
		return 1, errors.New("exec not available in test")
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		<-ctx.Done()
		return &agent.AgentResult{
			Summary:         "Interrupted cleanly",
			ConfidenceScore: 0.4,
			AgentSessionID:  "agent-checkpoint-before-warm",
			ExitCode:        1,
		}, ctx.Err()
	}

	err = buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "policy stop should be handled internally after terminal state is persisted")

	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventMu.Unlock()
	require.Contains(t, gotEvents, "session_failure", "policy stop should persist session failure details")
	require.Contains(t, gotEvents, "mention_warm", "policy stop should still attempt mention-index warmup after checkpointing")
	require.Less(t, indexOfEvent(gotEvents, "session_failure"), indexOfEvent(gotEvents, "mention_warm"), "terminal session state should be persisted before mention-index warmup")
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

func TestRunAgent_PublishesCheckpointForHumanInputPauseWithNonZeroExit(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)

	d := defaultDeps()
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("human-input-checkpoint"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			RequiresHumanInput: true,
			AgentSessionID:     "agent-human-input-1",
			ExitCode:           1,
			Error:              "deferred human input",
		}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.NoError(t, err, "human-input pauses should be handled internally")

	checkpoints := d.sessions.getCheckpointUpdates()
	require.NotEmpty(t, checkpoints, "human-input pause should publish checkpoint metadata even when the provider exits non-zero")
	require.Equal(t, models.CheckpointKindGracefulStop, checkpoints[len(checkpoints)-1].kind, "human-input pause should publish a graceful-stop checkpoint")
	require.Equal(t, "agent-human-input-1", checkpoints[len(checkpoints)-1].agentSessionID, "checkpoint should preserve provider session id for resume")
	require.NotEmpty(t, checkpoints[len(checkpoints)-1].snapshotKey, "human-input pause should persist the checkpoint snapshot key")
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusAwaitingInput), "human-input pause should leave the session awaiting input")
}

func TestRunAgent_DoesNotMarkAwaitingInputWhenHumanInputCheckpointFails(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	threadID := uuid.New()
	run.PrimaryThreadID = &threadID

	d := defaultDeps()
	d.sessionThreads = &mockSessionThreadStore{}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return nil, errors.New("snapshot unavailable")
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		return &agent.AgentResult{
			RequiresHumanInput: true,
			AgentSessionID:     "agent-human-input-snapshot-failed",
			ExitCode:           1,
			Error:              "deferred human input",
		}, nil
	}

	err := buildOrchestrator(d).RunAgent(context.Background(), run)
	require.Error(t, err, "RunAgent should fail the pause when the human-input checkpoint cannot be persisted")
	require.Contains(t, err.Error(), "human input checkpoint", "RunAgent should explain that the checkpoint persistence blocked the pause")
	require.Empty(t, d.sessions.getCheckpointUpdates(), "RunAgent should not publish checkpoint metadata when snapshot persistence fails")
	require.NotContains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusAwaitingInput), "RunAgent should not expose an answerable request without a durable checkpoint")
	results := d.sessions.getResultUpdates()
	require.NotEmpty(t, results, "RunAgent should persist a terminal result when checkpoint persistence fails")
	require.Equal(t, string(models.SessionStatusFailed), results[len(results)-1].status, "RunAgent should leave the session in a non-answerable failed state")
	require.Contains(t, d.sessionThreads.statuses(), models.ThreadStatusFailed, "RunAgent should fail the active thread when the human-input pause cannot be made answerable")
}

func TestRunAgent_DoesNotMarkAwaitingInputWhenHumanInputCheckpointMetadataFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		setupSessionStore     func(*mockSessionStore)
		expectCheckpointWrite bool
	}{
		{
			name: "publish returns error",
			setupSessionStore: func(store *mockSessionStore) {
				store.publishCheckpointErr = errors.New("checkpoint write failed")
			},
		},
		{
			name: "publish rejected",
			setupSessionStore: func(store *mockSessionStore) {
				published := false
				store.publishCheckpointOK = &published
			},
		},
		{
			name: "snapshot metadata update returns error",
			setupSessionStore: func(store *mockSessionStore) {
				store.updateSnapshotInfoErr = errors.New("snapshot metadata failed")
			},
			expectCheckpointWrite: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			run := testRun(orgID, issue.ID)
			threadID := uuid.New()
			run.PrimaryThreadID = &threadID

			d := defaultDeps()
			d.sessionThreads = &mockSessionThreadStore{}
			tt.setupSessionStore(d.sessions)
			d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader([]byte("human-input-checkpoint"))), nil
			}
			d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
				return &agent.AgentResult{
					RequiresHumanInput: true,
					AgentSessionID:     "agent-human-input-metadata-failed",
					ExitCode:           1,
					Error:              "deferred human input",
				}, nil
			}

			err := buildOrchestrator(d).RunAgent(context.Background(), run)
			require.Error(t, err, "RunAgent should fail the pause when checkpoint metadata is not durable")
			require.Contains(t, err.Error(), "human input", "RunAgent should explain that human-input pause persistence failed")
			if tt.expectCheckpointWrite {
				require.NotEmpty(t, d.sessions.getCheckpointUpdates(), "RunAgent should record the checkpoint write before failing on later metadata persistence")
			} else {
				require.Empty(t, d.sessions.getCheckpointUpdates(), "RunAgent should not record checkpoint metadata when publish fails")
			}
			require.NotContains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusAwaitingInput), "RunAgent should not expose an answerable request when metadata persistence fails")
			results := d.sessions.getResultUpdates()
			require.NotEmpty(t, results, "RunAgent should persist a terminal result when metadata persistence fails")
			require.Equal(t, string(models.SessionStatusFailed), results[len(results)-1].status, "RunAgent should leave the session in a non-answerable failed state")
			require.Contains(t, d.sessionThreads.statuses(), models.ThreadStatusFailed, "RunAgent should fail the active thread when metadata persistence fails")
		})
	}
}

func TestRunAgent_DoesNotExposeHumanInputAsAnswerableBeforeCheckpoint(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	issue := testIssue(orgID)
	run := testRun(orgID, issue.ID)
	logPersisted := make(chan struct{})
	allowAdapterReturn := make(chan struct{})
	runDone := make(chan error, 1)
	var logOnce sync.Once
	released := false
	doneRead := false

	d := defaultDeps()
	d.logs = &mockSessionLogStore{
		onCreate: func(log models.SessionLog) {
			if log.Level == "human_input" {
				logOnce.Do(func() { close(logPersisted) })
			}
		},
	}
	d.provider.SnapshotFn = func(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("human-input-checkpoint"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "human_input",
			Message:   "Approve Bash?",
			HumanInput: &agent.HumanInputRequest{
				ProviderRequestID: "toolu_early",
				Kind:              models.HumanInputRequestKindToolApproval,
				Title:             "Approve Bash?",
				Body:              "Claude needs approval before it can continue.",
				Choices: []models.HumanInputChoice{
					{ID: "approve", Label: "Approve"},
					{ID: "deny", Label: "Deny"},
				},
			},
		}
		<-allowAdapterReturn
		return &agent.AgentResult{
			RequiresHumanInput: true,
			AgentSessionID:     "agent-human-input-early",
			ExitCode:           1,
			Error:              "deferred human input",
		}, nil
	}

	go func() {
		runDone <- buildOrchestrator(d).RunAgent(context.Background(), run)
	}()
	t.Cleanup(func() {
		if !released {
			close(allowAdapterReturn)
		}
		if !doneRead {
			require.NoError(t, <-runDone, "RunAgent should finish after the blocked adapter is released")
		}
	})

	require.Eventually(t, func() bool {
		select {
		case <-logPersisted:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond, "human-input request log should be persisted while the adapter is still paused")

	require.Len(t, d.humanInputs.getRequests(), 1, "RunAgent should persist the human-input request as soon as the provider emits it")
	require.NotContains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusAwaitingInput), "RunAgent should not mark the session answerable before the pause checkpoint is published")

	close(allowAdapterReturn)
	released = true
	err := <-runDone
	doneRead = true
	require.NoError(t, err, "RunAgent should handle the deferred human-input pause internally")
	require.NotEmpty(t, d.sessions.getCheckpointUpdates(), "RunAgent should publish a checkpoint before making the request answerable")
	require.Contains(t, d.sessions.getStatusUpdates(), string(models.SessionStatusAwaitingInput), "RunAgent should mark the session awaiting input after checkpoint publication")
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
		if strings.HasPrefix(cmd, "git ") {
			return 0, nil
		}
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
	err := orch.ContinueSession(context.Background(), session, nil)
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

// TestOrchestrator_RehydrateSandboxAuth_NoSandboxAuth covers the orchestrator
// wrapper's "skip when sandboxAuth is nil" bail path. defaultDeps doesn't
// wire a SandboxAuth, so the wrapper must short-circuit at the first nil
// check and return (nil, nil) without touching the session store.
func TestOrchestrator_RehydrateSandboxAuth_NoSandboxAuth(t *testing.T) {
	t.Parallel()
	d := defaultDeps()
	// Pre-seed the session store with a page so we can prove the wrapper
	// never queried it (containerHoldingCalls stays at 0).
	d.sessions.containerHoldingPages = [][]models.Session{{
		models.Session{ID: uuid.New(), OrgID: uuid.New()},
	}}
	orch := buildOrchestrator(d)

	keep, err := orch.RehydrateSandboxAuthListeners(context.Background())
	require.NoError(t, err)
	require.Nil(t, keep, "the orchestrator wrapper must return a nil keep when sandboxAuth isn't wired so callers skip the sweep")
	require.Equal(t, 0, d.sessions.containerHoldingCalls, "wrapper must short-circuit before touching the session store when sandboxAuth is nil")
}

// TestOrchestrator_RehydrateSandboxAuth_SuccessPath covers the wrapper's
// happy path: with a non-nil SandboxAuth and a session store that satisfies
// ContainerHoldingSessionLister, the wrapper plumbs through to the
// freestanding helper and returns its keep set. MockSandboxProvider's
// default IsAlive returns true, so the seeded row goes all the way through
// — IsAlive → repo lookup → Listen — and lands in the keep set. That
// exercises every line of the wrapper (213-231) plus most of the per-row
// success path in the freestanding helper.
func TestOrchestrator_RehydrateSandboxAuth_SuccessPath(t *testing.T) {
	t.Parallel()
	agent.SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { agent.SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	orgID := testOrg()
	repoID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	containerID := "container-rehydrate-success"
	sessionID := uuid.New()
	d := defaultDeps()
	d.nodeID = "worker-a"
	authStub := &fakeSandboxAuthServer{}
	d.sandboxAuth = authStub
	d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
	d.users = fakeUserStore{}
	cid := containerID
	d.sessions.containerHoldingPages = [][]models.Session{{
		models.Session{ID: sessionID, OrgID: orgID, ContainerID: &cid, RepositoryID: &repoID},
	}}
	orch := buildOrchestrator(d)

	keep, err := orch.RehydrateSandboxAuthListeners(context.Background())
	require.NoError(t, err)
	require.NotNil(t, keep, "wrapper success path must return non-nil keep so the caller knows sweep is safe")
	require.Contains(t, keep, sessionID, "the alive container's session must be Listen'd and added to the keep set")
	require.GreaterOrEqual(t, d.sessions.containerHoldingCalls, 1, "wrapper must have queried the session store at least once")
	require.Equal(t, 1, authStub.listenCalls, "Listen must have been called exactly once for the seeded session")
}
