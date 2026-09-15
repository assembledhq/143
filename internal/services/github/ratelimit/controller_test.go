package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/cache"
)

func TestLocalObservationPreservesEffectiveDeadlineMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		replaces bool
	}{
		{name: "shorter observation retains attribution", duration: time.Minute},
		{name: "equal observation retains first attribution", duration: 3 * time.Hour},
		{name: "longer observation replaces attribution", duration: 4 * time.Hour, replaces: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newLocalStore(10)
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			expected := cache.GitHubRateLimitState{
				Generation: 1, EpisodeCount: 1, BlockedUntil: now.Add(3 * time.Hour),
				Kind: "primary", Resource: "core", Reason: "primary_reset",
			}
			store.observe("installation", cache.GitHubRateLimitObservation{
				Now: now, BlockedUntil: expected.BlockedUntil, Kind: expected.Kind,
				Resource: expected.Resource, Reason: expected.Reason, TTL: 5 * time.Hour,
			})
			observation := cache.GitHubRateLimitObservation{
				Now: now.Add(time.Second), BlockedUntil: now.Add(tt.duration),
				Kind: "secondary", Resource: "graphql", Reason: "retry_after", TTL: 5 * time.Hour,
			}
			if tt.replaces {
				expected.BlockedUntil = observation.BlockedUntil
				expected.Kind, expected.Resource, expected.Reason = observation.Kind, observation.Resource, observation.Reason
			}
			actual, opened, applied := store.observe("installation", observation)
			require.True(t, applied, "concurrent observation should belong to the active episode")
			require.False(t, opened, "concurrent observation should retain the active generation")
			require.Equal(t, expected, actual, "local deadline and metadata should come from the same observation")
		})
	}
}

func TestLocalRememberPreservesOnlyAuthoritativeLiveProbes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		change          func(*cache.GitHubRateLimitState)
		expired, retain bool
	}{
		{name: "stale snapshot preserves live claim", retain: true},
		{name: "expired lease is not restored", expired: true},
		{name: "newer generation wins", change: func(s *cache.GitHubRateLimitState) { s.Generation++ }},
		{name: "stronger deadline wins", change: func(s *cache.GitHubRateLimitState) { s.BlockedUntil = s.BlockedUntil.Add(time.Minute) }},
		{name: "completion tombstone wins", change: func(s *cache.GitHubRateLimitState) { s.BlockedUntil = time.Time{} }},
		{name: "newer completion wins", change: func(s *cache.GitHubRateLimitState) { s.Generation++; s.BlockedUntil = time.Time{} }},
		{name: "replacement claim wins", change: func(s *cache.GitHubRateLimitState) {
			s.ProbeOwner, s.ProbeToken, s.ProbeGeneration = "replacement", "replacement-token", s.Generation
			s.ProbeUntil = s.BlockedUntil.Add(time.Minute)
		}},
		{name: "older nonempty claim cannot replace live authority", retain: true, change: func(s *cache.GitHubRateLimitState) {
			s.ProbeOwner, s.ProbeToken, s.ProbeGeneration, s.ProbeUntil = "old-owner", "old-token", s.Generation, s.BlockedUntil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			store := newLocalStore(10)
			claimed := cache.GitHubRateLimitState{
				Generation: 1, EpisodeCount: 1, BlockedUntil: now.Add(-time.Second), Kind: "primary", Resource: "core", Reason: "reset",
				ProbeOwner: "owner", ProbeToken: "token", ProbeGeneration: 1, ProbeUntil: now.Add(defaultProbeLease),
			}
			if tt.expired {
				claimed.ProbeUntil = now
			}
			store.remember("key", claimed, now)
			incoming := claimed
			incoming.ProbeOwner, incoming.ProbeToken, incoming.ProbeGeneration, incoming.ProbeUntil = "", "", 0, time.Time{}
			if tt.change != nil {
				tt.change(&incoming)
			}
			expected := incoming
			if tt.retain {
				expected = claimed
			}
			store.remember("key", incoming, now)
			require.Equal(t, expected, store.get("key", now), "remember should preserve live authority only for an otherwise equal stale snapshot")
		})
	}
}

func TestControllerStaleSnapshotRetainsProbeDuringRedisOutage(t *testing.T) {
	t.Parallel()
	client, _, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	requireStaleSnapshotOutage(t, newControllerTestInstance(t, &now, client), &now, 42)
}

func TestControllerCoordinatesIndependentInstances(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		observation Observation
		recovered   bool
	}{
		{name: "success", observation: Observation{StatusCode: http.StatusOK}, recovered: true},
		{name: "not modified", observation: Observation{StatusCode: http.StatusNotModified}, recovered: true},
		{name: "permission denied", observation: Observation{StatusCode: http.StatusForbidden}, recovered: true},
		{name: "not found", observation: Observation{StatusCode: http.StatusNotFound}, recovered: true},
		{name: "validation failure", observation: Observation{StatusCode: http.StatusUnprocessableEntity}, recovered: true},
		{name: "graphql application error", observation: Observation{StatusCode: http.StatusOK, GraphQLErrors: []GraphQLError{{Type: "NOT_FOUND", Message: "Could not resolve to a Repository"}}}, recovered: true},
		{name: "request canceled", observation: Observation{StatusCode: http.StatusOK, RequestErr: context.Canceled}},
		{name: "body interrupted", observation: Observation{StatusCode: http.StatusNotFound, BodyErr: errors.New("interrupted body")}},
		{name: "graphql invalid envelope", observation: Observation{StatusCode: http.StatusOK, BodyErr: errors.New("invalid envelope")}},
		{name: "request timeout", observation: Observation{StatusCode: http.StatusRequestTimeout}},
		{name: "too early", observation: Observation{StatusCode: http.StatusTooEarly}},
		{name: "server unavailable", observation: Observation{StatusCode: http.StatusServiceUnavailable}},
		{name: "redirect", observation: Observation{StatusCode: http.StatusSeeOther}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			redisClient, _, _ := newControllerTestRedis(t)

			now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
			controllerA := newControllerTestInstance(t, &now, redisClient)
			controllerB := newControllerTestInstance(t, &now, redisClient)
			permit := requireAdmission(t, controllerA, Scope{InstallationID: 42, Caller: "sync"}, "first request should be admitted before throttle evidence")
			result := controllerA.Observe(context.Background(), permit, Observation{
				StatusCode: http.StatusForbidden, Header: http.Header{"Retry-After": []string{"60"}}, Message: "secondary rate limit",
			})
			require.True(t, result.EpisodeOpened, "first throttle should open one shared episode")
			require.Equal(t, now.Add(time.Minute), result.Deferral.RetryAt, "server timing should become the exact shared deadline")

			deferral := requireDeferral(t, controllerB, Scope{InstallationID: 42, Caller: "review"}, "another controller should defer the blocked installation")
			require.Equal(t, now.Add(time.Minute), deferral.RetryAt, "independent controller should see the shared deadline")
			requireAdmission(t, controllerB, Scope{InstallationID: 43, Caller: "review"}, "an unaffected installation should continue")

			now = now.Add(time.Minute)
			probe := requireProbeAdmission(t, controllerA, Scope{InstallationID: 42, Caller: "sync"}, true, "one caller should acquire the recovery probe", "eligible request should be identified as the recovery probe")
			requireDeferral(t, controllerB, Scope{InstallationID: 42, Caller: "review"}, "competing caller should defer behind the probe lease")

			recovered := controllerA.Observe(context.Background(), probe, tt.observation)
			require.False(t, recovered.Classification.RateLimited, "non-throttle probe should not be classified as rate limited")
			requireProbeAdmission(t, controllerA, Scope{InstallationID: 42, Caller: "review"}, !tt.recovered, "completed probe should release its lease", "only definitive non-throttle responses should reopen ordinary admission")
			if tt.recovered {
				requireProbeAdmission(t, controllerB, Scope{InstallationID: 42, Caller: "review"}, false, "successful generation-matched probe should reopen the installation", "another controller should observe definitive recovery")
			}
		})
	}
}

func TestControllerHydratesFallbackFromSharedState(t *testing.T) {
	t.Parallel()

	redisClient, _, closeRedis := newControllerTestRedis(t)

	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controllerA := newControllerTestInstance(t, &now, redisClient)
	controllerB := newControllerTestInstance(t, &now, redisClient)

	permit := requireAdmission(t, controllerA, Scope{InstallationID: 42}, "initial request should be admitted")
	result := observeTestThrottle(controllerA, permit, "60")
	requireDeferral(t, controllerB, Scope{InstallationID: 42}, "second controller should load and defer from shared state")

	closeRedis()
	now = result.Deferral.RetryAt
	probe := requireProbeAdmission(t, controllerB, Scope{InstallationID: 42}, true, "hydrated fallback should admit a recovery probe during Redis loss", "fallback recovery should retain the shared episode and issue a probe")
	require.Equal(t, int64(1), probe.Generation, "fallback probe should retain the shared generation")
}

func TestControllerReconcilesRetainedFallbackAfterRedisRecordLoss(t *testing.T) {
	t.Parallel()
	client, server, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	requireRecordLossRecovery(t, newControllerTestInstance(t, &now, client), newControllerTestInstance(t, &now, client), &now, 42, func(key string) { server.Del(key) })
}

func requireRecordLossRecovery(t *testing.T, a, b *Controller, now *time.Time, installationID int64, loseRecord func(string)) {
	t.Helper()
	scope := Scope{InstallationID: installationID}
	initial := requireAdmission(t, a, scope, "initial request should be admitted")
	throttled := observeTestThrottle(a, initial, "60")
	requireDeferral(t, b, scope, "second controller should retain the shared episode locally")
	loseRecord(a.key(installationID))
	deferral := requireDeferral(t, b, scope, "retained state should be restored before ordinary admission")
	require.Equal(t, throttled.Deferral.RetryAt, deferral.RetryAt, "record loss must preserve the known provider deadline exactly")
	*now = throttled.Deferral.RetryAt
	probe := requireProbeAdmission(t, b, scope, true, "restored expired cooldown should issue one recovery probe", "restored recovery probe should retain generation and token fencing")
	b.Observe(context.Background(), probe, Observation{StatusCode: http.StatusOK})
	requireProbeAdmission(t, a, scope, false, "successful restored probe should publish a tombstone to other controllers", "ordinary admission should resume after recovered state is reconciled")
}

func TestControllerRestoresStrongerDeadlineAfterRollbackProbeThrottles(t *testing.T) {
	t.Parallel()
	client, server, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	requireRollbackRecovery(t, newControllerTestInstance(t, &now, client), newControllerTestInstance(t, &now, client), newControllerTestInstance(t, &now, client), &now, 42, true, func(key string, deadline time.Time) {
		server.HSet(key, "blocked_until", fmt.Sprintf("%d", deadline.UnixMilli()))
	})
}

func requireRollbackRecovery(t *testing.T, a, b, c *Controller, now *time.Time, installationID int64, advanceGeneration bool, rollback func(string, time.Time)) {
	t.Helper()
	scope, key := Scope{InstallationID: installationID}, a.key(installationID)
	initial := requireAdmission(t, a, scope, "initial request should be admitted")
	retained := observeTestThrottle(a, initial, "600")
	requireDeferral(t, b, scope, "second controller should retain the provider deadline locally")
	*now = now.Add(time.Second)
	rollback(key, *now)
	premature := requireProbeAdmission(t, c, scope, true, "controller without retained evidence should claim the rolled-back deadline", "rolled-back deadline should admit one premature probe")
	generation := premature.Generation
	if advanceGeneration {
		throttled := observeTestThrottle(c, premature, "5")
		generation++
		require.Equal(t, generation, throttled.Deferral.Generation, "premature throttle should advance Redis before restoration")
		require.True(t, retained.Deferral.RetryAt.After(throttled.Deferral.RetryAt), "retained deadline should remain the stronger evidence")
	}
	deferral := requireDeferral(t, b, scope, "retained controller should reconcile stronger evidence across generations")
	require.Equal(t, retained.Deferral.RetryAt, deferral.RetryAt, "ordinary admission should remain suppressed until the exact retained deadline")
	state, err := a.cache.GetGitHubRateLimitState(context.Background(), key)
	require.NoError(t, err, "reconciled state should be readable")
	require.Equal(t, generation, state.Generation, "deadline evidence should not roll back generation authority")
	require.Equal(t, retained.Deferral.RetryAt, state.BlockedUntil, "shared state should restore the exact provider deadline")
	require.Empty(t, state.ProbeToken, "restoration should leave no probe admitted against superseded evidence")
	c.Observe(context.Background(), premature, Observation{StatusCode: http.StatusOK})
	c.Observe(context.Background(), premature, Observation{RequestErr: errors.New("late premature failure")})
	late := observeTestThrottle(c, premature, "5")
	require.False(t, late.EpisodeOpened, "late shorter throttle must not advance restored state")
	require.Equal(t, retained.Deferral.RetryAt, late.Deferral.RetryAt, "late outcomes must retain the exact restored deadline")
	after, err := a.cache.GetGitHubRateLimitState(context.Background(), key)
	require.NoError(t, err, "reconciled state should remain readable after late outcomes")
	require.Equal(t, state, after, "late outcomes must preserve all restored evidence")
	deferral = requireDeferral(t, a, scope, "ordinary requests should remain suppressed before the restored provider deadline")
	require.Equal(t, retained.Deferral.RetryAt, deferral.RetryAt, "continued suppression should expose the exact restored deadline")
	*now = retained.Deferral.RetryAt
	probe := requireProbeAdmission(t, b, scope, true, "one normal recovery probe should be admitted at the restored deadline", "normal recovery probe should remain generation fenced")
	require.Equal(t, generation, probe.Generation, "normal recovery should use the current Redis generation")
	b.Observe(context.Background(), probe, Observation{StatusCode: http.StatusOK})
	requireProbeAdmission(t, a, scope, false, "successful current probe should restore ordinary admission", "completed recovery should leave a current-generation tombstone")
}

func TestControllerReconcilesRedisLossAcrossProbeTransitions(t *testing.T) {
	t.Parallel()

	redisClient, redisServer, _ := newControllerTestRedis(t)

	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controller := newControllerTestInstance(t, &now, redisClient)
	initial := requireAdmission(t, controller, Scope{InstallationID: 42}, "initial request should be admitted")
	first := observeTestThrottle(controller, initial, "1")
	now = first.Deferral.RetryAt
	key := controller.key(42)

	redisServer.Del(key)
	claimed, ok, degraded := controller.claim(context.Background(), key, "claim-after-loss", now, now.Add(defaultProbeLease))
	require.True(t, ok, "claim should restore a lost record before acquiring the probe")
	require.False(t, degraded, "successful Redis reconciliation should remain shared rather than degraded")
	require.Equal(t, "claim-after-loss", claimed.ProbeToken, "restored claim should retain its unique token")

	redisServer.Del(key)
	secondDeadline := now.Add(10 * time.Minute)
	observed, opened, applied, degraded := controller.observe(context.Background(), key, cache.GitHubRateLimitObservation{
		Now: now, BlockedUntil: secondDeadline, Kind: "secondary", Reason: "probe throttled after loss",
		ProbeGeneration: claimed.Generation, ProbeToken: claimed.ProbeToken, TTL: recoveryStateTTL,
	})
	require.True(t, opened, "probe observation should restore its lost lease before advancing generation")
	require.True(t, applied, "current probe observation should apply after reconciliation")
	require.False(t, degraded, "successful observation reconciliation should remain shared")
	require.Equal(t, claimed.Generation+1, observed.Generation, "restored probe throttle should advance exactly one generation")

	now = secondDeadline
	probe := requireProbeAdmission(t, controller, Scope{InstallationID: 42}, true, "advanced episode should issue a current recovery probe", "recovery request should carry probe fencing")
	redisServer.Del(key)
	completed, degraded := controller.complete(context.Background(), key, probe.ProbeOwner, probe.ProbeToken, probe.Generation, true)
	require.True(t, completed, "completion should restore a lost current lease before publishing recovery")
	require.False(t, degraded, "successful completion reconciliation should remain shared")
	state, err := redisClient.GetGitHubRateLimitState(context.Background(), key)
	require.NoError(t, err, "recovered Redis tombstone should be readable")
	require.Equal(t, probe.Generation, state.Generation, "completion should preserve the current generation tombstone")
	require.True(t, state.BlockedUntil.IsZero(), "completion after record loss should clear the shared cooldown")
}

func TestControllerProbeLeaseValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		lease, expected time.Duration
		invalid         bool
	}{
		{name: "negative defaults", lease: -time.Second, expected: defaultProbeLease},
		{name: "zero defaults", expected: defaultProbeLease},
		{name: "positive preserved", lease: time.Second, expected: time.Second},
		{name: "retention boundary", lease: recoveryStateTTL, expected: recoveryStateTTL},
		{name: "beyond retention", lease: recoveryStateTTL + time.Nanosecond, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller, err := NewController(Config{ProbeLease: tt.lease})
			if tt.invalid {
				require.Error(t, err, "probe lease must not outlive its retained claim")
				require.Nil(t, controller, "unsupported lease should not create a controller")
				return
			}
			require.NoError(t, err, "lease no longer than retention should remain supported")
			require.Equal(t, tt.expected, controller.probeLease, "constructor should preserve valid leases and apply the existing default")
		})
	}
}

func TestControllerModesAllowlistAndDeadlines(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		mode          Mode
		allowlist     []int64
		installation  int64
		expectEnforce bool
	}{
		{name: "observe computes but does not suppress", mode: ModeObserve, installation: 42},
		{name: "enforce suppresses allowlisted installation", mode: ModeEnforce, allowlist: []int64{42}, installation: 42, expectEnforce: true},
		{name: "enforce excludes installation outside allowlist", mode: ModeEnforce, allowlist: []int64{99}, installation: 42},
		{name: "off does not participate", mode: ModeOff, installation: 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller, err := NewController(Config{Mode: tt.mode, Environment: "test", AppID: 1, InstallationAllowlist: tt.allowlist, Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: tt.name})
			require.NoError(t, err, "controller mode should validate")
			permit := requireAdmission(t, controller, Scope{InstallationID: tt.installation}, "initial request should be admitted")
			controller.Observe(context.Background(), permit, Observation{StatusCode: http.StatusTooManyRequests, Message: "rate limit"})
			_, err = controller.Before(context.Background(), Scope{InstallationID: tt.installation})
			require.Equal(t, tt.expectEnforce, err != nil, "only an enforce-mode participating installation should be suppressed")
		})
	}

	controller := newControllerTestInstance(t, &now, nil)
	requireLongDeadlineRetention(t, controller, controller, &now, 42)
}

func requireLongDeadlineRetention(t *testing.T, a, b *Controller, now *time.Time, installationID int64) {
	t.Helper()
	scope := Scope{InstallationID: installationID}
	initial := requireAdmission(t, a, scope, "retention installation should admit its first request")
	reset := now.Add(3 * time.Hour)
	long := a.Observe(context.Background(), initial, Observation{StatusCode: http.StatusForbidden, Header: http.Header{
		"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}, "X-Ratelimit-Resource": {"core"},
	}})
	require.Equal(t, reset, long.Deferral.RetryAt, "provider reset must survive beyond the local five-minute cap")
	*now = now.Add(time.Second)
	short := a.Observe(context.Background(), initial, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"60"}, "X-Ratelimit-Resource": {"graphql"}}})
	require.Equal(t, long.Deferral, short.Deferral, "short observation must preserve the effective deadline and its complete attribution")
	state, _ := a.get(context.Background(), a.key(installationID))
	require.Equal(t, cache.GitHubRateLimitState{Generation: 1, EpisodeCount: 1, BlockedUntil: reset, Kind: string(long.Deferral.Kind), Resource: "core", Reason: long.Deferral.Reason}, state, "retained state must keep metadata from the strongest observation")
	*now = now.Add(2*time.Hour + 20*time.Minute)
	deferral := requireDeferral(t, b, scope, "retention must survive beyond the shorter observation TTL")
	require.Equal(t, reset, deferral.RetryAt, "retention must preserve the exact longer provider deadline")
}

func TestControllerProbeTokenFencesSameOwnerReclaim(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name string }{{"local"}, {"shared"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var client *cache.Client
			if tt.name == "shared" {
				client, _, _ = newControllerTestRedis(t)
			}
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			a := newControllerTestInstance(t, &now, client)
			b := a
			if client != nil {
				b = newControllerTestInstance(t, &now, client)
			}
			requireSameOwnerRecovery(t, a, b, &now, 42)
		})
	}
}

func requireSameOwnerRecovery(t *testing.T, controller, peer *Controller, now *time.Time, installationID int64) {
	t.Helper()
	scope := Scope{InstallationID: installationID}
	initial := requireAdmission(t, controller, scope, "initial request should be admitted")
	throttled := observeTestThrottle(controller, initial, "1")
	*now = throttled.Deferral.RetryAt
	oldProbe := requireProbeAdmission(t, controller, scope, true, "old probe should claim the eligible episode", "old request should carry probe metadata")

	*now = now.Add(30 * time.Second)
	newProbe := requireProbeAdmission(t, controller, scope, true, "same controller should reclaim after lease expiry", "reclaimed request should be a probe")
	require.Equal(t, oldProbe.ProbeOwner, newProbe.ProbeOwner, "same process should retain its owner identity")
	require.NotEqual(t, oldProbe.ProbeToken, newProbe.ProbeToken, "each claim should receive a unique fencing token")

	controller.Observe(context.Background(), oldProbe, Observation{StatusCode: http.StatusOK, Header: make(http.Header)})
	controller.Observe(context.Background(), oldProbe, Observation{RequestErr: errors.New("late transport failure")})
	lateThrottle := controller.Observe(context.Background(), oldProbe, Observation{StatusCode: http.StatusTooManyRequests})
	require.False(t, lateThrottle.EpisodeOpened, "late old throttle should not open or extend the reclaimed episode")
	deferral := requireDeferral(t, controller, scope, "late old results must leave the reclaimed lease authoritative")
	require.Equal(t, now.Add(30*time.Second), deferral.RetryAt, "competing work should wait for the current claim token")

	requireDeferral(t, peer, scope, "peer should cache the current lease before its release")
	controller.Observe(context.Background(), newProbe, Observation{RequestErr: errors.New("failed probe")})
	replacement := requireProbeAdmission(t, peer, scope, true, "peer must claim immediately after release", "released cached authority must not be resurrected")
	require.Equal(t, newProbe.Generation+1, replacement.Generation, "failed release should advance only the fencing generation")
	retained := peer.fallback.get(peer.key(installationID), *now)
	require.Equal(t, int64(1), retained.EpisodeCount, "release must preserve the existing no-hint backoff episode")
	controller.Observe(context.Background(), newProbe, Observation{StatusCode: http.StatusOK})
	requireDeferral(t, controller, scope, "released probe success cannot clear the replacement lease")
	later := observeTestThrottle(controller, oldProbe, "600")
	require.False(t, later.EpisodeOpened, "late explicit evidence must not advance the episode")
	require.Equal(t, replacement.Generation, later.Deferral.Generation, "late explicit evidence must retain current generation authority")
	peer.Observe(context.Background(), replacement, Observation{StatusCode: http.StatusOK})
	shared := peer.cache
	peer.cache = &cache.Client{}
	deferral = requireDeferral(t, peer, scope, "rejected current success must remember the stronger cooldown before Redis loss")
	require.Equal(t, now.Add(10*time.Minute), deferral.RetryAt, "late provider deadline must suppress competing work exactly")
	peer.cache = shared
	*now = deferral.RetryAt
	current := requireAdmission(t, peer, scope, "stronger deadline should allow a new probe only when eligible")
	peer.Observe(context.Background(), current, Observation{StatusCode: http.StatusOK})
	observeTestThrottle(controller, oldProbe, "1200")
	requireProbeAdmission(t, controller, scope, false, "current claim token should close the episode", "completed tombstones must reject late old throttle authority")
}

func TestControllerStaleProbeThrottleUsesEffectiveDeadlineMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		retryAfter   string
		usesIncoming bool
	}{
		{name: "shorter stale response preserves current attribution", retryAfter: "5"},
		{name: "equal stale response preserves current attribution", retryAfter: "600"},
		{name: "longer stale response uses incoming attribution", retryAfter: "1200", usesIncoming: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			controller := newControllerTestInstance(t, &now, nil)
			initial := requireAdmission(t, controller, Scope{InstallationID: 42}, "initial request should be admitted")
			first := observeTestThrottle(controller, initial, "1")
			now = first.Deferral.RetryAt
			oldProbe := requireAdmission(t, controller, Scope{InstallationID: 42}, "old probe should claim the eligible episode")
			now = now.Add(defaultProbeLease)
			currentProbe := requireAdmission(t, controller, Scope{InstallationID: 42}, "current probe should reclaim the expired lease")
			current := controller.Observe(context.Background(), currentProbe, Observation{
				StatusCode: http.StatusForbidden, Header: http.Header{
					"X-Ratelimit-Remaining": []string{"0"},
					"X-Ratelimit-Reset":     []string{strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)},
					"X-Ratelimit-Resource":  []string{"core"},
				}, Message: "current primary cooldown",
			})
			require.True(t, current.EpisodeOpened, "current probe throttle should advance the shared generation")
			retained := controller.fallback.get(controller.key(42), now)
			stale := controller.Observe(context.Background(), oldProbe, Observation{
				StatusCode: http.StatusTooManyRequests, Header: http.Header{
					"Retry-After": []string{tt.retryAfter}, "X-Ratelimit-Resource": []string{"graphql"},
				}, Message: "stale secondary throttle",
			})
			expected := *current.Deferral
			if tt.usesIncoming {
				expected.RetryAt = now.Add(20 * time.Minute)
				expected.Kind, expected.Resource, expected.Reason = KindSecondary, "graphql", "stale secondary throttle"
				retained.BlockedUntil, retained.Kind, retained.Resource, retained.Reason = expected.RetryAt, string(expected.Kind), expected.Resource, expected.Reason
			}
			require.False(t, stale.EpisodeOpened, "stale token must not mutate the newer episode")
			require.Equal(t, expected, *stale.Deferral, "stale response deferral should describe the evidence supplying its effective deadline")
			require.Equal(t, retained, controller.fallback.get(controller.key(42), now), "late evidence may extend only deadline attribution while retaining current generation and episode")
		})
	}
}

func TestControllerFailedProbeClaimUsesConcurrentCooldownExtension(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	controller := newControllerTestInstance(t, &now, nil)
	requireConcurrentExtension(t, controller, controller, &now, 42)
}

func requireConcurrentExtension(t *testing.T, a, b *Controller, now *time.Time, installationID int64) {
	t.Helper()
	scope := Scope{InstallationID: installationID}
	initial := requireAdmission(t, a, scope, "initial request should be admitted")
	*now = observeTestThrottle(a, initial, "1").Deferral.RetryAt
	later := now.Add(10 * time.Minute)
	b.beforeClaim = func() {
		b.beforeClaim = nil
		observeTestThrottle(a, initial, "600")
	}
	deferral := requireDeferral(t, b, scope, "concurrent extension should make the atomic probe claim defer")
	require.Equal(t, later, deferral.RetryAt, "failed claim must use the strongest returned cooldown instead of an invented lease deadline")
}

func TestLocalDeadlineIsBoundedAndIncreasing(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	deadlines := []time.Duration{
		localDeadline(now, 0, "key").Sub(now),
		localDeadline(now, 1, "key").Sub(now),
		localDeadline(now, 2, "key").Sub(now),
		localDeadline(now, 20, "key").Sub(now),
	}
	require.Less(t, deadlines[0], deadlines[1], "second no-hint episode should wait longer")
	require.Less(t, deadlines[1], deadlines[2], "third no-hint episode should wait longer")
	require.LessOrEqual(t, deadlines[3], 5*time.Minute+29*time.Second, "local backoff plus jitter must remain bounded")
}

func TestLocalStateDominatesDoesNotRestoreProbeAcrossStrongerSharedDeadline(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	local := cache.GitHubRateLimitState{
		Generation: 1, BlockedUntil: now.Add(time.Minute), ProbeToken: "premature-probe", ProbeUntil: now.Add(30 * time.Second),
	}
	tests := []struct {
		name     string
		remote   cache.GitHubRateLimitState
		expected bool
	}{
		{name: "missing record needs restoration", remote: cache.GitHubRateLimitState{}, expected: true},
		{name: "same generation tombstone wins", remote: cache.GitHubRateLimitState{Generation: 1}},
		{name: "stronger shared deadline fences stale local probe", remote: cache.GitHubRateLimitState{Generation: 1, BlockedUntil: now.Add(10 * time.Minute)}},
		{name: "same deadline may restore missing current probe", remote: cache.GitHubRateLimitState{Generation: 1, BlockedUntil: local.BlockedUntil}, expected: true},
		{name: "newer shared generation with shorter deadline needs evidence merge", remote: cache.GitHubRateLimitState{Generation: 2, BlockedUntil: now.Add(30 * time.Second)}, expected: true},
		{name: "newer shared generation with equal deadline wins", remote: cache.GitHubRateLimitState{Generation: 2, BlockedUntil: local.BlockedUntil}},
		{name: "newer shared completion tombstone wins", remote: cache.GitHubRateLimitState{Generation: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, localStateDominates(local, tt.remote), "dominance should preserve newer evidence without resurrecting stale probes")
		})
	}
}

func TestControllerNoHintProbeBackoffAndCrashRecovery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	controller := newControllerTestInstance(t, &now, nil)

	permit := requireAdmission(t, controller, Scope{InstallationID: 42}, "initial request should be admitted")
	result := controller.Observe(context.Background(), permit, Observation{StatusCode: http.StatusTooManyRequests})
	delays := []time.Duration{result.Deferral.RetryAt.Sub(now)}
	for range 3 {
		now = result.Deferral.RetryAt
		permit = requireProbeAdmission(t, controller, Scope{InstallationID: 42}, true, "eligible request should acquire the next recovery probe", "eligible request should be generation-fenced as a probe")
		result = controller.Observe(context.Background(), permit, Observation{StatusCode: http.StatusTooManyRequests})
		delays = append(delays, result.Deferral.RetryAt.Sub(now))
	}
	require.Greater(t, delays[1], delays[0], "first failed probe should increase no-hint backoff")
	require.Greater(t, delays[2], delays[1], "second failed probe should increase no-hint backoff")
	require.LessOrEqual(t, delays[3], 5*time.Minute+29*time.Second, "no-hint backoff must remain within the local recovery bound")

	now = result.Deferral.RetryAt
	requireProbeAdmission(t, controller, Scope{InstallationID: 42}, true, "eligible request should acquire a probe before the simulated crash", "simulated crashed request should own the probe lease")
	deferral := requireDeferral(t, controller, Scope{InstallationID: 42}, "another request should defer behind the live probe")
	require.Equal(t, now.Add(30*time.Second), deferral.RetryAt, "probe deferral should expose the exact lease expiry")
	now = deferral.RetryAt
	requireProbeAdmission(t, controller, Scope{InstallationID: 42}, true, "an expired probe lease should permit recovery", "recovery after a crashed probe should retain generation fencing")
}

func TestControllerSuccessfulLastAllowanceDefersOnlySubsequentWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
	}{
		{name: "successful last allowance", status: http.StatusOK},
		{name: "not modified with exhausted allowance", status: http.StatusNotModified},
		{name: "not found with exhausted allowance", status: http.StatusNotFound},
		{name: "validation failure with exhausted allowance", status: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
			reset := now.Add(10 * time.Minute)
			controller := newControllerTestInstance(t, &now, nil)
			permit := requireAdmission(t, controller, Scope{InstallationID: 42}, "last allowed request should be admitted")
			result := controller.Observe(context.Background(), permit, Observation{StatusCode: tt.status, Header: http.Header{
				"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(reset.Unix(), 10)}, "X-Ratelimit-Resource": []string{"core"},
			}})
			require.True(t, result.Classification.RateLimited, "exhausted success headers should open a cooldown for later work")
			require.Equal(t, "core", result.Deferral.Resource, "cooldown should retain provider resource attribution")
			deferral := requireDeferral(t, controller, Scope{InstallationID: 42}, "subsequent work should wait until reset")
			require.Equal(t, reset, deferral.RetryAt, "subsequent work should use the provider reset")
		})
	}
}

func TestControllerFallbackIsBoundedAndReportsSuppression(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	controller, err := NewController(Config{
		Mode: ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{41, 42, 43},
		Logger: zerolog.New(&logs), Now: func() time.Time { return now }, FallbackLimit: 2,
	})
	require.NoError(t, err, "controller should initialize without Redis")
	for _, installationID := range []int64{41, 42, 43} {
		permit, beforeErr := controller.Before(context.Background(), Scope{InstallationID: installationID})
		require.NoError(t, beforeErr, "initial fallback request should be admitted")
		controller.Observe(context.Background(), permit, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"X-Ratelimit-Resource": []string{"core"}}})
	}
	require.Len(t, controller.fallback.entries, 2, "process-local coordination must cap retained installations")
	requireDeferral(t, controller, Scope{InstallationID: 42, Caller: "pr_health", Route: "/repos/:owner/:repo/pulls/:id", SyncReason: "stale_reconcile"}, "known fallback cooldown should suppress enforce-mode work")

	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	var degradedEvents, deferralEvents int
	var deferralLog map[string]any
	for _, line := range lines {
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event), "controller telemetry should be valid JSON")
		switch event["message"] {
		case "github rate limit coordination degraded to process-local state":
			degradedEvents++
		case "github request deferred":
			deferralEvents++
			deferralLog = event
		}
	}
	require.Equal(t, 1, degradedEvents, "Redis degradation should emit one bounded process-local fallback event")
	require.Equal(t, 1, deferralEvents, "suppressed request should emit a separate deferral event")
	require.Equal(t, true, deferralLog["github_request_suppressed"], "enforcement telemetry should distinguish actual suppression")
	require.Equal(t, "core", deferralLog["github_rate_limit_resource"], "deferral telemetry should retain resource attribution")
	require.Equal(t, "/repos/:owner/:repo/pulls/:id", deferralLog["github_route"], "deferral telemetry should retain normalized route attribution")
	require.Equal(t, "stale_reconcile", deferralLog["github_sync_reason"], "deferral telemetry should retain sync attribution")
}

// Each fixture owns its clock, controller state, and optional Redis server.
func newControllerTestRedis(t *testing.T) (*cache.Client, *miniredis.Miniredis, func()) {
	t.Helper()
	server := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "isolated Redis client should initialize")
	closeRedis := sync.OnceFunc(func() { require.NoError(t, client.Close(), "isolated Redis client should close") })
	t.Cleanup(closeRedis)
	return client, server, closeRedis
}

func newControllerTestInstance(t *testing.T, now *time.Time, client *cache.Client) *Controller {
	t.Helper()
	controller, err := NewController(Config{
		Mode: ModeEnforce, Environment: "test", AppID: 1, InstallationAllowlist: []int64{42},
		Cache: client, Logger: zerolog.Nop(), Now: func() time.Time { return *now },
	})
	require.NoError(t, err, "isolated controller should initialize")
	return controller
}

func requireAdmission(t *testing.T, controller *Controller, scope Scope, message string) Permit {
	t.Helper()
	permit, err := controller.Before(context.Background(), scope)
	require.NoError(t, err, message)
	return permit
}

func requireProbeAdmission(t *testing.T, controller *Controller, scope Scope, probe bool, message, probeMessage string) Permit {
	t.Helper()
	permit := requireAdmission(t, controller, scope, message)
	require.Equal(t, probe, permit.Probe, probeMessage)
	return permit
}

func requireDeferral(t *testing.T, controller *Controller, scope Scope, message string) *Deferral {
	t.Helper()
	_, err := controller.Before(context.Background(), scope)
	var deferral *Deferral
	require.True(t, errors.As(err, &deferral), message)
	return deferral
}

func observeTestThrottle(controller *Controller, permit Permit, retryAfter string) ObservationResult {
	return controller.Observe(context.Background(), permit, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{retryAfter}}})
}

func requireStaleSnapshotOutage(t *testing.T, controller *Controller, now *time.Time, installationID int64) {
	t.Helper()
	scope := Scope{InstallationID: installationID}
	*now = observeTestThrottle(controller, requireAdmission(t, controller, scope, "stale-read scenario should admit its initial request"), "1").Deferral.RetryAt
	key := controller.key(installationID)
	stale, err := controller.cache.GetGitHubRateLimitState(context.Background(), key)
	require.NoError(t, err, "pre-claim Redis snapshot should be readable")
	requireProbeAdmission(t, controller, scope, true, "eligible request should claim the probe", "claim should establish probe authority")
	claimed := controller.fallback.get(key, *now)
	controller.fallback.remember(key, stale, *now)
	require.Equal(t, claimed, controller.fallback.get(key, *now), "delayed pre-claim snapshot must preserve all live probe fields")
	oldSnapshot, oldReadTime := claimed, *now
	*now = claimed.ProbeUntil
	probe := requireProbeAdmission(t, controller, scope, true, "expired probe should be reclaimed", "replacement should own a fresh probe token")
	claimed = controller.fallback.get(key, *now)
	controller.fallback.remember(key, oldSnapshot, oldReadTime)
	require.Equal(t, claimed, controller.fallback.get(key, *now), "delayed old-token read with its original timestamp must preserve the newer claim")
	sharedCache := controller.cache
	controller.cache = &cache.Client{} // A disconnected cache forces the same fallback path as a Redis outage.
	defer func() { controller.cache = sharedCache }()
	deferral := requireDeferral(t, controller, scope, "Redis outage must not admit a second probe before the remembered lease expires")
	require.Equal(t, claimed.ProbeUntil, deferral.RetryAt, "fallback should defer until the exact retained lease deadline")
	controller.Observe(context.Background(), probe, Observation{StatusCode: http.StatusOK})
	requireProbeAdmission(t, controller, scope, false, "current completion should reopen admission during the outage", "successful completion must not resurrect the remembered probe")
}

func TestControllerRetainsRecoveryGenerationAcrossOutages(t *testing.T) {
	t.Parallel()
	client, _, _ := newControllerTestRedis(t)
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	requireRecoveryOutage(t, newControllerTestInstance(t, &now, client), newControllerTestInstance(t, &now, client), &now, 42)
}

func requireRecoveryOutage(t *testing.T, a, b *Controller, now *time.Time, installationID int64) {
	t.Helper()
	scope, key := Scope{InstallationID: installationID}, a.key(installationID)
	*now = observeTestThrottle(a, requireAdmission(t, a, scope, "initial request should be admitted"), "1").Deferral.RetryAt
	probe := requireAdmission(t, a, scope, "expired cooldown should issue a probe")
	a.Observe(context.Background(), probe, Observation{StatusCode: http.StatusOK})
	for _, c := range []*Controller{a, b} {
		requireProbeAdmission(t, c, scope, false, "recovery should admit ordinary work", "local and learned tombstones must not issue probes")
		state, claimed := c.fallback.claim(key, "owner", "token", *now, now.Add(defaultProbeLease))
		require.False(t, claimed, "local tombstones must reject direct probe claims")
		require.Equal(t, cache.GitHubRateLimitState{Generation: 1, EpisodeCount: 1}, state, "both completing and observing processes should retain the full tombstone")
	}
	shared := b.cache
	b.cache = &cache.Client{}
	second := observeTestThrottle(b, requireAdmission(t, b, scope, "learned tombstone should remain nonblocking during an outage"), "60")
	require.Equal(t, int64(2), second.Deferral.Generation, "outage throttle must advance beyond the recovered generation")
	b.cache = shared
	deferral := requireDeferral(t, b, scope, "old shared tombstone must not erase the new local episode")
	require.Equal(t, second.Deferral.RetryAt, deferral.RetryAt, "reconciliation must retain the exact new deadline")
	_, opened, applied, degraded := a.observe(context.Background(), key, cache.GitHubRateLimitObservation{Now: *now, ProbeGeneration: probe.Generation, ProbeToken: probe.ProbeToken})
	require.Equal(t, [3]bool{}, [3]bool{opened, applied, degraded}, "stale rejection must remain unapplied without opening an episode or degrading Redis")
	sharedA := a.cache
	a.cache = &cache.Client{}
	require.Equal(t, second.Deferral.RetryAt, requireDeferral(t, a, scope, "rejected observation must retain the newer shared cooldown during immediate Redis loss").RetryAt, "fallback must honor the exact authoritative deadline")
	a.cache = sharedA
	*now = second.Deferral.RetryAt
	probe = requireAdmission(t, b, scope, "new generation should issue its recovery probe")
	b.Observe(context.Background(), probe, Observation{StatusCode: http.StatusOK})
	completed, _, _, _ := a.observe(context.Background(), key, cache.GitHubRateLimitObservation{Now: *now, ProbeGeneration: probe.Generation, ProbeToken: probe.ProbeToken})
	require.Equal(t, completed, a.fallback.get(key, *now), "rejected observation must also retain a learned completion tombstone")
	require.Equal(t, cache.GitHubRateLimitState{Generation: 2, EpisodeCount: 2}, b.fallback.get(key, *now), "local completion must retain generation and episode history")
	b.fallback.remember(key, b.fallback.get(key, *now), now.Add(time.Minute))
	require.Equal(t, cache.GitHubRateLimitState{}, b.fallback.get(key, now.Add(recoveryStateTTL)), "recovery tombstones must expire within the bounded retention window")
}

func TestControllerOfflineCompletionPreservesUnseenRemoteEvidence(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		probe bool
	}{{name: "future cooldown"}, {name: "live remote probe", probe: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _, _ := newControllerTestRedis(t)
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			requireOfflineCompletionSafety(t, newControllerTestInstance(t, &now, client), newControllerTestInstance(t, &now, client), &now, 42, tt.probe)
		})
	}
}

func requireOfflineCompletionSafety(t *testing.T, a, b *Controller, now *time.Time, installationID int64, remoteProbe bool) {
	t.Helper()
	scope := Scope{InstallationID: installationID}
	initial := requireAdmission(t, b, scope, "initial request should be admitted")
	first := observeTestThrottle(b, initial, "10")
	requireDeferral(t, a, scope, "offline process should learn the original deadline")
	shared := a.cache
	a.cache = &cache.Client{}
	*now = now.Add(9 * time.Second)
	extended := observeTestThrottle(b, initial, "91")
	*now = first.Deferral.RetryAt
	localProbe := requireAdmission(t, a, scope, "offline process should probe at its known deadline")
	a.Observe(context.Background(), localProbe, Observation{StatusCode: http.StatusOK})
	if remoteProbe {
		*now = extended.Deferral.RetryAt
		requireAdmission(t, b, scope, "remote process should hold the current probe")
	}
	before, err := shared.GetGitHubRateLimitState(context.Background(), a.key(installationID))
	require.NoError(t, err, "remote evidence should be readable before reconnect")
	a.cache = shared
	deferral := requireDeferral(t, a, scope, "offline completion must not clear unseen remote evidence")
	require.Equal(t, strongestStateDeadline(before.BlockedUntil, before), deferral.RetryAt, "reconnect should defer until the remote cooldown or live lease ends")
	after, err := shared.GetGitHubRateLimitState(context.Background(), a.key(installationID))
	require.NoError(t, err, "remote evidence should remain readable after reconnect")
	require.Equal(t, before, after, "offline success must preserve all remote generation, cooldown and probe fields")
}
