package github

import "context"

// PullRequestSyncOrigin distinguishes actual read leaders from callers merely
// waiting on their singleflight. In particular, queued stale refreshes do not
// participate in the sweep-only background budget.
type PullRequestSyncOrigin string

const (
	PullRequestSyncOriginSweep  PullRequestSyncOrigin = "sweep"
	PullRequestSyncOriginQueued PullRequestSyncOrigin = "queued"
	PullRequestSyncOriginDirect PullRequestSyncOrigin = "direct"
)

type pullRequestSyncOriginKey struct{}

func WithPullRequestSyncOrigin(ctx context.Context, origin PullRequestSyncOrigin) context.Context {
	return context.WithValue(ctx, pullRequestSyncOriginKey{}, origin)
}

func PullRequestSyncOriginFromContext(ctx context.Context) PullRequestSyncOrigin {
	if reconciliationScopeFromContext(ctx) != nil {
		return PullRequestSyncOriginSweep
	}
	if origin, _ := ctx.Value(pullRequestSyncOriginKey{}).(PullRequestSyncOrigin); origin == PullRequestSyncOriginQueued {
		return origin
	}
	return PullRequestSyncOriginDirect
}
