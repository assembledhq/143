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

type AcquireCodeReviewSandboxHolderParams struct {
	SessionID     uuid.UUID
	ThreadID      uuid.UUID
	ContainerID   string
	OwnerNodeID   string
	LeaseToken    uuid.UUID
	LeaseDuration time.Duration
}

// AcquireCodeReview retains the exact sandbox owned by an active review. The
// review and session rows are locked while eligibility is checked, so a
// concurrent terminal transition or container handoff cannot pass an old
// observation into the insert. A successful turn on the same container and
// owner starts a new bounded idle interval; its fencing token is preserved.
// An expired row may be rearmed by a still-eligible turn: the session-row
// lock serializes acquisition with GC finalization, while the active turn hold
// or queued/running agent job protects the container during a reviewer lane.
func (s *SessionSandboxHolderStore) AcquireCodeReview(ctx context.Context, orgID uuid.UUID, params AcquireCodeReviewSandboxHolderParams) (models.SessionSandboxHolder, bool, error) {
	if orgID == uuid.Nil || params.SessionID == uuid.Nil || params.ThreadID == uuid.Nil || params.ContainerID == "" || params.OwnerNodeID == "" || params.LeaseToken == uuid.Nil {
		return models.SessionSandboxHolder{}, false, fmt.Errorf("acquire code review sandbox holder: missing ownership identity")
	}
	leaseSeconds := int(params.LeaseDuration.Seconds())
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	rows, err := s.db.Query(ctx, `
		WITH eligible AS MATERIALIZED (
			SELECT m.id AS review_id
			FROM code_review_session_metadata m
			JOIN sessions s ON s.org_id = m.org_id AND s.id = m.session_id
			JOIN nodes n ON n.id = s.worker_node_id
			WHERE m.org_id = @org_id
			  AND m.session_id = @session_id
			  AND m.status IN ('queued', 'running')
			  AND m.stale = false
			  AND s.origin = 'code_review'
			  AND s.container_id = @container_id
			  AND s.worker_node_id = @owner_node_id
			  AND n.status = 'active' AND n.last_heartbeat_at >= now() - interval '90 seconds'
			  AND s.revision_context->>'head_sha' = m.head_sha
			  AND EXISTS (
				SELECT 1 FROM code_review_agent_results r
				WHERE r.org_id = m.org_id AND r.session_id = m.session_id
				  AND r.structured_result->>'thread_id' = @thread_id
				  AND r.role IN ('reviewer', 'orchestrator')
			  )
			FOR UPDATE OF m, s
		)
		INSERT INTO session_sandbox_holders AS h (
			org_id, session_id, container_id, holder_kind, holder_id,
			owner_node_id, lease_token, status, heartbeat_at, expires_at
		)
		SELECT @org_id, @session_id, @container_id, 'code_review', eligible.review_id,
			@owner_node_id, @lease_token, 'active', now(), now() + (@lease_seconds * interval '1 second')
		FROM eligible
		ON CONFLICT (org_id, session_id, holder_kind, holder_id)
			WHERE status IN ('active', 'draining')
		DO UPDATE SET heartbeat_at = now(),
			created_at = now(),
			expires_at = now() + (@lease_seconds * interval '1 second'),
			updated_at = now()
		WHERE h.container_id = EXCLUDED.container_id
		  AND h.owner_node_id = EXCLUDED.owner_node_id
		  AND h.status = 'active'
		RETURNING `+sessionSandboxHolderColumns, pgx.NamedArgs{
		"org_id": orgID, "session_id": params.SessionID,
		"thread_id": params.ThreadID.String(), "container_id": params.ContainerID,
		"owner_node_id": params.OwnerNodeID, "lease_token": params.LeaseToken,
		"lease_seconds": leaseSeconds,
	})
	if err != nil {
		return models.SessionSandboxHolder{}, false, fmt.Errorf("acquire code review sandbox holder: %w", err)
	}
	holder, err := pgx.CollectOneRow(rows, scanSessionSandboxHolderRow)
	if err == pgx.ErrNoRows {
		return models.SessionSandboxHolder{}, false, nil
	}
	if err != nil {
		return models.SessionSandboxHolder{}, false, fmt.Errorf("acquire code review sandbox holder: %w", err)
	}
	return holder, true, nil
}

// RenewCodeReview keeps an already-owned handoff container available while
// the active review controller advances. A delayed controller cannot transfer
// ownership: container, node, and lease token stay unchanged, and the holder
// must still be live. Retention never extends past 120 seconds from creation.
func (s *SessionSandboxHolderStore) RenewCodeReview(ctx context.Context, orgID, sessionID uuid.UUID, leaseDuration time.Duration) (bool, error) {
	leaseSeconds := int(leaseDuration.Seconds())
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	tag, err := s.db.Exec(ctx, `
		WITH eligible AS MATERIALIZED (
			SELECT m.id AS review_id, s.container_id, s.worker_node_id
			FROM code_review_session_metadata m
			JOIN sessions s ON s.org_id = m.org_id AND s.id = m.session_id
			JOIN nodes n ON n.id = s.worker_node_id
			WHERE m.org_id = @org_id AND m.session_id = @session_id
			  AND m.status IN ('queued', 'running') AND m.stale = false
			  AND s.origin = 'code_review'
			  AND s.revision_context->>'head_sha' = m.head_sha
			  AND n.status = 'active' AND n.last_heartbeat_at >= now() - interval '90 seconds'
			FOR UPDATE OF m, s
		)
		UPDATE session_sandbox_holders h
		SET heartbeat_at = now(),
			expires_at = LEAST(now() + (@lease_seconds * interval '1 second'), h.created_at + interval '120 seconds'),
			updated_at = now()
		FROM eligible e
		WHERE h.org_id = @org_id AND h.session_id = @session_id
		  AND h.holder_kind = 'code_review' AND h.holder_id = e.review_id
		  AND h.container_id = e.container_id AND h.owner_node_id = e.worker_node_id
		  AND h.status = 'active' AND h.expires_at > now()
		  AND h.created_at + interval '120 seconds' > now()
		  AND h.heartbeat_at <= now() - interval '20 seconds'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "lease_seconds": leaseSeconds,
	})
	if err != nil {
		return false, fmt.Errorf("renew code review sandbox holder: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ReleaseTerminalCodeReview drops a review holder after the review is no
// longer eligible. This is safe from any node because physical destruction
// remains separately guarded by FinalizeContainerDestroy on the owner.
func (s *SessionSandboxHolderStore) ReleaseTerminalCodeReview(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		WITH terminal AS MATERIALIZED (
			SELECT id FROM code_review_session_metadata
			WHERE org_id = @org_id AND session_id = @session_id
			  AND (status NOT IN ('queued', 'running') OR stale = true)
			FOR UPDATE
		)
		UPDATE session_sandbox_holders h
		SET status = 'released', released_at = COALESCE(released_at, now()), updated_at = now()
		FROM terminal t
		WHERE h.org_id = @org_id AND h.session_id = @session_id
		  AND h.holder_kind = 'code_review' AND h.holder_id = t.id
		  AND h.status IN ('active', 'draining')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	})
	if err != nil {
		return false, fmt.Errorf("release terminal code review sandbox holder: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ReleaseCodeReviewHoldersByOwner drops idle review retention immediately
// after a worker enters drain. Active turns and previews keep their separate
// holds; other nodes can then restore the checkpoint without waiting for the
// review lease deadline. A failed node is handled by system GC instead.
// lint:allow-no-orgid reason="worker drain releases review holders owned by one node across organizations"
func (s *SessionSandboxHolderStore) ReleaseCodeReviewHoldersByOwner(ctx context.Context, nodeID string) (int64, error) {
	if nodeID == "" {
		return 0, fmt.Errorf("release code review holders by owner: missing node id")
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE session_sandbox_holders h
		SET status = 'released', released_at = COALESCE(released_at, now()), updated_at = now()
		FROM nodes n
		WHERE n.id = $1 AND n.status = 'draining'
		  AND h.owner_node_id = n.id
		  AND h.holder_kind = 'code_review' AND h.status IN ('active', 'draining')`, nodeID)
	if err != nil {
		return 0, fmt.Errorf("release code review holders by owner: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ExpireCodeReviewHolders is a cross-org system cleanup for holder rows that
// no longer protect a live, matching review workspace. The owner-node GC
// independently finalizes and destroys the now-unheld physical container.
// lint:allow-no-orgid reason="system sandbox GC expires review holders across organizations"
func (s *SessionSandboxHolderStore) ExpireCodeReviewHolders(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	tag, err := s.db.Exec(ctx, `
		WITH candidates AS MATERIALIZED (
			SELECT h.id
			FROM session_sandbox_holders h
			LEFT JOIN code_review_session_metadata m
			  ON m.org_id = h.org_id AND m.session_id = h.session_id AND m.id = h.holder_id
			LEFT JOIN sessions s ON s.org_id = h.org_id AND s.id = h.session_id
			LEFT JOIN nodes n ON n.id = h.owner_node_id
			WHERE h.holder_kind = 'code_review' AND h.status IN ('active', 'draining')
			  AND (
				h.expires_at <= now() OR h.created_at + interval '120 seconds' <= now()
				OR m.id IS NULL OR m.status NOT IN ('queued', 'running') OR m.stale = true
				OR s.id IS NULL OR s.container_id IS DISTINCT FROM h.container_id
				OR s.worker_node_id IS DISTINCT FROM h.owner_node_id
				OR s.revision_context->>'head_sha' IS DISTINCT FROM m.head_sha
				OR n.id IS NULL OR n.status <> 'active'
				OR n.last_heartbeat_at < now() - interval '90 seconds'
			  )
			ORDER BY h.expires_at, h.id
			LIMIT $1 FOR UPDATE OF h SKIP LOCKED
		)
		UPDATE session_sandbox_holders h
		SET status = 'expired', released_at = COALESCE(released_at, now()), updated_at = now()
		FROM candidates c WHERE h.id = c.id`, limit)
	if err != nil {
		return 0, fmt.Errorf("expire code review sandbox holders: %w", err)
	}
	return tag.RowsAffected(), nil
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
