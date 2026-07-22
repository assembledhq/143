package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/assembledhq/143/internal/services/workspace"
)

func TestSessionPromptStyleCodeReviewUsesRawTask(t *testing.T) {
	t.Parallel()

	session := &models.Session{Origin: models.SessionOriginCodeReview}

	require.Equal(t, PromptStyleRawTask, sessionPromptStyle(session), "code review sessions should pass the stored synthesis task through verbatim")
}

func TestRecordedRunFailureErrorSurvivesWrapping(t *testing.T) {
	t.Parallel()

	recorded := markRunFailureRecorded(errors.New("categorized auth failure"))
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "direct marker", err: recorded, expected: true},
		{name: "wrapped marker", err: fmt.Errorf("setup sandbox: %w", recorded), expected: true},
		{name: "ordinary error", err: errors.New("generic failure"), expected: false},
		{name: "nil error", err: nil, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, isRunFailureRecorded(tt.err),
				"recorded failure detection should survive contextual wrapping without matching generic errors")
		})
	}
}

func TestRunAgentRecordsUsageOnlyAfterTurnHoldIsPublished(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("orchestrator.go")
	require.NoError(t, err, "orchestrator.go should be readable for lifecycle ordering regression test")

	body := string(src)
	runStart := strings.Index(body, "func (o *Orchestrator) RunAgent(")
	continueStart := strings.Index(body, "func (o *Orchestrator) ContinueSession(")
	require.NotEqual(t, -1, runStart, "RunAgent should exist")
	require.NotEqual(t, -1, continueStart, "ContinueSession should exist")
	require.Less(t, runStart, continueStart, "RunAgent should appear before ContinueSession in orchestrator.go")

	runBody := body[runStart:continueStart]
	hold := strings.Index(runBody, "o.sessions.AcquireTurnHold")
	usage := strings.Index(runBody, "o.usageTracker.ContainerStarted")
	require.NotEqual(t, -1, hold, "RunAgent should publish the turn hold")
	require.NotEqual(t, -1, usage, "RunAgent should record container usage")
	require.Less(t, hold, usage, "RunAgent should record usage only after the DB row owns the container so pre-hold crashes do not create open usage events for unowned containers")
}

func TestContinueSessionWaitsForReusedCodeReviewWorkspaceBeforeTurnHold(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("orchestrator.go")
	require.NoError(t, err, "orchestrator.go should be readable for workspace readiness ordering regression test")

	body := string(src)
	continueStart := strings.Index(body, "func (o *Orchestrator) ContinueSession(")
	require.NotEqual(t, -1, continueStart, "ContinueSession should exist")
	continueBody := body[continueStart:]
	barrier := strings.Index(continueBody, "waitForSandboxWorkspaceReady(")
	hold := strings.Index(continueBody, "o.sessions.AcquireTurnHold")
	require.NotEqual(t, -1, barrier, "ContinueSession should wait for the reused code review workspace")
	require.NotEqual(t, -1, hold, "ContinueSession should acquire the shared sandbox turn hold")
	require.Less(t, barrier, hold, "workspace readiness must be proven before acquiring the shared turn hold so a waiting sibling cannot release the winner's hold")
}

func TestThreadRuntimeAlreadyActiveDoesNotFailSessionBeforeRetry(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("orchestrator.go")
	require.NoError(t, err, "orchestrator.go should be readable for active-runtime retry regression test")

	body := string(src)
	for _, tt := range []struct {
		name      string
		start     string
		nextStart string
	}{
		{
			name:      "RunAgent",
			start:     "threadRuntimeCtl, err = o.startThreadRuntimeControl(ctx, run, *primaryThreadID, sandbox",
			nextStart: "if threadRuntimeCtl != nil {",
		},
		{
			name:      "ContinueSession",
			start:     "threadRuntimeCtl, err = o.startThreadRuntimeControl(ctx, session, *opts.ThreadID, sandbox",
			nextStart: "if threadRuntimeCtl != nil {",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			start := strings.Index(body, tt.start)
			require.NotEqual(t, -1, start, "orchestrator should start thread runtime control in "+tt.name)
			remainder := body[start:]
			end := strings.Index(remainder, tt.nextStart)
			require.NotEqual(t, -1, end, "orchestrator should continue after thread runtime control in "+tt.name)

			block := remainder[:end]
			activeRuntimeCheck := strings.Index(block, "errors.Is(err, ErrThreadRuntimeAlreadyActive)")
			failRun := strings.Index(block, "o.failRun")
			require.NotEqual(t, -1, activeRuntimeCheck, "active runtime conflicts should be recognized before generic failure cleanup in "+tt.name)
			require.NotEqual(t, -1, failRun, "non-retryable thread runtime startup errors should still use generic failure cleanup in "+tt.name)
			require.Less(t, activeRuntimeCheck, failRun, "active runtime conflicts should return for worker retry before marking the session failed in "+tt.name)
		})
	}
}

func TestWarmMentionIndexFromSandboxAsyncDoesNotBlockCaller(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	reader := &blockingMentionIndexFileReader{release: release}
	o := &Orchestrator{
		fileReader:     reader,
		mentionIndexes: workspace.NewMentionIndexCache(workspace.MentionIndexCacheConfig{}),
	}
	session := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	liveSandbox := &Sandbox{ID: "container-1", WorkDir: "/workspace"}

	done := make(chan struct{})
	go func() {
		o.warmMentionIndexFromSandboxAsync(context.Background(), session, liveSandbox, "snapshot-1", zerolog.Nop())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(25 * time.Millisecond):
		close(release)
		require.Fail(t, "async mention-index warmup should return before the workspace traversal completes")
		return
	}
	close(release)
}

func TestPrepareSandboxRepositoryRunsBootstrapCommandsFromWorkDir(t *testing.T) {
	t.Parallel()

	workDir := "/home/sandbox/backend"
	provider := &testInternalSandboxProvider{
		readFiles: map[string][]byte{
			path.Join(workDir, repoconfig.ConfigPath): []byte(`{
				"bootstrap": {
					"commands": ["npm install", "npm run lint:js -w assets"]
				}
			}`),
		},
	}
	o := &Orchestrator{provider: provider}
	sandbox := &Sandbox{ID: "sandbox-1", WorkDir: workDir}

	err := o.prepareSandboxRepository(context.Background(), sandbox, workDir, zerolog.Nop())

	require.NoError(t, err, "prepareSandboxRepository should run configured bootstrap commands successfully")
	require.Equal(t, []string{
		"cd '/home/sandbox/backend' && npm install",
		"cd '/home/sandbox/backend' && npm run lint:js -w assets",
	}, provider.execCalls, "bootstrap commands should run in order from the repository workdir")
}

func TestPrepareSandboxRepositoryReturnsBootstrapCommandFailure(t *testing.T) {
	t.Parallel()

	workDir := "/home/sandbox/backend"
	provider := &testInternalSandboxProvider{
		execExit:   127,
		execStderr: "sh: eslint: not found",
		readFiles: map[string][]byte{
			path.Join(workDir, repoconfig.ConfigPath): []byte(`{
				"bootstrap": {
					"commands": ["npm run lint:js -w assets"]
				}
			}`),
		},
	}
	o := &Orchestrator{provider: provider}
	sandbox := &Sandbox{ID: "sandbox-1", WorkDir: workDir}

	err := o.prepareSandboxRepository(context.Background(), sandbox, workDir, zerolog.Nop())

	require.Error(t, err, "prepareSandboxRepository should fail when a configured bootstrap command exits non-zero")
	require.Contains(t, err.Error(), "npm run lint:js -w assets", "bootstrap setup error should identify the command that failed")
	require.Contains(t, err.Error(), "sh: eslint: not found", "bootstrap setup error should include stderr so missing tool failures are actionable")
}

func TestPrepareSandboxRepositoryMaterializesEmptyRepoReadinessConfig(t *testing.T) {
	t.Parallel()

	workDir := "/home/sandbox/backend"
	repoID := uuid.New()
	orgID := uuid.New()
	provider := &testInternalSandboxProvider{
		readFiles: map[string][]byte{
			path.Join(workDir, repoconfig.ConfigPath): []byte(`{"pr_readiness":{"checks":[]}}`),
		},
	}
	readiness := &testInternalPRReadinessStore{}
	o := &Orchestrator{provider: provider, prReadiness: readiness}
	sandbox := &Sandbox{ID: "sandbox-1", WorkDir: workDir}
	session := &models.Session{ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID}

	err := o.prepareSandboxRepository(context.Background(), sandbox, workDir, zerolog.Nop(), session)

	require.NoError(t, err, "prepareSandboxRepository should accept an empty repo readiness config")
	require.True(t, readiness.materializeCalled, "empty repo readiness config should still materialize so stale repo-config checks are deactivated")
	require.Equal(t, orgID, readiness.materializeOrgID, "materialization should be org-scoped")
	require.Equal(t, repoID, readiness.materializeRepoID, "materialization should be repository-scoped")
	require.Empty(t, readiness.materializeChecks, "empty repo readiness config should pass an empty check set")
}

func TestPrepareSandboxRepositoryClearsRepoReadinessWhenConfigMissing(t *testing.T) {
	t.Parallel()

	workDir := "/home/sandbox/backend"
	repoID := uuid.New()
	orgID := uuid.New()
	provider := &testInternalSandboxProvider{}
	readiness := &testInternalPRReadinessStore{}
	o := &Orchestrator{provider: provider, prReadiness: readiness}
	sandbox := &Sandbox{ID: "sandbox-1", WorkDir: workDir}
	session := &models.Session{ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID}

	err := o.prepareSandboxRepository(context.Background(), sandbox, workDir, zerolog.Nop(), session)

	require.NoError(t, err, "prepareSandboxRepository should tolerate missing repo config")
	require.True(t, readiness.materializeCalled, "missing repo config should materialize an empty check set so stale repo-config checks are deactivated")
	require.Equal(t, orgID, readiness.materializeOrgID, "materialization should remain org-scoped when repo config is missing")
	require.Equal(t, repoID, readiness.materializeRepoID, "materialization should remain repository-scoped when repo config is missing")
	require.Empty(t, readiness.materializeChecks, "missing repo config should clear repo-config readiness checks")
}

type blockingMentionIndexFileReader struct {
	release chan struct{}
}

func (r *blockingMentionIndexFileReader) ListDir(ctx context.Context, _, _, _ string) ([]sandbox.FileEntry, error) {
	select {
	case <-r.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *blockingMentionIndexFileReader) ReadFile(context.Context, string, string, string) (string, bool, error) {
	return "", false, errors.New("not used")
}

func (r *blockingMentionIndexFileReader) ReadFileContext(context.Context, string, string, string, int, int, int) (sandbox.FileContextResult, error) {
	return sandbox.FileContextResult{}, errors.New("not used")
}

type testInternalSessionLogStore struct {
	logs             []models.SessionLog
	markedThreadID   *uuid.UUID
	markedOrgID      uuid.UUID
	markedSessionID  uuid.UUID
	markedTurnNumber int
	markedMessage    string
}

func (s *testInternalSessionLogStore) Create(ctx context.Context, log *models.SessionLog) error {
	s.logs = append(s.logs, *log)
	return nil
}

func (s *testInternalSessionLogStore) MarkAssistantTranscriptDuplicate(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	threadID *uuid.UUID,
	turnNumber int,
	message string,
) error {
	s.markedOrgID = orgID
	s.markedSessionID = sessionID
	if threadID != nil {
		copied := *threadID
		s.markedThreadID = &copied
	}
	s.markedTurnNumber = turnNumber
	s.markedMessage = message
	return nil
}

type testInternalSessionMessageStore struct {
	messages []models.SessionMessage
}

func (s *testInternalSessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	s.messages = append(s.messages, *msg)
	return nil
}

func (s *testInternalSessionMessageStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error) {
	return nil, nil
}

type testInternalUserLookup struct {
	user models.User
	err  error
}

func (s testInternalUserLookup) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.User, error) {
	if s.err != nil {
		return models.User{}, s.err
	}
	return s.user, nil
}

type testInternalIssueStore struct {
	issue models.Issue
	err   error
}

func (s testInternalIssueStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Issue, error) {
	if s.err != nil {
		return models.Issue{}, s.err
	}
	return s.issue, nil
}

func (s testInternalIssueStore) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, models.IssueStatus) error {
	return s.err
}

type testInternalRepoStore struct {
	repo models.Repository
	err  error
}

func (s testInternalRepoStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	return s.repo, nil
}

type testInternalGitHubTokens struct {
	token string
	err   error
}

func (s testInternalGitHubTokens) GetInstallationToken(context.Context, int64) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.token, nil
}

type testInternalPRReadinessStore struct {
	materializeCalled bool
	materializeOrgID  uuid.UUID
	materializeRepoID uuid.UUID
	materializeChecks []models.PRReadinessCustomCheck
}

func (s *testInternalPRReadinessStore) MaterializeRepoConfigChecks(_ context.Context, orgID, repositoryID uuid.UUID, checks []models.PRReadinessCustomCheck) error {
	s.materializeCalled = true
	s.materializeOrgID = orgID
	s.materializeRepoID = repositoryID
	s.materializeChecks = append([]models.PRReadinessCustomCheck(nil), checks...)
	return nil
}

func (s *testInternalPRReadinessStore) ResolvePolicy(context.Context, uuid.UUID, *uuid.UUID) (models.PRReadinessResolvedPolicy, error) {
	return models.PRReadinessResolvedPolicy{Config: models.DefaultPRReadinessPolicyConfig(), Source: "default"}, nil
}

func (s *testInternalPRReadinessStore) GetLatestBySession(context.Context, uuid.UUID, uuid.UUID) (*models.PRReadinessRun, error) {
	return nil, pgx.ErrNoRows
}

func (s *testInternalPRReadinessStore) CreateRun(context.Context, *models.PRReadinessRun) error {
	return nil
}

type testInternalSandboxProvider struct {
	execExit   int
	execErr    error
	execStderr string
	execCalls  []string
	execFn     func(cmd string, stdout, stderr io.Writer) (int, error)
	readFiles  map[string][]byte
	readErr    error
	writes     map[string][]byte
}

type testInternalSessionThreadStore struct {
	thread         models.SessionThread
	updateStatuses []models.ThreadStatus
}

func (s *testInternalSessionThreadStore) GetByID(_ context.Context, _, threadID uuid.UUID) (models.SessionThread, error) {
	if s.thread.ID != threadID {
		return models.SessionThread{}, pgx.ErrNoRows
	}
	return s.thread, nil
}

func (s *testInternalSessionThreadStore) UpdateStatus(_ context.Context, _, _ uuid.UUID, status models.ThreadStatus) error {
	s.updateStatuses = append(s.updateStatuses, status)
	s.thread.Status = status
	return nil
}

func (s *testInternalSessionThreadStore) CompleteTurn(context.Context, uuid.UUID, uuid.UUID, int, string) error {
	return nil
}

func (s *testInternalSessionThreadStore) UpdateResult(context.Context, uuid.UUID, uuid.UUID, models.ThreadStatus, *models.SessionResult) error {
	return nil
}

func (s *testInternalSessionThreadStore) ClearPendingMessages(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *testInternalSessionThreadStore) ClaimNextQueuedForSession(context.Context, uuid.UUID, uuid.UUID, int) (models.SessionThread, error) {
	return models.SessionThread{}, pgx.ErrNoRows
}

func (p *testInternalSandboxProvider) Name() string { return "test" }

func (p *testInternalSandboxProvider) Create(context.Context, SandboxConfig) (*Sandbox, error) {
	return nil, nil
}

func (p *testInternalSandboxProvider) CloneRepo(context.Context, *Sandbox, string, string, string) error {
	return nil
}

func (p *testInternalSandboxProvider) Exec(_ context.Context, _ *Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	p.execCalls = append(p.execCalls, cmd)
	if p.execFn != nil {
		return p.execFn(cmd, stdout, stderr)
	}
	if p.execStderr != "" {
		_, _ = io.WriteString(stderr, p.execStderr)
	}
	return p.execExit, p.execErr
}

func (p *testInternalSandboxProvider) ReadFile(_ context.Context, _ *Sandbox, path string) ([]byte, error) {
	if p.readErr != nil {
		return nil, p.readErr
	}
	if data, ok := p.readFiles[path]; ok {
		return append([]byte(nil), data...), nil
	}
	if data, ok := p.writes[path]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, nil
}

func (p *testInternalSandboxProvider) WriteFile(_ context.Context, _ *Sandbox, path string, data []byte) error {
	if p.writes == nil {
		p.writes = make(map[string][]byte)
	}
	p.writes[path] = append([]byte(nil), data...)
	return nil
}

func TestWaitForSandboxWorkspaceReady(t *testing.T) {
	t.Parallel()

	type probeResult struct {
		exitCode int
		err      error
		stdout   string
		stderr   string
	}
	const (
		expectedBranch = "143/abc12345/code-review"
		expectedHead   = "8802cf3c2664073d13c7522489d674c64889282a"
	)
	tests := []struct {
		name           string
		results        []probeResult
		timeout        time.Duration
		cancelContext  bool
		cancelChecks   []error
		expectedErr    error
		expectedCalls  int
		expectedChecks int
	}{
		{
			name: "ready on first probe",
			results: []probeResult{{
				exitCode: 0,
				stdout:   expectedHead + "\n" + expectedBranch + "\n",
			}},
			timeout:       100 * time.Millisecond,
			expectedCalls: 1,
		},
		{
			name: "waits through empty and default branch states",
			results: []probeResult{
				{exitCode: 128, stderr: "fatal: current branch master has no commits"},
				{exitCode: 0, stdout: "1111111111111111111111111111111111111111\nmain\n"},
				{exitCode: 0, stdout: expectedHead + "\n" + expectedBranch + "\n"},
			},
			timeout:       100 * time.Millisecond,
			expectedCalls: 3,
		},
		{
			name: "stops before readiness when durable cancellation is observed",
			results: []probeResult{{
				exitCode: 128,
				stderr:   "fatal: current branch master has no commits",
			}},
			timeout:        100 * time.Millisecond,
			cancelChecks:   []error{nil, ErrThreadCancelledBeforeWorkspaceReady},
			expectedErr:    ErrThreadCancelledBeforeWorkspaceReady,
			expectedCalls:  1,
			expectedChecks: 2,
		},
		{
			name: "times out on a different pull request head",
			results: []probeResult{{
				exitCode: 0,
				stdout:   "2222222222222222222222222222222222222222\n" + expectedBranch + "\n",
			}},
			timeout:       5 * time.Millisecond,
			expectedErr:   ErrSandboxWorkspaceNotReady,
			expectedCalls: -1,
		},
		{
			name:          "honors cancellation before probing",
			results:       []probeResult{{exitCode: 0, stdout: expectedHead + "\n" + expectedBranch + "\n"}},
			timeout:       100 * time.Millisecond,
			cancelContext: true,
			expectedErr:   context.Canceled,
			expectedCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			attempt := 0
			provider := &testInternalSandboxProvider{execFn: func(_ string, stdout, stderr io.Writer) (int, error) {
				resultIndex := attempt
				if resultIndex >= len(tt.results) {
					resultIndex = len(tt.results) - 1
				}
				attempt++
				result := tt.results[resultIndex]
				_, _ = io.WriteString(stdout, result.stdout)
				_, _ = io.WriteString(stderr, result.stderr)
				return result.exitCode, result.err
			}}
			ctx := context.Background()
			if tt.cancelContext {
				cancelledCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelledCtx
			}
			sandbox := &Sandbox{ID: "shared-review", WorkDir: "/workspace/repo's checkout"}
			cancelCheckCalls := 0
			var checkCancellation func(context.Context) error
			if tt.cancelChecks != nil {
				checkCancellation = func(context.Context) error {
					index := cancelCheckCalls
					if index >= len(tt.cancelChecks) {
						index = len(tt.cancelChecks) - 1
					}
					cancelCheckCalls++
					return tt.cancelChecks[index]
				}
			}

			err := waitForSandboxWorkspaceReady(ctx, provider, sandbox, expectedBranch, expectedHead, tt.timeout, time.Millisecond, 4*time.Millisecond, checkCancellation)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "workspace readiness should return the expected transient or context error")
			} else {
				require.NoError(t, err, "workspace readiness should pass only after the authoritative branch and head are checked out")
			}
			if tt.expectedCalls >= 0 {
				require.Equal(t, tt.expectedCalls, len(provider.execCalls), "workspace readiness should execute the expected number of git probes")
			} else {
				require.NotEmpty(t, provider.execCalls, "workspace readiness timeout should perform at least one git probe")
			}
			if len(provider.execCalls) > 0 {
				require.Contains(t, provider.execCalls[0], "'/workspace/repo'\\''s checkout'", "workspace readiness should shell-quote the sandbox workdir")
			}
			require.Equal(t, tt.expectedChecks, cancelCheckCalls, "workspace readiness should check durable cancellation at the expected points")
		})
	}
}

func TestNextSandboxWorkspaceReadyBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		current  time.Duration
		maximum  time.Duration
		expected time.Duration
	}{
		{name: "doubles the initial delay", current: 100 * time.Millisecond, maximum: 2 * time.Second, expected: 200 * time.Millisecond},
		{name: "doubles below the cap", current: 400 * time.Millisecond, maximum: 2 * time.Second, expected: 800 * time.Millisecond},
		{name: "clamps instead of overshooting", current: 800 * time.Millisecond, maximum: time.Second, expected: time.Second},
		{name: "stays at the cap", current: 2 * time.Second, maximum: 2 * time.Second, expected: 2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, nextSandboxWorkspaceReadyBackoff(tt.current, tt.maximum), "workspace readiness backoff should double quickly and remain bounded")
		})
	}
}

func TestStopWorkspaceWaitIfThreadCancelled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		cancelRequested  bool
		status           models.ThreadStatus
		expectedErr      error
		expectedStatuses []models.ThreadStatus
	}{
		{name: "active thread continues waiting", status: models.ThreadStatusRunning},
		{name: "durable cancellation terminalizes thread", cancelRequested: true, status: models.ThreadStatusRunning, expectedErr: ErrThreadCancelledBeforeWorkspaceReady, expectedStatuses: []models.ThreadStatus{models.ThreadStatusCancelled}},
		{name: "already cancelled thread stops without another write", status: models.ThreadStatusCancelled, expectedErr: ErrThreadCancelledBeforeWorkspaceReady},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			threadID := uuid.New()
			thread := models.SessionThread{ID: threadID, Status: tt.status}
			if tt.cancelRequested {
				requestedAt := time.Now()
				thread.CancelRequestedAt = &requestedAt
			}
			threadStore := &testInternalSessionThreadStore{thread: thread}
			orchestrator := &Orchestrator{sessionThreads: threadStore}

			err := orchestrator.stopWorkspaceWaitIfThreadCancelled(context.Background(), uuid.New(), threadID)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "workspace wait should stop when durable thread cancellation is present")
			} else {
				require.NoError(t, err, "workspace wait should continue for an active uncancelled thread")
			}
			require.Equal(t, tt.expectedStatuses, threadStore.updateStatuses, "workspace wait cancellation should persist the expected terminal status updates")
		})
	}
}

func TestOrchestratorMaterializeChangeset(t *testing.T) {
	t.Parallel()
	containerID := "sandbox-1"
	patch := "diff --git a/api.go b/api.go\n+code\n"
	provider := &testInternalSandboxProvider{}
	provider.execFn = func(cmd string, stdout, stderr io.Writer) (int, error) {
		switch {
		case strings.HasPrefix(cmd, "df -Pk"):
			_, _ = io.WriteString(stdout, "1048576\n")
		case strings.Contains(cmd, "worktree add"):
			_, _ = io.WriteString(stdout, "abc123\n")
		case strings.Contains(cmd, "rev-parse HEAD") && strings.Contains(cmd, "diff --binary"):
			_, _ = io.WriteString(stdout, "abc123\n"+patch)
		}
		return 0, nil
	}
	orchestrator := &Orchestrator{provider: provider, logger: zerolog.Nop()}
	result, err := orchestrator.MaterializeChangeset(context.Background(), &models.Session{ID: uuid.New(), ContainerID: &containerID}, models.SessionChangeset{
		ID: uuid.New(), OrderIndex: 1, Title: "API integration", TargetBranch: "main",
	}, patch)
	require.NoError(t, err, "materialization should create an independent worktree and apply its assigned source patch")
	require.Equal(t, "abc123", result.HeadSHA, "materialization should capture the worktree head")
	require.Equal(t, patch, result.Diff, "materialization should persist the actual worktree diff for verification")
	require.Contains(t, result.WorkingBranch, "2-api-integration", "working branch should be stable and reviewable")
	require.Contains(t, string(provider.writes[result.WorktreePath+"/.143-split.patch"]), "+code", "assigned patch should be written into the target worktree")
}

func TestOrchestratorMaterializeChangesetRejectsInsufficientDisk(t *testing.T) {
	t.Parallel()
	containerID := "sandbox-1"
	provider := &testInternalSandboxProvider{execFn: func(cmd string, stdout, stderr io.Writer) (int, error) {
		_, _ = io.WriteString(stdout, "1024\n")
		return 0, nil
	}}
	orchestrator := &Orchestrator{provider: provider, logger: zerolog.Nop()}
	_, err := orchestrator.MaterializeChangeset(context.Background(), &models.Session{ID: uuid.New(), ContainerID: &containerID}, models.SessionChangeset{ID: uuid.New(), Title: "API", TargetBranch: "main"}, "")
	require.ErrorIs(t, err, ErrChangesetDiskBudget, "materialization should fail clearly before git worktree creation when disk headroom is insufficient")
	require.Len(t, provider.execCalls, 1, "disk budget failure should not mutate git state")
}

func (p *testInternalSandboxProvider) Destroy(context.Context, *Sandbox) error {
	return nil
}

func (p *testInternalSandboxProvider) IsAlive(context.Context, *Sandbox) (bool, error) {
	return true, nil
}

func (p *testInternalSandboxProvider) ConnectionInfo(context.Context, *Sandbox) (*SandboxConnectionInfo, error) {
	return nil, nil
}

func (p *testInternalSandboxProvider) Snapshot(context.Context, *Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

func (p *testInternalSandboxProvider) Restore(context.Context, *Sandbox, io.Reader) error {
	return nil
}

func (p *testInternalSandboxProvider) ExecStream(context.Context, *Sandbox, string, func([]byte), io.Writer) (int, error) {
	return 0, nil
}

type testInternalCodingCredentialProvider struct {
	resolvable map[models.ProviderName][]models.DecryptedCodingCredential
}

func (p testInternalCodingCredentialProvider) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	return p.resolvable[provider], nil
}

type testInternalQueuedCodingCredentialProvider struct {
	resolvable map[models.ProviderName][]models.DecryptedCodingCredential
	picks      []models.DecryptedCodingCredential
}

func (p *testInternalQueuedCodingCredentialProvider) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	return p.resolvable[provider], nil
}

func (p *testInternalQueuedCodingCredentialProvider) PickRunnableMulti(_ context.Context, _ models.Scope, _ []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	if len(p.picks) == 0 {
		return nil, errEnvCodingCredentialNotFound
	}
	picked := p.picks[0]
	p.picks = p.picks[1:]
	return &picked, nil
}

type testInternalClaudeCodeAuthProvider struct {
	sub         *models.AnthropicSubscription
	id          uuid.UUID
	storedScope models.Scope
	storedID    uuid.UUID
	storedSub   *models.AnthropicSubscription
	storeErr    error
}

func (p testInternalClaudeCodeAuthProvider) HasActiveSubscription(context.Context, uuid.UUID) (bool, error) {
	return p.sub != nil, nil
}

func (p testInternalClaudeCodeAuthProvider) GetValidToken(context.Context, uuid.UUID) (*models.AnthropicSubscription, *uuid.UUID, error) {
	if p.sub == nil {
		return nil, nil, nil
	}
	id := p.id
	return p.sub, &id, nil
}

func (p *testInternalClaudeCodeAuthProvider) StoreTokenByID(_ context.Context, scope models.Scope, credID uuid.UUID, sub models.AnthropicSubscription) (bool, error) {
	if p.storeErr != nil {
		return false, p.storeErr
	}
	p.storedScope = scope
	p.storedID = credID
	p.storedSub = &sub
	return true, nil
}

type testInternalOrgStore struct {
	org models.Organization
	err error
}

func (s testInternalOrgStore) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	if s.err != nil {
		return models.Organization{}, s.err
	}
	return s.org, nil
}

func TestSetupFreshSandbox_CodexAPIKeyUsesResolvedEnv(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	userID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	credID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: testInternalCodingCredentialProvider{
			resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderOpenAI: {
					{
						ID:       credID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderOpenAI,
						Priority: 1,
						Status:   models.CodingCredentialStatusActive,
						Config:   models.OpenAIConfig{APIKey: "sk-openai"},
					},
				},
			},
		},
		Provider: provider,
		Logger:   zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
	}
	session := &models.Session{
		ID:                uuid.MustParse("44444444-4444-4444-4444-444444444444"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeCodex,
		TriggeredByUserID: &userID,
	}

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{
		"OPENAI_API_KEY": "sk-openai",
	}, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should accept the already-resolved Codex API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.codex/auth.json", "setupFreshSandbox should not require Codex auth.json when the selected unified credential is an API key")
}

func TestSetupFreshSandbox_CodexOrgAPIKeyFallbackUsesResolvedEnv(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("10101010-1111-2222-3333-444444444444")
	userID := uuid.MustParse("55555555-6666-7777-8888-999999999999")
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: testInternalCodingCredentialProvider{
			resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderOpenAI: {
					{
						ID:       uuid.New(),
						OrgID:    orgID,
						Provider: models.ProviderOpenAI,
						Priority: 1,
						Status:   models.CodingCredentialStatusActive,
						Config:   models.OpenAIConfig{APIKey: "sk-org-openai"},
					},
				},
			},
		},
		Provider: provider,
		Logger:   zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
	}
	session := &models.Session{
		ID:                uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeCodex,
		TriggeredByUserID: &userID,
	}
	envVars := env.Resolve(context.Background(), orgID, models.AgentTypeCodex, &userID)

	_, _, billingMode, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-legacy", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, envVars, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should honor the org-scoped OpenAI API-key fallback")
	require.Equal(t, TokenBillingModeAPIKey, billingMode, "setupFreshSandbox should classify the org OpenAI credential as an API-key billing mode")
	require.NotContains(t, provider.writes, "/home/sandbox/.codex/auth.json", "setupFreshSandbox should not require Codex auth.json when the resolved unified credential is an API key")
}

func TestBuildTokenUsageHint_PreservesExplicitClaudeSubscriptionBillingMode(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	userID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	orch := &Orchestrator{logger: zerolog.Nop()}

	actual := orch.buildTokenUsageHint(context.Background(), models.AgentTypeClaudeCode, orgID, &userID, map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-fallback",
		"ANTHROPIC_MODEL":   models.ClaudeCodeModelSonnet46,
	}, TokenUsageHint{
		AgentType:      models.AgentTypeClaudeCode,
		EffectiveModel: models.ClaudeCodeModelSonnet46,
		BillingMode:    TokenBillingModeSubscription,
	})

	require.Equal(t, TokenBillingModeSubscription, actual.BillingMode, "explicit billing mode from the auth path should not be overwritten by the fallback Anthropic API key env var")
	require.Equal(t, models.ClaudeCodeModelSonnet46, actual.EffectiveModel, "buildTokenUsageHint should retain the effective model")
}

func TestBuildTokenUsageHint_UsesAgentConfigModelDefaultsWhenEnvOmitsThem(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	userID := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	orgSettings := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_MODEL":"gpt-5.4"},"claude_code":{"ANTHROPIC_MODEL":"claude-sonnet-4-6"},"opencode":{"OPENCODE_MODEL":"google/gemini-3-flash"}}}`)
	env := NewAgentEnv(AgentEnvDeps{
		Orgs: testInternalOrgStore{
			org: models.Organization{
				ID:       orgID,
				Settings: orgSettings,
			},
		},
		Logger: zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:    env,
		logger: zerolog.Nop(),
	}

	tests := []struct {
		name      string
		agentType models.AgentType
		expected  string
	}{
		{name: "codex", agentType: models.AgentTypeCodex, expected: models.CodexModelGPT54},
		{name: "claude", agentType: models.AgentTypeClaudeCode, expected: models.ClaudeCodeModelSonnet46},
		{name: "opencode", agentType: models.AgentTypeOpenCode, expected: models.OpenCodeModelGemini3Flash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := orch.buildTokenUsageHint(context.Background(), tt.agentType, orgID, &userID, map[string]string{}, TokenUsageHint{AgentType: tt.agentType})
			require.Equal(t, tt.expected, actual.EffectiveModel, "buildTokenUsageHint should recover the agent_config model default when env injection is intentionally skipped")
		})
	}
}

func TestSetupFreshSandbox_CodexAPIKeyDoesNotRePickSubscriptionAtSamePriority(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("01010101-0101-0101-0101-010101010101")
	userID := uuid.MustParse("02020202-0202-0202-0202-020202020202")
	apiKeyID := uuid.MustParse("03030303-0303-0303-0303-030303030303")
	subID := uuid.MustParse("04040404-0404-0404-0404-040404040404")
	apiKeyRow := models.DecryptedCodingCredential{
		ID:       apiKeyID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderOpenAI,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config:   models.OpenAIConfig{APIKey: "sk-openai-api-key", APIType: "responses"},
	}
	subRow := models.DecryptedCodingCredential{
		ID:       subID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderOpenAISubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.OpenAISubscriptionConfig{
			AccessToken:  "same-priority-codex-access",
			RefreshToken: "same-priority-codex-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
			AccountType:  "plus",
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderOpenAI:             {apiKeyRow},
			models.ProviderOpenAISubscription: {subRow},
		},
		picks: []models.DecryptedCodingCredential{apiKeyRow, subRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
	}
	session := &models.Session{
		ID:                uuid.MustParse("05050505-0505-0505-0505-050505050505"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeCodex,
		TriggeredByUserID: &userID,
	}
	envVars := env.Resolve(context.Background(), orgID, models.AgentTypeCodex, &userID)

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, envVars, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should use the already-resolved Codex API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.codex/auth.json", "setupFreshSandbox should not re-pick a same-priority Codex subscription after env resolution selected an API key")
}

func TestSetupFreshSandbox_ClaudeAPIKeyDoesNotInjectLowerPrioritySubscription(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	userID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	apiKeyID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	subID := uuid.MustParse("88888888-8888-8888-8888-888888888888")
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: testInternalCodingCredentialProvider{
			resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderAnthropic: {
					{
						ID:       apiKeyID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderAnthropic,
						Priority: 1,
						Status:   models.CodingCredentialStatusActive,
						Config:   models.AnthropicConfig{APIKey: "sk-ant-api-key"},
					},
				},
				models.ProviderAnthropicSubscription: {
					{
						ID:       subID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderAnthropicSubscription,
						Priority: 2,
						Status:   models.CodingCredentialStatusActive,
						Config: models.AnthropicSubscriptionConfig{
							AccessToken:  "lower-priority-access",
							RefreshToken: "lower-priority-refresh",
							ExpiresAt:    time.Now().Add(time.Hour),
						},
					},
				},
			},
		},
		Provider: provider,
		Logger:   zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: subID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "lower-priority-access",
				RefreshToken: "lower-priority-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("99999999-9999-9999-9999-999999999999"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-api-key",
	}, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should accept the already-resolved Claude API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.claude/.credentials.json", "setupFreshSandbox should not inject a lower-priority Claude subscription over the selected API key")
}

func TestSetupFreshSandbox_ClaudeAPIKeyDoesNotRePickSubscriptionAtSamePriority(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("12121212-1212-1212-1212-121212121212")
	userID := uuid.MustParse("23232323-2323-2323-2323-232323232323")
	apiKeyID := uuid.MustParse("34343434-3434-3434-3434-343434343434")
	subID := uuid.MustParse("45454545-4545-4545-4545-454545454545")
	apiKeyRow := models.DecryptedCodingCredential{
		ID:       apiKeyID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropic,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config:   models.AnthropicConfig{APIKey: "sk-ant-api-key"},
	}
	subRow := models.DecryptedCodingCredential{
		ID:       subID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "same-priority-access",
			RefreshToken: "same-priority-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropic:             {apiKeyRow},
			models.ProviderAnthropicSubscription: {subRow},
		},
		picks: []models.DecryptedCodingCredential{apiKeyRow, subRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: subID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "same-priority-access",
				RefreshToken: "same-priority-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("56565656-5656-5656-5656-565656565656"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}
	envVars := env.Resolve(context.Background(), orgID, models.AgentTypeClaudeCode, &userID)

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, envVars, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should use the already-resolved Claude API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.claude/.credentials.json", "setupFreshSandbox should not re-pick a same-priority subscription after env resolution selected an API key")
}

func TestSetupFreshSandbox_ClaudeSubscriptionUsesUnifiedPickedToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("67676767-6767-6767-6767-676767676767")
	userID := uuid.MustParse("78787878-7878-7878-7878-787878787878")
	unifiedID := uuid.MustParse("89898989-8989-8989-8989-898989898989")
	legacyID := uuid.MustParse("90909090-9090-9090-9090-909090909090")
	unifiedRow := models.DecryptedCodingCredential{
		ID:       unifiedID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:   "unified-access",
			RefreshToken:  "unified-refresh",
			ExpiresAt:     time.Now().Add(time.Hour),
			AccountType:   "claude_pro",
			RateLimitTier: "default",
			Scopes:        []string{"user:inference"},
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropicSubscription: {unifiedRow},
		},
		picks: []models.DecryptedCodingCredential{unifiedRow, unifiedRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: legacyID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "legacy-access",
				RefreshToken: "legacy-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("abababab-abab-abab-abab-abababababab"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{}, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should inject the selected unified Claude subscription")
	written := provider.writes["/home/sandbox/.claude/.credentials.json"]
	require.Contains(t, string(written), "unified-access", "Claude credentials file should use the unified resolver's selected subscription")
	require.NotContains(t, string(written), "legacy-access", "Claude credentials file should not fall back to the legacy org-wide subscription when unified selected a row")
}

func TestSetupFreshSandbox_ReturnsResolvedAuthBillingMode(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1")
	userID := uuid.MustParse("b2b2b2b2-b2b2-b2b2-b2b2-b2b2b2b2b2b2")
	unifiedID := uuid.MustParse("c3c3c3c3-c3c3-c3c3-c3c3-c3c3c3c3c3c3")
	unifiedRow := models.DecryptedCodingCredential{
		ID:       unifiedID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "fresh-auth-access",
			RefreshToken: "fresh-auth-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropicSubscription: {unifiedRow},
		},
		picks: []models.DecryptedCodingCredential{unifiedRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: unifiedID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "fresh-auth-access",
				RefreshToken: "fresh-auth-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("d4d4d4d4-d4d4-d4d4-d4d4-d4d4d4d4d4d4"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}

	_, _, billingMode, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{}, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should succeed for a fresh Claude subscription run")
	require.Equal(t, TokenBillingModeSubscription, billingMode, "setupFreshSandbox should return the auth-selected billing mode for fresh runs")
}

func TestHarvestClaudeCodeCredentials_StoresSandboxRefreshedToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("19191919-1919-1919-1919-191919191919")
	userID := uuid.MustParse("29292929-2929-2929-2929-292929292929")
	credID := uuid.MustParse("39393939-3939-3939-3939-393939393939")
	expiresAt := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond)
	credsPath := "/home/sandbox/.claude/.credentials.json"
	credsJSON, err := json.Marshal(map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken":      "refreshed-access",
			"refreshToken":     "refreshed-refresh",
			"expiresAt":        expiresAt.UnixMilli(),
			"scopes":           []string{"user:profile", "user:inference", "user:sessions:claude_code"},
			"subscriptionType": "claude_max",
			"rateLimitTier":    "default_claude_max_20x",
		},
	})
	require.NoError(t, err, "test credentials JSON should marshal")

	provider := &testInternalSandboxProvider{readFiles: map[string][]byte{credsPath: credsJSON}}
	auth := &testInternalClaudeCodeAuthProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		Provider: provider,
		Logger:   zerolog.Nop(),
	})
	picked := models.DecryptedCodingCredential{
		ID:       credID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	env.recordCredentialPick(orgID, &userID, models.ProviderAnthropic, picked)
	orch := &Orchestrator{
		env:            env,
		provider:       provider,
		logger:         zerolog.Nop(),
		claudeCodeAuth: auth,
	}
	session := &models.Session{
		ID:                uuid.MustParse("49494949-4949-4949-4949-494949494949"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}

	stored, err := orch.harvestClaudeCodeCredentials(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox"}, TokenBillingModeSubscription, zerolog.Nop())

	require.NoError(t, err, "harvesting refreshed Claude credentials should not fail")
	require.True(t, stored, "harvesting should report that refreshed credentials were persisted")
	require.Equal(t, models.Scope{OrgID: orgID, UserID: &userID}, auth.storedScope, "harvesting should store against the selected credential scope")
	require.Equal(t, credID, auth.storedID, "harvesting should store against the selected credential id")
	require.NotNil(t, auth.storedSub, "harvesting should persist the refreshed subscription")
	require.Equal(t, "refreshed-access", auth.storedSub.AccessToken, "harvesting should persist the refreshed access token")
	require.Equal(t, "refreshed-refresh", auth.storedSub.RefreshToken, "harvesting should persist the refreshed refresh token")
	require.Equal(t, expiresAt, auth.storedSub.ExpiresAt, "harvesting should persist the refreshed expiration")
	require.Equal(t, "claude_max", auth.storedSub.AccountType, "harvesting should persist the refreshed account type")
	require.Equal(t, "default_claude_max_20x", auth.storedSub.RateLimitTier, "harvesting should persist the refreshed rate limit tier")
	require.Equal(t, []string{"user:profile", "user:inference", "user:sessions:claude_code"}, auth.storedSub.Scopes, "harvesting should persist refreshed scopes")
}

func TestHarvestClaudeCodeCredentials_RejectsAbsurdFutureExpiration(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("59595959-5959-5959-5959-595959595959")
	userID := uuid.MustParse("69696969-6969-6969-6969-696969696969")
	credID := uuid.MustParse("79797979-7979-7979-7979-797979797979")
	credsPath := "/home/sandbox/.claude/.credentials.json"
	credsJSON, err := json.Marshal(map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken":  "fake-access",
			"refreshToken": "fake-refresh",
			"expiresAt":    time.Now().Add(10 * 365 * 24 * time.Hour).UnixMilli(),
		},
	})
	require.NoError(t, err, "test credentials JSON should marshal")

	provider := &testInternalSandboxProvider{readFiles: map[string][]byte{credsPath: credsJSON}}
	auth := &testInternalClaudeCodeAuthProvider{}
	env := NewAgentEnv(AgentEnvDeps{Provider: provider, Logger: zerolog.Nop()})
	env.recordCredentialPick(orgID, &userID, models.ProviderAnthropic, models.DecryptedCodingCredential{
		ID:       credID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	})
	orch := &Orchestrator{env: env, provider: provider, logger: zerolog.Nop(), claudeCodeAuth: auth}
	session := &models.Session{ID: uuid.New(), OrgID: orgID, AgentType: models.AgentTypeClaudeCode, TriggeredByUserID: &userID}

	stored, err := orch.harvestClaudeCodeCredentials(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox"}, TokenBillingModeSubscription, zerolog.Nop())

	require.Error(t, err, "harvesting should reject implausibly long-lived sandbox credentials")
	require.False(t, stored, "harvesting should not persist implausible credentials")
	require.Nil(t, auth.storedSub, "harvesting should not store rejected credentials")
}

func TestHarvestClaudeCodeCredentials_LegacyInjectedUnchangedTokenNoOps(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("89898989-8989-8989-8989-898989898989")
	userID := uuid.MustParse("99999999-9999-9999-9999-999999999999")
	credID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	sub := &models.AnthropicSubscription{
		AccessToken:   "legacy-access",
		RefreshToken:  "legacy-refresh",
		ExpiresAt:     time.Now().Add(time.Hour).Truncate(time.Millisecond),
		AccountType:   "claude_pro",
		RateLimitTier: "default",
		Scopes:        []string{"user:profile", "user:inference"},
	}
	provider := &testInternalSandboxProvider{}
	auth := &testInternalClaudeCodeAuthProvider{sub: sub, id: credID}
	env := NewAgentEnv(AgentEnvDeps{Provider: provider, Logger: zerolog.Nop()})
	orch := &Orchestrator{env: env, provider: provider, logger: zerolog.Nop(), claudeCodeAuth: auth}
	sandbox := &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox"}

	injected, _, err := orch.injectClaudeCodeAuth(context.Background(), orgID, &userID, sandbox)
	require.NoError(t, err, "legacy Claude auth injection should succeed")
	require.True(t, injected, "legacy Claude auth injection should write sandbox credentials")

	session := &models.Session{ID: uuid.New(), OrgID: orgID, AgentType: models.AgentTypeClaudeCode, TriggeredByUserID: &userID}
	stored, err := orch.harvestClaudeCodeCredentials(context.Background(), session, sandbox, TokenBillingModeSubscription, zerolog.Nop())

	require.NoError(t, err, "harvesting unchanged legacy credentials should not fail")
	require.False(t, stored, "harvesting unchanged legacy credentials should no-op")
	require.Nil(t, auth.storedSub, "harvesting should not store unchanged legacy credentials")
}

func TestEnsureClaudeCodeAuth_SetupTokenInjectsEnvAndSkipsHarvest(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("babababa-baba-baba-baba-babababababa")
	userID := uuid.MustParse("cacacaca-caca-caca-caca-cacacacacaca")
	credID := uuid.MustParse("dadadada-dada-dada-dada-dadadadadada")
	sandbox := &Sandbox{ID: "sandbox-setup-token", HomeDir: "/home/sandbox"}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: &envCodingCredentialProvider{},
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	picked := models.DecryptedCodingCredential{
		ID:       credID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AuthMode:            models.AnthropicSubscriptionAuthModeSetupToken,
			OAuthToken:          "claude-setup-token",
			OAuthTokenExpiresAt: time.Now().Add(365 * 24 * time.Hour),
			AccountType:         "claude_max",
		},
	}
	env.recordCredentialPick(orgID, &userID, models.ProviderAnthropic, picked)
	orch := &Orchestrator{
		env:            env,
		provider:       provider,
		logger:         zerolog.Nop(),
		claudeCodeAuth: &testInternalClaudeCodeAuthProvider{},
	}
	run := &models.Session{
		ID:                uuid.MustParse("eaeaeaea-eaea-eaea-eaea-eaeaeaeaeaea"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}
	envVars := map[string]string{
		"ANTHROPIC_API_KEY":     "sk-ant-fallback",
		"ANTHROPIC_AUTH_TOKEN":  "anthropic-auth-token",
		"ANTHROPIC_MODEL":       models.ClaudeCodeModelSonnet46,
		"UNRELATED_ENV":         "keep-me",
		"CLAUDE_CODE_MAX_THINK": "1",
	}

	billingMode, err := orch.ensureClaudeCodeAuth(context.Background(), run, nil, sandbox, envVars)

	require.NoError(t, err, "setup-token auth should prepare Claude Code without refreshing")
	require.Equal(t, TokenBillingModeSubscription, billingMode, "setup-token auth should use subscription billing mode")
	require.Equal(t, "claude-setup-token", envVars["CLAUDE_CODE_OAUTH_TOKEN"], "setup-token auth should inject the Claude Code OAuth token env var")
	require.NotContains(t, envVars, "ANTHROPIC_API_KEY", "setup-token auth should clear higher-precedence Anthropic API-key auth")
	require.NotContains(t, envVars, "ANTHROPIC_AUTH_TOKEN", "setup-token auth should clear higher-precedence Anthropic auth token")
	require.Equal(t, "keep-me", envVars["UNRELATED_ENV"], "setup-token auth should leave unrelated env vars untouched")
	require.Contains(t, provider.execCalls, "rm -f '/home/sandbox/.claude/.credentials.json'", "setup-token auth should remove stale Claude credentials")

	stored, harvestErr := orch.harvestClaudeCodeCredentials(context.Background(), run, sandbox, TokenBillingModeSubscription, zerolog.Nop())

	require.NoError(t, harvestErr, "setup-token credentials should skip sandbox credential harvest")
	require.False(t, stored, "setup-token credentials should not persist harvested rotating tokens")
}

func TestCreateAssistantMessage_CarriesThreadID(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	threadID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	logs := &testInternalSessionLogStore{}
	messages := &testInternalSessionMessageStore{}
	orch := &Orchestrator{
		agentRunLogs:    logs,
		sessionMessages: messages,
		logger:          zerolog.Nop(),
	}

	err := orch.createAssistantMessage(context.Background(), sessionID, orgID, &threadID, 4, &AgentResult{
		Summary: "Final answer",
	})
	require.NoError(t, err, "createAssistantMessage should persist the assistant transcript")
	require.Len(t, messages.messages, 1, "assistant message should be created")
	require.NotNil(t, messages.messages[0].ThreadID, "assistant message should preserve the thread id")
	require.Equal(t, threadID, *messages.messages[0].ThreadID, "assistant message should use the provided thread id")
	require.NotNil(t, logs.markedThreadID, "duplicate marker should preserve the thread id")
	require.Equal(t, threadID, *logs.markedThreadID, "duplicate marker should use the provided thread id")
	require.Equal(t, 4, logs.markedTurnNumber, "duplicate marker should use the provided turn number")
	require.Equal(t, "Final answer", logs.markedMessage, "duplicate marker should target the assistant summary")
}

func TestCreateAssistantMessage_PersistsCacheOnlyAndNativeCostUsage(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	sessionID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	messages := &testInternalSessionMessageStore{}
	orch := &Orchestrator{
		sessionMessages: messages,
		logger:          zerolog.Nop(),
	}

	err := orch.createAssistantMessage(context.Background(), sessionID, orgID, nil, 2, &AgentResult{
		Summary: "Cached reply",
		TokenUsage: TokenUsage{
			CachedInputTokens:   123,
			CacheCreationTokens: 45,
			NativeCost: &TokenCost{
				Amount: 12.5,
				Unit:   TokenCostUnitCredits,
				Source: TokenCostSourceDerived,
			},
		},
	})

	require.NoError(t, err, "createAssistantMessage should persist assistant messages with cache-only/native-cost token usage")
	require.Len(t, messages.messages, 1, "assistant message should be created")
	require.NotNil(t, messages.messages[0].TokenUsage, "cache-only/native-cost usage should still be persisted on the assistant message")
}

func TestCreateAssistantMessage_DoesNotPersistUnavailableTokenUsage(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("abababab-abab-abab-abab-abababababab")
	sessionID := uuid.MustParse("cdcdcdcd-cdcd-cdcd-cdcd-cdcdcdcdcdcd")
	messages := &testInternalSessionMessageStore{}
	orch := &Orchestrator{
		sessionMessages: messages,
		logger:          zerolog.Nop(),
	}

	err := orch.createAssistantMessage(context.Background(), sessionID, orgID, nil, 3, &AgentResult{
		Summary: "No usage reported",
		TokenUsage: FinalizeTokenUsage(TokenUsage{}, TokenUsageHint{
			AgentType:      models.AgentTypeCodex,
			EffectiveModel: models.CodexModelGPT54,
			BillingMode:    TokenBillingModeSubscription,
		}),
	})

	require.NoError(t, err, "createAssistantMessage should not fail when token usage is unavailable")
	require.Len(t, messages.messages, 1, "assistant message should be created")
	require.Nil(t, messages.messages[0].TokenUsage, "assistant message should leave token usage nil when the provider reported no token payload")
}

func TestBuildRunResult_DoesNotPersistUnavailableTokenUsage(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("12121212-3434-5656-7878-909090909090")
	runID := uuid.MustParse("abababab-cdcd-efef-0101-121212121212")
	orch := &Orchestrator{
		logger: zerolog.Nop(),
	}
	run := &models.Session{
		ID:    runID,
		OrgID: orgID,
	}

	result := orch.buildRunResult(context.Background(), run, nil, &AgentResult{
		Summary: "No usage reported",
		TokenUsage: FinalizeTokenUsage(TokenUsage{}, TokenUsageHint{
			AgentType:      models.AgentTypeCodex,
			EffectiveModel: models.CodexModelGPT54,
			BillingMode:    TokenBillingModeSubscription,
		}),
	})

	require.Nil(t, result.TokenUsage, "buildRunResult should leave token usage nil when the provider reported no token payload")
}

func TestBuildRunResult_CapturesReviewArtifact(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("12121212-3434-5656-7878-909090909090")
	runID := uuid.MustParse("abababab-cdcd-efef-0101-121212121212")
	baseSHA := "base123"
	diff := strings.Join([]string{
		"diff --git a/src/app.ts b/src/app.ts",
		"--- a/src/app.ts",
		"+++ b/src/app.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n")
	store := &internalReviewArtifactStore{saved: map[string][]byte{}}
	provider := &testInternalSandboxProvider{
		execFn: func(cmd string, stdout, stderr io.Writer) (int, error) {
			switch {
			case cmd == "git rev-parse HEAD":
				_, _ = io.WriteString(stdout, "head123\n")
				return 0, nil
			case strings.HasPrefix(cmd, "git status --porcelain"):
				return 0, nil
			case strings.Contains(cmd, "src/app.ts"):
				_, _ = io.WriteString(stdout, "export const value = 1\n")
				return 0, nil
			default:
				_, _ = io.WriteString(stderr, "unexpected command")
				return 1, nil
			}
		},
	}
	orch := &Orchestrator{
		provider:  provider,
		snapshots: store,
		logger:    zerolog.Nop(),
	}
	run := &models.Session{ID: runID, OrgID: orgID, BaseCommitSHA: &baseSHA}

	result := orch.buildRunResult(context.Background(), run, &Sandbox{WorkDir: "/workspace"}, &AgentResult{Diff: diff, Summary: "done"})

	require.NotNil(t, result.ReviewArtifactKey, "buildRunResult should attach a review artifact key")
	require.Contains(t, *result.ReviewArtifactKey, "review-artifacts/"+orgID.String()+"/"+runID.String()+"/", "artifact key should be scoped by org and session")
	require.Equal(t, 1, result.ReviewArtifactFileCount, "buildRunResult should record stored file count")
	require.Len(t, store.saved, 1, "buildRunResult should upload one artifact")
}

func TestBuildRunResult_ReviewArtifactUploadFailureIsNonFatal(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	baseSHA := "base123"
	diff := "diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1 +1 @@\n-old\n+new\n"
	store := &internalReviewArtifactStore{saveErr: errors.New("upload failed")}
	provider := &testInternalSandboxProvider{
		execFn: func(cmd string, stdout, stderr io.Writer) (int, error) {
			switch {
			case cmd == "git rev-parse HEAD":
				_, _ = io.WriteString(stdout, "head123\n")
				return 0, nil
			case strings.HasPrefix(cmd, "git status --porcelain"):
				return 0, nil
			case strings.Contains(cmd, "src/app.ts"):
				_, _ = io.WriteString(stdout, "export const value = 1\n")
				return 0, nil
			default:
				return 0, nil
			}
		},
	}
	orch := &Orchestrator{provider: provider, snapshots: store, logger: zerolog.Nop()}
	run := &models.Session{ID: runID, OrgID: orgID, BaseCommitSHA: &baseSHA}

	result := orch.buildRunResult(context.Background(), run, &Sandbox{WorkDir: "/workspace"}, &AgentResult{Diff: diff, Summary: "done"})

	require.NotNil(t, result.Diff, "buildRunResult should still return the session diff")
	require.Nil(t, result.ReviewArtifactKey, "buildRunResult should omit artifact metadata when upload fails")
}

func TestCleanupReviewArtifactDeletesUploadedArtifact(t *testing.T) {
	t.Parallel()

	key := "review-artifacts/org/session/artifact.json.gz"
	store := &internalReviewArtifactStore{saved: map[string][]byte{key: []byte("payload")}}
	orch := &Orchestrator{snapshots: store, logger: zerolog.Nop()}

	orch.cleanupReviewArtifact(context.Background(), &models.SessionResult{ReviewArtifactKey: &key}, zerolog.Nop())

	require.Equal(t, []string{key}, store.deleted, "cleanupReviewArtifact should delete the uploaded artifact")
	require.Empty(t, store.saved, "cleanupReviewArtifact should remove the stored payload")
}

type internalReviewArtifactStore struct {
	saved   map[string][]byte
	deleted []string
	saveErr error
}

func (s *internalReviewArtifactStore) Save(_ context.Context, key string, reader io.Reader) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.saved == nil {
		s.saved = map[string][]byte{}
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.saved[key] = body
	return nil
}

func (s *internalReviewArtifactStore) Load(_ context.Context, key string, writer io.Writer) error {
	_, err := writer.Write(s.saved[key])
	return err
}

func (s *internalReviewArtifactStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	delete(s.saved, key)
	return nil
}

func TestStreamLogs_CarriesThreadID(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	threadID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	logs := &testInternalSessionLogStore{}
	orch := &Orchestrator{
		agentRunLogs: logs,
		logger:       zerolog.Nop(),
	}

	logCh := make(chan LogEntry, 1)
	logCh <- LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   "streamed message",
	}
	close(logCh)

	orch.streamLogs(context.Background(), sessionID, orgID, models.AgentTypeClaudeCode, &threadID, 2, logCh, nil)

	require.Len(t, logs.logs, 1, "streamLogs should persist the log entry")
	require.NotNil(t, logs.logs[0].ThreadID, "persisted log should preserve the thread id")
	require.Equal(t, threadID, *logs.logs[0].ThreadID, "persisted log should use the provided thread id")
	require.Equal(t, 2, logs.logs[0].TurnNumber, "persisted log should keep the turn number")
	require.Equal(t, "streamed message", logs.logs[0].Message, "persisted log should keep the message content")
	require.Nil(t, logs.logs[0].Metadata, "persisted log should leave absent metadata as SQL null")
}

func TestStreamLogs_PersistsMetadataAsJSON(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	logs := &testInternalSessionLogStore{}
	orch := &Orchestrator{
		agentRunLogs: logs,
		logger:       zerolog.Nop(),
	}

	logCh := make(chan LogEntry, 1)
	logCh <- LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   "with metadata",
		Metadata:  map[string]interface{}{"step": "two"},
	}
	close(logCh)

	orch.streamLogs(context.Background(), sessionID, orgID, models.AgentTypeClaudeCode, nil, 1, logCh, nil)

	require.Len(t, logs.logs, 1, "streamLogs should persist the log entry")
	require.NotNil(t, logs.logs[0].Metadata, "non-nil metadata should be marshaled and persisted")
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(logs.logs[0].Metadata, &decoded), "persisted metadata should be valid JSON")
	require.Equal(t, "two", decoded["step"], "persisted metadata should round-trip the entry payload")
}

func TestStreamLogs_DropsUnmarshalableMetadata(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	logs := &testInternalSessionLogStore{}
	orch := &Orchestrator{
		agentRunLogs: logs,
		logger:       zerolog.Nop(),
	}

	logCh := make(chan LogEntry, 1)
	logCh <- LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   "bad metadata",
		Metadata:  map[string]interface{}{"fn": func() {}},
	}
	close(logCh)

	orch.streamLogs(context.Background(), sessionID, orgID, models.AgentTypeClaudeCode, nil, 1, logCh, nil)

	require.Len(t, logs.logs, 1, "streamLogs should still persist the log entry when metadata fails to marshal")
	require.Nil(t, logs.logs[0].Metadata, "unmarshalable metadata should be dropped to nil rather than blocking the log")
}

func TestHumanInputRequestFromQuestionLog(t *testing.T) {
	t.Parallel()

	req := humanInputRequestFromQuestionLog(LogEntry{
		Level:   "question",
		Message: "Which approach should Claude use?",
		Metadata: map[string]interface{}{
			"title":        "Choose approach",
			"context":      "Migration touches settings.",
			"blocks_phase": "implementation",
			"options": []interface{}{
				map[string]interface{}{"label": "Reuse table", "description": "Keep the current schema."},
				"Create table",
			},
		},
	})

	require.Equal(t, models.HumanInputRequestKindFreeText, req.Kind, "legacy question logs should remain free-text compatible")
	require.Equal(t, "Choose approach", req.Title, "metadata title should become request title")
	require.Equal(t, "Which approach should Claude use?", req.Body, "log message should become request body")
	require.NotNil(t, req.Context, "metadata context should be preserved")
	require.Equal(t, "Migration touches settings.", *req.Context, "metadata context should round-trip")
	require.NotNil(t, req.BlocksPhase, "metadata phase should be preserved")
	require.Equal(t, "implementation", *req.BlocksPhase, "metadata phase should round-trip")
	require.Equal(t, []models.HumanInputChoice{
		{ID: "reuse-table", Label: "Reuse table", Description: "Keep the current schema."},
		{ID: "create-table", Label: "Create table"},
	}, req.Choices, "metadata options should become normalized choice rows")
}

func TestPrepareSandboxGitHubAuth_LegacyAddsCoAuthorTrailer(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	userID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	noreply := "4+alice@users.noreply.github.com"
	orch := &Orchestrator{
		users: testInternalUserLookup{
			user: models.User{
				ID:                 userID,
				OrgID:              orgID,
				Name:               "Alice Example",
				Email:              "alice@example.com",
				GitHubNoreplyEmail: &noreply,
			},
		},
		logger: zerolog.Nop(),
	}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}
	repo := &models.Repository{FullName: "assembledhq/143"}
	cfg := &SandboxConfig{HomeDir: "/home/sandbox", Env: map[string]string{}}

	authState, err := orch.prepareSandboxGitHubAuth(context.Background(), run, repo, "ghp_test123", cfg, zerolog.Nop())
	require.NoError(t, err, "prepareSandboxGitHubAuth should not fail on the legacy fallback path")
	require.Nil(t, authState, "prepareSandboxGitHubAuth should not create auth state on the legacy fallback path")
	require.Equal(t, "ghp_test123", cfg.Env["GITHUB_TOKEN"], "prepareSandboxGitHubAuth should expose the fallback token on the legacy path")
	require.Equal(t, "143 Agent", cfg.Env[sandboxauth.GitNameEnvVar], "prepareSandboxGitHubAuth should seed the default git author name on the legacy path")
	require.Equal(t, "noreply@143.dev", cfg.Env[sandboxauth.GitEmailEnvVar], "prepareSandboxGitHubAuth should seed the default git author email on the legacy path")
	require.Equal(t, "Co-authored-by: Alice Example <4+alice@users.noreply.github.com>", cfg.Env[sandboxauth.CoAuthorEnvVar], "prepareSandboxGitHubAuth should attach a co-author trailer when the triggering user can be loaded")
}

func TestPrepareSandboxGitHubAuth_LegacyIgnoresUserLookupFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	userID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	orch := &Orchestrator{
		users:  testInternalUserLookup{err: errors.New("user lookup failed")},
		logger: zerolog.Nop(),
	}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}
	cfg := &SandboxConfig{HomeDir: "/home/sandbox", Env: map[string]string{}}

	authState, err := orch.prepareSandboxGitHubAuth(context.Background(), run, &models.Repository{FullName: "assembledhq/143"}, "ghp_test123", cfg, zerolog.Nop())
	require.NoError(t, err, "prepareSandboxGitHubAuth should not fail when the legacy co-author lookup is best-effort")
	require.Nil(t, authState, "prepareSandboxGitHubAuth should not create auth state on the legacy fallback path")
	require.Equal(t, "ghp_test123", cfg.Env["GITHUB_TOKEN"], "prepareSandboxGitHubAuth should still expose the fallback token when user lookup fails")
	require.Empty(t, cfg.Env[sandboxauth.CoAuthorEnvVar], "prepareSandboxGitHubAuth should skip the co-author trailer when the triggering user cannot be loaded")
}

func TestSessionWorkingBranch_PrefersPersistedBranch(t *testing.T) {
	t.Parallel()

	workingBranch := "143/persisted/fix-auth"
	run := &models.Session{ID: uuid.New(), WorkingBranch: &workingBranch}

	require.Equal(t, workingBranch, sessionWorkingBranch(run, &models.Issue{Title: "Ignored"}), "sessionWorkingBranch should reuse the persisted working branch when present")
}

func TestSetupFreshSandbox_CodeReviewChecksOutPullRequestHead(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	repoID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	repo := models.Repository{
		ID:             repoID,
		OrgID:          orgID,
		FullName:       "assembledhq/143",
		DefaultBranch:  "main",
		CloneURL:       "https://github.com/assembledhq/143.git",
		InstallationID: 42,
	}
	title := "Code review for assembledhq/143#42"
	session := &models.Session{
		ID:           uuid.MustParse("88888888-8888-8888-8888-888888888888"),
		OrgID:        orgID,
		Origin:       models.SessionOriginCodeReview,
		RepositoryID: &repoID,
		AgentType:    models.AgentType("test"),
		Title:        &title,
		RevisionContext: json.RawMessage(`{
			"kind": "code_review",
			"github_pr_number": 42,
			"head_sha": "expected-head-sha"
		}`),
	}
	provider := &testInternalSandboxProvider{
		execFn: func(cmd string, stdout, stderr io.Writer) (int, error) {
			if cmd == "git rev-parse HEAD" {
				_, _ = io.WriteString(stdout, "expected-head-sha\n")
			}
			return 0, nil
		},
	}
	orch := &Orchestrator{
		repositories: testInternalRepoStore{repo: repo},
		github:       testInternalGitHubTokens{token: "ghp_test123"},
		provider:     provider,
		logger:       zerolog.Nop(),
	}

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", WorkDir: "/home/sandbox/backend", HomeDir: "/home/sandbox"}, nil, zerolog.Nop(), nil)

	require.NoError(t, err, "setupFreshSandbox should check out the recorded PR head for code review sessions")
	require.Contains(t, provider.execCalls, "git fetch --quiet --no-tags 'https://x-access-token:ghp_test123@github.com/assembledhq/143.git' 'pull/42/head'", "code review checkout should fetch the GitHub PR head ref using an authenticated URL, since origin has been scrubbed of its token and the credential helper is not configured yet")
	require.NotContains(t, provider.execCalls, "git fetch --quiet --no-tags origin 'pull/42/head'", "code review checkout must not fetch from the token-less origin remote")
	require.Contains(t, provider.execCalls, "git checkout -B '143/88888888/code-review-for-assembledhq-143-42' FETCH_HEAD", "code review checkout should reset the session branch to the PR head")
	require.Contains(t, provider.execCalls, "git rev-parse HEAD", "code review checkout should verify the checked-out head SHA")
	require.NotContains(t, provider.execCalls, "git checkout -b '143/88888888/code-review-for-assembledhq-143-42'", "code review checkout should not branch from the cloned target branch tip")

	// Defense-in-depth ordering: the git credential helper must be configured
	// right after the clone, before any authenticated git operation, so future
	// git ops added to fresh-sandbox setup can reach GitHub via the auth socket.
	bootstrapIdx := indexOfExecCall(provider.execCalls, "143-tools git-bootstrap --workdir=/home/sandbox/backend")
	fetchIdx := indexOfExecCall(provider.execCalls, "git fetch --quiet --no-tags 'https://x-access-token:ghp_test123@github.com/assembledhq/143.git' 'pull/42/head'")
	require.GreaterOrEqual(t, bootstrapIdx, 0, "code review setup should run git-bootstrap to install the credential helper")
	require.Less(t, bootstrapIdx, fetchIdx, "git-bootstrap should run before the PR head fetch so the credential helper is configured first")
}

func indexOfExecCall(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

func TestSetupFreshSandbox_WorkingBranchCheckoutFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	repoID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	issueID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	repo := models.Repository{
		ID:             repoID,
		OrgID:          orgID,
		FullName:       "assembledhq/143",
		DefaultBranch:  "main",
		CloneURL:       "https://github.com/assembledhq/143.git",
		InstallationID: 42,
	}
	issue := models.Issue{ID: issueID, OrgID: orgID, RepositoryID: &repoID, Title: "Fix checkout failure"}

	tests := []struct {
		name       string
		execExit   int
		execErr    error
		execStderr string
		wantErr    string
	}{
		{
			name:     "exec error",
			execExit: 1,
			execErr:  errors.New("exec failed"),
			wantErr:  "create working branch 143/",
		},
		{
			name:       "non-zero exit",
			execExit:   17,
			execStderr: "branch already exists",
			wantErr:    "exit=17 stderr=branch already exists",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := &models.Session{
				ID:             uuid.MustParse("88888888-8888-8888-8888-888888888888"),
				OrgID:          orgID,
				PrimaryIssueID: &issueID,
				RepositoryID:   &repoID,
				AgentType:      models.AgentType("test"),
			}
			provider := &testInternalSandboxProvider{
				execExit:   tt.execExit,
				execErr:    tt.execErr,
				execStderr: tt.execStderr,
			}
			orch := &Orchestrator{
				issues:       testInternalIssueStore{issue: issue},
				repositories: testInternalRepoStore{repo: repo},
				github:       testInternalGitHubTokens{token: "ghp_test123"},
				provider:     provider,
				logger:       zerolog.Nop(),
			}

			_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", WorkDir: "/home/sandbox/backend", HomeDir: "/home/sandbox"}, nil, zerolog.Nop(), nil)
			require.Error(t, err, "setupFreshSandbox should fail when the working branch cannot be created")
			require.Contains(t, err.Error(), tt.wantErr, "setupFreshSandbox should surface the working-branch checkout failure")
		})
	}
}
