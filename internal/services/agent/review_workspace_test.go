package agent

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
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
	if strings.Contains(command, "rev-parse HEAD") {
		_, err := io.WriteString(stdout, p.actualHead+"\n")
		return 0, err
	}
	return 0, nil
}

func (p *reviewWorkspaceProvider) Destroy(context.Context, *Sandbox) error {
	p.destroyCount++
	return nil
}

func TestPrepareCodeReviewWorkspaceExactHead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		actualHead  string
		wantErr     bool
		wantDestroy int
	}{
		{name: "ready after one clone and exact checkout", actualHead: "expected-head"},
		{name: "mismatched checkout destroys unpublished container", actualHead: "unexpected-head", wantErr: true, wantDestroy: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &reviewWorkspaceProvider{actualHead: tt.actualHead}
			orchestrator := &Orchestrator{
				provider: provider,
				sandboxCapacity: NewSandboxCapacityGate(SandboxCapacityGateConfig{
					Counter: reviewWorkspaceCapacityCounter{}, MaxActive: 2, Logger: zerolog.Nop(),
				}),
				env: NewAgentEnv(AgentEnvDeps{}),
				repositories: testInternalRepoStore{repo: models.Repository{
					FullName: "test/repo", CloneURL: "https://github.com/test/repo.git", DefaultBranch: "main",
				}},
				github: testInternalGitHubTokens{token: "short-lived-token"},
				logger: zerolog.Nop(),
			}
			repoID, orgID := uuid.New(), uuid.New()
			title := "Review test change"
			session := &models.Session{
				ID: uuid.New(), OrgID: orgID, Origin: models.SessionOriginCodeReview,
				AgentType: models.AgentTypeCodex, RepositoryID: &repoID, Title: &title,
				RevisionContext: []byte(`{"kind":"code_review","github_pr_number":42,"head_sha":"expected-head"}`),
			}
			preparationJobID := uuid.New()
			sandbox, err := orchestrator.PrepareCodeReviewWorkspace(jobctx.WithJobID(context.Background(), preparationJobID), session, "expected-head")
			if tt.wantErr {
				require.Error(t, err, "an unexpected checkout head must reject the prepared workspace")
				require.Nil(t, sandbox, "a failed checkout must not transfer an unpublished sandbox")
			} else {
				require.NoError(t, err, "an exact checkout should prepare one reusable workspace")
				require.Equal(t, "prepared-container", sandbox.ID, "the prepared sandbox should be returned for fenced publication")
			}
			require.Equal(t, 1, provider.createCount, "workspace preparation should create one sandbox")
			require.Equal(t, preparationJobID.String(), provider.createdConfig.PreparationJobID, "created sandbox should identify the exact preparation lease for GC")
			require.Equal(t, 1, provider.cloneCount, "workspace preparation should clone the repository once")
			require.Equal(t, tt.wantDestroy, provider.destroyCount, "only failed preparation should destroy its unpublished container")
			require.Contains(t, strings.Join(provider.commands, "\n"), "pull/42/head", "preparation should fetch the captured pull request head")
		})
	}
}
