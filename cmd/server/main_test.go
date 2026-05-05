package main

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

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

	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", func() bool { return true })

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

	provider := buildWorkerMetadataProvider(nil, false, "", func() bool { return true })

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
	provider := buildWorkerMetadataProvider(nil, true, "http://worker-1:8080", func() bool { return ready })

	metadata := provider()
	require.NotContains(t, metadata, "preview_capable", "preview_capable should be hidden until the HTTP listener is bound")
	require.Equal(t, "http://worker-1:8080", metadata["preview_internal_base_url"], "preview internal URL should remain available in metadata")

	ready = true
	metadata = provider()
	require.Equal(t, true, metadata["preview_capable"], "preview_capable should be advertised once routing is ready")
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
	require.Contains(t, body, "const nodeDrainMarkTimeout = 5 * time.Second",
		"node drain DB marking should use a short bounded timeout")
	require.Contains(t, body, "nodeDrainCtx, nodeDrainCancel := context.WithTimeout(context.Background(), nodeDrainMarkTimeout)",
		"node drain DB marking should not consume the worker job drain context")
	require.Contains(t, body, "nodeManager.RequestDrain(nodeDrainCtx, time.Now())",
		"node drain DB marking should use the short node-drain context")
	require.Contains(t, body, "drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.WorkerDrainTimeout)",
		"worker jobs should keep the full configured drain timeout")

	nodeDrain := strings.Index(body, "nodeManager.RequestDrain(nodeDrainCtx, time.Now())")
	workerDrain := strings.Index(body, "drainCtx, drainCancel := context.WithTimeout(context.Background(), cfg.WorkerDrainTimeout)")
	activeJobLoop := strings.Index(body, "activeJobs := 0")
	require.NotEqual(t, -1, nodeDrain, "shutdown should mark the node draining")
	require.NotEqual(t, -1, workerDrain, "shutdown should create the worker drain context")
	require.NotEqual(t, -1, activeJobLoop, "shutdown should wait for active jobs")
	require.Less(t, nodeDrain, workerDrain, "node drain DB marking should happen before the worker drain budget starts")
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
