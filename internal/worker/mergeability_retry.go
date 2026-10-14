package worker

import "time"

const githubRetryPolicyMergeabilityWait GitHubRetryPolicy = "mergeability_wait"
const mergeabilityMaximumPollDelay = 2 * time.Minute

// newPullRequestMergeabilityWait preserves the dependency budget while letting
// the worker select a growing poll delay after loading the durable window.
func newPullRequestMergeabilityWait(err error) *RetryableError {
	delay := 5 * time.Second
	budget := githubRateLimitMaxRetryDuration
	return &RetryableError{Err: err, RetryAfter: &delay, MaxRetryDuration: &budget, GitHubRetryPolicy: githubRetryPolicyMergeabilityWait}
}

func mergeabilityPollDelay(startedAt, now time.Time) time.Duration {
	elapsed := now.Sub(startedAt)
	delay := 5 * time.Second
	// Elapsed time represents the cumulative waits, so polls progress through
	// 5, 10, 20, 40, 80 and then 120 seconds even after a job is reclaimed.
	for elapsed >= delay && delay < mergeabilityMaximumPollDelay {
		elapsed -= delay
		delay = min(2*delay, mergeabilityMaximumPollDelay)
	}
	return delay
}
