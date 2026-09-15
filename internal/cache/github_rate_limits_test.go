package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGitHubRateLimitObservationPreservesEffectiveDeadlineMetadata(t *testing.T) {
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
			client, _ := testRedisClient(t)
			now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
			key := "{ghrl:v1:test:app:1:inst:42}:state"
			expected := GitHubRateLimitState{
				Generation: 1, EpisodeCount: 1, BlockedUntil: now.Add(3 * time.Hour),
				Kind: "primary", Resource: "core", Reason: "primary_reset",
			}
			requireGitHubObservation(t, client, key, GitHubRateLimitObservation{
				Now: now, BlockedUntil: expected.BlockedUntil, Kind: expected.Kind,
				Resource: expected.Resource, Reason: expected.Reason, TTL: 5 * time.Hour,
			}, true, true, "initial primary observation should establish cooldown attribution")
			observation := GitHubRateLimitObservation{
				Now: now.Add(time.Second), BlockedUntil: now.Add(tt.duration),
				Kind: "secondary", Resource: "graphql", Reason: "retry_after", TTL: 5 * time.Hour,
			}
			if tt.replaces {
				expected.BlockedUntil = observation.BlockedUntil
				expected.Kind, expected.Resource, expected.Reason = observation.Kind, observation.Resource, observation.Reason
			}
			actual := requireGitHubObservation(t, client, key, observation, false, true, "concurrent observation should merge atomically")
			require.Equal(t, expected, actual, "retained deadline and metadata should come from the same observation")
		})
	}
}

func TestGitHubRateLimitScriptsPreserveGenerationAndProbeLease(t *testing.T) {
	t.Parallel()

	client, redisServer := testRedisClient(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	key := "{ghrl:v1:test:app:1:inst:42}:state"

	first := requireGitHubObservation(t, client, key, secondaryTestObservation(now, now.Add(time.Minute), "throttle"), true, true, "first throttle observation should execute atomically")
	require.Equal(t, int64(1), first.Generation, "first throttle should create generation one")
	require.Equal(t, int64(1), first.EpisodeCount, "first throttle should count one episode")

	concurrent := requireGitHubObservation(t, client, key, secondaryTestObservation(now.Add(time.Second), now.Add(2*time.Minute), "same episode"), false, true, "concurrent throttle observation should execute atomically")
	require.Equal(t, int64(1), concurrent.Generation, "same episode should retain its generation")
	require.Equal(t, now.Add(2*time.Minute), concurrent.BlockedUntil, "same episode may only extend the deadline")

	probe := requireGitHubProbeClaim(t, client, key, "worker-a", "probe-a-1", now.Add(2*time.Minute), now.Add(2*time.Minute+30*time.Second), true, "first eligible controller should own the probe")
	require.Equal(t, int64(1), probe.ProbeGeneration, "probe should be fenced to the current generation")
	require.Equal(t, "probe-a-1", probe.ProbeToken, "probe should carry its unique claim token")

	requireGitHubProbeClaim(t, client, key, "worker-b", "probe-b-1", now.Add(2*time.Minute), now.Add(2*time.Minute+30*time.Second), false, "a live probe lease should suppress competing probes")

	throttledProbe := requireGitHubObservation(t, client, key, GitHubRateLimitObservation{
		Now: now.Add(2 * time.Minute), BlockedUntil: now.Add(5 * time.Minute), Kind: "secondary", Reason: "probe throttled", ProbeGeneration: 1, ProbeToken: "probe-a-1", TTL: 3 * time.Hour,
	}, true, true, "throttled probe should atomically advance the generation")
	require.Equal(t, int64(2), throttledProbe.Generation, "throttled probe should fence delayed generation-one success")

	requireGitHubProbeCompletion(t, client, key, "worker-a", "probe-a-1", 1, true, false, "late success must not clear newer throttle evidence")

	staleLeaseKey := "{ghrl:v1:test:app:1:inst:43}:state"
	requireGitHubObservation(t, client, staleLeaseKey, secondaryTestObservation(now, now.Add(time.Minute), "first episode"), true, true, "separate cooldown should initialize")
	requireGitHubProbeClaim(t, client, staleLeaseKey, "worker-a", "probe-a-old", now.Add(time.Minute), now.Add(90*time.Second), true, "separate probe should hold the first generation lease")
	advanced := requireGitHubObservation(t, client, staleLeaseKey, secondaryTestObservation(now.Add(time.Minute), now.Add(2*time.Minute), "independent throttle"), true, true, "independent throttle should atomically supersede an old lease")
	require.Equal(t, int64(2), advanced.Generation, "independent evidence should create a new fenced generation")
	state, err := client.GetGitHubRateLimitState(ctx, staleLeaseKey)
	require.NoError(t, err, "advanced state should remain readable")
	require.Empty(t, state.ProbeOwner, "generation advance should remove the stale probe owner")
	require.Zero(t, state.ProbeGeneration, "generation advance should remove the stale probe generation")

	retentionKey := "{ghrl:v1:test:app:1:inst:44}:state"
	longDeadline := now.Add(3 * time.Hour)
	requireGitHubObservation(t, client, retentionKey, GitHubRateLimitObservation{
		Now: now, BlockedUntil: longDeadline, Kind: "primary", Reason: "long reset", TTL: 2*time.Hour + 10*time.Minute,
	}, true, true, "long server deadline should initialize retained state")
	requireGitHubObservation(t, client, retentionKey, GitHubRateLimitObservation{
		Now: now.Add(time.Second), BlockedUntil: now.Add(time.Minute), Kind: "secondary", Reason: "shorter concurrent throttle", TTL: 2*time.Hour + 10*time.Minute,
	}, false, true, "shorter observation should update without shortening retention")
	require.GreaterOrEqual(t, redisServer.TTL(retentionKey), 3*time.Hour, "record TTL should retain the effective maximum server deadline")
}

func TestGitHubRateLimitProbeTokenFencesSameOwnerReclaims(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	key := "{ghrl:v1:test:app:1:inst:45}:state"
	requireGitHubObservation(t, client, key, secondaryTestObservation(now, now.Add(time.Minute), "throttle"), true, true, "cooldown should initialize")

	oldProbe := requireGitHubProbeClaim(t, client, key, "same-worker", "old-claim", now.Add(time.Minute), now.Add(90*time.Second), true, "old probe should hold the first lease")
	newProbe := requireGitHubProbeClaim(t, client, key, "same-worker", "new-claim", now.Add(90*time.Second), now.Add(2*time.Minute), true, "new token should distinguish the reclaimed lease")
	require.Equal(t, oldProbe.Generation, newProbe.Generation, "lease reclaim should not advance the throttle generation")

	requireGitHubProbeCompletion(t, client, key, "same-worker", "old-claim", oldProbe.Generation, true, false, "late old success must not clear the reclaimed lease")
	requireGitHubProbeCompletion(t, client, key, "same-worker", "old-claim", oldProbe.Generation, false, false, "late old error must not release the reclaimed lease")
	observation := GitHubRateLimitObservation{
		Now: now.Add(95 * time.Second), BlockedUntil: now.Add(4 * time.Minute), Kind: "secondary", Reason: "late old throttle",
		ProbeGeneration: oldProbe.Generation, ProbeToken: "old-claim", TTL: 3 * time.Hour,
	}
	state := requireGitHubObservation(t, client, key, observation, false, false, "late old throttle should be fenced atomically")
	require.Equal(t, newProbe, state, "newer same-owner lease and its complete state should remain authoritative")

	requireGitHubProbeCompletion(t, client, key, "same-worker", "new-claim", newProbe.Generation, true, true, "current unique token should close the episode")
	state = requireGitHubObservation(t, client, key, observation, false, false, "late throttle should return the completed tombstone")
	require.Equal(t, GitHubRateLimitState{Generation: 1, EpisodeCount: 1}, state, "rejected observation should retain completed generation without stale fields")
	state = requireGitHubObservation(t, client, key+":missing", observation, false, false, "unknown probe should remain fenced after record loss")
	require.Equal(t, GitHubRateLimitState{}, state, "missing record snapshot should decode to an empty state")
}

func TestGitHubRateLimitReconcileRestoresOnlyMissingOrOlderState(t *testing.T) {
	t.Parallel()

	client, redisServer := testRedisClient(t)
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	key := "{ghrl:v1:test:app:1:inst:46}:state"
	local := GitHubRateLimitState{
		Generation: 2, EpisodeCount: 2, BlockedUntil: now.Add(10 * time.Minute),
		Kind: "secondary", Resource: "graphql", Reason: "retained local throttle",
		ProbeOwner: "worker-a", ProbeToken: "probe-2", ProbeGeneration: 2, ProbeUntil: now.Add(30 * time.Second),
	}

	reconciled := requireGitHubReconciliation(t, client, key, local, now, true, "missing Redis state should be reconciled atomically")
	require.Equal(t, local, reconciled, "reconciliation should restore the complete cooldown and probe fence")

	redisServer.Del(key)
	requireGitHubObservation(t, client, key, secondaryTestObservation(now, now.Add(time.Minute), "rolled-back generation"), true, true, "older Redis generation should be created for rollback simulation")
	reconciled = requireGitHubReconciliation(t, client, key, local, now, true, "older Redis state should be reconciled atomically")
	require.Equal(t, local, reconciled, "rolled-back Redis should recover the retained generation and token")

	strongerRemoteKey := "{ghrl:v1:test:app:1:inst:47}:state"
	strongerDeadline := now.Add(20 * time.Minute)
	requireGitHubObservation(t, client, strongerRemoteKey, GitHubRateLimitObservation{
		Now: now, BlockedUntil: strongerDeadline, Kind: "primary", Resource: "core", Reason: "newer provider deadline", TTL: 3 * time.Hour,
	}, true, true, "post-loss Redis evidence should initialize")
	retainedOlderDeadline := local
	retainedOlderDeadline.BlockedUntil = now.Add(10 * time.Minute)
	retainedOlderDeadline.ProbeOwner, retainedOlderDeadline.ProbeToken, retainedOlderDeadline.ProbeGeneration, retainedOlderDeadline.ProbeUntil = "", "", 0, time.Time{}
	reconciled = requireGitHubReconciliation(t, client, strongerRemoteKey, retainedOlderDeadline, now, true, "generation reconciliation should preserve stronger post-loss evidence")
	require.Equal(t, retainedOlderDeadline.Generation, reconciled.Generation, "reconciliation should restore the higher generation")
	require.Equal(t, strongerDeadline, reconciled.BlockedUntil, "reconciliation must not shorten stronger provider evidence")
	require.Equal(t, "newer provider deadline", reconciled.Reason, "deadline attribution should follow the retained strongest evidence")

	liveProbeKey := "{ghrl:v1:test:app:1:inst:48}:state"
	requireGitHubObservation(t, client, liveProbeKey, secondaryTestObservation(now, now.Add(time.Minute), "short rolled-back deadline"), true, true, "live-probe state should initialize")
	remoteProbe := requireGitHubProbeClaim(t, client, liveProbeKey, "remote-worker", "remote-probe", now.Add(time.Minute), now.Add(90*time.Second), true, "post-loss probe should hold its lease")
	retainedWithDeadline := remoteProbe
	retainedWithDeadline.BlockedUntil = now.Add(10 * time.Minute)
	retainedWithDeadline.Kind, retainedWithDeadline.Reason = "primary", "retained longer deadline"
	retainedWithDeadline.ProbeOwner, retainedWithDeadline.ProbeToken = "local-worker", "local-probe"
	reconciled = requireGitHubReconciliation(t, client, liveProbeKey, retainedWithDeadline, now.Add(time.Minute), true, "retained deadline should reconcile while fencing a prematurely admitted probe")
	require.Equal(t, retainedWithDeadline.BlockedUntil, reconciled.BlockedUntil, "known server deadline should survive a different live probe")
	require.Empty(t, reconciled.ProbeToken, "probe admitted against the shorter deadline should lose completion authority")
	requireGitHubProbeCompletion(t, client, liveProbeKey, "remote-worker", "remote-probe", remoteProbe.Generation, true, false, "premature success must not clear the restored future deadline")
	requireGitHubProbeCompletion(t, client, liveProbeKey, "remote-worker", "remote-probe", remoteProbe.Generation, false, false, "premature failure must not release restored state")
	afterLateThrottle := requireGitHubObservation(t, client, liveProbeKey, GitHubRateLimitObservation{
		Now: now.Add(2 * time.Minute), BlockedUntil: now.Add(20 * time.Minute), Kind: "secondary", Reason: "late premature throttle",
		ProbeGeneration: remoteProbe.Generation, ProbeToken: "remote-probe", TTL: 3 * time.Hour,
	}, false, false, "late premature throttle should be checked against the restored state")
	require.Equal(t, retainedWithDeadline.BlockedUntil, afterLateThrottle.BlockedUntil, "late outcomes must preserve the exact restored deadline")
	blockedClaim := requireGitHubProbeClaim(t, client, liveProbeKey, "other-worker", "too-early", now.Add(2*time.Minute), now.Add(150*time.Second), false, "restored cooldown should exclude all probes until its exact deadline")
	require.Equal(t, retainedWithDeadline.BlockedUntil, blockedClaim.BlockedUntil, "early claimant should receive the restored exact deadline")
	currentProbe := requireGitHubProbeClaim(t, client, liveProbeKey, "other-worker", "current-probe", retainedWithDeadline.BlockedUntil, retainedWithDeadline.BlockedUntil.Add(30*time.Second), true, "one new probe should recover normally after the restored deadline")
	requireGitHubProbeCompletion(t, client, liveProbeKey, "other-worker", "current-probe", currentProbe.Generation, true, true, "current probe should publish normal recovery")

	advancedRollbackKey := "{ghrl:v1:test:app:1:inst:49}:state"
	short := requireGitHubObservation(t, client, advancedRollbackKey, secondaryTestObservation(now, now.Add(time.Minute), "rolled-back deadline"), true, true, "rolled-back state should initialize before a premature probe")
	premature := requireGitHubProbeClaim(t, client, advancedRollbackKey, "remote-worker", "premature-probe", short.BlockedUntil, short.BlockedUntil.Add(30*time.Second), true, "premature probe should hold the rolled-back generation")
	advanced := requireGitHubObservation(t, client, advancedRollbackKey, GitHubRateLimitObservation{
		Now: short.BlockedUntil, BlockedUntil: now.Add(2 * time.Minute), Kind: "secondary", Reason: "premature probe throttled",
		ProbeGeneration: premature.Generation, ProbeToken: premature.ProbeToken, TTL: 3 * time.Hour,
	}, true, true, "premature throttle should advance the rolled-back state")
	require.Equal(t, int64(2), advanced.Generation, "premature throttle should demonstrate the higher-generation ordering")
	retainedAcrossGeneration := GitHubRateLimitState{
		Generation: 1, EpisodeCount: 1, BlockedUntil: now.Add(10 * time.Minute),
		Kind: "primary", Resource: "core", Reason: "retained provider reset",
	}
	reconciled = requireGitHubReconciliation(t, client, advancedRollbackKey, retainedAcrossGeneration, short.BlockedUntil, true, "stronger retained deadline should reconcile across the rollback-advanced generation")
	require.Equal(t, advanced.Generation, reconciled.Generation, "deadline reconciliation must retain current lease generation authority")
	require.Equal(t, retainedAcrossGeneration.BlockedUntil, reconciled.BlockedUntil, "reconciliation should restore the exact retained provider deadline")
	require.Equal(t, retainedAcrossGeneration.Reason, reconciled.Reason, "stronger deadline attribution should follow the retained evidence")
	require.Empty(t, reconciled.ProbeToken, "deadline restoration should fence any probe admitted against superseded evidence")

	tombstoneKey := "{ghrl:v1:test:app:1:inst:50}:state"
	tombstoneState := requireGitHubObservation(t, client, tombstoneKey, secondaryTestObservation(now, now.Add(time.Minute), "completed state"), true, true, "tombstone state should initialize")
	tombstoneProbe := requireGitHubProbeClaim(t, client, tombstoneKey, "current-worker", "current-probe", tombstoneState.BlockedUntil, tombstoneState.BlockedUntil.Add(30*time.Second), true, "current probe should hold the episode")
	requireGitHubProbeCompletion(t, client, tombstoneKey, "current-worker", "current-probe", tombstoneProbe.Generation, true, true, "current success should complete normally")
	retainedBeforeCompletion := retainedAcrossGeneration
	retainedBeforeCompletion.Generation = tombstoneProbe.Generation
	reconciled = requireGitHubReconciliation(t, client, tombstoneKey, retainedBeforeCompletion, now, false, "retained evidence should be checked against the completion tombstone")
	require.True(t, reconciled.BlockedUntil.IsZero(), "completion tombstone should remain authoritative")

	requireGitHubProbeCompletion(t, client, key, local.ProbeOwner, local.ProbeToken, local.Generation, true, true, "restored unique probe token should retain completion authority")
	reconciled = requireGitHubReconciliation(t, client, key, local, now, false, "reconciliation against a current tombstone should remain readable")
	requireGitHubProbeClaim(t, client, key, "worker", "no-probe", now, now.Add(time.Minute), false, "recovered Redis tombstones must not acquire probes")
	reconciled = requireGitHubReconciliation(t, client, key+":restored", reconciled, now, true, "a missing Redis record should retain the known recovery generation")
	require.Equal(t, local.Generation, reconciled.Generation, "tombstone should preserve the recovered generation")
	require.True(t, reconciled.BlockedUntil.IsZero(), "tombstone should keep the episode recovered")
}

func requireGitHubProbeClaim(t *testing.T, client *Client, key, owner, token string, now, leaseUntil time.Time, expected bool, message string) GitHubRateLimitState {
	t.Helper()
	state, claimed, err := client.ClaimGitHubRateLimitProbe(context.Background(), key, owner, token, now, leaseUntil, 3*time.Hour)
	require.NoError(t, err, message)
	require.Equal(t, expected, claimed, message)
	return state
}

func requireGitHubProbeCompletion(t *testing.T, client *Client, key, owner, token string, generation int64, success, expected bool, message string) {
	t.Helper()
	completed, err := client.CompleteGitHubRateLimitProbe(context.Background(), key, owner, token, generation, success, 3*time.Hour)
	require.NoError(t, err, message)
	require.Equal(t, expected, completed, message)
}

func requireGitHubObservation(t *testing.T, client *Client, key string, observation GitHubRateLimitObservation, expectOpened, expectApplied bool, message string) GitHubRateLimitState {
	t.Helper()
	atomicClient := *client
	atomicClient.rdb = githubObservationWithoutRead{client.rdb}
	state, opened, applied, err := atomicClient.ObserveGitHubRateLimit(context.Background(), key, observation)
	require.NoError(t, err, message)
	require.Equal(t, expectOpened, opened, "%s: episode transition", message)
	require.Equal(t, expectApplied, applied, "%s: evidence application", message)
	return state
}

func requireGitHubReconciliation(t *testing.T, client *Client, key string, retained GitHubRateLimitState, now time.Time, expected bool, message string) GitHubRateLimitState {
	t.Helper()
	state, applied, err := client.ReconcileGitHubRateLimitState(context.Background(), key, retained, now, 3*time.Hour)
	require.NoError(t, err, message)
	require.Equal(t, expected, applied, "%s: retained evidence application", message)
	return state
}

func secondaryTestObservation(now, deadline time.Time, reason string) GitHubRateLimitObservation {
	return GitHubRateLimitObservation{
		Now: now, BlockedUntil: deadline, Kind: "secondary", Reason: reason, TTL: 3 * time.Hour,
	}
}

// An observation must return its complete script snapshot even if a later read
// would fail; replaying an already rejected observation locally is unsafe.
type githubObservationWithoutRead struct{ redis.UniversalClient }

func (githubObservationWithoutRead) HGetAll(context.Context, string) *redis.MapStringStringCmd {
	return redis.NewMapStringStringResult(nil, errors.New("unexpected post-observation HGETALL"))
}
