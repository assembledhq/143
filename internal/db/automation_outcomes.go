package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrAutomationOutcomeAlreadyReported = errors.New("automation run outcome already reported with different values")

type AutomationOutcomeStore struct {
	db TxStarter
}

func NewAutomationOutcomeStore(db TxStarter) *AutomationOutcomeStore {
	return &AutomationOutcomeStore{db: db}
}

const automationOutcomeColumns = `id, org_id, automation_id, automation_run_id, session_id,
	repository, pull_request_number, pull_request_url, pull_request_title, head_sha,
	decision, reason, source, reported_at, created_at`

func scanAutomationOutcome(row pgx.Row) (models.AutomationRunOutcome, error) {
	var outcome models.AutomationRunOutcome
	err := row.Scan(
		&outcome.ID, &outcome.OrgID, &outcome.AutomationID, &outcome.AutomationRunID, &outcome.SessionID,
		&outcome.Repository, &outcome.PullRequestNumber, &outcome.PullRequestURL, &outcome.PullRequestTitle, &outcome.HeadSHA,
		&outcome.Decision, &outcome.Reason, &outcome.Source, &outcome.ReportedAt, &outcome.CreatedAt,
	)
	return outcome, err
}

// Create records a run's final business outcome once. Identical retries are
// idempotent; a second, different report is rejected so the audit record cannot
// silently change after the agent has completed.
func (s *AutomationOutcomeStore) Create(ctx context.Context, orgID uuid.UUID, outcome *models.AutomationRunOutcome, action *models.AutomationRunExternalAction) (models.AutomationRunOutcome, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.AutomationRunOutcome{}, fmt.Errorf("begin automation outcome transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `INSERT INTO automation_run_outcomes (
		org_id, automation_id, automation_run_id, session_id,
		repository, pull_request_number, pull_request_url, pull_request_title, head_sha,
		decision, reason, source
	) VALUES (
		@org_id, @automation_id, @automation_run_id, @session_id,
		@repository, @pull_request_number, @pull_request_url, @pull_request_title, @head_sha,
		@decision, @reason, @source
	)
	ON CONFLICT (org_id, automation_run_id) DO NOTHING
	RETURNING ` + automationOutcomeColumns

	created, scanErr := scanAutomationOutcome(tx.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":              orgID,
		"automation_id":       outcome.AutomationID,
		"automation_run_id":   outcome.AutomationRunID,
		"session_id":          outcome.SessionID,
		"repository":          outcome.Repository,
		"pull_request_number": outcome.PullRequestNumber,
		"pull_request_url":    outcome.PullRequestURL,
		"pull_request_title":  outcome.PullRequestTitle,
		"head_sha":            outcome.HeadSHA,
		"decision":            outcome.Decision,
		"reason":              outcome.Reason,
		"source":              outcome.Source,
	}))
	if errors.Is(scanErr, pgx.ErrNoRows) {
		existing, getErr := getAutomationOutcomeByRunID(ctx, tx, orgID, outcome.AutomationRunID)
		if getErr != nil {
			return models.AutomationRunOutcome{}, getErr
		}
		if !sameAutomationOutcome(existing, *outcome, action) {
			return models.AutomationRunOutcome{}, ErrAutomationOutcomeAlreadyReported
		}
		if err := tx.Commit(ctx); err != nil {
			return models.AutomationRunOutcome{}, fmt.Errorf("commit idempotent automation outcome transaction: %w", err)
		}
		return existing, nil
	}
	if scanErr != nil {
		return models.AutomationRunOutcome{}, fmt.Errorf("insert automation outcome: %w", scanErr)
	}

	if action != nil {
		action.OrgID = orgID
		action.OutcomeID = created.ID
		if err := tx.QueryRow(ctx, `INSERT INTO automation_run_external_actions (
			org_id, outcome_id, provider, action_type, external_id, url, verification_status
		) VALUES (
			@org_id, @outcome_id, @provider, @action_type, @external_id, @url, @verification_status
		)
		RETURNING id, created_at`, pgx.NamedArgs{
			"org_id":              orgID,
			"outcome_id":          created.ID,
			"provider":            action.Provider,
			"action_type":         action.ActionType,
			"external_id":         action.ExternalID,
			"url":                 action.URL,
			"verification_status": action.VerificationStatus,
		}).Scan(&action.ID, &action.CreatedAt); err != nil {
			return models.AutomationRunOutcome{}, fmt.Errorf("insert automation external action: %w", err)
		}
		created.ExternalAction = action
	}

	if err := tx.Commit(ctx); err != nil {
		return models.AutomationRunOutcome{}, fmt.Errorf("commit automation outcome transaction: %w", err)
	}
	return created, nil
}

func sameAutomationOutcome(existing, requested models.AutomationRunOutcome, requestedAction *models.AutomationRunExternalAction) bool {
	if existing.AutomationID != requested.AutomationID ||
		existing.AutomationRunID != requested.AutomationRunID ||
		existing.SessionID != requested.SessionID ||
		existing.Repository != requested.Repository ||
		existing.PullRequestNumber != requested.PullRequestNumber ||
		existing.PullRequestURL != requested.PullRequestURL ||
		stringValue(existing.PullRequestTitle) != stringValue(requested.PullRequestTitle) ||
		stringValue(existing.HeadSHA) != stringValue(requested.HeadSHA) ||
		existing.Decision != requested.Decision ||
		existing.Reason != requested.Reason ||
		existing.Source != requested.Source {
		return false
	}
	if existing.ExternalAction == nil || requestedAction == nil {
		return existing.ExternalAction == nil && requestedAction == nil
	}
	return existing.ExternalAction.Provider == requestedAction.Provider &&
		existing.ExternalAction.ActionType == requestedAction.ActionType &&
		stringValue(existing.ExternalAction.ExternalID) == stringValue(requestedAction.ExternalID) &&
		existing.ExternalAction.URL == requestedAction.URL
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func getAutomationOutcomeByRunID(ctx context.Context, q DBTX, orgID, runID uuid.UUID) (models.AutomationRunOutcome, error) {
	query := `SELECT ` + automationOutcomeColumns + ` FROM automation_run_outcomes
		WHERE org_id = @org_id AND automation_run_id = @automation_run_id`
	outcome, err := scanAutomationOutcome(q.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "automation_run_id": runID}))
	if err != nil {
		return models.AutomationRunOutcome{}, fmt.Errorf("get automation outcome: %w", err)
	}
	var action models.AutomationRunExternalAction
	err = q.QueryRow(ctx, `SELECT id, org_id, outcome_id, provider, action_type, external_id, url, verification_status, created_at
		FROM automation_run_external_actions
		WHERE org_id = @org_id AND outcome_id = @outcome_id`, pgx.NamedArgs{
		"org_id": orgID, "outcome_id": outcome.ID,
	}).Scan(&action.ID, &action.OrgID, &action.OutcomeID, &action.Provider, &action.ActionType, &action.ExternalID, &action.URL, &action.VerificationStatus, &action.CreatedAt)
	if err == nil {
		outcome.ExternalAction = &action
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationRunOutcome{}, fmt.Errorf("get automation external action: %w", err)
	}
	return outcome, nil
}

func (s *AutomationOutcomeStore) GetByRunID(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRunOutcome, error) {
	return getAutomationOutcomeByRunID(ctx, s.db, orgID, runID)
}

type AutomationDecisionFilters struct {
	Limit              int
	Cursor             string
	Decision           *models.AutomationOutcomeDecision
	OutcomeNotReported bool
	PullRequestNumber  int
}

const (
	automationDecisionTargetCTEs = `WITH raw_targeted AS (
	SELECT
		ar.id AS run_id,
		ar.automation_id,
		ar.org_id,
		ar.status AS execution_status,
		ar.triggered_at,
		ar.completed_at,
		s.id AS session_id,
		ar.config_snapshot #>> '{github,repository}' AS repository,
		CASE
			WHEN ar.config_snapshot #>> '{github,pull_request_number}' ~ '^[1-9][0-9]*$'
			THEN (ar.config_snapshot #>> '{github,pull_request_number}')::integer
		END AS pull_request_number,
		COALESCE(
			NULLIF(ar.config_snapshot #>> '{github,pull_request_url}', ''),
			'https://github.com/' || (ar.config_snapshot #>> '{github,repository}') || '/pull/' || (ar.config_snapshot #>> '{github,pull_request_number}')
		) AS pull_request_url,
		COALESCE(
			NULLIF(ar.config_snapshot #>> '{github,pull_request_title}', ''),
			NULLIF(target_outcome.pull_request_title, '')
		) AS pull_request_title,
		COALESCE(
			NULLIF(ar.config_snapshot #>> '{github,head_sha}', ''),
			NULLIF(target_outcome.head_sha, '')
		) AS head_sha
	FROM automation_runs ar
	LEFT JOIN automation_run_outcomes target_outcome
	  ON target_outcome.org_id = ar.org_id
	 AND target_outcome.automation_run_id = ar.id
	LEFT JOIN LATERAL (
		SELECT sal.session_id AS id
		FROM session_automation_links sal
		WHERE sal.org_id = ar.org_id AND sal.automation_run_id = ar.id
		ORDER BY sal.created_at DESC
		LIMIT 1
	) s ON true
	WHERE ar.org_id = @org_id
	  AND ar.automation_id = @automation_id
	  AND ar.triggered_by = 'github'
	  AND ar.config_snapshot #>> '{github,repository}' <> ''
	  AND ar.config_snapshot #>> '{github,pull_request_number}' ~ '^[1-9][0-9]*$'
), ranked AS (
	SELECT raw_targeted.*,
		count(*) OVER (
			PARTITION BY repository, pull_request_number, COALESCE(head_sha, '')
		) AS attempt_count,
		row_number() OVER (
			PARTITION BY repository, pull_request_number, COALESCE(head_sha, '')
			ORDER BY triggered_at DESC, run_id DESC
		) AS revision_rank
	FROM raw_targeted
), latest AS (
	SELECT * FROM ranked WHERE revision_rank = 1
)`
	outcomeHistoriesCTE = `, outcome_histories AS (
	SELECT
		history_run.repository,
		history_run.pull_request_number,
		COALESCE(history_run.head_sha, '') AS head_sha_key,
		jsonb_agg(
			to_jsonb(history_outcome) || jsonb_build_object(
				'external_action', CASE
					WHEN history_action.id IS NULL THEN 'null'::jsonb
					ELSE to_jsonb(history_action)
				END
			)
			ORDER BY history_outcome.reported_at DESC, history_outcome.id DESC
		) AS attempt_outcomes
	FROM raw_targeted history_run
	JOIN automation_run_outcomes history_outcome
	  ON history_outcome.org_id = history_run.org_id
	 AND history_outcome.automation_run_id = history_run.run_id
	LEFT JOIN automation_run_external_actions history_action
	  ON history_action.org_id = history_outcome.org_id
	 AND history_action.outcome_id = history_outcome.id
	GROUP BY history_run.repository, history_run.pull_request_number, COALESCE(history_run.head_sha, '')
)`
)

func (s *AutomationOutcomeStore) ListDecisions(ctx context.Context, orgID, automationID uuid.UUID, filters AutomationDecisionFilters) ([]models.AutomationDecision, error) {
	query := automationDecisionTargetCTEs + outcomeHistoriesCTE + `
	SELECT
		l.automation_id, l.run_id, l.session_id,
		l.repository, l.pull_request_number, l.pull_request_url, l.pull_request_title, l.head_sha,
		l.execution_status, l.triggered_at, l.completed_at, l.attempt_count,
		o.id, o.org_id, o.automation_id, o.automation_run_id, o.session_id,
		o.repository, o.pull_request_number, o.pull_request_url, o.pull_request_title, o.head_sha,
		o.decision, o.reason, o.source, o.reported_at, o.created_at,
		a.id, a.org_id, a.outcome_id, a.provider, a.action_type, a.external_id, a.url, a.verification_status, a.created_at,
		h.attempt_outcomes
	FROM latest l
	LEFT JOIN automation_run_outcomes o
	  ON o.org_id = l.org_id AND o.automation_run_id = l.run_id
	LEFT JOIN automation_run_external_actions a
	  ON a.org_id = o.org_id AND a.outcome_id = o.id
	LEFT JOIN outcome_histories h
	  ON h.repository = l.repository
	 AND h.pull_request_number = l.pull_request_number
	 AND h.head_sha_key = COALESCE(l.head_sha, '')
	WHERE true`
	args := pgx.NamedArgs{"org_id": orgID, "automation_id": automationID}
	if filters.Decision != nil {
		query += ` AND EXISTS (
			SELECT 1
			FROM raw_targeted filter_run
			JOIN automation_run_outcomes filter_outcome
			  ON filter_outcome.org_id = filter_run.org_id
			 AND filter_outcome.automation_run_id = filter_run.run_id
			WHERE filter_run.repository = l.repository
			  AND filter_run.pull_request_number = l.pull_request_number
			  AND COALESCE(filter_run.head_sha, '') = COALESCE(l.head_sha, '')
			  AND filter_outcome.decision = @decision
		)`
		args["decision"] = *filters.Decision
	} else if filters.OutcomeNotReported {
		query += ` AND o.id IS NULL AND l.execution_status NOT IN ('pending', 'running', 'failed')`
	}
	if filters.PullRequestNumber > 0 {
		query += ` AND l.pull_request_number = @pull_request_number`
		args["pull_request_number"] = filters.PullRequestNumber
	}
	if filters.Cursor != "" {
		if cursorID, err := uuid.Parse(filters.Cursor); err == nil {
			query += ` AND (l.triggered_at < (SELECT triggered_at FROM automation_runs WHERE id = @cursor_id AND org_id = @org_id AND automation_id = @automation_id)
				OR (l.triggered_at = (SELECT triggered_at FROM automation_runs WHERE id = @cursor_id AND org_id = @org_id AND automation_id = @automation_id) AND l.run_id < @cursor_id))`
			args["cursor_id"] = cursorID
		}
	}
	query += ` ORDER BY l.triggered_at DESC, l.run_id DESC`
	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("list automation decisions: %w", err)
	}
	defer rows.Close()

	decisions := make([]models.AutomationDecision, 0)
	for rows.Next() {
		decision, err := scanAutomationDecision(rows)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate automation decisions: %w", err)
	}
	return decisions, nil
}

func scanAutomationDecision(row pgx.Row) (models.AutomationDecision, error) {
	var decision models.AutomationDecision
	var attemptCount int64
	var outcomeID, outcomeOrgID, outcomeAutomationID, outcomeRunID, outcomeSessionID *uuid.UUID
	var outcomeRepository, outcomeURL, outcomeDecision, outcomeReason, outcomeSource *string
	var outcomePRNumber *int
	var outcomeTitle, outcomeHeadSHA *string
	var outcomeReportedAt, outcomeCreatedAt *time.Time
	var actionID, actionOrgID, actionOutcomeID *uuid.UUID
	var actionProvider, actionType, actionExternalID, actionURL, actionVerification *string
	var actionCreatedAt *time.Time
	var attemptOutcomesJSON []byte
	err := row.Scan(
		&decision.AutomationID, &decision.RunID, &decision.SessionID,
		&decision.Target.Repository, &decision.Target.PullRequestNumber, &decision.Target.PullRequestURL, &decision.Target.PullRequestTitle, &decision.Target.HeadSHA,
		&decision.ExecutionStatus, &decision.TriggeredAt, &decision.CompletedAt, &attemptCount,
		&outcomeID, &outcomeOrgID, &outcomeAutomationID, &outcomeRunID, &outcomeSessionID,
		&outcomeRepository, &outcomePRNumber, &outcomeURL, &outcomeTitle, &outcomeHeadSHA,
		&outcomeDecision, &outcomeReason, &outcomeSource, &outcomeReportedAt, &outcomeCreatedAt,
		&actionID, &actionOrgID, &actionOutcomeID, &actionProvider, &actionType, &actionExternalID, &actionURL, &actionVerification, &actionCreatedAt,
		&attemptOutcomesJSON,
	)
	if err != nil {
		return models.AutomationDecision{}, fmt.Errorf("scan automation decision: %w", err)
	}
	decision.AttemptCount = int(attemptCount)
	if outcomeID != nil && outcomeOrgID != nil && outcomeAutomationID != nil && outcomeRunID != nil && outcomeSessionID != nil && outcomeRepository != nil && outcomePRNumber != nil && outcomeURL != nil && outcomeDecision != nil && outcomeReason != nil && outcomeSource != nil && outcomeReportedAt != nil && outcomeCreatedAt != nil {
		outcome := &models.AutomationRunOutcome{
			ID: *outcomeID, OrgID: *outcomeOrgID, AutomationID: *outcomeAutomationID,
			AutomationRunID: *outcomeRunID, SessionID: *outcomeSessionID,
			Repository: *outcomeRepository, PullRequestNumber: *outcomePRNumber, PullRequestURL: *outcomeURL,
			PullRequestTitle: outcomeTitle, HeadSHA: outcomeHeadSHA,
			Decision: models.AutomationOutcomeDecision(*outcomeDecision), Reason: *outcomeReason,
			Source: models.AutomationOutcomeSource(*outcomeSource), ReportedAt: *outcomeReportedAt, CreatedAt: *outcomeCreatedAt,
		}
		if actionID != nil && actionOrgID != nil && actionOutcomeID != nil && actionProvider != nil && actionType != nil && actionURL != nil && actionVerification != nil && actionCreatedAt != nil {
			outcome.ExternalAction = &models.AutomationRunExternalAction{
				ID: *actionID, OrgID: *actionOrgID, OutcomeID: *actionOutcomeID,
				Provider: *actionProvider, ActionType: models.AutomationExternalActionType(*actionType),
				ExternalID: actionExternalID, URL: *actionURL,
				VerificationStatus: models.AutomationExternalActionVerificationStatus(*actionVerification), CreatedAt: *actionCreatedAt,
			}
		}
		decision.Outcome = outcome
	}
	decision.AttemptOutcomes = make([]models.AutomationRunOutcome, 0)
	if len(attemptOutcomesJSON) > 0 {
		if err := json.Unmarshal(attemptOutcomesJSON, &decision.AttemptOutcomes); err != nil {
			return models.AutomationDecision{}, fmt.Errorf("decode automation decision attempt outcomes: %w", err)
		}
	}
	return decision, nil
}

func (s *AutomationOutcomeStore) GetDecisionStats(ctx context.Context, orgID, automationID uuid.UUID) (models.AutomationDecisionStats, error) {
	query := automationDecisionTargetCTEs + `
	SELECT
		count(DISTINCT (l.repository, l.pull_request_number)),
		count(*),
		COALESCE(sum(l.attempt_count), 0)::bigint,
		count(*) FILTER (WHERE o.id IS NULL AND l.execution_status IN ('pending', 'running')),
		count(*) FILTER (WHERE o.decision = 'passed'),
		count(*) FILTER (WHERE o.decision = 'changes_requested'),
		count(*) FILTER (WHERE o.decision = 'advisory'),
		count(*) FILTER (WHERE o.decision = 'not_applicable'),
		count(*) FILTER (WHERE o.id IS NULL AND l.execution_status NOT IN ('pending', 'running', 'failed')),
		count(*) FILTER (WHERE o.id IS NULL AND l.execution_status = 'failed')
	FROM latest l
	LEFT JOIN automation_run_outcomes o
	  ON o.org_id = l.org_id AND o.automation_run_id = l.run_id`
	var stats models.AutomationDecisionStats
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "automation_id": automationID}).Scan(
		&stats.UniquePullRequests, &stats.UniqueRevisions, &stats.TotalRuns,
		&stats.Evaluating, &stats.Passed, &stats.ChangesRequested, &stats.Advisory,
		&stats.NotApplicable, &stats.OutcomeNotReported, &stats.ExecutionFailed,
	)
	if err != nil {
		return models.AutomationDecisionStats{}, fmt.Errorf("get automation decision stats: %w", err)
	}
	return stats, nil
}

func ParseAutomationDecisionFilter(raw string) (*models.AutomationOutcomeDecision, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, false, nil
	}
	if value == "outcome_not_reported" {
		return nil, true, nil
	}
	decision := models.AutomationOutcomeDecision(value)
	if err := decision.Validate(); err != nil {
		return nil, false, err
	}
	return &decision, false, nil
}
