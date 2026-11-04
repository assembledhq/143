package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// The provider runs real Git and the production preparation wrapper locally.
// Only container lifecycle and the sandbox-only auth bootstrap are simulated.
type coldReviewCloneProvider struct {
	localReviewEvidenceProvider
	home, destination        string
	cloneEntries             []string
	cloneIndexAbsent         bool
	cloneMissingObjects      string
	bootstrapCount           int
	genericClones, destroys  int
	hydrationAuthentications []reviewGitAuthentication
}

func (p *coldReviewCloneProvider) Create(context.Context, SandboxConfig) (*Sandbox, error) {
	return &Sandbox{ID: "cold-review", WorkDir: p.destination, HomeDir: p.home}, nil
}

func (p *coldReviewCloneProvider) Destroy(context.Context, *Sandbox) error {
	p.destroys++
	return nil
}

func (p *coldReviewCloneProvider) CloneRepo(context.Context, *Sandbox, string, string, string) error {
	p.genericClones++
	return fmt.Errorf("cold review unexpectedly used generic clone")
}

func (p *coldReviewCloneProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	if strings.HasPrefix(command, "143-tools git-bootstrap ") {
		p.bootstrapCount++
		return 0, nil
	}
	return p.localReviewEvidenceProvider.Exec(ctx, sandbox, command, stdout, stderr)
}

func (p *coldReviewCloneProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	encoded, err := io.ReadAll(input)
	if err != nil {
		return -1, err
	}
	var message struct {
		Payload        reviewWorkspacePreparationPayload `json:"payload"`
		Authentication reviewGitAuthentication           `json:"authentication"`
	}
	if err := json.Unmarshal(encoded, &message); err != nil {
		return -1, err
	}
	if message.Payload.Standalone && message.Payload.PlatformCommand && message.Payload.AuthenticateGit {
		p.hydrationAuthentications = append(p.hydrationAuthentications, message.Authentication)
	}
	code, err := p.localReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, command, bytes.NewReader(encoded), stdout, stderr)
	if code != 0 || err != nil || message.Payload.CloneDestination == "" {
		return code, err
	}
	entries, err := os.ReadDir(p.destination)
	if err != nil {
		return -1, err
	}
	for _, entry := range entries {
		p.cloneEntries = append(p.cloneEntries, entry.Name())
	}
	_, indexErr := os.Stat(filepath.Join(p.destination, ".git/index"))
	p.cloneIndexAbsent = os.IsNotExist(indexErr)
	probe := exec.CommandContext(ctx, "/usr/bin/git", "-C", p.destination, "rev-list", "--objects", "--missing=print", "HEAD")
	output, err := probe.CombinedOutput()
	p.cloneMissingObjects = string(output)
	return code, err
}

func TestPrepareCodeReviewWorkspaceColdCloneMaterializesOnlyCapturedHead(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	// Advance the target after capture, so neither the current target tree nor
	// a fresh base calculation can substitute for the captured review inputs.
	require.NoError(t, os.WriteFile(filepath.Join(f.source, "target-tip-only.go"), []byte("new target tip only\n"), 0o600), "advanced target fixture should contain distinct source")
	reviewInputsGit(t, f.source, "add", ".")
	reviewInputsGit(t, f.source, "commit", "-qm", "advance target after capture")
	targetTip := reviewInputsGit(t, f.source, "rev-parse", "HEAD")
	targetBlob := reviewInputsGit(t, f.source, "rev-parse", "HEAD:target-tip-only.go")
	reviewInputsGit(t, f.source, "push", "-q", f.remote, "main")
	require.NotEqual(t, f.head, targetTip, "initial clone target must differ from the captured PR head")
	expectedPatch, err := exec.Command("/usr/bin/git", "-C", f.source, "diff", "--binary", "--no-ext-diff", "--no-textconv", f.base+"..."+f.head, "--").CombinedOutput()
	require.NoError(t, err, "independent Git comparison must capture the expected PR diff")
	mergeBase := reviewInputsGit(t, f.source, "merge-base", f.base, f.head)

	home := t.TempDir()
	destination := filepath.Join(home, "cold review checkout")
	reviewInputsWritableCleanup(t, home)
	provider := &coldReviewCloneProvider{
		localReviewEvidenceProvider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: t.Logf},
		home:                        home, destination: destination,
	}
	var logs bytes.Buffer
	tokens := &reviewConfigGitHubTokens{token: "synthetic-clone-token"}
	tokens.onToken = func() {
		if tokens.calls > 1 {
			tokens.token = "synthetic-hydration-token"
		}
	}
	o := &Orchestrator{
		provider: provider, sandboxCapacity: NewSandboxCapacityGate(SandboxCapacityGateConfig{
			Counter: reviewWorkspaceCapacityCounter{}, MaxActive: 2, Logger: zerolog.Nop(),
		}),
		env:          NewAgentEnv(AgentEnvDeps{}),
		repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo", CloneURL: f.remote, DefaultBranch: "main"}},
		github:       tokens, logger: zerolog.New(&logs),
	}
	session := reviewInputsSession(t, f.base, f.head)
	branch := "review/captured-head"
	session.WorkingBranch = &branch
	sandbox, err := o.PrepareCodeReviewWorkspace(jobctx.WithLockToken(context.Background(), uuid.New()), session, f.head)
	require.NoError(t, err, "real cold preparation must finish checkout and immutable input preparation")
	require.Equal(t, []string{"review_inputs_hydrate"}, reviewInputAttemptStages(t, logs.Bytes()), "new unpublished clones must prepare their inputs in one hydration-capable pass")
	require.Equal(t, 2, tokens.calls, "hydration must resolve current credentials after clone and checkout")
	require.Equal(t, []reviewGitAuthentication{{RepoURL: f.remote, Token: "synthetic-hydration-token"}}, provider.hydrationAuthentications, "cold hydration must use the refreshed token instead of the earlier clone token")
	require.Equal(t, "cold-review", sandbox.ID, "successful cold preparation should transfer its unpublished sandbox")
	require.Equal(t, 0, provider.genericClones, "cold reviews must use the bounded clone transport")
	require.Equal(t, []string{".git"}, provider.cloneEntries, "clone must not materialize target-branch worktree files")
	require.True(t, provider.cloneIndexAbsent, "clone must not build an initial target-branch index")
	require.Contains(t, provider.cloneMissingObjects, "?"+targetBlob, "clone must leave target-only blobs promised instead of downloading them for an unused checkout")
	require.Equal(t, 1, provider.bootstrapCount, "cold preparation must retain Git authentication bootstrap")
	require.Equal(t, 0, provider.destroys, "successful preparation must leave destruction to its new owner")
	require.Equal(t, f.head, reviewInputsGit(t, destination, "rev-parse", "HEAD"), "the final workspace must match the captured PR head")
	require.Equal(t, branch, reviewInputsGit(t, destination, "branch", "--show-current"), "the captured checkout must retain its session working branch")
	changed, err := os.ReadFile(filepath.Join(destination, "changed.go"))
	require.NoError(t, err, "captured head source must be materialized")
	require.Equal(t, "PR head implementation\n", string(changed), "the worktree must contain captured PR source")
	_, err = os.Stat(filepath.Join(destination, "target-tip-only.go"))
	require.ErrorIs(t, err, os.ErrNotExist, "the unused target tip must never appear in the final worktree")

	manifestPath, patchPath := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
	manifest, err := os.ReadFile(filepath.Join(destination, manifestPath))
	require.NoError(t, err, "cold preparation must produce immutable review identity")
	var actualManifest map[string]any
	require.NoError(t, json.Unmarshal(manifest, &actualManifest), "immutable manifest must contain valid JSON")
	expectedManifest := map[string]any{
		"version": float64(1), "org_id": session.OrgID.String(), "session_id": session.ID.String(),
		"repository_id": session.RepositoryID.String(), "repository": "example/repo",
		"base_sha": f.base, "head_sha": f.head, "merge_base_sha": mergeBase,
		"patch_sha256": fmt.Sprintf("%x", sha256.Sum256(expectedPatch)),
	}
	require.Equal(t, expectedManifest, actualManifest, "manifest must retain exact captured identity, merge base, and patch digest")
	patch, err := os.ReadFile(filepath.Join(destination, patchPath))
	require.NoError(t, err, "cold preparation must produce immutable review diff")
	require.Equal(t, expectedPatch, patch, "immutable diff must exactly match the independently captured PR comparison")
	require.NoError(t, os.Rename(strings.TrimPrefix(f.remote, "file://"), strings.TrimPrefix(f.remote, "file://")+"-offline"), "remote must be unavailable for final source-completeness verification")
	require.NoError(t, o.execReviewEvidence(context.Background(), sandbox, reviewTreeEvidenceScript(destination, f.base, f.head, ""), ""), "captured base, head, and merge-base source closures must be complete offline")
	logs.Reset()
	require.NoError(t, o.ensureCodeReviewInputs(context.Background(), session, sandbox), "final captured inputs must verify and remain reusable offline")
	require.Equal(t, []string{"review_inputs_verify"}, reviewInputAttemptStages(t, logs.Bytes()), "reused workspaces must retain complete warm verification without preparation or hydration")
	require.Equal(t, 2, tokens.calls, "warm reuse must not acquire an installation token")
}
