package cache

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

type TimeoutConfig struct {
	Dial  time.Duration
	Read  time.Duration
	Write time.Duration
}

type Config struct {
	Topology   string
	URL        string
	Addrs      []string
	MasterName string
	Password   string
	PoolSize   int
	Timeouts   TimeoutConfig
}

type Client struct {
	rdb     redis.UniversalClient
	breaker *CircuitBreaker
	logger  zerolog.Logger
	metrics *Metrics
}

func New(cfg Config, logger zerolog.Logger, metrics *Metrics) *Client {
	cfg.applyDefaults()
	if !cfg.valid() {
		return nil
	}

	var rdb redis.UniversalClient
	switch cfg.Topology {
	case "", "standalone":
		opts, err := redis.ParseURL(cfg.URL)
		if err != nil {
			logger.Error().Err(err).Str("url", cfg.URL).Msg("invalid Redis URL, running without Redis")
			return nil
		}
		if cfg.Password != "" {
			opts.Password = cfg.Password
		}
		opts.DialTimeout = cfg.Timeouts.Dial
		opts.ReadTimeout = cfg.Timeouts.Read
		opts.WriteTimeout = cfg.Timeouts.Write
		if cfg.PoolSize > 0 {
			opts.PoolSize = cfg.PoolSize
		}
		rdb = redis.NewClient(opts)
	case "sentinel":
		if cfg.MasterName == "" || len(cfg.Addrs) == 0 {
			logger.Warn().Msg("Redis sentinel config incomplete, running without Redis")
			return nil
		}
		rdb = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    cfg.MasterName,
			SentinelAddrs: cfg.Addrs,
			Password:      cfg.Password,
			PoolSize:      cfg.PoolSize,
			DialTimeout:   cfg.Timeouts.Dial,
			ReadTimeout:   cfg.Timeouts.Read,
			WriteTimeout:  cfg.Timeouts.Write,
		})
	case "cluster":
		if len(cfg.Addrs) == 0 {
			logger.Warn().Msg("Redis cluster config incomplete, running without Redis")
			return nil
		}
		rdb = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        cfg.Addrs,
			Password:     cfg.Password,
			PoolSize:     cfg.PoolSize,
			DialTimeout:  cfg.Timeouts.Dial,
			ReadTimeout:  cfg.Timeouts.Read,
			WriteTimeout: cfg.Timeouts.Write,
		})
	default:
		logger.Error().Str("topology", cfg.Topology).Msg("invalid Redis topology, running without Redis")
		return nil
	}

	client := &Client{
		rdb:     rdb,
		breaker: NewCircuitBreaker(logger),
		logger:  logger,
		metrics: metrics,
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		logger.Warn().Err(err).Msg("Redis ping failed on startup, will retry via circuit breaker")
		client.breaker.ForceOpen()
	} else {
		logger.Info().Msg("Redis connected")
	}
	return client
}

func (c *Client) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

func (c *Client) Available() bool {
	if c == nil || c.rdb == nil {
		return false
	}
	if c.breaker.Ready() {
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if !c.breaker.Allow() {
		return false
	}
	start := time.Now()
	err := c.rdb.Ping(ctx).Err()
	c.metrics.RecordCommand(ctx, "ping", time.Since(start).Seconds())
	if err != nil {
		c.breaker.RecordFailure()
		c.logger.Warn().Err(err).Msg("redis availability probe failed")
		return false
	}
	c.breaker.RecordSuccess()
	return true
}

func (c *Client) Healthy(ctx context.Context) bool {
	if c == nil || c.rdb == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return c.rdb.Ping(ctx).Err() == nil
}

func (c *Client) doCommand(ctx context.Context, command string, fn func() error) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis unavailable")
	}
	if !c.breaker.Allow() {
		return errors.New("redis breaker open")
	}

	start := time.Now()
	err := fn()
	c.metrics.RecordCommand(ctx, command, time.Since(start).Seconds())
	if err != nil {
		c.breaker.RecordFailure()
		return err
	}
	c.breaker.RecordSuccess()
	return nil
}

func (c *Client) raw() redis.UniversalClient {
	if c == nil {
		return nil
	}
	return c.rdb
}

func (c *Client) GetBytes(ctx context.Context, key string) ([]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis unavailable")
	}
	if !c.breaker.Allow() {
		return nil, errors.New("redis breaker open")
	}

	var value []byte
	start := time.Now()
	result, err := c.rdb.Get(ctx, key).Bytes()
	c.metrics.RecordCommand(ctx, "get", time.Since(start).Seconds())
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, err
		}
		c.breaker.RecordFailure()
		return nil, err
	}
	c.breaker.RecordSuccess()
	value = result
	return value, nil
}

func (c *Client) SetBytes(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis unavailable")
	}

	return c.doCommand(ctx, "set", func() error {
		return c.rdb.Set(ctx, key, value, ttl).Err()
	})
}

func ParseAddrs(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	addrs := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addrs = append(addrs, part)
	}
	return addrs
}

func (c Config) valid() bool {
	switch c.Topology {
	case "", "standalone":
		return c.URL != ""
	case "sentinel":
		return len(c.Addrs) > 0 && c.MasterName != ""
	case "cluster":
		return len(c.Addrs) > 0
	default:
		return false
	}
}

func (c *Config) applyDefaults() {
	if c.Topology == "" {
		c.Topology = "standalone"
	}
	if c.Timeouts.Dial == 0 {
		c.Timeouts.Dial = 5 * time.Second
	}
	if c.Timeouts.Read == 0 {
		c.Timeouts.Read = 3 * time.Second
	}
	if c.Timeouts.Write == 0 {
		c.Timeouts.Write = 3 * time.Second
	}
}
