//go:build redis_integration

package ratelimit

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// These mutation tests require the existing local owned-child Redis harness,
// rather than trusting an arbitrary endpoint supplied through the environment.
func requireOwnedQuotaRedisFixture(t *testing.T, client redis.UniversalClient) {
	t.Helper()
	var addrs []string
	switch typed := client.(type) {
	case *redis.Client:
		addrs = []string{typed.Options().Addr}
	case *redis.ClusterClient:
		addrs = typed.Options().Addrs
	default:
		t.Fatal("quota contract requires a known local Redis fixture client")
	}
	runDir := filepath.Dir(os.Getenv("TMPDIR"))
	require.True(t, strings.HasPrefix(filepath.Base(runDir), "run."), "quota mutation tests require the isolated harness run directory")
	for _, addr := range addrs {
		host, portText, err := net.SplitHostPort(addr)
		require.NoError(t, err, "owned Redis fixture address should parse")
		require.Equal(t, "127.0.0.1", host, "quota contract must never mutate a remote Redis server")
		port, err := strconv.Atoi(portText)
		require.NoError(t, err, "owned Redis fixture port should parse")
		require.GreaterOrEqual(t, port, 24000, "quota fixture must use the harness's reserved test port range")
		require.Less(t, port, 34000, "quota fixture must use the harness's reserved test port range")
		logPath := filepath.Join(runDir, portText, "redis.log")
		stat, err := os.Stat(logPath)
		require.NoError(t, err, "owned Redis fixture must have this run's startup log")
		require.LessOrEqual(t, stat.Size(), int64(1024*1024), "owned fixture startup log should be bounded")
		startupLog, err := os.ReadFile(logPath)
		require.NoError(t, err, "owned fixture startup log should be readable")
		probe := redis.NewClient(&redis.Options{Addr: addr})
		info, err := probe.Info(context.Background(), "server").Result()
		require.NoError(t, probe.Close(), "read-only fixture ownership probe should close")
		require.NoError(t, err, "owned fixture should identify its actual serving process")
		var processID string
		for _, line := range strings.Split(info, "\n") {
			if value, found := strings.CutPrefix(strings.TrimSpace(line), "process_id:"); found {
				processID = value
			}
		}
		require.Regexp(t, `^[0-9]+$`, processID, "serving fixture process should identify itself")
		require.Regexp(t, `(?m)^`+regexp.QuoteMeta(processID)+`:[A-Z] `, string(startupLog), "serving process must match this harness's owned child startup log")
	}
}

func runRedisQuotaFamiliesContract(t *testing.T, newClient func() *cache.Client, raw redis.UniversalClient, appID int64) {
	t.Helper()
	requireOwnedQuotaRedisFixture(t, raw)
	tests := []struct {
		name                            string
		firstReset, secondReset         time.Duration
		firstRemaining, secondRemaining int64
		laterLow                        bool
	}{
		{name: "earlier healthy then later low", firstReset: time.Minute, secondReset: 2 * time.Minute, firstRemaining: 90, secondRemaining: 10, laterLow: true},
		{name: "later low then earlier healthy", firstReset: 2 * time.Minute, secondReset: time.Minute, firstRemaining: 10, secondRemaining: 90, laterLow: true},
		{name: "earlier low then later healthy", firstReset: time.Minute, secondReset: 2 * time.Minute, firstRemaining: 10, secondRemaining: 90},
		{name: "later healthy then earlier low", firstReset: 2 * time.Minute, secondReset: time.Minute, firstRemaining: 90, secondRemaining: 10},
		{name: "both low", firstReset: 2 * time.Minute, secondReset: time.Minute, firstRemaining: 5, secondRemaining: 10, laterLow: true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, b := newClient(), newClient()
			require.NotNil(t, a, "first independent quota client should initialize")
			require.NotNil(t, b, "second independent quota client should initialize")
			t.Cleanup(func() {
				require.NoError(t, a.Close(), "first independent quota client should close")
				require.NoError(t, b.Close(), "second independent quota client should close")
			})
			ctx := context.Background()
			key := fmt.Sprintf("{ghrl:v1:family-integration:app:%d:inst:42}:quota:core", appID+int64(i))
			familiesKey := key + ":families:v2"
			require.NoError(t, raw.Del(ctx, key, familiesKey, key+":admission:v2").Err(), "only synthetic quota keys on owned fixtures should be initialized")
			t.Cleanup(func() {
				require.NoError(t, raw.Del(context.Background(), key, familiesKey, key+":admission:v2").Err(), "only synthetic owned-fixture quota keys should be cleaned")
			})
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			for j, observation := range []cache.GitHubQuotaObservation{
				{Now: now, ResetAt: now.Add(tt.firstReset), Limit: 100, Remaining: tt.firstRemaining},
				{Now: now, ResetAt: now.Add(tt.secondReset), Limit: 100, Remaining: tt.secondRemaining},
			} {
				client := a
				if j == 1 {
					client = b
				}
				applied, err := client.ObserveGitHubQuota(ctx, key, observation)
				require.NoError(t, err, "all three keys must execute atomically on actual standalone or Cluster Redis")
				require.True(t, applied, "a distinct live family must survive either arrival order")
			}
			first, err := a.AdmitGitHubBackground(ctx, key, "first", now, 20, time.Minute)
			require.NoError(t, err, "first periodic slot should be available on actual Redis")
			require.Equal(t, cache.GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(time.Minute)}, first, "all reset families should share one distributed admission lane")
			second, err := b.AdmitGitHubBackground(ctx, key, "second", now, 20, time.Minute)
			require.NoError(t, err, "another process should honor the distributed slot")
			require.Equal(t, cache.GitHubBackgroundAdmission{Known: true, Low: true, RetryAt: now.Add(time.Minute)}, second, "a low family hidden from the legacy scalar must still restrict admission")
			fields, err := raw.HGetAll(ctx, familiesKey).Result()
			require.NoError(t, err, "both live families should be stored on actual Redis")
			require.Equal(t, map[string]string{
				fmt.Sprintf("reset:%d", now.Add(tt.firstReset).UnixMilli()):  strconv.FormatInt(tt.firstRemaining, 10),
				fmt.Sprintf("reset:%d", now.Add(tt.secondReset).UnixMilli()): strconv.FormatInt(tt.secondRemaining, 10),
			}, fields, "family state should preserve the exact independent reset minima")
			ttl, err := raw.PTTL(ctx, familiesKey).Result()
			require.NoError(t, err, "family TTL should be readable on actual Redis")
			require.InDelta(t, (2 * time.Minute).Milliseconds(), ttl.Milliseconds(), 5000, "family storage must expire at its latest bounded reset")
			later, err := a.AdmitGitHubBackground(ctx, key, "second", now.Add(time.Minute), 20, time.Minute)
			require.NoError(t, err, "logical reset expiry should execute independently of Redis wall time")
			require.Equal(t, tt.laterLow, later.Low, "only the later live family may continue reserve enforcement")
			require.True(t, later.Known, "the later family must stay known after the earlier reset")
		})
	}
}

func runRedisQuotaAdmissionLaneContract(t *testing.T, newClient func() *cache.Client, raw redis.UniversalClient, appID int64) {
	t.Helper()
	requireOwnedQuotaRedisFixture(t, raw)
	tests := []struct {
		name string
		kind string
	}{
		{name: "legacy expiry retains slot and waiter", kind: "legacy_expiry"},
		{name: "healthy admission clears served waiter", kind: "healthy"},
		{name: "legacy positions migrate once", kind: "migration"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, b := newClient(), newClient()
			require.NotNil(t, a, "first isolated quota client should initialize")
			require.NotNil(t, b, "second isolated quota client should initialize")
			t.Cleanup(func() {
				require.NoError(t, a.Close(), "first isolated lane client should close")
				require.NoError(t, b.Close(), "second isolated lane client should close")
			})
			ctx := context.Background()
			key := fmt.Sprintf("{ghrl:v1:lane-integration:app:%d:inst:42}:quota:core", appID+int64(i))
			laneKey := key + ":admission:v2"
			require.NoError(t, raw.Del(ctx, key, key+":families:v2", laneKey).Err(), "initialize only synthetic keys on the verified owned Redis fixture")
			t.Cleanup(func() {
				require.NoError(t, raw.Del(context.Background(), key, key+":families:v2", laneKey).Err(), "clean only synthetic owned-fixture lane keys")
			})
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			if tt.kind == "migration" {
				deadline := now.Add(30 * time.Minute).UnixMilli()
				require.NoError(t, raw.HSet(ctx, key,
					"reset", now.Add(time.Hour).UnixMilli(), "limit", 100, "remaining", 10,
					"candidate:older", fmt.Sprintf("%d:5", deadline), "sequence", 5, "next_admission", now.Add(time.Minute).UnixMilli()).Err(), "seed the previous release's scalar and FIFO format")
				require.NoError(t, raw.PExpire(ctx, key, time.Hour).Err(), "bound the synthetic legacy fixture lifetime")
				newcomer, err := a.AdmitGitHubBackground(ctx, key, "newcomer", now, 20, time.Minute)
				require.NoError(t, err, "migration must access all three same-slot keys atomically")
				require.Equal(t, cache.GitHubBackgroundAdmission{Known: true, Low: true, RetryAt: now.Add(time.Minute)}, newcomer, "migration must preserve the consumed slot and older position")
				require.NoError(t, raw.HSet(ctx, key, "candidate:later-old-client", fmt.Sprintf("%d:1", deadline), "next_admission", now.Add(15*time.Minute).UnixMilli()).Err(), "later old-client shadow writes should remain isolated")
				older, err := b.AdmitGitHubBackground(ctx, key, "older", now.Add(time.Minute), 20, time.Minute)
				require.NoError(t, err, "another client should honor the migrated order")
				require.Equal(t, cache.GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(2 * time.Minute)}, older, "later legacy writes must not be reimported into the initialized lane")
				return
			}
			firstReset, secondReset := time.Minute, 2*time.Minute
			firstRemaining, secondRemaining := int64(10), int64(90)
			interval, elapsed := time.Minute, time.Duration(0)
			if tt.kind == "legacy_expiry" {
				firstReset, secondReset = 100*time.Millisecond, 2*time.Second
				firstRemaining, secondRemaining = 90, 10
				interval, elapsed = time.Second, 50*time.Millisecond
			}
			for _, observation := range []cache.GitHubQuotaObservation{
				{Now: now, ResetAt: now.Add(firstReset), Limit: 100, Remaining: firstRemaining},
				{Now: now, ResetAt: now.Add(secondReset), Limit: 100, Remaining: secondRemaining},
			} {
				_, err := a.ObserveGitHubQuota(ctx, key, observation)
				require.NoError(t, err, "each reset family should be stored on the actual Redis fixture")
			}
			first, err := a.AdmitGitHubBackground(ctx, key, "first", now.Add(elapsed), 20, interval)
			require.NoError(t, err, "first work should consume the shared slot")
			require.True(t, first.Admitted, "first work should be admitted")
			waiting, err := b.AdmitGitHubBackground(ctx, key, "waiting", now.Add(elapsed), 20, interval)
			require.NoError(t, err, "second client should queue its older work")
			require.False(t, waiting.Admitted, "older waiting work must honor the consumed slot")
			if tt.kind == "legacy_expiry" {
				// Exercise real Redis key expiry without waiting a production-sized
				// minute; the family and admission lane must survive independently.
				require.NoError(t, raw.PExpire(ctx, key, time.Millisecond).Err(), "expire only the synthetic legacy scalar")
				require.Eventually(t, func() bool {
					count, err := raw.Exists(ctx, key).Result()
					return err == nil && count == 0
				}, time.Second, time.Millisecond, "the actual legacy key must expire before checking the independent lane")
				newcomer, err := a.AdmitGitHubBackground(ctx, key, "newcomer", now.Add(firstReset), 20, interval)
				require.NoError(t, err, "later low-family control should survive actual legacy expiry")
				require.Equal(t, cache.GitHubBackgroundAdmission{Known: true, Low: true, RetryAt: now.Add(elapsed + interval)}, newcomer, "legacy expiry must not create an early slot or let new work overtake the waiter")
				older, err := b.AdmitGitHubBackground(ctx, key, "waiting", now.Add(elapsed+interval), 20, interval)
				require.NoError(t, err, "the original waiter should receive its preserved turn")
				require.True(t, older.Admitted, "new work must not displace the original waiter after legacy expiry")
				return
			}
			recovered, err := b.AdmitGitHubBackground(ctx, key, "waiting", now.Add(61*time.Second), 20, interval)
			require.NoError(t, err, "healthy remaining quota should admit waiting work")
			require.Equal(t, cache.GitHubBackgroundAdmission{Known: true, Admitted: true}, recovered, "healthy admission must consume obsolete queue presence")
			_, err = a.ObserveGitHubQuota(ctx, key, cache.GitHubQuotaObservation{Now: now.Add(62 * time.Second), ResetAt: now.Add(3 * time.Minute), Limit: 100, Remaining: 10})
			require.NoError(t, err, "another low family should establish the next limited period")
			newcomer, err := a.AdmitGitHubBackground(ctx, key, "newcomer", now.Add(62*time.Second), 20, interval)
			require.NoError(t, err, "new work should not be blocked by already-served healthy-period work")
			require.Equal(t, cache.GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(122 * time.Second)}, newcomer, "the next low period must start without a stale FIFO head")
		})
	}
}
