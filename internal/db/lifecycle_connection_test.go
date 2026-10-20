package db

import (
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestLifecycleConnectionApplicationName(t *testing.T) {
	t.Parallel()

	const build = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name     string
		role     models.LifecycleWriterRole
		build    string
		expected string
	}{
		{name: "API", role: models.LifecycleWriterRoleAPI, build: build, expected: "143:api:" + build},
		{name: "worker", role: models.LifecycleWriterRoleWorker, build: build, expected: "143:worker:" + build},
		{name: "combined", role: models.LifecycleWriterRoleAll, build: build, expected: "143:all:" + build},
		{name: "standalone executor", role: models.LifecycleWriterRoleSessionExecutor, build: build, expected: "143:session_executor:" + build},
		{name: "exact uppercase SHA", role: models.LifecycleWriterRoleAPI, build: strings.ToUpper(build), expected: "143:api:" + strings.ToUpper(build)},
		{name: "development binary", role: models.LifecycleWriterRoleAPI, build: "dev", expected: "143:api:dev"},
		{name: "missing build", role: models.LifecycleWriterRoleAPI, expected: "143:api:unknown"},
		{name: "non-SHA build fingerprint", role: models.LifecycleWriterRoleAPI, build: "build-a", expected: "143:api:sha256-5e616f6135eb478ecd04b3d3df35b56e"},
		{name: "malformed SHA fingerprint", role: models.LifecycleWriterRoleAPI, build: strings.Repeat("g", 40), expected: "143:api:sha256-1d5f913d4591f2de124be0d31d4cebf2"},
		{name: "longer hex build fingerprint", role: models.LifecycleWriterRoleSessionExecutor, build: strings.Repeat("a", 64), expected: "143:session_executor:sha256-ffe054fe7ae0cb6dc65c3af9b61d5209"},
		{name: "long build fingerprint", role: models.LifecycleWriterRoleSessionExecutor, build: strings.Repeat("a", 128), expected: "143:session_executor:sha256-6836cf13bac400e9105071cd6af47084"},
		{name: "path fingerprint", role: models.LifecycleWriterRoleAPI, build: "/synthetic/worktree/build", expected: "143:api:sha256-f576cedc36e4de5d3dd76e2962e84db1"},
		{name: "control bytes fingerprint", role: models.LifecycleWriterRoleAPI, build: "build\x00name\n", expected: "143:api:sha256-0dd64d92fecb8fea6d2b3e708b377326"},
		{name: "non-ASCII fingerprint", role: models.LifecycleWriterRoleAPI, build: "commit-é\n", expected: "143:api:sha256-7e2818b6398c4bedafd9f572832bea31"},
		{name: "whitespace is not a build", role: models.LifecycleWriterRoleAPI, build: " ", expected: "143:api:sha256-36a9e7f1c95b82ffb99743e0c5c4ce95"},
		{name: "missing role preserves connection naming", build: build},
		{name: "unknown role preserves connection naming", role: "scheduler", build: build},
		{name: "operator is not a lease-writer role", role: "operator", build: build},
		{name: "case-sensitive role", role: "API", build: build},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := LifecycleConnectionApplicationName(tt.role, tt.build)
			require.Equal(t, tt.expected, actual, "connection labels must retain only safe exact build values or an explicitly marked fingerprint")
			require.LessOrEqual(t, len(actual), 63, "labels must fit PostgreSQL application_name without server truncation")
			for _, character := range actual {
				require.True(t, character >= 32 && character < 127, "connection labels must be printable ASCII even for malformed build metadata")
			}
		})
	}
}

func TestLifecycleOperatorConnectionApplicationName(t *testing.T) {
	t.Parallel()
	const build = "0123456789aBcDeF0123456789aBcDeF01234567"
	tests := []struct {
		name, build, expected string
	}{
		{name: "exact SHA", build: build, expected: "143:operator:" + build},
		{name: "development binary", build: "dev", expected: "143:operator:dev"},
		{name: "missing build", expected: "143:operator:unknown"},
		{name: "arbitrary build fingerprint", build: "build-a", expected: "143:operator:sha256-5e616f6135eb478ecd04b3d3df35b56e"},
		{name: "invalid SHA fingerprint", build: strings.Repeat("g", 40), expected: "143:operator:sha256-1d5f913d4591f2de124be0d31d4cebf2"},
		{name: "long hex is not truncated", build: strings.Repeat("a", 64), expected: "143:operator:sha256-ffe054fe7ae0cb6dc65c3af9b61d5209"},
		{name: "path remains private", build: "/synthetic/worktree/build", expected: "143:operator:sha256-f576cedc36e4de5d3dd76e2962e84db1"},
		{name: "controls remain private", build: "build\x00name\n", expected: "143:operator:sha256-0dd64d92fecb8fea6d2b3e708b377326"},
		{name: "non-ASCII remains private", build: "commit-é\n", expected: "143:operator:sha256-7e2818b6398c4bedafd9f572832bea31"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := LifecycleOperatorConnectionApplicationName(tt.build)
			require.Equal(t, tt.expected, actual, "operator labels must share the runtime build privacy policy without adopting a lease-writer role")
			require.LessOrEqual(t, len(actual), 63, "operator labels must fit PostgreSQL application_name without truncation")
			for _, character := range actual {
				require.True(t, character >= 32 && character < 127, "operator labels must be printable ASCII for every build input")
			}
		})
	}
}

func TestNewPoolConfigApplicationName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		application string
		expected    string
	}{
		{name: "opt-in overrides DSN naming", application: "143:api:dev", expected: "143:api:dev"},
		{name: "operator opt-in overrides DSN naming", application: "143:operator:dev", expected: "143:operator:dev"},
		{name: "empty option preserves DSN naming", expected: "synthetic-existing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := NewPoolConfig("postgres://synthetic:fixture@localhost/synthetic?application_name=synthetic-existing&lock_timeout=3s&statement_timeout=7s&search_path=synthetic&pool_max_conns=9&pool_min_conns=2&pool_min_idle_conns=1", PoolOptions{
				MaxConns: 4, MaxConnIdleTime: time.Minute, ApplicationName: tt.application,
			})
			require.NoError(t, err, "optional attribution must compose with existing connection and pool configuration")
			require.Equal(t, map[string]string{
				"application_name": tt.expected, "lock_timeout": "3s", "statement_timeout": "7s", "search_path": "synthetic",
			}, cfg.ConnConfig.RuntimeParams, "only the explicitly selected application_name may change startup runtime parameters")
			require.Equal(t, [3]int32{4, 2, 1}, [3]int32{cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns}, "attribution must not change configured connection budgets")
			require.Equal(t, time.Minute, cfg.MaxConnIdleTime, "attribution must retain configured idle cleanup")
		})
	}
}
