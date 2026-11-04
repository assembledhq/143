package ratelimit

import (
	"net/http"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAdmitCheckRefreshSharesInstallationSlot(t *testing.T) {
	t.Parallel()
	client, server, _ := newControllerTestRedis(t)
	otherClient := cache.New(cache.Config{URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, otherClient, "second worker should have its own Redis connection")
	t.Cleanup(func() { require.NoError(t, otherClient.Close(), "second worker connection should close") })
	a := newCheckRefreshController(t, Config{Mode: ModeOff, Environment: "test", AppID: 1, Cache: client})
	b := newCheckRefreshController(t, Config{Mode: ModeEnforce, Environment: "test", AppID: 1, Cache: otherClient, InstallationAllowlist: []int64{43}})
	otherApp := newCheckRefreshController(t, Config{Environment: "test", AppID: 2, Cache: otherClient})
	otherEnv := newCheckRefreshController(t, Config{Environment: "other", AppID: 1, Cache: otherClient})

	tests := []struct {
		controller     *Controller
		installationID int64
		advance        time.Duration
		continuation   bool
		admitted       bool
	}{
		{controller: b, installationID: 42, continuation: true, admitted: true},
		{controller: a, installationID: 42, admitted: true},
		{controller: b, installationID: 42},
		{controller: b, installationID: 43, admitted: true},
		{controller: otherApp, installationID: 42, admitted: true},
		{controller: otherEnv, installationID: 42, admitted: true},
		{controller: b, installationID: 42, advance: 29 * time.Second, continuation: true, admitted: true},
		{controller: b, installationID: 42},
		{controller: b, installationID: 42, advance: time.Second, admitted: true},
		{controller: a, installationID: 42},
	}
	for i, tt := range tests {
		server.FastForward(tt.advance)
		check := tt.controller.AdmitCheckRefresh
		if tt.continuation {
			check = tt.controller.CheckRefreshHasHeadroom
		}
		admitted, err := check(t.Context(), tt.installationID)
		require.NoError(t, err, "step %d should query the shared budget", i)
		require.Equal(t, tt.admitted, admitted, "step %d should admit only independent scopes and expired slots", i)
	}
}

func TestAdmitCheckRefreshPreservesCoreHeadroom(t *testing.T) {
	t.Parallel()
	client, server, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	config := Config{Mode: ModeOff, Environment: "test", AppID: 1, Cache: client, Now: func() time.Time { return now }}
	observer := newCheckRefreshController(t, config)
	config.Mode = ModeEnforce
	config.InstallationAllowlist = []int64{43}
	worker := newCheckRefreshController(t, config)
	config.Mode = ModeObserve
	config.InstallationAllowlist = nil
	config.BackgroundQuotaReserve = 20
	sweepObserver := newCheckRefreshController(t, config)
	permit := Permit{Scope: Scope{InstallationID: 42}}
	header := quotaTestHeaders(now, checkRefreshQuotaReserve)
	header.Set("X-RateLimit-Limit", "5000")
	observer.Observe(t.Context(), permit, Observation{StatusCode: http.StatusUnauthorized, Header: header})
	admitted, err := worker.AdmitCheckRefresh(t.Context(), 42)
	require.NoError(t, err, "untrusted quota evidence should leave the slot available")
	require.True(t, admitted, "an authentication failure should not reserve core quota")

	now = now.Add(30 * time.Second)
	server.FastForward(30 * time.Second)
	observer.Observe(t.Context(), permit, Observation{StatusCode: http.StatusOK, Header: header})
	healthy := quotaTestHeaders(now, 4500)
	healthy.Set("X-RateLimit-Limit", "5000")
	sweepObserver.Observe(t.Context(), permit, Observation{StatusCode: http.StatusOK, Header: healthy})
	admitted, err = worker.AdmitCheckRefresh(t.Context(), 42)
	require.NoError(t, err, "trusted headers from the other controller should inform admission")
	require.False(t, admitted, "a live reset family at the cutoff should reserve its remaining quota for reviews")
	canContinue, err := worker.CheckRefreshHasHeadroom(t.Context(), 42)
	require.NoError(t, err, "continuation should see the same trusted quota as initial admission")
	require.False(t, canContinue, "an extra page should respect the live quota reserve")

	now = now.Add(time.Hour - 30*time.Second)
	server.FastForward(time.Hour - 30*time.Second)
	admitted, err = worker.AdmitCheckRefresh(t.Context(), 42)
	require.NoError(t, err, "the next provider window should make progress")
	require.True(t, admitted, "expired low quota evidence should no longer block check refreshes")
}

func TestAdmitCheckRefreshFailsClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		appID          int64
		installationID int64
		missingControl bool
		closedClient   bool
	}{
		{name: "controller missing", appID: 1, installationID: 42, missingControl: true},
		{name: "app unresolved", installationID: 42},
		{name: "installation unresolved", appID: 1},
		{name: "redis missing", appID: 1, installationID: 42},
		{name: "redis client closed", appID: 1, installationID: 42, closedClient: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var controller *Controller
			if !tt.missingControl {
				cfg := Config{AppID: tt.appID}
				if tt.closedClient {
					client, _, closeClient := newControllerTestRedis(t)
					closeClient()
					cfg.Cache = client
				}
				controller = newCheckRefreshController(t, cfg)
			}
			admitted, err := controller.AdmitCheckRefresh(t.Context(), tt.installationID)
			require.Error(t, err, "an unavailable budget should be surfaced to the caller")
			require.False(t, admitted, "uncertain budget ownership should defer check refreshes")
			canContinue, err := controller.CheckRefreshHasHeadroom(t.Context(), tt.installationID)
			require.Error(t, err, "an unavailable budget should also be surfaced during pagination")
			require.False(t, canContinue, "uncertain budget ownership should defer continuation pages")
		})
	}
}

func newCheckRefreshController(t *testing.T, cfg Config) *Controller {
	t.Helper()
	controller, err := NewController(cfg)
	require.NoError(t, err, "check refresh controller should initialize")
	return controller
}
