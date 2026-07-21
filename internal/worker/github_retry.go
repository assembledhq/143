package worker

import (
	"errors"
	"time"

	ghservice "github.com/assembledhq/143/internal/services/github"
)

const (
	githubRateLimitFallbackRetryAfter = time.Minute
	githubRateLimitMaxRetryDuration   = 2 * time.Hour
)

func githubRetryableError(err error) *RetryableError {
	classification := ghservice.ClassifyRetry(err, time.Now())
	if !classification.Retryable {
		return nil
	}
	retryable := &RetryableError{
		Err:            err,
		ConsumeAttempt: !classification.RateLimited,
		RetryAfter:     classification.RetryAfter,
	}
	if classification.RateLimited {
		if retryable.RetryAfter == nil {
			retryAfter := githubRateLimitFallbackRetryAfter
			retryable.RetryAfter = &retryAfter
		}
		retryWindow := githubRateLimitMaxRetryDuration
		retryable.MaxRetryDuration = &retryWindow
	}
	return retryable
}

func classifyGitHubJobError(err error) error {
	if retryable := githubRetryableError(err); retryable != nil {
		return retryable
	}
	var apiErr *ghservice.GitHubAPIError
	if errors.As(err, &apiErr) {
		return &FatalError{Err: err}
	}
	return err
}
