package ratelimit

import (
	"context"
	"errors"
	"time"
)

const (
	checkRefreshInterval = 30 * time.Second
	// Leave 500 core requests for foreground work, with room for five PR
	// detail reads, the first check and status pages, and a branch lookup.
	// Additional pages recheck headroom before reading.
	checkRefreshQuotaReserve = 500 + 8
)

// AdmitCheckRefresh budgets webhook-driven full PR refreshes separately from
// provider cooldowns. All installations use the shared budget, including
// installations outside the cooldown enforcement allowlist.
func (c *Controller) AdmitCheckRefresh(ctx context.Context, installationID int64) (bool, error) {
	return c.checkRefreshAdmission(ctx, installationID, checkRefreshInterval)
}

// CheckRefreshHasHeadroom checks whether an already admitted refresh can read
// another page. The check does not spend or extend the installation's slot.
func (c *Controller) CheckRefreshHasHeadroom(ctx context.Context, installationID int64) (bool, error) {
	return c.checkRefreshAdmission(ctx, installationID, 0)
}

func (c *Controller) checkRefreshAdmission(ctx context.Context, installationID int64, interval time.Duration) (bool, error) {
	if c == nil || c.appID <= 0 || installationID <= 0 {
		return false, errors.New("GitHub check refresh budget requires a resolved App installation")
	}
	return c.cache.AdmitGitHubCheckRefresh(ctx, c.backgroundQuotaKey(installationID), c.now().UTC(), checkRefreshQuotaReserve, interval)
}
