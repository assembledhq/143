package preview

import (
	"context"
	"io"
	"net"

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
	// The returned PreviewHandle contains connection details for DialPreview.
	StartPreview(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig) (*PreviewHandle, error)

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
	Host        string
	Port        int
	Credential  InfraCredential
}
