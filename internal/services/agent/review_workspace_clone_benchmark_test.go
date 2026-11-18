package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
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

// BenchmarkReviewWorkspaceColdClone compares clone through complete immutable
// review inputs, including cold-only input preparation for the private path.
// It uses local filtered Git transport; it does
// not model network latency, Docker exec/inspect, capacity admission, auth
// bootstrap, or publication. Run with -benchtime=1x -count=5 for bounded samples.
func BenchmarkReviewWorkspaceColdClone(b *testing.B) {
	for _, advanced := range []bool{false, true} {
		name := "stable_target"
		if advanced {
			name = "advanced_target"
		}
		b.Run(name, func(b *testing.B) {
			root := b.TempDir()
			source := filepath.Join(root, "source")
			git := func(bench *testing.B, directory string, args ...string) string {
				bench.Helper()
				cmd := exec.Command("/usr/bin/git", append([]string{"-C", directory}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
				out, err := cmd.CombinedOutput()
				require.NoError(bench, err, "synthetic repository command should succeed: %v: %s", args, out)
				return strings.TrimSpace(string(out))
			}
			require.NoError(b, os.MkdirAll(source, 0o700), "synthetic source directory should exist")
			git(b, source, "init", "-q", "-b", "main")
			git(b, source, "config", "user.name", "Synthetic Benchmark")
			git(b, source, "config", "user.email", "benchmark@example.invalid")
			rng := rand.New(rand.NewPCG(143, 42))
			writeFiles := func(prefix string, count int) {
				b.Helper()
				require.NoError(b, os.MkdirAll(filepath.Join(source, prefix), 0o700), "synthetic tree directory should exist")
				for i := range count {
					// Independent deterministic text blobs resist Git delta reuse.
					blob := make([]byte, 8*1024)
					for offset := range blob {
						blob[offset] = byte(rng.Uint32())
					}
					content := hex.EncodeToString(blob) + "\n"
					require.NoError(b, os.WriteFile(filepath.Join(source, prefix, fmt.Sprintf("file-%04d.txt", i)), []byte(content), 0o600), "synthetic source blob should be written")
				}
			}
			writeFiles("captured", 512)
			git(b, source, "add", ".")
			git(b, source, "commit", "-qm", "synthetic captured base")
			base := git(b, source, "rev-parse", "HEAD")
			git(b, source, "checkout", "-qb", "review")
			require.NoError(b, os.WriteFile(filepath.Join(source, "change.txt"), []byte("captured review change\n"), 0o600), "synthetic review change should exist")
			git(b, source, "add", ".")
			git(b, source, "commit", "-qm", "synthetic captured head")
			head := git(b, source, "rev-parse", "HEAD")
			git(b, source, "checkout", "-q", "main")
			if advanced {
				writeFiles("later-target", 1024)
				git(b, source, "add", ".")
				git(b, source, "commit", "-qm", "synthetic target after capture")
			}
			remoteDir := filepath.Join(root, "remote.git")
			git(b, source, "clone", "-q", "--bare", source, remoteDir)
			git(b, remoteDir, "config", "uploadpack.allowFilter", "true")
			git(b, remoteDir, "config", "uploadpack.allowAnySHA1InWant", "true")
			remote := "file://" + remoteDir
			for _, privateClone := range []bool{false, true} {
				mode := "generic_checkout"
				if privateClone {
					mode = "private_no_checkout_cold_inputs"
				}
				b.Run(mode, func(b *testing.B) {
					var cloneTime, readyTime time.Duration
					for range b.N {
						b.StopTimer()
						home := b.TempDir()
						b.Cleanup(func() {
							err := filepath.Walk(home, func(name string, info os.FileInfo, err error) error {
								if err != nil || !info.IsDir() {
									return err
								}
								return os.Chmod(name, 0o700)
							})
							require.NoError(b, err, "immutable input directories should permit fixture cleanup")
						})
						sandbox := &Sandbox{ID: uuid.NewString(), HomeDir: home, WorkDir: filepath.Join(home, "checkout"), Env: map[string]string{"HOME": home, "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"}}
						provider := &managedReviewInputsProvider{localReviewEvidenceProvider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: b.Logf}}
						o := &Orchestrator{provider: provider, repositories: testInternalRepoStore{repo: models.Repository{CloneURL: remote}}, github: testInternalGitHubTokens{}, logger: zerolog.Nop()}
						repoID := uuid.New()
						revision, err := json.Marshal(codeReviewCheckoutContext{Kind: "code_review", GitHubRepo: "example/repo", GitHubPRNumber: 42, BaseSHA: base, HeadSHA: head})
						require.NoError(b, err, "synthetic captured review identity should serialize")
						session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview, RepositoryID: &repoID, RevisionContext: revision}
						ctx := context.Background()
						prepareInputs := o.ensureCodeReviewInputs
						if privateClone {
							prepareInputs = o.prepareColdCodeReviewInputs
						}
						b.StartTimer()
						started := time.Now()
						if privateClone {
							require.NoError(b, o.cloneRepositoryForReview(ctx, sandbox, remote, "main", "synthetic-token"), "private clone should create an unpublished repository")
						} else {
							parent := *sandbox
							parent.WorkDir = home
							command := fmt.Sprintf("git clone --filter=blob:none --branch 'main' '%s' '%s'", shellEscapeSingleQuote(remote), shellEscapeSingleQuote(sandbox.WorkDir))
							exit, err := provider.Exec(ctx, &parent, command, io.Discard, io.Discard)
							require.NoError(b, err, "generic clone should execute")
							require.Equal(b, 0, exit, "generic clone should check out the target branch")
							// Model the generic authenticated clone's second remote-scrub exec;
							// the file transport itself requires no credential.
							exit, err = provider.Exec(ctx, sandbox, "git remote set-url origin '"+shellEscapeSingleQuote(remote)+"'", io.Discard, io.Discard)
							require.NoError(b, err, "generic remote scrub should execute")
							require.Equal(b, 0, exit, "generic remote scrub should succeed")
						}
						cloneTime += time.Since(started)
						require.NoError(b, o.checkoutCodeReviewHead(ctx, sandbox, remote, "", 42, "working-review", head), "both paths should check out the same captured head")
						require.NoError(b, prepareInputs(ctx, session, sandbox), "both paths should prepare complete immutable review inputs")
						readyTime += time.Since(started)
						b.StopTimer()
						require.Equal(b, head, git(b, sandbox.WorkDir, "rev-parse", "HEAD"), "ready workspace should retain the exact captured review head")
					}
					b.ReportMetric(float64(cloneTime.Nanoseconds())/float64(b.N), "clone-ns/op")
					b.ReportMetric(float64(readyTime.Nanoseconds())/float64(b.N), "ready-ns/op")
				})
			}
		})
	}
}
