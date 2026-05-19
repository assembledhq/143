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

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

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
	}{
		{
			name:                   "preview-capable worker advertises both fields",
			previewCapable:         true,
			previewInternalBaseURL: "http://worker-1:8080",
			wantPreviewCapable:     true,
			wantInternalBaseURL:    "http://worker-1:8080",
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

			metadata := buildBaseMetadata(tt.previewCapable, tt.previewInternalBaseURL)

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
		})
	}
}

// TestBuildWorkerMetadataProvider_PreservesPreviewFields guards against the
// regression where SetMetadataProvider in startProcessWorkers replaced the
// initial provider without re-emitting preview-routing fields, causing the
// next heartbeat to wipe preview_capable and break Start Preview routing.
func TestBuildWorkerMetadataProvider_PreservesPreviewFields(t *testing.T) {
	t.Parallel()

	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", func() bool { return true }, nil)

	metadata := provider()

	if got, ok := metadata["preview_capable"]; !ok || got != true {
		t.Errorf("preview_capable must persist across worker startup, got %v (present=%v)", got, ok)
	}
	if got, ok := metadata["preview_internal_base_url"]; !ok || got != "http://worker-1:8080" {
		t.Errorf("preview_internal_base_url must persist across worker startup, got %v (present=%v)", got, ok)
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

	provider := buildWorkerMetadataProvider(nil, false, "", func() bool { return true }, nil)

	metadata := provider()

	if _, ok := metadata["preview_capable"]; ok {
		t.Errorf("preview_capable should be omitted when worker is not preview-capable")
	}
	if _, ok := metadata["preview_internal_base_url"]; ok {
		t.Errorf("preview_internal_base_url should be omitted when not configured")
	}
}

func TestBuildWorkerMetadataProvider_DelaysPreviewCapabilityUntilReady(t *testing.T) {
	t.Parallel()

	ready := false
	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", func() bool { return ready }, nil)

	metadata := provider()
	require.NotContains(t, metadata, "preview_capable", "preview_capable should be hidden until the HTTP listener is bound")
	require.Equal(t, "http://worker-1:8080", metadata["preview_internal_base_url"], "preview internal URL should remain available in metadata")

	ready = true
	metadata = provider()
	require.Equal(t, true, metadata["preview_capable"], "preview_capable should be advertised once routing is ready")
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

func TestBuildWorkerMetadataProvider_IncludesSandboxCapacity(t *testing.T) {
	t.Parallel()

	gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   mainTestLiveSandboxCounter{count: 2},
		MaxActive: 4,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", func() bool { return true }, gate)

	metadata := provider()

	require.Equal(t, 2, metadata["live_sandbox_count"], "worker metadata should expose local live sandbox count")
	require.Equal(t, 0, metadata["reserved_sandbox_count"], "worker metadata should expose in-flight sandbox reservations")
	require.Equal(t, 4, metadata["max_active_sandboxes"], "worker metadata should expose the per-machine sandbox cap")
}

// TestMainStartupRunsRehydrateBeforeWorkers guards the sandbox-auth socket
// sweep invariant: process workers must not be able to call Listen for a new
// job while the boot-time rehydrate/sweep pass is still deciding which
// session directories are stale.
func TestMainStartupRunsRehydrateBeforeWorkers(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("main.go")
	require.NoError(t, err, "main.go should be readable for startup ordering regression test")

	body := string(src)
	startWorkers := strings.Index(body, "\n\t\tprocessWorkers = startProcessWorkers(")
	rehydrate := strings.Index(body, "orch.RehydrateSandboxAuthListeners(")
	require.NotEqual(t, -1, startWorkers, "startup should still start process workers")
	require.NotEqual(t, -1, rehydrate, "startup should still run sandbox auth rehydrate")
	require.Less(t, rehydrate, startWorkers, "sandbox auth rehydrate/sweep must run before process workers can claim jobs")
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

	nodeDrain := strings.Index(body, "nodeManager.RequestDrain(nodeDrainCtx, time.Now())")
	httpDrainSignal := strings.Index(body, "close(shutdownCh)")
	httpDrainDelay := strings.Index(body, "time.Sleep(httpDrainPropagationDelay)")
	workerDrain := strings.Index(body, "drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.WorkerDrainTimeout)")
	activeJobLoop := strings.Index(body, "activeJobs := 0")
	require.NotEqual(t, -1, nodeDrain, "shutdown should mark the node draining")
	require.NotEqual(t, -1, httpDrainSignal, "shutdown should mark HTTP health as draining")
	require.NotEqual(t, -1, httpDrainDelay, "shutdown should wait for proxy health propagation")
	require.NotEqual(t, -1, workerDrain, "shutdown should create the worker drain context")
	require.NotEqual(t, -1, activeJobLoop, "shutdown should wait for active jobs")
	require.Less(t, nodeDrain, httpDrainSignal, "node drain DB marking should happen before HTTP health drain begins")
	require.Less(t, httpDrainSignal, httpDrainDelay, "HTTP health should be marked draining before waiting for proxy propagation")
	require.Less(t, httpDrainDelay, workerDrain, "proxy propagation should finish before the worker drain budget starts")
	require.Less(t, workerDrain, activeJobLoop, "the worker drain budget should be reserved for the active-job wait")
}

func TestDeployWorkflowWaitsForWorkerRolloverTerminalStatus(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("../../.github/workflows/deploy.yml")
	require.NoError(t, err, "deploy workflow should be readable for worker rollover regression test")

	body := string(src)
	require.Contains(t, body, `VERIFY_TIMEOUT_SECONDS: "4200"`,
		"worker rollover verification should cover the full 45m drain plus recreate/healthcheck with margin")
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
