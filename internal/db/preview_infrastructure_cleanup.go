package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

var previewInfrastructureCleanupQuery = fmt.Sprintf(`
	SELECT c.owner_index,
		(pi.id IS NULL OR COALESCE(pi.session_id, '00000000-0000-0000-0000-000000000000'::uuid) = c.session_id)
		AND NOT (
			COALESCE(pi.status IN %s AND (
				pi.preview_handle = c.handle OR
				(pi.status = 'starting' AND pi.worker_node_id = c.worker_node_id)
			), FALSE)
			OR EXISTS (
				SELECT 1 FROM preview_runtimes pr
				WHERE pr.org_id = c.org_id AND pr.preview_instance_id = c.preview_id
				  AND pr.status IN %s
				  AND (pr.preview_handle = c.handle OR
					(pr.status = 'starting' AND pr.preview_handle = ''
					 AND pr.worker_node_id = c.worker_node_id AND pi.id IS NOT NULL))
			)
			OR EXISTS (
				SELECT 1 FROM nodes n
				WHERE n.id = c.worker_node_id AND n.id <> @worker_node_id
				  AND n.mode IN ('worker', 'all')
				  AND n.status IN ('active', 'draining')
				  AND n.last_heartbeat_at >= @stale_before
			)
		) AS eligible
	FROM unnest(@org_ids::uuid[], @preview_ids::uuid[], @session_ids::uuid[],
		@handles::text[], @worker_node_ids::text[])
		WITH ORDINALITY AS c(org_id, preview_id, session_id, handle, worker_node_id, owner_index)
	LEFT JOIN preview_instances pi ON pi.org_id = c.org_id AND pi.id = c.preview_id`, activeStatusFilter, activeRuntimeStatusFilter)

// ResolvePreviewInfrastructureCleanup authorizes deletion only when persisted
// handles, active runtimes, and other fresh worker generations cannot own it.
// Tenant pairs are joined explicitly for each candidate; nil sessions represent
// standalone previews. Invalid identities and missing result rows fail closed.
// A starting same-generation preview or blank runtime also protects the handoff
// before the manager commits a newly returned provider handle during a recycle.
// lint:allow-no-orgid reason="cross-org infrastructure maintenance batches explicit org and preview identity pairs"
func (s *PreviewStore) ResolvePreviewInfrastructureCleanup(ctx context.Context, owners []models.PreviewInfrastructureOwner, workerNodeID string, staleBefore time.Time) (map[models.PreviewInfrastructureOwner]bool, error) {
	eligible := make(map[models.PreviewInfrastructureOwner]bool, len(owners))
	valid := make([]models.PreviewInfrastructureOwner, 0, len(owners))
	orgIDs := make([]uuid.UUID, 0, len(owners))
	previewIDs := make([]uuid.UUID, 0, len(owners))
	sessionIDs := make([]uuid.UUID, 0, len(owners))
	handles := make([]string, 0, len(owners))
	workerNodeIDs := make([]string, 0, len(owners))
	for _, owner := range owners {
		eligible[owner] = false
		if !owner.Valid() {
			continue
		}
		valid = append(valid, owner)
		orgIDs = append(orgIDs, owner.OrgID)
		previewIDs = append(previewIDs, owner.PreviewID)
		sessionIDs = append(sessionIDs, owner.SessionID)
		handles = append(handles, owner.Handle)
		workerNodeIDs = append(workerNodeIDs, owner.WorkerNodeID)
	}
	if len(valid) == 0 {
		return eligible, nil
	}
	if !s.Configured() || strings.TrimSpace(workerNodeID) == "" || staleBefore.IsZero() {
		return eligible, fmt.Errorf("preview infrastructure ownership query is not configured")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := s.db.Query(queryCtx, previewInfrastructureCleanupQuery, pgx.NamedArgs{
		"org_ids": orgIDs, "preview_ids": previewIDs, "session_ids": sessionIDs,
		"handles": handles, "worker_node_ids": workerNodeIDs,
		"worker_node_id": workerNodeID, "stale_before": staleBefore,
	})
	if err != nil {
		return eligible, fmt.Errorf("resolve preview infrastructure cleanup: %w", err)
	}
	results, err := pgx.CollectRows(rows, pgx.RowToStructByName[struct {
		Index    int64 `db:"owner_index"`
		Eligible bool  `db:"eligible"`
	}])
	if err != nil {
		return eligible, fmt.Errorf("scan preview infrastructure cleanup: %w", err)
	}
	for _, result := range results {
		if result.Index < 1 || result.Index > int64(len(valid)) {
			return eligible, fmt.Errorf("preview infrastructure cleanup returned invalid owner index %d", result.Index)
		}
	}
	for _, result := range results {
		eligible[valid[result.Index-1]] = result.Eligible
	}
	return eligible, nil
}

var recoverPreviewRuntimesAfterWorkerRestartQuery = fmt.Sprintf(`WITH lost AS (
	UPDATE preview_runtimes
	SET status = 'lost', error = @reason, unavailable_reason = 'owner_lost',
		stopped_at = COALESCE(stopped_at, now()), updated_at = now()
	WHERE worker_node_id = @worker_node_id AND created_at < @process_started_at
	  AND status IN %s
	RETURNING id, org_id, preview_instance_id
)
UPDATE preview_instances pi
SET status = 'unavailable', current_phase = 'unavailable', error = @reason,
	unavailable_reason = 'owner_lost', preview_holding_container = FALSE,
	stopped_at = COALESCE(stopped_at, now()), updated_at = now()
WHERE pi.worker_node_id = @worker_node_id
  AND pi.created_at < @process_started_at
  AND pi.status IN %s
  AND NOT EXISTS (
	SELECT 1 FROM preview_runtimes remaining
	WHERE remaining.org_id = pi.org_id AND remaining.preview_instance_id = pi.id
	  AND remaining.status IN %s
	  AND NOT EXISTS (
		SELECT 1 FROM lost retired
		WHERE retired.org_id = remaining.org_id AND retired.id = remaining.id
	  )
  )`, activeRuntimeStatusFilter, activeStatusFilter, activeRuntimeStatusFilter)

// CapturePreviewRuntimeRecoveryCutoff uses the database clock so runtime
// created_at values and the process startup fence share one time source.
// lint:allow-no-orgid reason="startup infrastructure clock lookup reads no tenant data"
func (s *PreviewStore) CapturePreviewRuntimeRecoveryCutoff(ctx context.Context) (time.Time, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cutoff time.Time
	if err := s.db.QueryRow(queryCtx, `SELECT clock_timestamp()`).Scan(&cutoff); err != nil {
		return time.Time{}, fmt.Errorf("capture preview runtime recovery cutoff: %w", err)
	}
	return cutoff, nil
}

// RecoverPreviewRuntimesAfterWorkerRestart retires this worker identity's old
// serving rows, including local previews without runtime rows, before its fresh
// provider starts heartbeating or accepting jobs.
// processStartedAt must be captured before node registration or job admission.
// Other generations and rows created after that cutoff are never invalidated;
// their active ownership also protects the preview and its sandbox hold.
// lint:allow-no-orgid reason="cross-org startup recovery retires only this worker generation before its captured process cutoff"
func (s *PreviewStore) RecoverPreviewRuntimesAfterWorkerRestart(ctx context.Context, workerNodeID string, processStartedAt time.Time) (int64, error) {
	if strings.TrimSpace(workerNodeID) == "" || processStartedAt.IsZero() {
		return 0, fmt.Errorf("preview restart recovery requires worker identity and process start cutoff")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tag, err := s.db.Exec(queryCtx, recoverPreviewRuntimesAfterWorkerRestartQuery, pgx.NamedArgs{
		"worker_node_id": workerNodeID, "process_started_at": processStartedAt,
		"reason": "preview worker restarted",
	})
	if err != nil {
		return 0, fmt.Errorf("recover preview runtimes after worker restart: %w", err)
	}
	return tag.RowsAffected(), nil
}
