package agent_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/reviewpreparation"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type activationReviewRole struct{}

func (activationReviewRole) ResolveAgentRoleForThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.CodeReviewAgentRole, bool, error) {
	return models.CodeReviewAgentRoleReviewer, true, nil
}

func TestReviewPreparationActivationAcrossLifecycles(t *testing.T) {
	t.Parallel()
	states := []struct {
		name      string
		enabled   bool
		names     []string
		nilPolicy bool
		active    bool
	}{
		{name: "default", nilPolicy: true},
		{name: "global off", names: []string{"acme/backend"}},
		{name: "empty allowlist", enabled: true},
		{name: "outside allowlist", enabled: true, names: []string{"other/repository"}},
		{name: "selected", enabled: true, names: []string{"acme/backend"}, active: true},
	}
	lifecycles := []string{"initial", "cold continuation", "restored continuation", "reused continuation", "reused inferred continuation"}
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			t.Parallel()
			for _, lifecycle := range lifecycles {
				t.Run(lifecycle, func(t *testing.T) {
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
					d := defaultDeps()
					configurePinnedReviewFixture(session, &d)
					d.issues.issue = issue
					d.codeReviewRoles = activationReviewRole{}
					if state.nilPolicy {
						d.reviewPreparationPolicy = nil
					} else {
						policy, err := reviewpreparation.NewPolicy(state.enabled, state.names)
						require.NoError(t, err, "test activation configuration must construct")
						d.reviewPreparationPolicy = policy
					}
					d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{ID: threadID, OrgID: orgID, SessionID: session.ID, Status: models.ThreadStatusRunning, ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly}}
					d.messages.messages = []models.SessionMessage{{ID: 1, SessionID: session.ID, OrgID: orgID, ThreadID: &threadID, TurnNumber: 1, Role: models.MessageRoleUser, Source: models.SessionMessageSourceCodeReview, Content: "Review pinned source."}}
					switch lifecycle {
					case "restored continuation":
						session.SnapshotKey = strPtr("review-activation-checkpoint")
						d.snapshots.data = map[string][]byte{*session.SnapshotKey: []byte("snapshot")}
						d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, reader io.Reader) error {
							_, err := io.Copy(io.Discard, reader)
							return err
						}
					case "reused continuation", "reused inferred continuation":
						session.ContainerID = strPtr("review-activation-container")
						session.SandboxState = models.SandboxStateRunning
						d.sessions.peekContainerIDFn = func() (string, error) { return *session.ContainerID, nil }
					}
					d.sessions.getByIDFn = func(uuid.UUID, uuid.UUID) (models.Session, error) { return *session, nil }
					d.provider.ReadFileFn = func(_ context.Context, _ *agent.Sandbox, name string) ([]byte, error) {
						if strings.HasSuffix(name, ".143/config.json") {
							return []byte(`{"bootstrap":{"commands":["activation-dependency-bootstrap"]}}`), nil
						}
						return nil, nil
					}
					previous := d.provider.ExecFn
					bootstrapCalls, evidenceCalls, strictProbes, legacyProbes, reviewerCalls := 0, 0, 0, 0, 0
					d.provider.ExecFn = func(ctx context.Context, sandbox *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
						if strings.Contains(command, "activation-dependency-bootstrap") {
							bootstrapCalls++
						}
						if strings.Contains(command, `"receipt":{`) {
							evidenceCalls++
						}
						if strings.Contains(command, "review readiness requires literal repository metadata") {
							strictProbes++
						}
						if strings.Contains(command, "branch=$(git -C") {
							legacyProbes++
							_, err := io.WriteString(stdout, reviewFixtureHead+"\n143/test/code-review\n"+reviewFixtureHead+"\n")
							return 0, err
						}
						return previous(ctx, sandbox, command, stdout, stderr)
					}
					d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
						reviewerCalls++
						return &agent.AgentResult{Summary: "reviewed"}, nil
					}
					orchestrator := buildOrchestrator(d)
					var err error
					if lifecycle == "initial" {
						err = orchestrator.RunAgent(context.Background(), session)
					} else {
						var opts *agent.ContinueSessionOptions
						if lifecycle != "reused inferred continuation" {
							opts = &agent.ContinueSessionOptions{ThreadID: &threadID}
						}
						err = orchestrator.ContinueSession(context.Background(), session, opts)
					}
					require.NoError(t, err, "known review activation state must preserve a runnable lifecycle")
					require.Equal(t, 1, reviewerCalls, "reviewer must execute once after its applicable workspace setup")
					if state.active {
						require.Equal(t, 1, bootstrapCalls, "selected review must prepare declared dependencies once per workspace")
						require.Positive(t, evidenceCalls, "selected review must require workspace evidence")
					} else {
						if lifecycle != "initial" {
							require.Equal(t, 0, bootstrapCalls, "inactive built-in reviewer continuation must retain minimal setup")
						}
						require.Equal(t, 0, evidenceCalls, "inactive review must not enter mandatory evidence preparation")
					}
					if strings.HasPrefix(lifecycle, "reused") {
						if state.active {
							require.Positive(t, strictProbes, "selected reuse must use bounded metadata readiness")
							require.Equal(t, 0, legacyProbes, "selected reuse must avoid the legacy Git probe")
						} else {
							require.Equal(t, 0, strictProbes, "inactive reuse must preserve legacy probe transport")
							if lifecycle == "reused inferred continuation" {
								require.Equal(t, 0, legacyProbes, "inactive inferred continuation must preserve the original explicit-thread barrier scope")
							} else {
								require.Positive(t, legacyProbes, "inactive explicit-thread reuse must retain the preexisting readiness barrier")
							}
						}
					}
				})
			}
		})
	}
}
