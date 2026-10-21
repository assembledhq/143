package demoseed

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type demoPoolSettings struct {
	host, database, user, password         string
	port                                   uint16
	connectTimeout                         time.Duration
	maxConns, minConns, minIdleConns       int32
	maxLifetime, lifetimeJitter, maxIdle   time.Duration
	healthCheck, pingTimeout               time.Duration
	statementCapacity, descriptionCapacity int
	connectionString                       string
}

func demoPoolSettingsOf(config *pgxpool.Config) demoPoolSettings {
	return demoPoolSettings{
		host: config.ConnConfig.Host, database: config.ConnConfig.Database,
		user: config.ConnConfig.User, password: config.ConnConfig.Password,
		port: config.ConnConfig.Port, connectTimeout: config.ConnConfig.ConnectTimeout,
		maxConns: config.MaxConns, minConns: config.MinConns, minIdleConns: config.MinIdleConns,
		maxLifetime: config.MaxConnLifetime, lifetimeJitter: config.MaxConnLifetimeJitter,
		maxIdle: config.MaxConnIdleTime, healthCheck: config.HealthCheckPeriod,
		pingTimeout: config.PingTimeout, statementCapacity: config.ConnConfig.StatementCacheCapacity,
		descriptionCapacity: config.ConnConfig.DescriptionCacheCapacity,
		connectionString:    config.ConnString(),
	}
}

func TestDemoSeedLifecyclePoolConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, raw, build, expectedName string
	}{
		{
			name: "URI configuration and aliases", build: "0123456789abcdef0123456789abcdef01234567",
			raw:          "postgres://synthetic:fixture@localhost:55432/source?dbname=target&application_name=old&APPLICATION_NAME=upper&Application_Name=mixed&options=-c+application_name%3Doption+-c+idle_in_transaction_session_timeout%3D25000&lock_timeout=3s&statement_timeout=7s&search_path=synthetic&pool_max_conns=9&pool_min_conns=2&pool_min_idle_conns=1&pool_max_conn_lifetime=2h&pool_max_conn_lifetime_jitter=1m&pool_max_conn_idle_time=2m&pool_health_check_period=1m&default_query_exec_mode=cache_statement&statement_cache_capacity=43&description_cache_capacity=44&connect_timeout=4",
			expectedName: "143:operator:0123456789abcdef0123456789abcdef01234567",
		},
		{
			name: "native keyword configuration", build: "dev",
			raw:          "host=localhost port=55432 user=synthetic password=fixture dbname=target sslmode=disable application_name=old Application_Name=mixed options='-c application_name=option -c idle_in_transaction_session_timeout=25000' lock_timeout=3s pool_max_conns=7 pool_min_conns=2 default_query_exec_mode=exec connect_timeout=4",
			expectedName: "143:operator:dev",
		},
		{name: "missing build remains explicit", raw: "postgres://synthetic@localhost/target?sslmode=disable", expectedName: "143:operator:unknown"},
		{name: "arbitrary build stays private", raw: "postgres://synthetic@localhost/target?sslmode=disable", build: "build-a", expectedName: "143:operator:sha256-5e616f6135eb478ecd04b3d3df35b56e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			baseline, err := pgxpool.ParseConfig(tt.raw)
			require.NoError(t, err, "parse the demo pool's native baseline configuration")
			expectedParams := make(map[string]string, len(baseline.ConnConfig.RuntimeParams))
			for key, value := range baseline.ConnConfig.RuntimeParams {
				if !strings.EqualFold(key, "application_name") {
					expectedParams[key] = value
				}
			}
			expectedParams["application_name"] = tt.expectedName
			actual, err := lifecyclePoolConfig(tt.raw, tt.build)
			require.NoError(t, err, "add passive attribution without changing the native connection policy")
			require.Equal(t, demoPoolSettingsOf(baseline), demoPoolSettingsOf(actual), "attribution must preserve native targets, credentials, timeouts and every pool budget")
			require.Equal(t, expectedParams, actual.ConnConfig.RuntimeParams, "only case-insensitive application names may change; options and unrelated GUCs must survive")
			require.Equal(t, pgx.QueryExecModeSimpleProtocol, actual.ConnConfig.DefaultQueryExecMode, "demo seed execution must retain its existing multi-statement simple protocol")
		})
	}
}

func TestDemoSeedLifecyclePoolConfigErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, raw string
	}{
		{name: "malformed URI", raw: "postgres://synthetic@localhost/%invalid"},
		{name: "invalid keyword port", raw: "host=localhost port=invalid dbname=target"},
		{name: "invalid pool budget", raw: "postgres://synthetic@localhost/target?pool_max_conns=invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			baseline, err := pgxpool.ParseConfig(tt.raw)
			require.Error(t, err, "the original parser must reject this synthetic configuration")
			require.Nil(t, baseline, "native parse failures must not produce a usable connection configuration")
			expectedError := fmt.Errorf("parse database URL: %w", err).Error()
			actual, err := lifecyclePoolConfig(tt.raw, "dev")
			require.EqualError(t, err, expectedError, "attribution must retain the existing parser's diagnostics without a second wrapper")
			require.Nil(t, actual, "attribution must never turn a malformed target into a connection attempt")
		})
	}
}
