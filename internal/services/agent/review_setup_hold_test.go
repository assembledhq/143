package agent_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewSetupLifecycleHolderStore struct {
	agent.SessionSandboxHolderStore
	mu                sync.Mutex
	created           []db.CreateSessionSandboxHolderParams
	released          []db.CreateSessionSandboxHolderParams
	active            map[uuid.UUID]db.CreateSessionSandboxHolderParams
	createErr         error
	onOperatorRelease func()
}

func (s *reviewSetupLifecycleHolderStore) CreateActive(_ context.Context, _ uuid.UUID, params db.CreateSessionSandboxHolderParams) (models.SessionSandboxHolder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, params)
	if s.createErr != nil {
		return models.SessionSandboxHolder{}, s.createErr
	}
	if s.active == nil {
		s.active = make(map[uuid.UUID]db.CreateSessionSandboxHolderParams)
	}
	s.active[params.HolderID] = params
	return models.SessionSandboxHolder{ID: uuid.New(), SessionID: params.SessionID, HolderID: params.HolderID, HolderKind: params.HolderKind, LeaseToken: params.LeaseToken}, nil
}

func (s *reviewSetupLifecycleHolderStore) ReleaseWithLease(_ context.Context, _ uuid.UUID, session uuid.UUID, kind models.SessionSandboxHolderKind, holder, token uuid.UUID) (bool, error) {
	s.mu.Lock()
	current, ok := s.active[holder]
	if !ok || current.SessionID != session || current.HolderKind != kind || current.LeaseToken != token {
		s.mu.Unlock()
		return false, nil
	}
	s.released = append(s.released, current)
	delete(s.active, holder)
	s.mu.Unlock()
	if kind == models.SessionSandboxHolderKindOperator && s.onOperatorRelease != nil {
		s.onOperatorRelease()
	}
	return true, nil
}

func (s *reviewSetupLifecycleHolderStore) HeartbeatWithLease(_ context.Context, _ uuid.UUID, session uuid.UUID, kind models.SessionSandboxHolderKind, holder, token uuid.UUID, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.active[holder]
	return ok && current.SessionID == session && current.HolderKind == kind && current.LeaseToken == token, nil
}

func (s *reviewSetupLifecycleHolderStore) CountActiveThreadRuntimesBySession(_ context.Context, _ uuid.UUID, session uuid.UUID) (int, error) {
	return s.CountActiveThreadRuntimesBySessionExcluding(context.Background(), uuid.Nil, session, uuid.Nil)
}

func (s *reviewSetupLifecycleHolderStore) CountActiveThreadRuntimesBySessionExcluding(_ context.Context, _ uuid.UUID, session, excluded uuid.UUID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, holder := range s.active {
		if id != excluded && holder.SessionID == session && holder.HolderKind == models.SessionSandboxHolderKindThreadRuntime {
			count++
		}
	}
	return count, nil
}

func (s *reviewSetupLifecycleHolderStore) count(kind models.SessionSandboxHolderKind) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, holder := range s.active {
		if holder.HolderKind == kind {
			count++
		}
	}
	return count
}

type reviewSetupLifecycleRuntimeStore struct {
	agent.ThreadRuntimeStore
	mu      sync.Mutex
	runtime map[uuid.UUID]models.ThreadRuntime
	created atomic.Int64
}

func (s *reviewSetupLifecycleRuntimeStore) CreateStarting(_ context.Context, org uuid.UUID, params db.CreateThreadRuntimeParams) (models.ThreadRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := models.ThreadRuntime{ID: uuid.New(), OrgID: org, SessionID: params.SessionID, ThreadID: params.ThreadID, LeaseToken: params.LeaseToken}
	if s.runtime == nil {
		s.runtime = make(map[uuid.UUID]models.ThreadRuntime)
	}
	s.runtime[params.ThreadID] = runtime
	s.created.Add(1)
	return runtime, nil
}

func (s *reviewSetupLifecycleRuntimeStore) GetActiveByThread(_ context.Context, _ uuid.UUID, thread uuid.UUID) (models.ThreadRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime, ok := s.runtime[thread]
	if !ok {
		return models.ThreadRuntime{}, pgx.ErrNoRows
	}
	return runtime, nil
}

func (s *reviewSetupLifecycleRuntimeStore) MarkTerminalWithLease(_ context.Context, _ uuid.UUID, runtime, _ uuid.UUID, _ models.ThreadRuntimeStatus, _, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for thread, current := range s.runtime {
		if current.ID == runtime {
			delete(s.runtime, thread)
		}
	}
	return true, nil
}

func (s *reviewSetupLifecycleRuntimeStore) HeartbeatWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration) (bool, error) {
	return true, nil
}

type reviewSetupLifecycleInboxStore struct {
	agent.ThreadInboxStore
	mu         sync.Mutex
	delivering map[uuid.UUID]int
}

func (s *reviewSetupLifecycleInboxStore) LastAcknowledgedSequence(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}

func (s *reviewSetupLifecycleInboxStore) MarkAckedForSeedMessages(_ context.Context, _, _, _, _ uuid.UUID, messages []int64) (int64, error) {
	return int64(len(messages)), nil
}

func (s *reviewSetupLifecycleInboxStore) MarkDeliveringForMessages(_ context.Context, _, thread, _, _ uuid.UUID, _ string, messages []int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivering == nil {
		s.delivering = make(map[uuid.UUID]int)
	}
	s.delivering[thread] += len(messages)
	return int64(len(messages)), nil
}

func (s *reviewSetupLifecycleInboxStore) CountPendingByThread(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}

func (s *reviewSetupLifecycleInboxStore) claimed(thread uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivering[thread]
}

type reviewSetupLifecycleThreadStore struct {
	*mockSessionThreadStore
	org, session uuid.UUID
}

func (s reviewSetupLifecycleThreadStore) GetByID(_ context.Context, _ uuid.UUID, thread uuid.UUID) (models.SessionThread, error) {
	return models.SessionThread{ID: thread, SessionID: s.session, OrgID: s.org, Status: models.ThreadStatusRunning}, nil
}

func newReviewSetupLifecycleOrchestrator(d testDeps, session *models.Session, holders *reviewSetupLifecycleHolderStore, runtimes *reviewSetupLifecycleRuntimeStore, inbox *reviewSetupLifecycleInboxStore) *agent.Orchestrator {
	return agent.NewOrchestrator(agent.OrchestratorConfig{
		Provider: d.provider, Adapters: map[models.AgentType]agent.AgentAdapter{d.adapter.Name(): d.adapter},
		Sessions: d.sessions, SessionThreads: reviewSetupLifecycleThreadStore{mockSessionThreadStore: &mockSessionThreadStore{}, org: session.OrgID, session: session.ID},
		SessionLogs: d.logs, SessionMessages: d.messages, SessionQuestions: d.questions, HumanInputRequests: d.humanInputs,
		Issues: d.issues, Repositories: d.repos, Jobs: d.jobs, GitHub: d.github,
		CodingCredentials: codingCredsForTest(d), Credentials: d.creds, Snapshots: d.snapshots,
		ThreadRuntimes: runtimes, ThreadInbox: inbox, SandboxHolders: holders,
		Logger: zerolog.Nop(), MaxConcurrent: 3,
	})
}

func reviewSetupLifecycleSession(t *testing.T) *models.Session {
	t.Helper()
	org := testOrg()
	session := testRun(org, testIssue(org).ID)
	session.Origin, session.Status = models.SessionOriginCodeReview, models.SessionStatusIdle
	session.CurrentTurn = 1
	session.ValidationPolicy = models.SessionValidationPolicySkip
	session.RevisionContext = []byte(`{"kind":"code_review","github_repo":"acme/backend","github_pr_number":42,"base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	head, branch, container := strings.Repeat("a", 40), "review", "shared-review-container"
	session.BaseCommitSHA, session.WorkingBranch, session.ContainerID = &head, &branch, &container
	session.SandboxState = models.SandboxStateRunning
	return session
}

func reviewSetupLifecycleExec(session *models.Session, command string, stdout io.Writer) (int, error) {
	if strings.Contains(command, "with pathlib.Path(sys.argv[1]).open() as marker:") {
		return 42, nil
	}
	if strings.HasPrefix(command, "git -C ") && strings.Contains(command, "rev-parse --verify HEAD") {
		_, err := io.WriteString(stdout, *session.BaseCommitSHA+"\n"+*session.WorkingBranch+"\n"+*session.BaseCommitSHA+"\n")
		return 0, err
	}
	return 0, nil
}

func TestReviewSetupPinProtectsBlockedSiblingUntilRuntimeHandoff(t *testing.T) {
	t.Parallel()
	d := defaultDeps()
	session := reviewSetupLifecycleSession(t)
	threadA, threadB := uuid.New(), uuid.New()
	d.messages.messages = []models.SessionMessage{
		{ID: 1, OrgID: session.OrgID, SessionID: session.ID, ThreadID: &threadA, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review the captured source."},
		{ID: 2, OrgID: session.OrgID, SessionID: session.ID, ThreadID: &threadB, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review the captured source."},
	}
	d.sessions.peekContainerIDFn = func() (string, error) { return *session.ContainerID, nil }
	holders, runtimes, inbox := &reviewSetupLifecycleHolderStore{}, &reviewSetupLifecycleRuntimeStore{}, &reviewSetupLifecycleInboxStore{}
	var handoffs atomic.Int64
	holders.onOperatorRelease = func() {
		require.Equal(t, 1, holders.count(models.SessionSandboxHolderKindThreadRuntime), "each setup pin must hand off to a durable runtime holder before releasing")
		thread := threadA
		if handoffs.Add(1) == 2 {
			thread = threadB
		}
		require.Equal(t, 1, inbox.claimed(thread), "runtime seed ownership must exist before the setup pin releases")
	}
	d.sessions.releaseHoldFn = func() (bool, string, error) {
		return holders.count(models.SessionSandboxHolderKindOperator)+holders.count(models.SessionSandboxHolderKindThreadRuntime) == 0, *session.ContainerID, nil
	}
	firstExecuting, secondPreparing, releaseSecond := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var inputs, executions atomic.Int64
	d.provider.ExecFn = func(ctx context.Context, _ *agent.Sandbox, command string, stdout, _ io.Writer) (int, error) {
		if strings.Contains(command, "identity = json.loads(sys.argv[1])") && inputs.Add(1) == 2 {
			close(secondPreparing)
			select {
			case <-releaseSecond:
			case <-ctx.Done():
				return -1, ctx.Err()
			}
		}
		return reviewSetupLifecycleExec(session, command, stdout)
	}
	d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
		if executions.Add(1) == 1 {
			close(firstExecuting)
			<-secondPreparing
			return nil, errors.New("first reviewer failed")
		}
		require.Equal(t, 0, holders.count(models.SessionSandboxHolderKindOperator), "the setup pin must be released before provider execution")
		require.Equal(t, 1, holders.count(models.SessionSandboxHolderKindThreadRuntime), "runtime ownership must replace the setup pin before provider execution")
		require.Equal(t, 1, inbox.claimed(threadB), "the successor must claim its seed only after input preparation")
		return &agent.AgentResult{Summary: "Reviewed captured source."}, nil
	}
	orchestrator := newReviewSetupLifecycleOrchestrator(d, session, holders, runtimes, inbox)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, second := make(chan error, 1), make(chan error, 1)
	copyA, copyB := *session, *session
	go func() {
		first <- orchestrator.ContinueSession(ctx, &copyA, &agent.ContinueSessionOptions{ThreadID: &threadA})
	}()
	select {
	case <-firstExecuting:
	case <-ctx.Done():
		t.Fatal("first reviewer must reach provider execution")
	}
	go func() {
		second <- orchestrator.ContinueSession(ctx, &copyB, &agent.ContinueSessionOptions{ThreadID: &threadB})
	}()
	select {
	case <-secondPreparing:
	case <-ctx.Done():
		t.Fatal("second reviewer must reach its blocked input preparation")
	}
	require.Equal(t, 1, holders.count(models.SessionSandboxHolderKindOperator), "blocked input preparation must retain a durable setup pin")
	require.Equal(t, int64(1), runtimes.created.Load(), "the blocked sibling must not create its runtime")
	require.Zero(t, inbox.claimed(threadB), "the blocked sibling must leave its seed unclaimed")
	select {
	case err := <-first:
		require.Error(t, err, "the first reviewer must exercise failure cleanup while its sibling prepares")
	case <-ctx.Done():
		t.Fatal("the first reviewer must finish failure cleanup without waiting on its sibling")
	}
	require.Equal(t, 1, holders.count(models.SessionSandboxHolderKindOperator), "a failed sibling must not release the preparer's independent pin")
	require.Zero(t, d.provider.GetDestroyCalls(), "sibling failure must not destroy a workspace with a preparing reviewer")
	close(releaseSecond)
	select {
	case err := <-second:
		require.NoError(t, err, "the preparing sibling must complete its protected runtime handoff")
	case <-ctx.Done():
		t.Fatal("the preparing reviewer must finish after its input barrier opens")
	}
	holders.mu.Lock()
	defer holders.mu.Unlock()
	for _, created := range holders.created {
		count := 0
		for _, released := range holders.released {
			if released.HolderID == created.HolderID && released.LeaseToken == created.LeaseToken {
				count++
			}
		}
		require.Equal(t, 1, count, "each setup or runtime holder must release once with its own exact token")
	}
}

func TestReviewSetupPinFailurePathsLeaveRuntimeAndSeedUnclaimed(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"fatal inputs", "temporary inputs", "cancelled inputs", "holder creation", "late container destruction"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			d := defaultDeps()
			session := reviewSetupLifecycleSession(t)
			thread := uuid.New()
			d.messages.messages = []models.SessionMessage{{ID: 1, OrgID: session.OrgID, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review the captured inputs."}}
			d.sessions.peekContainerIDFn = func() (string, error) { return *session.ContainerID, nil }
			d.sessions.releaseHoldFn = func() (bool, string, error) { return false, *session.ContainerID, nil }
			holders, runtimes, inbox := &reviewSetupLifecycleHolderStore{}, &reviewSetupLifecycleRuntimeStore{}, &reviewSetupLifecycleInboxStore{}
			if failure == "holder creation" {
				holders.createErr = errors.New("holder database unavailable")
			}
			var acquisitions atomic.Int64
			d.sessions.acquireHoldFn = func(container string) (string, error) {
				if acquisitions.Add(1) == 2 && failure == "late container destruction" {
					return "", nil
				}
				return container, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var inputs, executions, postPinCommands atomic.Int64
			d.provider.ExecFn = func(commandCtx context.Context, _ *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
				holders.mu.Lock()
				created := len(holders.created)
				holders.mu.Unlock()
				if created != 0 {
					postPinCommands.Add(1)
				}
				if strings.Contains(command, "identity = json.loads(sys.argv[1])") {
					inputs.Add(1)
					require.Equal(t, 1, holders.count(models.SessionSandboxHolderKindOperator), "input failures must occur while their independent setup pin is active")
					require.Zero(t, runtimes.created.Load(), "input failures must precede runtime ownership")
					require.Zero(t, inbox.claimed(thread), "input failures must precede seed delivery")
					switch failure {
					case "fatal inputs":
						_, err := io.WriteString(stderr, "prepared review input digest mismatch")
						return 1, err
					case "temporary inputs":
						return -1, errors.New("temporary sandbox transport failure")
					case "cancelled inputs":
						cancel()
						<-commandCtx.Done()
						return -1, commandCtx.Err()
					}
				}
				return reviewSetupLifecycleExec(session, command, stdout)
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions.Add(1)
				return &agent.AgentResult{}, nil
			}
			err := newReviewSetupLifecycleOrchestrator(d, session, holders, runtimes, inbox).ContinueSession(ctx, session, &agent.ContinueSessionOptions{ThreadID: &thread})
			switch failure {
			case "fatal inputs":
				require.ErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "invalid captured evidence must terminalize only its reviewer")
			case "temporary inputs", "holder creation":
				require.ErrorIs(t, err, agent.ErrSandboxWorkspaceNotReady, "temporary preparation failures must remain retryable")
			case "cancelled inputs":
				require.ErrorIs(t, err, context.Canceled, "caller cancellation must retain its original cause")
			case "late container destruction":
				require.ErrorIs(t, err, agent.ErrStaleSandboxIDCleared, "a failed strict container fence must reopen durable state and retry")
			}
			require.Zero(t, executions.Load(), "a failed setup must not execute its reviewer")
			require.Zero(t, runtimes.created.Load(), "a failed setup must not create or close a thread runtime")
			require.Zero(t, inbox.claimed(thread), "a failed setup must leave its seed available for retry")
			require.Zero(t, holders.count(models.SessionSandboxHolderKindOperator), "all setup failure exits must release their independent pin")
			if failure == "holder creation" || failure == "late container destruction" {
				require.Zero(t, inputs.Load(), "failed durable setup ownership must stop before immutable input execution")
				require.Zero(t, postPinCommands.Load(), "a failed setup pin fence must prevent every later workspace command")
			}
			holders.mu.Lock()
			defer holders.mu.Unlock()
			require.Len(t, holders.created, 1, "each attempt must try one independent setup pin")
			if failure != "holder creation" {
				require.Len(t, holders.released, 1, "each acquired setup pin must release exactly once on failure")
				require.Equal(t, holders.created[0], holders.released[0], "failure cleanup must preserve the exact holder identity and token")
			}
		})
	}
}
