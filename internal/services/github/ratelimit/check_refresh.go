package ratelimit

import (
	"context"
	"errors"
	"time"
)

const (
	checkRefreshInterval = 30 * time.Second
	// A full refresh can use five PR detail reads while mergeability is
	// pending, plus check runs, statuses and an optional branch lookup.
	checkRefreshQuotaReserve = 500 + 8
)

// AdmitCheckRefresh budgets webhook-driven full PR refreshes separately from
// provider cooldowns. All installations use the shared budget, including
// installations outside the cooldown enforcement allowlist.
func (c *Controller) AdmitCheckRefresh(ctx context.Context, installationID int64) (bool, error) {
	if c == nil || c.appID <= 0 || installationID <= 0 {
		return false, errors.New("GitHub check refresh budget requires a resolved App installation")
	}
	return c.cache.AdmitGitHubCheckRefresh(ctx, c.backgroundQuotaKey(installationID), c.now().UTC(), checkRefreshQuotaReserve, checkRefreshInterval)
}
