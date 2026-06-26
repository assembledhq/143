package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type SessionExecutorStatus string

const (
	SessionExecutorStatusStarting  SessionExecutorStatus = "starting"
	SessionExecutorStatusRunning   SessionExecutorStatus = "running"
	SessionExecutorStatusDraining  SessionExecutorStatus = "draining"
	SessionExecutorStatusRequeued  SessionExecutorStatus = "requeued"
	SessionExecutorStatusCompleted SessionExecutorStatus = "completed"
	SessionExecutorStatusFailed    SessionExecutorStatus = "failed"
	SessionExecutorStatusLost      SessionExecutorStatus = "lost"
)

func (s SessionExecutorStatus) Validate() error {
	switch s {
	case SessionExecutorStatusStarting,
		SessionExecutorStatusRunning,
		SessionExecutorStatusDraining,
		SessionExecutorStatusRequeued,
		SessionExecutorStatusCompleted,
		SessionExecutorStatusFailed,
		SessionExecutorStatusLost:
		return nil
	default:
		return fmt.Errorf("invalid SessionExecutorStatus: %q", s)
	}
}

type JobOwnerKind string

const (
	JobOwnerKindWorker          JobOwnerKind = "worker"
	JobOwnerKindSessionExecutor JobOwnerKind = "session_executor"
)

func (k JobOwnerKind) Validate() error {
	switch k {
	case JobOwnerKindWorker, JobOwnerKindSessionExecutor:
		return nil
	default:
		return fmt.Errorf("invalid JobOwnerKind: %q", k)
	}
}

type SessionExecutor struct {
	ID                uuid.UUID             `db:"id" json:"id"`
	OrgID             uuid.UUID             `db:"org_id" json:"org_id"`
	SessionID         uuid.UUID             `db:"session_id" json:"session_id"`
	ThreadID          *uuid.UUID            `db:"thread_id" json:"thread_id,omitempty"`
	JobID             uuid.UUID             `db:"job_id" json:"job_id"`
	JobType           string                `db:"job_type" json:"job_type"`
	HostNodeID        string                `db:"host_node_id" json:"host_node_id"`
	OwnerID           string                `db:"owner_id" json:"owner_id"`
	LockToken         uuid.UUID             `db:"lock_token" json:"lock_token"`
	Status            SessionExecutorStatus `db:"status" json:"status"`
	ContainerID       *string               `db:"container_id" json:"container_id,omitempty"`
	Image             string                `db:"image" json:"image"`
	BuildSHA          string                `db:"build_sha" json:"build_sha"`
	HeartbeatAt       *time.Time            `db:"heartbeat_at" json:"heartbeat_at,omitempty"`
	LeaseExpiresAt    *time.Time            `db:"lease_expires_at" json:"lease_expires_at,omitempty"`
	RuntimeDeadlineAt *time.Time            `db:"runtime_deadline_at" json:"runtime_deadline_at,omitempty"`
	DrainIntent       DrainIntent           `db:"drain_intent" json:"drain_intent"`
	DrainRequestedAt  *time.Time            `db:"drain_requested_at" json:"drain_requested_at,omitempty"`
	DrainDeadlineAt   *time.Time            `db:"drain_deadline_at" json:"drain_deadline_at,omitempty"`
	StartedAt         time.Time             `db:"started_at" json:"started_at"`
	CompletedAt       *time.Time            `db:"completed_at" json:"completed_at,omitempty"`
	ExitCode          *int                  `db:"exit_code" json:"exit_code,omitempty"`
	LastError         *string               `db:"last_error" json:"last_error,omitempty"`
	CreatedAt         time.Time             `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time             `db:"updated_at" json:"updated_at"`
}

type CreateSessionExecutorParams struct {
	SessionID         uuid.UUID
	ThreadID          *uuid.UUID
	JobID             uuid.UUID
	JobType           string
	HostNodeID        string
	OwnerID           string
	LockToken         uuid.UUID
	Image             string
	BuildSHA          string
	RuntimeDeadlineAt *time.Time
}
