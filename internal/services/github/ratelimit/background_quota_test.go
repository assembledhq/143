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
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
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
		{name: "duplicate resource", change: func(h http.Header) { h.Add("X-RateLimit-Resource", "graphql") }},
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

func TestBackgroundQuotaHonorsObservationAndCanaryModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		mode         Mode
		allowlist    []int64
		installation int64
		participates bool
		enforces     bool
		admitted     bool
		decision     string
	}{
		{name: "default observe empty allowlist", installation: 42, participates: true, admitted: true, decision: "would_skip"},
		{name: "observe empty allowlist", mode: ModeObserve, installation: 42, participates: true, admitted: true, decision: "would_skip"},
		{name: "observe selected", mode: ModeObserve, allowlist: []int64{42}, installation: 42, participates: true, admitted: true, decision: "would_skip"},
		{name: "observe unselected", mode: ModeObserve, allowlist: []int64{43}, installation: 42, admitted: true},
		{name: "enforce empty allowlist", mode: ModeEnforce, installation: 42, admitted: true},
		{name: "enforce selected", mode: ModeEnforce, allowlist: []int64{42}, installation: 42, participates: true, enforces: true, decision: "skipped"},
		{name: "enforce unselected", mode: ModeEnforce, allowlist: []int64{43}, installation: 42, admitted: true},
		{name: "off", mode: ModeOff, allowlist: []int64{42}, installation: 42, admitted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _, _ := newControllerTestRedis(t)
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			var logs bytes.Buffer
			controller, err := NewController(Config{
				Mode: tt.mode, Environment: "test", AppID: 1, InstallationAllowlist: tt.allowlist,
				Cache: client, Now: func() time.Time { return now }, BackgroundQuotaReserve: 20, Logger: zerolog.New(&logs),
			})
			require.NoError(t, err, "isolated controller should preserve rollout mode")
			require.Equal(t, tt.participates, controller.Participates(tt.installation), "optional quota must not change cooldown participation")
			require.Equal(t, tt.enforces, controller.EnforcesBackgroundQuota(tt.installation), "only explicit enforcement canaries may suppress work")
			controller.Observe(context.Background(), Permit{Scope: Scope{InstallationID: tt.installation}}, Observation{StatusCode: http.StatusOK, Header: quotaTestHeaders(now, 10)})
			require.True(t, controller.AdmitBackground(context.Background(), tt.installation, "tenant:pr-a"), "first background candidate should remain available")
			require.Equal(t, tt.admitted, controller.AdmitBackground(context.Background(), tt.installation, "tenant:pr-b"), "observe and non-canary work must never be suppressed")
			if tt.decision != "" {
				require.Contains(t, logs.String(), `"github_background_quota_decision":"`+tt.decision+`"`, "quota logs must distinguish prediction from suppression")
			} else {
				require.Empty(t, logs.String(), "nonparticipating controllers must not claim quota decisions")
			}
		})
	}
}

func TestBackgroundQuotaRejectsFailedPrincipalEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		err    error
		body   error
	}{
		{name: "authentication error", status: http.StatusUnauthorized},
		{name: "permission error", status: http.StatusForbidden},
		{name: "server error", status: http.StatusServiceUnavailable},
		{name: "redirect", status: http.StatusTemporaryRedirect},
		{name: "transport error", status: http.StatusOK, err: errors.New("interrupted request")},
		{name: "body error", status: http.StatusOK, body: errors.New("interrupted response")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _, _ := newControllerTestRedis(t)
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			controller := newQuotaController(t, &now, client, 20, zerolog.Nop())
			controller.Observe(context.Background(), Permit{Scope: Scope{InstallationID: 42}}, Observation{StatusCode: tt.status, Header: quotaTestHeaders(now, 10), RequestErr: tt.err, BodyErr: tt.body})
			require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant:pr-a"), "unverified headers should leave quota unknown")
			require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant:pr-b"), "failed principal evidence must not restrict another background candidate")
		})
	}
}

func TestBackgroundQuotaFailedLaterWindowDoesNotReleaseReserve(t *testing.T) {
	t.Parallel()
	client, _, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	controller := newQuotaController(t, &now, client, 20, zerolog.Nop())
	permit := Permit{Scope: Scope{InstallationID: 42}}
	controller.Observe(context.Background(), permit, Observation{StatusCode: http.StatusOK, Header: quotaTestHeaders(now, 10)})
	require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant:pr-a"), "valid low-quota evidence should establish one shared slot")
	now = now.Add(time.Minute)
	unauthenticated := quotaTestHeaders(now, 60)
	unauthenticated.Set("X-RateLimit-Limit", "60")
	controller.Observe(context.Background(), permit, Observation{StatusCode: http.StatusUnauthorized, Header: unauthenticated})
	require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant:pr-b"), "the next legitimate periodic slot should remain available")
	require.False(t, controller.AdmitBackground(context.Background(), 42, "tenant:pr-c"), "later authentication-failure headers must not overwrite the retained installation budget")
}

func TestBackgroundQuotaDegradationVisibilityIsScopedAndBounded(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	controller := newQuotaController(t, &now, nil, 20, zerolog.New(&logs))
	for _, installation := range []int64{42, 42, 43, 43} {
		require.True(t, controller.AdmitBackground(context.Background(), installation, "tenant:pr-a"), "each scoped coordination failure must fail open")
	}
	require.Equal(t, 2, strings.Count(logs.String(), "bypassing optimization"), "one unavailable installation must not hide the next installation's fault")
	require.Contains(t, logs.String(), `"github_installation_id":42`, "degradation should identify the first affected scope in logs")
	require.Contains(t, logs.String(), `"github_installation_id":43`, "degradation should identify the second affected scope in logs")
	now = now.Add(time.Minute)
	require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant:pr-b"), "persistent coordination loss should continue failing open")
	require.Equal(t, 3, strings.Count(logs.String(), "bypassing optimization"), "persistent faults should remain visible at a bounded cadence")
}

func TestBackgroundQuotaDecisionMetricsExcludeScopeIdentity(t *testing.T) {
	t.Parallel()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()), "isolated quota metric reader should close")
	})
	counter, err := provider.Meter("background-quota-test").Int64Counter("github.background_quota.decisions")
	require.NoError(t, err, "quota decision counter should initialize")
	client, _, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	controller := newQuotaController(t, &now, client, 20, zerolog.Nop())
	controller.backgroundQuotaDecisions = counter
	controller.Observe(context.Background(), Permit{Scope: Scope{InstallationID: 42}}, Observation{StatusCode: http.StatusOK, Header: quotaTestHeaders(now, 10)})
	require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant-a:pr-a"), "first enforcing candidate should consume one slot")
	require.False(t, controller.AdmitBackground(context.Background(), 42, "tenant-b:pr-b"), "second enforcing candidate should emit a skip decision")
	controller.mode = ModeObserve
	require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant-c:pr-c"), "observe should emit a prediction while permitting work")
	controller.cache = nil
	require.True(t, controller.AdmitBackground(context.Background(), 42, "tenant-d:pr-d"), "coordination loss should emit a degradation while permitting work")
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected), "quota decisions should reach the SDK reader")
	type point struct {
		count      int64
		dimensions attribute.Set
	}
	actual := make(map[string]point)
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "github.background_quota.decisions" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok, "quota decision metric should be a counter")
			for _, value := range sum.DataPoints {
				outcome, ok := value.Attributes.Value("outcome")
				require.True(t, ok, "each decision should have a bounded outcome")
				actual[outcome.AsString()] = point{value.Value, value.Attributes}
			}
		}
	}
	expected := map[string]point{
		"admitted":   {1, attribute.NewSet(attribute.String("mode", "enforce"), attribute.String("outcome", "admitted"))},
		"skipped":    {1, attribute.NewSet(attribute.String("mode", "enforce"), attribute.String("outcome", "skipped"))},
		"would_skip": {1, attribute.NewSet(attribute.String("mode", "observe"), attribute.String("outcome", "would_skip"))},
		"degraded":   {1, attribute.NewSet(attribute.String("mode", "observe"), attribute.String("outcome", "degraded"))},
	}
	require.Equal(t, expected, actual, "quota metrics must contain only mode and bounded outcome, excluding installation, tenant, and PR identities")
}
