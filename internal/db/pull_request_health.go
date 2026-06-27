package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const (
	prHealthCurrentSelectColumns = `pull_request_id, org_id, version, head_sha, base_sha, summary_json,
		summary_preview_json, enrichment_status, enriched_at, created_at, updated_at`
	prHealthSnapshotSelectColumns = `pull_request_id, org_id, version, head_sha, base_sha, summary_json,
		conflict_payload, failing_tests_payload, payload_size_bytes, enrichment_status, enriched_at, created_at`
	prRepairRunSelectColumns = `id, org_id, pull_request_id, session_id, thread_id, action_type, health_version,
		workspace_mode, active, obsoleted_by_version, created_at, updated_at,
		COALESCE(head_sha, '') AS head_sha, COALESCE(base_sha, '') AS base_sha,
		auto_attempt, trigger_reason, triggered_by_source, triggered_by_user_id`
)

func (s *PullRequestStore) beginTx(ctx context.Context) (pgx.Tx, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("db does not support transactions")
	}
	return txStarter.Begin(ctx)
}

func (s *PullRequestStore) GetHealthCurrent(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.PullRequestHealthCurrent, error) {
	query := `
		SELECT ` + prHealthCurrentSelectColumns + `
		FROM pull_request_health_current
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
	})
	if err != nil {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("query pull_request_health_current: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestHealthCurrent])
}

func (s *PullRequestStore) GetHealthSnapshot(ctx context.Context, orgID, pullRequestID uuid.UUID, version int64) (models.PullRequestHealthSnapshot, error) {
	query := `
		SELECT ` + prHealthSnapshotSelectColumns + `
		FROM pull_request_health_snapshots
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id AND version = @version`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"version":         version,
	})
	if err != nil {
		return models.PullRequestHealthSnapshot{}, fmt.Errorf("query pull_request_health_snapshots: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestHealthSnapshot])
}

func (s *PullRequestStore) ListOpenStaleForHealthSync(ctx context.Context, orgID uuid.UUID, before time.Time, limit int) ([]models.PullRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE org_id = @org_id
		  AND status = 'open'
		  AND (github_state_synced_at IS NULL OR github_state_synced_at < @before)
		ORDER BY github_state_synced_at ASC NULLS FIRST, updated_at ASC
		LIMIT @limit`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"before": before,
		"limit":  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list stale pull requests for health sync: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequest])
}

func (s *PullRequestStore) UpsertHealthSummary(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA, baseSHA string, summary models.PullRequestHealthSummary, preview json.RawMessage) (models.PullRequestHealthCurrent, error) {
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("marshal pull request health summary: %w", err)
	}
	if len(preview) == 0 {
		preview = summaryJSON
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("begin pull request health summary upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existing models.PullRequestHealthCurrent
	existingRows, err := tx.Query(ctx, `
		SELECT `+prHealthCurrentSelectColumns+`
		FROM pull_request_health_current
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
	})
	if err != nil {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("query existing pull request health current: %w", err)
	}
	existing, err = pgx.CollectOneRow(existingRows, pgx.RowToStructByName[models.PullRequestHealthCurrent])
	hasExisting := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("decode existing pull request health current: %w", err)
	}

	now := time.Now().UTC()
	version := int64(1)
	enrichmentStatus := models.PullRequestHealthEnrichmentStatusNotRequested
	var enrichedAt *time.Time
	if hasExisting {
		version = existing.Version
		if existing.HeadSHA != headSHA || existing.BaseSHA != baseSHA || !bytes.Equal(existing.SummaryJSON, summaryJSON) || !bytes.Equal(existing.SummaryPreviewJSON, preview) {
			version = existing.Version + 1
		} else {
			enrichmentStatus = existing.EnrichmentStatus
			enrichedAt = existing.EnrichedAt
		}
	}

	if !hasExisting || version != existing.Version {
		if _, err := tx.Exec(ctx, `
			INSERT INTO pull_request_health_snapshots (
				pull_request_id, org_id, version, head_sha, base_sha, summary_json,
				enrichment_status
			) VALUES (
				@pull_request_id, @org_id, @version, @head_sha, @base_sha, @summary_json,
				@enrichment_status
			)`, pgx.NamedArgs{
			"pull_request_id":   pullRequestID,
			"org_id":            orgID,
			"version":           version,
			"head_sha":          headSHA,
			"base_sha":          baseSHA,
			"summary_json":      summaryJSON,
			"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
		}); err != nil {
			return models.PullRequestHealthCurrent{}, fmt.Errorf("insert pull request health snapshot: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE pull_request_repair_runs
			SET active = false,
				obsoleted_by_version = @version,
				updated_at = now()
			WHERE org_id = @org_id
			  AND pull_request_id = @pull_request_id
			  AND active = true
			  AND health_version < @version
			  AND head_sha IS DISTINCT FROM @head_sha`, pgx.NamedArgs{
			"org_id":          orgID,
			"pull_request_id": pullRequestID,
			"version":         version,
			"head_sha":        headSHA,
		}); err != nil {
			return models.PullRequestHealthCurrent{}, fmt.Errorf("obsolete prior pull request repair runs: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO pull_request_health_current (
			pull_request_id, org_id, version, head_sha, base_sha, summary_json,
			summary_preview_json, enrichment_status, enriched_at, created_at, updated_at
		) VALUES (
			@pull_request_id, @org_id, @version, @head_sha, @base_sha, @summary_json,
			@summary_preview_json, @enrichment_status, @enriched_at, @created_at, @updated_at
		)
		ON CONFLICT (pull_request_id) DO UPDATE
		SET version = EXCLUDED.version,
			head_sha = EXCLUDED.head_sha,
			base_sha = EXCLUDED.base_sha,
			summary_json = EXCLUDED.summary_json,
			summary_preview_json = EXCLUDED.summary_preview_json,
			enrichment_status = EXCLUDED.enrichment_status,
			enriched_at = EXCLUDED.enriched_at,
			updated_at = EXCLUDED.updated_at`, pgx.NamedArgs{
		"pull_request_id":      pullRequestID,
		"org_id":               orgID,
		"version":              version,
		"head_sha":             headSHA,
		"base_sha":             baseSHA,
		"summary_json":         summaryJSON,
		"summary_preview_json": preview,
		"enrichment_status":    enrichmentStatus,
		"enriched_at":          enrichedAt,
		"created_at":           now,
		"updated_at":           now,
	}); err != nil {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("upsert pull request health current: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE pull_requests
		SET head_sha = @head_sha,
			base_sha = @base_sha,
			merge_state = @merge_state,
			has_conflicts = @has_conflicts,
			failing_test_count = @failing_test_count,
			needs_agent_action = @needs_agent_action,
			github_state_synced_at = now(),
			health_version = @version,
			updated_at = now()
		WHERE id = @pull_request_id AND org_id = @org_id`, pgx.NamedArgs{
		"pull_request_id":    pullRequestID,
		"org_id":             orgID,
		"head_sha":           headSHA,
		"base_sha":           baseSHA,
		"merge_state":        summary.MergeState,
		"has_conflicts":      summary.HasConflicts,
		"failing_test_count": summary.FailingTestCount,
		"needs_agent_action": summary.NeedsAgentAction,
		"version":            version,
	}); err != nil {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("update pull request health hot summary fields: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.PullRequestHealthCurrent{}, fmt.Errorf("commit pull request health summary upsert: %w", err)
	}

	return models.PullRequestHealthCurrent{
		PullRequestID:      pullRequestID,
		OrgID:              orgID,
		Version:            version,
		HeadSHA:            headSHA,
		BaseSHA:            baseSHA,
		SummaryJSON:        summaryJSON,
		SummaryPreviewJSON: preview,
		EnrichmentStatus:   enrichmentStatus,
		EnrichedAt:         enrichedAt,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

func (s *PullRequestStore) UpdateHealthEnrichment(ctx context.Context, orgID, pullRequestID uuid.UUID, version int64, conflictPayload, failingTestsPayload json.RawMessage, status models.PullRequestHealthEnrichmentStatus) error {
	payloadSize := len(conflictPayload) + len(failingTestsPayload)
	now := time.Now().UTC()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin pull request health enrichment update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE pull_request_health_snapshots
		SET conflict_payload = @conflict_payload,
			failing_tests_payload = @failing_tests_payload,
			payload_size_bytes = @payload_size_bytes,
			enrichment_status = @enrichment_status,
			enriched_at = @enriched_at
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND version = @version`, pgx.NamedArgs{
		"org_id":                orgID,
		"pull_request_id":       pullRequestID,
		"version":               version,
		"conflict_payload":      conflictPayload,
		"failing_tests_payload": failingTestsPayload,
		"payload_size_bytes":    payloadSize,
		"enrichment_status":     status,
		"enriched_at":           now,
	}); err != nil {
		return fmt.Errorf("update pull request health snapshot enrichment: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE pull_request_health_current
		SET enrichment_status = @enrichment_status,
			enriched_at = @enriched_at,
			updated_at = now()
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND version = @version`, pgx.NamedArgs{
		"org_id":            orgID,
		"pull_request_id":   pullRequestID,
		"version":           version,
		"enrichment_status": status,
		"enriched_at":       now,
	}); err != nil {
		return fmt.Errorf("update pull request health current enrichment: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PullRequestStore) GetActiveRepairRun(ctx context.Context, orgID, pullRequestID uuid.UUID, action models.PullRequestRepairActionType, healthVersion int64) (models.PullRequestRepairRun, error) {
	query := `
		SELECT ` + prRepairRunSelectColumns + `
		FROM pull_request_repair_runs
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND action_type = @action_type
		  AND health_version = @health_version
		  AND active = true
		ORDER BY created_at DESC
		LIMIT 1`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"action_type":     action,
		"health_version":  healthVersion,
	})
	if err != nil {
		return models.PullRequestRepairRun{}, fmt.Errorf("query active pull request repair run: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestRepairRun])
}

func (s *PullRequestStore) GetActiveRepairRunByHead(ctx context.Context, orgID, pullRequestID uuid.UUID, action models.PullRequestRepairActionType, headSHA string) (models.PullRequestRepairRun, error) {
	query := `
		SELECT ` + prRepairRunSelectColumns + `
		FROM pull_request_repair_runs
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND action_type = @action_type
		  AND head_sha = @head_sha
		  AND active = true
		ORDER BY created_at DESC
		LIMIT 1`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"action_type":     action,
		"head_sha":        headSHA,
	})
	if err != nil {
		return models.PullRequestRepairRun{}, fmt.Errorf("query active pull request repair run by head: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestRepairRun])
}

func (s *PullRequestStore) ListActiveRepairRuns(ctx context.Context, orgID, pullRequestID uuid.UUID, healthVersion int64) ([]models.PullRequestRepairRun, error) {
	query := `
		SELECT ` + prRepairRunSelectColumns + `
		FROM pull_request_repair_runs
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND health_version = @health_version
		  AND active = true
		ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"health_version":  healthVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("list active pull request repair runs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestRepairRun])
}

func (s *PullRequestStore) ListActiveRepairRunsByHead(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string) ([]models.PullRequestRepairRun, error) {
	query := `
		SELECT ` + prRepairRunSelectColumns + `
		FROM pull_request_repair_runs
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha = @head_sha
		  AND active = true
		ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"head_sha":        headSHA,
	})
	if err != nil {
		return nil, fmt.Errorf("list active pull request repair runs by head: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestRepairRun])
}

func (s *PullRequestStore) CountAutoRepairRunsByHead(ctx context.Context, orgID, pullRequestID uuid.UUID, action models.PullRequestRepairActionType, headSHA string) (int, error) {
	query := `
		SELECT count(*)
		FROM pull_request_repair_runs
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND action_type = @action_type
		  AND head_sha = @head_sha
		  AND auto_attempt = true`

	var count int
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"action_type":     action,
		"head_sha":        headSHA,
	}).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count automatic pull request repair runs by head: %w", err)
	}
	return count, nil
}

func (s *PullRequestStore) CreateRepairRun(ctx context.Context, run *models.PullRequestRepairRun) error {
	if run.WorkspaceMode == "" {
		run.WorkspaceMode = models.PullRequestRepairWorkspaceModeSnapshotContinuation
	}
	if run.TriggeredBySource == "" {
		run.TriggeredBySource = models.PullRequestRepairTriggerSourceManual
	}
	query := `
		INSERT INTO pull_request_repair_runs (
			org_id, pull_request_id, session_id, thread_id, action_type, health_version, workspace_mode, active, obsoleted_by_version,
			head_sha, base_sha, auto_attempt, trigger_reason, triggered_by_source, triggered_by_user_id
		) VALUES (
			@org_id, @pull_request_id, @session_id, @thread_id, @action_type, @health_version, @workspace_mode, @active, @obsoleted_by_version,
			@head_sha, @base_sha, @auto_attempt, @trigger_reason, @triggered_by_source, @triggered_by_user_id
		)
		RETURNING id, created_at, updated_at`

	return s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":               run.OrgID,
		"pull_request_id":      run.PullRequestID,
		"session_id":           run.SessionID,
		"thread_id":            run.ThreadID,
		"action_type":          run.ActionType,
		"health_version":       run.HealthVersion,
		"workspace_mode":       run.WorkspaceMode,
		"active":               run.Active,
		"obsoleted_by_version": run.ObsoletedByVersion,
		"head_sha":             run.HeadSHA,
		"base_sha":             run.BaseSHA,
		"auto_attempt":         run.AutoAttempt,
		"trigger_reason":       run.TriggerReason,
		"triggered_by_source":  run.TriggeredBySource,
		"triggered_by_user_id": run.TriggeredByUserID,
	}).Scan(&run.ID, &run.CreatedAt, &run.UpdatedAt)
}

func (s *PullRequestStore) DeactivateRepairRun(ctx context.Context, orgID, repairRunID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE pull_request_repair_runs
		SET active = false, updated_at = now()
		WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{
		"org_id": orgID,
		"id":     repairRunID,
	})
	return err
}
