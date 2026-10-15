package cache

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitHubQuotaLegacyExpiryPreservesLiveAdmissionLane(t *testing.T) {
	t.Parallel()
	client, server := testRedisClient(t)
	ctx, key := context.Background(), "{github-quota:legacy-fifo-expiry}:core"
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	for _, observation := range []GitHubQuotaObservation{
		{Now: now, ResetAt: now.Add(time.Minute), Limit: 100, Remaining: 90},
		{Now: now, ResetAt: now.Add(2 * time.Minute), Limit: 100, Remaining: 10},
	} {
		_, err := client.ObserveGitHubQuota(ctx, key, observation)
		require.NoError(t, err, "both live families must be retained")
	}
	server.FastForward(50 * time.Second)
	first, err := client.AdmitGitHubBackground(ctx, key, "first", now.Add(50*time.Second), 20, time.Minute)
	require.NoError(t, err, "first work should receive the shared slot")
	require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(110 * time.Second)}, first, "the periodic slot must run until fifty seconds after the earlier reset")
	waiting, err := client.AdmitGitHubBackground(ctx, key, "waiting", now.Add(50*time.Second), 20, time.Minute)
	require.NoError(t, err, "an older waiter should enter the isolated lane")
	require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, RetryAt: now.Add(110 * time.Second)}, waiting, "the older waiter must retain its place until the consumed slot ends")
	before, err := client.rdb.HGetAll(ctx, githubQuotaAdmissionKey(key)).Result()
	require.NoError(t, err, "lane state should be readable before an old writer runs")
	server.FastForward(time.Second)
	_, err = previousGitHubQuotaObserveScript.Run(ctx, client.rdb, []string{key}, now.Add(51*time.Second).UnixMilli(), now.Add(time.Minute).UnixMilli(), 100, 80).Int()
	require.NoError(t, err, "an old observer should shorten only its legacy scalar TTL")
	after, err := client.rdb.HGetAll(ctx, githubQuotaAdmissionKey(key)).Result()
	require.NoError(t, err, "the independent lane must survive the old observation")
	require.Equal(t, before, after, "old scalar observations must never mutate new-client FIFO or slot state")
	server.FastForward(9 * time.Second)
	require.False(t, server.Exists(key), "the earlier legacy scalar should physically expire")
	require.True(t, server.Exists(githubQuotaAdmissionKey(key)), "the later low family must retain the separate lane")
	newcomer, err := client.AdmitGitHubBackground(ctx, key, "newcomer", now.Add(time.Minute), 20, time.Minute)
	require.NoError(t, err, "new work should still see later low-family control")
	require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, RetryAt: now.Add(110 * time.Second)}, newcomer, "legacy expiry must not create an early slot or let new work overtake the older waiter")
	ready, err := client.AdmitGitHubBackground(ctx, key, "waiting", now.Add(110*time.Second), 20, time.Minute)
	require.NoError(t, err, "the original waiter should receive the next real slot")
	require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(170 * time.Second)}, ready, "the original FIFO order must survive legacy key deletion")
	require.Equal(t, 10*time.Second, server.TTL(githubQuotaAdmissionKey(key)), "lane storage must expire with the latest live family even when its periodic slot would extend further")
}

func TestGitHubQuotaHealthyOrUnknownAdmissionClearsServedCandidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		laterHealthy bool
	}{
		{name: "healthy remaining family", laterHealthy: true},
		{name: "all families expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, server := testRedisClient(t)
			ctx, key := context.Background(), "{github-quota:lane-recovery}:core"
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			_, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Minute), Limit: 100, Remaining: 10})
			require.NoError(t, err, "earlier low family should establish a periodic lane")
			if tt.laterHealthy {
				_, err = client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(2 * time.Minute), Limit: 100, Remaining: 90})
				require.NoError(t, err, "a later healthy family should remain independent")
			}
			_, err = client.AdmitGitHubBackground(ctx, key, "first", now, 20, time.Minute)
			require.NoError(t, err, "first work should consume the periodic slot")
			_, err = client.AdmitGitHubBackground(ctx, key, "waiting", now, 20, time.Minute)
			require.NoError(t, err, "waiting work should enter the lane")
			// Preserve a legacy shadow long enough to prove healthy/unknown
			// cleanup prevents it from being imported into a later low period.
			require.NoError(t, client.rdb.HSet(ctx, key, "candidate:waiting", fmt.Sprintf("%d:1", now.Add(30*time.Minute).UnixMilli()), "sequence", 1, "next_admission", now.Add(time.Minute).UnixMilli()).Err(), "synthetic old-client shadow should be present")
			require.NoError(t, client.rdb.PExpire(ctx, key, time.Hour).Err(), "synthetic shadow should survive its logical scalar reset")
			server.FastForward(61 * time.Second)
			recovered, err := client.AdmitGitHubBackground(ctx, key, "waiting", now.Add(61*time.Second), 20, time.Minute)
			require.NoError(t, err, "healthy or unknown quota must allow waiting work to run")
			require.Equal(t, GitHubBackgroundAdmission{Known: tt.laterHealthy, Admitted: true}, recovered, "healthy or unknown admission must consume obsolete queue presence")
			fields, err := client.rdb.HGetAll(ctx, githubQuotaAdmissionKey(key)).Result()
			require.NoError(t, err, "recovered lane state should be inspectable")
			expected := map[string]string{}
			if tt.laterHealthy {
				expected["migrated"] = "1"
			}
			require.Equal(t, expected, fields, "healthy recovery should retain only a bounded migration marker; unknown recovery should remove the lane")
			legacy, err := client.rdb.HGetAll(ctx, key).Result()
			require.NoError(t, err, "legacy scalar should remain readable after shadow cleanup")
			require.Equal(t, map[string]string{"reset": strconv.FormatInt(now.Add(time.Minute).UnixMilli(), 10), "limit": "100", "remaining": "10"}, legacy, "only obsolete legacy queue and slot fields may be cleared")
			_, err = client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now.Add(62 * time.Second), ResetAt: now.Add(3 * time.Minute), Limit: 100, Remaining: 10})
			require.NoError(t, err, "a new low family should initialize its current lane")
			newcomer, err := client.AdmitGitHubBackground(ctx, key, "newcomer", now.Add(62*time.Second), 20, time.Minute)
			require.NoError(t, err, "new low-period work should not wait behind already-served work")
			require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(122 * time.Second)}, newcomer, "a completed healthy-period candidate must not stall the next low period")
		})
	}
}

func TestGitHubQuotaAdmissionLaneMigratesLegacyPositionsOnce(t *testing.T) {
	t.Parallel()
	client, server := testRedisClient(t)
	ctx, key := context.Background(), "{github-quota:lane-migration}:core"
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	_, err := previousGitHubQuotaObserveScript.Run(ctx, client.rdb, []string{key}, now.UnixMilli(), now.Add(time.Hour).UnixMilli(), 100, 10).Int()
	require.NoError(t, err, "a previous release should establish legacy-only low evidence")
	deadline := now.Add(30 * time.Minute).UnixMilli()
	require.NoError(t, client.rdb.HSet(ctx, key, map[string]any{
		"candidate:older": fmt.Sprintf("%d:5", deadline), "candidate:newer": fmt.Sprintf("%d:8", deadline),
		"candidate:expired": fmt.Sprintf("%d:1", now.UnixMilli()), "candidate:unbounded": fmt.Sprintf("%d:2", now.Add(time.Hour).UnixMilli()),
		"candidate:invalid-sequence": fmt.Sprintf("%d:0", deadline), "sequence": 99, "next_admission": now.Add(time.Minute).UnixMilli(),
	}).Err(), "migration should receive valid positions plus malformed synthetic entries")
	newcomer, err := client.AdmitGitHubBackground(ctx, key, "newcomer", now, 20, time.Minute)
	require.NoError(t, err, "the first new-client admission should migrate bounded legacy state")
	require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, RetryAt: now.Add(time.Minute)}, newcomer, "migration must preserve both existing positions and the already-consumed slot")
	fields, err := client.rdb.HGetAll(ctx, githubQuotaAdmissionKey(key)).Result()
	require.NoError(t, err, "migrated state should be inspectable")
	require.Equal(t, map[string]string{
		"migrated": "1", "sequence": "9", "next_admission": strconv.FormatInt(now.Add(time.Minute).UnixMilli(), 10),
		"candidate:older": fmt.Sprintf("%d:5", deadline), "candidate:newer": fmt.Sprintf("%d:8", deadline), "candidate:newcomer": fmt.Sprintf("%d:9", deadline),
	}, fields, "only valid bounded legacy candidates should precede new work")
	require.Equal(t, time.Hour, server.TTL(githubQuotaAdmissionKey(key)), "migrated lane lifetime must be bounded by its live quota family")
	require.NoError(t, client.rdb.HSet(ctx, key, "candidate:later-old-client", fmt.Sprintf("%d:1", deadline), "next_admission", now.Add(15*time.Minute).UnixMilli()).Err(), "a later old client may update only its isolated shadow lane")
	ready, err := client.AdmitGitHubBackground(ctx, key, "older", now.Add(time.Minute), 20, time.Minute)
	require.NoError(t, err, "the original oldest position should receive the next slot")
	require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, RetryAt: now.Add(2 * time.Minute)}, ready, "later legacy writes must not be reimported into an initialized new-client lane")
}
