package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const previewGroupColumns = `id, org_id, repository_id, group_kind, branch, preview_config_name,
	pull_request_number, source_type, source_id, source_url, current_target_id,
	latest_commit_sha, current_status, pinned, created_by_user_id, created_at, last_activity_at`

const previewCurrentEffectiveStatusSQL = `CASE
		WHEN pg.current_status = 'warm' AND latest.stopped_reason = 'session_prewarm_policy' THEN 'warm'
		ELSE COALESCE(latest.status, pg.current_status)
	END`

const previewCurrentSummaryColumns = `pg.id, pg.org_id, pg.repository_id, pg.group_kind, pg.branch, pg.preview_config_name,
	pg.pull_request_number, pg.source_type, pg.source_id, pg.source_url, pg.current_target_id,
	pg.latest_commit_sha, pg.current_status, pg.pinned, pg.created_by_user_id, pg.created_at, pg.last_activity_at,
	repo.full_name AS repository_full_name,
	` + previewCurrentEffectiveStatusSQL + `::text AS status,
	CASE
		WHEN pg.pinned THEN 'pinned'
		WHEN pg.latest_commit_sha = '' THEN 'unknown'
		WHEN COALESCE(latest.base_commit_sha, '') <> '' AND latest.base_commit_sha <> pg.latest_commit_sha THEN 'outdated'
		ELSE 'current'
	END AS freshness,
	COALESCE(latest.base_commit_sha, '') AS running_commit_sha,
	latest.id AS current_preview_id,
	latest.expires_at, latest.stopped_at, COALESCE(latest.stopped_reason, '') AS stopped_reason,
	COALESCE(latest.error, '') AS error, COALESCE(latest.current_phase, '') AS current_phase,
	COALESCE(stats.attempt_count, 0)::int AS attempt_count,
	COALESCE(stats.target_count, 0)::int AS target_count,
	false AS resumable, NULL::integer AS resume_estimate_seconds`

var prSourceIDRe = regexp.MustCompile(`^([^/\s]+)/([^#\s]+)#([0-9]+)(?:@.+)?$`)

func nilIfUUIDZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

type PreviewCurrentIndexFilters struct {
	RepositoryID *uuid.UUID
	Scope        string
	Pinned       *bool
	Query        string
	CursorTime   *time.Time
	CursorID     *uuid.UUID
	Limit        int
}

func ParsePRSourceID(sourceID string) (string, string, int, bool) {
	match := prSourceIDRe.FindStringSubmatch(strings.TrimSpace(sourceID))
	if len(match) != 4 {
		return "", "", 0, false
	}
	number, err := strconv.Atoi(match[3])
	if err != nil {
		return "", "", 0, false
	}
	return match[1], match[2], number, true
}

func (s *PreviewStore) UpsertPreviewGroupForTarget(ctx context.Context, orgID uuid.UUID, target models.PreviewTarget, latestCommitSHA string) (*models.PreviewGroup, error) {
	group := classifyPreviewGroup(target)
	if latestCommitSHA == "" {
		latestCommitSHA = target.CommitSHA
	}
	currentStatus, err := s.currentTargetStatus(ctx, orgID, target.ID)
	if err != nil {
		return nil, err
	}
	if target.SourceType == models.PreviewSourceTypeSession && strings.TrimSpace(target.SourceID) != "" {
		if sessionID, parseErr := uuid.Parse(strings.TrimSpace(target.SourceID)); parseErr == nil {
			if row, migrated, migrateErr := s.migrateSessionPreviewGroupToTarget(ctx, orgID, sessionID, target, latestCommitSHA, currentStatus); migrateErr != nil {
				return nil, migrateErr
			} else if migrated {
				if err := s.AttachTargetToPreviewGroup(ctx, orgID, target.ID, row.ID); err != nil {
					return nil, err
				}
				return row, nil
			}
		}
	}
	lastActivityAt := target.CreatedAt
	if lastActivityAt.IsZero() {
		lastActivityAt = time.Now()
	}
	createdBy := target.CreatedByUserID
	query := fmt.Sprintf(`
		INSERT INTO preview_groups (
			org_id, repository_id, group_kind, branch, preview_config_name,
			pull_request_number, source_type, source_id, source_url, current_target_id,
			latest_commit_sha, current_status, pinned, created_by_user_id, last_activity_at
		) VALUES (
			@org_id, @repository_id, @group_kind, @branch, @preview_config_name,
			@pull_request_number, @source_type, @source_id, @source_url, @current_target_id,
			@latest_commit_sha, @current_status, @pinned, @created_by_user_id, @last_activity_at
		)
		ON CONFLICT (org_id, repository_id, group_kind, branch, preview_config_name, COALESCE(pull_request_number, 0), source_type, source_id, pinned)
		DO UPDATE SET
			current_target_id = CASE
				WHEN EXCLUDED.last_activity_at >= preview_groups.last_activity_at THEN EXCLUDED.current_target_id
				ELSE preview_groups.current_target_id
			END,
			latest_commit_sha = CASE
				WHEN EXCLUDED.last_activity_at >= preview_groups.last_activity_at THEN EXCLUDED.latest_commit_sha
				ELSE preview_groups.latest_commit_sha
			END,
			current_status = CASE
				WHEN EXCLUDED.last_activity_at >= preview_groups.last_activity_at THEN EXCLUDED.current_status
				ELSE preview_groups.current_status
			END,
			source_url = CASE
				WHEN EXCLUDED.source_url <> '' THEN EXCLUDED.source_url
				ELSE preview_groups.source_url
			END,
			last_activity_at = GREATEST(preview_groups.last_activity_at, EXCLUDED.last_activity_at)
		RETURNING %s`, previewGroupColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":              orgID,
		"repository_id":       target.RepositoryID,
		"group_kind":          group.kind,
		"branch":              target.Branch,
		"preview_config_name": target.PreviewConfigName,
		"pull_request_number": group.pullRequestNumber,
		"source_type":         group.sourceType,
		"source_id":           group.sourceID,
		"source_url":          group.sourceURL,
		"current_target_id":   target.ID,
		"latest_commit_sha":   latestCommitSHA,
		"current_status":      currentStatus,
		"pinned":              group.pinned,
		"created_by_user_id":  &createdBy,
		"last_activity_at":    lastActivityAt,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert preview group: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewGroup])
	if err != nil {
		return nil, fmt.Errorf("scan preview group: %w", err)
	}
	if err := s.AttachTargetToPreviewGroup(ctx, orgID, target.ID, row.ID); err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *PreviewStore) migrateSessionPreviewGroupToTarget(ctx context.Context, orgID, sessionID uuid.UUID, target models.PreviewTarget, latestCommitSHA, currentStatus string) (*models.PreviewGroup, bool, error) {
	group := classifyPreviewGroup(target)
	lastActivityAt := target.CreatedAt
	if lastActivityAt.IsZero() {
		lastActivityAt = time.Now()
	}
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		UPDATE preview_groups
		SET group_kind = @group_kind,
		    branch = @branch,
		    pull_request_number = @pull_request_number,
		    source_type = @source_type,
		    source_id = @source_id,
		    source_url = @source_url,
		    current_target_id = @current_target_id,
		    latest_commit_sha = @latest_commit_sha,
		    current_status = @current_status,
		    last_activity_at = GREATEST(last_activity_at, @last_activity_at)
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND group_kind = 'session'
		  AND source_id = @session_source_id
		  AND preview_config_name = @preview_config_name
		  AND pinned = false
		RETURNING %s`, previewGroupColumns),
		pgx.NamedArgs{
			"org_id":              orgID,
			"repository_id":       target.RepositoryID,
			"group_kind":          group.kind,
			"branch":              target.Branch,
			"preview_config_name": target.PreviewConfigName,
			"pull_request_number": group.pullRequestNumber,
			"source_type":         group.sourceType,
			"source_id":           group.sourceID,
			"source_url":          group.sourceURL,
			"current_target_id":   target.ID,
			"latest_commit_sha":   latestCommitSHA,
			"current_status":      currentStatus,
			"last_activity_at":    lastActivityAt,
			"session_source_id":   sessionID.String(),
		},
	)
	if err != nil {
		return nil, false, fmt.Errorf("migrate session preview group to target: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewGroup])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("collect migrated session preview group: %w", err)
	}
	return &row, true, nil
}

type previewGroupClassification struct {
	kind              models.PreviewGroupKind
	sourceType        models.PreviewSourceType
	sourceID          string
	sourceURL         string
	pullRequestNumber *int
	pinned            bool
}

func classifyPreviewGroup(target models.PreviewTarget) previewGroupClassification {
	if _, _, number, ok := ParsePRSourceID(target.SourceID); ok {
		prNumber := number
		return previewGroupClassification{
			kind:              models.PreviewGroupKindPullRequest,
			sourceType:        models.PreviewSourceTypePullRequest,
			sourceID:          fmt.Sprintf("%s#%d", repositorySourcePrefix(target.SourceID), number),
			sourceURL:         target.SourceURL,
			pullRequestNumber: &prNumber,
		}
	}
	if strings.TrimSpace(target.SourceID) != "" {
		return previewGroupClassification{
			kind:       models.PreviewGroupKindSource,
			sourceType: target.SourceType,
			sourceID:   target.SourceID,
			sourceURL:  target.SourceURL,
		}
	}
	return previewGroupClassification{
		kind:       models.PreviewGroupKindBranch,
		sourceType: target.SourceType,
		sourceURL:  target.SourceURL,
	}
}

func repositorySourcePrefix(sourceID string) string {
	owner, repo, _, ok := ParsePRSourceID(sourceID)
	if !ok {
		return sourceID
	}
	return owner + "/" + repo
}

func (s *PreviewStore) currentTargetStatus(ctx context.Context, orgID, targetID uuid.UUID) (string, error) {
	var status string
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT status::text
			FROM preview_instances
			WHERE org_id = @org_id AND preview_target_id = @target_id
			ORDER BY created_at DESC
			LIMIT 1
		), 'target_created')`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID},
	).Scan(&status)
	if err != nil {
		return "", fmt.Errorf("load current target status: %w", err)
	}
	return status, nil
}

func (s *PreviewStore) UpsertPreviewGroupStatus(ctx context.Context, orgID uuid.UUID, groupID uuid.UUID, status models.PreviewStatus) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE preview_groups
		SET current_status = @status, last_activity_at = now()
		WHERE id = @group_id AND org_id = @org_id`,
		pgx.NamedArgs{"org_id": orgID, "group_id": groupID, "status": status},
	)
	if err != nil {
		return fmt.Errorf("update preview group status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview group not found")
	}
	return nil
}

func (s *PreviewStore) syncPreviewGroupStatusForPreview(ctx context.Context, orgID, previewID uuid.UUID, status models.PreviewStatus) error {
	_, err := s.db.Exec(ctx, `
		UPDATE preview_groups pg
		SET current_status = @status, last_activity_at = now()
		FROM preview_targets target
		JOIN preview_instances pi ON pi.preview_target_id = target.id AND pi.org_id = target.org_id
		WHERE pg.id = target.preview_group_id
		  AND pg.org_id = @org_id
		  AND pi.id = @preview_id
		  AND pg.current_target_id = target.id`,
		pgx.NamedArgs{"org_id": orgID, "preview_id": previewID, "status": status},
	)
	if err != nil {
		return fmt.Errorf("sync preview group status for preview: %w", err)
	}
	return nil
}

func (s *PreviewStore) AttachTargetToPreviewGroup(ctx context.Context, orgID uuid.UUID, targetID uuid.UUID, groupID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE preview_targets SET preview_group_id = @group_id
		WHERE id = @target_id AND org_id = @org_id`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID, "group_id": groupID},
	)
	if err != nil {
		return fmt.Errorf("attach target to preview group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview target not found")
	}
	return nil
}

func (s *PreviewStore) UpsertSessionPreviewWarmGroup(ctx context.Context, orgID, repositoryID, sessionID, userID uuid.UUID, previewConfigName string) (*models.PreviewGroup, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		INSERT INTO preview_groups (
			org_id, repository_id, group_kind, branch, preview_config_name,
			pull_request_number, source_type, source_id, source_url, current_target_id,
			latest_commit_sha, current_status, pinned, created_by_user_id, last_activity_at
		) VALUES (
			@org_id, @repository_id, 'session', '', @preview_config_name,
			NULL, 'session', @source_id, '', NULL,
			'', 'warm', false, @created_by_user_id, now()
		)
		ON CONFLICT (org_id, repository_id, group_kind, branch, preview_config_name, COALESCE(pull_request_number, 0), source_type, source_id, pinned)
		DO UPDATE SET
			current_status = 'warm',
			last_activity_at = now()
		RETURNING %s`, previewGroupColumns),
		pgx.NamedArgs{
			"org_id":              orgID,
			"repository_id":       repositoryID,
			"preview_config_name": previewConfigName,
			"source_id":           sessionID.String(),
			"created_by_user_id":  nilIfUUIDZero(userID),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("upsert session preview warm group: %w", err)
	}
	group, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewGroup])
	if err != nil {
		return nil, fmt.Errorf("collect session preview warm group: %w", err)
	}
	return &group, nil
}

// UpdatePreviewGroupsLatestSHAForBranch updates latest_commit_sha for all
// non-pinned branch-kind preview groups in a repository that track the given
// branch. Called by the GitHub push webhook handler when a branch is pushed.
// Returns the number of rows updated.
func (s *PreviewStore) UpdatePreviewGroupsLatestSHAForBranch(ctx context.Context, orgID, repositoryID uuid.UUID, branch, latestCommitSHA string) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE preview_groups
		SET latest_commit_sha = @latest_commit_sha, last_activity_at = now()
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND group_kind = 'branch'
		  AND branch = @branch
		  AND pinned = false`,
		pgx.NamedArgs{
			"org_id":            orgID,
			"repository_id":     repositoryID,
			"branch":            branch,
			"latest_commit_sha": latestCommitSHA,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("update preview groups latest sha for branch: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpdatePreviewGroupsLatestSHAForPR updates latest_commit_sha for all
// pull_request-kind preview groups matching the given PR number. Called by
// the GitHub PR synchronize webhook. Returns the number of rows updated.
func (s *PreviewStore) UpdatePreviewGroupsLatestSHAForPR(ctx context.Context, orgID, repositoryID uuid.UUID, prNumber int, latestCommitSHA string) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE preview_groups
		SET latest_commit_sha = @latest_commit_sha, last_activity_at = now()
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND group_kind = 'pull_request'
		  AND pull_request_number = @pull_request_number`,
		pgx.NamedArgs{
			"org_id":              orgID,
			"repository_id":       repositoryID,
			"pull_request_number": prNumber,
			"latest_commit_sha":   latestCommitSHA,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("update preview groups latest sha for pr: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *PreviewStore) UpdatePreviewGroupLatestSHA(ctx context.Context, orgID uuid.UUID, groupID uuid.UUID, latestCommitSHA string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE preview_groups
		SET latest_commit_sha = @latest_commit_sha, last_activity_at = now()
		WHERE id = @group_id AND org_id = @org_id`,
		pgx.NamedArgs{"org_id": orgID, "group_id": groupID, "latest_commit_sha": latestCommitSHA},
	)
	if err != nil {
		return fmt.Errorf("update preview group latest sha: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("preview group not found")
	}
	return nil
}

func (s *PreviewStore) GetPreviewGroup(ctx context.Context, orgID uuid.UUID, groupID uuid.UUID) (*models.PreviewGroup, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT %s FROM preview_groups WHERE id = @group_id AND org_id = @org_id`, previewGroupColumns),
		pgx.NamedArgs{"org_id": orgID, "group_id": groupID},
	)
	if err != nil {
		return nil, fmt.Errorf("query preview group: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewGroup])
	if err != nil {
		return nil, fmt.Errorf("get preview group: %w", err)
	}
	return &row, nil
}

func (s *PreviewStore) GetPreviewCurrentSummary(ctx context.Context, orgID uuid.UUID, groupID uuid.UUID) (models.PreviewCurrentSummary, error) {
	rows, err := s.db.Query(ctx, previewCurrentSummaryQuery("pg.id = @group_id", ""),
		pgx.NamedArgs{
			"org_id":        orgID,
			"group_id":      groupID,
			"repository_id": (*uuid.UUID)(nil),
			"q":             "",
			"pinned":        (*bool)(nil),
			"cursor_time":   (*time.Time)(nil),
			"cursor_id":     (*uuid.UUID)(nil),
			"limit":         1,
		},
	)
	if err != nil {
		return models.PreviewCurrentSummary{}, fmt.Errorf("query preview current summary: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewCurrentSummary])
	if err != nil {
		return models.PreviewCurrentSummary{}, fmt.Errorf("get preview current summary: %w", err)
	}
	return row, nil
}

func (s *PreviewStore) ListPreviewCurrentIndex(ctx context.Context, orgID uuid.UUID, filters PreviewCurrentIndexFilters) ([]models.PreviewCurrentSummary, error) {
	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	scopePredicate := previewCurrentScopePredicate(filters.Scope)
	query := previewCurrentSummaryQuery(scopePredicate, "ORDER BY pg.last_activity_at DESC, pg.id DESC LIMIT @limit")
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": filters.RepositoryID,
		"q":             strings.TrimSpace(filters.Query),
		"pinned":        filters.Pinned,
		"cursor_time":   filters.CursorTime,
		"cursor_id":     filters.CursorID,
		"limit":         limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list preview current index: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewCurrentSummary])
}

func (s *PreviewStore) CountPreviewCurrentIndexScopes(ctx context.Context, orgID uuid.UUID, filters PreviewCurrentIndexFilters) (map[string]int, error) {
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT
			COUNT(*) FILTER (WHERE `+previewCurrentEffectiveStatusSQL+` IN ('starting', 'ready', 'partially_ready', 'unhealthy', 'recycling'))::int AS running,
			COUNT(*) FILTER (WHERE `+previewCurrentEffectiveStatusSQL+` = 'warm')::int AS resumable,
			COUNT(*) FILTER (WHERE %s)::int AS attention,
			COUNT(*) FILTER (WHERE `+previewCurrentEffectiveStatusSQL+` IN ('stopped', 'expired', 'failed', 'unavailable') AND pg.last_activity_at >= now() - interval '7 days')::int AS recent
		FROM preview_groups pg
		LEFT JOIN LATERAL (
			SELECT status::text, base_commit_sha, stopped_reason
			FROM preview_instances
			WHERE org_id = pg.org_id AND preview_target_id = pg.current_target_id
			ORDER BY created_at DESC
			LIMIT 1
		) latest ON TRUE
		WHERE pg.org_id = @org_id
		  AND (@repository_id::uuid IS NULL OR pg.repository_id = @repository_id)
		  AND (@pinned::boolean IS NULL OR pg.pinned = @pinned)
		  AND (@q = '' OR pg.branch ILIKE '%%' || @q || '%%' OR pg.source_id ILIKE '%%' || @q || '%%')`, previewCurrentAttentionPredicate()),
		pgx.NamedArgs{
			"org_id":        orgID,
			"repository_id": filters.RepositoryID,
			"pinned":        filters.Pinned,
			"q":             strings.TrimSpace(filters.Query),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("count preview current index scopes: %w", err)
	}
	type counts struct {
		Running   int `db:"running"`
		Resumable int `db:"resumable"`
		Attention int `db:"attention"`
		Recent    int `db:"recent"`
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[counts])
	if err != nil {
		return nil, fmt.Errorf("scan preview current index counts: %w", err)
	}
	return map[string]int{"running": row.Running, "resumable": row.Resumable, "attention": row.Attention, "recent": row.Recent}, nil
}

func (s *PreviewStore) ListPreviewGroupHistory(ctx context.Context, orgID uuid.UUID, groupID uuid.UUID, cursorTime *time.Time, cursorID *uuid.UUID, limit int) ([]models.PreviewTargetHistory, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.Query(ctx, `
		SELECT target.id AS target_id, target.commit_sha, target.preview_config_name,
			target.source_type, target.source_id, target.created_at,
			target.commit_sha = pg.latest_commit_sha AS is_latest_head
		FROM preview_targets target
		JOIN preview_groups pg ON pg.id = target.preview_group_id AND pg.org_id = target.org_id
		WHERE target.org_id = @org_id
		  AND target.preview_group_id = @group_id
		  AND (@cursor_id::uuid IS NULL OR (target.created_at, target.id) < (@cursor_time, @cursor_id))
		ORDER BY target.created_at DESC, target.id DESC
		LIMIT @limit`,
		pgx.NamedArgs{"org_id": orgID, "group_id": groupID, "cursor_time": cursorTime, "cursor_id": cursorID, "limit": limit},
	)
	if err != nil {
		return nil, fmt.Errorf("list preview group history: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewTargetHistory])
}

// BackfillPreviewGroups assigns preview_group_id to all preview_targets rows
// that still have a NULL group pointer. It processes rows in ascending
// created_at order so the oldest target in a group becomes the group creator.
// Each batch is processed in a single call to UpsertPreviewGroupForTarget per
// target; the upsert is idempotent so rows already linked are skipped. Returns
// the total number of targets linked.
func (s *PreviewStore) BackfillPreviewGroups(ctx context.Context, orgID uuid.UUID, batchSize int) (int, error) {
	if batchSize <= 0 || batchSize > 1000 {
		batchSize = 500
	}
	total := 0
	for {
		rows, err := s.db.Query(ctx, `
			SELECT id, org_id, repository_id, branch, commit_sha, preview_config_name,
			       resolved_config_digest, source_type, source_id, source_url,
			       created_by_user_id, request_id, preview_group_id, created_at
			FROM preview_targets
			WHERE org_id = @org_id
			  AND preview_group_id IS NULL
			ORDER BY created_at ASC, id ASC
			LIMIT @batch_size`,
			pgx.NamedArgs{"org_id": orgID, "batch_size": batchSize},
		)
		if err != nil {
			return total, fmt.Errorf("backfill preview groups query: %w", err)
		}
		targets, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewTarget])
		if err != nil {
			return total, fmt.Errorf("backfill preview groups scan: %w", err)
		}
		if len(targets) == 0 {
			break
		}
		for _, target := range targets {
			if _, err := s.UpsertPreviewGroupForTarget(ctx, orgID, target, target.CommitSHA); err != nil {
				return total, fmt.Errorf("backfill upsert group for target %s: %w", target.ID, err)
			}
			total++
		}
		if len(targets) < batchSize {
			break
		}
	}
	return total, nil
}

func previewCurrentScopePredicate(scope string) string {
	switch scope {
	case "running":
		return previewCurrentEffectiveStatusSQL + " IN ('starting', 'ready', 'partially_ready', 'unhealthy', 'recycling')"
	case "resumable":
		return previewCurrentEffectiveStatusSQL + " = 'warm'"
	case "attention":
		return previewCurrentAttentionPredicate()
	case "recent":
		return previewCurrentEffectiveStatusSQL + " IN ('stopped', 'expired', 'failed', 'unavailable') AND pg.last_activity_at >= now() - interval '7 days'"
	default:
		return "TRUE"
	}
}

func previewCurrentAttentionPredicate() string {
	return `(` + previewCurrentEffectiveStatusSQL + ` IN ('failed', 'unavailable', 'blocked', 'capacity_blocked', 'config_invalid', 'outdated')
		OR pg.latest_commit_sha = ''
		OR (
			pg.pinned = false
			AND pg.latest_commit_sha <> ''
			AND COALESCE(latest.base_commit_sha, '') <> ''
			AND latest.base_commit_sha <> pg.latest_commit_sha
		))`
}

func previewCurrentSummaryQuery(extraPredicate, suffix string) string {
	if extraPredicate == "" {
		extraPredicate = "TRUE"
	}
	if suffix == "" {
		suffix = "LIMIT @limit"
	}
	return fmt.Sprintf(`
		SELECT %s
		FROM preview_groups pg
		JOIN repositories repo ON repo.id = pg.repository_id AND repo.org_id = pg.org_id
		LEFT JOIN LATERAL (
			SELECT id, status, base_commit_sha, expires_at, stopped_at, stopped_reason, error, current_phase
			FROM preview_instances
			WHERE org_id = pg.org_id AND preview_target_id = pg.current_target_id
			ORDER BY created_at DESC
			LIMIT 1
		) latest ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(pi.id)::int AS attempt_count, COUNT(DISTINCT target.id)::int AS target_count
			FROM preview_targets target
			LEFT JOIN preview_instances pi ON pi.org_id = target.org_id AND pi.preview_target_id = target.id
			WHERE target.org_id = pg.org_id AND target.preview_group_id = pg.id
		) stats ON TRUE
		WHERE pg.org_id = @org_id
		  AND (@repository_id::uuid IS NULL OR pg.repository_id = @repository_id)
		  AND (@pinned::boolean IS NULL OR pg.pinned = @pinned)
		  AND (@q = '' OR pg.branch ILIKE '%%' || @q || '%%'
		       OR pg.source_id ILIKE '%%' || @q || '%%'
		       OR repo.full_name ILIKE '%%' || @q || '%%')
		  AND (@cursor_id::uuid IS NULL OR (pg.last_activity_at, pg.id) < (@cursor_time, @cursor_id))
		  AND %s
		%s`, previewCurrentSummaryColumns, extraPredicate, suffix)
}
