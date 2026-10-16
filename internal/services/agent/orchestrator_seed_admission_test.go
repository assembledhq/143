package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type initialSeedAdmissionInbox struct {
	agent.ThreadInboxStore
	failure bool
	calls   int
}

func (s *initialSeedAdmissionInbox) LastAcknowledgedSequence(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}
func (s *initialSeedAdmissionInbox) MarkDeliveringForMessages(_ context.Context, _, _, _, _ uuid.UUID, _ string, ids []int64) (int64, error) {
	return int64(len(ids)), nil
}
func (s *initialSeedAdmissionInbox) MarkAckedForSeedMessages(_ context.Context, _, _, _, _ uuid.UUID, ids []int64) (int64, error) {
	s.calls++
	if s.failure {
		return 0, errors.New("seed acknowledgment unavailable")
	}
	return int64(len(ids)), nil
}
func (s *initialSeedAdmissionInbox) CountPendingByThread(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}

func TestRunAgent_InitialSeedAdmissionPrecedesProvider(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		failure bool
	}{{name: "admitted initial seed"}, {name: "failed initial seed acknowledgment", failure: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := testOrg()
			issue := testIssue(orgID)
			run := testRun(orgID, issue.ID)
			threadID := uuid.New()
			run.PrimaryThreadID = &threadID
			deps := defaultDeps()
			require.NoError(t, deps.messages.Create(context.Background(), &models.SessionMessage{OrgID: orgID, SessionID: run.ID, ThreadID: &threadID, Role: models.MessageRoleUser, TurnNumber: 1, Content: "Initial instruction"}), "seed initial user input before run_agent")
			inbox := &initialSeedAdmissionInbox{failure: tt.failure}
			deps.threadInbox = inbox
			deps.threadRuntimes = &reviewSetupLifecycleRuntimeStore{}
			executions := 0
			deps.adapter.executeFn = func(_ context.Context, _ *agent.Sandbox, _ *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions++
				require.Equal(t, 1, inbox.calls, "initial seed must have durable admission before provider execution")
				return &agent.AgentResult{ExitCode: 0, Summary: "Initial instruction complete"}, nil
			}
			err := buildOrchestrator(deps).RunAgent(context.Background(), run)
			if tt.failure {
				require.ErrorContains(t, err, "seed acknowledgment unavailable", "initial ack failure must stop before provider acceptance")
				require.Equal(t, 0, executions, "failed seed admission cannot launch initial provider")
			} else {
				require.NoError(t, err, "admitted initial seed should execute normally")
				require.Equal(t, 1, executions, "initial provider should execute once after admission")
			}
		})
	}
}

func (s *initialSeedAdmissionInbox) ListAppliedMessageIDs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []int64) ([]int64, error) {
	return nil, nil
}

func (s *initialSeedAdmissionInbox) ListUnsettledAppliedMessageIDs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []int64) ([]int64, error) {
	return nil, nil
}

func (s *initialSeedAdmissionInbox) InspectContinuationRecovery(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, int64) (bool, bool, error) {
	return false, false, nil
}

func (s *initialSeedAdmissionInbox) ListAppliedRecoveryMessageIDs(_ context.Context, _, _, _ uuid.UUID, id int64, _ []int64) ([]int64, error) {
	return []int64{id}, nil
}
