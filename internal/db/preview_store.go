package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// =============================================================================
// PreviewStore
// =============================================================================

// PreviewStore manages preview_instances and related tables.
// It uses TxStarter because stop operations need transactional consistency
// (stop preview + revoke all access sessions atomically).
type PreviewStore struct {
	db TxStarter
}

// NewPreviewStore creates a new PreviewStore.
func NewPreviewStore(db TxStarter) *PreviewStore {
	return &PreviewStore{db: db}
}

// Begin starts a transaction.
// lint:allow-no-orgid reason="transaction helper; org scoping is enforced by the wrapped queries"
func (s *PreviewStore) Begin(ctx context.Context) (pgx.Tx, error) {
	return s.db.Begin(ctx)
}

// WithTx returns a new PreviewStore that uses the given transaction.
// lint:allow-no-orgid reason="transaction helper; org scoping is enforced by the wrapped queries"
func (s *PreviewStore) WithTx(tx pgx.Tx) *PreviewStore {
	return &PreviewStore{db: tx}
}

// activeStatusFilter is the SQL IN clause for active preview statuses.
// Keep in sync with models.PreviewStatus.IsActive().
// This is a constant interpolated via fmt.Sprintf — safe because it is never user input.
const activeStatusFilter = `('starting', 'ready', 'partially_ready', 'unhealthy')`

// terminalStatusFilter is the SQL IN clause for preview statuses that can be
// shown as the most recent preview history when no active preview exists.
const terminalStatusFilter = `('stopped', 'expired', 'failed')`

// --- Column lists ---

const previewInstanceColumns = `id, session_id, org_id, user_id, profile_name, name, status,
	provider, worker_node_id, preview_handle, primary_service, port,
	config_digest, base_commit_sha, last_accessed_at, expires_at, stopped_at,
	last_path, memory_limit_mb, cpu_limit_millis, disk_limit_mb, recycle_config, recycle_sandbox, error, created_at, updated_at, recycled_at, recycle_scheduled_at, preview_holding_container`

const previewServiceColumns = `id, preview_instance_id, service_name, role, status,
	command, cwd, port, pid, error, created_at`

const previewInfraColumns = `id, preview_instance_id, infra_name, template,
	container_id, status, host, port, credentials_hash, error, created_at`

const previewSnapshotColumns = `id, preview_instance_id, trigger, url_path, blob_ref,
	viewport_width, viewport_height, console_errors, file_changes, created_at`

const previewLogColumns = `id, preview_instance_id, org_id, level, step, message,
	metadata, created_at`

const previewAccessSessionColumns = `id, org_id, user_id, preview_instance_id,
	session_token_hash, issued_at, expires_at, revoked_at, last_accessed_at, created_at`

const previewStartupCacheColumns = `id, org_id, repo_id, snapshot_key, blob_path,
	size_bytes, worker_node_id, last_used_at, created_at`

const prPreviewStateColumns = `id, org_id, repo_id, pr_number, github_comment_id,
	last_preview_instance_id, last_screenshot_blob_path, last_visual_diff_blob_path,
	base_snapshot_key, status, created_at, updated_at`

// =============================================================================
// Preview Instance CRUD
// =============================================================================

// CreatePreviewInstance inserts a new preview instance.
func (s *PreviewStore) CreatePreviewInstance(ctx context.Context, p *models.PreviewInstance) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_instances (
			session_id, org_id, user_id, profile_name, name, status, provider,
			worker_node_id, preview_handle, primary_service, port,
			config_digest, base_commit_sha, expires_at,
			last_path, memory_limit_mb, cpu_limit_millis, disk_limit_mb, recycle_config, recycle_sandbox
		) VALUES (
			@session_id, @org_id, @user_id, @profile_name, @name, @status, @provider,
			@worker_node_id, @preview_handle, @primary_service, @port,
			@config_digest, @base_commit_sha, @expires_at,
			@last_path, @memory_limit_mb, @cpu_limit_millis, @disk_limit_mb, @recycle_config, @recycle_sandbox
		) RETURNING %s`, previewInstanceColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id":       p.SessionID,
		"org_id":           p.OrgID,
		"user_id":          p.UserID,
		"profile_name":     p.ProfileName,
		"name":             p.Name,
		"status":           p.Status,
		"provider":         p.Provider,
		"worker_node_id":   p.WorkerNodeID,
		"preview_handle":   p.PreviewHandle,
		"primary_service":  p.PrimaryService,
		"port":             p.Port,
		"config_digest":    p.ConfigDigest,
		"base_commit_sha":  p.BaseCommitSHA,
		"expires_at":       p.ExpiresAt,
		"last_path":        p.LastPath,
		"memory_limit_mb":  p.MemoryLimitMB,
		"cpu_limit_millis": p.CPULimitMillis,
		"disk_limit_mb":    p.DiskLimitMB,
		"recycle_config":   p.RecycleConfig,
		"recycle_sandbox":  p.RecycleSandbox,
	})
	if err != nil {
		return fmt.Errorf("insert preview instance: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return fmt.Errorf("scan preview instance: %w", err)
	}
	*p = row
	return nil
}

// GetPreviewInstance returns a preview instance by ID, scoped to org.
func (s *PreviewStore) GetPreviewInstance(ctx context.Context, orgID, id uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances WHERE id = @id AND org_id = @org_id`, previewInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query preview instance: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("get preview instance: %w", err)
	}
	return &row, nil
}

// GetActivePreviewForSession returns the active preview for a session, if any.
func (s *PreviewStore) GetActivePreviewForSession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE org_id = @org_id AND session_id = @session_id
		AND status IN %s
		LIMIT 1`, previewInstanceColumns, activeStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query active preview: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("get active preview: %w", err)
	}
	return &row, nil
}

// GetLatestFailedPreviewForSession returns the failed preview only when it is
// the newest preview row for the session. This keeps async startup failures
// visible without resurrecting stale failures after a later successful preview
// has been stopped.
func (s *PreviewStore) GetLatestFailedPreviewForSession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE org_id = @org_id AND session_id = @session_id
		AND status = 'failed'
		AND NOT EXISTS (
			SELECT 1 FROM preview_instances newer
			WHERE newer.org_id = @org_id
			  AND newer.session_id = @session_id
			  AND newer.created_at > preview_instances.created_at
		)
		ORDER BY created_at DESC
		LIMIT 1`, previewInstanceColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query latest failed preview: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("get latest failed preview: %w", err)
	}
	return &row, nil
}

// GetLatestTerminalPreviewForSession returns the newest inactive preview row
// for a session so the UI can show when the last preview ran and stopped.
func (s *PreviewStore) GetLatestTerminalPreviewForSession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE org_id = @org_id AND session_id = @session_id
		AND status IN %s
		ORDER BY created_at DESC
		LIMIT 1`, previewInstanceColumns, terminalStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query latest terminal preview: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("get latest terminal preview: %w", err)
	}
	return &row, nil
}

// UpdatePreviewWorkerNodeID reassigns a starting preview to the worker that
// claimed its pinned job after the originally targeted worker was declared
// dead.
func (s *PreviewStore) UpdatePreviewWorkerNodeID(ctx context.Context, orgID, id uuid.UUID, workerNodeID string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE preview_instances SET worker_node_id = @worker_node_id, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status = 'starting'`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "worker_node_id": workerNodeID},
	)
	if err != nil {
		return fmt.Errorf("update preview worker node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview instance not found or no longer starting")
	}
	return nil
}

// UpdatePreviewStatus updates the status and optional error of a preview.
// For terminal statuses (stopped, failed, expired), it also sets stopped_at
// and cascades the terminal transition to non-terminal child preview_services
// and preview_infrastructure rows so the UI's startup checklist doesn't spin
// forever on orphaned children.
func (s *PreviewStore) UpdatePreviewStatus(ctx context.Context, orgID, id uuid.UUID, status models.PreviewStatus, errMsg string) error {
	if status.IsTerminal() {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin terminal preview status transaction: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		txStore := s.WithTx(tx)
		rowsAffected, err := txStore.updatePreviewStatus(ctx, orgID, id, status, errMsg)
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return fmt.Errorf("preview instance not found")
		}
		if err := txStore.cascadeChildrenToTerminal(ctx, orgID, id, status, errMsg); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit terminal preview status transaction: %w", err)
		}
		return nil
	}

	rowsAffected, err := s.updatePreviewStatus(ctx, orgID, id, status, errMsg)
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("preview instance not found")
	}
	return nil
}

func (s *PreviewStore) updatePreviewStatus(ctx context.Context, orgID, id uuid.UUID, status models.PreviewStatus, errMsg string) (int64, error) {
	var query string
	if status.IsTerminal() {
		query = `UPDATE preview_instances SET status = @status, error = @error, stopped_at = now(), updated_at = now()
			WHERE id = @id AND org_id = @org_id`
	} else {
		query = `UPDATE preview_instances SET status = @status, error = @error, updated_at = now()
			WHERE id = @id AND org_id = @org_id`
	}
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id": id, "org_id": orgID, "status": status, "error": errMsg,
	})
	if err != nil {
		return 0, fmt.Errorf("update preview status: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpdatePreviewStatusIfActive atomically transitions a preview to the given
// status only if its current status is not terminal (stopped, failed, expired).
// Returns true if the update took effect, false if the preview was already
// terminal (no error). This eliminates the TOCTOU window in RecyclePreview.
//
// When the new status is terminal, it also cascades the transition to
// non-terminal child preview_services / preview_infrastructure rows.
func (s *PreviewStore) UpdatePreviewStatusIfActive(ctx context.Context, orgID, id uuid.UUID, status models.PreviewStatus, errMsg string) (bool, error) {
	if status.IsTerminal() {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return false, fmt.Errorf("begin conditional terminal preview status transaction: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		txStore := s.WithTx(tx)
		rowsAffected, err := txStore.updatePreviewStatusIfActive(ctx, orgID, id, status, errMsg)
		if err != nil {
			return false, err
		}
		if rowsAffected == 0 {
			return false, nil
		}
		if err := txStore.cascadeChildrenToTerminal(ctx, orgID, id, status, errMsg); err != nil {
			return true, err
		}
		if err := tx.Commit(ctx); err != nil {
			return true, fmt.Errorf("commit conditional terminal preview status transaction: %w", err)
		}
		return true, nil
	}

	rowsAffected, err := s.updatePreviewStatusIfActive(ctx, orgID, id, status, errMsg)
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

func (s *PreviewStore) updatePreviewStatusIfActive(ctx context.Context, orgID, id uuid.UUID, status models.PreviewStatus, errMsg string) (int64, error) {
	query := `UPDATE preview_instances SET status = @status, error = @error, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status NOT IN ('stopped', 'failed', 'expired')`
	if status.IsTerminal() {
		query = `UPDATE preview_instances SET status = @status, error = @error, stopped_at = now(), updated_at = now()
			WHERE id = @id AND org_id = @org_id AND status NOT IN ('stopped', 'failed', 'expired')`
	}
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id": id, "org_id": orgID, "status": status, "error": errMsg,
	})
	if err != nil {
		return 0, fmt.Errorf("conditional update preview status: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpdatePreviewAccess updates the last_accessed_at timestamp.
func (s *PreviewStore) UpdatePreviewAccess(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_instances SET last_accessed_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("update preview access: %w", err)
	}
	return nil
}

// UpdatePreviewExpiry sets a new expires_at timestamp.
func (s *PreviewStore) UpdatePreviewExpiry(ctx context.Context, orgID, id uuid.UUID, expiresAt time.Time) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_instances SET expires_at = @expires_at, updated_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "expires_at": expiresAt},
	)
	if err != nil {
		return fmt.Errorf("update preview expiry: %w", err)
	}
	return nil
}

// UpdateLastPath updates the last proxied request path (for navigation restore on restart).
func (s *PreviewStore) UpdateLastPath(ctx context.Context, orgID, id uuid.UUID, path string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_instances SET last_path = @path, updated_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "path": path},
	)
	if err != nil {
		return fmt.Errorf("update last path: %w", err)
	}
	return nil
}

// StopPreview sets status to stopped and records the stop time. It also
// cascades the terminal transition to non-terminal child preview_services
// and preview_infrastructure rows so the UI's startup checklist reaches a
// terminal state instead of spinning on orphaned children.
//
// This should be called within a transaction that also revokes access sessions.
func (s *PreviewStore) StopPreview(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		fmt.Sprintf(`UPDATE preview_instances SET status = @new_status, stopped_at = now(), updated_at = now()
		 WHERE id = @id AND org_id = @org_id AND status IN %s`, activeStatusFilter),
		pgx.NamedArgs{"id": id, "org_id": orgID, "new_status": models.PreviewStatusStopped},
	)
	if err != nil {
		return fmt.Errorf("stop preview: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview instance not found or already stopped")
	}
	if err := s.cascadeChildrenToTerminal(ctx, orgID, id, models.PreviewStatusStopped, ""); err != nil {
		return err
	}
	return nil
}

// cascadeChildrenToTerminal flips non-terminal preview_services and
// preview_infrastructure rows to a terminal state when their parent preview
// transitions to terminal. Without this, the frontend's startup checklist
// (which reads child statuses to drive spinner state) keeps spinning forever
// when a launch is interrupted before the worker reaches its own terminal-
// state writes — for example, when a rolling deploy kills the API process
// after it reserved the rows but before the worker received the launch RPC.
//
// Mapping:
//   - parent="failed": children still in starting/provisioning/unhealthy go to
//     "failed" (they were the failure, by elimination); already-ready/healthy
//     children go to "stopped" (they had reached health, so the failure was
//     elsewhere).
//   - parent="stopped"/"expired": all non-terminal children go to "stopped".
//
// The parent's error is propagated only into rows that were still pending
// (starting/provisioning/unhealthy) and have no error of their own; rows
// with their own captured error keep it.
func (s *PreviewStore) cascadeChildrenToTerminal(ctx context.Context, orgID, previewID uuid.UUID, parentStatus models.PreviewStatus, parentErr string) error {
	if !parentStatus.IsTerminal() {
		return nil
	}

	// "stopped" / "expired" parent — everything still alive becomes "stopped".
	// "failed" parent — pending children become "failed", ready/healthy become "stopped".
	pendingTarget := string(models.PreviewServiceStatusStopped)
	healthyTarget := string(models.PreviewServiceStatusStopped)
	infraPendingTarget := string(models.PreviewInfraStatusStopped)
	infraHealthyTarget := string(models.PreviewInfraStatusStopped)
	if parentStatus == models.PreviewStatusFailed {
		pendingTarget = string(models.PreviewServiceStatusFailed)
		infraPendingTarget = string(models.PreviewInfraStatusFailed)
	}

	if _, err := s.db.Exec(ctx,
		`UPDATE preview_services SET
			status = CASE
				WHEN status = 'starting' THEN @pending_target
				WHEN status = 'ready' THEN @healthy_target
				ELSE status
			END,
			error = CASE
				WHEN status = 'starting' AND (error IS NULL OR error = '') THEN @parent_err
				ELSE error
			END
		 WHERE preview_instance_id = @pid
		   AND status NOT IN ('stopped', 'failed')
		   AND preview_instance_id IN (SELECT id FROM preview_instances WHERE id = @pid AND org_id = @org_id)`,
		pgx.NamedArgs{
			"pid":            previewID,
			"org_id":         orgID,
			"pending_target": pendingTarget,
			"healthy_target": healthyTarget,
			"parent_err":     parentErr,
		},
	); err != nil {
		return fmt.Errorf("cascade preview services to terminal: %w", err)
	}

	if _, err := s.db.Exec(ctx,
		`UPDATE preview_infrastructure SET
			status = CASE
				WHEN status IN ('provisioning', 'unhealthy') THEN @pending_target
				WHEN status = 'healthy' THEN @healthy_target
				ELSE status
			END,
			error = CASE
				WHEN status IN ('provisioning', 'unhealthy') AND (error IS NULL OR error = '') THEN @parent_err
				ELSE error
			END
		 WHERE preview_instance_id = @pid
		   AND status NOT IN ('stopped', 'failed')
		   AND preview_instance_id IN (SELECT id FROM preview_instances WHERE id = @pid AND org_id = @org_id)`,
		pgx.NamedArgs{
			"pid":            previewID,
			"org_id":         orgID,
			"pending_target": infraPendingTarget,
			"healthy_target": infraHealthyTarget,
			"parent_err":     parentErr,
		},
	); err != nil {
		return fmt.Errorf("cascade preview infrastructure to terminal: %w", err)
	}

	return nil
}

// AcquirePreviewHold marks this preview as a holder of the shared sandbox
// container. Called once the preview is known to be driving the container
// (either because it hydrated one, or because it attached to a live
// turn-created container). The session_id is returned so callers can look up
// the session row without a second round-trip when they need to coordinate
// teardown with turn_holding_container.
func (s *PreviewStore) AcquirePreviewHold(ctx context.Context, orgID, previewID uuid.UUID) (sessionID uuid.UUID, err error) {
	query := `UPDATE preview_instances
		SET preview_holding_container = TRUE, updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING session_id`
	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     previewID,
		"org_id": orgID,
	}).Scan(&sessionID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("acquire preview hold: %w", err)
	}
	return sessionID, nil
}

// ReleasePreviewHold flips preview_holding_container to false and returns
// the sibling state needed to decide on destroy:
//   - sessionID: the session this preview belongs to
//   - turnStillHolds: whether sessions.turn_holding_container is TRUE
//   - containerID: the session's current container_id (empty when NULL)
//
// destroyNow is true when the caller should tear down the container and clear
// container_id. If false, either the turn still holds it or there was no
// container to begin with.
//
// Packaging these reads in one statement avoids a race where turn_holding_container
// flips between our release and a follow-up read.
func (s *PreviewStore) ReleasePreviewHold(ctx context.Context, orgID, previewID uuid.UUID) (destroyNow bool, sessionID uuid.UUID, containerID string, err error) {
	query := `WITH released AS (
			UPDATE preview_instances
			SET preview_holding_container = FALSE, updated_at = now()
			WHERE id = @id AND org_id = @org_id
			RETURNING session_id
		)
		SELECT
			released.session_id,
			COALESCE(s.container_id, '') AS container_id,
			COALESCE(s.turn_holding_container, FALSE) AS turn_holds
		FROM released
		JOIN sessions s ON s.id = released.session_id AND s.org_id = @org_id`

	var turnHolds bool
	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     previewID,
		"org_id": orgID,
	}).Scan(&sessionID, &containerID, &turnHolds)
	if err != nil {
		return false, uuid.Nil, "", fmt.Errorf("release preview hold: %w", err)
	}
	return containerID != "" && !turnHolds, sessionID, containerID, nil
}

// UpdatePreviewReservationConfig overwrites the config-derived fields of a
// preview row. Called by LaunchPreview after the handler resolves the final
// config (e.g. workspace autodetect replacing defaults) so the persisted row
// matches the config the services are actually started from.
//
// This is strictly a reservation-phase update: it only applies while the
// preview is still in 'starting' status, so a concurrent StopPreview can't be
// clobbered. The returned bool reports whether the row was still pending.
func (s *PreviewStore) UpdatePreviewReservationConfig(
	ctx context.Context,
	orgID, id uuid.UUID,
	name, primaryService, configDigest string,
	memoryLimitMB, cpuLimitMillis, diskLimitMB int,
	recycleConfig, recycleSandbox []byte,
) (bool, error) {
	query := `UPDATE preview_instances
		SET name = @name,
		    primary_service = @primary,
		    config_digest = @digest,
		    memory_limit_mb = @memory_limit_mb,
		    cpu_limit_millis = @cpu_limit_millis,
		    disk_limit_mb = @disk_limit_mb,
		    recycle_config = @recycle_config,
		    recycle_sandbox = @recycle_sandbox,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status = 'starting'`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":               id,
		"org_id":           orgID,
		"name":             name,
		"primary":          primaryService,
		"digest":           configDigest,
		"memory_limit_mb":  memoryLimitMB,
		"cpu_limit_millis": cpuLimitMillis,
		"disk_limit_mb":    diskLimitMB,
		"recycle_config":   recycleConfig,
		"recycle_sandbox":  recycleSandbox,
	})
	if err != nil {
		return false, fmt.Errorf("update preview reservation config: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// UpdatePreviewRecycleConfig overwrites config-derived fields for an existing
// active preview before an in-place recycle. Unlike reservation updates, this
// is allowed outside the initial starting reservation because a user restart
// should pick up the current workspace preview config.
func (s *PreviewStore) UpdatePreviewRecycleConfig(
	ctx context.Context,
	orgID, id uuid.UUID,
	name, primaryService, configDigest string,
	memoryLimitMB, cpuLimitMillis, diskLimitMB int,
	recycleConfig []byte,
) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE preview_instances
		SET name = @name,
		    primary_service = @primary,
		    config_digest = @digest,
		    memory_limit_mb = @memory_limit_mb,
		    cpu_limit_millis = @cpu_limit_millis,
		    disk_limit_mb = @disk_limit_mb,
		    recycle_config = @recycle_config,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status NOT IN ('stopped', 'failed', 'expired')`,
		pgx.NamedArgs{
			"id":               id,
			"org_id":           orgID,
			"name":             name,
			"primary":          primaryService,
			"digest":           configDigest,
			"memory_limit_mb":  memoryLimitMB,
			"cpu_limit_millis": cpuLimitMillis,
			"disk_limit_mb":    diskLimitMB,
			"recycle_config":   recycleConfig,
		},
	)
	if err != nil {
		return fmt.Errorf("update preview recycle config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview instance not found or no longer active")
	}
	return nil
}

// UpdatePreviewHandle updates the provider handle and primary port. A new
// handle implies the preview process restarted successfully, so recycled_at is
// refreshed to anchor the next recycle window.
func (s *PreviewStore) UpdatePreviewHandle(ctx context.Context, orgID, id uuid.UUID, handle string, port int) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE preview_instances SET preview_handle = @handle, port = @port, updated_at = now(), recycled_at = now()
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "handle": handle, "port": port},
	)
	if err != nil {
		return fmt.Errorf("update preview handle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview instance not found")
	}
	return nil
}

// ListActivePreviews returns all active previews for a given worker node.
// lint:allow-no-orgid reason="worker-scoped background sweep; scans across all orgs by design"
func (s *PreviewStore) ListActivePreviews(ctx context.Context, workerNodeID string) ([]models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE worker_node_id = @worker_node_id AND status IN %s
		ORDER BY created_at ASC`, previewInstanceColumns, activeStatusFilter)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"worker_node_id": workerNodeID})
	if err != nil {
		return nil, fmt.Errorf("list active previews: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("scan preview instance: %w", err)
	}
	return result, nil
}

// ListActivePreviewsRecycledBefore returns active previews on the given worker
// whose last successful start/recycle was before the cutoff time. Used by the
// recycler to avoid repeatedly restarting a long-lived row after its first recycle.
// lint:allow-no-orgid reason="worker-scoped background recycler; scans across all orgs by design"
func (s *PreviewStore) ListActivePreviewsRecycledBefore(ctx context.Context, workerNodeID string, recycledBefore time.Time) ([]models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE worker_node_id = @worker_node_id AND status IN %s AND recycled_at < @recycled_before
		ORDER BY recycled_at ASC`, previewInstanceColumns, activeStatusFilter)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"worker_node_id":  workerNodeID,
		"recycled_before": recycledBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("list active previews recycled before: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("scan preview instance: %w", err)
	}
	return result, nil
}

// ScheduleRecycle marks a preview for recycle in the future by stamping the
// recycle_scheduled_at column. Safe to call only when the column is NULL —
// the partial update avoids pushing the window out repeatedly if the
// recycler sweeps more than once within the grace period.
func (s *PreviewStore) ScheduleRecycle(ctx context.Context, orgID, previewID uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE preview_instances
		 SET recycle_scheduled_at = @at, updated_at = now()
		 WHERE id = @id AND org_id = @org_id AND recycle_scheduled_at IS NULL`,
		pgx.NamedArgs{"id": previewID, "org_id": orgID, "at": at},
	)
	if err != nil {
		return false, fmt.Errorf("schedule recycle: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClearRecycleSchedule resets the scheduled recycle timestamp back to NULL.
// Called after the recycler performs (or cancels) the recycle.
func (s *PreviewStore) ClearRecycleSchedule(ctx context.Context, orgID, previewID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_instances
		 SET recycle_scheduled_at = NULL, updated_at = now()
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": previewID, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("clear recycle schedule: %w", err)
	}
	return nil
}

// ListPreviewsScheduledToRecycleBefore returns active previews on this worker
// whose recycle_scheduled_at is at or before the cutoff — i.e., their grace
// period has elapsed and the recycler should now restart them.
// lint:allow-no-orgid reason="worker-scoped background recycler; scans across all orgs by design"
func (s *PreviewStore) ListPreviewsScheduledToRecycleBefore(ctx context.Context, workerNodeID string, cutoff time.Time) ([]models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE worker_node_id = @worker_node_id AND status IN %s
		  AND recycle_scheduled_at IS NOT NULL AND recycle_scheduled_at <= @cutoff
		ORDER BY recycle_scheduled_at ASC`, previewInstanceColumns, activeStatusFilter)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"worker_node_id": workerNodeID,
		"cutoff":         cutoff,
	})
	if err != nil {
		return nil, fmt.Errorf("list previews scheduled to recycle: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("scan preview instance: %w", err)
	}
	return result, nil
}

// CountActivePreviewsByOrg counts active previews for concurrency cap enforcement.
func (s *PreviewStore) CountActivePreviewsByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM preview_instances WHERE org_id = @org_id
		 AND status IN %s`, activeStatusFilter),
		pgx.NamedArgs{"org_id": orgID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active previews by org: %w", err)
	}
	return count, nil
}

// CountActivePreviewsByUser counts active previews for per-user cap enforcement.
func (s *PreviewStore) CountActivePreviewsByUser(ctx context.Context, orgID, userID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM preview_instances
		 WHERE org_id = @org_id AND user_id = @user_id
		 AND status IN %s`, activeStatusFilter),
		pgx.NamedArgs{"org_id": orgID, "user_id": userID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active previews by user: %w", err)
	}
	return count, nil
}

// CountActivePreviewsByWorker counts active previews on a worker node.
// lint:allow-no-orgid reason="cross-org worker capacity stats for scheduling"
func (s *PreviewStore) CountActivePreviewsByWorker(ctx context.Context, workerNodeID string) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM preview_instances WHERE worker_node_id = @worker
		 AND status IN %s`, activeStatusFilter),
		pgx.NamedArgs{"worker": workerNodeID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active previews by worker: %w", err)
	}
	return count, nil
}

// ListExpiredPreviews returns active previews whose hard TTL has passed.
// lint:allow-no-orgid reason="cross-org cleanup scan for expired previews"
func (s *PreviewStore) ListExpiredPreviews(ctx context.Context, cutoff time.Time) ([]models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE status IN %s
		AND expires_at < @cutoff
		ORDER BY expires_at ASC`, previewInstanceColumns, activeStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"cutoff": cutoff})
	if err != nil {
		return nil, fmt.Errorf("query expired previews: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInstance])
}

// ListExpiredPreviewsForWorker returns active previews owned by the given worker
// whose hard TTL has passed.
// lint:allow-no-orgid reason="worker-scoped cleanup scan across orgs"
func (s *PreviewStore) ListExpiredPreviewsForWorker(ctx context.Context, workerNodeID string, cutoff time.Time) ([]models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE status IN %s
		AND worker_node_id = @worker_node_id
		AND expires_at < @cutoff
		ORDER BY expires_at ASC`, previewInstanceColumns, activeStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"worker_node_id": workerNodeID, "cutoff": cutoff})
	if err != nil {
		return nil, fmt.Errorf("query expired previews by worker: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInstance])
}

// ListIdlePreviews returns active previews with no activity since the cutoff.
//
// Previews whose session currently has an active agent turn
// (sessions.turn_holding_container = TRUE) are excluded. While a user is
// actively iterating — e.g. the agent is editing files and the preview is
// hot-reloading — we do not want the idle sweeper to reap the preview out
// from under them; turn activity is the user's implicit heartbeat.
// lint:allow-no-orgid reason="cross-org cleanup scan for idle previews"
func (s *PreviewStore) ListIdlePreviews(ctx context.Context, idleSince time.Time) ([]models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE status IN %s
		AND last_accessed_at < @idle_since
		AND NOT EXISTS (
			SELECT 1 FROM sessions s
			WHERE s.id = preview_instances.session_id
			  AND s.org_id = preview_instances.org_id
			  AND s.turn_holding_container = TRUE
		)
		ORDER BY last_accessed_at ASC`, previewInstanceColumns, activeStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"idle_since": idleSince})
	if err != nil {
		return nil, fmt.Errorf("query idle previews: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInstance])
}

// ListIdlePreviewsForWorker returns active previews owned by the given worker
// with no activity since the cutoff.
//
// Previews whose session currently has an active agent turn
// (sessions.turn_holding_container = TRUE) are excluded. While a user is
// actively iterating — e.g. the agent is editing files and the preview is
// hot-reloading — we do not want the idle sweeper to reap the preview out
// from under them; turn activity is the user's implicit heartbeat.
// lint:allow-no-orgid reason="worker-scoped cleanup scan across orgs"
func (s *PreviewStore) ListIdlePreviewsForWorker(ctx context.Context, workerNodeID string, idleSince time.Time) ([]models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE status IN %s
		AND worker_node_id = @worker_node_id
		AND last_accessed_at < @idle_since
		AND NOT EXISTS (
			SELECT 1 FROM sessions s
			WHERE s.id = preview_instances.session_id
			  AND s.org_id = preview_instances.org_id
			  AND s.turn_holding_container = TRUE
		)
		ORDER BY last_accessed_at ASC`, previewInstanceColumns, activeStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"worker_node_id": workerNodeID, "idle_since": idleSince})
	if err != nil {
		return nil, fmt.Errorf("query idle previews by worker: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInstance])
}

// =============================================================================
// Preview Services
// =============================================================================

// CreatePreviewService inserts a new preview service record.
// lint:allow-no-orgid reason="preview_services is a child of preview_instances and is scoped via preview_instance_id FK; the parent row carries org_id"
func (s *PreviewStore) CreatePreviewService(ctx context.Context, svc *models.PreviewService) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_services (
			preview_instance_id, service_name, role, status, command, cwd, port
		) VALUES (
			@preview_instance_id, @service_name, @role, @status, @command, @cwd, @port
		)
		ON CONFLICT (preview_instance_id, service_name) DO UPDATE SET
			role = EXCLUDED.role,
			status = EXCLUDED.status,
			command = EXCLUDED.command,
			cwd = EXCLUDED.cwd,
			port = EXCLUDED.port,
			pid = NULL,
			error = ''
		RETURNING %s`, previewServiceColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"preview_instance_id": svc.PreviewInstanceID,
		"service_name":        svc.ServiceName,
		"role":                svc.Role,
		"status":              svc.Status,
		"command":             svc.Command,
		"cwd":                 svc.Cwd,
		"port":                svc.Port,
	})
	if err != nil {
		return fmt.Errorf("insert preview service: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewService])
	if err != nil {
		return fmt.Errorf("scan preview service: %w", err)
	}
	*svc = row
	return nil
}

// UpdateServiceStatus updates a service's status and optional error, scoped through org.
func (s *PreviewStore) UpdateServiceStatus(ctx context.Context, orgID, previewInstanceID uuid.UUID, serviceName string, status models.PreviewServiceStatus, errMsg string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_services SET status = @status, error = @error
		 WHERE preview_instance_id = @pid AND service_name = @name
		 AND preview_instance_id IN (SELECT id FROM preview_instances WHERE id = @pid AND org_id = @org_id)`,
		pgx.NamedArgs{"pid": previewInstanceID, "name": serviceName, "status": status, "error": errMsg, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("update service status: %w", err)
	}
	return nil
}

// UpdateServicePID records the OS process ID for a service, scoped through org.
func (s *PreviewStore) UpdateServicePID(ctx context.Context, orgID, previewInstanceID uuid.UUID, serviceName string, pid int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_services SET pid = @pid
		 WHERE preview_instance_id = @instance_id AND service_name = @name
		 AND preview_instance_id IN (SELECT id FROM preview_instances WHERE id = @instance_id AND org_id = @org_id)`,
		pgx.NamedArgs{"instance_id": previewInstanceID, "name": serviceName, "pid": pid, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("update service pid: %w", err)
	}
	return nil
}

// ListServicesByPreview returns all services for a preview instance, scoped through org.
func (s *PreviewStore) ListServicesByPreview(ctx context.Context, orgID, previewInstanceID uuid.UUID) ([]models.PreviewService, error) {
	query := `SELECT ps.id, ps.preview_instance_id, ps.service_name, ps.role, ps.status,
		ps.command, ps.cwd, ps.port, ps.pid, ps.error, ps.created_at
		FROM preview_services ps
		JOIN preview_instances pi ON pi.id = ps.preview_instance_id
		WHERE ps.preview_instance_id = @pid AND pi.org_id = @org_id
		ORDER BY ps.created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list preview services: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewService])
}

// =============================================================================
// Preview Infrastructure
// =============================================================================

// CreatePreviewInfrastructure inserts a new infrastructure record.
// lint:allow-no-orgid reason="preview_infrastructure is a child of preview_instances and is scoped via preview_instance_id FK; the parent row carries org_id"
func (s *PreviewStore) CreatePreviewInfrastructure(ctx context.Context, infra *models.PreviewInfrastructure) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_infrastructure (
			preview_instance_id, infra_name, template, container_id, status,
			host, port, credentials_hash
		) VALUES (
			@preview_instance_id, @infra_name, @template, @container_id, @status,
			@host, @port, @credentials_hash
		)
		ON CONFLICT (preview_instance_id, infra_name) DO UPDATE SET
			template = EXCLUDED.template,
			container_id = EXCLUDED.container_id,
			status = EXCLUDED.status,
			host = EXCLUDED.host,
			port = EXCLUDED.port,
			credentials_hash = EXCLUDED.credentials_hash,
			error = ''
		RETURNING %s`, previewInfraColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"preview_instance_id": infra.PreviewInstanceID,
		"infra_name":          infra.InfraName,
		"template":            infra.Template,
		"container_id":        infra.ContainerID,
		"status":              infra.Status,
		"host":                infra.Host,
		"port":                infra.Port,
		"credentials_hash":    infra.CredentialsHash,
	})
	if err != nil {
		return fmt.Errorf("insert preview infrastructure: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInfrastructure])
	if err != nil {
		return fmt.Errorf("scan preview infrastructure: %w", err)
	}
	*infra = row
	return nil
}

// UpdateInfraStatus updates an infrastructure service's status, scoped through org.
func (s *PreviewStore) UpdateInfraStatus(ctx context.Context, orgID, previewInstanceID uuid.UUID, infraName string, status models.PreviewInfraStatus, errMsg string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_infrastructure SET status = @status, error = @error
		 WHERE preview_instance_id = @pid AND infra_name = @name
		 AND preview_instance_id IN (SELECT id FROM preview_instances WHERE id = @pid AND org_id = @org_id)`,
		pgx.NamedArgs{"pid": previewInstanceID, "name": infraName, "status": status, "error": errMsg, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("update infra status: %w", err)
	}
	return nil
}

// ListInfraByPreview returns all infrastructure for a preview instance, scoped through org.
func (s *PreviewStore) ListInfraByPreview(ctx context.Context, orgID, previewInstanceID uuid.UUID) ([]models.PreviewInfrastructure, error) {
	query := `SELECT pi2.id, pi2.preview_instance_id, pi2.infra_name, pi2.template,
		pi2.container_id, pi2.status, pi2.host, pi2.port, pi2.credentials_hash, pi2.error, pi2.created_at
		FROM preview_infrastructure pi2
		JOIN preview_instances pi ON pi.id = pi2.preview_instance_id
		WHERE pi2.preview_instance_id = @pid AND pi.org_id = @org_id
		ORDER BY pi2.created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list preview infrastructure: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewInfrastructure])
}

// =============================================================================
// Preview Snapshots
// =============================================================================

// CreateSnapshot inserts a new screenshot snapshot.
// lint:allow-no-orgid reason="preview_snapshots is a child of preview_instances and is scoped via preview_instance_id FK; the parent row carries org_id"
func (s *PreviewStore) CreateSnapshot(ctx context.Context, snap *models.PreviewSnapshot) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_snapshots (
			preview_instance_id, trigger, url_path, blob_ref,
			viewport_width, viewport_height, console_errors, file_changes
		) VALUES (
			@preview_instance_id, @trigger, @url_path, @blob_ref,
			@viewport_width, @viewport_height, @console_errors, @file_changes
		) RETURNING %s`, previewSnapshotColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"preview_instance_id": snap.PreviewInstanceID,
		"trigger":             snap.Trigger,
		"url_path":            snap.URLPath,
		"blob_ref":            snap.BlobRef,
		"viewport_width":      snap.ViewportWidth,
		"viewport_height":     snap.ViewportHeight,
		"console_errors":      snap.ConsoleErrors,
		"file_changes":        snap.FileChanges,
	})
	if err != nil {
		return fmt.Errorf("insert preview snapshot: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewSnapshot])
	if err != nil {
		return fmt.Errorf("scan preview snapshot: %w", err)
	}
	*snap = row
	return nil
}

// ListSnapshotsByPreview returns the screenshot timeline for a preview, scoped through org.
func (s *PreviewStore) ListSnapshotsByPreview(ctx context.Context, orgID, previewInstanceID uuid.UUID) ([]models.PreviewSnapshot, error) {
	query := `SELECT ps.id, ps.preview_instance_id, ps.trigger, ps.url_path, ps.blob_ref,
		ps.viewport_width, ps.viewport_height, ps.console_errors, ps.file_changes, ps.created_at
		FROM preview_snapshots ps
		JOIN preview_instances pi ON pi.id = ps.preview_instance_id
		WHERE ps.preview_instance_id = @pid AND pi.org_id = @org_id
		ORDER BY ps.created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list preview snapshots: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewSnapshot])
}

// CountSnapshotsByPreview returns the snapshot count for limit enforcement, scoped through org.
func (s *PreviewStore) CountSnapshotsByPreview(ctx context.Context, orgID, previewInstanceID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM preview_snapshots ps
		 JOIN preview_instances pi ON pi.id = ps.preview_instance_id
		 WHERE ps.preview_instance_id = @pid AND pi.org_id = @org_id`,
		pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count snapshots: %w", err)
	}
	return count, nil
}

// DeleteOldestSnapshots removes the oldest snapshots beyond a keep limit, scoped through org.
// It returns the blob_ref paths of the deleted snapshots so callers can clean up
// the corresponding files on disk.
func (s *PreviewStore) DeleteOldestSnapshots(ctx context.Context, orgID, previewInstanceID uuid.UUID, keepCount int) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`DELETE FROM preview_snapshots WHERE id IN (
			SELECT ps.id FROM preview_snapshots ps
			JOIN preview_instances pi ON pi.id = ps.preview_instance_id
			WHERE ps.preview_instance_id = @pid AND pi.org_id = @org_id
			ORDER BY ps.created_at DESC
			OFFSET @keep
		) RETURNING blob_ref`,
		pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID, "keep": keepCount},
	)
	if err != nil {
		return nil, fmt.Errorf("delete oldest snapshots: %w", err)
	}
	defer rows.Close()

	var blobRefs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return blobRefs, fmt.Errorf("scan blob_ref: %w", err)
		}
		if ref != "" {
			blobRefs = append(blobRefs, ref)
		}
	}
	return blobRefs, rows.Err()
}

// =============================================================================
// Preview Logs
// =============================================================================

// CreatePreviewLog inserts a new preview log entry.
func (s *PreviewStore) CreatePreviewLog(ctx context.Context, log *models.PreviewLog) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_logs (preview_instance_id, org_id, level, step, message, metadata)
		VALUES (@preview_instance_id, @org_id, @level, @step, @message, @metadata)
		RETURNING %s`, previewLogColumns)

	// metadata is NOT NULL in Postgres with a DEFAULT '{}'. Passing a nil
	// or empty json.RawMessage binds explicit NULL — which trips the
	// not-null constraint because the DEFAULT only applies when the column
	// is omitted from the INSERT. OnServiceFailed callers in particular
	// pass an unset Metadata, and that silently dropped service-exit logs
	// in production. Treat a zero-length value (nil slice or empty slice)
	// as "use the column default".
	metadata := log.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"preview_instance_id": log.PreviewInstanceID,
		"org_id":              log.OrgID,
		"level":               log.Level,
		"step":                log.Step,
		"message":             log.Message,
		"metadata":            metadata,
	})
	if err != nil {
		return fmt.Errorf("insert preview log: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewLog])
	if err != nil {
		return fmt.Errorf("scan preview log: %w", err)
	}
	*log = row
	return nil
}

// ListLogsByPreview returns logs for a preview, supporting cursor-based pagination.
// Uses (created_at, id) for stable cursor ordering to avoid missing entries with
// identical timestamps. Results are capped at 200 per page.
func (s *PreviewStore) ListLogsByPreview(ctx context.Context, orgID, previewInstanceID uuid.UUID, afterID *uuid.UUID) ([]models.PreviewLog, error) {
	var query string
	args := pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID}

	if afterID != nil {
		query = fmt.Sprintf(`SELECT %s FROM preview_logs
			WHERE preview_instance_id = @pid AND org_id = @org_id
			AND (created_at, id) > (
				SELECT created_at, id FROM preview_logs WHERE id = @after_id
			)
			ORDER BY created_at ASC, id ASC
			LIMIT 200`, previewLogColumns)
		args["after_id"] = *afterID
	} else {
		query = fmt.Sprintf(`SELECT %s FROM preview_logs
			WHERE preview_instance_id = @pid AND org_id = @org_id
			ORDER BY created_at ASC, id ASC
			LIMIT 200`, previewLogColumns)
	}

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("list preview logs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewLog])
}

// ListLatestLogsByPreview returns the latest preview logs in chronological
// order for display as a bounded tail.
func (s *PreviewStore) ListLatestLogsByPreview(ctx context.Context, orgID, previewInstanceID uuid.UUID) ([]models.PreviewLog, error) {
	query := fmt.Sprintf(`SELECT %s FROM (
			SELECT %s FROM preview_logs
			WHERE preview_instance_id = @pid AND org_id = @org_id
			ORDER BY created_at DESC, id DESC
			LIMIT 200
		) latest
		ORDER BY created_at ASC, id ASC`, previewLogColumns, previewLogColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list latest preview logs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewLog])
}

// =============================================================================
// Preview Access Sessions
// =============================================================================

// CreateAccessSession inserts a new preview access session.
func (s *PreviewStore) CreateAccessSession(ctx context.Context, sess *models.PreviewAccessSession) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_access_sessions (
			org_id, user_id, preview_instance_id,
			session_token_hash, expires_at
		) VALUES (
			@org_id, @user_id, @preview_instance_id,
			@session_token_hash, @expires_at
		) RETURNING %s`, previewAccessSessionColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":              sess.OrgID,
		"user_id":             sess.UserID,
		"preview_instance_id": sess.PreviewInstanceID,
		"session_token_hash":  sess.SessionTokenHash,
		"expires_at":          sess.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("insert preview access session: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewAccessSession])
	if err != nil {
		return fmt.Errorf("scan preview access session: %w", err)
	}
	*sess = row
	return nil
}

// GetAccessSessionByToken looks up an access session by token hash, scoped to org.
func (s *PreviewStore) GetAccessSessionByToken(ctx context.Context, orgID uuid.UUID, tokenHash string) (*models.PreviewAccessSession, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_access_sessions
		WHERE org_id = @org_id AND session_token_hash = @hash`, previewAccessSessionColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "hash": tokenHash})
	if err != nil {
		return nil, fmt.Errorf("query access session: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewAccessSession])
	if err != nil {
		return nil, fmt.Errorf("get access session: %w", err)
	}
	return &row, nil
}

// GetAccessSessionByTokenUnscoped looks up an access session by token hash
// without org scoping. Used by the preview gateway during bootstrap exchange,
// where the org is not yet known. Safe because token hashes are derived from
// 32 cryptographically random bytes.
// lint:allow-no-orgid reason="pre-auth gateway bootstrap; token hash is opaque and identifies the org"
func (s *PreviewStore) GetAccessSessionByTokenUnscoped(ctx context.Context, tokenHash string) (*models.PreviewAccessSession, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_access_sessions
		WHERE session_token_hash = @hash`, previewAccessSessionColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"hash": tokenHash})
	if err != nil {
		return nil, fmt.Errorf("query access session: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewAccessSession])
	if err != nil {
		return nil, fmt.Errorf("get access session: %w", err)
	}
	return &row, nil
}

// GetAccessSessionByID retrieves an access session by ID, scoped to org.
func (s *PreviewStore) GetAccessSessionByID(ctx context.Context, orgID, id uuid.UUID) (*models.PreviewAccessSession, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_access_sessions
		WHERE id = @id AND org_id = @org_id`, previewAccessSessionColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query access session: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewAccessSession])
	if err != nil {
		return nil, fmt.Errorf("get access session by id: %w", err)
	}
	return &row, nil
}

// RevokeAccessSession revokes a single access session, scoped to org.
func (s *PreviewStore) RevokeAccessSession(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_access_sessions SET revoked_at = now() WHERE id = @id AND org_id = @org_id AND revoked_at IS NULL`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("revoke access session: %w", err)
	}
	return nil
}

// RevokeAllForPreview revokes all access sessions for a preview instance, scoped through org.
func (s *PreviewStore) RevokeAllForPreview(ctx context.Context, orgID, previewInstanceID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_access_sessions SET revoked_at = now()
		 WHERE preview_instance_id = @pid AND org_id = @org_id AND revoked_at IS NULL`,
		pgx.NamedArgs{"pid": previewInstanceID, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("revoke all access sessions for preview: %w", err)
	}
	return nil
}

// UpdateAccessSessionActivity updates the last_accessed_at for an access session, scoped to org.
func (s *PreviewStore) UpdateAccessSessionActivity(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_access_sessions SET last_accessed_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("update access session activity: %w", err)
	}
	return nil
}

// ErrSessionRevoked is returned when an extend operation targets a revoked session.
var ErrSessionRevoked = fmt.Errorf("access session has been revoked")

// ExtendAccessSessionExpiry extends the expires_at for an active access session.
// Returns ErrSessionRevoked if the session was revoked (0 rows affected).
func (s *PreviewStore) ExtendAccessSessionExpiry(ctx context.Context, orgID, id uuid.UUID, newExpiry time.Time) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE preview_access_sessions SET expires_at = @expires_at
		 WHERE id = @id AND org_id = @org_id AND revoked_at IS NULL`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "expires_at": newExpiry},
	)
	if err != nil {
		return fmt.Errorf("extend access session expiry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionRevoked
	}
	return nil
}

// =============================================================================
// Preview Startup Cache
// =============================================================================

// UpsertStartupCache creates or updates a startup cache entry.
func (s *PreviewStore) UpsertStartupCache(ctx context.Context, entry *models.PreviewStartupCache) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_startup_cache (
			org_id, repo_id, snapshot_key, blob_path, size_bytes, worker_node_id
		) VALUES (
			@org_id, @repo_id, @snapshot_key, @blob_path, @size_bytes, @worker_node_id
		)
		ON CONFLICT (org_id, repo_id, snapshot_key, worker_node_id)
		DO UPDATE SET blob_path = EXCLUDED.blob_path, size_bytes = EXCLUDED.size_bytes,
			last_used_at = now()
		RETURNING %s`, previewStartupCacheColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":         entry.OrgID,
		"repo_id":        entry.RepoID,
		"snapshot_key":   entry.SnapshotKey,
		"blob_path":      entry.BlobPath,
		"size_bytes":     entry.SizeBytes,
		"worker_node_id": entry.WorkerNodeID,
	})
	if err != nil {
		return fmt.Errorf("upsert startup cache: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewStartupCache])
	if err != nil {
		return fmt.Errorf("scan startup cache: %w", err)
	}
	*entry = row
	return nil
}

// FindMatchingCache looks up a startup cache entry by snapshot key.
func (s *PreviewStore) FindMatchingCache(ctx context.Context, orgID, repoID uuid.UUID, snapshotKey, workerNodeID string) (*models.PreviewStartupCache, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_startup_cache
		WHERE org_id = @org_id AND repo_id = @repo_id AND snapshot_key = @key AND worker_node_id = @worker_node_id`, previewStartupCacheColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID, "repo_id": repoID, "key": snapshotKey, "worker_node_id": workerNodeID,
	})
	if err != nil {
		return nil, fmt.Errorf("query startup cache: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewStartupCache])
	if err != nil {
		return nil, fmt.Errorf("get startup cache: %w", err)
	}
	return &row, nil
}

// TouchCache updates the last_used_at timestamp, scoped to org.
func (s *PreviewStore) TouchCache(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_startup_cache SET last_used_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("touch cache: %w", err)
	}
	return nil
}

// ListCacheByWorker returns cache entries for a worker, ordered by last_used_at
// for LRU eviction.
// lint:allow-no-orgid reason="cross-org worker LRU eviction scan"
func (s *PreviewStore) ListCacheByWorker(ctx context.Context, workerNodeID string) ([]models.PreviewStartupCache, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_startup_cache
		WHERE worker_node_id = @worker ORDER BY last_used_at ASC`, previewStartupCacheColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"worker": workerNodeID})
	if err != nil {
		return nil, fmt.Errorf("list cache by worker: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewStartupCache])
}

// DeleteCache removes a cache entry by ID, scoped to org.
func (s *PreviewStore) DeleteCache(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM preview_startup_cache WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("delete cache: %w", err)
	}
	return nil
}

// =============================================================================
// PR Preview State
// =============================================================================

// UpsertPRPreviewState creates or updates PR preview state.
func (s *PreviewStore) UpsertPRPreviewState(ctx context.Context, state *models.PRPreviewState) error {
	query := fmt.Sprintf(`
		INSERT INTO pr_preview_state (
			org_id, repo_id, pr_number, github_comment_id,
			last_preview_instance_id, last_screenshot_blob_path,
			last_visual_diff_blob_path, base_snapshot_key, status
		) VALUES (
			@org_id, @repo_id, @pr_number, @github_comment_id,
			@last_preview_instance_id, @last_screenshot_blob_path,
			@last_visual_diff_blob_path, @base_snapshot_key, @status
		)
		ON CONFLICT (org_id, repo_id, pr_number) DO UPDATE SET
			github_comment_id = COALESCE(EXCLUDED.github_comment_id, pr_preview_state.github_comment_id),
			last_preview_instance_id = EXCLUDED.last_preview_instance_id,
			last_screenshot_blob_path = EXCLUDED.last_screenshot_blob_path,
			last_visual_diff_blob_path = EXCLUDED.last_visual_diff_blob_path,
			base_snapshot_key = EXCLUDED.base_snapshot_key,
			status = EXCLUDED.status,
			updated_at = now()
		RETURNING %s`, prPreviewStateColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                     state.OrgID,
		"repo_id":                    state.RepoID,
		"pr_number":                  state.PRNumber,
		"github_comment_id":          state.GitHubCommentID,
		"last_preview_instance_id":   state.LastPreviewInstanceID,
		"last_screenshot_blob_path":  state.LastScreenshotBlobPath,
		"last_visual_diff_blob_path": state.LastVisualDiffBlobPath,
		"base_snapshot_key":          state.BaseSnapshotKey,
		"status":                     state.Status,
	})
	if err != nil {
		return fmt.Errorf("upsert pr preview state: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PRPreviewState])
	if err != nil {
		return fmt.Errorf("scan pr preview state: %w", err)
	}
	*state = row
	return nil
}

// GetPRPreviewState returns the PR preview state for an org/repo/PR.
func (s *PreviewStore) GetPRPreviewState(ctx context.Context, orgID, repoID uuid.UUID, prNumber int) (*models.PRPreviewState, error) {
	query := fmt.Sprintf(`SELECT %s FROM pr_preview_state
		WHERE org_id = @org_id AND repo_id = @repo_id AND pr_number = @pr_number`, prPreviewStateColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID, "repo_id": repoID, "pr_number": prNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("query pr preview state: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PRPreviewState])
	if err != nil {
		return nil, fmt.Errorf("get pr preview state: %w", err)
	}
	return &row, nil
}

// UpdatePRPreviewStatus updates just the status field of a PR preview state, scoped to org.
func (s *PreviewStore) UpdatePRPreviewStatus(ctx context.Context, orgID, id uuid.UUID, status models.PRPreviewStatus) error {
	_, err := s.db.Exec(ctx,
		`UPDATE pr_preview_state SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "status": status},
	)
	if err != nil {
		return fmt.Errorf("update pr preview status: %w", err)
	}
	return nil
}

// =============================================================================
// Transactional operations
// =============================================================================

// StopPreviewWithRevocation atomically stops a preview and revokes all its
// access sessions in a single transaction.
func (s *PreviewStore) StopPreviewWithRevocation(ctx context.Context, orgID, previewID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := s.WithTx(tx)

	if err := txStore.StopPreview(ctx, orgID, previewID); err != nil {
		return err
	}
	if err := txStore.RevokeAllForPreview(ctx, orgID, previewID); err != nil {
		return fmt.Errorf("revoke access sessions: %w", err)
	}

	return tx.Commit(ctx)
}
