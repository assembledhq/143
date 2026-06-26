package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CodeReviewStore struct {
	db DBTX
}

func NewCodeReviewStore(db DBTX) *CodeReviewStore {
	return &CodeReviewStore{db: db}
}

const codeReviewPolicyColumns = `id, org_id, repository_id, active, version, enabled, approval_mode,
		description_policy, risk_policy, agent_roster, inline_comment_limit, final_review_template, created_by_user_id, created_at`

const codeReviewMetadataColumns = `id, org_id, session_id, repository_id, pull_request_id, policy_id,
	base_sha, head_sha, from_fork, trigger_source, status, decision, acceptable, stale, superseded_by_session_id,
	review_output_key, prompt_artifact_key, github_review_id, github_review_url, final_review_body,
	failure_reason, completed_at, created_at`

const codeReviewAgentResultColumns = `id, org_id, session_id, agent_provider, agent_model, role, status,
	raw_output, structured_result, created_at`

const codeReviewFindingColumns = `id, org_id, session_id, agent_result_id, dedupe_key, severity,
	confidence, path, start_line, end_line, summary, body, selected_for_inline, github_comment_id, created_at`

func (s *CodeReviewStore) ResolvePolicy(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID) (models.CodeReviewResolvedPolicy, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewPolicyColumns+`
		FROM code_review_policies
		WHERE org_id = @org_id
		  AND active = true
		  AND (repository_id IS NULL OR repository_id = @repository_id)
		ORDER BY CASE WHEN repository_id = @repository_id THEN 0 ELSE 1 END, created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID})
	if err != nil {
		return models.CodeReviewResolvedPolicy{}, fmt.Errorf("query code review policy: %w", err)
	}
	record, err := collectOneCodeReviewPolicy(rows)
	if err != nil {
		if err == pgx.ErrNoRows {
			return models.CodeReviewResolvedPolicy{
				Config: models.DefaultCodeReviewPolicyConfig(),
				Source: "default",
			}, nil
		}
		return models.CodeReviewResolvedPolicy{}, err
	}
	source := "organization"
	if record.RepositoryID != nil {
		source = "repository"
	}
	return models.CodeReviewResolvedPolicy{Config: record.Config(), Source: source, Policy: &record}, nil
}

func (s *CodeReviewStore) GetPolicyByID(ctx context.Context, orgID, policyID uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewPolicyColumns+`
		FROM code_review_policies
		WHERE org_id = @org_id
		  AND id = @id`, pgx.NamedArgs{
		"org_id": orgID,
		"id":     policyID,
	})
	if err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("query code review policy by id: %w", err)
	}
	return collectOneCodeReviewPolicy(rows)
}

func (s *CodeReviewStore) SavePolicy(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID, config models.CodeReviewPolicyConfig, createdByUserID *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	config = models.ResolveCodeReviewPolicyConfig(&config)
	if err := config.Validate(); err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("save code review policy requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("begin code review policy tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM code_review_policies
		WHERE org_id = @org_id
		  AND repository_id IS NOT DISTINCT FROM @repository_id`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
	}).Scan(&version); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("select next code review policy version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE code_review_policies
		SET active = false
		WHERE org_id = @org_id
		  AND active = true
		  AND repository_id IS NOT DISTINCT FROM @repository_id`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
	}); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("inactivate code review policy: %w", err)
	}
	descriptionPolicy, riskPolicy, agentRoster, err := marshalCodeReviewPolicyParts(config)
	if err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	rows, err := tx.Query(ctx, `
			INSERT INTO code_review_policies (
				org_id, repository_id, active, version, enabled, approval_mode, description_policy,
				risk_policy, agent_roster, inline_comment_limit, final_review_template, created_by_user_id
			) VALUES (
				@org_id, @repository_id, true, @version, @enabled, @approval_mode, @description_policy,
				@risk_policy, @agent_roster, @inline_comment_limit, @final_review_template, @created_by_user_id
			)
			RETURNING `+codeReviewPolicyColumns, pgx.NamedArgs{
		"org_id":                orgID,
		"repository_id":         repositoryID,
		"version":               version,
		"enabled":               config.Enabled,
		"approval_mode":         config.ApprovalMode,
		"description_policy":    descriptionPolicy,
		"risk_policy":           riskPolicy,
		"agent_roster":          agentRoster,
		"inline_comment_limit":  config.InlineCommentLimit,
		"final_review_template": config.FinalReviewTemplate,
		"created_by_user_id":    createdByUserID,
	})
	if err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("insert code review policy: %w", err)
	}
	record, err := collectOneCodeReviewPolicy(rows)
	if err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("commit code review policy tx: %w", err)
	}
	return record, nil
}

func (s *CodeReviewStore) CreateSessionMetadata(ctx context.Context, metadata *models.CodeReviewSessionMetadata) error {
	if err := metadata.TriggerSource.Validate(); err != nil {
		return err
	}
	if err := metadata.Status.Validate(); err != nil {
		return err
	}
	if metadata.Decision != nil {
		if err := metadata.Decision.Validate(); err != nil {
			return err
		}
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO code_review_session_metadata (
			org_id, session_id, repository_id, pull_request_id, policy_id, base_sha, head_sha,
			from_fork, trigger_source, status, decision, acceptable, stale, superseded_by_session_id,
			review_output_key, prompt_artifact_key, github_review_id, github_review_url, final_review_body,
			failure_reason, completed_at
		) VALUES (
			@org_id, @session_id, @repository_id, @pull_request_id, @policy_id, @base_sha, @head_sha,
			@from_fork, @trigger_source, @status, @decision, @acceptable, @stale, @superseded_by_session_id,
			@review_output_key, @prompt_artifact_key, @github_review_id, @github_review_url, @final_review_body,
			@failure_reason, @completed_at
		)
		ON CONFLICT (org_id, review_output_key) DO UPDATE
		SET review_output_key = EXCLUDED.review_output_key
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":                   metadata.OrgID,
		"session_id":               metadata.SessionID,
		"repository_id":            metadata.RepositoryID,
		"pull_request_id":          metadata.PullRequestID,
		"policy_id":                metadata.PolicyID,
		"base_sha":                 metadata.BaseSHA,
		"head_sha":                 metadata.HeadSHA,
		"from_fork":                metadata.FromFork,
		"trigger_source":           metadata.TriggerSource,
		"status":                   metadata.Status,
		"decision":                 metadata.Decision,
		"acceptable":               metadata.Acceptable,
		"stale":                    metadata.Stale,
		"superseded_by_session_id": metadata.SupersededBySessionID,
		"review_output_key":        metadata.ReviewOutputKey,
		"prompt_artifact_key":      metadata.PromptArtifactKey,
		"github_review_id":         metadata.GitHubReviewID,
		"github_review_url":        metadata.GitHubReviewURL,
		"final_review_body":        metadata.FinalReviewBody,
		"failure_reason":           metadata.FailureReason,
		"completed_at":             metadata.CompletedAt,
	})
	if err != nil {
		return fmt.Errorf("create code review metadata: %w", err)
	}
	created, err := collectOneCodeReviewMetadata(rows)
	if err != nil {
		return err
	}
	*metadata = created
	return nil
}

func (s *CodeReviewStore) GetRunningByPullRequestHead(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, policyID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewMetadataColumns+`
		FROM code_review_session_metadata
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha = @head_sha
		  AND policy_id = @policy_id
		  AND status IN ('queued', 'running')
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"head_sha":        headSHA,
		"policy_id":       policyID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("query running code review: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) MarkRunning(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'running'
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status = 'queued'
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("mark code review running: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) MarkStaleForPullRequestExceptHead(ctx context.Context, orgID, pullRequestID uuid.UUID, currentHeadSHA string) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'stale', stale = true, completed_at = COALESCE(completed_at, now())
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha <> @current_head_sha
		  AND status IN ('queued', 'running')`, pgx.NamedArgs{
		"org_id":           orgID,
		"pull_request_id":  pullRequestID,
		"current_head_sha": currentHeadSHA,
	})
	if err != nil {
		return 0, fmt.Errorf("mark stale code reviews: %w", err)
	}
	return tag.RowsAffected(), nil
}

type CompleteCodeReviewParams struct {
	SessionID       uuid.UUID
	Decision        models.CodeReviewDecision
	Acceptable      bool
	GitHubReviewID  *int64
	GitHubReviewURL *string
	FinalReviewBody string
}

func (s *CodeReviewStore) CompleteReview(ctx context.Context, orgID uuid.UUID, params CompleteCodeReviewParams) (models.CodeReviewSessionMetadata, error) {
	if err := params.Decision.Validate(); err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'completed',
		    decision = @decision,
		    acceptable = @acceptable,
		    github_review_id = @github_review_id,
		    github_review_url = @github_review_url,
		    final_review_body = @final_review_body,
		    failure_reason = NULL,
		    completed_at = now()
		WHERE org_id = @org_id
		  AND session_id = @session_id
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":            orgID,
		"session_id":        params.SessionID,
		"decision":          params.Decision,
		"acceptable":        params.Acceptable,
		"github_review_id":  params.GitHubReviewID,
		"github_review_url": params.GitHubReviewURL,
		"final_review_body": params.FinalReviewBody,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("complete code review: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) FailReview(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'failed',
		    decision = 'blocked',
		    acceptable = false,
		    failure_reason = @failure_reason,
		    completed_at = now()
		WHERE org_id = @org_id
		  AND session_id = @session_id
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"failure_reason": reason,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("fail code review: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

type CodeReviewListFilters struct {
	RepositoryID *uuid.UUID
	Decision     *models.CodeReviewDecision
	Status       *models.CodeReviewSessionStatus
	Acceptable   *bool
	Search       string
	Limit        int
}

func (s *CodeReviewStore) ListReviews(ctx context.Context, orgID uuid.UUID, filters CodeReviewListFilters) ([]models.CodeReviewListItem, error) {
	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	args := pgx.NamedArgs{
		"org_id": orgID,
		"limit":  limit,
	}
	query := `
			SELECT m.id, m.org_id, m.session_id, m.repository_id, m.pull_request_id, m.policy_id,
			       m.base_sha, m.head_sha, m.from_fork, m.trigger_source, m.status, m.decision, m.acceptable, m.stale,
			       m.superseded_by_session_id, m.review_output_key, m.prompt_artifact_key, m.github_review_id,
			       m.github_review_url, m.final_review_body, m.failure_reason, m.completed_at, m.created_at,
		       s.title AS session_title, r.name AS repository_name, pr.github_repo, pr.github_pr_number,
		       pr.github_pr_url, pr.title AS pull_request_title, pr.authored_by AS pull_request_author
		FROM code_review_session_metadata m
			JOIN sessions s ON s.id = m.session_id AND s.org_id = m.org_id
			JOIN repositories r ON r.id = m.repository_id AND r.org_id = m.org_id
			JOIN pull_requests pr ON pr.id = m.pull_request_id AND pr.org_id = m.org_id
			WHERE m.org_id = @org_id`
	if filters.RepositoryID != nil {
		query += `
			  AND m.repository_id = @repository_id`
		args["repository_id"] = *filters.RepositoryID
	}
	if filters.Decision != nil {
		if err := filters.Decision.Validate(); err != nil {
			return nil, err
		}
		query += `
			  AND m.decision = @decision`
		args["decision"] = *filters.Decision
	}
	if filters.Status != nil {
		if err := filters.Status.Validate(); err != nil {
			return nil, err
		}
		query += `
			  AND m.status = @status`
		args["status"] = *filters.Status
	}
	if filters.Acceptable != nil {
		query += `
			  AND m.acceptable = @acceptable`
		args["acceptable"] = *filters.Acceptable
	}
	if search := strings.TrimSpace(filters.Search); search != "" {
		query += `
			  AND (pr.title ILIKE @search OR pr.github_repo ILIKE @search OR pr.github_pr_number::text = @search_exact OR COALESCE(s.title, '') ILIKE @search)`
		args["search"] = "%" + search + "%"
		args["search_exact"] = strings.TrimPrefix(search, "#")
	}
	query += `
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT @limit`
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query code review list: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewListItem])
}

func (s *CodeReviewStore) CreateAgentResult(ctx context.Context, result *models.CodeReviewAgentResult) error {
	if err := result.Role.Validate(); err != nil {
		return err
	}
	if err := result.Status.Validate(); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO code_review_agent_results (
			org_id, session_id, agent_provider, agent_model, role, status, raw_output, structured_result
		) VALUES (
			@org_id, @session_id, @agent_provider, @agent_model, @role, @status, @raw_output, @structured_result
		)
		RETURNING `+codeReviewAgentResultColumns, pgx.NamedArgs{
		"org_id":            result.OrgID,
		"session_id":        result.SessionID,
		"agent_provider":    result.AgentProvider,
		"agent_model":       result.AgentModel,
		"role":              result.Role,
		"status":            result.Status,
		"raw_output":        result.RawOutput,
		"structured_result": result.StructuredResult,
	})
	if err != nil {
		return fmt.Errorf("create code review agent result: %w", err)
	}
	created, err := collectOneCodeReviewAgentResult(rows)
	if err != nil {
		return err
	}
	*result = created
	return nil
}

func (s *CodeReviewStore) ListAgentResults(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.CodeReviewAgentResult, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewAgentResultColumns+`
		FROM code_review_agent_results
		WHERE org_id = @org_id
		  AND session_id = @session_id
		ORDER BY created_at ASC, id ASC`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("list code review agent results: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewAgentResult])
}

func (s *CodeReviewStore) CreateFinding(ctx context.Context, finding *models.CodeReviewFinding) error {
	if err := finding.Severity.Validate(); err != nil {
		return err
	}
	if err := finding.Confidence.Validate(); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO code_review_findings (
			org_id, session_id, agent_result_id, dedupe_key, severity, confidence,
			path, start_line, end_line, summary, body, selected_for_inline, github_comment_id
		) VALUES (
			@org_id, @session_id, @agent_result_id, @dedupe_key, @severity, @confidence,
			@path, @start_line, @end_line, @summary, @body, @selected_for_inline, @github_comment_id
		)
		ON CONFLICT (org_id, session_id, dedupe_key) DO UPDATE
		SET selected_for_inline = EXCLUDED.selected_for_inline
		RETURNING `+codeReviewFindingColumns, pgx.NamedArgs{
		"org_id":              finding.OrgID,
		"session_id":          finding.SessionID,
		"agent_result_id":     finding.AgentResultID,
		"dedupe_key":          finding.DedupeKey,
		"severity":            finding.Severity,
		"confidence":          finding.Confidence,
		"path":                finding.Path,
		"start_line":          finding.StartLine,
		"end_line":            finding.EndLine,
		"summary":             finding.Summary,
		"body":                finding.Body,
		"selected_for_inline": finding.SelectedForInline,
		"github_comment_id":   finding.GitHubCommentID,
	})
	if err != nil {
		return fmt.Errorf("create code review finding: %w", err)
	}
	created, err := collectOneCodeReviewFinding(rows)
	if err != nil {
		return err
	}
	*finding = created
	return nil
}

func (s *CodeReviewStore) ListFindings(ctx context.Context, orgID, sessionID uuid.UUID, selectedOnly bool) ([]models.CodeReviewFinding, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewFindingColumns+`
		FROM code_review_findings
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND (@selected_only = false OR selected_for_inline = true)
		ORDER BY selected_for_inline DESC, severity DESC, created_at ASC, id ASC`, pgx.NamedArgs{
		"org_id":        orgID,
		"session_id":    sessionID,
		"selected_only": selectedOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list code review findings: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewFinding])
}

func (s *CodeReviewStore) MarkFindingPosted(ctx context.Context, orgID, findingID uuid.UUID, githubCommentID int64) (models.CodeReviewFinding, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_findings
		SET github_comment_id = @github_comment_id,
		    selected_for_inline = true
		WHERE org_id = @org_id
		  AND id = @id
		RETURNING `+codeReviewFindingColumns, pgx.NamedArgs{
		"org_id":            orgID,
		"id":                findingID,
		"github_comment_id": githubCommentID,
	})
	if err != nil {
		return models.CodeReviewFinding{}, fmt.Errorf("mark code review finding posted: %w", err)
	}
	return collectOneCodeReviewFinding(rows)
}

func (s *CodeReviewStore) MarkFindingsSelectedForInline(ctx context.Context, orgID, sessionID uuid.UUID, findingIDs []uuid.UUID) (int64, error) {
	if len(findingIDs) == 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE code_review_findings
		SET selected_for_inline = true
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND id = ANY(@finding_ids)`, pgx.NamedArgs{
		"org_id":      orgID,
		"session_id":  sessionID,
		"finding_ids": findingIDs,
	})
	if err != nil {
		return 0, fmt.Errorf("mark code review findings selected for inline: %w", err)
	}
	return tag.RowsAffected(), nil
}

func marshalCodeReviewPolicyParts(config models.CodeReviewPolicyConfig) ([]byte, []byte, []byte, error) {
	descriptionPolicy, err := json.Marshal(config.DescriptionPolicy)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal code review description policy: %w", err)
	}
	riskPolicy, err := json.Marshal(config.RiskPolicy)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal code review risk policy: %w", err)
	}
	agentRoster, err := json.Marshal(config.AgentRoster)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal code review agent roster: %w", err)
	}
	return descriptionPolicy, riskPolicy, agentRoster, nil
}

func collectOneCodeReviewPolicy(rows pgx.Rows) (models.CodeReviewPolicyRecord, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return models.CodeReviewPolicyRecord{}, err
		}
		return models.CodeReviewPolicyRecord{}, pgx.ErrNoRows
	}
	var record models.CodeReviewPolicyRecord
	var descriptionPolicy, riskPolicy, agentRoster []byte
	if err := rows.Scan(&record.ID, &record.OrgID, &record.RepositoryID, &record.Active, &record.Version, &record.Enabled, &record.ApprovalMode,
		&descriptionPolicy, &riskPolicy, &agentRoster, &record.InlineCommentLimit, &record.FinalReviewTemplate, &record.CreatedByUserID, &record.CreatedAt); err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	if err := json.Unmarshal(descriptionPolicy, &record.DescriptionPolicy); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("decode code review description policy: %w", err)
	}
	if err := json.Unmarshal(riskPolicy, &record.RiskPolicy); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("decode code review risk policy: %w", err)
	}
	if err := json.Unmarshal(agentRoster, &record.AgentRoster); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("decode code review agent roster: %w", err)
	}
	return record, rows.Err()
}

func collectOneCodeReviewMetadata(rows pgx.Rows) (models.CodeReviewSessionMetadata, error) {
	defer rows.Close()
	metadata, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewSessionMetadata])
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	return metadata, nil
}

func collectOneCodeReviewAgentResult(rows pgx.Rows) (models.CodeReviewAgentResult, error) {
	defer rows.Close()
	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewAgentResult])
	if err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	return result, nil
}

func collectOneCodeReviewFinding(rows pgx.Rows) (models.CodeReviewFinding, error) {
	defer rows.Close()
	finding, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewFinding])
	if err != nil {
		return models.CodeReviewFinding{}, err
	}
	return finding, nil
}
