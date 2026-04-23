package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestConfigApplyDefaultsAndValid(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.applyDefaults()
	require.Equal(t, "standalone", cfg.Topology, "defaults should assume standalone Redis")
	require.Equal(t, 5*time.Second, cfg.Timeouts.Dial, "defaults should set a dial timeout")
	require.Equal(t, 3*time.Second, cfg.Timeouts.Read, "defaults should set a read timeout")
	require.Equal(t, 3*time.Second, cfg.Timeouts.Write, "defaults should set a write timeout")

	require.False(t, cfg.valid(), "standalone config without a URL should be invalid")
	cfg.URL = "redis://localhost:6379/0"
	require.True(t, cfg.valid(), "standalone config with a URL should be valid")

	cfg = Config{Topology: "sentinel"}
	require.False(t, cfg.valid(), "sentinel config should require a master name and addrs")
	cfg.Addrs = []string{"127.0.0.1:26379"}
	cfg.MasterName = "mymaster"
	require.True(t, cfg.valid(), "sentinel config should validate when fully configured")

	cfg = Config{Topology: "cluster"}
	require.False(t, cfg.valid(), "cluster config should require addrs")
	cfg.Addrs = []string{"127.0.0.1:6379"}
	require.True(t, cfg.valid(), "cluster config should validate with addrs")
}

func TestParseAddrs(t *testing.T) {
	t.Parallel()

	require.Nil(t, ParseAddrs(""), "empty input should return nil")
	require.Equal(t, []string{"127.0.0.1:1", "127.0.0.1:2"}, ParseAddrs(" 127.0.0.1:1, ,127.0.0.1:2 "), "parser should trim and drop empty items")
}

func TestNew_InvalidConfigsReturnNil(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "invalid url", cfg: Config{Topology: "standalone", URL: "://bad"}},
		{name: "incomplete sentinel", cfg: Config{Topology: "sentinel"}},
		{name: "incomplete cluster", cfg: Config{Topology: "cluster"}},
		{name: "invalid topology", cfg: Config{Topology: "mystery"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Nil(t, New(tt.cfg, zerolog.Nop(), nil), "invalid config should disable Redis cleanly")
		})
	}
}

func TestNew_SentinelAndClusterCreateClients(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)

	sentinel := New(Config{Topology: "sentinel", MasterName: "mymaster", Addrs: []string{mr.Addr()}, Password: "secret", PoolSize: 4}, zerolog.Nop(), nil)
	require.NotNil(t, sentinel, "complete sentinel configs should build a client even if startup ping fails")

	cluster := New(Config{Topology: "cluster", Addrs: []string{mr.Addr()}, Password: "secret", PoolSize: 4}, zerolog.Nop(), nil)
	require.NotNil(t, cluster, "complete cluster configs should build a client even if startup ping fails")
}

func TestNew_StartupPingFailureOpensBreaker(t *testing.T) {
	t.Parallel()

	client, mr := testRedisClient(t)
	addr := mr.Addr()
	mr.Close()
	_ = client.Close()

	created := New(Config{Topology: "standalone", URL: "redis://" + addr}, zerolog.Nop(), nil)
	require.NotNil(t, created, "constructor should still return a client when startup ping fails")
	require.Equal(t, breakerStateOpen, created.breaker.State(), "startup ping failures should open the breaker")
}

func TestClientNilHelpers(t *testing.T) {
	t.Parallel()

	var client *Client
	require.NoError(t, client.Close(), "nil client close should be a no-op")
	require.False(t, client.Available(), "nil client should be unavailable")
	require.False(t, client.Healthy(context.Background()), "nil client should be unhealthy")
	require.Error(t, client.doCommand(context.Background(), "ping", func() error { return nil }), "nil client commands should fail")
}

func TestClientDoCommandAndAvailabilityFailure(t *testing.T) {
	t.Parallel()

	client, mr := testRedisClient(t)
	require.NoError(t, client.doCommand(context.Background(), "ping", func() error {
		return client.raw().Ping(context.Background()).Err()
	}), "command wrapper should allow healthy Redis operations")

	client.breaker.cooldown = time.Millisecond
	client.breaker.ForceOpen()
	mr.Close()
	time.Sleep(2 * time.Millisecond)

	require.False(t, client.Available(), "availability probe should fail when Redis is down")
	require.Equal(t, breakerStateOpen, client.breaker.State(), "failed recovery probes should leave the breaker open")
}

func TestClientDoCommandBreakerOpenAndRaw(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	client.breaker.ForceOpen()
	require.Error(t, client.doCommand(context.Background(), "ping", func() error { return nil }), "open breaker should reject commands before invoking Redis")
	require.NotNil(t, client.raw(), "raw client accessor should expose the underlying Redis client")
}
