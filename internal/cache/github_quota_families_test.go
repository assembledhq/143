package cache

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGitHubQuotaLiveFamilies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                            string
		firstReset, secondReset         time.Duration
		firstRemaining, secondRemaining int64
		afterFirstExpiryLow             bool
	}{
		{name: "earlier healthy then later low", firstReset: time.Minute, secondReset: 2 * time.Minute, firstRemaining: 90, secondRemaining: 10, afterFirstExpiryLow: true},
		{name: "later low then earlier healthy", firstReset: 2 * time.Minute, secondReset: time.Minute, firstRemaining: 10, secondRemaining: 90, afterFirstExpiryLow: true},
		{name: "earlier low then later healthy", firstReset: time.Minute, secondReset: 2 * time.Minute, firstRemaining: 10, secondRemaining: 90},
		{name: "later healthy then earlier low", firstReset: 2 * time.Minute, secondReset: time.Minute, firstRemaining: 90, secondRemaining: 10},
		{name: "both low earlier first", firstReset: time.Minute, secondReset: 2 * time.Minute, firstRemaining: 10, secondRemaining: 5, afterFirstExpiryLow: true},
		{name: "both low later first", firstReset: 2 * time.Minute, secondReset: time.Minute, firstRemaining: 5, secondRemaining: 10, afterFirstExpiryLow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, server := testRedisClient(t)
			ctx, key := context.Background(), "{github-quota:families}:core"
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			for _, observation := range []GitHubQuotaObservation{
				{Now: now, ResetAt: now.Add(tt.firstReset), Limit: 100, Remaining: tt.firstRemaining},
				{Now: now, ResetAt: now.Add(tt.secondReset), Limit: 100, Remaining: tt.secondRemaining},
			} {
				applied, err := client.ObserveGitHubQuota(ctx, key, observation)
				require.NoError(t, err, "each live reset family should be recorded")
				require.True(t, applied, "a different live reset must not be rejected based on arrival order")
			}
			require.Equal(t, 2*time.Minute, server.TTL(githubQuotaFamiliesKey(key)), "family storage must remain until the latest live reset")
			require.Equal(t, tt.firstReset, server.TTL(key), "the legacy scalar must retain its original single-family lifetime")
			first, err := client.AdmitGitHubBackground(ctx, key, "first", now, 20, time.Minute)
			require.NoError(t, err, "the first candidate should use shared family evidence")
			require.Equal(t, 2*time.Minute, server.TTL(githubQuotaAdmissionKey(key)), "the isolated admission lane must survive the earlier family reset")
			require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(time.Minute)}, first, "any low family should start a single shared periodic lane")
			second, err := client.AdmitGitHubBackground(ctx, key, "second", now, 20, time.Minute)
			require.NoError(t, err, "the next candidate should use the same lane")
			require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, RetryAt: now.Add(time.Minute)}, second, "different reset families must not create additional admission lanes")
			afterExpiry, err := client.AdmitGitHubBackground(ctx, key, "second", now.Add(time.Minute), 20, time.Minute)
			require.NoError(t, err, "one family's expiry should leave the other family authoritative")
			require.Equal(t, tt.afterFirstExpiryLow, afterExpiry.Low, "only live families should continue reserve enforcement")
			require.True(t, afterExpiry.Known, "the later family must survive the earlier family's expiry")
			fields, err := client.rdb.HGetAll(ctx, githubQuotaFamiliesKey(key)).Result()
			require.NoError(t, err, "family expiry should be inspectable")
			laterRemaining := tt.secondRemaining
			if tt.firstReset > tt.secondReset {
				laterRemaining = tt.firstRemaining
			}
			require.Equal(t, map[string]string{fmt.Sprintf("reset:%d", now.Add(2*time.Minute).UnixMilli()): strconv.FormatInt(laterRemaining, 10)}, fields, "expired exact families should be removed independently")
		})
	}
}

func TestGitHubQuotaFamiliesMonotonicAndStaleEvidence(t *testing.T) {
	t.Parallel()
	client, _ := testRedisClient(t)
	ctx, key := context.Background(), "{github-quota:monotonic}:core"
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		elapsed, reset time.Duration
		remaining      int64
		applied        bool
	}{
		{name: "earlier healthy", reset: time.Minute, remaining: 90, applied: true},
		{name: "later healthy", reset: 2 * time.Minute, remaining: 80, applied: true},
		{name: "later becomes low", elapsed: time.Second, reset: 2 * time.Minute, remaining: 10, applied: true},
		{name: "earlier becomes low", elapsed: time.Second, reset: time.Minute, remaining: 5, applied: true},
		{name: "late higher later family", elapsed: 2 * time.Second, reset: 2 * time.Minute, remaining: 99},
		{name: "late higher earlier family", elapsed: 2 * time.Second, reset: time.Minute, remaining: 99},
		{name: "expired family cannot return", elapsed: time.Minute, reset: time.Minute, remaining: 0},
		{name: "unbounded reset rejected", reset: 2*time.Hour + time.Millisecond, remaining: 0},
	}
	for _, tt := range tests {
		applied, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now.Add(tt.elapsed), ResetAt: now.Add(tt.reset), Limit: 100, Remaining: tt.remaining})
		require.NoError(t, err, "%s should complete without a coordination failure", tt.name)
		require.Equal(t, tt.applied, applied, "%s should preserve per-family monotonic evidence", tt.name)
	}
	admission, err := client.AdmitGitHubBackground(ctx, key, "candidate", now.Add(time.Minute), 20, time.Minute)
	require.NoError(t, err, "the later low family should survive stale observations")
	require.True(t, admission.Low, "higher or stale responses must not hide another family's low quota")
	fields, err := client.rdb.HGetAll(ctx, githubQuotaFamiliesKey(key)).Result()
	require.NoError(t, err, "remaining family evidence should be readable")
	require.Equal(t, map[string]string{fmt.Sprintf("reset:%d", now.Add(2*time.Minute).UnixMilli()): "10"}, fields, "the later family's minimum should remain after earlier expiry")
}

func TestGitHubQuotaFamilyOverflowIsBoundedAndConservative(t *testing.T) {
	t.Parallel()
	client, server := testRedisClient(t)
	ctx, key := context.Background(), "{github-quota:family-cap}:core"
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= githubQuotaMaxFamilies+100; i++ {
		remaining := int64(90)
		if i == githubQuotaMaxFamilies+1 {
			remaining = 5
		}
		applied, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Duration(i) * time.Minute), Limit: 100, Remaining: remaining})
		require.NoError(t, err, "bounded overflow family %d should remain observable", i)
		require.True(t, applied, "bounded overflow must conservatively retain valid evidence %d", i)
	}
	fields, err := client.rdb.HGetAll(ctx, githubQuotaFamiliesKey(key)).Result()
	require.NoError(t, err, "bounded family summary should be readable")
	expectedFields := map[string]string{
		"overflow_remaining": "5",
		"overflow_reset":     strconv.FormatInt(now.Add(116*time.Minute).UnixMilli(), 10),
	}
	for i := 1; i <= githubQuotaMaxFamilies; i++ {
		expectedFields[fmt.Sprintf("reset:%d", now.Add(time.Duration(i)*time.Minute).UnixMilli())] = "90"
	}
	require.Equal(t, expectedFields, fields, "bounded storage must retain every exact family and the conservative overflow minimum through its latest reset")
	require.Equal(t, 116*time.Minute, server.TTL(githubQuotaFamiliesKey(key)), "storage retention must be bounded by observed reset deadlines")
	afterExactExpiry, err := client.AdmitGitHubBackground(ctx, key, "candidate", now.Add(18*time.Minute), 20, time.Minute)
	require.NoError(t, err, "overflow should remain usable after all exact families expire")
	require.True(t, afterExactExpiry.Low, "bounded overflow may conservatively retain low evidence until its latest reset")
	allExpired, err := client.AdmitGitHubBackground(ctx, key, "candidate", now.Add(116*time.Minute), 20, time.Minute)
	require.NoError(t, err, "all family deadlines should release admission")
	require.Equal(t, GitHubBackgroundAdmission{Admitted: true}, allExpired, "no overflow evidence may survive its latest reset")
	require.False(t, server.Exists(githubQuotaFamiliesKey(key)), "expired family storage should be deleted even before Redis's wall clock TTL")
}

// Frozen previous-release script: rolling writers must retain their existing
// scalar format without erasing new family evidence or sharing new Lua logic.
var previousGitHubQuotaObserveScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local reset = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local remaining = tonumber(ARGV[4])
local prior_reset = tonumber(redis.call('HGET', KEYS[1], 'reset') or '0')
local prior_remaining = tonumber(redis.call('HGET', KEYS[1], 'remaining') or '0')
if reset <= now or reset < prior_reset or (reset > prior_reset and prior_reset > now) or (reset == prior_reset and remaining > prior_remaining) then return 0 end
redis.call('HSET', KEYS[1], 'reset', reset, 'limit', limit, 'remaining', remaining)
redis.call('PEXPIRE', KEYS[1], reset - now)
return 1
`)

func TestGitHubQuotaFamiliesSurviveLegacyWritersAndRollback(t *testing.T) {
	t.Parallel()
	client, server := testRedisClient(t)
	ctx, key := context.Background(), "{github-quota:legacy}:core"
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	_, err := previousGitHubQuotaObserveScript.Run(ctx, client.rdb, []string{key}, now.UnixMilli(), now.Add(time.Minute).UnixMilli(), 100, 90).Int()
	require.NoError(t, err, "a previous-release writer should establish legacy scalar evidence")
	_, err = client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(2 * time.Minute), Limit: 100, Remaining: 10})
	require.NoError(t, err, "the new writer should retain another live family")
	before, err := client.rdb.HGetAll(ctx, githubQuotaFamiliesKey(key)).Result()
	require.NoError(t, err, "new family snapshot should be readable before rollback writes")
	_, err = previousGitHubQuotaObserveScript.Run(ctx, client.rdb, []string{key}, now.UnixMilli(), now.Add(time.Minute).UnixMilli(), 100, 80).Int()
	require.NoError(t, err, "a rolled-back writer should keep its original scalar contract")
	after, err := client.rdb.HGetAll(ctx, githubQuotaFamiliesKey(key)).Result()
	require.NoError(t, err, "new family snapshot should survive an old writer")
	require.Equal(t, before, after, "old writes must not corrupt or replace the family snapshot")
	legacy, err := client.rdb.HGetAll(ctx, key).Result()
	require.NoError(t, err, "legacy snapshot should remain readable by a rolled-back release")
	require.Equal(t, map[string]string{"reset": strconv.FormatInt(now.Add(time.Minute).UnixMilli(), 10), "limit": "100", "remaining": "80"}, legacy, "old and new writers must preserve the previous scalar storage contract")
	require.Equal(t, time.Minute, server.TTL(key), "the old script should exercise its original shorter TTL")
	server.FastForward(time.Minute)
	admission, err := client.AdmitGitHubBackground(ctx, key, "first", now.Add(time.Minute), 20, time.Minute)
	require.NoError(t, err, "new admission should recover after old scalar TTL deletion")
	require.True(t, admission.Known && admission.Low, "the later low family must survive an older writer's earlier key expiry")
	require.Equal(t, time.Minute, server.TTL(githubQuotaAdmissionKey(key)), "isolated candidate storage must receive a bounded family lifetime")
	rejected, err := client.AdmitGitHubBackground(ctx, key, "second", now.Add(time.Minute), 20, time.Minute)
	require.NoError(t, err, "the recovered lane should preserve shared interval control")
	require.False(t, rejected.Admitted, "recreating the scalar key must not make every background candidate bypass admission")
}
