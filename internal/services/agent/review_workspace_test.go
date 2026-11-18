package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
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
	managedClones int
	cloneExit     int
	cloneCancel   context.CancelFunc
	cleanupCount  int
	destroyErr    error
	destroyCount  int
	commands      []string
	actualHead    string
	createdConfig SandboxConfig
}

func (*reviewWorkspaceProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	return nil, os.ErrNotExist
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

func (p *reviewWorkspaceProvider) ExecWithStdin(_ context.Context, _ *Sandbox, _ string, input io.Reader, _, _ io.Writer) (int, error) {
	var message struct {
		Payload reviewWorkspacePreparationPayload `json:"payload"`
	}
	if err := json.NewDecoder(input).Decode(&message); err != nil {
		return -1, err
	}
	if message.Payload.CloneDestination != "" {
		p.managedClones++
		if p.cloneCancel != nil {
			p.cloneCancel()
		}
		return p.cloneExit, nil
	}
	return 0, nil
}

func (p *reviewWorkspaceProvider) Exec(_ context.Context, _ *Sandbox, command string, stdout, _ io.Writer) (int, error) {
	p.commands = append(p.commands, command)
	if strings.Contains(command, "valid = False") {
		p.cleanupCount++
	}
	if strings.Contains(command, "rev-parse HEAD") {
		_, err := io.WriteString(stdout, p.actualHead+"\n")
		return 0, err
	}
	return 0, nil
}

func (p *reviewWorkspaceProvider) Destroy(ctx context.Context, _ *Sandbox) error {
	p.destroyCount++
	p.destroyErr = ctx.Err()
	return nil
}

func TestPrepareCodeReviewWorkspaceExactHead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		actualHead  string
		wantErr     bool
		wantDestroy int
		cloneExit   int
		cancelClone bool
	}{
		{name: "ready after one clone and exact checkout", actualHead: strings.Repeat("a", 40)},
		{name: "mismatched checkout destroys unpublished container", actualHead: "unexpected-head", wantErr: true, wantDestroy: 1},
		{name: "failed managed clone destroys unpublished container", cloneExit: 1, wantErr: true, wantDestroy: 1},
		{name: "cancelled managed clone destroys unpublished container", cancelClone: true, wantErr: true, wantDestroy: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := &reviewWorkspaceProvider{actualHead: tt.actualHead, cloneExit: tt.cloneExit}
			if tt.cancelClone {
				provider.cloneCancel = cancel
			}
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
				logger: zerolog.New(&logs),
			}
			repoID, orgID := uuid.New(), uuid.New()
			title := "Review test change"
			session := &models.Session{
				ID: uuid.New(), OrgID: orgID, Origin: models.SessionOriginCodeReview,
				AgentType: models.AgentTypeCodex, RepositoryID: &repoID, Title: &title,
				RevisionContext: []byte(`{"kind":"code_review","github_repo":"test/repo","base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","github_pr_number":42,"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
			}
			preparationLeaseToken := uuid.New()
			sandbox, err := orchestrator.PrepareCodeReviewWorkspace(jobctx.WithLockToken(ctx, preparationLeaseToken), session, strings.Repeat("a", 40))
			if tt.wantErr {
				require.Error(t, err, "failed clone or checkout must reject the prepared workspace")
				if tt.cancelClone {
					require.ErrorIs(t, err, context.Canceled, "managed clone cancellation must retain its cause")
				}
				require.Nil(t, sandbox, "a failed checkout must not transfer an unpublished sandbox")
			} else {
				require.NoError(t, err, "an exact checkout should prepare one reusable workspace")
				require.Equal(t, "prepared-container", sandbox.ID, "the prepared sandbox should be returned for fenced publication")
			}
			require.Equal(t, 2, strings.Count(logs.String(), `"stage":"review_workspace_setup"`), "setup must emit paired boundaries before container creation")
			require.Contains(t, logs.String(), `"stage":"review_workspace_setup","outcome":"succeeded"`, "completed setup must retain its outcome even when later checkout fails")
			require.Equal(t, 1, provider.createCount, "workspace preparation should create one sandbox")
			require.Equal(t, preparationLeaseToken.String(), provider.createdConfig.PreparationLeaseToken, "created sandbox should identify the exact preparation attempt for GC")
			require.Equal(t, 0, provider.cloneCount, "cold review preparation must avoid generic provider cloning")
			require.Equal(t, 1, provider.managedClones, "cold review preparation must clone through private bounded input once")
			wantCleanups := 1
			if !tt.wantErr {
				wantCleanups++ // Successful input preparation also confirms its managed process.
			}
			require.Equal(t, wantCleanups, provider.cleanupCount, "every managed preparation must confirm process cleanup")
			require.Equal(t, tt.wantDestroy, provider.destroyCount, "only failed preparation should destroy its unpublished container")
			require.NoError(t, provider.destroyErr, "failed preparation must destroy its container with an uncancelled cleanup context")
			commands := strings.Join(provider.commands, "\n")
			if tt.cloneExit != 0 || tt.cancelClone {
				require.NotContains(t, commands, "git checkout", "unsuccessful clone must never reach checkout or transfer ownership")
			} else {
				require.Contains(t, commands, strings.Repeat("a", 40), "preparation should check out the exact captured pull request head")
			}
		})
	}
}
