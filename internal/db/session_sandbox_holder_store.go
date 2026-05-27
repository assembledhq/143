package db

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type SessionSandboxHolderStore struct {
	db DBTX
}

func NewSessionSandboxHolderStore(db DBTX) *SessionSandboxHolderStore {
	return &SessionSandboxHolderStore{db: db}
}

type CreateSessionSandboxHolderParams struct {
	SessionID     uuid.UUID
	ContainerID   string
	HolderKind    models.SessionSandboxHolderKind
	HolderID      uuid.UUID
	OwnerNodeID   string
	LeaseToken    uuid.UUID
	LeaseDuration time.Duration
}

const sessionSandboxHolderColumns = `id, org_id, session_id, container_id, holder_kind,
	holder_id, owner_node_id, lease_token, status, heartbeat_at, expires_at,
	created_at, released_at, updated_at`

func (s *SessionSandboxHolderStore) CreateActive(ctx context.Context, orgID uuid.UUID, params CreateSessionSandboxHolderParams) (models.SessionSandboxHolder, error) {
	if err := params.HolderKind.Validate(); err != nil {
		return models.SessionSandboxHolder{}, err
	}
	leaseSeconds := int(params.LeaseDuration.Seconds())
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}

	rows, err := s.db.Query(ctx, `
		INSERT INTO session_sandbox_holders (
			org_id, session_id, container_id, holder_kind, holder_id,
			owner_node_id, lease_token, status, heartbeat_at, expires_at
		)
		VALUES (
			@org_id, @session_id, @container_id, @holder_kind, @holder_id,
			@owner_node_id, @lease_token, 'active', now(), now() + (@lease_seconds * interval '1 second')
		)
		ON CONFLICT (org_id, session_id, holder_kind, holder_id)
			WHERE status IN ('active', 'draining')
		DO UPDATE
		SET container_id = EXCLUDED.container_id,
			owner_node_id = EXCLUDED.owner_node_id,
			lease_token = EXCLUDED.lease_token,
			status = 'active',
			heartbeat_at = now(),
			expires_at = EXCLUDED.expires_at,
			updated_at = now()
		RETURNING `+sessionSandboxHolderColumns, pgx.NamedArgs{
		"org_id":        orgID,
		"session_id":    params.SessionID,
		"container_id":  params.ContainerID,
		"holder_kind":   params.HolderKind,
		"holder_id":     params.HolderID,
		"owner_node_id": params.OwnerNodeID,
		"lease_token":   params.LeaseToken,
		"lease_seconds": leaseSeconds,
	})
	if err != nil {
		return models.SessionSandboxHolder{}, fmt.Errorf("create session sandbox holder: %w", err)
	}
	holder, err := pgx.CollectOneRow(rows, scanSessionSandboxHolderRow)
	if err != nil {
		return models.SessionSandboxHolder{}, fmt.Errorf("create session sandbox holder: %w", err)
	}
	return holder, nil
}

func scanSessionSandboxHolderRow(row pgx.CollectableRow) (models.SessionSandboxHolder, error) {
	var holder models.SessionSandboxHolder
	var holderKind string
	var status string
	var releasedAt pgtype.Timestamptz
	if err := row.Scan(
		&holder.ID,
		&holder.OrgID,
		&holder.SessionID,
		&holder.ContainerID,
		&holderKind,
		&holder.HolderID,
		&holder.OwnerNodeID,
		&holder.LeaseToken,
		&status,
		&holder.HeartbeatAt,
		&holder.ExpiresAt,
		&holder.CreatedAt,
		&releasedAt,
		&holder.UpdatedAt,
	); err != nil {
		return models.SessionSandboxHolder{}, err
	}
	holder.HolderKind = models.SessionSandboxHolderKind(holderKind)
	holder.Status = models.SessionSandboxHolderStatus(status)
	if releasedAt.Valid {
		t := releasedAt.Time.UTC()
		holder.ReleasedAt = &t
	}
	holder.HeartbeatAt = holder.HeartbeatAt.In(time.UTC)
	holder.ExpiresAt = holder.ExpiresAt.In(time.UTC)
	holder.CreatedAt = holder.CreatedAt.In(time.UTC)
	holder.UpdatedAt = holder.UpdatedAt.In(time.UTC)
	return holder, nil
}

func (s *SessionSandboxHolderStore) ReleaseWithLease(ctx context.Context, orgID, sessionID uuid.UUID, kind models.SessionSandboxHolderKind, holderID, leaseToken uuid.UUID) (bool, error) {
	if err := kind.Validate(); err != nil {
		return false, err
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE session_sandbox_holders
		SET status = 'released',
			released_at = now(),
			updated_at = now()
		WHERE org_id = $1
		  AND session_id = $2
		  AND holder_kind = $3
		  AND holder_id = $4
		  AND lease_token = $5
		  AND status IN ('active', 'draining')`, orgID, sessionID, kind, holderID, leaseToken)
	if err != nil {
		return false, fmt.Errorf("release session sandbox holder: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *SessionSandboxHolderStore) HeartbeatWithLease(ctx context.Context, orgID, sessionID uuid.UUID, kind models.SessionSandboxHolderKind, holderID, leaseToken uuid.UUID, leaseDuration time.Duration) (bool, error) {
	if err := kind.Validate(); err != nil {
		return false, err
	}
	leaseSeconds := int(leaseDuration.Seconds())
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE session_sandbox_holders
		SET heartbeat_at = now(),
			expires_at = now() + ($6 * interval '1 second'),
			updated_at = now()
		WHERE org_id = $1
		  AND session_id = $2
		  AND holder_kind = $3
		  AND holder_id = $4
		  AND lease_token = $5
		  AND status IN ('active', 'draining')`, orgID, sessionID, kind, holderID, leaseToken, leaseSeconds)
	if err != nil {
		return false, fmt.Errorf("heartbeat session sandbox holder: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *SessionSandboxHolderStore) CountActiveBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM session_sandbox_holders
		WHERE org_id = $1
		  AND session_id = $2
		  AND status IN ('active', 'draining')
		  AND expires_at > now()`, orgID, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active session sandbox holders: %w", err)
	}
	return count, nil
}

func (s *SessionSandboxHolderStore) CountActiveThreadRuntimesBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM session_sandbox_holders
		WHERE org_id = $1
		  AND session_id = $2
		  AND holder_kind = 'thread_runtime'
		  AND status IN ('active', 'draining')
		  AND expires_at > now()`, orgID, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active thread runtime holders: %w", err)
	}
	return count, nil
}

func (s *SessionSandboxHolderStore) CountActiveThreadRuntimesBySessionExcluding(ctx context.Context, orgID, sessionID, excludedHolderID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM session_sandbox_holders
		WHERE org_id = $1
		  AND session_id = $2
		  AND holder_kind = 'thread_runtime'
		  AND holder_id <> $3
		  AND status IN ('active', 'draining')
		  AND expires_at > now()`, orgID, sessionID, excludedHolderID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active sibling thread runtime holders: %w", err)
	}
	return count, nil
}
