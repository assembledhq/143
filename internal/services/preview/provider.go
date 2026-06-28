package preview

import (
	"context"
	"io"
	"net"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// PreviewCapableProvider abstracts the sandbox-side preview lifecycle.
//
// The API/gateway layer interacts only through this interface. It does not
// know whether the preview is backed by a local Docker container, a VM
// tunnel, or another transport.
type PreviewCapableProvider interface {
	// StartPreview handles the full preview lifecycle inside a sandbox:
	//   1. Provision infrastructure containers (if any) and wait for health
	//   2. Generate ephemeral credentials and inject into services
	//   3. Run init scripts against infrastructure
	//   4. Start application services in dependency order
	//   5. Wait for all readiness probes to pass
	//
	// StartPreviewOptions carries launch metadata and platform env. ExtraEnv
	// is merged into every service's environment after the user's
	// declared env and any infrastructure-injected credentials, so it can
	// carry platform-level values (e.g. PREVIEW_ORIGIN) that must always be
	// available and should win over user overrides. It is also injected into
	// service build commands so build steps can detect the preview runtime;
	// unlike credentials and secret bundles, ExtraEnv carries only non-secret
	// platform context, so exposing it at build time leaks nothing.
	//
	// observer receives per-service Ready/Failed transitions as they happen,
	// so callers (typically the manager) can persist per-service state to the
	// DB without waiting for StartPreview to fully return. observer may be
	// nil; provider implementations must tolerate it.
	//
	// The returned PreviewHandle contains connection details for DialPreview.
	StartPreview(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, opts StartPreviewOptions, observer ServiceObserver) (*PreviewHandle, error)

	// StopPreview gracefully stops all preview processes and tears down
	// infrastructure containers. Safe to call multiple times.
	StopPreview(ctx context.Context, handle string) error

	// DialPreview opens a transport connection to the primary service's port.
	// The returned PreviewStream carries HTTP and WebSocket traffic.
	// Support services are not directly exposed — they are only reachable
	// from other processes inside the sandbox via localhost.
	DialPreview(ctx context.Context, handle string) (PreviewStream, error)

	// PreviewStatus returns the current status of all services and
	// infrastructure in a preview.
	PreviewStatus(ctx context.Context, handle string) (*PreviewStatusSnapshot, error)
}

type PreviewCachePrewarmProvider interface {
	PrewarmPreviewInstallCaches(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, opts StartPreviewOptions, observer ServiceObserver) error
}

// PreviewSoftRestartProvider restarts application services in an existing
// preview without reprovisioning infrastructure or rerunning install/build
// phases. Providers that cannot do this should omit the interface; callers
// fall back to a full recycle.
type PreviewSoftRestartProvider interface {
	SoftRestartPreview(ctx context.Context, handle string, observer ServiceObserver) (*PreviewHandle, error)
}

type StartPreviewOptions struct {
	OrgID        uuid.UUID
	RepositoryID uuid.UUID
	SessionID    uuid.UUID
	ConfigDigest string
	ExtraEnv     map[string]string
}

// PreviewHandle is returned by StartPreview and contains the information
// needed to connect to and manage the preview.
type PreviewHandle struct {
	// Handle is an opaque identifier for the preview within this provider.
	Handle string

	// PrimaryPort is the port the primary service is listening on.
	PrimaryPort int

	// InfraCredentials maps infra_name → generated credentials.
	// These are ephemeral and exist only for the lifetime of the preview.
	InfraCredentials map[string]InfraCredential

	// PartiallyReady is true when progressive preview is enabled and the
	// primary service is ready but support services are still starting.
	PartiallyReady bool
}

// InfraCredential holds the auto-generated credentials for a platform
// infrastructure service (PostgreSQL, Redis, etc.).
type InfraCredential struct {
	Host     string
	Port     int
	Username string
	Password string
	Database string // e.g. "preview_db" for PostgreSQL, empty for Redis
}

// PreviewStream is an abstract transport to the primary service.
// It carries HTTP and WebSocket traffic. The implementation may be a direct
// TCP connection (Docker), a provider tunnel (E2B), or a worker-to-worker
// proxy (multi-node).
type PreviewStream interface {
	net.Conn

	// Close terminates the stream. Must be safe to call multiple times.
	Close() error
}

// PreviewStreamDialer is a function that opens a PreviewStream.
// Used by the gateway for connection pooling.
type PreviewStreamDialer func(ctx context.Context) (PreviewStream, error)

// PreviewStatusSnapshot is a point-in-time view of all components in a preview.
type PreviewStatusSnapshot struct {
	Services       []ServiceSnapshot
	Infrastructure []InfraSnapshot
}

// ServiceSnapshot is the current state of a single application service.
type ServiceSnapshot struct {
	Name   string
	Status models.PreviewServiceStatus
	PID    int
	Port   int
	Error  string
}

// ServiceObserver receives notifications when a preview service flips state
// during StartPreview. It is invoked from both the calling goroutine and
// background goroutines spawned by the provider, so implementations must be
// safe for concurrent use and should return promptly. A nil observer is
// allowed and is treated as a no-op.
type ServiceObserver interface {
	// OnServiceOutput is invoked for each stdout/stderr line emitted while a
	// service is still starting. Implementations should keep this lightweight:
	// providers call it from the process output path.
	OnServiceOutput(name, line string)
	// OnInstallFailed is invoked when preview.install fails before any
	// application service is started. tail holds the captured install output.
	OnInstallFailed(errMsg string, tail []string)
	// OnServiceReady is invoked once a service has passed its readiness probe.
	OnServiceReady(name string, port, pid int)
	// OnServiceFailed is invoked when a service has crashed at boot, exited
	// non-zero before becoming ready, or otherwise failed to come up. tail
	// holds up to the last few hundred lines of stdout/stderr captured from
	// the service process; it is best-effort and may be empty.
	OnServiceFailed(name, errMsg string, tail []string)
}

type CacheObserver interface {
	OnDependencyCacheRestore(status string, cacheKey string, sizeBytes int64, err error)
	OnDependencyCacheSave(status string, cacheKey string, sizeBytes int64, err error)
	OnPackageManagerCacheRestore(status string, cacheKey string, sizeBytes int64, err error)
	OnPackageManagerCacheSave(status string, cacheKey string, sizeBytes int64, err error)
	OnBuildCacheRestore(status string, cacheKey string, sizeBytes int64, err error)
	OnBuildCacheSave(status string, cacheKey string, sizeBytes int64, err error)
}

// InfraSnapshot is the current state of a platform infrastructure container.
type InfraSnapshot struct {
	Name        string
	Template    string
	ContainerID string
	Status      models.PreviewInfraStatus
	Host        string
	Port        int
	Error       string
}

// =============================================================================
// Process management types
// =============================================================================

// ProcessHandle represents a running application service process inside the sandbox.
type ProcessHandle struct {
	ServiceName string
	PID         int
	Port        int
	Cmd         string
	Stdout      io.ReadCloser
	Stderr      io.ReadCloser
}

// InfraHandle represents a running infrastructure container.
type InfraHandle struct {
	InfraName   string
	Template    string
	ContainerID string
	Credential  InfraCredential
}
