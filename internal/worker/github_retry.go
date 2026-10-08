package worker

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"github.com/assembledhq/143/internal/db"
	ghservice "github.com/assembledhq/143/internal/services/github"
)

const (
	githubRateLimitMinimumRetryAfter = time.Minute
	githubRateLimitJitterRange       = 30 * time.Second
	githubRateLimitMaxRetryDuration  = 2 * time.Hour
)

func githubRetryableError(err error, retryKey string) *RetryableError {
	now := time.Now()
	classification := ghservice.ClassifyRetry(err, now)
	if !classification.Retryable {
		return nil
	}
	retryWindow := githubRateLimitMaxRetryDuration
	retryable := &RetryableError{
		Err:               err,
		ConsumeAttempt:    !classification.RateLimited,
		RetryAfter:        classification.RetryAfter,
		MaxRetryDuration:  &retryWindow,
		GitHubRetryPolicy: GitHubRetryPolicyBackoff,
	}
	if classification.RateLimited {
		switch {
		case classification.ControllerManaged && classification.RetryAt != nil:
			retryAt := classification.RetryAt.UTC()
			retryable.GitHubRetryPolicy = GitHubRetryPolicyExact
			retryable.GitHubRetryAt = &retryAt
			retryable.RetryAfter = nil
		case classification.RetryAfter == nil:
			retryable.GitHubRetryPolicy = GitHubRetryPolicyNoHint
			retryable.GitHubRetryKey = retryKey
			retryable.RetryAfter = nil
		default:
			retryable.RetryAfter = githubRateLimitRetryAfter(classification.RetryAfter, retryKey)
		}
	}
	return retryable
}

// Reconciliation joins failures from independent operations. Split only that
// explicit aggregate boundary: cancellation of a later database operation must
// not erase an earlier throttle, but a canceled HTTP operation must retain its
// entire error tree when ClassifyRetry decides whether it is terminal.
func githubReconciliationRetryableError(err error, retryKey string) *RetryableError {
	var causes []error
	var collect func(error)
	collect = func(cause error) {
		if joined, ok := cause.(interface{ Unwrap() []error }); ok {
			for _, independent := range joined.Unwrap() {
				collect(independent)
			}
			return
		}
		if ghservice.ClassifyRetry(cause, time.Now()).Retryable {
			causes = append(causes, cause)
		}
	}
	collect(err)
	retryable := githubRetryableError(errors.Join(causes...), retryKey)
	if retryable != nil {
		retryable.Err = err
	}
	return retryable
}

func applyGitHubRetrySchedule(retryable *RetryableError, retryWindowStartedAt, now time.Time) {
	if retryable == nil {
		return
	}
	switch retryable.GitHubRetryPolicy {
	case GitHubRetryPolicyNoHint:
		retryAt := githubNoHintRetryAt(retryWindowStartedAt, now, retryable.GitHubRetryKey)
		delay := retryAt.Sub(now)
		retryable.RetryAfter = &delay
	case GitHubRetryPolicyExact:
		if retryable.GitHubRetryAt == nil {
			return
		}
		delay := retryable.GitHubRetryAt.Sub(now)
		if delay < 0 {
			delay = 0
		}
		retryable.RetryAfter = &delay
	}
}

func githubNoHintRetryAt(retryWindowStartedAt, failureAt time.Time, retryKey string) time.Time {
	elapsed := failureAt.Sub(retryWindowStartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	var slot time.Duration
	switch {
	case elapsed < time.Minute:
		slot = time.Minute
	case elapsed < 3*time.Minute:
		slot = 3 * time.Minute
	case elapsed < 7*time.Minute:
		slot = 7 * time.Minute
	default:
		steps := (elapsed-7*time.Minute)/(5*time.Minute) + 1
		slot = 7*time.Minute + steps*5*time.Minute
	}
	candidate := retryWindowStartedAt.Add(slot)
	minimum := failureAt.Add(githubRateLimitMinimumRetryAfter)
	if candidate.Before(minimum) {
		candidate = minimum
	}
	return candidate.Add(githubRateLimitJitter(retryKey)).UTC()
}

func githubRateLimitRetryAfter(upstream *time.Duration, retryKey string) *time.Duration {
	delay := githubRateLimitMinimumRetryAfter
	if upstream != nil && *upstream > delay {
		delay = *upstream
	}
	delay += githubRateLimitJitter(retryKey)
	return &delay
}

func githubRateLimitJitter(retryKey string) time.Duration {
	digest := sha256.Sum256([]byte(retryKey))
	jitterSlots := uint32(githubRateLimitJitterRange / time.Second)
	return time.Duration(binary.BigEndian.Uint32(digest[:4])%jitterSlots) * time.Second
}

func classifyGitHubJobError(err error, retryKey string) error {
	if errors.Is(err, db.ErrCodeReviewPublicationLockBusy) {
		delay := 15 * time.Second
		retryWindow := githubRateLimitMaxRetryDuration
		return &RetryableError{Err: err, ConsumeAttempt: false, RetryAfter: &delay, MaxRetryDuration: &retryWindow}
	}
	if retryable := githubRetryableError(err, retryKey); retryable != nil {
		return retryable
	}
	var apiErr *ghservice.GitHubAPIError
	if errors.As(err, &apiErr) {
		return &FatalError{Err: err}
	}
	return err
}
