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
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

func (c *Controller) observeBackgroundQuota(ctx context.Context, installationID int64, observation Observation, classification Classification, now time.Time) {
	if c.backgroundQuotaReserve <= 0 || observation.RequestErr != nil || observation.BodyErr != nil || !isTrustedQuotaResponse(observation.StatusCode, classification) {
		return
	}
	quota, ok := backgroundQuotaFromHeaders(observation.Header, now)
	if !ok {
		return
	}
	if _, err := c.cache.ObserveGitHubQuota(ctx, c.backgroundQuotaKey(installationID), quota); err != nil {
		c.logBackgroundQuotaDegraded(ctx, installationID, err)
	}
}

func backgroundQuotaFromHeaders(header http.Header, now time.Time) (cache.GitHubQuotaObservation, bool) {
	if len(header.Values("X-RateLimit-Resource")) != 1 || strings.TrimSpace(header.Get("X-RateLimit-Resource")) != "core" {
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
		c.logBackgroundQuotaDegraded(ctx, installationID, errors.New("background quota candidate identity missing"))
		return true
	}
	digest := sha256.Sum256([]byte(candidate))
	admission, err := c.cache.AdmitGitHubBackground(ctx, c.backgroundQuotaKey(installationID), fmt.Sprintf("%x", digest), c.now().UTC(), c.backgroundQuotaReserve, c.backgroundQuotaInterval)
	if err != nil || admission.Overflow {
		if err == nil {
			err = errors.New("background quota candidate capacity reached")
		}
		c.logBackgroundQuotaDegraded(ctx, installationID, err)
		return true
	}
	outcome := "admitted"
	if !admission.Known {
		outcome = "unknown"
	}
	if !admission.Admitted {
		outcome = "skipped"
		if c.mode == ModeObserve {
			outcome = "would_skip"
		}
		c.logger.Info().Int64("github_installation_id", installationID).
			Str("github_rate_limit_mode", string(c.mode)).
			Str("github_background_quota_decision", outcome).
			Time("github_background_retry_at", admission.RetryAt).
			Int64("github_background_quota_reserve", c.backgroundQuotaReserve).
			Msg("github background quota budget decision")
	}
	c.recordBackgroundQuotaDecision(ctx, outcome)
	return c.mode != ModeEnforce || admission.Admitted
}

func (c *Controller) BackgroundQuotaEnabled() bool {
	return c != nil && c.backgroundQuotaReserve > 0
}

func (c *Controller) backgroundQuotaKey(installationID int64) string {
	return fmt.Sprintf("{ghrl:v1:%s:app:%d:inst:%d}:quota:core", c.environment, c.appID, installationID)
}

func isTrustedQuotaResponse(status int, classification Classification) bool {
	return status >= 200 && status < 300 || status == http.StatusNotModified || (status == http.StatusForbidden || status == http.StatusTooManyRequests) && classification.RateLimited
}

func (c *Controller) BackgroundQuotaEnforcementEnabled() bool {
	return c.BackgroundQuotaEnabled() && c.mode == ModeEnforce && c.appID > 0 && len(c.allowlist) > 0
}

func (c *Controller) EnforcesBackgroundQuota(installationID int64) bool {
	return c.BackgroundQuotaEnforcementEnabled() && c.Participates(installationID)
}

func (c *Controller) recordBackgroundQuotaDecision(ctx context.Context, outcome string) {
	if c.backgroundQuotaDecisions != nil {
		c.backgroundQuotaDecisions.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("mode", string(c.mode)), attribute.String("outcome", outcome)))
	}
}

func (c *Controller) logBackgroundQuotaDegraded(ctx context.Context, installationID int64, err error) {
	c.recordBackgroundQuotaDecision(ctx, "degraded")
	now := c.now().UTC()
	c.backgroundQuotaLogMu.Lock()
	defer c.backgroundQuotaLogMu.Unlock()
	if previous, ok := c.backgroundQuotaDegradedAt[installationID]; ok && now.Sub(previous) < time.Minute {
		return
	}
	if len(c.backgroundQuotaDegradedAt) >= 1024 {
		for id, previous := range c.backgroundQuotaDegradedAt {
			if now.Sub(previous) >= time.Minute {
				delete(c.backgroundQuotaDegradedAt, id)
			}
		}
		if len(c.backgroundQuotaDegradedAt) >= 1024 {
			return
		}
	}
	c.backgroundQuotaDegradedAt[installationID] = now
	c.logger.Warn().Err(err).Int64("github_installation_id", installationID).
		Str("github_rate_limit_mode", string(c.mode)).Bool("github_background_quota_degraded", true).
		Msg("github background quota coordination unavailable; bypassing optimization")
}
