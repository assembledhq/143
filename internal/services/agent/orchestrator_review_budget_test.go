package agent_test

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReviewPreparationBudgetStartsAfterQueueBeforeWorkspaceSetup(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		initial bool
		reused  bool
		restore bool
	}{
		{name: "initial run", initial: true},
		{name: "cold thread continuation"},
		{name: "reused thread continuation", reused: true},
		{name: "restored thread continuation", restore: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				d := defaultDeps()
				org := testOrg()
				session := testRun(org, testIssue(org).ID)
				session.Origin = models.SessionOriginCodeReview
				session.InteractionMode = models.SessionInteractionModeSingleRun
				session.ValidationPolicy = models.SessionValidationPolicySkip
				session.RevisionContext = []byte(`{"kind":"code_review","github_repo":"acme/backend","github_pr_number":42,"base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
				head, branch := strings.Repeat("a", 40), "review"
				session.BaseCommitSHA, session.WorkingBranch = &head, &branch
				var opts *agent.ContinueSessionOptions
				if !test.initial {
					session.Status, session.CurrentTurn = models.SessionStatusIdle, 1
					thread := uuid.New()
					opts = &agent.ContinueSessionOptions{ThreadID: &thread}
					d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{ID: thread, SessionID: session.ID, OrgID: org, Status: models.ThreadStatusRunning}}
					d.messages.messages = []models.SessionMessage{{ID: 1, OrgID: org, SessionID: session.ID, ThreadID: &thread, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review the captured local inputs."}}
				}
				if test.reused {
					container := "existing-review-container"
					session.ContainerID, session.SandboxState = &container, models.SandboxStateRunning
					d.sessions.peekContainerIDFn = func() (string, error) { return container, nil }
				}
				if test.restore {
					key := "synthetic-review-snapshot"
					session.SnapshotKey, session.SandboxState = &key, models.SandboxStateSnapshotted
					d.snapshots.data = map[string][]byte{key: []byte("snapshot")}
					d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, reader io.Reader) error {
						time.Sleep(2 * time.Minute)
						_, err := io.Copy(io.Discard, reader)
						return err
					}
				}
				d.provider.CreateFn = func(_ context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
					time.Sleep(time.Minute)
					return &agent.Sandbox{ID: "new-review-container", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir, Provider: "test"}, nil
				}
				d.provider.CloneRepoFn = func(context.Context, *agent.Sandbox, string, string, string) error {
					time.Sleep(2 * time.Minute)
					return nil
				}
				var preparations, executions atomic.Int64
				d.provider.ExecFn = func(ctx context.Context, _ *agent.Sandbox, command string, stdout, _ io.Writer) (int, error) {
					if strings.Contains(command, "identity = json.loads(sys.argv[1])") {
						preparations.Add(1)
						deadline, ok := ctx.Deadline()
						require.True(t, ok, "shared input preparation must carry a bounded deadline")
						require.Equal(t, started.Add(agent.CodeReviewWorkspacePreparationTimeout), deadline, "input generation must retain the setup-start deadline despite an hour-old queued job")
						elapsed := 3 * time.Minute
						if test.reused {
							elapsed = 0
						}
						require.Equal(t, agent.CodeReviewWorkspacePreparationTimeout-elapsed, time.Until(deadline), "create plus clone or restore must consume the original preparation budget")
						return 0, nil
					}
					if strings.Contains(command, "with pathlib.Path(sys.argv[1]).open() as marker:") {
						return 42, nil
					}
					if command == "git rev-parse HEAD" {
						_, err := io.WriteString(stdout, head+"\n")
						return 0, err
					}
					if strings.HasPrefix(command, "git -C ") && strings.Contains(command, "rev-parse --verify HEAD") {
						_, err := io.WriteString(stdout, head+"\n"+branch+"\n"+head+"\n")
						return 0, err
					}
					return 0, nil
				}
				d.adapter.executeFn = func(ctx context.Context, _ *agent.Sandbox, _ *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
					executions.Add(1)
					require.NoError(t, ctx.Err(), "preparation deadline marker must not cancel reviewer execution")
					return &agent.AgentResult{Summary: "Reviewed local inputs."}, nil
				}
				ctx := jobctx.WithJobCreatedAt(context.Background(), started.Add(-time.Hour))
				orchestrator := buildOrchestrator(d)
				var err error
				if test.initial {
					err = orchestrator.RunAgent(ctx, session)
				} else {
					err = orchestrator.ContinueSession(ctx, session, opts)
				}
				require.NoError(t, err, "a legitimately fresh setup must succeed after queue delay")
				require.Positive(t, preparations.Load(), "every code review path must prepare or verify its immutable bundle")
				require.Equal(t, int64(1), executions.Load(), "each runtime path must execute its reviewer exactly once")
			})
		})
	}
}
