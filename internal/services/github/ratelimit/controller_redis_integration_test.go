//go:build redis_integration

package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/cache"
)

func TestRedisIntegrationStandaloneCoordinatesIndependentControllers(t *testing.T) {
	t.Parallel()

	redisURL := os.Getenv("GITHUB_RATE_LIMIT_REDIS_URL")
	require.NotEmpty(t, redisURL, "GITHUB_RATE_LIMIT_REDIS_URL is required; skipped infrastructure is not passing evidence")
	options, err := redis.ParseURL(redisURL)
	require.NoError(t, err, "standalone Redis URL should parse")
	ttlClient := redis.NewClient(options)
	t.Cleanup(func() { require.NoError(t, ttlClient.Close(), "standalone TTL client should close") })
	runRedisControllerContract(t, func() *cache.Client {
		return cache.New(cache.Config{Topology: "standalone", URL: redisURL}, zerolog.Nop(), nil)
	}, ttlClient, 7001)
}

func TestRedisIntegrationClusterCoordinatesIndependentControllers(t *testing.T) {
	t.Parallel()

	addrs := cache.ParseAddrs(os.Getenv("GITHUB_RATE_LIMIT_REDIS_CLUSTER_ADDRS"))
	require.NotEmpty(t, addrs, "GITHUB_RATE_LIMIT_REDIS_CLUSTER_ADDRS is required; skipped infrastructure is not passing evidence")
	ttlClient := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { require.NoError(t, ttlClient.Close(), "cluster TTL client should close") })
	runRedisControllerContract(t, func() *cache.Client {
		return cache.New(cache.Config{Topology: "cluster", Addrs: addrs}, zerolog.Nop(), nil)
	}, ttlClient, 7002)
}

func runRedisControllerContract(t *testing.T, newClient func() *cache.Client, ttlClient redis.UniversalClient, appID int64) {
	t.Helper()
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	newController := func(owner string) *Controller {
		t.Helper()
		client := newClient()
		require.NotNil(t, client, "independent Redis client should initialize for %s", owner)
		t.Cleanup(func() { require.NoError(t, client.Close(), "independent Redis client should close for %s", owner) })
		controller, err := NewController(Config{Mode: ModeEnforce, Environment: "integration", AppID: appID,
			InstallationAllowlist: []int64{42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54},
			Cache:                 client, Logger: zerolog.Nop(), Now: func() time.Time { return now }, Owner: owner})
		require.NoError(t, err, "independent controller should initialize for %s", owner)
		return controller
	}
	controllerA, controllerB, controllerC := newController("controller-a"), newController("controller-b"), newController("controller-c")

	permit := requireAdmission(t, controllerA, Scope{InstallationID: 42, Caller: "sync"}, "initial request should be admitted")
	first := controllerA.Observe(context.Background(), permit, Observation{StatusCode: http.StatusForbidden, Header: http.Header{"Retry-After": []string{"1"}}, Message: "secondary rate limit"})
	require.True(t, first.EpisodeOpened, "first throttle should open exactly one Redis episode")
	require.Equal(t, int64(1), first.Deferral.Generation, "first Redis episode should use generation one")

	requireDeferral(t, controllerB, Scope{InstallationID: 42, Caller: "review"}, "second process should honor the shared cooldown")

	now = now.Add(time.Second)
	probe := requireProbeAdmission(t, controllerA, Scope{InstallationID: 42, Caller: "sync"}, true, "first process should claim the recovery probe", "claimed request should carry probe fencing metadata")
	requireDeferral(t, controllerB, Scope{InstallationID: 42, Caller: "review"}, "second process should defer behind the shared probe lease")

	throttledProbe := controllerA.Observe(context.Background(), probe, Observation{StatusCode: http.StatusTooManyRequests, Message: "secondary rate limit"})
	require.True(t, throttledProbe.EpisodeOpened, "throttled probe should advance the episode once")
	require.Equal(t, int64(2), throttledProbe.Deferral.Generation, "throttled probe should advance the Redis generation")
	controllerA.Observe(context.Background(), probe, Observation{StatusCode: http.StatusOK, Header: make(http.Header)})
	requireDeferral(t, controllerB, Scope{InstallationID: 42, Caller: "review"}, "late generation-one success must not clear generation-two throttle evidence")

	now = throttledProbe.Deferral.RetryAt
	recoveryProbe := requireProbeAdmission(t, controllerB, Scope{InstallationID: 42, Caller: "review"}, true, "expired cooldown should permit one new recovery probe", "new recovery probe should be generation fenced")
	controllerB.Observe(context.Background(), recoveryProbe, Observation{StatusCode: http.StatusOK, Header: make(http.Header)})
	requireAdmission(t, controllerA, Scope{InstallationID: 42, Caller: "sync"}, "successful current-generation probe should clear the shared episode")

	requireSameOwnerRecovery(t, controllerA, controllerB, &now, 43)
	requireProbeAdmission(t, controllerB, Scope{InstallationID: 43}, false, "current token should recover the installation across processes", "remote recovery should admit ordinary work")

	requireLongDeadlineRetention(t, controllerA, controllerB, &now, 44)
	ttl, err := ttlClient.PTTL(context.Background(), controllerA.key(44)).Result()
	require.NoError(t, err, "real Redis should expose the retained record TTL")
	require.GreaterOrEqual(t, ttl, 3*time.Hour, "short observation must not reduce real Redis retention below the effective long deadline")

	requireConcurrentExtension(t, controllerA, controllerB, &now, 45)

	requireRecordLossRecovery(t, controllerA, controllerB, &now, 46, func(key string) {
		require.NoError(t, ttlClient.Del(context.Background(), key).Err(), "real Redis record should be deleted for loss simulation")
	})

	initial := requireAdmission(t, controllerA, Scope{InstallationID: 47, Caller: "sync"}, "rollback installation should admit its first request")
	first = controllerA.Observe(context.Background(), initial, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}}})
	now = first.Deferral.RetryAt
	probe = requireAdmission(t, controllerA, Scope{InstallationID: 47, Caller: "sync"}, "first generation should issue a probe")
	second := controllerA.Observe(context.Background(), probe, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"120"}}})
	require.Equal(t, int64(2), second.Deferral.Generation, "failed probe should create the retained newer generation")
	requireDeferral(t, controllerB, Scope{InstallationID: 47, Caller: "review"}, "independent controller should retain generation two locally")
	rollbackKey := fmt.Sprintf("{ghrl:v1:integration:app:%d:inst:47}:state", appID)
	require.NoError(t, ttlClient.Del(context.Background(), rollbackKey).Err(), "real Redis state should be cleared before rollback simulation")
	require.NoError(t, ttlClient.HSet(context.Background(), rollbackKey,
		"generation", 1, "episode_count", 1, "blocked_until", first.Deferral.RetryAt.UnixMilli(),
		"kind", "secondary", "resource", "", "reason", "rolled back").Err(), "older generation should be installed for rollback simulation")
	deferral := requireDeferral(t, controllerB, Scope{InstallationID: 47, Caller: "review"}, "retained newer generation should replace rolled-back Redis state")
	require.Equal(t, second.Deferral.RetryAt, deferral.RetryAt, "rollback reconciliation should preserve the newer exact cooldown")
	now = second.Deferral.RetryAt
	recoveryProbe = requireAdmission(t, controllerB, Scope{InstallationID: 47, Caller: "review"}, "reconciled newer generation should issue one recovery probe")
	controllerB.Observe(context.Background(), recoveryProbe, Observation{StatusCode: http.StatusOK, Header: make(http.Header)})
	requireProbeAdmission(t, controllerA, Scope{InstallationID: 47, Caller: "sync"}, false, "successful reconciled probe should restore ordinary admission", "rolled-back episode should end with a non-probe admission")

	initial = requireAdmission(t, controllerA, Scope{InstallationID: 48, Caller: "sync"}, "stale-observation installation should admit its first request")
	first = controllerA.Observe(context.Background(), initial, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}}})
	now = first.Deferral.RetryAt
	oldProbe := requireAdmission(t, controllerA, Scope{InstallationID: 48, Caller: "sync"}, "old probe should claim the stale-observation episode")
	now = now.Add(defaultProbeLease)
	newProbe := requireAdmission(t, controllerA, Scope{InstallationID: 48, Caller: "sync"}, "new probe should reclaim the expired stale-observation lease")
	current := controllerA.Observe(context.Background(), newProbe, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"600"}}})
	staleKey := fmt.Sprintf("{ghrl:v1:integration:app:%d:inst:48}:state", appID)
	beforeStale, err := ttlClient.HGetAll(context.Background(), staleKey).Result()
	require.NoError(t, err, "current shared episode should be readable before stale response")
	staleThrottle := controllerA.Observe(context.Background(), oldProbe, Observation{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"5"}}})
	require.False(t, staleThrottle.EpisodeOpened, "stale Redis token should not open an episode")
	require.Equal(t, current.Deferral.RetryAt, staleThrottle.Deferral.RetryAt, "stale Redis response should return the stronger current shared deadline")
	afterStale, err := ttlClient.HGetAll(context.Background(), staleKey).Result()
	require.NoError(t, err, "current shared episode should remain readable after stale response")
	require.Equal(t, beforeStale, afterStale, "stale response should not mutate the current generation or probe fields")

	requireRollbackRecovery(t, controllerA, controllerB, controllerC, &now, 49, false, func(key string, deadline time.Time) {
		require.NoError(t, ttlClient.HSet(context.Background(), key, "blocked_until", deadline.UnixMilli()).Err(), "same generation should roll back to a shorter deadline")
		require.NoError(t, ttlClient.HDel(context.Background(), key, "probe_owner", "probe_token", "probe_generation", "probe_until").Err(), "rollback should begin without a probe")
	})

	requireRollbackRecovery(t, controllerA, controllerB, controllerC, &now, 50, true, func(key string, deadline time.Time) {
		require.NoError(t, ttlClient.HSet(context.Background(), key, "blocked_until", deadline.UnixMilli()).Err(), "Redis should roll back the retained deadline")
	})
	requireStaleSnapshotOutage(t, controllerA, &now, 51)
	requireRecoveryOutage(t, controllerA, controllerB, &now, 52)
	requireOfflineCompletionSafety(t, controllerA, controllerB, &now, 53, false)
	requireOfflineCompletionSafety(t, controllerA, controllerB, &now, 54, true)

}
