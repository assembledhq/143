package preview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_SingleService(t *testing.T) {
	t.Parallel()

	raw := `{
		"version": "3",
		"name": "frontend",
		"command": ["npm", "run", "dev"],
		"cwd": "frontend",
		"port": 3000,
		"env": {"NODE_ENV": "development"},
		"ready": {"http_path": "/", "timeout_seconds": 90},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed", "destinations": []}
	}`

	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	// Should normalize to multi-service with name as service key.
	if cfg.Primary != "frontend" {
		t.Errorf("Primary = %q, want %q", cfg.Primary, "frontend")
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("len(Services) = %d, want 1", len(cfg.Services))
	}
	svc, ok := cfg.Services["frontend"]
	if !ok {
		t.Fatal("Services[\"frontend\"] not found")
	}
	if svc.Port != 3000 {
		t.Errorf("Port = %d, want 3000", svc.Port)
	}
	if svc.Cwd != "frontend" {
		t.Errorf("Cwd = %q, want %q", svc.Cwd, "frontend")
	}
	if len(svc.Command) != 3 || svc.Command[0] != "npm" {
		t.Errorf("Command = %v, want [npm run dev]", svc.Command)
	}
}

func TestParseConfig_MultiService(t *testing.T) {
	t.Parallel()

	raw := `{
		"version": "3",
		"name": "Full Stack",
		"primary": "frontend",
		"services": {
			"frontend": {
				"command": ["npm", "run", "dev"],
				"port": 3000,
				"ready": {"http_path": "/", "timeout_seconds": 90}
			},
			"backend": {
				"command": ["python", "manage.py", "runserver", "0.0.0.0:4000"],
				"cwd": "backend",
				"port": 4000,
				"ready": {"http_path": "/health", "timeout_seconds": 60}
			}
		},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed"}
	}`

	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	if cfg.Primary != "frontend" {
		t.Errorf("Primary = %q, want %q", cfg.Primary, "frontend")
	}
	if len(cfg.Services) != 2 {
		t.Errorf("len(Services) = %d, want 2", len(cfg.Services))
	}
}

func TestParseConfig_WithInfrastructure(t *testing.T) {
	t.Parallel()

	raw := `{
		"version": "3",
		"name": "Full Stack (Local DB)",
		"primary": "frontend",
		"services": {
			"frontend": {"command": ["npm", "run", "dev"], "port": 3000, "ready": {"http_path": "/"}}
		},
		"infrastructure": {
			"db": {
				"template": "postgres-16",
				"init_script": "db/seed.sql",
				"inject_env": {"DATABASE_URL": "postgres://{{username}}:{{password}}@{{host}}:{{port}}/{{database}}"},
				"inject_into": ["frontend"]
			}
		},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed"}
	}`

	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	if len(cfg.Infrastructure) != 1 {
		t.Fatalf("len(Infrastructure) = %d, want 1", len(cfg.Infrastructure))
	}
	db := cfg.Infrastructure["db"]
	if db.Template != "postgres-16" {
		t.Errorf("Template = %q, want %q", db.Template, "postgres-16")
	}
	if db.InitScript != "db/seed.sql" {
		t.Errorf("InitScript = %q, want %q", db.InitScript, "db/seed.sql")
	}
}

func TestParseConfig_FromRepoConfigPreviewSection(t *testing.T) {
	t.Parallel()

	raw := `{
		"preview": {
			"version": "3",
			"name": "dogfood",
			"primary": "web",
			"services": {
				"web": {
					"command": ["npm", "run", "dev"],
					"port": 3000,
					"ready": {"http_path": "/"}
				}
			},
			"credentials": {"mode": "none"},
			"network": {"mode": "managed"}
		},
		"validation": {
			"commands": ["npm run lint:js"]
		}
	}`

	cfg, err := ParseConfig([]byte(raw))
	require.NoError(t, err, "ParseConfig should accept nested preview config inside .143/config.json")
	require.Equal(t, "web", cfg.Primary, "ParseConfig should parse the nested preview section")
	require.Contains(t, cfg.Services, "web", "ParseConfig should preserve services from the nested preview section")
}

func TestDogfoodPreviewConfig_ServerUsesRegisteredReadinessPath(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "test should resolve its source file path")
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))

	raw, err := os.ReadFile(filepath.Join(repoRoot, ".143", "config.json"))
	require.NoError(t, err, "dogfood preview config should be readable")

	cfg, err := ParseConfig(raw)
	require.NoError(t, err, "dogfood preview config should parse")
	server, ok := cfg.Services["server"]
	require.True(t, ok, "dogfood preview config should define the server service")
	require.Equal(t, "/readyz", server.Ready.HTTPPath, "server readiness probe should hit the registered public readiness endpoint")
}

func TestParseConfig_RepoConfigWithoutPreviewSection(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig([]byte(`{
		"validation": {
			"commands": ["npm run lint:js"]
		}
	}`))
	require.Error(t, err, "ParseConfig should reject .143/config.json files that omit the preview section")
	require.Contains(t, err.Error(), "missing preview section", "ParseConfig should explain that .143/config.json requires a preview section for preview parsing")
}

func TestParseConfig_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseConfig_BothFormats(t *testing.T) {
	t.Parallel()

	raw := `{
		"version": "3",
		"name": "test",
		"command": ["echo"],
		"port": 3000,
		"services": {"web": {"command": ["npm", "start"], "port": 3000, "ready": {"http_path": "/"}}},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed"}
	}`
	_, err := ParseConfig([]byte(raw))
	if err == nil {
		t.Fatal("expected error when both single-service and multi-service fields present")
	}
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     models.PreviewConfig
		wantErr int // expected error count, 0 = valid
	}{
		{
			name: "valid single service",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 0,
		},
		{
			name: "valid multi service",
			cfg: models.PreviewConfig{
				Primary: "frontend",
				Services: map[string]models.ServiceConfig{
					"frontend": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
					"backend":  {Command: []string{"python", "app.py"}, Port: 4000, Ready: models.ReadinessProbe{HTTPPath: "/health"}},
				},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 0,
		},
		{
			name: "missing primary",
			cfg: models.PreviewConfig{
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "primary references missing service",
			cfg: models.PreviewConfig{
				Primary:        "missing",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "no services",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 2, // no services + primary references missing
		},
		{
			name: "too many services",
			cfg: models.PreviewConfig{
				Primary: "a",
				Services: map[string]models.ServiceConfig{
					"a": {Command: []string{"a"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
					"b": {Command: []string{"b"}, Port: 3001, Ready: models.ReadinessProbe{HTTPPath: "/"}},
					"c": {Command: []string{"c"}, Port: 3002, Ready: models.ReadinessProbe{HTTPPath: "/"}},
					"d": {Command: []string{"d"}, Port: 3003, Ready: models.ReadinessProbe{HTTPPath: "/"}},
					"e": {Command: []string{"e"}, Port: 3004, Ready: models.ReadinessProbe{HTTPPath: "/"}},
				},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "port out of range",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 80, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "duplicate ports",
			cfg: models.PreviewConfig{
				Primary: "a",
				Services: map[string]models.ServiceConfig{
					"a": {Command: []string{"a"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
					"b": {Command: []string{"b"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
				},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "missing command",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "missing ready path",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "shell injection in ready path",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/; rm -rf /"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "cwd escapes repo root",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Cwd: "../../etc", Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "cwd is absolute path",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Cwd: "/etc/passwd", Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantErr: 1,
		},
		{
			name: "unsupported infra template",
			cfg: models.PreviewConfig{
				Primary:  "app",
				Services: map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{
					"search": {Template: "elasticsearch-8"},
				},
			},
			wantErr: 1,
		},
		{
			name: "too many infra",
			cfg: models.PreviewConfig{
				Primary:  "app",
				Services: map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{
					"db":    {Template: "postgres-16"},
					"cache": {Template: "redis-7"},
					"extra": {Template: "mysql-8"},
				},
			},
			wantErr: 1,
		},
		{
			name: "infra inject_into unknown service",
			cfg: models.PreviewConfig{
				Primary:  "app",
				Services: map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{
					"db": {Template: "postgres-16", InjectInto: []string{"nonexistent"}},
				},
			},
			wantErr: 1,
		},
		{
			name: "credential inject_into unknown service",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
				Credentials:    models.CredentialConfig{Mode: "managed_env", InjectInto: []string{"unknown"}},
			},
			wantErr: 1,
		},
		{
			name: "init_script escapes repo",
			cfg: models.PreviewConfig{
				Primary:  "app",
				Services: map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{
					"db": {Template: "postgres-16", InitScript: "../../../etc/passwd"},
				},
			},
			wantErr: 1,
		},
		{
			name: "invalid network mode",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
				Network:        models.NetworkConfig{Mode: "bogus"},
			},
			wantErr: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := ValidateConfig(&tt.cfg)
			if len(errs) != tt.wantErr {
				t.Errorf("ValidateConfig() returned %d errors, want %d: %v", len(errs), tt.wantErr, errs)
			}
		})
	}
}

func TestResolveConfig_NonConnected(t *testing.T) {
	t.Parallel()

	baseCfg := &models.PreviewConfig{
		Version: "3",
		Name:    "Test",
		Primary: "frontend",
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
			"backend":  {Command: []string{"python", "app.py"}, Port: 4000, Ready: models.ReadinessProbe{HTTPPath: "/health"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/base_seed.sql", InjectInto: []string{"backend"}},
		},
		Credentials: models.CredentialConfig{Mode: "none"},
		Network:     models.NetworkConfig{Mode: "managed"},
	}

	diffCfg := &models.PreviewConfig{
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "run", "dev"}, Port: 3000, Cwd: "frontend", Ready: models.ReadinessProbe{HTTPPath: "/", TimeoutSeconds: 120}},
			"backend":  {Command: []string{"python", "app.py", "--debug"}, Port: 4000, Ready: models.ReadinessProbe{HTTPPath: "/health"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/test_seed.sql"},
		},
	}

	resolved := ResolveConfig(baseCfg, diffCfg)

	// Primary comes from base.
	if resolved.Primary != "frontend" {
		t.Errorf("Primary = %q, want %q", resolved.Primary, "frontend")
	}

	// Credentials and network from base.
	if resolved.Credentials.Mode != "none" {
		t.Errorf("Credentials.Mode = %q, want %q", resolved.Credentials.Mode, "none")
	}

	// Service runtime fields from diff.
	fe := resolved.Services["frontend"]
	if fe.Cwd != "frontend" {
		t.Errorf("frontend.Cwd = %q, want %q", fe.Cwd, "frontend")
	}
	if fe.Command[0] != "npm" || fe.Command[1] != "run" {
		t.Errorf("frontend.Command = %v, want [npm run dev]", fe.Command)
	}

	be := resolved.Services["backend"]
	if len(be.Command) != 3 || be.Command[2] != "--debug" {
		t.Errorf("backend.Command = %v, want [python app.py --debug]", be.Command)
	}

	// Infrastructure: template/inject from base, init_script from diff.
	db := resolved.Infrastructure["db"]
	if db.Template != "postgres-16" {
		t.Errorf("db.Template = %q, want %q", db.Template, "postgres-16")
	}
	if db.InitScript != "db/test_seed.sql" {
		t.Errorf("db.InitScript = %q, want %q (from diff)", db.InitScript, "db/test_seed.sql")
	}
	if len(db.InjectInto) != 1 || db.InjectInto[0] != "backend" {
		t.Errorf("db.InjectInto = %v, want [backend] (from base)", db.InjectInto)
	}
}

func TestResolveConfig_Connected_PinsEverythingToBase(t *testing.T) {
	t.Parallel()

	baseCfg := &models.PreviewConfig{
		Version: "3",
		Primary: "frontend",
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/base_seed.sql"},
		},
		Credentials: models.CredentialConfig{Mode: "managed_env", CredentialSet: "staging"},
		Network:     models.NetworkConfig{Mode: "managed", Destinations: []string{"staging_db"}},
	}

	diffCfg := &models.PreviewConfig{
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "run", "malicious"}, Port: 9999, Cwd: "/etc"},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/malicious.sql"},
		},
	}

	resolved := ResolveConfig(baseCfg, diffCfg)

	// All service fields pinned to base.
	fe := resolved.Services["frontend"]
	if fe.Command[0] != "npm" || fe.Command[1] != "start" {
		t.Errorf("Command = %v, want [npm start] (pinned to base)", fe.Command)
	}
	if fe.Port != 3000 {
		t.Errorf("Port = %d, want 3000 (pinned to base)", fe.Port)
	}

	// init_script pinned to base for connected config.
	db := resolved.Infrastructure["db"]
	if db.InitScript != "db/base_seed.sql" {
		t.Errorf("InitScript = %q, want %q (pinned to base for connected)", db.InitScript, "db/base_seed.sql")
	}
}

func TestResolveConfig_DiffCannotAddServices(t *testing.T) {
	t.Parallel()

	baseCfg := &models.PreviewConfig{
		Version: "3",
		Primary: "app",
		Services: map[string]models.ServiceConfig{
			"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{},
		Credentials:    models.CredentialConfig{Mode: "none"},
	}

	diffCfg := &models.PreviewConfig{
		Services: map[string]models.ServiceConfig{
			"app":     {Command: []string{"npm", "dev"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
			"sneaked": {Command: []string{"evil"}, Port: 6666, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
	}

	resolved := ResolveConfig(baseCfg, diffCfg)

	// Only services from base should exist.
	if len(resolved.Services) != 1 {
		t.Errorf("len(Services) = %d, want 1 (diff cannot add services)", len(resolved.Services))
	}
	if _, ok := resolved.Services["sneaked"]; ok {
		t.Error("diff-added service 'sneaked' should not appear in resolved config")
	}
}

func TestIsConnected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  models.PreviewConfig
		want bool
	}{
		{
			name: "mode none",
			cfg:  models.PreviewConfig{Credentials: models.CredentialConfig{Mode: "none"}},
			want: false,
		},
		{
			name: "mode empty",
			cfg:  models.PreviewConfig{Credentials: models.CredentialConfig{Mode: ""}},
			want: false,
		},
		{
			name: "managed_env",
			cfg:  models.PreviewConfig{Credentials: models.CredentialConfig{Mode: "managed_env"}},
			want: true,
		},
		{
			name: "has destinations",
			cfg:  models.PreviewConfig{Network: models.NetworkConfig{Destinations: []string{"db"}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsConnected(&tt.cfg); got != tt.want {
				t.Errorf("IsConnected() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       models.PreviewConfig
		wantReady models.PreviewReadiness
	}{
		{
			name: "ready - simple config",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
				Credentials:    models.CredentialConfig{Mode: "none"},
			},
			wantReady: models.PreviewReadinessReady,
		},
		{
			name: "admin setup - has credentials",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
				Credentials:    models.CredentialConfig{Mode: "managed_env", CredentialSet: "staging", Env: []string{"DB_URL"}},
			},
			wantReady: models.PreviewReadinessAdminSetupRequired,
		},
		{
			name: "admin setup - has destinations",
			cfg: models.PreviewConfig{
				Primary:        "app",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
				Network:        models.NetworkConfig{Mode: "managed", Destinations: []string{"staging_db"}},
			},
			wantReady: models.PreviewReadinessAdminSetupRequired,
		},
		{
			name: "not supported - invalid config",
			cfg: models.PreviewConfig{
				Primary:        "missing",
				Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
				Infrastructure: map[string]models.InfrastructureConfig{},
			},
			wantReady: models.PreviewReadinessNotSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := DetectReadiness(&tt.cfg)
			if result.Readiness != tt.wantReady {
				t.Errorf("Readiness = %q, want %q (errors: %v)", result.Readiness, tt.wantReady, result.ValidationErrors)
			}
		})
	}
}

func TestResolveResourceLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      models.PreviewConfig
		expected models.ResourceLimits
	}{
		{
			name: "single service uses small preview tier",
			cfg: models.PreviewConfig{
				Services: map[string]models.ServiceConfig{"app": {}},
			},
			expected: models.ResourceLimits{MemoryMB: 512, CPUMillis: 500},
		},
		{
			name: "multi service without managed infrastructure uses standard preview tier",
			cfg: models.PreviewConfig{
				Services: map[string]models.ServiceConfig{"a": {}, "b": {}},
			},
			expected: models.ResourceLimits{MemoryMB: 1024, CPUMillis: 1000},
		},
		{
			name: "multi service with managed infrastructure uses heavy preview tier",
			cfg: models.PreviewConfig{
				Services: map[string]models.ServiceConfig{"frontend": {}, "server": {}},
				Infrastructure: map[string]models.InfrastructureConfig{
					"db": {Template: "postgres-17"},
				},
			},
			expected: models.ResourceLimits{MemoryMB: 2048, CPUMillis: 2000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ResolveResourceLimits(&tt.cfg), "resource tier should match preview topology")
		})
	}
}

func TestApplyResourceLimitsToSandboxConfig(t *testing.T) {
	t.Parallel()

	cfg := &models.PreviewConfig{
		Services: map[string]models.ServiceConfig{
			"frontend": {},
			"server":   {},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-17"},
		},
	}
	sandboxCfg := agent.DefaultSandboxConfig()

	ApplyResourceLimitsToSandboxConfig(&sandboxCfg, cfg)

	require.Equal(t, 2048, sandboxCfg.MemoryLimitMB, "sandbox config should use the preview topology memory tier")
	require.Equal(t, 2.0, sandboxCfg.CPULimit, "sandbox config should convert preview millicores into CPU cores")
}

func TestLookupInfraTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		found bool
	}{
		{"postgres-17", true},
		{"postgres-16", true},
		{"redis-7", true},
		{"mysql-8", true},
		{"elasticsearch-8", false},
		{"kafka-3", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, ok := LookupInfraTemplate(tt.name)
			if ok != tt.found {
				t.Errorf("LookupInfraTemplate(%q) found = %v, want %v", tt.name, ok, tt.found)
			}
		})
	}
}

func TestParseConfig_RoundTrip(t *testing.T) {
	t.Parallel()

	// Parse, then marshal back, then parse again — should be equivalent.
	raw := `{
		"version": "3",
		"name": "frontend",
		"command": ["npm", "run", "dev"],
		"port": 3000,
		"ready": {"http_path": "/"},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed"}
	}`

	cfg1, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}

	data, err := json.Marshal(cfg1)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The marshaled form is in multi-service format, so parse it as such.
	cfg2 := &models.PreviewConfig{}
	if err := json.Unmarshal(data, cfg2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg1.Primary != cfg2.Primary {
		t.Errorf("Primary mismatch: %q vs %q", cfg1.Primary, cfg2.Primary)
	}
	if len(cfg1.Services) != len(cfg2.Services) {
		t.Errorf("Services count mismatch: %d vs %d", len(cfg1.Services), len(cfg2.Services))
	}
}

// TestParseConfig_CommittedDogfoodConfig guards against silent regressions
// in the committed repo preview config at the repo root. The dogfood config
// is the configuration used when a 143 session on this very repo clicks
// "Start Preview", so a broken file manifests as a broken developer loop.
func TestParseConfig_CommittedDogfoodConfig(t *testing.T) {
	t.Parallel()

	// Walk up from the package dir to the repo root (where `.143/` lives).
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, ".143", "config.json")
		if _, err := os.Stat(candidate); err == nil {
			raw, err := os.ReadFile(candidate)
			if err != nil {
				t.Fatalf("read %s: %v", candidate, err)
			}
			if _, err := ParseConfig(raw); err != nil {
				t.Fatalf("committed .143/config.json failed to parse: %v", err)
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("repo root .143/config.json not found from test working directory")
		}
		dir = parent
	}
}

func TestCommittedDogfoodFrontendScriptBindsExternally(t *testing.T) {
	t.Parallel()

	// Walk up from the package dir to the repo root (where `.143/` lives).
	dir, err := os.Getwd()
	require.NoError(t, err, "test should resolve the package working directory")
	for {
		candidate := filepath.Join(dir, ".143", "preview-frontend.sh")
		if _, err := os.Stat(candidate); err == nil {
			raw, err := os.ReadFile(candidate)
			require.NoError(t, err, "test should read committed dogfood frontend preview script")
			require.Contains(t, string(raw), "HOSTNAME=0.0.0.0", "dogfood Next preview must bind externally so the worker proxy can dial the sandbox IP")
			require.Contains(t, string(raw), "npm run build", "dogfood Next preview should run a production build before serving")
			require.Contains(t, string(raw), "cp -R .next/static .next/standalone/frontend/.next/static", "dogfood Next preview should stage generated CSS and other static chunks next to the standalone server")
			require.Contains(t, string(raw), "cp -R public .next/standalone/frontend/public", "dogfood Next preview should stage public assets next to the standalone server")
			require.Contains(t, string(raw), "node .next/standalone/frontend/server.js", "dogfood Next preview should serve the standalone production build")
			require.NotContains(t, string(raw), "npm run dev", "dogfood Next preview must avoid dev server HMR in the preview gateway")
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("repo root .143/preview-frontend.sh not found from test working directory")
		}
		dir = parent
	}
}
