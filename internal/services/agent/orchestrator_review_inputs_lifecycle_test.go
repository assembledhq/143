package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type reviewInputGateRuntimeStore struct {
	agent.ThreadRuntimeStore
	created atomic.Int64
	closed  atomic.Int64
}

func (s *reviewInputGateRuntimeStore) CreateStarting(_ context.Context, org uuid.UUID, params db.CreateThreadRuntimeParams) (models.ThreadRuntime, error) {
	s.created.Add(1)
	return models.ThreadRuntime{ID: uuid.New(), OrgID: org, SessionID: params.SessionID, ThreadID: params.ThreadID, LeaseToken: params.LeaseToken}, nil
}

func (s *reviewInputGateRuntimeStore) MarkTerminalWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.ThreadRuntimeStatus, string, string) (bool, error) {
	s.closed.Add(1)
	return true, nil
}

func (s *reviewInputGateRuntimeStore) HeartbeatWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration) (bool, error) {
	return true, nil
}

type reviewInputGateInboxStore struct {
	agent.ThreadInboxStore
	delivering atomic.Int64
}

func (s *reviewInputGateInboxStore) LastAcknowledgedSequence(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}

func (s *reviewInputGateInboxStore) MarkAckedForSeedMessages(_ context.Context, _, _, _, _ uuid.UUID, messages []int64) (int64, error) {
	return int64(len(messages)), nil
}

func (s *reviewInputGateInboxStore) MarkDeliveringForMessages(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, _ string, messages []int64) (int64, error) {
	s.delivering.Add(int64(len(messages)))
	return int64(len(messages)), nil
}

func TestContinueSessionReviewInputsFailBeforeRuntimeAndInboxOwnership(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		failure     string
		preparation int64
	}{
		{name: "reused availability", failure: "availability", preparation: 1},
		{name: "reused digest", failure: "digest", preparation: 1},
		{name: "reused source integrity", failure: "source", preparation: 1},
		{name: "reused cancellation", failure: "cancellation", preparation: 1},
		{name: "reused head", failure: "head", preparation: 1},
		{name: "reused invalid marker", failure: "marker", preparation: 0},
		{name: "reused malformed revision", failure: "revision", preparation: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			d := defaultDeps()
			org := testOrg()
			issue := testIssue(org)
			session := testRun(org, issue.ID)
			session.Origin, session.Status = models.SessionOriginCodeReview, models.SessionStatusIdle
			session.CurrentTurn = 1
			head, base, branch, container := strings.Repeat("a", 40), strings.Repeat("b", 40), "review", "review-source"
			session.BaseCommitSHA, session.WorkingBranch, session.ContainerID = &head, &branch, &container
			session.SandboxState = models.SandboxStateRunning
			revision, err := json.Marshal(map[string]any{"kind": "code_review", "github_repo": d.repos.repo.FullName, "github_pr_number": 42, "base_sha": base, "head_sha": head})
			require.NoError(t, err, "captured input revision should encode")
			session.RevisionContext = revision
			if test.failure == "revision" {
				session.RevisionContext = []byte(`{`)
			}
			thread := uuid.New()
			d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{ID: thread, SessionID: session.ID, OrgID: org, Status: models.ThreadStatusRunning}}
			d.messages.messages = []models.SessionMessage{{ID: 42, OrgID: org, SessionID: session.ID, ThreadID: &thread, TurnNumber: 1, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review captured changes."}}
			d.sessions.peekContainerIDFn = func() (string, error) { return container, nil }
			d.sessions.releaseHoldFn = func() (bool, string, error) { return false, container, nil }
			runtimes, inbox := &reviewInputGateRuntimeStore{}, &reviewInputGateInboxStore{}
			d.threadRuntimes, d.threadInbox = runtimes, inbox
			var preparations, executions atomic.Int64
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			d.provider.ExecFn = func(_ context.Context, _ *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
				if strings.Contains(command, "identity = json.loads(sys.argv[1])") {
					if preparations.Add(1) == test.preparation {
						switch test.failure {
						case "availability":
							return -1, errors.New("temporary executor connection failure")
						case "digest":
							_, writeErr := io.WriteString(stderr, "prepared review input digest or merge base mismatch")
							return 1, writeErr
						case "source":
							_, writeErr := io.WriteString(stderr, "captured review source object hash mismatch")
							return 125, writeErr
						case "head":
							_, writeErr := io.WriteString(stderr, "workspace HEAD differs from captured review head")
							return 1, writeErr
						case "cancellation":
							cancel(agent.ErrUserCancelCause)
							return -1, context.Canceled
						}
					}
					return 0, nil
				}
				if strings.Contains(command, "with pathlib.Path(sys.argv[1]).open() as marker:") {
					if test.failure == "marker" {
						_, writeErr := io.WriteString(stdout, "{}")
						return 0, writeErr
					}
					return 42, nil
				}
				if strings.HasPrefix(command, "git -C ") && strings.Contains(command, "rev-parse --verify HEAD") {
					_, writeErr := io.WriteString(stdout, head+"\n"+branch+"\n"+head+"\n")
					return 0, writeErr
				}
				return 0, nil
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions.Add(1)
				return &agent.AgentResult{}, nil
			}
			err = buildOrchestrator(d).ContinueSession(ctx, session, &agent.ContinueSessionOptions{ThreadID: &thread, QueuedMessageID: func() *int64 { id := int64(42); return &id }()})
			require.Equal(t, test.preparation, preparations.Load(), "only the targeted immutable input gate must fail")
			switch test.failure {
			case "availability":
				require.ErrorIs(t, err, agent.ErrSandboxWorkspaceNotReady, "temporary availability must preserve the waiting retry")
				require.Empty(t, d.sessionThreads.updateResultCalls, "waiting retry must not terminalize the review thread")
			case "digest", "source", "head", "marker", "revision":
				require.ErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "invalid captured evidence must remain fatal")
				require.NotErrorIs(t, err, agent.ErrSandboxWorkspaceNotReady, "evidence mismatch must never become an availability retry")
				require.Len(t, d.sessionThreads.updateResultCalls, 1, "invalid evidence must persist the reviewer failure")
				require.Equal(t, models.ThreadStatusFailed, d.sessionThreads.updateResultCalls[0].status, "reviewer must not remain running after fatal input validation")
			case "cancellation":
				require.ErrorIs(t, err, context.Canceled, "cancellation must win over a simultaneous dependency result")
				require.NotErrorIs(t, err, agent.ErrSandboxWorkspaceNotReady, "cancellation must not become an availability retry")
			}
			require.Zero(t, executions.Load(), "input failure must not launch the reviewer")
			require.Zero(t, runtimes.created.Load(), "input failure must not claim a runtime")
			require.Zero(t, runtimes.closed.Load(), "an unstarted runtime must not be marked completed")
			require.Zero(t, inbox.delivering.Load(), "the seeded message must remain unclaimed for retry")
		})
	}
}

func TestContinueSessionManualRuntimeConflictPrecedesWorkspacePreparation(t *testing.T) {
	t.Parallel()
	d := defaultDeps()
	org := testOrg()
	session := testRun(org, testIssue(org).ID)
	session.Origin, session.Status = models.SessionOriginManual, models.SessionStatusIdle
	session.CurrentTurn = 1
	container := "manual-source"
	session.ContainerID, session.SandboxState = &container, models.SandboxStateRunning
	thread := uuid.New()
	d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{ID: thread, SessionID: session.ID, OrgID: org, Status: models.ThreadStatusRunning}}
	d.messages.messages = []models.SessionMessage{{ID: 42, OrgID: org, SessionID: session.ID, ThreadID: &thread, TurnNumber: 1, Role: models.MessageRoleUser, Content: "Continue the task."}}
	d.sessions.releaseHoldFn = func() (bool, string, error) { return false, container, nil }
	runtimes := &activeRuntimeConflictStore{}
	d.threadRuntimes = runtimes
	var preparations, executions atomic.Int64
	d.adapter.prepareFn = func(context.Context, *agent.AgentInput) (*agent.AgentPrompt, error) {
		preparations.Add(1)
		return &agent.AgentPrompt{}, nil
	}
	d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
		executions.Add(1)
		return &agent.AgentResult{}, nil
	}
	err := buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &thread})
	require.ErrorIs(t, err, agent.ErrThreadRuntimeAlreadyActive, "manual continuations must reject an active runtime at their original gate")
	require.Len(t, runtimes.createCalls, 1, "the original runtime conflict check must execute")
	require.Zero(t, preparations.Load(), "manual runtime conflicts must precede workspace prompt preparation")
	require.Zero(t, executions.Load(), "an active runtime must prevent duplicate execution")
}

func TestRunAgentReviewCheckoutFailuresPersistTerminalState(t *testing.T) {
	t.Parallel()
	for _, invalidContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "captured checkout fails", true: "context fails after clone"}[invalidContext], func(t *testing.T) {
			t.Parallel()
			d := defaultDeps()
			org := testOrg()
			issue := testIssue(org)
			run := testRun(org, issue.ID)
			run.Origin = models.SessionOriginCodeReview
			run.RepositoryID = &d.repos.repo.ID
			threadID := uuid.New()
			run.PrimaryThreadID = &threadID
			run.RevisionContext = []byte(`{"kind":"code_review","github_repo":"example/repo","github_pr_number":42,"base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
			d.sessionThreads = &mockSessionThreadStore{}
			var executions atomic.Int64
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions.Add(1)
				return &agent.AgentResult{}, nil
			}
			d.provider.CloneRepoFn = func(context.Context, *agent.Sandbox, string, string, string) error {
				if invalidContext {
					run.RevisionContext = []byte(`{`)
				}
				return nil
			}
			d.provider.ExecFn = func(_ context.Context, _ *agent.Sandbox, command string, stdout, _ io.Writer) (int, error) {
				if command == "git rev-parse HEAD" {
					_, err := io.WriteString(stdout, strings.Repeat("c", 40)+"\n")
					return 0, err
				}
				return 0, nil
			}
			err := buildOrchestrator(d).RunAgent(context.Background(), run)
			require.Error(t, err, "invalid review checkout must reject agent execution")
			if invalidContext {
				require.ErrorContains(t, err, "revision context", "context failure should retain its cause")
			} else {
				require.ErrorIs(t, err, agent.ErrStalePullRequestHead, "checkout failure should retain its exact-head fence")
			}
			require.Zero(t, executions.Load(), "reviewer must never run with invalid captured source")
			results := d.sessions.getResultUpdates()
			require.Len(t, results, 1, "startup failure should persist one session terminal result")
			require.Equal(t, models.SessionStatusFailed, results[0].status, "startup failure must terminalize the session")
			require.Len(t, d.sessionThreads.updateResultCalls, 1, "startup failure must terminalize the primary thread")
			require.Equal(t, models.ThreadStatusFailed, d.sessionThreads.updateResultCalls[0].status, "primary thread must not remain running")
			require.Equal(t, 1, d.sessions.releaseHoldCalls, "startup failure should release its owned turn hold")
			require.Equal(t, 1, d.provider.GetDestroyCalls(), "startup failure should release its unpublished source container")
		})
	}
}
