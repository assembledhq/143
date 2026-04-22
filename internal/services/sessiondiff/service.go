package sessiondiff

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/services/agent"
)

const SandboxMetadataBaseCommitSHA = "base_commit_sha"

// Collect returns the authoritative session diff: the current workspace
// compared against the immutable recorded base commit, plus synthetic additions
// for any untracked files.
func Collect(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox) (string, error) {
	var checkStdout, checkStderr bytes.Buffer
	checkExit, err := provider.Exec(ctx, sandbox, "git rev-parse --is-inside-work-tree", &checkStdout, &checkStderr)
	if err != nil {
		return "", fmt.Errorf("check git repo: %w", err)
	}
	if checkExit != 0 {
		return "", nil
	}

	baseCommitSHA := ""
	if sandbox.Metadata != nil {
		baseCommitSHA = sandbox.Metadata[SandboxMetadataBaseCommitSHA]
	}

	diffCmd := "git diff"
	if baseCommitSHA != "" {
		diffCmd = fmt.Sprintf("git diff --find-renames --binary %s -- .", shellEscapeSingleQuote(baseCommitSHA))
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := provider.Exec(ctx, sandbox, diffCmd, &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("exec git diff: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("git diff exited with code %d: %s", exitCode, stderr.String())
	}

	untrackedDiff, err := collectUntrackedDiffs(ctx, provider, sandbox)
	if err != nil {
		return "", err
	}
	return stdout.String() + untrackedDiff, nil
}

func collectUntrackedDiffs(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox) (string, error) {
	var stdout, stderr bytes.Buffer
	exitCode, err := provider.Exec(ctx, sandbox, "git ls-files --others --exclude-standard -- .", &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("list untracked files: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("git ls-files exited with code %d: %s", exitCode, stderr.String())
	}

	var builder strings.Builder
	for _, filePath := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		filePath = strings.TrimSpace(filePath)
		if filePath == "" {
			continue
		}

		var fileStdout, fileStderr bytes.Buffer
		cmd := fmt.Sprintf("git diff --find-renames --binary --no-index -- /dev/null '%s'", shellEscapeSingleQuote(filePath))
		exitCode, err = provider.Exec(ctx, sandbox, cmd, &fileStdout, &fileStderr)
		if err != nil {
			return "", fmt.Errorf("diff untracked file %s: %w", filePath, err)
		}
		if exitCode != 0 && exitCode != 1 {
			return "", fmt.Errorf("git diff for untracked file %s exited with code %d: %s", filePath, exitCode, fileStderr.String())
		}
		builder.WriteString(fileStdout.String())
	}

	return builder.String(), nil
}

func shellEscapeSingleQuote(value string) string {
	return strings.ReplaceAll(value, `'`, `'\''`)
}
