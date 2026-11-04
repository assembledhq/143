package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type targetedReviewFetchFixture struct {
	remote, base, head, mergeBase, targetTip string
	patch                                    []byte
}

func targetedReviewGit(tb testing.TB, directory, input string, args ...string) []byte {
	tb.Helper()
	cmd := exec.Command("/usr/bin/git", append([]string{"-C", directory}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	require.NoError(tb, err, "synthetic git command %v should succeed: %s", args, output)
	return output
}

func newTargetedReviewFetchFixture(tb testing.TB, unrelatedBranches, commitsPerBranch int) targetedReviewFetchFixture {
	tb.Helper()
	directory := tb.TempDir()
	git := func(input string, args ...string) string {
		tb.Helper()
		return strings.TrimSpace(string(targetedReviewGit(tb, directory, input, args...)))
	}
	git("", "init", "-q", "--bare", "-b", "main")
	git("", "config", "uploadpack.allowFilter", "true")
	git("", "config", "uploadpack.allowAnySHA1InWant", "true")
	var history strings.Builder
	addCommit := func(ref string, mark, parent int, name, contents string) {
		fmt.Fprintf(&history, "commit %s\nmark :%d\ncommitter Synthetic Review <review@example.invalid> %d +0000\ndata 9\nsynthetic\n", ref, mark, 1700000000+mark)
		if parent != 0 {
			fmt.Fprintf(&history, "from :%d\n", parent)
		}
		fmt.Fprintf(&history, "M 100644 inline %s\ndata %d\n%s\n\n", name, len(contents), contents)
	}
	addCommit("refs/heads/main", 1, 0, "changed.txt", "common implementation\n")
	addCommit("refs/heads/main", 2, 1, "base-only.txt", "captured base context\n")
	addCommit("refs/pull/42/head", 3, 1, "changed.txt", "captured review implementation\n")
	addCommit("refs/heads/main", 4, 2, "later-target.txt", "target after capture\n")
	next := 5
	for branch := range unrelatedBranches {
		parent := 1
		for commit := range commitsPerBranch {
			addCommit(fmt.Sprintf("refs/heads/unrelated-%03d", branch), next, parent, "unrelated.txt", fmt.Sprintf("branch %d commit %d\n", branch, commit))
			parent = next
			next++
		}
	}
	git(history.String(), "fast-import", "--quiet")
	base := git("", "rev-parse", "main^")
	head := git("", "rev-parse", "refs/pull/42/head")
	return targetedReviewFetchFixture{
		remote: "file://" + directory, base: base, head: head,
		mergeBase: git("", "merge-base", base, head),
		targetTip: git("", "rev-parse", "main"),
		patch:     targetedReviewGit(tb, directory, "", "diff", "--binary", "--no-ext-diff", "--no-textconv", base+"..."+head, "--"),
	}
}

func targetedReviewFetchWorkspace(tb testing.TB, fixture targetedReviewFetchFixture) (*Orchestrator, *Sandbox, *models.Session) {
	tb.Helper()
	home := tb.TempDir()
	tb.Cleanup(func() {
		err := filepath.WalkDir(home, func(name string, entry os.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return err
			}
			return os.Chmod(name, 0o700)
		})
		require.NoError(tb, err, "immutable inputs should permit fixture cleanup")
	})
	sandbox := &Sandbox{ID: uuid.NewString(), HomeDir: home, WorkDir: filepath.Join(home, "checkout"), Env: map[string]string{
		"HOME": home, "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
	}}
	o := &Orchestrator{
		provider:     &managedReviewInputsProvider{localReviewEvidenceProvider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: tb.Logf}},
		repositories: testInternalRepoStore{repo: models.Repository{CloneURL: fixture.remote}},
		github:       testInternalGitHubTokens{}, logger: zerolog.Nop(),
	}
	repoID := uuid.New()
	revision, err := json.Marshal(codeReviewCheckoutContext{Kind: "code_review", GitHubRepo: "example/repo", GitHubPRNumber: 42, BaseSHA: fixture.base, HeadSHA: fixture.head})
	require.NoError(tb, err, "synthetic review identity should encode")
	session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), RepositoryID: &repoID, Origin: models.SessionOriginCodeReview, RevisionContext: revision}
	return o, sandbox, session
}

func initTargetedReviewFetch(ctx context.Context, o *Orchestrator, sandbox *Sandbox, fixture targetedReviewFetchFixture) error {
	quote := func(value string) string { return "'" + shellEscapeSingleQuote(value) + "'" }
	destination := quote(sandbox.WorkDir)
	git := "/usr/bin/git -c core.hooksPath=/dev/null -c credential.helper= -c maintenance.auto=false -c gc.auto=0"
	script := "set -eu\n" +
		git + " init --quiet --template=/dev/null --initial-branch=main " + destination + "\n" +
		"cd " + destination + "\n" +
		git + " config --local remote.origin.url " + quote(fixture.remote) + "\n" +
		reviewGitAuthenticationConfigScript(sandbox.WorkDir, fixture.remote) +
		git + " fetch --quiet --no-tags --no-write-fetch-head --filter=blob:none origin " + quote(fixture.base) + " " + quote(fixture.head)
	ctx, cancel := codeReviewPreparationContext(ctx)
	defer cancel()
	deadline, err := reviewPreparationDeadlineUnix(ctx)
	if err != nil {
		return err
	}
	parent := *sandbox
	parent.WorkDir = sandbox.HomeDir
	payload := reviewWorkspacePreparationPayload{
		WorkDir: parent.WorkDir, Standalone: true, AuthenticateGit: true, CloneDestination: sandbox.WorkDir,
		ExpectedRepoURL: fixture.remote, Prepare: script, DeadlineUnix: deadline,
		CancelPath: filepath.Join(parent.WorkDir, "143-review-cancel-"+uuid.NewString()),
	}
	return o.execReviewPreparationPayload(ctx, &parent, payload, reviewGitAuthentication{fixture.remote, "synthetic-token"}, o.logger)
}

func prepareTargetedReviewFetchWorkspace(ctx context.Context, o *Orchestrator, sandbox *Sandbox, session *models.Session, fixture targetedReviewFetchFixture) error {
	if err := o.checkoutCodeReviewHead(ctx, sandbox, fixture.remote, "", 42, "review/captured", fixture.head); err != nil {
		return err
	}
	return o.prepareColdCodeReviewInputs(ctx, session, sandbox)
}

func verifyTargetedReviewFetchWorkspace(tb testing.TB, o *Orchestrator, sandbox *Sandbox, session *models.Session, fixture targetedReviewFetchFixture) {
	tb.Helper()
	_, patchPath := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", fixture.base, fixture.head)
	patch, err := os.ReadFile(filepath.Join(sandbox.WorkDir, patchPath))
	require.NoError(tb, err, "both paths should write the immutable patch")
	require.Equal(tb, fixture.patch, patch, "immutable patch bytes should match an independent source comparison")
	remote := strings.TrimPrefix(fixture.remote, "file://")
	offline := remote + "-offline"
	require.NoError(tb, os.Rename(remote, offline), "synthetic remote should be unavailable during verification")
	defer func() {
		require.NoError(tb, os.Rename(offline, remote), "synthetic remote should be restored for later iterations")
	}()
	ctx := context.Background()
	actualHead := strings.TrimSpace(string(targetedReviewGit(tb, sandbox.WorkDir, "", "rev-parse", "HEAD")))
	require.Equal(tb, fixture.head, actualHead, "both paths should check out the exact captured head")
	require.NoError(tb, o.execReviewEvidence(ctx, sandbox, reviewTreeEvidenceScript(sandbox.WorkDir, fixture.base, fixture.head, ""), ""), "both paths should have complete pinned trees offline")
	require.NoError(tb, o.ensureCodeReviewInputs(ctx, session, sandbox), "both paths should verify reusable pinned evidence offline")
	var stdout, stderr bytes.Buffer
	exit, err := o.provider.Exec(ctx, sandbox, "/usr/bin/git merge-base '"+fixture.base+"' '"+fixture.head+"'", &stdout, &stderr)
	require.NoError(tb, err, "offline merge base should execute")
	require.Equal(tb, 0, exit, "offline merge base should succeed: %s", stderr.String())
	require.Equal(tb, fixture.mergeBase, strings.TrimSpace(stdout.String()), "both paths should have the exact captured merge base offline")
}

func TestTargetedReviewWorkspaceFetch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		movePRHead  bool
		unavailable bool
		broadClone  bool
	}{
		{name: "PR-only captured head and divergent base"},
		{name: "broad clone remains safe during PR-only head fetch", broadClone: true},
		{name: "moved PR ref with still available captured head", movePRHead: true},
		{name: "unavailable captured head", unavailable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			branches, commitsPerBranch := 3, 5
			if tt.broadClone {
				// Exceed Git’s auto commit-graph threshold. PR-only head fetches
				// must preserve the managed preparation topology guard.
				branches, commitsPerBranch = 64, 20
			}
			fixture := newTargetedReviewFetchFixture(t, branches, commitsPerBranch)
			if tt.movePRHead {
				targetedReviewGit(t, strings.TrimPrefix(fixture.remote, "file://"), "", "update-ref", "refs/pull/42/head", fixture.targetTip)
			}
			if tt.unavailable {
				fixture.head = strings.Repeat("f", 40)
			}
			o, sandbox, session := targetedReviewFetchWorkspace(t, fixture)
			ctx := context.Background()
			if tt.broadClone {
				require.NoError(t, o.cloneRepositoryForReview(ctx, sandbox, fixture.remote, "main", "synthetic-token"), "broad clone should initialize a partial repository")
				require.NoError(t, prepareTargetedReviewFetchWorkspace(ctx, o, sandbox, session, fixture), "the captured head fetch should keep Git topology safe for managed preparation")
				verifyTargetedReviewFetchWorkspace(t, o, sandbox, session, fixture)
				return
			}
			err := initTargetedReviewFetch(ctx, o, sandbox, fixture)
			if tt.unavailable {
				require.Error(t, err, "a missing captured object must fail the experimental fetch")
				fallback, fallbackSandbox, fallbackSession := targetedReviewFetchWorkspace(t, fixture)
				require.NoError(t, fallback.cloneRepositoryForReview(ctx, fallbackSandbox, fixture.remote, "main", "synthetic-token"), "fallback broad clone should still be available in a fresh workspace")
				require.Error(t, prepareTargetedReviewFetchWorkspace(ctx, fallback, fallbackSandbox, fallbackSession, fixture), "fallback must reject the unavailable capture even when the PR ref exists")
				return
			}
			require.NoError(t, err, "targeted fetch should retrieve both pinned commits")
			require.NoError(t, prepareTargetedReviewFetchWorkspace(ctx, o, sandbox, session, fixture), "targeted workspace should accept the same available captured head as ordinary preparation")
			verifyTargetedReviewFetchWorkspace(t, o, sandbox, session, fixture)
		})
	}
}

// BenchmarkReviewWorkspaceTargetedFetch measures the clone, checkout and managed
// immutable-input phases against a shared local filtered Git source. Each
// iteration starts with a fresh destination; the host filesystem cache is not
// controlled. It excludes GitHub policy, network, Docker and auth bootstrap.
func BenchmarkReviewWorkspaceTargetedFetch(b *testing.B) {
	fixture := newTargetedReviewFetchFixture(b, 64, 20)
	cases := []struct {
		name string
		init func(context.Context, *Orchestrator, *Sandbox) error
	}{
		{name: "broad", init: func(ctx context.Context, o *Orchestrator, sandbox *Sandbox) error {
			return o.cloneRepositoryForReview(ctx, sandbox, fixture.remote, "main", "synthetic-token")
		}},
		{name: "targeted", init: func(ctx context.Context, o *Orchestrator, sandbox *Sandbox) error {
			return initTargetedReviewFetch(ctx, o, sandbox, fixture)
		}},
	}
	for _, tt := range cases {
		b.Run(tt.name, func(b *testing.B) {
			var initialFetchTime time.Duration
			var packedObjects, packedBytes int64
			b.StopTimer()
			for range b.N {
				o, sandbox, session := targetedReviewFetchWorkspace(b, fixture)
				ctx := context.Background()
				b.StartTimer()
				started := time.Now()
				require.NoError(b, tt.init(ctx, o, sandbox), "both fetch strategies should initialize a filtered workspace")
				initialFetchTime += time.Since(started)
				require.NoError(b, prepareTargetedReviewFetchWorkspace(ctx, o, sandbox, session, fixture), "both fetch strategies should produce immutable review inputs")
				b.StopTimer()
				verifyTargetedReviewFetchWorkspace(b, o, sandbox, session, fixture)
				stats := string(targetedReviewGit(b, sandbox.WorkDir, "", "count-objects", "-v"))
				for _, stat := range strings.Split(stats, "\n") {
					if value, ok := strings.CutPrefix(stat, "in-pack: "); ok {
						var count int64
						_, err := fmt.Sscan(value, &count)
						require.NoError(b, err, "Git should report its packed object count")
						packedObjects += count
					}
				}
				packs, err := filepath.Glob(filepath.Join(sandbox.WorkDir, ".git", "objects", "pack", "*.pack"))
				require.NoError(b, err, "pack paths should be readable")
				for _, pack := range packs {
					info, err := os.Stat(pack)
					require.NoError(b, err, "pack size should be readable")
					packedBytes += info.Size()
				}
			}
			b.ReportMetric(float64(initialFetchTime.Nanoseconds())/float64(b.N), "initial-fetch-ns/op")
			b.ReportMetric(float64(packedObjects)/float64(b.N), "ready-packed-objects/op")
			b.ReportMetric(float64(packedBytes)/float64(b.N), "ready-pack-B/op")
		})
	}
}
