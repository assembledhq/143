package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/cache"
)

func TestControllerBeforeExternal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		mode          Mode
		allowlist     []int64
		installation  int64
		blockOffset   time.Duration
		probeOffset   time.Duration
		expectDefer   bool
		expectPredict bool
	}{
		{name: "known cooldown", mode: ModeEnforce, allowlist: []int64{42}, installation: 42, blockOffset: time.Minute, expectDefer: true, expectPredict: true},
		{name: "live recovery probe", mode: ModeEnforce, allowlist: []int64{42}, installation: 42, blockOffset: -time.Second, probeOffset: 30 * time.Second, expectDefer: true, expectPredict: true},
		{name: "stronger cooldown", mode: ModeEnforce, allowlist: []int64{42}, installation: 42, blockOffset: time.Minute, probeOffset: 30 * time.Second, expectDefer: true, expectPredict: true},
		{name: "reset reached", mode: ModeEnforce, allowlist: []int64{42}, installation: 42},
		{name: "probe lease expired", mode: ModeEnforce, allowlist: []int64{42}, installation: 42, blockOffset: -time.Minute, probeOffset: -time.Second},
		{name: "other installation", mode: ModeEnforce, allowlist: []int64{42}, installation: 43, blockOffset: time.Minute},
		{name: "unknown installation", mode: ModeEnforce, allowlist: []int64{42}, blockOffset: time.Minute},
		{name: "empty enforcement allowlist", mode: ModeEnforce, installation: 42, blockOffset: time.Minute},
		{name: "observation only", mode: ModeObserve, installation: 42, blockOffset: time.Minute, expectPredict: true},
		{name: "observation allowlist excludes installation", mode: ModeObserve, allowlist: []int64{43}, installation: 42, blockOffset: time.Minute},
		{name: "disabled", mode: ModeOff, installation: 42, blockOffset: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
			var logs bytes.Buffer
			controller, err := NewController(Config{
				Mode: tt.mode, Environment: "test", AppID: 1, InstallationAllowlist: tt.allowlist,
				Logger: zerolog.New(&logs), Now: func() time.Time { return now },
			})
			require.NoError(t, err, "external admission fixture should initialize")
			state := cache.GitHubRateLimitState{
				Generation: 7, EpisodeCount: 3, BlockedUntil: now.Add(tt.blockOffset),
				Kind: string(KindPrimary), Resource: "core", Reason: "primary_reset",
			}
			if tt.probeOffset != 0 {
				state.ProbeOwner, state.ProbeToken, state.ProbeGeneration = "trusted-host", "trusted-probe", state.Generation
				state.ProbeUntil = now.Add(tt.probeOffset)
			}
			key := controller.key(tt.installation)
			controller.fallback.remember(key, state, now)
			scope := Scope{InstallationID: tt.installation, Caller: "sandbox_gh", Route: "/repos/:owner/:repo/pulls/:id"}
			err = controller.BeforeExternal(context.Background(), scope)
			if tt.expectDefer {
				var deferral *Deferral
				require.ErrorAs(t, err, &deferral, "covered external traffic should receive a typed local deferral")
				retryAt := state.BlockedUntil
				if state.ProbeUntil.After(retryAt) {
					retryAt = state.ProbeUntil
				}
				require.Equal(t, &Deferral{Kind: KindPrimary, Resource: "core", InstallationID: tt.installation, RetryAt: retryAt, Generation: 7, Reason: "primary_reset"}, deferral, "external admission should preserve the strongest retained deadline and its attribution")
			} else {
				require.NoError(t, err, "expired, disabled, observed, or unrelated traffic should remain available")
			}
			require.Equal(t, state, controller.fallback.get(key, now), "external admission must never claim a probe, clear recovery, or change retained evidence")
			require.Equal(t, uint64(0), controller.probeSeq.Load(), "external admission must not allocate recovery probe authority")
			predictions := 0
			decoder := json.NewDecoder(&logs)
			for decoder.More() {
				var event map[string]any
				require.NoError(t, decoder.Decode(&event), "controller logs should remain structured JSON")
				if event["message"] == "github request deferred" {
					predictions++
					require.Equal(t, tt.expectDefer, event["github_request_suppressed"], "prediction telemetry must distinguish actual enforcement")
					require.Equal(t, scope.Caller, event["github_caller"], "external admission must retain the host-bound caller")
					require.Equal(t, scope.Route, event["github_route"], "external admission must retain the normalized route")
				}
			}
			expectedPredictions := 0
			if tt.expectPredict {
				expectedPredictions = 1
			}
			require.Equal(t, expectedPredictions, predictions, "only a participating live cooldown should predict a deferral")
		})
	}
}

func TestControllerExternalAdmissionPreservesTrustedRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		shared bool
	}{{name: "process local"}, {name: "shared Redis", shared: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var client *cache.Client
			if tt.shared {
				client, _, _ = newControllerTestRedis(t)
			}
			now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
			controller := newControllerTestInstance(t, &now, client)
			scope := Scope{InstallationID: 42, Caller: "sandbox_tools"}
			permit := requireAdmission(t, controller, scope, "trusted request should initially be admitted")
			throttled := observeTestThrottle(controller, permit, "60")
			now = throttled.Deferral.RetryAt
			before, _ := controller.get(context.Background(), controller.key(42))
			for range 3 {
				require.NoError(t, controller.BeforeExternal(context.Background(), scope), "external requests should resume at reset without relying on a sandbox response")
			}
			after, _ := controller.get(context.Background(), controller.key(42))
			require.Equal(t, before, after, "external traffic after reset must leave trusted recovery evidence intact")
			probe := requireProbeAdmission(t, controller, Scope{InstallationID: 42, Caller: "pr_health"}, true, "trusted request should still acquire recovery authority", "external admission must not claim or clear the pending probe")
			var deferral *Deferral
			require.ErrorAs(t, controller.BeforeExternal(context.Background(), scope), &deferral, "external requests should defer while a trusted probe is in flight")
			require.Equal(t, now.Add(defaultProbeLease), deferral.RetryAt, "external requests should respect the full trusted probe lease")
			controller.Observe(context.Background(), probe, Observation{StatusCode: http.StatusOK})
			require.NoError(t, controller.BeforeExternal(context.Background(), scope), "trusted recovery should reopen external admission")
			requireProbeAdmission(t, controller, scope, false, "trusted recovery should retain ordinary admission", "the recovery tombstone should remain authoritative")
		})
	}
}

func TestNilControllerBeforeExternal(t *testing.T) {
	t.Parallel()
	var controller *Controller
	require.NoError(t, controller.BeforeExternal(context.Background(), Scope{InstallationID: 42}), "an absent optional controller should preserve external request availability")
}
