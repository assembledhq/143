package ratelimit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func quotaTestHeaders(now time.Time, remaining int64) http.Header {
	return http.Header{
		"X-Ratelimit-Limit": {"100"}, "X-Ratelimit-Remaining": {strconv.FormatInt(remaining, 10)},
		"X-Ratelimit-Reset": {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)}, "X-Ratelimit-Resource": {"core"},
	}
}

func newQuotaController(t *testing.T, now *time.Time, client *cache.Client, reserve int64, logger zerolog.Logger) *Controller {
	t.Helper()
	controller, err := NewController(Config{
		Mode: ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42, 43},
		Cache: client, Now: func() time.Time { return *now }, BackgroundQuotaReserve: reserve, BackgroundQuotaInterval: time.Minute, Logger: logger,
	})
	require.NoError(t, err, "quota controller should initialize with isolated dependencies")
	return controller
}

func TestBackgroundQuotaRejectsMalformedAndExpiredHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(http.Header)
	}{
		{name: "wrong resource", change: func(h http.Header) { h.Set("X-RateLimit-Resource", "graphql") }},
		{name: "unknown resource", change: func(h http.Header) { h.Del("X-RateLimit-Resource") }},
		{name: "missing remaining", change: func(h http.Header) { h.Del("X-RateLimit-Remaining") }},
		{name: "malformed remaining", change: func(h http.Header) { h.Set("X-RateLimit-Remaining", "invalid") }},
		{name: "duplicate remaining", change: func(h http.Header) { h.Add("X-RateLimit-Remaining", "99") }},
		{name: "negative remaining", change: func(h http.Header) { h.Set("X-RateLimit-Remaining", "-1") }},
		{name: "remaining above limit", change: func(h http.Header) { h.Set("X-RateLimit-Remaining", "101") }},
		{name: "zero limit", change: func(h http.Header) { h.Set("X-RateLimit-Limit", "0") }},
		{name: "expired window", change: func(h http.Header) { h.Set("X-RateLimit-Reset", "1") }},
		{name: "unbounded reset", change: func(h http.Header) { h.Set("X-RateLimit-Reset", "9223372036854775807") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			header := quotaTestHeaders(now, 10)
			tt.change(header)
			_, valid := backgroundQuotaFromHeaders(header, now)
			require.False(t, valid, "invalid or non-core evidence must not control background admission")
		})
	}
}

func TestBackgroundQuotaSharesTrustedObservations(t *testing.T) {
	t.Parallel()
	client, _, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	a, b := newQuotaController(t, &now, client, 20, zerolog.Nop()), newQuotaController(t, &now, client, 20, zerolog.Nop())
	ctx := context.Background()
	permit := requireAdmission(t, a, Scope{InstallationID: 42}, "trusted request should establish its installation")
	a.Observe(ctx, permit, Observation{StatusCode: 200, Header: quotaTestHeaders(now, 10)})
	require.True(t, b.AdmitBackground(ctx, 42, "tenant-a:pr-a"), "another controller should obtain the first shared slot")
	require.False(t, a.AdmitBackground(ctx, 42, "tenant-b:pr-b"), "another candidate must wait for a shared periodic slot")
	a.Observe(ctx, permit, Observation{StatusCode: 200, Header: quotaTestHeaders(now, 90)})
	require.False(t, b.AdmitBackground(ctx, 42, "tenant-b:pr-b"), "late higher quota must not undo low-quota protection")
	now = now.Add(time.Minute)
	require.True(t, b.AdmitBackground(ctx, 42, "tenant-b:pr-b"), "oldest queued candidate should obtain the next slot")
	require.True(t, b.AdmitBackground(ctx, 43, "tenant-c:pr-c"), "an unknown healthy installation must stay independent")
	now = now.Add(time.Hour)
	require.True(t, b.AdmitBackground(ctx, 42, "tenant-a:pr-a"), "expired window evidence must stop restricting background progress")
}

func TestBackgroundQuotaBypassesDisabledUnresolvedAndFailedResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		reserve      int64
		installation int64
		failure      bool
	}{
		{name: "disabled budget", installation: 42},
		{name: "unknown installation", reserve: 20},
		{name: "outside allowlist", reserve: 20, installation: 99},
		{name: "transport error", reserve: 20, installation: 42, failure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _, _ := newControllerTestRedis(t)
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			controller := newQuotaController(t, &now, client, tt.reserve, zerolog.Nop())
			observation := Observation{StatusCode: 200, Header: quotaTestHeaders(now, 10)}
			if tt.failure {
				observation.RequestErr = errors.New("transport failed")
			}
			controller.Observe(context.Background(), Permit{Scope: Scope{InstallationID: tt.installation}}, observation)
			require.True(t, controller.AdmitBackground(context.Background(), tt.installation, "tenant:pr-a"), "unknown or disabled budget must permit ordinary work")
			require.True(t, controller.AdmitBackground(context.Background(), tt.installation, "tenant:pr-b"), "untrusted responses must not establish a periodic budget")
		})
	}
}

func TestBackgroundQuotaUnavailableRedisLogsOptimizationBypass(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	controller := newQuotaController(t, &now, nil, 20, zerolog.New(&logs))
	for _, candidate := range []string{"tenant:pr-a", "tenant:pr-b"} {
		require.True(t, controller.AdmitBackground(context.Background(), 42, candidate), "missing Redis must fail open for optional budgeting")
	}
	require.Equal(t, 1, strings.Count(logs.String(), "bypassing optimization"), "coordination loss should emit one bounded explicit bypass event")
	require.Contains(t, logs.String(), `"github_background_quota_degraded":true`, "operator telemetry should identify the optional budget gap")
}

func TestBackgroundQuotaConfigurationBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		reserve  int64
		interval time.Duration
		invalid  bool
	}{
		{name: "disabled defaults"},
		{name: "bounded enabled", reserve: 20, interval: time.Minute},
		{name: "negative reserve", reserve: -1, invalid: true},
		{name: "negative interval", interval: -time.Second, invalid: true},
		{name: "too frequent", interval: time.Millisecond, invalid: true},
		{name: "unbounded delay", interval: time.Hour, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewController(Config{BackgroundQuotaReserve: tt.reserve, BackgroundQuotaInterval: tt.interval})
			if tt.invalid {
				require.Error(t, err, "unsafe background budget configuration should fail construction")
				return
			}
			require.NoError(t, err, "disabled or bounded background budgeting should initialize")
		})
	}
}
