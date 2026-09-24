package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// Exercise real git state: a review starts with the PR's changes already
// committed, and must still detect subsequent committed and uncommitted edits.
func TestResultDiffOrWorkspaceFallback_CodeReviewBaseline(t *testing.T) {
	t.Parallel()
	const originalPRDiff = "diff --git a/pr.txt b/pr.txt\n--- a/pr.txt\n+++ b/pr.txt\n@@ -1 +1 @@\n-base\n+existing PR change\n"
	const reviewerDiff = "diff --git a/pr.txt b/pr.txt\n--- a/pr.txt\n+++ b/pr.txt\n@@ -1 +1 @@\n-existing PR change\n+reviewer edit\n"
	const untrackedDiff = "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+reviewer edit\n"
	tests := []struct {
		name     string
		origin   models.SessionOrigin
		edit     string
		expected string
	}{
		{name: "unchanged PR", origin: models.SessionOriginCodeReview},
		{name: "committed reviewer edit", origin: models.SessionOriginCodeReview, edit: "committed", expected: reviewerDiff},
		{name: "staged reviewer edit", origin: models.SessionOriginCodeReview, edit: "staged", expected: reviewerDiff},
		{name: "unstaged reviewer edit", origin: models.SessionOriginCodeReview, edit: "unstaged", expected: reviewerDiff},
		{name: "untracked reviewer file", origin: models.SessionOriginCodeReview, edit: "untracked", expected: untrackedDiff},
		{name: "coding session retains PR diff", origin: models.SessionOriginManual, expected: originalPRDiff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(context.Background(), "git", args...)
				cmd.Dir = dir
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "git fixture command %v should succeed: %s", args, output)
				return strings.TrimSpace(string(output))
			}
			write := func(name, contents string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600), "fixture file should be written")
			}
			git("init", "-b", "main")
			git("config", "user.name", "Review test")
			git("config", "user.email", "review-test@example.com")
			git("config", "commit.gpgsign", "false")
			write("pr.txt", "base\n")
			git("add", ".")
			git("commit", "-m", "base")
			git("update-ref", "refs/remotes/origin/main", "HEAD")
			git("checkout", "-b", "pr")
			write("pr.txt", "existing PR change\n")
			git("commit", "-am", "PR changes")
			pinnedHead := git("rev-parse", "HEAD")

			switch tt.edit {
			case "committed", "staged", "unstaged":
				write("pr.txt", "reviewer edit\n")
				if tt.edit == "staged" {
					git("add", "pr.txt")
				}
				if tt.edit == "committed" {
					git("commit", "-am", "Reviewer edit")
				}
			case "untracked":
				write("new.txt", "reviewer edit\n")
			}

			provider := &testInternalSandboxProvider{execFn: func(command string, stdout, stderr io.Writer) (int, error) {
				cmd := exec.CommandContext(context.Background(), "sh", "-c", command)
				cmd.Dir = dir
				cmd.Stdout, cmd.Stderr = stdout, stderr
				if err := cmd.Run(); err != nil {
					var exitErr *exec.ExitError
					if errors.As(err, &exitErr) {
						return exitErr.ExitCode(), nil
					}
					return 0, err
				}
				return 0, nil
			}}
			target := "main"
			run := &models.Session{Origin: tt.origin, BaseCommitSHA: &pinnedHead, TargetBranch: &target}
			orch := &Orchestrator{provider: provider, logger: zerolog.Nop()}
			diff := orch.resultDiffOrWorkspaceFallback(context.Background(), run, &Sandbox{WorkDir: dir}, "")
			// Blob abbreviations vary with git configuration; compare the entire
			// patch apart from its index line.
			diff = regexp.MustCompile(`(?m)^index [^\n]+\n`).ReplaceAllString(diff, "")
			require.Equal(t, tt.expected, diff, "review diffs should contain only changes since the pinned PR head")
		})
	}
}
