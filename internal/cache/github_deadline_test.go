package cache

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewEnablesRedisContextDeadlines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "standalone"},
		{name: "sentinel"},
		{name: "cluster"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			redisServer := miniredis.RunT(t)
			client := New(Config{
				Topology:   tt.name,
				URL:        "redis://" + redisServer.Addr(),
				Addrs:      []string{redisServer.Addr()},
				MasterName: "test-primary",
				Timeouts:   TimeoutConfig{Dial: 50 * time.Millisecond, Read: 50 * time.Millisecond, Write: 50 * time.Millisecond},
			}, zerolog.Nop(), nil)
			require.NotNil(t, client, "valid topology should create a client even when its startup probe fails")
			t.Cleanup(func() {
				require.NoError(t, client.Close(), "topology client should close cleanly")
			})

			var respectsDeadlines bool
			switch raw := client.raw().(type) {
			case *redis.Client:
				require.NotEqual(t, "cluster", tt.name, "cluster topology should use a cluster client")
				respectsDeadlines = raw.Options().ContextTimeoutEnabled
			case *redis.ClusterClient:
				require.Equal(t, "cluster", tt.name, "only cluster topology should use a cluster client")
				respectsDeadlines = raw.Options().ContextTimeoutEnabled
			default:
				t.Fatalf("unexpected Redis client type %T", raw)
			}
			require.True(t, respectsDeadlines, "every topology should honor caller context deadlines during network reads and writes")
		})
	}
}

func TestGetGitHubRateLimitStateRespectsDeadlineDuringStalledRead(t *testing.T) {
	t.Parallel()

	redisServer := miniredis.RunT(t)
	client := New(Config{Topology: "standalone", URL: "redis://" + redisServer.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "standalone configuration should create a client")
	t.Cleanup(func() {
		require.NoError(t, client.Close(), "client should close cleanly after a timed-out read")
	})
	requested := make(chan struct{}, 1)
	redisServer.Server().SetPreHook(func(_ *server.Peer, command string, _ ...string) bool {
		if !strings.EqualFold(command, "hgetall") {
			return false
		}
		select {
		case requested <- struct{}{}:
		default:
		}
		// Handle the command without a reply, leaving the real Redis socket read
		// pending until the caller deadline expires.
		return true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	state, err := client.GetGitHubRateLimitState(ctx, "{ghrl:v1:test:app:1:inst:42}:state")
	elapsed := time.Since(started)
	require.Error(t, err, "stalled Redis reads should return an error when the caller deadline expires")
	var timeoutErr net.Error
	require.ErrorAs(t, err, &timeoutErr, "stalled reads should fail with a deadline or network timeout")
	require.True(t, timeoutErr.Timeout(), "Redis should terminate the pending read because its deadline expired")
	require.Equal(t, GitHubRateLimitState{}, state, "timed-out reads should not return fabricated cooldown state")
	require.Less(t, elapsed, time.Second, "caller deadline should bound the read before the default three-second Redis timeout")
	select {
	case <-requested:
	default:
		t.Fatal("GitHub state lookup should reach Redis before exercising the stalled network read")
	}
}
