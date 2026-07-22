package worker

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	ghservice "github.com/assembledhq/143/internal/services/github"
)

const (
	githubRateLimitMinimumRetryAfter = time.Minute
	githubRateLimitJitterRange       = 30 * time.Second
	githubRateLimitMaxRetryDuration  = 2 * time.Hour
)

func githubRetryableError(err error, retryKey string) *RetryableError {
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
		retryable.RetryAfter = githubRateLimitRetryAfter(classification.RetryAfter, retryKey)
		retryWindow := githubRateLimitMaxRetryDuration
		retryable.MaxRetryDuration = &retryWindow
	}
	return retryable
}

func githubRateLimitRetryAfter(upstream *time.Duration, retryKey string) *time.Duration {
	delay := githubRateLimitMinimumRetryAfter
	if upstream != nil && *upstream > delay {
		delay = *upstream
	}
	digest := sha256.Sum256([]byte(retryKey))
	jitterSlots := uint32(githubRateLimitJitterRange / time.Second)
	jitter := time.Duration(binary.BigEndian.Uint32(digest[:4])%jitterSlots) * time.Second
	delay += jitter
	return &delay
}

func classifyGitHubJobError(err error, retryKey string) error {
	if retryable := githubRetryableError(err, retryKey); retryable != nil {
		return retryable
	}
	var apiErr *ghservice.GitHubAPIError
	if errors.As(err, &apiErr) {
		return &FatalError{Err: err}
	}
	return err
}
