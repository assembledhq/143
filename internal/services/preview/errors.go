package preview

import (
	"errors"
	"strconv"
)

// Provider-side failure sentinels. The HTTP handler classifies preview-launch
// errors via errors.Is so the frontend gets a specific code and message
// instead of a generic "failed to start preview". Wrap with %w when adding
// context (e.g. fmt.Errorf("%w: pull postgres:17-alpine: %v", ErrInfraImageUnavailable, err)).
var (
	// ErrPreviewCapacity is returned when a preview cannot start because a
	// preview or sandbox concurrency cap is full. Wrap with %w so callers can
	// detect transient capacity pressure with errors.Is.
	ErrPreviewCapacity = errors.New("preview capacity reached")

	// ErrInfraImageUnavailable means a preview infrastructure container could
	// not be created because the requested image is not present locally and
	// the on-demand pull failed (registry unreachable, image renamed/removed,
	// rate limit, no network egress, etc.).
	ErrInfraImageUnavailable = errors.New("preview infrastructure image unavailable")

	// ErrInfraStartFailed means Docker accepted the create call but the
	// container failed to start, or container creation itself failed for a
	// reason other than missing image (resource limits, label conflict, etc.).
	ErrInfraStartFailed = errors.New("preview infrastructure container failed to start")

	// ErrInfraUnhealthy means the container started but its health check
	// (pg_isready, redis-cli ping, etc.) did not pass within the timeout.
	ErrInfraUnhealthy = errors.New("preview infrastructure container failed health check")

	// ErrInitScriptFailed means a user-supplied init script (e.g. seed SQL)
	// returned a non-zero exit code or could not be read from the workspace.
	ErrInitScriptFailed = errors.New("preview init script failed")

	// ErrInstallFailed means a preview.install command returned non-zero,
	// timed out, or could not prepare its lockfile-based cache state.
	ErrInstallFailed = errors.New("preview install failed")

	// ErrServiceNotReady means an application service was launched but its
	// readiness probe never passed within the configured timeout. The user's
	// preview command likely crashed at boot or never bound to its declared
	// port.
	ErrServiceNotReady = errors.New("preview service readiness probe failed")

	// ErrServiceBuildFailed means a service's build command (preview build
	// phase) exited non-zero or timed out before any service was started.
	ErrServiceBuildFailed = errors.New("preview service build failed")
)

type CapacityScope string

const (
	CapacityScopeUser   CapacityScope = "user"
	CapacityScopeOrg    CapacityScope = "org"
	CapacityScopeWorker CapacityScope = "worker"
)

type CapacityError struct {
	Scope  CapacityScope
	Active int
	Limit  int
}

func (e *CapacityError) Error() string {
	return e.UserMessage()
}

func (e *CapacityError) Unwrap() error {
	return ErrPreviewCapacity
}

func (e *CapacityError) UserMessage() string {
	if e == nil {
		return PreviewCapacityMessage
	}
	switch e.Scope {
	case CapacityScopeUser:
		return "You have reached your per-user preview limit: " + pluralizePreviewCount(e.Active) + " out of " + strconv.Itoa(e.Limit) + " allowed. Stop one of your previews or ask an admin to raise the per-user preview limit in General settings."
	case CapacityScopeOrg:
		return "Your organization has reached its active preview limit. Stop another active preview or try again shortly."
	case CapacityScopeWorker:
		return "This preview worker is at capacity. Try again shortly."
	default:
		return PreviewCapacityMessage
	}
}

func pluralizePreviewCount(count int) string {
	if count == 1 {
		return "1 active preview"
	}
	return strconv.Itoa(count) + " active previews"
}
