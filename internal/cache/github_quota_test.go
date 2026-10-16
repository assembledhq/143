package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestGitHubQuotaObservationOrdering(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		observation GitHubQuotaObservation
		applied     bool
		low         bool
	}{
		{name: "lower same-window quota", observation: GitHubQuotaObservation{Now: now.Add(time.Second), ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 10}, applied: true, low: true},
		{name: "out of order higher quota", observation: GitHubQuotaObservation{Now: now.Add(time.Second), ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 50}, low: true},
		{name: "earlier live family", applied: true, observation: GitHubQuotaObservation{Now: now.Add(time.Second), ResetAt: now.Add(30 * time.Minute), Limit: 100, Remaining: 90}, low: true},
		{name: "later live family", applied: true, observation: GitHubQuotaObservation{Now: now.Add(time.Second), ResetAt: now.Add(2 * time.Hour), Limit: 100, Remaining: 90}, low: true},
		{name: "legitimate new window", observation: GitHubQuotaObservation{Now: now.Add(time.Hour), ResetAt: now.Add(2 * time.Hour), Limit: 100, Remaining: 90}, applied: true},
		{name: "expired reset", observation: GitHubQuotaObservation{Now: now, ResetAt: now, Limit: 100, Remaining: 90}, low: true},
		{name: "invalid remaining", observation: GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 100, Remaining: -1}, low: true},
		{name: "invalid limit", observation: GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 0}, low: true},
		{name: "remaining above limit", observation: GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 101}, low: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _ := testRedisClient(t)
			ctx, key := context.Background(), "{github-quota:test}:core"
			applied, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 20})
			require.NoError(t, err, "initial quota observation should persist")
			require.True(t, applied, "initial window should become authoritative")
			applied, err = client.ObserveGitHubQuota(ctx, key, tt.observation)
			require.NoError(t, err, "ordered quota observation should complete")
			require.Equal(t, tt.applied, applied, "only valid newer or lower quota evidence should apply")
			admissionAt := now.Add(2 * time.Second)
			if tt.observation.Now.After(admissionAt) {
				admissionAt = tt.observation.Now
			}
			admission, err := client.AdmitGitHubBackground(ctx, key, "candidate", admissionAt, 20, time.Minute)
			require.NoError(t, err, "shared quota observation should inform admission")
			require.Equal(t, tt.low, admission.Low, "budget must use the strongest current-window quota evidence")
		})
	}
}

func TestGitHubQuotaDistributedFairAdmission(t *testing.T) {
	t.Parallel()
	clientA, server := testRedisClient(t)
	clientB := New(Config{Topology: "standalone", URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
	t.Cleanup(func() { require.NoError(t, clientB.Close(), "second shared-state client should close") })
	ctx := context.Background()
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	key := "{github-quota:fair}:core"
	_, err := clientA.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 10})
	require.NoError(t, err, "low shared quota should persist")
	tests := []struct {
		client    *Client
		candidate string
		elapsed   time.Duration
		admitted  bool
	}{
		{client: clientA, candidate: "org-a:oldest", admitted: true},
		{client: clientB, candidate: "org-b:oldest"},
		{client: clientA, candidate: "org-a:next"},
		{client: clientA, candidate: "org-a:oldest", elapsed: time.Minute},
		{client: clientA, candidate: "org-a:next", elapsed: time.Minute},
		{client: clientB, candidate: "org-b:oldest", elapsed: time.Minute, admitted: true},
		{client: clientB, candidate: "org-b:oldest", elapsed: time.Minute},
		{client: clientA, candidate: "org-a:next", elapsed: 2 * time.Minute, admitted: true},
		{client: clientA, candidate: "org-a:oldest", elapsed: 3 * time.Minute, admitted: true},
	}
	for i, tt := range tests {
		admission, err := tt.client.AdmitGitHubBackground(ctx, key, tt.candidate, now.Add(tt.elapsed), 20, time.Minute)
		require.NoError(t, err, "admission %d should complete across independent clients", i)
		require.Equal(t, tt.admitted, admission.Admitted, "admission %d should preserve interval and candidate order", i)
	}
}

func TestGitHubQuotaClosedCandidatesExpire(t *testing.T) {
	t.Parallel()
	client, _ := testRedisClient(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	ctx, key := context.Background(), "{github-quota:expiry}:core"
	_, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 10})
	require.NoError(t, err, "quota window should initialize")
	first, err := client.AdmitGitHubBackground(ctx, key, "first", now, 20, time.Minute)
	require.NoError(t, err, "first background work should obtain a slot")
	require.True(t, first.Admitted, "periodic progress should begin immediately")
	_, err = client.AdmitGitHubBackground(ctx, key, "closed", now, 20, time.Minute)
	require.NoError(t, err, "later closing candidate should enter the queue")
	_, err = client.AdmitGitHubBackground(ctx, key, "live", now, 20, time.Minute)
	require.NoError(t, err, "live candidate should enter the queue")
	waiting, err := client.AdmitGitHubBackground(ctx, key, "live", now.Add(10*time.Minute), 20, time.Minute)
	require.NoError(t, err, "live candidate should renew its queue presence")
	require.False(t, waiting.Admitted, "oldest queued candidate should retain its turn while present")
	ready, err := client.AdmitGitHubBackground(ctx, key, "live", now.Add(30*time.Minute), 20, time.Minute)
	require.NoError(t, err, "absent candidate should expire without database cleanup")
	require.True(t, ready.Admitted, "live candidate should progress after the closed candidate expires")
}

func TestGitHubQuotaFairnessSurvivesReconciliationCadence(t *testing.T) {
	t.Parallel()
	client, _ := testRedisClient(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	ctx, key := context.Background(), "{github-quota:cadence}:core"
	_, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 10})
	require.NoError(t, err, "known low quota should initialize")
	tests := []struct {
		candidate string
		elapsed   time.Duration
		admitted  bool
	}{
		{candidate: "tenant-a:oldest", admitted: true},
		{candidate: "tenant-b:oldest"},
		{candidate: "tenant-a:oldest", elapsed: 10 * time.Minute},
		{candidate: "tenant-b:oldest", elapsed: 10 * time.Minute, admitted: true},
		{candidate: "tenant-b:oldest", elapsed: 10 * time.Minute},
		{candidate: "tenant-a:oldest", elapsed: 20 * time.Minute, admitted: true},
		{candidate: "tenant-b:oldest", elapsed: 20 * time.Minute},
		{candidate: "tenant-b:oldest", elapsed: 30 * time.Minute, admitted: true},
	}
	for i, tt := range tests {
		admission, err := client.AdmitGitHubBackground(ctx, key, tt.candidate, now.Add(tt.elapsed), 20, time.Minute)
		require.NoError(t, err, "cadence-spaced admission %d should complete", i)
		require.Equal(t, tt.admitted, admission.Admitted, "cadence-spaced admission %d should preserve both tenants' turns", i)
	}
}

func TestGitHubQuotaAdmissionBypassesUnknownExpiredAndDisabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reserve int64
		expired bool
		known   bool
	}{
		{name: "unknown quota", reserve: 20},
		{name: "disabled budget", known: true},
		{name: "expired window", reserve: 20, known: true, expired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _ := testRedisClient(t)
			now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
			ctx, key := context.Background(), "{github-quota:bypass}:core"
			if tt.known {
				_, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Minute), Limit: 100, Remaining: 10})
				require.NoError(t, err, "known quota should initialize")
			}
			if tt.expired {
				now = now.Add(time.Minute)
			}
			admission, err := client.AdmitGitHubBackground(ctx, key, "candidate", now, tt.reserve, time.Minute)
			require.NoError(t, err, "unknown or disabled budget should not block")
			require.Equal(t, GitHubBackgroundAdmission{Admitted: true}, admission, "expired, unknown, or disabled quotas should admit ordinary work")
		})
	}
}

func TestGitHubQuotaAdmissionOverflowAndUnavailable(t *testing.T) {
	t.Parallel()
	client, _ := testRedisClient(t)
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	ctx, key := context.Background(), "{github-quota:capacity}:core"
	_, err := client.ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{Now: now, ResetAt: now.Add(time.Hour), Limit: 100, Remaining: 10})
	require.NoError(t, err, "bounded queue quota should initialize")
	entries := map[string]any{}
	for i := 0; i < 1024; i++ {
		entries[fmt.Sprintf("candidate:%d", i)] = fmt.Sprintf("%d:%d", now.Add(time.Minute).UnixMilli(), i+1)
	}
	require.NoError(t, client.rdb.HSet(ctx, key, entries).Err(), "bounded queue should fill using synthetic candidates")
	admission, err := client.AdmitGitHubBackground(ctx, key, "extra", now, 20, time.Minute)
	require.NoError(t, err, "queue overflow should report an optimization bypass")
	require.Equal(t, GitHubBackgroundAdmission{Known: true, Low: true, Admitted: true, Overflow: true}, admission, "a full candidate queue should fail open without unbounded growth")
	_, err = (*Client)(nil).AdmitGitHubBackground(ctx, key, "candidate", now, 20, time.Minute)
	require.Error(t, err, "missing Redis should be visible to the caller")
	_, err = (*Client)(nil).ObserveGitHubQuota(ctx, key, GitHubQuotaObservation{})
	require.Error(t, err, "missing observation storage should be visible to the caller")
}
