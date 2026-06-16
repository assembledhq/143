package sandboxauth

import "github.com/google/uuid"

const (
	BrokerActionAcquire = "sandbox_auth_acquire"
	BrokerActionRelease = "sandbox_auth_release"
)

type BrokerAcquireRequest struct {
	OrgID     uuid.UUID `json:"org_id"`
	SessionID uuid.UUID `json:"session_id"`
	HolderID  uuid.UUID `json:"holder_id"`
}

type BrokerAcquireResponse struct {
	SocketPath string `json:"socket_path"`
}

type BrokerReleaseRequest struct {
	OrgID     uuid.UUID `json:"org_id"`
	SessionID uuid.UUID `json:"session_id"`
	HolderID  uuid.UUID `json:"holder_id"`
}

type BrokerReleaseResponse struct {
	Released bool `json:"released"`
}
