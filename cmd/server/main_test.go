package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/services/agent"
)

type mainTestLiveSandboxCounter struct {
	count int
}

func (m mainTestLiveSandboxCounter) CountLiveSandboxes(context.Context) (int, error) {
	return m.count, nil
}

func TestBuildBaseMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		previewCapable         bool
		previewInternalBaseURL string
		wantPreviewCapable     bool
		wantInternalBaseURL    string
		wantAuthCheck          bool
	}{
		{
			name:                   "preview-capable worker advertises both fields",
			previewCapable:         true,
			previewInternalBaseURL: "http://worker-1:8080",
			wantPreviewCapable:     true,
			wantInternalBaseURL:    "http://worker-1:8080",
			wantAuthCheck:          true,
		},
		{
			name:                   "non-preview-capable node omits preview_capable",
			previewCapable:         false,
			previewInternalBaseURL: "",
			wantPreviewCapable:     false,
			wantInternalBaseURL:    "",
		},
		{
			name:                   "internal base URL without capability still emits URL",
			previewCapable:         false,
			previewInternalBaseURL: "http://worker-1:8080",
			wantPreviewCapable:     false,
			wantInternalBaseURL:    "http://worker-1:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metadata := buildBaseMetadata(tt.previewCapable, tt.previewInternalBaseURL, "")

			if _, ok := metadata["build_sha"]; !ok {
				t.Errorf("expected build_sha to always be present")
			}

			gotCapable, hasCapable := metadata["preview_capable"]
			if tt.wantPreviewCapable {
				if !hasCapable || gotCapable != true {
					t.Errorf("expected preview_capable=true, got %v (present=%v)", gotCapable, hasCapable)
				}
			} else if hasCapable {
				t.Errorf("expected preview_capable to be omitted, got %v", gotCapable)
			}

			gotURL, hasURL := metadata["preview_internal_base_url"]
			if tt.wantInternalBaseURL != "" {
				if !hasURL || gotURL != tt.wantInternalBaseURL {
					t.Errorf("expected preview_internal_base_url=%q, got %v (present=%v)", tt.wantInternalBaseURL, gotURL, hasURL)
				}
			} else if hasURL {
				t.Errorf("expected preview_internal_base_url to be omitted, got %v", gotURL)
			}

			gotAuthCheck, hasAuthCheck := metadata["preview_rpc_auth_check"]
			if tt.wantAuthCheck {
				if !hasAuthCheck || gotAuthCheck != true {
					t.Errorf("expected preview_rpc_auth_check=true, got %v (present=%v)", gotAuthCheck, hasAuthCheck)
				}
			} else if hasAuthCheck {
				t.Errorf("expected preview_rpc_auth_check to be omitted, got %v", gotAuthCheck)
			}
		})
	}
}

// TestBuildWorkerMetadataProvider_PreservesPreviewFields guards against the
// regression where SetMetadataProvider in startProcessWorkers replaced the
// initial provider without re-emitting preview-routing fields, causing the
// next heartbeat to wipe preview_capable and break Start Preview routing.
func TestBuildWorkerMetadataProvider_PreservesPreviewFields(t *testing.T) {
	t.Parallel()

	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", "", func() bool { return true }, nil, agent.StaticEgressRuntimeConfig{})

	metadata := provider()

	if got, ok := metadata["preview_capable"]; !ok || got != true {
		t.Errorf("preview_capable must persist across worker startup, got %v (present=%v)", got, ok)
	}
	if got, ok := metadata["preview_internal_base_url"]; !ok || got != "http://worker-1:8080" {
		t.Errorf("preview_internal_base_url must persist across worker startup, got %v (present=%v)", got, ok)
	}
	if got, ok := metadata["preview_rpc_auth_check"]; !ok || got != true {
		t.Errorf("preview_rpc_auth_check must persist across worker startup, got %v (present=%v)", got, ok)
	}
	if _, ok := metadata["active_job_count"]; !ok {
		t.Errorf("expected active_job_count to be present in worker metadata")
	}
	if _, ok := metadata["active_run_agent_count"]; !ok {
		t.Errorf("expected active_run_agent_count to be present in worker metadata")
	}
}

func TestBuildWorkerMetadataProvider_NonPreviewCapable(t *testing.T) {
	t.Parallel()

	provider := buildWorkerMetadataProvider(nil, false, "", "", func() bool { return true }, nil, agent.StaticEgressRuntimeConfig{})

	metadata := provider()

	if _, ok := metadata["preview_capable"]; ok {
		t.Errorf("preview_capable should be omitted when worker is not preview-capable")
	}
	if _, ok := metadata["preview_rpc_auth_check"]; ok {
		t.Errorf("preview_rpc_auth_check should be omitted when worker is not preview-capable")
	}
	if _, ok := metadata["preview_internal_base_url"]; ok {
		t.Errorf("preview_internal_base_url should be omitted when not configured")
	}
}

func TestBuildWorkerMetadataProvider_DelaysPreviewCapabilityUntilReady(t *testing.T) {
	t.Parallel()

	ready := false
	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", "", func() bool { return ready }, nil, agent.StaticEgressRuntimeConfig{})

	metadata := provider()
	require.NotContains(t, metadata, "preview_capable", "preview_capable should be hidden until the HTTP listener is bound")
	require.NotContains(t, metadata, "preview_rpc_auth_check", "preview auth-check capability should be hidden until the HTTP listener is bound")
	require.Equal(t, "http://worker-1:8080", metadata["preview_internal_base_url"], "preview internal URL should remain available in metadata")

	ready = true
	metadata = provider()
	require.Equal(t, true, metadata["preview_capable"], "preview_capable should be advertised once routing is ready")
	require.Equal(t, true, metadata["preview_rpc_auth_check"], "preview auth-check capability should be advertised once routing is ready")
}

func TestBuildStaticEgressMetadataRequiresVerifiedCapability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		runtime agent.StaticEgressRuntimeConfig
		want    map[string]any
	}{
		{
			name: "configured but not verified",
			runtime: agent.StaticEgressRuntimeConfig{
				Enabled:  true,
				Capable:  false,
				PublicIP: "203.0.113.10",
			},
			want: map[string]any{},
		},
		{
			name: "verified static egress",
			runtime: agent.StaticEgressRuntimeConfig{
				Enabled:  true,
				Capable:  true,
				PublicIP: "203.0.113.10",
			},
			want: map[string]any{
				"static_egress_capable":   true,
				"static_egress_public_ip": "203.0.113.10",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildStaticEgressMetadata(tt.runtime)
			require.Equal(t, tt.want, got, "worker metadata should only advertise verified static egress capability")
		})
	}
}

func TestResolveWorkerMaxActiveSandboxes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		workerProcessCount int
		configured         int
		expected           int
	}{
		{name: "explicit cap wins", workerProcessCount: 4, configured: 6, expected: 6},
		{name: "zero cap derives from process count", workerProcessCount: 4, configured: 0, expected: 4},
		{name: "invalid process count falls back to config default", workerProcessCount: 0, configured: 0, expected: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := resolveWorkerMaxActiveSandboxes(tt.workerProcessCount, tt.configured)

			require.Equal(t, tt.expected, got, "resolved live sandbox capacity should follow the configured precedence")
		})
	}
}

func TestPreviewDependencyCacheEnabledWithConfiguredBucket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{
			name: "bucket enables cache by default",
			cfg: config.Config{
				PreviewDependencyCacheBucket: "preview-dependency-cache",
			},
			want: true,
		},
		{
			name: "empty bucket keeps cache disabled",
			cfg:  config.Config{},
			want: false,
		},
		{
			name: "blank bucket keeps cache disabled",
			cfg: config.Config{
				PreviewDependencyCacheBucket: "   ",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := previewDependencyCacheEnabled(tt.cfg)

			require.Equal(t, tt.want, got, "dependency cache should be enabled exactly when an L2 bucket is configured")
		})
	}
}

func TestPreviewDependencyCacheUsesNormalizedLocalDir(t *testing.T) {
	t.Parallel()

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, parser.ParseComments)
	require.NoError(t, err, "test should parse cmd/server/main.go")

	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		kv, ok := node.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "LocalDir" {
			return true
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "ResolvePreviewDependencyCacheLocalDir" {
			return true
		}
		found = true
		return false
	})

	require.True(t, found, "dependency cache construction should normalize PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR so opt-out sentinels disable L1")
}

func TestValidateSessionExecutorStartupConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name: "production worker requires executor image",
			cfg: config.Config{
				Env:  "production",
				Mode: "worker",
			},
			wantErr: true,
		},
		{
			name: "production all mode requires executor image",
			cfg: config.Config{
				Env:  "production",
				Mode: "all",
			},
			wantErr: true,
		},
		{
			name: "production api mode does not require executor image",
			cfg: config.Config{
				Env:  "production",
				Mode: "api",
			},
		},
		{
			name: "local worker can fall back to inline execution",
			cfg: config.Config{
				Env:  "development",
				Mode: "worker",
			},
		},
		{
			name: "production worker accepts configured executor image",
			cfg: config.Config{
				Env:                  "production",
				Mode:                 "worker",
				SessionExecutorImage: "143:test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSessionExecutorStartupConfig(&tt.cfg)

			if tt.wantErr {
				require.Error(t, err, "production worker-capable modes should fail fast without an executor image")
				require.Contains(t, err.Error(), "SESSION_EXECUTOR_IMAGE", "error should name the missing production setting")
				return
			}
			require.NoError(t, err, "startup config should allow this executor configuration")
		})
	}
}

func TestSessionExecutorGroupAdd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dockerGID string
		expected  []string
	}{
		{name: "empty docker gid omits supplemental groups", dockerGID: "", expected: nil},
		{name: "configured docker gid is passed through", dockerGID: "123", expected: []string{"123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sessionExecutorGroupAdd(tt.dockerGID)

			require.Equal(t, tt.expected, got, "session executor group add should reflect the configured Docker socket group")
		})
	}
}

func TestSessionExecutorBindsIncludeStaticEgressCapabilityMount(t *testing.T) {
	t.Parallel()

	expected := []string{
		"/var/run/docker.sock:/var/run/docker.sock",
		"/var/run/143/sandbox-auth:/var/run/143/sandbox-auth",
		"/etc/143:/etc/143:ro",
	}

	require.Equal(t, expected, sessionExecutorBinds(), "session executors should mount every host resource needed for sandbox creation")
}

func TestSessionExecutorIDFromEnv(t *testing.T) {
	t.Setenv("SESSION_EXECUTOR_ID", "")
	id, ok, err := sessionExecutorIDFromEnv()
	require.NoError(t, err, "empty SESSION_EXECUTOR_ID should not error")
	require.False(t, ok, "empty SESSION_EXECUTOR_ID should report no session executor")
	require.Equal(t, uuid.Nil, id, "empty SESSION_EXECUTOR_ID should return nil uuid")

	t.Setenv("SESSION_EXECUTOR_ID", "2af66543-71df-4d0c-911c-f2c77accaf4b")
	id, ok, err = sessionExecutorIDFromEnv()
	require.NoError(t, err, "valid SESSION_EXECUTOR_ID should parse")
	require.True(t, ok, "valid SESSION_EXECUTOR_ID should report session executor mode")
	require.Equal(t, "2af66543-71df-4d0c-911c-f2c77accaf4b", id.String(), "valid SESSION_EXECUTOR_ID should return parsed uuid")

	t.Setenv("SESSION_EXECUTOR_ID", "not-a-uuid")
	_, ok, err = sessionExecutorIDFromEnv()
	require.Error(t, err, "invalid SESSION_EXECUTOR_ID should error")
	require.True(t, ok, "invalid non-empty SESSION_EXECUTOR_ID should still report executor intent")
}

func TestBuildWorkerMetadataProvider_IncludesSandboxCapacity(t *testing.T) {
	t.Parallel()

	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   mainTestLiveSandboxCounter{count: 2},
		MaxActive: 4,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", "", func() bool { return true }, gate, agent.StaticEgressRuntimeConfig{})

	metadata := provider()

	require.Equal(t, 2, metadata["live_sandbox_count"], "worker metadata should expose local live sandbox count")
	require.Equal(t, 0, metadata["reserved_sandbox_count"], "worker metadata should expose in-flight sandbox reservations")
	require.Equal(t, 4, metadata["max_active_sandboxes"], "worker metadata should expose the per-machine sandbox cap")
}

// TestMainStartupReconcilesSocketsBeforeWorkers guards the sandbox-auth socket
// invariant: the worker must re-pin credential sockets for containers that
// survived a restart before process workers can claim jobs, so an in-flight
// agent's git push never races an empty socket. The boot-time reconciler pass
// (socketReconciler.ReconcileOnce) supersedes the old node-scoped rehydrate.
func TestMainStartupReconcilesSocketsBeforeWorkers(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for startup ordering regression test")

	body := string(src)
	startWorkers := strings.Index(body, "processWorkers = startProcessWorkers(")
	reconcile := strings.Index(body, "socketReconciler.ReconcileOnce(")
	require.NotEqual(t, -1, startWorkers, "startup should still start process workers")
	require.NotEqual(t, -1, reconcile, "startup should run the sandbox auth socket reconciler")
	require.Less(t, reconcile, startWorkers, "sandbox auth socket reconcile must run before process workers can claim jobs")
}

func TestMainStartupDoesNotSweepSandboxAuthSocketDirs(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for startup socket-sweep regression test")

	body := string(src)
	require.NotContains(t, body, "SandboxAuthSweep", "startup must not sweep sandbox auth socket dirs because a new worker generation can unlink sockets owned by an older generation during rolling deploy")
	require.NotContains(t, body, "SweepStaleSessionDirs", "startup must not call the low-level sandbox auth socket sweep during rolling deploy")
}

func TestMainRegistersInternalSandboxAuthBrokerBeforeWorkers(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for startup ordering regression test")

	body := string(src)
	registerBroker := strings.Index(body, "registerInternalSandboxAuthRoutes(router, services.SandboxAuthBroker")
	startWorkers := strings.Index(body, "processWorkers = startProcessWorkers(")
	require.NotEqual(t, -1, registerBroker, "startup should register internal sandbox auth broker routes")
	require.NotEqual(t, -1, startWorkers, "startup should still start process workers")
	require.Less(t, registerBroker, startWorkers, "internal sandbox auth broker routes must exist before session executors can be launched")
}

func TestBuildServicesSessionExecutorUsesRemoteSandboxAuthBroker(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for sandbox auth broker wiring regression test")

	body := string(src)
	roleDetect := strings.Index(body, "sessionExecutorIDFromEnv()")
	remoteClient := strings.Index(body, "sandboxauth.NewRemoteBrokerClient")
	localServer := strings.Index(body, "sandboxauth.NewServer")
	require.NotEqual(t, -1, roleDetect, "buildServices should detect session executor role")
	require.NotEqual(t, -1, remoteClient, "session executors should use the remote sandbox auth broker client")
	require.NotEqual(t, -1, localServer, "long-lived workers should still construct the local socket server")
	require.Less(t, roleDetect, remoteClient, "session executor role detection should happen before constructing the remote broker client")
	require.Less(t, remoteClient, localServer, "session executor branch should avoid falling through to local socket server construction")
}

func TestBuildServicesWiresLinearAgentWorkerDepsWithoutFeatureFlagGate(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for Linear agent worker wiring regression test")

	body := string(src)
	depsAssign := strings.Index(body, "svc.LinearAgentDeps = &worker.LinearAgentEventHandlerDeps")
	require.NotEqual(t, -1, depsAssign, "buildServices should still wire LinearAgentDeps")

	beforeDeps := body[:depsAssign]
	lastFeatureFlagGate := strings.LastIndex(beforeDeps, "if cfg.LinearAgentEnabled")
	lastFuncStart := strings.LastIndex(beforeDeps, "func buildServices(")
	require.Less(t, lastFeatureFlagGate, lastFuncStart, "LinearAgentDeps must be wired even when LINEAR_AGENT_ENABLED=false so queued jobs drain")
}

func TestMainProductionWorkersPreflightSandboxAuthBeforeConstructingServer(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for startup preflight regression test")

	body := string(src)
	preflight := strings.Index(body, "sandboxauth.ValidateSocketDirForStartup")
	construct := strings.Index(body, "sandboxauth.NewServer")
	require.NotEqual(t, -1, preflight, "worker startup should preflight the sandbox auth socket dir")
	require.NotEqual(t, -1, construct, "worker startup should still construct the sandbox auth server")
	require.Less(t, preflight, construct, "startup preflight must run before constructing the socket server so bad workers fail before claiming jobs")
	require.Contains(t, body, `cfg.Env == "production"`, "the sandbox auth preflight should be production-scoped so local dev can opt into the legacy fallback path")
}

func TestMainValidatesSessionExecutorConfigBeforeConstructingRouter(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for startup ordering regression test")

	body := string(src)
	preflight := strings.Index(body, "validateSessionExecutorStartupConfig(cfg)")
	construct := strings.Index(body, "api.NewRouter(")
	require.NotEqual(t, -1, preflight, "startup should validate durable session executor config")
	require.NotEqual(t, -1, construct, "startup should still construct the API router")
	require.Less(t, preflight, construct, "production executor config should fail before expensive server/router wiring")
}

func TestMainPassesConfiguredNodeIDToWorkers(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err, "main.go should parse for worker node ID wiring regression test")

	var workerStart *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if ok && fn.Name == "startProcessWorkers" {
			workerStart = call
			return false
		}
		return true
	})

	require.NotNil(t, workerStart, "startup should call startProcessWorkers")
	require.GreaterOrEqual(t, len(workerStart.Args), 4, "startProcessWorkers call should include the node ID argument")

	nodeArg, ok := workerStart.Args[3].(*ast.SelectorExpr)
	require.True(t, ok, "worker node ID argument should be a selector expression")
	root, ok := nodeArg.X.(*ast.Ident)
	require.True(t, ok, "worker node ID argument should be rooted at cfg")
	require.Equal(t, "cfg", root.Name, "workers should use the configured node ID root")
	require.Equal(t, "NodeID", nodeArg.Sel.Name, "workers should use cfg.NodeID rather than the Docker hostname")
}

func TestMainWiresStaticEgressIntoDurablePreviewRunner(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err, "main.go should parse for durable preview runner wiring regression test")

	var cfgLiteral *ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			selector, ok := lhs.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "PreviewStarter" || i >= len(assign.Rhs) {
				continue
			}
			call, ok := assign.Rhs[i].(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				continue
			}
			fun, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || fun.Sel.Name != "NewStartRunner" {
				continue
			}
			lit, ok := call.Args[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			cfgLiteral = lit
			return false
		}
		return true
	})

	require.NotNil(t, cfgLiteral, "startup should construct a durable preview StartRunnerConfig")
	fields := map[string]bool{}
	for _, elt := range cfgLiteral.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		fields[key.Name] = true
	}
	require.True(t, fields["Orgs"], "durable preview runner should load org network settings")
	require.True(t, fields["StaticEgress"], "durable preview runner should fail closed and hydrate on the static egress bridge when org settings require it")
}

func TestMainAdvertisesPreviewAfterHTTPListen(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for preview readiness ordering test")

	body := string(src)
	listen := strings.Index(body, "net.Listen(\"tcp\"")
	ready := strings.Index(body, "previewRoutingReady.Store(true)")
	serve := strings.Index(body, "srv.Serve(")
	require.NotEqual(t, -1, listen, "startup should bind the HTTP listener explicitly")
	require.NotEqual(t, -1, ready, "startup should mark preview routing ready explicitly")
	require.NotEqual(t, -1, serve, "startup should serve the already-bound listener")
	require.Less(t, listen, ready, "preview routing must not be advertised until the HTTP listener is bound")
	require.Less(t, ready, serve, "preview routing should be advertised before serving blocks")
}

func TestMainRunsControlPlaneHealthAlertsOutsideWorkerMode(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for control-plane alert wiring test")

	body := string(src)
	alertGuard := strings.Index(body, "if cfg.Mode != \"worker\"")
	alertStart := strings.Index(body, "worker.RunControlPlaneHealthAlerts(")
	workerBlock := strings.Index(body, "if cfg.Mode == \"all\" || cfg.Mode == \"worker\"")
	require.NotEqual(t, -1, alertGuard, "startup should gate control-plane alerts away from worker-only mode")
	require.NotEqual(t, -1, alertStart, "startup should run queue and worker-heartbeat alerts from an API-capable process")
	require.NotEqual(t, -1, workerBlock, "startup should still have an explicit worker-mode block")
	require.Less(t, alertGuard, alertStart, "control-plane alert guard should wrap the sampler start")
	require.Less(t, alertStart, workerBlock, "control-plane alerts must start outside worker-only startup so they still run when workers are down")
}

func TestGracefulShutdownUsesShortNodeDrainContext(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for shutdown ordering regression test")

	body := string(src)
	require.Contains(t, body, "nodeDrainMarkTimeout      = 5 * time.Second",
		"node drain DB marking should use a short bounded timeout")
	require.Contains(t, body, "httpDrainPropagationDelay = 7 * time.Second",
		"HTTP drain propagation should cover Caddy's 2s health interval, 2s timeout, and DNS refresh slack")
	require.Contains(t, body, "httpShutdownTimeout       = 100 * time.Second",
		"HTTP shutdown should leave headroom inside docker-compose.app.yml stop_grace_period after drain propagation")
	require.Contains(t, body, "nodeDrainCtx, nodeDrainCancel := context.WithTimeout(context.Background(), nodeDrainMarkTimeout)",
		"node drain DB marking should not consume the worker job drain context")
	require.Contains(t, body, "nodeManager.RequestDrain(nodeDrainCtx, time.Now())",
		"node drain DB marking should use the short node-drain context")
	require.Contains(t, body, "drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.WorkerDrainTimeout)",
		"worker jobs should keep the full configured drain timeout")
	require.Contains(t, body, "shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)",
		"HTTP shutdown should use the bounded timeout constant")
	require.Contains(t, body, "waitForDBOwnedJobsToDrain(drainCtx, jobStore, cfg.NodeID, logger)",
		"worker shutdown should verify DB-owned jobs for the draining node before process exit")
	require.Contains(t, body, "jobStore.CountRunningOwnedByNode(ctx, nodeID)",
		"DB-owned drain verification should query the durable job owner state")

	nodeDrain := strings.Index(body, "nodeManager.RequestDrain(nodeDrainCtx, time.Now())")
	httpDrainSignal := strings.Index(body, "close(shutdownCh)")
	httpDrainDelay := strings.Index(body, "time.Sleep(httpDrainPropagationDelay)")
	workerDrain := strings.Index(body, "drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.WorkerDrainTimeout)")
	activeJobLoop := strings.Index(body, "activeJobs := 0")
	dbOwnedDrain := strings.Index(body, "waitForDBOwnedJobsToDrain(drainCtx, jobStore, cfg.NodeID, logger)")
	require.NotEqual(t, -1, nodeDrain, "shutdown should mark the node draining")
	require.NotEqual(t, -1, httpDrainSignal, "shutdown should mark HTTP health as draining")
	require.NotEqual(t, -1, httpDrainDelay, "shutdown should wait for proxy health propagation")
	require.NotEqual(t, -1, workerDrain, "shutdown should create the worker drain context")
	require.NotEqual(t, -1, activeJobLoop, "shutdown should wait for active jobs")
	require.NotEqual(t, -1, dbOwnedDrain, "shutdown should verify DB-owned jobs after active jobs drain")
	require.Less(t, nodeDrain, httpDrainSignal, "node drain DB marking should happen before HTTP health drain begins")
	require.Less(t, httpDrainSignal, httpDrainDelay, "HTTP health should be marked draining before waiting for proxy propagation")
	require.Less(t, httpDrainDelay, workerDrain, "proxy propagation should finish before the worker drain budget starts")
	require.Less(t, workerDrain, activeJobLoop, "the worker drain budget should be reserved for the active-job wait")
	require.Less(t, activeJobLoop, dbOwnedDrain, "DB-owned drain verification should run after in-process active jobs reach zero")
}

func TestDeployWorkflowWaitsForWorkerRolloverTerminalStatus(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("../../.github/workflows/deploy.yml")
	require.NoError(t, err, "deploy workflow should be readable for worker rollover regression test")

	body := string(src)
	require.Contains(t, body, `VERIFY_TIMEOUT_SECONDS: "360"`,
		"worker rollover verification should fail quickly because routine blue/green deploys do not wait for the old drain")
	require.Contains(t, body, `POLL_INTERVAL_SECONDS: "10"`,
		"worker rollover verification should poll often enough for a short deploy budget")
	require.Contains(t, body, "verify_worker()",
		"worker rollover verification should put per-host polling in a reusable function")
	require.Contains(t, body, "verify_worker \"$host\" &",
		"worker rollover verification should poll worker hosts in parallel")
	require.Contains(t, body, "for pid in \"${pids[@]}\"; do",
		"worker rollover verification should wait for all parallel host checks")
	require.Contains(t, body, `outcome="timeout"`,
		"worker rollover timeout should be reported as timeout, not successful in_progress")
	require.Contains(t, body, "overall_rc=1",
		"worker rollover timeout/failure should fail the deploy job so later deploys do not race host-side rollover")
	require.NotContains(t, body, `outcome="in_progress"`,
		"deploy workflow should not pass while detached worker rollover is still running")
}

// fakeDrainer is a postPRSnapshotDrainer that blocks WaitForPostPRSnapshotUploads
// on a release channel. Tests use it to drive the success-vs-timeout branches
// of drainPostPRUploads deterministically.
type fakeDrainer struct {
	release chan struct{}
	calls   atomic.Int32
}

func (f *fakeDrainer) WaitForPostPRSnapshotUploads() {
	f.calls.Add(1)
	<-f.release
}

func TestDrainPostPRUploads_NilDrainerNoop(t *testing.T) {
	t.Parallel()

	// Nil drainer must return immediately rather than panicking — this is
	// the api-only-mode path where workerServices.PR is never built.
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPostPRUploads(context.Background(), nil, zerolog.Nop())
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("drainPostPRUploads with nil drainer should return immediately")
	}
}

func TestDrainPostPRUploads_CompletesBeforeDeadline(t *testing.T) {
	t.Parallel()

	drainer := &fakeDrainer{release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPostPRUploads(ctx, drainer, zerolog.Nop())
	}()

	// Release the simulated upload — the drain should observe completion
	// and return without hitting drainCtx.Done().
	close(drainer.release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("drainPostPRUploads should return when uploads complete")
	}
	if got := drainer.calls.Load(); got != 1 {
		t.Errorf("WaitForPostPRSnapshotUploads should be called exactly once, got %d", got)
	}
}

func TestDrainPostPRUploads_CtxDoneTakesPrecedence(t *testing.T) {
	t.Parallel()

	// Drainer never releases — exercises the drainCtx.Done() branch. The
	// goroutine that called Wait remains parked; production accepts this
	// as the goroutine will be killed when the process exits and the
	// reaper will clear the stranded pending_snapshot_key row.
	drainer := &fakeDrainer{release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPostPRUploads(ctx, drainer, zerolog.Nop())
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("drainPostPRUploads should return once drainCtx expires")
	}
	if got := drainer.calls.Load(); got != 1 {
		t.Errorf("WaitForPostPRSnapshotUploads should be called exactly once, got %d", got)
	}
}
