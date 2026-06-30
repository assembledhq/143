package sessiondiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/services/agent"
)

// SandboxMetadataBaseCommitSHA is re-exported from the agent package so
// existing callers (adapters, etc.) that referenced sessiondiff.SandboxMetadataBaseCommitSHA
// keep compiling. The canonical declaration lives in the agent package
// (agent.SandboxMetadataBaseCommitSHA) — see the rationale there.
const SandboxMetadataBaseCommitSHA = agent.SandboxMetadataBaseCommitSHA

// SandboxMetadataTargetBranch is re-exported from the agent package; see
// agent.SandboxMetadataTargetBranch for the canonical declaration.
const SandboxMetadataTargetBranch = agent.SandboxMetadataTargetBranch

// ErrNoBaseCommitSHA is returned by Collect when the caller does not supply a
// base commit SHA. The previous behavior — falling back to plain `git diff`
// against the index — silently produced misleading results once the agent had
// committed its work (e.g. after a PR push), because a clean working tree
// would yield an empty diff and overwrite the previously persisted authoritative
// diff. Returning this sentinel forces callers to handle the missing-base case
// explicitly (typically by skipping the persistence write so the prior diff
// is preserved).
var ErrNoBaseCommitSHA = errors.New("sessiondiff: base commit sha is required")

// Collect returns the authoritative session diff: the working branch compared
// against the most recent common ancestor with the target branch, plus
// synthetic additions for any untracked files. baseCommitSHA must be non-empty —
// it is the immutable fallback used when the dynamic merge-base resolution
// fails (no network during fetch, missing remote ref, etc.).
//
// When targetBranch is supplied, Collect resolves the diff base dynamically
// by best-effort `git fetch origin <targetBranch>` followed by
// `git merge-base origin/<targetBranch> HEAD`. This makes the diff equivalent
// to what GitHub renders for the PR: changes on the working branch only,
// excluding anything pulled in by integrating the target branch back into the
// working branch (e.g. `git pull origin main` or merging main to resolve PR
// conflicts). When targetBranch is empty, or when fetch/merge-base fail,
// Collect falls back to diffing against the immutable baseCommitSHA snapshot
// captured at session creation.
//
// logger is used for Debug-level diagnostics when the merge-base path can't
// be resolved and Collect falls back to baseCommitSHA. A zero zerolog.Logger
// is acceptable (writes nowhere) for callers that don't have one.
func Collect(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox, baseCommitSHA, targetBranch string, logger zerolog.Logger) (string, error) {
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

	diffBase := resolveDiffBase(ctx, provider, sandbox, baseCommitSHA, targetBranch, logger)

	diffCmd := fmt.Sprintf("git diff --find-renames --binary %s -- .", shellEscapeSingleQuote(diffBase))

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

// resolveDiffBase returns the SHA Collect should diff against. When targetBranch
// is set, attempts to compute merge-base(origin/<targetBranch>, HEAD) — the same
// base GitHub uses for the PR. Falls back to baseCommitSHA on any failure
// (empty target branch, fetch error, missing remote ref, no common ancestor).
//
// The fetch is best-effort and intentionally swallows errors: a transient
// network blip or auth glitch should degrade us to the (still-correct, just
// possibly inflated post-merge) baseCommitSHA path rather than break the
// Changes tab entirely. In the common case the working branch has not had
// the target branch merged in, merge-base resolves to baseCommitSHA anyway,
// so the two paths agree.
//
// The fetch must run on every Collect rather than once at sandbox setup —
// long-running turns can outlive a fetch's freshness, and re-fetching
// immediately before the diff guarantees the merge-base reflects the actual
// state of origin/<branch> at diff time. The cost is one single-ref network
// round-trip per turn (sub-second for typical repos), which is amortized
// against the LLM call that just finished.
func resolveDiffBase(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox, baseCommitSHA, targetBranch string, logger zerolog.Logger) string {
	if targetBranch == "" {
		return baseCommitSHA
	}

	escapedBranch := shellEscapeSingleQuote(targetBranch)

	// Refresh origin/<targetBranch>. --quiet suppresses progress output;
	// --no-tags avoids pulling unrelated tag refs. If this fails (no network,
	// no auth, no remote) we fall through and use whatever local origin ref
	// is present, which is still better than the frozen baseCommitSHA.
	// stderr is buffered so we can surface auth/network failures via the
	// debug log on the fallback path; stdout is discarded since fetch
	// progress (suppressed by --quiet anyway) carries no signal.
	var fetchErr bytes.Buffer
	// --end-of-options ensures the branch is always parsed as a refspec, never
	// as a git option, even if upstream ref validation is ever bypassed.
	fetchCmd := fmt.Sprintf("git fetch --quiet --no-tags --end-of-options origin '%s'", escapedBranch)
	fetchExit, fetchExecErr := provider.Exec(ctx, sandbox, fetchCmd, io.Discard, &fetchErr)

	var mbOut, mbErr bytes.Buffer
	mbCmd := fmt.Sprintf("git merge-base 'origin/%s' HEAD", escapedBranch)
	exitCode, err := provider.Exec(ctx, sandbox, mbCmd, &mbOut, &mbErr)
	if err != nil || exitCode != 0 {
		logger.Debug().
			Str("target_branch", targetBranch).
			Str("fallback_base_commit_sha", baseCommitSHA).
			Int("fetch_exit", fetchExit).
			AnErr("fetch_exec_err", fetchExecErr).
			Str("fetch_stderr", redactPossibleToken(strings.TrimSpace(fetchErr.String()))).
			Int("merge_base_exit", exitCode).
			AnErr("merge_base_exec_err", err).
			Str("merge_base_stderr", strings.TrimSpace(mbErr.String())).
			Msg("sessiondiff: merge-base unavailable, falling back to base_commit_sha snapshot")
		return baseCommitSHA
	}
	mb := strings.TrimSpace(mbOut.String())
	if mb == "" {
		logger.Debug().
			Str("target_branch", targetBranch).
			Str("fallback_base_commit_sha", baseCommitSHA).
			Msg("sessiondiff: merge-base returned empty output, falling back to base_commit_sha snapshot")
		return baseCommitSHA
	}
	return mb
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

// redactPossibleToken scrubs `x-access-token:<token>` patterns out of fetch
// stderr before it lands in a structured log. CloneRepo strips the token
// from .git/config after clone, so a healthy sandbox should never expose
// it here — but a partially-configured remote (legacy GITHUB_TOKEN path,
// reused container post-orchestrator-restart, etc.) could still surface
// it in a fetch error message, so we redact defensively. The replacement
// keeps the rest of the error text intact for diagnosis.
func redactPossibleToken(s string) string {
	const marker = "x-access-token:"
	var b strings.Builder
	rest := s
	for {
		idx := strings.Index(rest, marker)
		if idx < 0 {
			b.WriteString(rest)
			return b.String()
		}
		// Emit everything up to and including the marker unchanged.
		b.WriteString(rest[:idx+len(marker)])
		// Token runs until the next '@' (URL terminator) or any whitespace.
		// Advance past it on the input side so we never re-scan the
		// REDACTED placeholder we're about to write — that's what caused
		// the obvious infinite-loop bug in the in-place replace_all variant.
		stop := idx + len(marker)
		for stop < len(rest) && rest[stop] != '@' && rest[stop] != ' ' && rest[stop] != '\t' && rest[stop] != '\n' && rest[stop] != '\r' {
			stop++
		}
		b.WriteString("REDACTED")
		rest = rest[stop:]
	}
}
