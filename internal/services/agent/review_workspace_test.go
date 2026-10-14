package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/reviewpreparation"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewWorkspaceCapacityCounter struct{}

func (reviewWorkspaceCapacityCounter) CountLiveSandboxes(context.Context) (int, error) { return 0, nil }

type reviewWorkspaceProvider struct {
	SandboxProvider
	createCount   int
	cloneCount    int
	destroyCount  int
	commands      []string
	actualHead    string
	createdConfig SandboxConfig
	failEvidence  bool
}

func (p *reviewWorkspaceProvider) Create(_ context.Context, cfg SandboxConfig) (*Sandbox, error) {
	p.createCount++
	p.createdConfig = cfg
	return &Sandbox{ID: "prepared-container", Provider: "test", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
}

func (p *reviewWorkspaceProvider) CloneRepo(context.Context, *Sandbox, string, string, string) error {
	p.cloneCount++
	return nil
}

func (p *reviewWorkspaceProvider) Exec(_ context.Context, _ *Sandbox, command string, stdout, _ io.Writer) (int, error) {
	p.commands = append(p.commands, command)
	if p.failEvidence && strings.Contains(command, `"receipt":{`) {
		return 1, nil
	}
	if command == "/usr/bin/git -c advice.graftFileDeprecated=false -c core.hooksPath=/dev/null rev-parse FETCH_HEAD" || strings.Contains(command, "rev-parse HEAD") {
		_, err := io.WriteString(stdout, p.actualHead+"\n")
		return 0, err
	}
	return 0, nil
}

func (p *reviewWorkspaceProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	input, err := io.ReadAll(stdin)
	if err != nil {
		return -1, err
	}
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return -1, err
	}
	command += " " + string(envelope.Payload)
	if strings.Contains(command, "clone --template=/dev/null --no-checkout") {
		p.cloneCount++
	}
	return p.Exec(ctx, sandbox, command, stdout, stderr)
}

func (p *reviewWorkspaceProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	return nil, nil
}

func (p *reviewWorkspaceProvider) Destroy(context.Context, *Sandbox) error {
	p.destroyCount++
	return nil
}

func TestPrepareCodeReviewWorkspaceExactHead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		actualHead   string
		wantErr      bool
		wantDestroy  int
		failEvidence bool
	}{
		{name: "ready after one clone and exact checkout", actualHead: strings.Repeat("a", 40)},
		{name: "mismatched checkout destroys unpublished container", actualHead: "unexpected-head", wantErr: true, wantDestroy: 1},
		{name: "incomplete evidence destroys unpublished container", actualHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", failEvidence: true, wantErr: true, wantDestroy: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &reviewWorkspaceProvider{actualHead: tt.actualHead, failEvidence: tt.failEvidence}
			repoID, orgID := uuid.New(), uuid.New()
			policy, err := reviewpreparation.NewPolicy(true, []string{"test/repo"})
			require.NoError(t, err, "explicit test repository activation must construct")
			orchestrator := &Orchestrator{
				reviewPreparationPolicy: policy,
				provider:                provider,
				sandboxCapacity: NewSandboxCapacityGate(SandboxCapacityGateConfig{
					Counter: reviewWorkspaceCapacityCounter{}, MaxActive: 2, Logger: zerolog.Nop(),
				}),
				env: NewAgentEnv(AgentEnvDeps{}),
				repositories: testInternalRepoStore{repo: models.Repository{
					ID: repoID, OrgID: orgID, FullName: "test/repo", CloneURL: "https://github.com/test/repo.git", DefaultBranch: "main",
				}},
				github: testInternalGitHubTokens{token: "short-lived-token"},
				logger: zerolog.Nop(),
			}
			title := "Review test change"
			session := &models.Session{
				ID: uuid.New(), OrgID: orgID, Origin: models.SessionOriginCodeReview,
				AgentType: models.AgentTypeCodex, RepositoryID: &repoID, Title: &title,
				RevisionContext: []byte(`{"kind":"code_review","github_repo":"test/repo","base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","github_pr_number":42,"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
			}
			preparationLeaseToken := uuid.New()
			sandbox, err := orchestrator.PrepareCodeReviewWorkspace(jobctx.WithLockToken(context.Background(), preparationLeaseToken), session, strings.Repeat("a", 40))
			if tt.wantErr {
				require.Error(t, err, "an unexpected checkout head must reject the prepared workspace")
				require.Nil(t, sandbox, "a failed checkout must not transfer an unpublished sandbox")
			} else {
				require.NoError(t, err, "an exact checkout should prepare one reusable workspace")
				require.Equal(t, "prepared-container", sandbox.ID, "the prepared sandbox should be returned for fenced publication")
			}
			require.Equal(t, 1, provider.createCount, "workspace preparation should create one sandbox")
			require.Equal(t, preparationLeaseToken.String(), provider.createdConfig.PreparationLeaseToken, "created sandbox should identify the exact preparation attempt for GC")
			require.Equal(t, 1, provider.cloneCount, "workspace preparation should clone the repository once")
			require.Equal(t, tt.wantDestroy, provider.destroyCount, "only failed preparation should destroy its unpublished container")
			require.Contains(t, strings.Join(provider.commands, "\n"), strings.Repeat("a", 40), "preparation should check out the exact captured pull request head")
		})
	}
}
