package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewInputsProvider struct {
	SandboxProvider
	calls    atomic.Int64
	destroys atomic.Int64
	shell    string
}

func (p *reviewInputsProvider) IsAlive(context.Context, *Sandbox) (bool, error) { return true, nil }

func (p *reviewInputsProvider) Destroy(context.Context, *Sandbox) error {
	p.destroys.Add(1)
	return nil
}

type reviewInputsRecoverySessions struct {
	SessionStore
	recovered bool
	params    []db.RecoverIncompleteCodeReviewWorkspaceParams
	errors    []error
}

func (s *reviewInputsRecoverySessions) RecoverIncompleteCodeReviewWorkspace(_ context.Context, _ uuid.UUID, params db.RecoverIncompleteCodeReviewWorkspaceParams) (bool, error) {
	s.params = append(s.params, params)
	if len(s.errors) >= len(s.params) && s.errors[len(s.params)-1] != nil {
		return false, s.errors[len(s.params)-1]
	}
	return s.recovered, nil
}

func reviewPreparationContext(jobID, token uuid.UUID) context.Context {
	return jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), token)
}

func (p *reviewInputsProvider) ReadFile(_ context.Context, _ *Sandbox, name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (p *reviewInputsProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	p.calls.Add(1)
	shell := p.shell
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Dir = sandbox.WorkDir
	cmd.Env = os.Environ()
	for key, value := range sandbox.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

func TestSharedReviewInputsSurviveBootstrapReviewerEditsAndSynthesis(t *testing.T) {
	t.Parallel()
	directory, base, head, _ := newReviewInputsGitFixture(t)
	configPath := filepath.Join(directory, repoconfig.ConfigPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700), "repository config directory should exist")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"bootstrap":{"commands":["printf 'generated\\n' > bootstrap-output.txt"]}}`), 0o600), "full repository bootstrap should create its real output")
	session := reviewInputsSession(t, base, head)
	provider, tokens := &reviewInputsProvider{}, &reviewInputsTokenSpy{}
	sandbox := &Sandbox{ID: "shared-container", WorkDir: directory}
	orchestrator := &Orchestrator{provider: provider, github: tokens, repositories: testInternalRepoStore{repo: models.Repository{FullName: "renamed/repository"}}}
	require.NoError(t, prepareSandboxRepositoryForSession(context.Background(), provider, session, sandbox, directory, repositoryPreparationFull, zerolog.Nop()), "initial RunAgent preparation should allow generated bootstrap output")
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "initial review inputs should depend on captured commits")
	manifest, patch := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", base, head)
	manifestBefore, err := os.ReadFile(filepath.Join(directory, manifest))
	require.NoError(t, err, "captured manifest should be present")
	patchBefore, err := os.ReadFile(filepath.Join(directory, patch))
	require.NoError(t, err, "captured patch should be present")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "change.txt"), []byte("reviewer workspace edit\n"), 0o600), "a reviewer can leave a tracked policy violation")
	reviewInputsGit(t, directory, "add", "change.txt")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "reviewer-note.txt"), []byte("reviewer workspace file\n"), 0o600), "a reviewer can leave an untracked policy violation")
	for _, mode := range []repositoryPreparationMode{repositoryPreparationMinimalReview, repositoryPreparationFull} {
		require.NoError(t, prepareSandboxRepositoryForSession(context.Background(), provider, session, sandbox, directory, mode, zerolog.Nop()), "later reviewer and synthesis preparation should complete")
		require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "warm ContinueSession evidence must survive workspace edits")
	}
	manifestAfter, err := os.ReadFile(filepath.Join(directory, manifest))
	require.NoError(t, err, "warm review manifest should remain readable")
	patchAfter, err := os.ReadFile(filepath.Join(directory, patch))
	require.NoError(t, err, "warm review patch should remain readable")
	require.Equal(t, manifestBefore, manifestAfter, "workspace policy violations must not change captured identity")
	require.Equal(t, patchBefore, patchAfter, "reviewers and synthesis should consume the same commit-to-commit patch")
	require.NotContains(t, string(patchAfter), "reviewer workspace", "prepared inputs must exclude reviewer edits")
	require.Equal(t, int64(0), tokens.calls.Load(), "all warm reviewers must reuse local evidence without authentication")
}

func TestReviewFetchCredentialsSurviveDashAndReachGit(t *testing.T) {
	t.Parallel()
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Skip("dash is required for the Ubuntu shell credential regression")
	}
	origin, base, head, _ := newReviewInputsGitFixture(t)
	root := t.TempDir()
	output, err := exec.Command("git", "clone", "-q", "--bare", origin, filepath.Join(root, "repo.git")).CombinedOutput()
	require.NoError(t, err, "local authenticated Git remote should exist: %s", output)
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err, "Git HTTP backend should be available")
	execPath := reviewInputsGit(t, origin, "--exec-path")
	backend := &cgi.Handler{Path: filepath.Join(execPath, "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "PATH=" + filepath.Dir(gitPath) + ":" + os.Getenv("PATH")}}
	var authenticated atomic.Int64
	const token = "synthetic-dash-fetch-token"
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		user, password, ok := request.BasicAuth()
		if !ok || user != "x-access-token" || password != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="review-fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		backend.ServeHTTP(w, request)
	}))
	t.Cleanup(remote.Close)
	directory := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.Mkdir(directory, 0o700), "empty RunAgent source checkout should exist")
	reviewInputsWritableCleanup(t, directory)
	reviewInputsGit(t, directory, "init", "-qb", "main")
	provider := &reviewInputsProvider{shell: dash}
	sandbox := &Sandbox{ID: "source", WorkDir: directory, Env: map[string]string{"GIT_SSL_NO_VERIFY": "true"}}
	orchestrator := &Orchestrator{provider: provider, github: testInternalGitHubTokens{token: token}, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo", CloneURL: remote.URL + "/repo.git"}}}
	require.NoError(t, orchestrator.checkoutCodeReviewHead(context.Background(), sandbox, remote.URL+"/repo.git", token, 42, "review", head), "RunAgent must fetch its exact head before credential bootstrap under dash")
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), reviewInputsSession(t, base, head), sandbox), "missing exact base must use authenticated child Git fetch under dash")
	require.Greater(t, authenticated.Load(), int64(0), "the real Git HTTP backend must receive the short-lived token")
	require.NotContains(t, reviewInputsGit(t, directory, "config", "--list"), token, "Git config must never retain the temporary token")
	require.NotContains(t, sandbox.Env, "GH143_REVIEW_FETCH_TOKEN", "shared sandbox state must not retain the temporary token")
}

type reviewInputsTokenSpy struct{ calls atomic.Int64 }

func (s *reviewInputsTokenSpy) GetInstallationToken(context.Context, int64) (string, error) {
	s.calls.Add(1)
	return "", nil
}

type reviewInputsAuthProvider struct {
	SandboxProvider
	commands []string
	tokens   []string
}

func (p *reviewInputsAuthProvider) Exec(_ context.Context, sandbox *Sandbox, command string, _, _ io.Writer) (int, error) {
	p.commands = append(p.commands, command)
	p.tokens = append(p.tokens, sandbox.Env["GH143_REVIEW_FETCH_TOKEN"])
	if len(p.commands) == 1 {
		return 42, nil
	}
	return 0, nil
}

func TestSharedReviewInputsKeepFetchCredentialsEphemeral(t *testing.T) {
	t.Parallel()
	provider := &reviewInputsAuthProvider{}
	session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
	orchestrator := &Orchestrator{
		provider:     provider,
		github:       testInternalGitHubTokens{token: "synthetic-fetch-token"},
		repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo", CloneURL: "https://github.com/example/repo.git"}},
	}
	sandbox := &Sandbox{ID: "container", WorkDir: "/workspace", Env: map[string]string{"KEEP": "original"}}
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "coordinator should authenticate only the missing-input fetch")
	require.Equal(t, []string{"", "synthetic-fetch-token"}, provider.tokens, "only the remote preparation process should receive the short-lived token")
	require.Equal(t, map[string]string{"KEEP": "original"}, sandbox.Env, "shared sandbox state must not retain temporary fetch credentials")
	require.NotContains(t, strings.Join(provider.commands, "\n"), "synthetic-fetch-token", "command logging must never contain fetch credentials")
}

func newReviewInputsGitFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "review's source")
	require.NoError(t, os.Mkdir(directory, 0o700), "isolated source directory should exist")
	reviewInputsWritableCleanup(t, directory)
	git := func(args ...string) string { return reviewInputsGit(t, directory, args...) }
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Review fixture")
	git("config", "user.email", "review@example.test")
	git("config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "change.txt"), []byte("before\n"), 0o600), "common source should be written")
	git("add", ".")
	git("commit", "-qm", "common base")
	mergeBase := git("rev-parse", "HEAD")
	git("checkout", "-qb", "review")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "change.txt"), []byte("after\n"), 0o600), "review change should be written")
	git("commit", "-qam", "review change")
	head := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "unrelated.txt"), []byte("base branch advance\n"), 0o600), "base-only source should be written")
	git("add", ".")
	git("commit", "-qm", "advance base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-q", "review")
	return directory, base, head, mergeBase
}

func reviewInputsWritableCleanup(t *testing.T, directory string) {
	t.Helper()
	t.Cleanup(func() {
		err := filepath.Walk(directory, func(name string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return os.Chmod(name, 0o700)
			}
			return nil
		})
		require.NoError(t, err, "read-only fixture directories should permit temporary cleanup")
	})
}

func reviewInputsGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git fixture command should succeed: %s", output)
	return strings.TrimSpace(string(output))
}

func reviewInputsSession(t *testing.T, base, head string) *models.Session {
	t.Helper()
	repositoryID := uuid.New()
	context, err := json.Marshal(codeReviewCheckoutContext{Kind: "code_review", GitHubRepo: "example/repo", GitHubPRNumber: 42, BaseSHA: base, HeadSHA: head})
	require.NoError(t, err, "captured review context should encode")
	return &models.Session{ID: uuid.New(), OrgID: uuid.New(), RepositoryID: &repositoryID, Origin: models.SessionOriginCodeReview, BaseCommitSHA: &head, RevisionContext: context}
}

func TestSharedReviewInputsCompleteAndImmutable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		shallow bool
	}{
		{name: "local captured commits"},
		{name: "missing base and history fetched once", shallow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			origin, base, head, mergeBase := newReviewInputsGitFixture(t)
			directory := origin
			if tt.shallow {
				directory = filepath.Join(t.TempDir(), "shallow")
				reviewInputsWritableCleanup(t, directory)
				output, err := exec.Command("git", "clone", "-q", "--depth=1", "--branch=review", "file://"+origin, directory).CombinedOutput()
				require.NoError(t, err, "shallow review fixture should clone: %s", output)
			}
			session := reviewInputsSession(t, base, head)
			provider, tokens := &reviewInputsProvider{}, &reviewInputsTokenSpy{}
			orchestrator := &Orchestrator{provider: provider, github: tokens, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo", CloneURL: origin}}}
			sandbox := &Sandbox{ID: "source-container", WorkDir: directory}
			require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "coordinator should prepare the complete captured diff")
			identity, err := codeReviewInputIdentityFromSession(session)
			require.NoError(t, err, "captured revision identity should validate")
			manifestPath, patchPath := CodeReviewInputPaths(identity.OrgID, identity.SessionID, identity.RepositoryID, identity.Repository, base, head)
			manifestBytes, err := os.ReadFile(filepath.Join(directory, manifestPath))
			require.NoError(t, err, "atomic bundle should publish a manifest")
			patch, err := os.ReadFile(filepath.Join(directory, patchPath))
			require.NoError(t, err, "atomic bundle should publish the complete patch")
			var manifest map[string]any
			require.NoError(t, json.Unmarshal(manifestBytes, &manifest), "manifest should be JSON")
			require.Equal(t, map[string]any{
				"version": float64(1), "org_id": session.OrgID.String(), "session_id": session.ID.String(), "repository_id": session.RepositoryID.String(),
				"repository": "example/repo", "base_sha": base, "head_sha": head, "merge_base_sha": mergeBase, "patch_sha256": fmt.Sprintf("%x", sha256.Sum256(patch)),
			}, manifest, "manifest should pin complete tenant/repository/revision evidence")
			require.Contains(t, string(patch), "-before\n+after", "prepared patch should contain the review change")
			require.NotContains(t, string(patch), "unrelated.txt", "prepared diff should exclude changes unique to the advanced base branch")
			before, err := os.Stat(filepath.Join(directory, manifestPath))
			require.NoError(t, err, "prepared manifest should be present")
			tokenCalls := tokens.calls.Load()
			require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "warm bundle should validate without fetching")
			after, err := os.Stat(filepath.Join(directory, manifestPath))
			require.NoError(t, err, "warm bundle should preserve the manifest")
			require.Equal(t, before.ModTime(), after.ModTime(), "warm validation should never rewrite shared evidence")
			require.Equal(t, tokenCalls, tokens.calls.Load(), "warm reviewers should not acquire a token or fetch GitHub inputs")
			if tt.shallow {
				require.Equal(t, int64(1), tokenCalls, "one coordinator authentication should recover missing history")
			} else {
				require.Equal(t, int64(0), tokenCalls, "complete local commits should require no remote authentication")
			}
		})
	}
}

func TestSharedReviewInputsRejectIncompleteOrModifiedEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit string
	}{
		{name: "missing exact base", edit: "missing-base"},
		{name: "unrelated captured histories", edit: "unrelated"},
		{name: "wrong workspace head", edit: "wrong-head"},
		{name: "modified prepared patch", edit: "patch"},
		{name: "mismatched manifest identity", edit: "manifest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory, base, head, _ := newReviewInputsGitFixture(t)
			session := reviewInputsSession(t, base, head)
			orchestrator := &Orchestrator{provider: &reviewInputsProvider{}, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo"}}}
			sandbox := &Sandbox{ID: "container", WorkDir: directory}
			if tt.edit == "patch" || tt.edit == "manifest" {
				require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "valid fixture should prepare before corruption")
			}
			switch tt.edit {
			case "missing-base":
				session = reviewInputsSession(t, strings.Repeat("1", 40), head)
			case "unrelated":
				reviewInputsGit(t, directory, "checkout", "-q", "--orphan", "unrelated")
				reviewInputsGit(t, directory, "rm", "-qrf", ".")
				reviewInputsGit(t, directory, "commit", "--allow-empty", "-qm", "unrelated root")
				otherBase := reviewInputsGit(t, directory, "rev-parse", "HEAD")
				reviewInputsGit(t, directory, "checkout", "-q", "review")
				session = reviewInputsSession(t, otherBase, head)
			case "wrong-head":
				reviewInputsGit(t, directory, "checkout", "-q", "main")
			case "patch", "manifest":
				manifest, patch := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", base, head)
				target := patch
				if tt.edit == "manifest" {
					target = manifest
				}
				file := filepath.Join(directory, target)
				require.NoError(t, os.Chmod(file, 0o600), "corruption fixture should allow writing")
				require.NoError(t, os.WriteFile(file, []byte("{}"), 0o600), "prepared evidence corruption should be written")
			}
			require.Error(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "incomplete or inconsistent evidence must never start a review")
		})
	}
}

func TestSharedReviewInputIdentityRequiresCapturedRevision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*models.Session)
	}{
		{name: "missing repository", mutate: func(session *models.Session) { session.RepositoryID = nil }},
		{name: "missing revision", mutate: func(session *models.Session) { session.RevisionContext = nil }},
		{name: "missing base", mutate: func(session *models.Session) {
			session.RevisionContext = []byte(`{"kind":"code_review","github_repo":"example/repo","github_pr_number":42,"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		}},
		{name: "abbreviated revision", mutate: func(session *models.Session) {
			session.RevisionContext = []byte(`{"kind":"code_review","github_repo":"example/repo","github_pr_number":42,"base_sha":"abc","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		}},
		{name: "session baseline differs from head", mutate: func(session *models.Session) {
			wrongBaseline := strings.Repeat("b", 40)
			session.BaseCommitSHA = &wrongBaseline
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
			tt.mutate(session)
			provider := &reviewInputsProvider{}
			orchestrator := &Orchestrator{provider: provider}
			require.Error(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, &Sandbox{ID: "container", WorkDir: t.TempDir()}), "incomplete identities should never begin workspace preparation")
			require.Equal(t, int64(0), provider.calls.Load(), "invalid identities should be rejected before any sandbox command")
		})
	}
}

func TestSharedReviewInputsRejectMissingFetchRemote(t *testing.T) {
	t.Parallel()
	session := reviewInputsSession(t, strings.Repeat("b", 40), strings.Repeat("a", 40))
	provider := &reviewInputsAuthProvider{}
	tokens := &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: provider, github: tokens, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo"}}}
	require.ErrorContains(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, &Sandbox{ID: "container", WorkDir: "/workspace"}), "clone URL unavailable", "missing remote inputs must never count as complete preparation")
	require.Equal(t, int64(0), tokens.calls.Load(), "an unavailable repository URL should be rejected before requesting authentication")
}

func TestSharedReviewInputPreparationMarkerLifecycle(t *testing.T) {
	t.Parallel()
	directory, base, head, _ := newReviewInputsGitFixture(t)
	session := reviewInputsSession(t, base, head)
	session.WorkspaceGeneration = 7
	ctx := reviewPreparationContext(uuid.New(), uuid.New())
	sandbox := &Sandbox{ID: "container", WorkDir: directory}
	orchestrator := &Orchestrator{provider: &reviewInputsProvider{}, sessions: &reviewInputsRecoverySessions{}, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo"}}}
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "owner should mark preparation before publishing a container")
	ready, err := orchestrator.codeReviewInputsPreparationFinished(ctx, session, sandbox)
	require.NoError(t, err, "sibling should inspect preparation state")
	require.False(t, ready, "checked-out HEAD alone must not bypass unfinished owner preparation")
	require.Empty(t, reviewInputsGit(t, directory, "ls-files", "--others", "--exclude-standard"), "pre-clone marker must remain outside the repository")
	orchestrator.clearCodeReviewInputPreparation(ctx, session, sandbox)
	ready, err = orchestrator.codeReviewInputsPreparationFinished(ctx, session, sandbox)
	require.NoError(t, err, "cancelled owner marker should be removable")
	require.True(t, ready, "owner cancellation should not leave a permanent readiness wait")
	require.NoError(t, orchestrator.markCodeReviewInputsPreparing(ctx, session, sandbox), "recovery owner should be able to mark a new preparation attempt")
	require.NoError(t, orchestrator.ensureCodeReviewInputs(ctx, session, sandbox), "recovery should publish a complete immutable bundle")
	ready, err = orchestrator.codeReviewInputsPreparationFinished(ctx, session, sandbox)
	require.NoError(t, err, "complete bundle should release the readiness barrier")
	require.True(t, ready, "atomic publication should clear the preparation marker")
}

func TestSharedReviewInputsConcurrentPreparationAndTenantIsolation(t *testing.T) {
	t.Parallel()
	directory, base, head, _ := newReviewInputsGitFixture(t)
	session := reviewInputsSession(t, base, head)
	provider := &reviewInputsProvider{}
	orchestrator := &Orchestrator{provider: provider, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo"}}}
	sandbox := &Sandbox{ID: "shared-container", WorkDir: directory}
	var wait sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err, "concurrent siblings should consume one complete immutable bundle")
	}
	require.Equal(t, int64(1), provider.calls.Load(), "overlapping sibling preparation should execute once per container/revision")
	other := *session
	other.OrgID = uuid.New()
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), &other, sandbox), "another tenant should prepare isolated evidence")
	manifest, _ := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", base, head)
	otherManifest, _ := CodeReviewInputPaths(other.OrgID, other.ID, *other.RepositoryID, "example/repo", base, head)
	require.NotEqual(t, manifest, otherManifest, "tenant identity must separate immutable input bundles")
	entries, err := os.ReadDir(filepath.Join(directory, ".git", "143-review"))
	require.NoError(t, err, "prepared input cache should exist")
	var directories []string
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, entry.Name())
		}
	}
	require.Len(t, directories, 2, "each tenant should have exactly one complete directory and no partial staging artifacts")
}
