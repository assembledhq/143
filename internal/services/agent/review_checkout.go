package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Distinguish checkout failure from HEAD verification failure in one shell.
const (
	reviewCheckoutFailed       = 11
	reviewCheckoutVerifyFailed = 12
)

func (o *Orchestrator) checkoutCodeReviewHead(ctx context.Context, sandbox *Sandbox, repoURL, token string, pullNumber int, branch, head string) error {
	if !reviewCommitPattern.MatchString(head) || branch == "" {
		return fmt.Errorf("code review checkout requires an exact captured head and working branch")
	}
	checkoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Both the probe and checkout can fetch missing objects in a partial clone.
	// Suppress automatic maintenance that would create forbidden commit graphs.
	const git = "git -c maintenance.auto=false -c gc.auto=0"
	probe := fmt.Sprintf(git+" cat-file -e '%s^{commit}'", head)
	exit, err := o.provider.Exec(checkoutCtx, sandbox, probe, io.Discard, io.Discard)
	if err := reviewCheckoutExecError(checkoutCtx, err, token); err != nil {
		return fmt.Errorf("inspect captured review head: %w", err)
	}

	var stderr bytes.Buffer
	if exit != 0 {
		// Keep private fetch credentials out of the checkout process and any
		// Git filters or hooks that run while it materializes the worktree.
		fetchSandbox := reviewInputFetchSandbox(sandbox, token)
		exit, err = o.provider.Exec(checkoutCtx, fetchSandbox, codeReviewFetchCommand(repoURL, head), io.Discard, &stderr)
		if err := reviewCheckoutExecError(checkoutCtx, err, token); err != nil {
			return fmt.Errorf("fetch captured review head: %w", err)
		}
		if exit != 0 {
			stderr.Reset()
			command := codeReviewFetchCommand(repoURL, fmt.Sprintf("pull/%d/head", pullNumber))
			exit, err = o.provider.Exec(checkoutCtx, fetchSandbox, command, io.Discard, &stderr)
			if err := reviewCheckoutExecError(checkoutCtx, err, token); err != nil {
				return fmt.Errorf("captured review head unavailable: %w", err)
			}
			if exit != 0 {
				return fmt.Errorf("captured review head unavailable: exit=%d %s", exit, redactReviewCheckoutToken(stderr.String(), token))
			}
		}
	}
	// Exec may return after cancellation. Check again before starting the
	// worktree mutation, independently of the provider's cancellation behavior.
	if err := checkoutCtx.Err(); err != nil {
		return fmt.Errorf("checkout captured review head: %w", err)
	}
	checkout := fmt.Sprintf(git+" checkout -B '%s' '%s' >/dev/null || exit %d\ngit rev-parse HEAD || exit %d",
		shellEscapeSingleQuote(branch), head, reviewCheckoutFailed, reviewCheckoutVerifyFailed)
	var stdout bytes.Buffer
	stderr.Reset()
	exit, err = o.provider.Exec(checkoutCtx, sandbox, checkout, &stdout, &stderr)
	if err := reviewCheckoutExecError(checkoutCtx, err, token); err != nil {
		return fmt.Errorf("checkout captured review head: %w", err)
	}
	switch exit {
	case reviewCheckoutVerifyFailed:
		return fmt.Errorf("verify captured review head: exit=%d %s", exit, redactReviewCheckoutToken(stderr.String(), token))
	case 0:
		if actual := strings.TrimSpace(stdout.String()); actual != head {
			return fmt.Errorf("%w: expected %s, got %s", ErrStalePullRequestHead, head, redactReviewCheckoutToken(actual, token))
		}
		return nil
	default:
		return fmt.Errorf("checkout captured review head: exit=%d %s", exit, redactReviewCheckoutToken(stderr.String(), token))
	}
}

func reviewCheckoutExecError(ctx context.Context, err error, token string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	message := redactReviewCheckoutToken(err.Error(), token)
	if message == err.Error() {
		return err
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, ErrCodeReviewPreparationTransport} {
		if errors.Is(err, cause) {
			return fmt.Errorf("%s: %w", message, cause)
		}
	}
	return errors.New(message)
}

func redactReviewCheckoutToken(message, token string) string {
	return redactGitToken(redactGitToken(message, strings.ReplaceAll(url.QueryEscape(token), "+", "%20")), token)
}
