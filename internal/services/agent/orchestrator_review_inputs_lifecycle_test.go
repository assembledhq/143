package agent_test

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

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
