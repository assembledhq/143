package ratelimit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/cache"
)

func (c *Controller) observeBackgroundQuota(ctx context.Context, installationID int64, observation Observation, now time.Time) {
	if c.backgroundQuotaReserve <= 0 || observation.RequestErr != nil || observation.StatusCode <= 0 {
		return
	}
	quota, ok := backgroundQuotaFromHeaders(observation.Header, now)
	if !ok {
		return
	}
	if _, err := c.cache.ObserveGitHubQuota(ctx, c.backgroundQuotaKey(installationID), quota); err != nil {
		c.logBackgroundQuotaDegraded(err)
	}
}

func backgroundQuotaFromHeaders(header http.Header, now time.Time) (cache.GitHubQuotaObservation, bool) {
	if strings.TrimSpace(header.Get("X-RateLimit-Resource")) != "core" {
		return cache.GitHubQuotaObservation{}, false
	}
	values := make([]int64, 3)
	for i, key := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if len(header.Values(key)) != 1 {
			return cache.GitHubQuotaObservation{}, false
		}
		value, err := strconv.ParseInt(strings.TrimSpace(header.Get(key)), 10, 64)
		if err != nil {
			return cache.GitHubQuotaObservation{}, false
		}
		values[i] = value
	}
	reset := time.Unix(values[2], 0).UTC()
	if values[0] <= 0 || values[1] < 0 || values[1] > values[0] || !reset.After(now) || reset.After(now.Add(2*time.Hour)) {
		return cache.GitHubQuotaObservation{}, false
	}
	return cache.GitHubQuotaObservation{Now: now, ResetAt: reset, Limit: values[0], Remaining: values[1]}, true
}

func (c *Controller) AdmitBackground(ctx context.Context, installationID int64, candidate string) bool {
	if c == nil || c.backgroundQuotaReserve <= 0 || !c.Participates(installationID) {
		return true
	}
	if strings.TrimSpace(candidate) == "" {
		c.logBackgroundQuotaDegraded(errors.New("background quota candidate identity missing"))
		return true
	}
	digest := sha256.Sum256([]byte(candidate))
	admission, err := c.cache.AdmitGitHubBackground(ctx, c.backgroundQuotaKey(installationID), fmt.Sprintf("%x", digest), c.now().UTC(), c.backgroundQuotaReserve, c.backgroundQuotaInterval)
	if err != nil || admission.Overflow {
		if err == nil {
			err = errors.New("background quota candidate capacity reached")
		}
		c.logBackgroundQuotaDegraded(err)
		return true
	}
	if !admission.Admitted {
		c.logger.Debug().Int64("github_installation_id", installationID).
			Time("github_background_retry_at", admission.RetryAt).
			Int64("github_background_quota_reserve", c.backgroundQuotaReserve).
			Msg("github background quota budget skipped reconciliation")
	}
	return admission.Admitted
}

func (c *Controller) BackgroundQuotaEnabled() bool {
	return c != nil && c.backgroundQuotaReserve > 0
}

func (c *Controller) backgroundQuotaKey(installationID int64) string {
	return fmt.Sprintf("{ghrl:v1:%s:app:%d:inst:%d}:quota:core", c.environment, c.appID, installationID)
}

func (c *Controller) logBackgroundQuotaDegraded(err error) {
	c.backgroundQuotaDegradedLog.Do(func() {
		c.logger.Warn().Err(err).Bool("github_background_quota_degraded", true).
			Msg("github background quota coordination unavailable; bypassing optimization")
	})
}
