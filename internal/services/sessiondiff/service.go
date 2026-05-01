package sessiondiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/services/agent"
)

// SandboxMetadataBaseCommitSHA is re-exported from the agent package so
// existing callers (adapters, etc.) that referenced sessiondiff.SandboxMetadataBaseCommitSHA
// keep compiling. The canonical declaration lives in the agent package
// (agent.SandboxMetadataBaseCommitSHA) — see the rationale there.
const SandboxMetadataBaseCommitSHA = agent.SandboxMetadataBaseCommitSHA

// ErrNoBaseCommitSHA is returned by Collect when the caller does not supply a
// base commit SHA. The previous behavior — falling back to plain `git diff`
// against the index — silently produced misleading results once the agent had
// committed its work (e.g. after a PR push), because a clean working tree
// would yield an empty diff and overwrite the previously persisted authoritative
// diff. Returning this sentinel forces callers to handle the missing-base case
// explicitly (typically by skipping the persistence write so the prior diff
// is preserved).
var ErrNoBaseCommitSHA = errors.New("sessiondiff: base commit sha is required")

// Collect returns the authoritative session diff: the current workspace
// compared against the immutable recorded base commit, plus synthetic additions
// for any untracked files. baseCommitSHA must be non-empty — Collect refuses
// to fall back to plain `git diff` so that callers cannot inadvertently store
// an empty diff after the workspace has been committed (post-push, post-merge).
func Collect(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox, baseCommitSHA string) (string, error) {
	var checkStdout, checkStderr bytes.Buffer
	checkExit, err := provider.Exec(ctx, sandbox, "git rev-parse --is-inside-work-tree", &checkStdout, &checkStderr)
	if err != nil {
		return "", fmt.Errorf("check git repo: %w", err)
	}
	if checkExit != 0 {
		return "", nil
	}

	if baseCommitSHA == "" {
		return "", ErrNoBaseCommitSHA
	}

	diffCmd := fmt.Sprintf("git diff --find-renames --binary %s -- .", shellEscapeSingleQuote(baseCommitSHA))

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
