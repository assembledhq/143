package agent_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestContinueReviewRestoredGitGuardPrecedesBootstrapAndToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		unsafe bool
	}{{name: "safe restored receipt"}, {name: "unsafe restored includes", unsafe: true}, {name: "unsafe restored pointer topology", unsafe: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			session.Origin = models.SessionOriginCodeReview
			session.InteractionMode = models.SessionInteractionModeSingleRun
			session.ValidationPolicy = models.SessionValidationPolicySkip
			session.Status = models.SessionStatusRunning
			session.CurrentTurn = 1
			threadID := uuid.New()
			session.PrimaryThreadID = &threadID
			session.SnapshotKey = strPtr("restored-review-checkpoint")
			d := defaultDeps()
			configurePinnedReviewFixture(session, &d)
			d.issues.issue = issue
			d.github.err = errors.New("installation token lookup must not occur for restored local evidence")
			d.snapshots.data = map[string][]byte{*session.SnapshotKey: []byte("snapshot source")}
			d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{ID: threadID, OrgID: orgID, SessionID: session.ID, Status: models.ThreadStatusRunning, ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly}}
			d.messages.messages = []models.SessionMessage{{ID: 1, SessionID: session.ID, OrgID: orgID, ThreadID: &threadID, TurnNumber: 1, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review local pinned source."}}
			d.sessions.getByIDFn = func(uuid.UUID, uuid.UUID) (models.Session, error) { return *session, nil }
			restored, guarded, bootstrapped := false, false, false
			d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, input io.Reader) error {
				_, err := io.Copy(io.Discard, input)
				restored = err == nil
				return err
			}
			previous := d.provider.ExecFn
			d.provider.ExecFn = func(ctx context.Context, sandbox *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
				if strings.Contains(command, `"guard_configuration":true`) {
					require.True(t, restored, "literal guard must inspect the restored checkout before any local bootstrap")
					guarded = true
					if tt.unsafe {
						_, err := io.WriteString(stderr, "unsafe restored local Git metadata")
						return 1, err
					}
				}
				if strings.Contains(command, "143-tools git-bootstrap") {
					require.True(t, guarded, "review-only common bootstrap must carry a finite pre-Git guard")
					bootstrapped = true
				}
				return previous(ctx, sandbox, command, stdout, stderr)
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				require.False(t, tt.unsafe, "unsafe restored metadata must never reach reviewer runtime")
				return &agent.AgentResult{Summary: "reviewed"}, nil
			}
			err := buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &threadID})
			require.True(t, restored, "review restoration must occur without eager installation-token acquisition")
			require.True(t, guarded, "every restored review must pass the common neutral guard before bootstrap")
			if tt.unsafe {
				require.ErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "unsafe restored metadata must terminate the exact review thread")
				require.False(t, bootstrapped, "rejected includes/pointer layouts must execute no Git bootstrap")
			} else {
				require.NoError(t, err, "safe restored review must reuse local evidence with a forbidden token provider")
				require.True(t, bootstrapped, "safe restored profile must preserve helper-before-recipe setup")
			}
		})
	}
}

func TestContinueReviewBudgetExpiryAfterReadinessKeepsLocalVerification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		cleanupStage string
	}{
		{name: "ordinary expiry retains platform verification"},
		{name: "readiness cleanup uncertainty forbids verification", cleanupStage: "readiness"},
		{name: "bootstrap cleanup uncertainty forbids verification", cleanupStage: "bootstrap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			session.Origin = models.SessionOriginCodeReview
			session.InteractionMode = models.SessionInteractionModeSingleRun
			session.ValidationPolicy = models.SessionValidationPolicySkip
			session.Status = models.SessionStatusIdle
			session.CurrentTurn = 1
			session.ContainerID = strPtr("ready-review")
			session.SandboxState = models.SandboxStateRunning
			d := defaultDeps()
			configurePinnedReviewFixture(session, &d)
			d.issues.issue = issue
			d.sessions.getByIDFn = func(uuid.UUID, uuid.UUID) (models.Session, error) { return *session, nil }
			d.github.err = errors.New("complete local-only verification must not look up installation auth")
			d.messages.messages = []models.SessionMessage{{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review ready local source."}}
			previous := d.provider.ExecFn
			guardCalls, verifyOnlyCalls, reviewerCalls := 0, 0, 0
			d.provider.ExecFn = func(ctx context.Context, sandbox *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
				if tt.cleanupStage == "readiness" && strings.Contains(command, `"expected_branch":`) {
					<-ctx.Done()
					return -1, errors.Join(agent.ErrCodeReviewPreparationCleanup, ctx.Err())
				}
				if strings.Contains(command, `"guard_configuration":true`) {
					guardCalls++
					<-ctx.Done()
					if tt.cleanupStage == "bootstrap" {
						return -1, errors.Join(agent.ErrCodeReviewPreparationCleanup, ctx.Err())
					}
					return -1, ctx.Err()
				}
				if strings.Contains(command, `"verify_only":true`) {
					verifyOnlyCalls++
				}
				return previous(ctx, sandbox, command, stdout, stderr)
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				reviewerCalls++
				return &agent.AgentResult{Summary: "reviewed"}, nil
			}
			ctx := agent.WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(200*time.Millisecond))
			err := buildOrchestrator(d).ContinueSession(ctx, session, nil)
			if tt.cleanupStage != "" {
				require.ErrorIs(t, err, agent.ErrCodeReviewPreparationCleanup, "deadline fallback must retain unconfirmed process cleanup")
				require.ErrorIs(t, err, context.DeadlineExceeded, "cleanup uncertainty must preserve the original budget expiry")
				require.ErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "unconfirmed cleanup must stop the initial review lifecycle")
				require.Equal(t, 0, verifyOnlyCalls, "unconfirmed termination forbids the post-budget verification allowance")
				require.Equal(t, 0, reviewerCalls, "reviewer cannot execute beside a potentially surviving preparation process")
				expectedGuards := 1
				if tt.cleanupStage == "readiness" {
					expectedGuards = 0
				}
				require.Equal(t, expectedGuards, guardCalls, "failed readiness must never begin bootstrap or retry its guard")
			} else {
				require.NoError(t, err, "ordinary expiry after ready metadata may validate existing evidence without renewing preparation")
				require.Equal(t, 1, guardCalls, "bootstrap must never retry after its original budget expires")
				require.Equal(t, 1, verifyOnlyCalls, "ordinary boundary expiry retains exactly one platform-only verification allowance")
				require.Equal(t, 1, reviewerCalls, "confirmed preparation cleanup permits the reviewer after local verification")
			}
		})
	}
}

type preparationRecheckTurnStore struct{ agent.CodeReviewTurnStore }

func (preparationRecheckTurnStore) Claim(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, int, int64) (bool, error) {
	return true, nil
}
func (preparationRecheckTurnStore) NativeResumeProvider(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (string, bool, error) {
	return "native-review-provider", true, nil
}

func TestContinueReviewReopensAuthBeforeRepairingWarmEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		inferredThread    bool
		cancelPreparation bool
	}{
		{name: "recovery without explicit continuation options"},
		{name: "thread inferred from the queued review message", inferredThread: true},
		{name: "cancelled warm sibling leaves shared credentials alive", inferredThread: true, cancelPreparation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			session.Origin = models.SessionOriginCodeReview
			session.InteractionMode = models.SessionInteractionModeSingleRun
			session.ValidationPolicy = models.SessionValidationPolicySkip
			session.Status = models.SessionStatusIdle
			session.CurrentTurn = 1
			session.ContainerID = strPtr("retained-review")
			session.SandboxState = models.SandboxStateRunning
			d := defaultDeps()
			configurePinnedReviewFixture(session, &d)
			d.issues.issue = issue
			d.orgs = &mockOrgStore{org: models.Organization{ID: orgID}}
			d.identityResolver = identity.NewResolver(d.github, zerolog.Nop())
			auth := &fakeSandboxAuthServer{}
			d.sandboxAuth = auth
			d.users = fakeUserStore{}
			d.messages.messages = []models.SessionMessage{{
				ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2,
				Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review the pinned change.",
			}}
			if tt.inferredThread {
				threadID := uuid.New()
				d.messages.messages[0].ThreadID = &threadID
				d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{
					ID: threadID, OrgID: orgID, SessionID: session.ID, Status: models.ThreadStatusRunning,
					ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly,
				}}
			}
			d.provider.ReadFileFn = func(_ context.Context, _ *agent.Sandbox, name string) ([]byte, error) {
				if strings.HasSuffix(name, ".143/config.json") {
					return []byte(`{"bootstrap":{"commands":["git ls-remote https://github.com/test/private-dependency.git"]}}`), nil
				}
				return nil, nil
			}
			previousExec := d.provider.ExecFn
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prepared, bootstrapped := false, false
			d.provider.ExecFn = func(ctx context.Context, sandbox *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
				if strings.Contains(command, "143-tools git-bootstrap") {
					require.Equal(t, 1, auth.listenCalls, "warm helper configuration should follow credential socket recreation")
					bootstrapped = true
				}
				if strings.Contains(command, "private-dependency.git") {
					require.Equal(t, 1, auth.listenCalls, "Git-based dependency preparation must have a live credential socket")
					require.True(t, bootstrapped, "legacy warm evidence should configure its Git credential helper before private dependency setup")
					require.Equal(t, 0, d.sessions.acquireHoldCalls, "warm evidence preparation must finish before acquiring the shared turn hold")
					prepared = true
					if tt.cancelPreparation {
						cancel()
						return -1, context.Canceled
					}
				}
				return previousExec(ctx, sandbox, command, stdout, stderr)
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				require.False(t, tt.cancelPreparation, "a cancelled warm sibling must not execute a reviewer")
				require.True(t, prepared, "every warm code-review recovery path should finish dependency evidence preparation before execution")
				return &agent.AgentResult{Summary: "reviewed"}, nil
			}
			d.sessions.releaseHoldFn = func() (bool, string, error) { return false, *session.ContainerID, nil }
			err := buildOrchestrator(d).ContinueSession(ctx, session, nil)
			if tt.cancelPreparation {
				require.ErrorIs(t, err, context.Canceled, "cancelled preparation should stop its sibling before it acquires a shared turn hold")
				require.Equal(t, 0, auth.closeCalls, "cancelled warm sibling must leave the reopened shared listener for the container's current owner")
				require.Equal(t, 0, d.sessions.acquireHoldCalls, "cancelled readiness waiter must not acquire or release another sibling's turn hold")
			} else {
				require.NoError(t, err, "warm review recovery should repair evidence with inferred ownership and existing read-only policy")
			}
			require.True(t, prepared, "nil continuation options must not bypass the code-review evidence gate")
		})
	}
}

func TestContinueReviewPreparationFailureIsTerminalForExactThread(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source string
		inferred     bool
		unscoped     bool
		recheck      bool
		cleanup      bool
	}{
		{name: "legacy fresh review", source: "fresh"},
		{name: "warm retained review", source: "warm"},
		{name: "warm cleanup uncertainty overrides cancellation", source: "warm", cleanup: true},
		{name: "recheck cleanup uncertainty retains fenced failure", source: "warm", recheck: true, cleanup: true},
		{name: "restored review checkpoint", source: "snapshot"},
		{name: "recovered warm inferred thread", source: "warm", inferred: true},
		{name: "legacy recovery without a resolvable thread", source: "fresh", inferred: true, unscoped: true},
		{name: "recheck retains fenced assessment failure ownership", source: "warm", recheck: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			session.Origin = models.SessionOriginCodeReview
			session.InteractionMode = models.SessionInteractionModeSingleRun
			session.ValidationPolicy = models.SessionValidationPolicySkip
			session.Status = models.SessionStatusRunning
			session.CurrentTurn = 1
			primaryThreadID, reviewerThreadID := uuid.New(), uuid.New()
			session.PrimaryThreadID = &primaryThreadID
			d := defaultDeps()
			configurePinnedReviewFixture(session, &d)
			d.issues.issue = issue
			d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{
				ID: reviewerThreadID, OrgID: orgID, SessionID: session.ID, Status: models.ThreadStatusRunning,
				ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly,
			}}
			d.messages.messages = []models.SessionMessage{{ID: 1, SessionID: session.ID, OrgID: orgID, ThreadID: &reviewerThreadID, TurnNumber: 1, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review the pull request."}}
			if tt.unscoped {
				d.messages.messages[0].ThreadID = nil
			}
			switch tt.source {
			case "warm":
				session.ContainerID = strPtr("retained-review")
				session.SandboxState = models.SandboxStateRunning
			case "snapshot":
				session.SnapshotKey = strPtr("review-checkpoint")
				d.snapshots.data = map[string][]byte{*session.SnapshotKey: []byte("review snapshot")}
				d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, reader io.Reader) error {
					_, err := io.Copy(io.Discard, reader)
					return err
				}
			}
			d.sessions.getByIDFn = func(uuid.UUID, uuid.UUID) (models.Session, error) { return *session, nil }
			if session.ContainerID != nil {
				d.sessions.peekContainerIDFn = func() (string, error) { return *session.ContainerID, nil }
			}
			d.provider.ReadFileFn = func(_ context.Context, _ *agent.Sandbox, name string) ([]byte, error) {
				if strings.HasSuffix(name, ".143/config.json") {
					return []byte(`{"bootstrap":{"commands":["prepare-dependency-evidence"]}}`), nil
				}
				return nil, nil
			}
			previousExec := d.provider.ExecFn
			bootstrapCalls := 0
			d.provider.ExecFn = func(ctx context.Context, sandbox *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
				if strings.Contains(command, "prepare-dependency-evidence") {
					bootstrapCalls++
					if tt.cleanup {
						return -1, errors.Join(agent.ErrCodeReviewPreparationCleanup, context.Canceled)
					}
					_, err := io.WriteString(stderr, "failed command containing secret-token")
					return 1, err
				}
				return previousExec(ctx, sandbox, command, stdout, stderr)
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				t.Fatal("reviewer must not execute after dependency setup fails")
				return nil, nil
			}
			var opts *agent.ContinueSessionOptions
			if !tt.inferred {
				opts = &agent.ContinueSessionOptions{ThreadID: &reviewerThreadID}
			}
			ctx := context.Background()
			orchestrator := buildOrchestrator(d)
			if tt.recheck {
				messageID := int64(1)
				opts.QueuedMessageID = &messageID
				opts.CodeReviewTurn = &agent.CodeReviewTurnContinueOptions{AssessmentID: uuid.New(), ThreadID: reviewerThreadID, ExpectedTurn: 1, MessageID: messageID}
				ctx = jobctx.WithJobID(jobctx.WithLockToken(ctx, uuid.New()), uuid.New())
				orchestrator.SetCodeReviewTurnStore(preparationRecheckTurnStore{})
			}
			err := orchestrator.ContinueSession(ctx, session, opts)
			if tt.recheck {
				require.NotErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "recheck preparation failure must retain its assessment/job lease failure path")
			} else {
				require.ErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "preparation failures must use the worker's terminal thread boundary instead of scheduling a second bootstrap")
			}
			if tt.cleanup {
				require.ErrorIs(t, err, agent.ErrCodeReviewPreparationCleanup, "initial and fenced recheck failure paths must retain cleanup uncertainty")
				require.ErrorIs(t, err, context.Canceled, "terminal cleanup must preserve the joined cancellation identity")
			}
			var preparationErr *agent.CodeReviewWorkspacePreparationError
			require.ErrorAs(t, err, &preparationErr, "terminal result must retain the safe preparation stage for controller projection")
			require.Equal(t, 1, bootstrapCalls, "one preparation failure should execute its declared bootstrap exactly once")
			if !tt.unscoped && !tt.recheck {
				require.Contains(t, d.sessionThreads.statusesForThread(reviewerThreadID), models.ThreadStatusFailed, "setup failure must persist on the exact explicit or inferred reviewer thread")
			}
			require.NotContains(t, d.sessionThreads.statusesForThread(primaryThreadID), models.ThreadStatusFailed, "setup failure must not poison the sibling synthesis thread")
			require.Empty(t, d.sessions.getResultUpdates(), "shared review session completion remains owned by its controller")
			found := false
			for _, update := range d.sessionThreads.updateResultCalls {
				if update.threadID == reviewerThreadID && update.result.Error != nil {
					require.Equal(t, preparationErr.Error(), *update.result.Error, "thread failure explanation must persist exactly the sanitized stage summary without commands or secrets")
					found = true
				}
			}
			require.Equal(t, !tt.unscoped && !tt.recheck, found, "controller should harvest exact initial thread failures while rechecks preserve assessment lease ownership")
		})
	}
}
