package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// activeRuntimeStatusFilter is the SQL IN clause for live runtime attachments.
// Keep in sync with models.PreviewRuntimeStatus.IsActive().
const activeRuntimeStatusFilter = `('starting', 'ready', 'draining')`

// terminalStatusFilter is the SQL IN clause for preview statuses that can be
// shown as the most recent preview history when no active preview exists.
const terminalStatusFilter = `('stopped', 'expired', 'failed', 'unavailable')`

// --- Column lists ---

const previewInstanceColumns = `id, COALESCE(session_id, '00000000-0000-0000-0000-000000000000'::uuid) AS session_id, preview_target_id, org_id, user_id, profile_name, name, status,
	provider, worker_node_id, preview_handle, primary_service, port,
	config_digest, base_commit_sha, last_accessed_at, expires_at, stopped_at,
	last_path, memory_limit_mb, cpu_limit_millis, disk_limit_mb, recycle_config, recycle_sandbox,
	current_phase, request_id, error, created_at, updated_at, recycled_at, recycle_scheduled_at,
	source_workspace_revision, source_workspace_revision_updated_at, unavailable_reason, preview_holding_container`

const previewRuntimeColumns = `id, org_id, preview_instance_id, runtime_epoch, worker_node_id,
	endpoint_url, preview_handle, primary_port, status, lease_expires_at,
	last_heartbeat_at, drain_requested_at, stopped_at, error, unavailable_reason, created_at, updated_at`

const previewTargetColumns = `id, org_id, repository_id, branch, commit_sha,
	preview_config_name, resolved_config_digest, source_type, source_id, source_url,
	created_by_user_id, request_id, created_at`

const previewLinkColumns = `id, org_id, preview_target_id, link_type, slug,
	repository_id, pr_number, created_at, updated_at`

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

const previewDependencyCacheColumns = `id, org_id, repo_id, cache_key, placement_key,
	blob_key, size_bytes, metadata, last_used_at, created_at`

const previewDependencyCacheLocationColumns = `id, org_id, repo_id, cache_key, placement_key,
	worker_node_id, size_bytes, last_used_at, created_at`

const prPreviewStateColumns = `id, org_id, repo_id, pr_number, github_comment_id,
	last_preview_instance_id, last_screenshot_blob_path, last_visual_diff_blob_path,
	base_snapshot_key, status, created_at, updated_at`

const branchPreviewSummaryColumns = `target.id AS target_id, active.id AS preview_id,
	target.repository_id, repo.full_name AS repository_full_name, target.branch, target.commit_sha, target.preview_config_name,
	target.source_type, target.source_id, target.source_url,
	COALESCE(active.status, 'target_created') AS status,
	target.created_at, active.expires_at`

// =============================================================================
// Preview Instance CRUD
// =============================================================================

// CreatePreviewTarget inserts a branch preview target.
//
// On conflict with an existing (branch, commit, config_name) tuple the row is
// updated in-place, but source_type/source_id are intentionally left unchanged
// so that the original provenance (e.g. the PR that first created the target)
// is never silently overwritten by a later manual or API trigger.
//
// On a concurrent race where a parallel request has already committed a target
// for the same source_id (idx_preview_targets_source_unique), we re-fetch that
// winner and return it, giving the caller an idempotent result.
func (s *PreviewStore) CreatePreviewTarget(ctx context.Context, target *models.PreviewTarget) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_targets (
			org_id, repository_id, branch, commit_sha, preview_config_name,
			resolved_config_digest, source_type, source_id, source_url, created_by_user_id, request_id
		) VALUES (
			@org_id, @repository_id, @branch, @commit_sha, @preview_config_name,
			@resolved_config_digest, @source_type, @source_id, @source_url, @created_by_user_id, @request_id
		)
		ON CONFLICT (org_id, repository_id, branch, commit_sha, preview_config_name)
		DO UPDATE SET
			source_url = EXCLUDED.source_url,
			request_id = EXCLUDED.request_id
		RETURNING %s`, previewTargetColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                 target.OrgID,
		"repository_id":          target.RepositoryID,
		"branch":                 target.Branch,
		"commit_sha":             target.CommitSHA,
		"preview_config_name":    target.PreviewConfigName,
		"resolved_config_digest": target.ResolvedConfigDigest,
		"source_type":            target.SourceType,
		"source_id":              target.SourceID,
		"source_url":             target.SourceURL,
		"created_by_user_id":     target.CreatedByUserID,
		"request_id":             target.RequestID,
	})
	if err != nil {
		return fmt.Errorf("insert preview target: %w", err)
	}
	row, scanErr := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewTarget])
	if scanErr != nil {
		// A concurrent request raced us on the source unique index. Re-fetch
		// the winning row so the caller gets an idempotent result.
		var pgErr *pgconn.PgError
		if target.SourceID != "" && errors.As(scanErr, &pgErr) &&
			pgErr.Code == "23505" && pgErr.ConstraintName == "idx_preview_targets_source_unique" {
			existing, fetchErr := s.GetPreviewTargetBySource(ctx, target.OrgID, target.SourceType, target.SourceID)
			if fetchErr != nil {
				return fmt.Errorf("insert preview target (concurrent source race): %w", fetchErr)
			}
			*target = *existing
			return nil
		}
		return fmt.Errorf("scan preview target: %w", scanErr)
	}
	*target = row
	return nil
}

// GetPreviewTarget returns a preview target by ID, scoped to org.
func (s *PreviewStore) GetPreviewTarget(ctx context.Context, orgID, id uuid.UUID) (*models.PreviewTarget, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_targets WHERE id = @id AND org_id = @org_id`, previewTargetColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query preview target: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewTarget])
	if err != nil {
		return nil, fmt.Errorf("get preview target: %w", err)
	}
	return &row, nil
}

// GetPreviewTargetByIdempotencyKey returns the target associated with a
// caller-provided idempotency key.
func (s *PreviewStore) GetPreviewTargetByIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (*models.PreviewTarget, error) {
	query := fmt.Sprintf(`SELECT %s
		FROM preview_targets target
		JOIN preview_idempotency_keys idem ON idem.preview_target_id = target.id
		WHERE idem.org_id = @org_id AND idem.idempotency_key = @key`, previewTargetColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "key": key})
	if err != nil {
		return nil, fmt.Errorf("query preview target idempotency key: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewTarget])
	if err != nil {
		return nil, fmt.Errorf("get preview target by idempotency key: %w", err)
	}
	return &row, nil
}

// GetPreviewTargetBySource returns the newest target for caller source
// metadata. This gives API/automation callers a natural idempotency key even
// when they cannot preserve an HTTP Idempotency-Key across retries.
func (s *PreviewStore) GetPreviewTargetBySource(ctx context.Context, orgID uuid.UUID, sourceType models.PreviewSourceType, sourceID string) (*models.PreviewTarget, error) {
	query := fmt.Sprintf(`SELECT %s
		FROM preview_targets
		WHERE org_id = @org_id AND source_type = @source_type AND source_id = @source_id
		ORDER BY created_at DESC
		LIMIT 1`, previewTargetColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "source_type": sourceType, "source_id": sourceID})
	if err != nil {
		return nil, fmt.Errorf("query preview target by source: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewTarget])
	if err != nil {
		return nil, fmt.Errorf("get preview target by source: %w", err)
	}
	return &row, nil
}

// UpsertPreviewIdempotencyKey records a stable caller key for a target.
func (s *PreviewStore) UpsertPreviewIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string, targetID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `INSERT INTO preview_idempotency_keys (org_id, idempotency_key, preview_target_id)
		VALUES (@org_id, @key, @target_id)
		ON CONFLICT (org_id, idempotency_key) DO UPDATE SET preview_target_id = EXCLUDED.preview_target_id`,
		pgx.NamedArgs{"org_id": orgID, "key": key, "target_id": targetID})
	if err != nil {
		return fmt.Errorf("upsert preview idempotency key: %w", err)
	}
	return nil
}

// UpdatePreviewTargetConfigDigest stores the digest of the resolved committed
// config once the worker has checked out the branch and parsed .143/config.json.
func (s *PreviewStore) UpdatePreviewTargetConfigDigest(ctx context.Context, orgID, targetID uuid.UUID, digest string) error {
	tag, err := s.db.Exec(ctx, `UPDATE preview_targets
		SET resolved_config_digest = @digest
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID, "digest": digest})
	if err != nil {
		return fmt.Errorf("update preview target config digest: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview target not found")
	}
	return nil
}

// ListBranchPreviewSummaries returns recent preview targets with their latest
// active runtime when one exists.
func (s *PreviewStore) ListBranchPreviewSummaries(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID, branch, status string, limit int) ([]models.BranchPreviewSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := fmt.Sprintf(`SELECT %s
		FROM preview_targets target
		JOIN repositories repo ON repo.id = target.repository_id AND repo.org_id = target.org_id
		LEFT JOIN LATERAL (
			SELECT id, status, expires_at
			FROM preview_instances
			WHERE org_id = target.org_id
			  AND preview_target_id = target.id
			ORDER BY created_at DESC
			LIMIT 1
		) active ON TRUE
		WHERE target.org_id = @org_id
		  AND (@repository_id::uuid IS NULL OR target.repository_id = @repository_id)
		  AND (@branch = '' OR target.branch = @branch)
		  AND (@status = '' OR COALESCE(active.status, 'target_created') = @status)
		ORDER BY target.created_at DESC
		LIMIT @limit`, branchPreviewSummaryColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
		"branch":        branch,
		"status":        status,
		"limit":         limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list branch preview summaries: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.BranchPreviewSummary])
}

// GetLatestPreviewTargetForBranch returns the newest target for a repository
// branch/config tuple, regardless of commit. It is used by durable PR links to
// find stale existing previews before deciding whether to start the latest head.
func (s *PreviewStore) GetLatestPreviewTargetForBranch(ctx context.Context, orgID, repositoryID uuid.UUID, branch, previewConfigName string) (*models.PreviewTarget, error) {
	query := fmt.Sprintf(`SELECT %s
		FROM preview_targets
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND branch = @branch
		  AND preview_config_name = @preview_config_name
		ORDER BY created_at DESC
		LIMIT 1`, previewTargetColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":              orgID,
		"repository_id":       repositoryID,
		"branch":              branch,
		"preview_config_name": previewConfigName,
	})
	if err != nil {
		return nil, fmt.Errorf("query latest preview target for branch: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewTarget])
	if err != nil {
		return nil, fmt.Errorf("get latest preview target for branch: %w", err)
	}
	return &row, nil
}

// GetActivePreviewForTarget returns the currently active runtime for a branch
// target, if one exists.
func (s *PreviewStore) GetActivePreviewForTarget(ctx context.Context, orgID, targetID uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE org_id = @org_id AND preview_target_id = @preview_target_id
		AND status IN %s
		ORDER BY created_at DESC
		LIMIT 1`, previewInstanceColumns, activeStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":            orgID,
		"preview_target_id": targetID,
	})
	if err != nil {
		return nil, fmt.Errorf("query active preview for target: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("get active preview for target: %w", err)
	}
	return &row, nil
}

// GetLatestPreviewForTarget returns the active runtime, or newest runtime
// history, for a branch preview target.
func (s *PreviewStore) GetLatestPreviewForTarget(ctx context.Context, orgID, targetID uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE org_id = @org_id AND preview_target_id = @preview_target_id
		ORDER BY
			CASE WHEN status IN %s THEN 0 ELSE 1 END,
			created_at DESC
		LIMIT 1`, previewInstanceColumns, activeStatusFilter)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":            orgID,
		"preview_target_id": targetID,
	})
	if err != nil {
		return nil, fmt.Errorf("query latest preview for target: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("get latest preview for target: %w", err)
	}
	return &row, nil
}

// GetPreviewForPublicHost returns the active runtime, or newest runtime
// history, addressable by a public preview host UUID. The host UUID is either a
// runtime preview ID for legacy session previews or a stable preview target ID
// for branch/PR previews.
// lint:allow-no-orgid reason="preview gateway has no org session; the unguessable preview host UUID is the public lookup key"
func (s *PreviewStore) GetPreviewForPublicHost(ctx context.Context, hostID uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_instances
		WHERE id = @host_id OR preview_target_id = @host_id
		ORDER BY
			CASE WHEN status IN %s THEN 0 ELSE 1 END,
			created_at DESC
		LIMIT 1`, previewInstanceColumns, activeStatusFilter)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"host_id": hostID})
	if err != nil {
		return nil, fmt.Errorf("query preview public host: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("get preview public host: %w", err)
	}
	return &row, nil
}

// AttachPreviewTarget links an existing runtime attempt to a branch preview
// target. It is used when a live session sandbox already exactly matches the
// requested branch target and can be reused instead of starting a cold clone.
func (s *PreviewStore) AttachPreviewTarget(ctx context.Context, orgID, previewID, targetID uuid.UUID) (*models.PreviewInstance, error) {
	query := fmt.Sprintf(`UPDATE preview_instances
		SET preview_target_id = @target_id, updated_at = now()
		WHERE id = @preview_id AND org_id = @org_id
		RETURNING %s`, previewInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"preview_id": previewID,
		"target_id":  targetID,
	})
	if err != nil {
		return nil, fmt.Errorf("attach preview target: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return nil, fmt.Errorf("scan attached preview target: %w", err)
	}
	return &row, nil
}

// UpsertPreviewLink creates or updates a stable preview link for a target.
func (s *PreviewStore) UpsertPreviewLink(ctx context.Context, link *models.PreviewLink) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_links (
			org_id, preview_target_id, link_type, slug, repository_id, pr_number
		) VALUES (
			@org_id, @preview_target_id, @link_type, @slug, @repository_id, @pr_number
		)
		ON CONFLICT (org_id, link_type, slug)
		DO UPDATE SET
			preview_target_id = EXCLUDED.preview_target_id,
			repository_id = EXCLUDED.repository_id,
			pr_number = EXCLUDED.pr_number,
			updated_at = now()
		RETURNING %s`, previewLinkColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":            link.OrgID,
		"preview_target_id": link.PreviewTargetID,
		"link_type":         link.LinkType,
		"slug":              link.Slug,
		"repository_id":     link.RepositoryID,
		"pr_number":         link.PRNumber,
	})
	if err != nil {
		return fmt.Errorf("upsert preview link: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewLink])
	if err != nil {
		return fmt.Errorf("scan preview link: %w", err)
	}
	*link = row
	return nil
}

// GetPreviewLinkBySlug returns a stable preview link by link namespace and
// slug, scoped to org.
func (s *PreviewStore) GetPreviewLinkBySlug(ctx context.Context, orgID uuid.UUID, linkType models.PreviewLinkType, slug string) (*models.PreviewLink, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_links
		WHERE org_id = @org_id AND link_type = @link_type AND slug = @slug`, previewLinkColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "link_type": linkType, "slug": slug})
	if err != nil {
		return nil, fmt.Errorf("query preview link: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewLink])
	if err != nil {
		return nil, fmt.Errorf("get preview link: %w", err)
	}
	return &row, nil
}

// CreatePreviewInstance inserts a new preview instance.
func (s *PreviewStore) CreatePreviewInstance(ctx context.Context, p *models.PreviewInstance) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_instances (
			session_id, org_id, user_id, profile_name, name, status, provider,
			worker_node_id, preview_handle, primary_service, port,
			config_digest, base_commit_sha, expires_at,
			last_path, memory_limit_mb, cpu_limit_millis, disk_limit_mb, recycle_config, recycle_sandbox,
			current_phase, request_id, source_workspace_revision, source_workspace_revision_updated_at
		) VALUES (
			@session_id, @org_id, @user_id, @profile_name, @name, @status, @provider,
			@worker_node_id, @preview_handle, @primary_service, @port,
			@config_digest, @base_commit_sha, @expires_at,
			@last_path, @memory_limit_mb, @cpu_limit_millis, @disk_limit_mb, @recycle_config, @recycle_sandbox,
			@current_phase, @request_id, @source_workspace_revision, @source_workspace_revision_updated_at
		) RETURNING %s`, previewInstanceColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id":                           p.SessionID,
		"org_id":                               p.OrgID,
		"user_id":                              p.UserID,
		"profile_name":                         p.ProfileName,
		"name":                                 p.Name,
		"status":                               p.Status,
		"provider":                             p.Provider,
		"worker_node_id":                       p.WorkerNodeID,
		"preview_handle":                       p.PreviewHandle,
		"primary_service":                      p.PrimaryService,
		"port":                                 p.Port,
		"config_digest":                        p.ConfigDigest,
		"base_commit_sha":                      p.BaseCommitSHA,
		"expires_at":                           p.ExpiresAt,
		"last_path":                            p.LastPath,
		"memory_limit_mb":                      p.MemoryLimitMB,
		"cpu_limit_millis":                     p.CPULimitMillis,
		"disk_limit_mb":                        p.DiskLimitMB,
		"recycle_config":                       p.RecycleConfig,
		"recycle_sandbox":                      p.RecycleSandbox,
		"current_phase":                        p.CurrentPhase,
		"request_id":                           p.RequestID,
		"source_workspace_revision":            p.SourceWorkspaceRevision,
		"source_workspace_revision_updated_at": p.SourceWorkspaceRevisionUpdatedAt,
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

// CreatePreviewRuntime inserts a live worker attachment for a preview instance.
func (s *PreviewStore) CreatePreviewRuntime(ctx context.Context, r *models.PreviewRuntime) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_runtimes (
			org_id, preview_instance_id, runtime_epoch, worker_node_id, endpoint_url,
			preview_handle, primary_port, status, lease_expires_at
		) VALUES (
			@org_id, @preview_instance_id, @runtime_epoch, @worker_node_id, @endpoint_url,
			@preview_handle, @primary_port, @status, @lease_expires_at
		) RETURNING %s`, previewRuntimeColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":              r.OrgID,
		"preview_instance_id": r.PreviewInstanceID,
		"runtime_epoch":       r.RuntimeEpoch,
		"worker_node_id":      r.WorkerNodeID,
		"endpoint_url":        r.EndpointURL,
		"preview_handle":      r.PreviewHandle,
		"primary_port":        r.PrimaryPort,
		"status":              r.Status,
		"lease_expires_at":    r.LeaseExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("insert preview runtime: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewRuntime])
	if err != nil {
		return fmt.Errorf("scan preview runtime: %w", err)
	}
	*r = row
	return nil
}

// CreateNextPreviewRuntime stops existing active epochs and inserts a starting
// runtime with the next epoch in one transaction.
func (s *PreviewStore) CreateNextPreviewRuntime(ctx context.Context, orgID, previewID uuid.UUID, workerNodeID, endpointURL string) (*models.PreviewRuntime, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin next preview runtime: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := s.WithTx(tx)
	if _, err := tx.Exec(ctx,
		fmt.Sprintf(`UPDATE preview_runtimes
		 SET status = 'stopped', stopped_at = COALESCE(stopped_at, now()), updated_at = now()
		 WHERE preview_instance_id = @preview_id AND org_id = @org_id AND status IN %s`, activeRuntimeStatusFilter),
		pgx.NamedArgs{"org_id": orgID, "preview_id": previewID},
	); err != nil {
		return nil, fmt.Errorf("stop previous preview runtimes: %w", err)
	}

	var nextEpoch int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(runtime_epoch), 0) + 1
		 FROM preview_runtimes
		 WHERE org_id = @org_id AND preview_instance_id = @preview_id`,
		pgx.NamedArgs{"org_id": orgID, "preview_id": previewID},
	).Scan(&nextEpoch); err != nil {
		return nil, fmt.Errorf("select next preview runtime epoch: %w", err)
	}

	runtime := newStartingPreviewRuntime(orgID, previewID, nextEpoch, workerNodeID, endpointURL)
	if err := txStore.CreatePreviewRuntime(ctx, runtime); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit next preview runtime: %w", err)
	}
	return runtime, nil
}

func newStartingPreviewRuntime(orgID, previewID uuid.UUID, epoch int, workerNodeID, endpointURL string) *models.PreviewRuntime {
	now := time.Now().UTC()
	return &models.PreviewRuntime{
		OrgID:             orgID,
		PreviewInstanceID: previewID,
		RuntimeEpoch:      epoch,
		WorkerNodeID:      workerNodeID,
		EndpointURL:       endpointURL,
		Status:            models.PreviewRuntimeStatusStarting,
		LeaseExpiresAt:    now.Add(90 * time.Second),
	}
}

// GetActivePreviewRuntime returns the latest live, leased runtime for a preview.
func (s *PreviewStore) GetActivePreviewRuntime(ctx context.Context, orgID, previewID uuid.UUID) (*models.PreviewRuntime, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM preview_runtimes
		WHERE org_id = @org_id
		  AND preview_instance_id = @preview_id
		  AND status IN %s
		  AND lease_expires_at > now()
		ORDER BY runtime_epoch DESC
		LIMIT 1`, previewRuntimeColumns, activeRuntimeStatusFilter)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "preview_id": previewID})
	if err != nil {
		return nil, fmt.Errorf("query active preview runtime: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewRuntime])
	if err != nil {
		return nil, fmt.Errorf("get active preview runtime: %w", err)
	}
	return &row, nil
}

// MarkPreviewRuntimeReady marks a runtime ready and mirrors routing fields onto
// preview_instances for API compatibility.
func (s *PreviewStore) MarkPreviewRuntimeReady(ctx context.Context, orgID, runtimeID uuid.UUID, handle string, port int) error {
	tag, err := s.db.Exec(ctx, `
		WITH runtime AS (
			UPDATE preview_runtimes
			SET status = 'ready',
				preview_handle = @handle,
				primary_port = @port,
				last_heartbeat_at = now(),
				updated_at = now()
			WHERE id = @runtime_id AND org_id = @org_id
			RETURNING preview_instance_id, worker_node_id
		)
		UPDATE preview_instances pi
		SET worker_node_id = runtime.worker_node_id,
			preview_handle = @handle,
			port = @port,
			updated_at = now(),
			recycled_at = now()
		FROM runtime
		WHERE pi.id = runtime.preview_instance_id AND pi.org_id = @org_id`,
		pgx.NamedArgs{"org_id": orgID, "runtime_id": runtimeID, "handle": handle, "port": port},
	)
	if err != nil {
		return fmt.Errorf("mark preview runtime ready: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview runtime not found")
	}
	return nil
}

func (s *PreviewStore) MarkPreviewRuntimeFailed(ctx context.Context, orgID, runtimeID uuid.UUID, reason string) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE preview_runtimes
		 SET status = 'failed',
		     stopped_at = COALESCE(stopped_at, now()),
		     error = @error,
		     updated_at = now()
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": runtimeID, "org_id": orgID, "error": reason},
	); err != nil {
		return fmt.Errorf("mark preview runtime failed: %w", err)
	}
	return nil
}

// CreateBranchPreviewInstance inserts a preview runtime reservation owned by a
// preview target instead of a session. Branch preview sandboxes are standalone,
// so session_id is persisted as NULL and preview_holding_container remains
// false; teardown destroys the recycle sandbox directly.
func (s *PreviewStore) CreateBranchPreviewInstance(ctx context.Context, p *models.PreviewInstance) error {
	if p.PreviewTargetID == nil || *p.PreviewTargetID == uuid.Nil {
		return fmt.Errorf("preview target id is required")
	}
	query := fmt.Sprintf(`
		INSERT INTO preview_instances (
			session_id, preview_target_id, org_id, user_id, profile_name, name, status, provider,
			worker_node_id, preview_handle, primary_service, port,
			config_digest, base_commit_sha, expires_at,
			last_path, memory_limit_mb, cpu_limit_millis, disk_limit_mb, recycle_config, recycle_sandbox,
			current_phase, request_id
		) VALUES (
			NULL, @preview_target_id, @org_id, @user_id, @profile_name, @name, @status, @provider,
			@worker_node_id, @preview_handle, @primary_service, @port,
			@config_digest, @base_commit_sha, @expires_at,
			@last_path, @memory_limit_mb, @cpu_limit_millis, @disk_limit_mb, @recycle_config, @recycle_sandbox,
			@current_phase, @request_id
		) RETURNING %s`, previewInstanceColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"preview_target_id": p.PreviewTargetID,
		"org_id":            p.OrgID,
		"user_id":           p.UserID,
		"profile_name":      p.ProfileName,
		"name":              p.Name,
		"status":            p.Status,
		"provider":          p.Provider,
		"worker_node_id":    p.WorkerNodeID,
		"preview_handle":    p.PreviewHandle,
		"primary_service":   p.PrimaryService,
		"port":              p.Port,
		"config_digest":     p.ConfigDigest,
		"base_commit_sha":   p.BaseCommitSHA,
		"expires_at":        p.ExpiresAt,
		"last_path":         p.LastPath,
		"memory_limit_mb":   p.MemoryLimitMB,
		"cpu_limit_millis":  p.CPULimitMillis,
		"disk_limit_mb":     p.DiskLimitMB,
		"recycle_config":    p.RecycleConfig,
		"recycle_sandbox":   p.RecycleSandbox,
		"current_phase":     p.CurrentPhase,
		"request_id":        p.RequestID,
	})
	if err != nil {
		return fmt.Errorf("insert branch preview instance: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewInstance])
	if err != nil {
		return fmt.Errorf("scan branch preview instance: %w", err)
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

// ReassignPreviewWorker reassigns a starting preview reservation and its active
// runtime routing row to the worker that actually claimed the start job.
func (s *PreviewStore) ReassignPreviewWorker(ctx context.Context, orgID, id uuid.UUID, workerNodeID, endpointURL string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin preview worker reassignment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := s.WithTx(tx)
	if err := txStore.UpdatePreviewWorkerNodeID(ctx, orgID, id, workerNodeID); err != nil {
		return err
	}
	if endpointURL != "" {
		if _, err := tx.Exec(ctx,
			fmt.Sprintf(`UPDATE preview_runtimes
			 SET status = 'stopped', stopped_at = COALESCE(stopped_at, now()), updated_at = now()
			 WHERE preview_instance_id = @preview_id AND org_id = @org_id AND status IN %s`, activeRuntimeStatusFilter),
			pgx.NamedArgs{"org_id": orgID, "preview_id": id},
		); err != nil {
			return fmt.Errorf("stop previous preview runtimes: %w", err)
		}

		var nextEpoch int
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(runtime_epoch), 0) + 1
			 FROM preview_runtimes
			 WHERE org_id = @org_id AND preview_instance_id = @preview_id`,
			pgx.NamedArgs{"org_id": orgID, "preview_id": id},
		).Scan(&nextEpoch); err != nil {
			return fmt.Errorf("select next preview runtime epoch: %w", err)
		}

		runtime := newStartingPreviewRuntime(orgID, id, nextEpoch, workerNodeID, endpointURL)
		if err := txStore.CreatePreviewRuntime(ctx, runtime); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit preview worker reassignment: %w", err)
	}
	return nil
}

// UpdatePreviewPhase records the current startup phase for status reloads and
// support diagnostics. The update is scoped to active startup because terminal
// status transitions own the final phase label.
func (s *PreviewStore) UpdatePreviewPhase(ctx context.Context, orgID, id uuid.UUID, phase string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE preview_instances SET current_phase = @phase, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status NOT IN ('stopped', 'failed', 'expired')`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "phase": phase},
	)
	if err != nil {
		return fmt.Errorf("update preview phase: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview instance not found or terminal")
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
	phase := previewPhaseForStatus(status)
	if status.IsTerminal() {
		query = `UPDATE preview_instances SET status = @status, current_phase = @phase, error = @error, preview_holding_container = FALSE, stopped_at = now(), updated_at = now()
			WHERE id = @id AND org_id = @org_id`
	} else {
		query = `UPDATE preview_instances SET status = @status, current_phase = @phase, error = @error, updated_at = now()
			WHERE id = @id AND org_id = @org_id`
	}
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id": id, "org_id": orgID, "status": status, "phase": phase, "error": errMsg,
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
	phase := previewPhaseForStatus(status)
	query := `UPDATE preview_instances SET status = @status, current_phase = @phase, error = @error, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status NOT IN ('stopped', 'failed', 'expired', 'unavailable')`
	if status.IsTerminal() {
		query = `UPDATE preview_instances SET status = @status, current_phase = @phase, error = @error, preview_holding_container = FALSE, stopped_at = now(), updated_at = now()
			WHERE id = @id AND org_id = @org_id AND status NOT IN ('stopped', 'failed', 'expired', 'unavailable')`
	}
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id": id, "org_id": orgID, "status": status, "phase": phase, "error": errMsg,
	})
	if err != nil {
		return 0, fmt.Errorf("conditional update preview status: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *PreviewStore) UpdatePreviewSourceWorkspaceRevision(ctx context.Context, orgID, id uuid.UUID, revision int64, revisionUpdatedAt time.Time) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE preview_instances
		SET source_workspace_revision = @workspace_revision,
		    source_workspace_revision_updated_at = @workspace_revision_updated_at,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"id":                            id,
			"org_id":                        orgID,
			"workspace_revision":            revision,
			"workspace_revision_updated_at": revisionUpdatedAt,
		},
	)
	if err != nil {
		return fmt.Errorf("update preview source workspace revision: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview instance not found")
	}
	return nil
}

func previewPhaseForStatus(status models.PreviewStatus) string {
	switch status {
	case models.PreviewStatusReady, models.PreviewStatusPartiallyReady:
		return "ready"
	case models.PreviewStatusUnhealthy:
		return "unhealthy"
	case models.PreviewStatusStopped:
		return "stopped"
	case models.PreviewStatusExpired:
		return "expired"
	case models.PreviewStatusUnavailable:
		return "unavailable"
	case models.PreviewStatusFailed:
		return "failed"
	default:
		return "starting"
	}
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
	if _, err := s.db.Exec(ctx,
		fmt.Sprintf(`UPDATE preview_runtimes
		 SET status = 'stopped', stopped_at = COALESCE(stopped_at, now()), updated_at = now()
		 WHERE preview_instance_id = @id AND org_id = @org_id AND status IN %s`, activeRuntimeStatusFilter),
		pgx.NamedArgs{"id": id, "org_id": orgID},
	); err != nil {
		return fmt.Errorf("stop preview runtimes: %w", err)
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
	// LEFT JOIN so that a deleted session (or a standalone preview with no
	// session_id) does not drop the row and leave us with ErrNoRows: the
	// UPDATE itself succeeded, and we still need its result to decide cleanup.
	// COALESCE maps a NULL session_id to the zero UUID — the same pattern used
	// throughout the preview columns — so we can always scan into uuid.UUID.
	query := `WITH released AS (
			UPDATE preview_instances
			SET preview_holding_container = FALSE, updated_at = now()
			WHERE id = @id AND org_id = @org_id
			RETURNING session_id
		)
		SELECT
			COALESCE(released.session_id, '00000000-0000-0000-0000-000000000000'::uuid) AS session_id,
			COALESCE(s.container_id, '') AS container_id,
			COALESCE(s.turn_holding_container, FALSE) AS turn_holds
		FROM released
		LEFT JOIN sessions s ON s.id = released.session_id AND s.org_id = @org_id`

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

// CountActiveStandalonePreviewsByOrg counts target-owned branch previews for
// standalone quota enforcement. Session-owned previews are intentionally
// excluded so API/manual usage cannot starve active agent sessions.
//
// Note: preview_target_id IS NOT NULL is the sole criterion. session_id IS NULL
// would incorrectly exclude hybrid previews — session previews that were later
// attached to a branch target via AttachPreviewTarget — which do consume
// branch-preview capacity and must count against the standalone quota.
func (s *PreviewStore) CountActiveStandalonePreviewsByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM preview_instances WHERE org_id = @org_id
		 AND preview_target_id IS NOT NULL
		 AND status IN %s`, activeStatusFilter),
		pgx.NamedArgs{"org_id": orgID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active standalone previews by org: %w", err)
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

// CountActiveStandalonePreviewsByUser counts target-owned branch previews for
// per-user standalone quota enforcement.
func (s *PreviewStore) CountActiveStandalonePreviewsByUser(ctx context.Context, orgID, userID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM preview_instances
		 WHERE org_id = @org_id AND user_id = @user_id
		 AND preview_target_id IS NOT NULL
		 AND status IN %s`, activeStatusFilter),
		pgx.NamedArgs{"org_id": orgID, "user_id": userID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active standalone previews by user: %w", err)
	}
	return count, nil
}

// CountActivePreviewsByWorker counts active previews on a worker node.
// lint:allow-no-orgid reason="cross-org worker capacity stats for scheduling"
func (s *PreviewStore) CountActivePreviewsByWorker(ctx context.Context, workerNodeID string) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(DISTINCT preview_instance_id) FROM preview_runtimes WHERE worker_node_id = @worker
		 AND status IN %s
		 AND lease_expires_at > now()`, activeRuntimeStatusFilter),
		pgx.NamedArgs{"worker": workerNodeID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active previews by worker: %w", err)
	}
	return count, nil
}

// CountActiveStandalonePreviewsByWorker counts target-owned branch previews on
// a worker. The scheduler still considers total worker load, but this metric is
// useful for observability and independent branch-preview caps.
// lint:allow-no-orgid reason="cross-org worker capacity stats for scheduling"
func (s *PreviewStore) CountActiveStandalonePreviewsByWorker(ctx context.Context, workerNodeID string) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(DISTINCT r.preview_instance_id)
		 FROM preview_runtimes r
		 JOIN preview_instances pi ON pi.id = r.preview_instance_id AND pi.org_id = r.org_id
		 WHERE r.worker_node_id = @worker
		 AND pi.preview_target_id IS NOT NULL
		 AND r.status IN %s
		 AND r.lease_expires_at > now()`, activeRuntimeStatusFilter),
		pgx.NamedArgs{"worker": workerNodeID},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active standalone previews by worker: %w", err)
	}
	return count, nil
}

// CountActivePreviewsByWorkers returns active preview counts keyed by worker
// node ID for all given worker IDs in a single query. Workers with no active
// previews are absent from the returned map. This replaces N sequential
// CountActivePreviewsByWorker calls in the worker selector hot path.
// lint:allow-no-orgid reason="cross-org worker capacity stats for scheduling"
func (s *PreviewStore) CountActivePreviewsByWorkers(ctx context.Context, workerIDs []string) (map[string]int, error) {
	if len(workerIDs) == 0 {
		return map[string]int{}, nil
	}
	query := fmt.Sprintf(`
		SELECT worker_node_id, COUNT(DISTINCT preview_instance_id) AS count
		FROM preview_runtimes
		WHERE worker_node_id = ANY(@worker_ids)
		  AND status IN %s
		  AND lease_expires_at > now()
		GROUP BY worker_node_id`, activeRuntimeStatusFilter)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"worker_ids": workerIDs})
	if err != nil {
		return nil, fmt.Errorf("count active previews by workers: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int, len(workerIDs))
	for rows.Next() {
		var nodeID string
		var count int
		if err := rows.Scan(&nodeID, &count); err != nil {
			return nil, fmt.Errorf("scan active preview count: %w", err)
		}
		counts[nodeID] = count
	}
	return counts, rows.Err()
}

// MarkPreviewRuntimesDrainingByWorker marks live runtimes owned by a draining worker.
// lint:allow-no-orgid reason="cross-org worker drain marks runtimes owned by this node"
func (s *PreviewStore) MarkPreviewRuntimesDrainingByWorker(ctx context.Context, workerNodeID string) (int64, error) {
	tag, err := s.db.Exec(ctx,
		fmt.Sprintf(`UPDATE preview_runtimes
		 SET status = 'draining', drain_requested_at = COALESCE(drain_requested_at, now()), updated_at = now()
		 WHERE worker_node_id = @worker AND status IN %s`, activeRuntimeStatusFilter),
		pgx.NamedArgs{"worker": workerNodeID},
	)
	if err != nil {
		return 0, fmt.Errorf("mark preview runtimes draining by worker: %w", err)
	}
	return tag.RowsAffected(), nil
}

// HeartbeatPreviewRuntimesByWorker extends leases for live runtimes owned by a worker.
// lint:allow-no-orgid reason="cross-org worker heartbeat extends runtimes owned by this node"
func (s *PreviewStore) HeartbeatPreviewRuntimesByWorker(ctx context.Context, workerNodeID string, leaseExpiresAt time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx,
		fmt.Sprintf(`UPDATE preview_runtimes
		 SET last_heartbeat_at = now(), lease_expires_at = @lease_expires_at, updated_at = now()
		 WHERE worker_node_id = @worker AND status IN %s`, activeRuntimeStatusFilter),
		pgx.NamedArgs{"worker": workerNodeID, "lease_expires_at": leaseExpiresAt},
	)
	if err != nil {
		return 0, fmt.Errorf("heartbeat preview runtimes by worker: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MarkActivePreviewRuntimesLostByWorker marks a worker's live runtimes lost and
// transitions their preview instances to unavailable.
// lint:allow-no-orgid reason="cross-org worker shutdown/recovery marks runtimes owned by this node"
func (s *PreviewStore) MarkActivePreviewRuntimesLostByWorker(ctx context.Context, workerNodeID, reason string) (int64, error) {
	return s.MarkActivePreviewRuntimesLostByWorkerWithReason(ctx, workerNodeID, reason, models.PreviewUnavailableReasonOwnerLost)
}

// MarkActivePreviewRuntimesLostByWorkerWithReason marks a worker's live
// runtimes lost and persists a typed user/operator-facing unavailable reason.
// lint:allow-no-orgid reason="cross-org worker shutdown/recovery marks runtimes owned by this node"
func (s *PreviewStore) MarkActivePreviewRuntimesLostByWorkerWithReason(ctx context.Context, workerNodeID, reason string, unavailableReason models.PreviewUnavailableReason) (int64, error) {
	if err := unavailableReason.Validate(); err != nil {
		return 0, err
	}
	tag, err := s.db.Exec(ctx,
		fmt.Sprintf(`WITH lost AS (
			UPDATE preview_runtimes
			SET status = 'lost',
				error = @reason,
				unavailable_reason = @unavailable_reason,
				stopped_at = COALESCE(stopped_at, now()),
				updated_at = now()
			WHERE worker_node_id = @worker AND status IN %s
			RETURNING org_id, preview_instance_id
		)
		UPDATE preview_instances pi
		SET status = 'unavailable',
			error = @reason,
			unavailable_reason = @unavailable_reason,
			stopped_at = COALESCE(stopped_at, now()),
			updated_at = now()
		FROM lost
		WHERE pi.id = lost.preview_instance_id
		  AND pi.org_id = lost.org_id
		  AND pi.status IN %s`, activeRuntimeStatusFilter, activeStatusFilter),
		pgx.NamedArgs{"worker": workerNodeID, "reason": reason, "unavailable_reason": unavailableReason},
	)
	if err != nil {
		return 0, fmt.Errorf("mark active preview runtimes lost by worker: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MarkExpiredPreviewRuntimesLost marks runtimes with expired leases lost and
// transitions their previews to unavailable.
// lint:allow-no-orgid reason="cross-org recovery sweep repairs stale preview runtime ownership"
func (s *PreviewStore) MarkExpiredPreviewRuntimesLost(ctx context.Context, cutoff time.Time, reason string) (int64, error) {
	tag, err := s.db.Exec(ctx,
		fmt.Sprintf(`WITH lost AS (
			UPDATE preview_runtimes
			SET status = 'lost',
				error = @reason,
				unavailable_reason = 'lease_expired',
				stopped_at = COALESCE(stopped_at, now()),
				updated_at = now()
			WHERE lease_expires_at < @cutoff AND status IN %s
			RETURNING org_id, preview_instance_id
		)
		UPDATE preview_instances pi
		SET status = 'unavailable',
			error = @reason,
			unavailable_reason = 'lease_expired',
			stopped_at = COALESCE(stopped_at, now()),
			updated_at = now()
		FROM lost
		WHERE pi.id = lost.preview_instance_id
		  AND pi.org_id = lost.org_id
		  AND pi.status IN %s`, activeRuntimeStatusFilter, activeStatusFilter),
		pgx.NamedArgs{"cutoff": cutoff, "reason": reason},
	)
	if err != nil {
		return 0, fmt.Errorf("mark expired preview runtimes lost: %w", err)
	}
	return tag.RowsAffected(), nil
}

// StandaloneCapacityCounts holds the three preview counts needed to enforce
// branch-preview capacity limits, fetched in a single DB round-trip.
type StandaloneCapacityCounts struct {
	UserStandalone int
	OrgStandalone  int
	WorkerTotal    int
}

// CheckStandaloneCapacityCounts returns all three counts needed by
// checkStandaloneConcurrencyCapsWithStore in a single query, replacing three
// serial COUNT round-trips with one.
// lint:allow-no-orgid reason="worker count is cross-org; org scoping applied inside scalar subqueries"
func (s *PreviewStore) CheckStandaloneCapacityCounts(ctx context.Context, orgID, userID uuid.UUID, workerNodeID string) (StandaloneCapacityCounts, error) {
	query := fmt.Sprintf(`
		SELECT
			(SELECT COUNT(*) FROM preview_instances
			 WHERE org_id = @org_id AND user_id = @user_id
			   AND preview_target_id IS NOT NULL AND status IN %s) AS user_standalone,
			(SELECT COUNT(*) FROM preview_instances
			 WHERE org_id = @org_id
			   AND preview_target_id IS NOT NULL AND status IN %s) AS org_standalone,
			(SELECT COUNT(*) FROM preview_instances
			 WHERE worker_node_id = @worker_node_id AND status IN %s) AS worker_total`,
		activeStatusFilter, activeStatusFilter, activeStatusFilter)
	var counts StandaloneCapacityCounts
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"user_id":        userID,
		"worker_node_id": workerNodeID,
	}).Scan(&counts.UserStandalone, &counts.OrgStandalone, &counts.WorkerTotal)
	if err != nil {
		return StandaloneCapacityCounts{}, fmt.Errorf("check standalone capacity counts: %w", err)
	}
	return counts, nil
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
		AND NOT EXISTS (
			SELECT 1 FROM session_sandbox_holders h
			WHERE h.org_id = preview_instances.org_id
			  AND h.session_id = preview_instances.session_id
			  AND h.holder_kind = 'thread_runtime'
			  AND h.status IN ('active', 'draining')
			  AND h.expires_at > now()
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
		AND NOT EXISTS (
			SELECT 1 FROM session_sandbox_holders h
			WHERE h.org_id = preview_instances.org_id
			  AND h.session_id = preview_instances.session_id
			  AND h.holder_kind = 'thread_runtime'
			  AND h.status IN ('active', 'draining')
			  AND h.expires_at > now()
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

func (s *PreviewStore) FindDependencyCache(ctx context.Context, orgID, repoID uuid.UUID, cacheKey string) (*models.PreviewDependencyCache, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_dependency_cache
		WHERE org_id = @org_id AND repo_id = @repo_id AND cache_key = @cache_key`, previewDependencyCacheColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "repo_id": repoID, "cache_key": cacheKey})
	if err != nil {
		return nil, fmt.Errorf("query dependency cache: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewDependencyCache])
	if err != nil {
		return nil, fmt.Errorf("get dependency cache: %w", err)
	}
	return &row, nil
}

func (s *PreviewStore) UpsertDependencyCache(ctx context.Context, entry *models.PreviewDependencyCache) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_dependency_cache (
			org_id, repo_id, cache_key, placement_key, blob_key, size_bytes, metadata
		) VALUES (
			@org_id, @repo_id, @cache_key, @placement_key, @blob_key, @size_bytes, @metadata
		)
		ON CONFLICT (org_id, repo_id, cache_key)
		DO UPDATE SET placement_key = EXCLUDED.placement_key,
			blob_key = EXCLUDED.blob_key,
			size_bytes = EXCLUDED.size_bytes,
			metadata = EXCLUDED.metadata,
			last_used_at = now()
		RETURNING %s`, previewDependencyCacheColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":        entry.OrgID,
		"repo_id":       entry.RepoID,
		"cache_key":     entry.CacheKey,
		"placement_key": entry.PlacementKey,
		"blob_key":      entry.BlobKey,
		"size_bytes":    entry.SizeBytes,
		"metadata":      entry.Metadata,
	})
	if err != nil {
		return fmt.Errorf("upsert dependency cache: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewDependencyCache])
	if err != nil {
		return fmt.Errorf("scan dependency cache: %w", err)
	}
	*entry = row
	return nil
}

func (s *PreviewStore) TouchDependencyCache(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE preview_dependency_cache SET last_used_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("touch dependency cache: %w", err)
	}
	return nil
}

func (s *PreviewStore) DeleteDependencyCache(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM preview_dependency_cache WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("delete dependency cache: %w", err)
	}
	return nil
}

func (s *PreviewStore) ListDependencyCacheLRU(ctx context.Context, orgID, repoID uuid.UUID, keepNewest, limit int) ([]models.PreviewDependencyCache, error) {
	if limit <= 0 {
		limit = 500
	}
	query := fmt.Sprintf(`SELECT %s FROM preview_dependency_cache
		WHERE org_id = @org_id AND repo_id = @repo_id
		ORDER BY last_used_at DESC OFFSET @keep_newest LIMIT @limit`, previewDependencyCacheColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "repo_id": repoID, "keep_newest": keepNewest, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list dependency cache lru: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewDependencyCache])
}

// ListExpiredDependencyCaches returns dependency cache entries whose last_used_at is before cutoff.
// lint:allow-no-orgid reason="background dependency cache cleanup scans expired cache metadata across orgs"
func (s *PreviewStore) ListExpiredDependencyCaches(ctx context.Context, cutoff time.Time, limit int) ([]models.PreviewDependencyCache, error) {
	if limit <= 0 {
		limit = 500
	}
	query := fmt.Sprintf(`SELECT %s FROM preview_dependency_cache
		WHERE last_used_at < @cutoff
		ORDER BY last_used_at ASC LIMIT @limit`, previewDependencyCacheColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"cutoff": cutoff, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list expired dependency caches: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewDependencyCache])
}

// ListDependencyCachesOverLimit returns dependency cache entries ranked beyond keepNewestPerRepo within each repo.
// lint:allow-no-orgid reason="background dependency cache cleanup scans LRU metadata across orgs"
func (s *PreviewStore) ListDependencyCachesOverLimit(ctx context.Context, keepNewestPerRepo, limit int) ([]models.PreviewDependencyCache, error) {
	if keepNewestPerRepo < 0 {
		keepNewestPerRepo = 0
	}
	if limit <= 0 {
		limit = 500
	}
	query := fmt.Sprintf(`SELECT %s FROM (
			SELECT %s, row_number() OVER (
				PARTITION BY org_id, repo_id ORDER BY last_used_at DESC
			) AS dependency_cache_rank
			FROM preview_dependency_cache
		) ranked_dependency_cache
		WHERE dependency_cache_rank > @keep_newest
		ORDER BY last_used_at ASC LIMIT @limit`, previewDependencyCacheColumns, previewDependencyCacheColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"keep_newest": keepNewestPerRepo, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list dependency caches over limit: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewDependencyCache])
}

func (s *PreviewStore) UpsertDependencyCacheLocation(ctx context.Context, location *models.PreviewDependencyCacheLocation) error {
	query := fmt.Sprintf(`
		INSERT INTO preview_dependency_cache_locations (
			org_id, repo_id, cache_key, placement_key, worker_node_id, size_bytes
		) VALUES (
			@org_id, @repo_id, @cache_key, @placement_key, @worker_node_id, @size_bytes
		)
		ON CONFLICT (org_id, repo_id, cache_key, worker_node_id)
		DO UPDATE SET placement_key = EXCLUDED.placement_key,
			size_bytes = EXCLUDED.size_bytes,
			last_used_at = now()
		RETURNING %s`, previewDependencyCacheLocationColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":         location.OrgID,
		"repo_id":        location.RepoID,
		"cache_key":      location.CacheKey,
		"placement_key":  location.PlacementKey,
		"worker_node_id": location.WorkerNodeID,
		"size_bytes":     location.SizeBytes,
	})
	if err != nil {
		return fmt.Errorf("upsert dependency cache location: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewDependencyCacheLocation])
	if err != nil {
		return fmt.Errorf("scan dependency cache location: %w", err)
	}
	*location = row
	return nil
}

func (s *PreviewStore) ListDependencyCacheWorkersByPlacement(ctx context.Context, orgID, repoID uuid.UUID, placementKey string, limit int) ([]models.PreviewDependencyCacheLocation, error) {
	query := fmt.Sprintf(`SELECT %s FROM preview_dependency_cache_locations
		WHERE org_id = @org_id AND repo_id = @repo_id AND placement_key = @placement_key
		ORDER BY last_used_at DESC LIMIT @limit`, previewDependencyCacheLocationColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "repo_id": repoID, "placement_key": placementKey, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list dependency cache workers by placement: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewDependencyCacheLocation])
}

func (s *PreviewStore) DeleteDependencyCacheLocation(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM preview_dependency_cache_locations WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("delete dependency cache location: %w", err)
	}
	return nil
}

// DeleteExpiredDependencyCacheLocations removes local cache location hints whose last_used_at is before cutoff.
// lint:allow-no-orgid reason="background dependency cache cleanup deletes stale ephemeral local cache hints across orgs"
func (s *PreviewStore) DeleteExpiredDependencyCacheLocations(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM preview_dependency_cache_locations WHERE last_used_at < @cutoff`,
		pgx.NamedArgs{"cutoff": cutoff},
	)
	if err != nil {
		return 0, fmt.Errorf("delete expired dependency cache locations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteDependencyCacheLocationByWorkerCacheKey removes a single local cache location hint for the given worker and cache key.
// lint:allow-no-orgid reason="worker cleanup deletes ephemeral local cache hints across orgs without exposing tenant data"
func (s *PreviewStore) DeleteDependencyCacheLocationByWorkerCacheKey(ctx context.Context, workerNodeID, cacheKey string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM preview_dependency_cache_locations WHERE worker_node_id = @worker_node_id AND cache_key = @cache_key`,
		pgx.NamedArgs{"worker_node_id": workerNodeID, "cache_key": cacheKey},
	)
	if err != nil {
		return fmt.Errorf("delete dependency cache location by worker cache key: %w", err)
	}
	return nil
}

// DeleteDependencyCacheLocationsForWorker removes all local cache location hints for the given worker node.
// lint:allow-no-orgid reason="worker cleanup deletes ephemeral local cache hints across orgs without exposing tenant data"
func (s *PreviewStore) DeleteDependencyCacheLocationsForWorker(ctx context.Context, workerNodeID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM preview_dependency_cache_locations WHERE worker_node_id = @worker_node_id`,
		pgx.NamedArgs{"worker_node_id": workerNodeID},
	)
	if err != nil {
		return fmt.Errorf("delete dependency cache locations for worker: %w", err)
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
