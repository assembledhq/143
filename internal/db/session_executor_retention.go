package db

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ListTerminalContainersByIdentity looks up one bounded host inventory batch.
// The paired tenant/executor join deliberately does not form a cross product.
// A terminal row with a still-owned job is ambiguous and must be preserved.
// lint:allow-no-orgid reason="host-local retention batches explicitly paired tenant and executor identities across orgs"
func (s *SessionExecutorStore) ListTerminalContainersByIdentity(ctx context.Context, identities []models.SessionExecutorIdentity, completedBefore time.Time) ([]models.SessionExecutorContainerRetention, error) {
	if len(identities) == 0 {
		return []models.SessionExecutorContainerRetention{}, nil
	}
	orgIDs := make([]uuid.UUID, len(identities))
	executorIDs := make([]uuid.UUID, len(identities))
	for i, identity := range identities {
		if identity.OrgID == uuid.Nil || identity.ID == uuid.Nil {
			return nil, fmt.Errorf("executor retention requires tenant and executor identities")
		}
		orgIDs[i], executorIDs[i] = identity.OrgID, identity.ID
	}
	rows, err := s.db.Query(ctx, `
		SELECT se.id, se.org_id, se.session_id, se.thread_id, se.job_id, se.job_type,
			se.host_node_id, se.container_id, se.image, se.build_sha, se.status,
			se.completed_at, se.lease_expires_at
		FROM session_executors se
		JOIN unnest($1::uuid[], $2::uuid[]) AS requested(org_id, executor_id)
		  ON se.org_id = requested.org_id AND se.id = requested.executor_id
		WHERE se.status IN ('completed', 'requeued', 'failed', 'lost')
		  AND se.completed_at < $3
		  AND se.container_id IS NOT NULL AND se.container_id <> ''
		  AND (se.lease_expires_at IS NULL OR se.lease_expires_at <= now())
		  AND NOT EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.org_id = se.org_id AND j.status = 'running'
			  AND ((j.owner_kind = 'session_executor' AND j.run_owner_id = se.id::text)
			    OR (j.id = se.job_id AND j.lock_token = se.lock_token))
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM session_executors active
			WHERE active.org_id = se.org_id AND active.container_id = se.container_id
			  AND active.status IN ('starting', 'running', 'draining')
		  )`, orgIDs, executorIDs, completedBefore)
	if err != nil {
		return nil, fmt.Errorf("list terminal executor containers: %w", err)
	}
	executors, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionExecutorContainerRetention])
	if err != nil {
		return nil, fmt.Errorf("collect terminal executor containers: %w", err)
	}
	return executors, nil
}
