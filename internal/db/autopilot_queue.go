package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type AutopilotQueueStore struct {
	db DBTX
}

func NewAutopilotQueueStore(db DBTX) *AutopilotQueueStore {
	return &AutopilotQueueStore{db: db}
}

type AutopilotQueueFilters struct {
	Cursor     string
	Limit      int
	Source     models.IssueSource
	RunState   models.AutopilotRunState
	Automation string
	RepoID     *uuid.UUID
	Query      string
	Sort       string
}

type AutopilotQueuePage struct {
	Rows       []models.AutopilotQueueRow
	NextCursor string
	Summary    models.AutopilotQueueSummary
}

type autopilotQueueDBRow struct {
	ID                     string `db:"id"`
	Rank                   int64  `db:"rank"`
	SourceType             string `db:"source_type"`
	SourceKey              string `db:"source_key"`
	Title                  string `db:"title"`
	IssueURL               sql.NullString
	RepoID                 sql.NullString
	RepoName               sql.NullString
	IssueStatus            string   `db:"issue_status"`
	CustomerImpactLabel    string   `db:"customer_impact_label"`
	CustomerImpactCount    int      `db:"customer_impact_count"`
	ImplementationEase     string   `db:"implementation_ease"`
	LowHangingFruitLabel   string   `db:"low_hanging_fruit_label"`
	LowHangingFruitReasons []string `db:"low_hanging_fruit_reasons"`
	ClusterSize            int64    `db:"cluster_size"`
	SessionID              sql.NullString
	SessionTitle           sql.NullString
	SessionUpdatedAt       sql.NullTime
	SessionStatus          sql.NullString
	SessionOrigin          sql.NullString
	SessionStartedAt       sql.NullTime
	SessionCompletedAt     sql.NullTime
	PRID                   sql.NullString
	PRNumber               sql.NullInt64
	PRURL                  sql.NullString
	PRStatus               sql.NullString
	PRMergedAt             sql.NullTime
	PRHeadSHA              sql.NullString
	PreviewTargetID        sql.NullString
	PreviewID              sql.NullString
	PreviewStatus          sql.NullString
	PreviewCommitSHA       sql.NullString
	SortScore              float64   `db:"sort_score"`
	ImpactScore            float64   `db:"impact_score"`
	EaseScore              float64   `db:"ease_score"`
	LastSeenAt             time.Time `db:"last_seen_at"`
}

func (s *AutopilotQueueStore) ListQueue(ctx context.Context, orgID uuid.UUID, filters AutopilotQueueFilters) (AutopilotQueuePage, error) {
	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset := 0
	if filters.Cursor != "" {
		parsed, err := strconv.Atoi(filters.Cursor)
		if err != nil || parsed < 0 {
			return AutopilotQueuePage{}, fmt.Errorf("invalid cursor")
		}
		offset = parsed
	}

	query, args := buildAutopilotQueueQuery(orgID, filters, limit+1, offset)
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return AutopilotQueuePage{}, fmt.Errorf("query autopilot queue: %w", err)
	}
	defer rows.Close()

	dbRows := make([]autopilotQueueDBRow, 0, limit)
	for rows.Next() {
		var row autopilotQueueDBRow
		if err := rows.Scan(
			&row.ID,
			&row.Rank,
			&row.SourceType,
			&row.SourceKey,
			&row.Title,
			&row.IssueURL,
			&row.RepoID,
			&row.RepoName,
			&row.IssueStatus,
			&row.CustomerImpactLabel,
			&row.CustomerImpactCount,
			&row.ImplementationEase,
			&row.LowHangingFruitLabel,
			&row.LowHangingFruitReasons,
			&row.ClusterSize,
			&row.SessionID,
			&row.SessionTitle,
			&row.SessionUpdatedAt,
			&row.SessionStatus,
			&row.SessionOrigin,
			&row.SessionStartedAt,
			&row.SessionCompletedAt,
			&row.PRID,
			&row.PRNumber,
			&row.PRURL,
			&row.PRStatus,
			&row.PRMergedAt,
			&row.PRHeadSHA,
			&row.PreviewTargetID,
			&row.PreviewID,
			&row.PreviewStatus,
			&row.PreviewCommitSHA,
			&row.SortScore,
			&row.ImpactScore,
			&row.EaseScore,
			&row.LastSeenAt,
		); err != nil {
			return AutopilotQueuePage{}, fmt.Errorf("scan autopilot queue: %w", err)
		}
		dbRows = append(dbRows, row)
	}
	if err := rows.Err(); err != nil {
		return AutopilotQueuePage{}, fmt.Errorf("iterate autopilot queue: %w", err)
	}

	hasMore := len(dbRows) > limit
	if hasMore {
		dbRows = dbRows[:limit]
	}

	out := AutopilotQueuePage{
		Rows: make([]models.AutopilotQueueRow, 0, len(dbRows)),
	}
	for _, row := range dbRows {
		modelRow := row.toModel()
		out.Rows = append(out.Rows, modelRow)
	}
	if hasMore {
		out.NextCursor = strconv.Itoa(offset + limit)
	}
	out.Summary = summarizeAutopilotQueue(out.Rows)
	return out, nil
}

func buildAutopilotQueueQuery(orgID uuid.UUID, filters AutopilotQueueFilters, limit int, offset int) (string, pgx.NamedArgs) {
	args := pgx.NamedArgs{
		"org_id":        orgID,
		"limit":         limit,
		"offset":        offset,
		"manual_source": models.IssueSourceManual,
	}

	where := []string{
		"i.org_id = @org_id",
		"i.deleted_at IS NULL",
		"i.source <> @manual_source",
		`(
			i.status IN ('open', 'triaged', 'in_progress')
			OR EXISTS (
				SELECT 1
				FROM session_issue_links active_sil
				JOIN sessions active_s ON active_s.id = active_sil.session_id
				WHERE active_sil.org_id = @org_id
				  AND active_s.org_id = @org_id
				  AND active_sil.issue_id = i.id
				  AND active_s.deleted_at IS NULL
				  AND active_s.status IN ('pending', 'running', 'awaiting_input', 'needs_human_guidance')
			)
		)`,
	}

	if filters.Source != "" {
		where = append(where, "i.source = @source")
		args["source"] = filters.Source
	}
	if filters.RepoID != nil {
		where = append(where, "i.repository_id = @repo_id")
		args["repo_id"] = *filters.RepoID
	}
	if strings.TrimSpace(filters.Query) != "" {
		where = append(where, "(i.title ILIKE @query OR i.external_id ILIKE @query)")
		args["query"] = "%" + strings.TrimSpace(filters.Query) + "%"
	}

	runStateSortExpression := `CASE
					WHEN i.session_status = 'pending' THEN 'queued'
					WHEN i.session_status = 'running' THEN 'running'
					WHEN i.session_status = 'awaiting_input' THEN 'awaiting_input'
					WHEN i.session_status = 'needs_human_guidance' THEN 'needs_review'
					WHEN i.session_status = 'failed' THEN 'failed'
					WHEN i.session_status = 'skipped' THEN 'skipped'
					WHEN i.pr_status = 'open' THEN 'pr_open'
					WHEN i.pr_status = 'merged' THEN 'merged'
					ELSE 'not_started'
				END`
	orderBy := "sort_score DESC NULLS LAST, impact_score DESC, ease_score DESC, i.last_seen_at DESC, i.id DESC"
	switch filters.Sort {
	case "impact":
		orderBy = "impact_score DESC, sort_score DESC NULLS LAST, ease_score DESC, i.last_seen_at DESC, i.id DESC"
	case "freshness":
		orderBy = "i.last_seen_at DESC, sort_score DESC NULLS LAST, impact_score DESC, ease_score DESC, i.id DESC"
	case "run_state":
		orderBy = runStateSortExpression + " ASC, sort_score DESC NULLS LAST, i.id DESC"
	}

	postWhere := []string{}
	if filters.RunState != "" {
		postWhere = append(postWhere, "i.display_run_state = @run_state")
		args["run_state"] = filters.RunState
	}
	switch filters.Automation {
	case "autorun_attempted":
		postWhere = append(postWhere, "i.trigger_mode = @trigger_mode")
		args["trigger_mode"] = models.AutopilotTriggerModeAuto
	case "manual_only":
		postWhere = append(postWhere, "(i.trigger_mode IS NULL OR i.trigger_mode <> @trigger_mode)")
		args["trigger_mode"] = models.AutopilotTriggerModeAuto
	case "ready_to_run":
		postWhere = append(postWhere, "i.available_action = @available_action")
		args["available_action"] = models.AutopilotQueueActionStartRun
	}
	postWhereSQL := "TRUE"
	if len(postWhere) > 0 {
		postWhereSQL = strings.Join(postWhere, " AND ")
	}

	query := fmt.Sprintf(`
		WITH ranked AS (
			SELECT
				i.id,
				i.external_id AS source_key,
				i.source AS source_type,
				i.title,
				COALESCE(
					i.raw_data->>'url',
					i.raw_data->>'permalink',
					i.raw_data->>'web_url',
					i.raw_data->>'webUrl',
					i.raw_data->>'external_url',
					i.raw_data#>>'{data,url}',
					i.raw_data#>>'{data,issue,url}',
					i.raw_data#>>'{data,issue,permalink}'
				) AS issue_url,
				i.repository_id AS repo_id,
				r.full_name AS repo_name,
				i.status AS issue_status,
				CASE
					WHEN i.affected_customer_count >= 25 OR i.severity IN ('critical', 'high') THEN 'High'
					WHEN i.affected_customer_count >= 5 THEN 'Medium'
					ELSE 'Low'
				END AS customer_impact_label,
				i.affected_customer_count AS customer_impact_count,
				CASE
					WHEN COALESCE(ce.tier, 3) <= 2 THEN 'High'
					WHEN COALESCE(ce.tier, 3) = 3 THEN 'Medium'
					ELSE 'Low'
				END AS implementation_ease,
				CASE
					WHEN COALESCE(ps.score, 0) >= 80 OR (i.affected_customer_count >= 25 AND COALESCE(ce.tier, 3) <= 2) THEN 'Very high'
					WHEN COALESCE(ps.score, 0) >= 60 OR (i.affected_customer_count >= 10 AND COALESCE(ce.tier, 3) <= 3) THEN 'High'
					WHEN COALESCE(ps.score, 0) >= 35 THEN 'Medium'
					ELSE 'Low'
				END AS low_hanging_fruit_label,
				ARRAY_REMOVE(ARRAY[
					CASE WHEN i.affected_customer_count >= 10 OR i.severity IN ('critical', 'high') THEN 'high customer impact' ELSE NULL END,
					CASE WHEN COALESCE(ce.tier, 3) <= 2 THEN 'straightforward implementation' ELSE NULL END,
					CASE WHEN ps.eligible_for_agent IS TRUE THEN 'eligible for automation' ELSE NULL END,
					CASE WHEN i.last_seen_at > now() - interval '7 days' THEN 'recent activity' ELSE NULL END
				], NULL)::text[] AS low_hanging_fruit_reasons,
				1::bigint AS cluster_size,
				latest.session_id,
				latest.session_title,
				latest.session_updated_at,
				latest.session_status,
				latest.session_origin,
				latest.session_started_at,
				latest.session_completed_at,
				latest.pr_id,
				latest.pr_number,
				latest.pr_url,
				latest.pr_status,
				latest.pr_merged_at,
				latest.pr_head_sha,
				preview.preview_target_id,
				preview.preview_id,
				preview.preview_status,
				preview.preview_commit_sha,
				COALESCE(ps.score, (
					(i.affected_customer_count::float * 2)
					+ CASE i.severity WHEN 'critical' THEN 30 WHEN 'high' THEN 20 WHEN 'medium' THEN 10 ELSE 0 END
					+ GREATEST(0, 30 - COALESCE(ce.tier, 3) * 7)
				)) AS sort_score,
				COALESCE(ps.customer_impact_score, i.affected_customer_count::float) AS impact_score,
				GREATEST(0, 100 - COALESCE(ce.tier, 3) * 20)::float AS ease_score,
				i.last_seen_at
			FROM issues i
			LEFT JOIN repositories r ON r.org_id = @org_id AND r.id = i.repository_id
			LEFT JOIN priority_scores ps ON ps.org_id = @org_id AND ps.issue_id = i.id
			LEFT JOIN complexity_estimates ce ON ce.org_id = @org_id AND ce.issue_id = i.id
			LEFT JOIN LATERAL (
				SELECT
					s.id AS session_id,
					COALESCE(s.title, i.title) AS session_title,
					s.last_activity_at AS session_updated_at,
					s.status AS session_status,
					s.origin AS session_origin,
					s.started_at AS session_started_at,
					s.completed_at AS session_completed_at,
					pr.id AS pr_id,
					pr.github_pr_number AS pr_number,
					pr.github_pr_url AS pr_url,
					pr.status AS pr_status,
					pr.merged_at AS pr_merged_at,
					pr.github_repo AS pr_repo,
					COALESCE(pr.head_sha, '') AS pr_head_sha
				FROM session_issue_links sil
				JOIN sessions s ON s.id = sil.session_id
				LEFT JOIN LATERAL (
					SELECT id, github_pr_number, github_pr_url, github_repo, status, merged_at, updated_at, head_sha
					FROM pull_requests pr
					WHERE pr.org_id = @org_id
					  AND pr.session_id = s.id
					ORDER BY pr.updated_at DESC, pr.id DESC
					LIMIT 1
				) pr ON true
				WHERE sil.org_id = @org_id
				  AND sil.issue_id = i.id
				  AND s.org_id = @org_id
				  AND s.deleted_at IS NULL
				ORDER BY
					CASE
						WHEN s.status IN ('pending', 'running') THEN 1
						WHEN s.status IN ('awaiting_input', 'needs_human_guidance') THEN 2
						WHEN pr.status = 'open' THEN 3
						ELSE 4
					END,
					s.last_activity_at DESC,
					s.id DESC
				LIMIT 1
			) latest ON true
			LEFT JOIN LATERAL (
				SELECT
					target.id AS preview_target_id,
					preview.id AS preview_id,
					COALESCE(preview.status, 'target_created') AS preview_status,
					target.commit_sha AS preview_commit_sha
				FROM preview_targets target
				LEFT JOIN LATERAL (
					SELECT pi.id, pi.status, pi.created_at
					FROM preview_instances pi
					WHERE pi.org_id = @org_id
					  AND pi.preview_target_id = target.id
					ORDER BY pi.created_at DESC, pi.id DESC
					LIMIT 1
				) preview ON true
				WHERE latest.pr_id IS NOT NULL
				  AND target.org_id = @org_id
				  AND target.repository_id = i.repository_id
				  AND target.source_type = 'pull_request'
				  AND (
				      target.source_id = (latest.pr_repo || '#' || latest.pr_number::text)
				      OR target.source_id LIKE (latest.pr_repo || '#' || latest.pr_number::text || '@%%')
				  )
				ORDER BY target.created_at DESC, target.id DESC
				LIMIT 1
			) preview ON true
			WHERE %s
		),
		projected AS (
			SELECT
				i.*,
				row_number() OVER (ORDER BY %s)::bigint AS rank,
				CASE
					WHEN i.session_status = 'pending' THEN 'queued'
					WHEN i.session_status = 'running' THEN 'running'
					WHEN i.session_status = 'awaiting_input' THEN 'awaiting_input'
					WHEN i.session_status = 'needs_human_guidance' THEN 'needs_review'
					WHEN i.session_status = 'failed' THEN 'failed'
					WHEN i.session_status = 'skipped' THEN 'skipped'
					WHEN i.pr_status = 'open' THEN 'pr_open'
					WHEN i.pr_status = 'merged' THEN 'merged'
					ELSE 'not_started'
				END AS display_run_state,
				CASE
					WHEN i.session_id IS NULL THEN NULL
					WHEN i.session_origin IN ('automation', 'project') THEN 'auto'
					ELSE 'manual'
				END AS trigger_mode,
				CASE
					WHEN i.session_status IN ('pending', 'running') THEN 'view_run'
					WHEN i.session_status IN ('awaiting_input', 'needs_human_guidance') THEN 'review'
					WHEN i.session_status = 'failed' THEN 'retry'
					WHEN i.session_status = 'skipped' THEN 'blocked'
					WHEN i.pr_status IN ('open', 'merged') THEN 'open_pr'
					WHEN i.repo_id IS NULL THEN 'blocked'
					ELSE 'start_run'
				END AS available_action
			FROM ranked i
		)
		SELECT
			i.id,
			i.rank,
			i.source_type,
			i.source_key,
			i.title,
			i.issue_url,
			i.repo_id,
			i.repo_name,
			i.issue_status,
			i.customer_impact_label,
			i.customer_impact_count,
			i.implementation_ease,
			i.low_hanging_fruit_label,
			i.low_hanging_fruit_reasons,
			i.cluster_size,
			i.session_id,
			i.session_title,
			i.session_updated_at,
			i.session_status,
			i.session_origin,
			i.session_started_at,
			i.session_completed_at,
			i.pr_id,
			i.pr_number,
			i.pr_url,
			i.pr_status,
			i.pr_merged_at,
			i.pr_head_sha,
			i.preview_target_id,
			i.preview_id,
			i.preview_status,
			i.preview_commit_sha,
			i.sort_score,
			i.impact_score,
			i.ease_score,
			i.last_seen_at
		FROM projected i
		WHERE %s
		ORDER BY %s
		LIMIT @limit OFFSET @offset`,
		strings.Join(where, " AND "),
		orderBy,
		postWhereSQL,
		orderBy,
	)
	return query, args
}

func (r autopilotQueueDBRow) toModel() models.AutopilotQueueRow {
	row := models.AutopilotQueueRow{
		ID:          uuid.MustParse(r.ID),
		Rank:        int(r.Rank),
		Source:      models.AutopilotIssueSource{Type: models.IssueSource(r.SourceType), Key: r.SourceKey},
		Title:       r.Title,
		IssueStatus: models.IssueStatus(r.IssueStatus),
		CustomerImpact: models.AutopilotCustomerImpact{
			Label: r.CustomerImpactLabel,
			Count: r.CustomerImpactCount,
		},
		ImplementationEase: r.ImplementationEase,
		LowHangingFruit: models.AutopilotLowHangingFruit{
			Label:       r.LowHangingFruitLabel,
			Reasons:     r.LowHangingFruitReasons,
			ClusterSize: int(r.ClusterSize),
		},
	}
	if r.IssueURL.Valid && strings.TrimSpace(r.IssueURL.String) != "" {
		issueURL := strings.TrimSpace(r.IssueURL.String)
		row.IssueURL = &issueURL
	}
	if r.RepoID.Valid && r.RepoName.Valid {
		row.Repo = &models.AutopilotRepoRef{ID: uuid.MustParse(r.RepoID.String), Name: r.RepoName.String}
	}
	if r.SessionID.Valid && r.SessionUpdatedAt.Valid {
		title := r.Title
		if r.SessionTitle.Valid {
			title = r.SessionTitle.String
		}
		row.LatestSession = &models.AutopilotSessionRef{
			ID:        uuid.MustParse(r.SessionID.String),
			Title:     title,
			UpdatedAt: r.SessionUpdatedAt.Time,
		}
		status := models.SessionStatus("")
		if r.SessionStatus.Valid {
			status = models.SessionStatus(r.SessionStatus.String)
		}
		var startedAt *time.Time
		if r.SessionStartedAt.Valid {
			startedAt = &r.SessionStartedAt.Time
		}
		row.LatestAgentRun = &models.AutopilotAgentRunRef{
			ID:          uuid.MustParse(r.SessionID.String),
			Status:      status,
			TriggerMode: triggerModeFromOrigin(r.SessionOrigin),
			StartedAt:   startedAt,
		}
	}
	if r.PRID.Valid && r.PRNumber.Valid && r.PRURL.Valid && r.PRStatus.Valid {
		var mergedAt *time.Time
		if r.PRMergedAt.Valid {
			mergedAt = &r.PRMergedAt.Time
		}
		row.LatestPR = &models.AutopilotPullRequestRef{
			ID:       uuid.MustParse(r.PRID.String),
			Number:   int(r.PRNumber.Int64),
			URL:      r.PRURL.String,
			Status:   models.PullRequestStatus(r.PRStatus.String),
			MergedAt: mergedAt,
		}
	}
	if r.PreviewTargetID.Valid {
		if targetID, err := uuid.Parse(r.PreviewTargetID.String); err == nil {
			preview := &models.AutopilotPreviewRef{
				TargetID: targetID,
				Status:   models.AutopilotPreviewStatusTargetCreated,
			}
			if r.PreviewID.Valid {
				if id, parseErr := uuid.Parse(r.PreviewID.String); parseErr == nil {
					preview.PreviewID = &id
				}
			}
			if r.PreviewStatus.Valid && strings.TrimSpace(r.PreviewStatus.String) != "" {
				preview.Status = models.AutopilotPreviewStatus(r.PreviewStatus.String)
			}
			if r.PreviewCommitSHA.Valid {
				preview.CommitSHA = strings.TrimSpace(r.PreviewCommitSHA.String)
			}
			if r.PRHeadSHA.Valid {
				preview.LatestCommitSHA = strings.TrimSpace(r.PRHeadSHA.String)
			}
			preview.NewCommitsAvailable = preview.CommitSHA != "" &&
				preview.LatestCommitSHA != "" &&
				preview.CommitSHA != preview.LatestCommitSHA
			row.LatestPreview = preview
		}
	}
	row.DisplayRunState, row.AvailableAction, row.ActionDisabledReason = deriveAutopilotRunStateAction(r)
	return row
}

func triggerModeFromOrigin(origin sql.NullString) models.AutopilotTriggerMode {
	if origin.Valid {
		switch models.SessionOrigin(origin.String) {
		case models.SessionOriginAutomation, models.SessionOriginProject:
			return models.AutopilotTriggerModeAuto
		}
	}
	return models.AutopilotTriggerModeManual
}

func deriveAutopilotRunStateAction(row autopilotQueueDBRow) (models.AutopilotRunState, models.AutopilotQueueAction, *string) {
	if row.SessionStatus.Valid {
		switch row.SessionStatus.String {
		case "pending":
			return models.AutopilotRunStateQueued, models.AutopilotQueueActionViewRun, nil
		case "running":
			return models.AutopilotRunStateRunning, models.AutopilotQueueActionViewRun, nil
		case "awaiting_input":
			return models.AutopilotRunStateAwaitingInput, models.AutopilotQueueActionReview, nil
		case "needs_human_guidance":
			return models.AutopilotRunStateNeedsReview, models.AutopilotQueueActionReview, nil
		case "failed":
			return models.AutopilotRunStateFailed, models.AutopilotQueueActionRetry, nil
		case "skipped":
			reason := "Automation policy skipped this issue."
			return models.AutopilotRunStateSkipped, models.AutopilotQueueActionBlocked, &reason
		}
	}
	if row.PRStatus.Valid {
		switch models.PullRequestStatus(row.PRStatus.String) {
		case models.PullRequestStatusOpen:
			return models.AutopilotRunStatePROpen, models.AutopilotQueueActionOpenPR, nil
		case models.PullRequestStatusMerged:
			return models.AutopilotRunStateMerged, models.AutopilotQueueActionOpenPR, nil
		}
	}
	if !row.RepoID.Valid {
		reason := "Select a repository before starting a run."
		return models.AutopilotRunStateNotStarted, models.AutopilotQueueActionBlocked, &reason
	}
	return models.AutopilotRunStateNotStarted, models.AutopilotQueueActionStartRun, nil
}

func summarizeAutopilotQueue(rows []models.AutopilotQueueRow) models.AutopilotQueueSummary {
	summary := models.AutopilotQueueSummary{RankedIssueCount: len(rows)}
	if len(rows) > 0 {
		summary.TopIssueID = &rows[0].ID
		var analyzedAt time.Time
		for _, row := range rows {
			switch row.DisplayRunState {
			case models.AutopilotRunStateRunning, models.AutopilotRunStateQueued:
				summary.ActiveRunCount++
			case models.AutopilotRunStateAwaitingInput, models.AutopilotRunStateNeedsReview:
				summary.NeedsReviewCount++
			case models.AutopilotRunStatePROpen:
				summary.OpenPRCount++
			case models.AutopilotRunStateNotStarted:
				if row.AvailableAction == models.AutopilotQueueActionStartRun {
					summary.AutorunnableCount++
				}
			}
			if row.LatestSession != nil && row.LatestSession.UpdatedAt.After(analyzedAt) {
				analyzedAt = row.LatestSession.UpdatedAt
			}
		}
		if !analyzedAt.IsZero() {
			summary.AnalyzedAt = &analyzedAt
		}
	}
	return summary
}
