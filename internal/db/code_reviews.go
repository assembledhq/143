package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type CodeReviewStore struct {
	db      DBTX
	streams *cache.CodeReviewStreams
	logger  zerolog.Logger
}

func NewCodeReviewStore(db DBTX) *CodeReviewStore {
	return &CodeReviewStore{db: db, logger: zerolog.Nop()}
}

// SetStreams injects the Redis helper used to fan code review lifecycle changes
// out to the org-scoped SSE stream. Publishing is best-effort: a nil helper (no
// Redis) simply means no live events and the frontend falls back to polling.
// lint:allow-no-orgid reason="process-wide dependency injection for Redis code review streaming"
func (s *CodeReviewStore) SetStreams(streams *cache.CodeReviewStreams) {
	s.streams = streams
}

// SetLogger injects the structured logger used for best-effort stream publishing.
// lint:allow-no-orgid reason="process-wide dependency injection for store logging"
func (s *CodeReviewStore) SetLogger(logger zerolog.Logger) {
	s.logger = logger
}

// publishUpdated emits a best-effort code review lifecycle event. Failures are
// logged and swallowed so a Redis hiccup never fails the underlying DB write.
func (s *CodeReviewStore) publishUpdated(ctx context.Context, metadata models.CodeReviewSessionMetadata) {
	if s.streams == nil {
		return
	}
	// Batch transitions publish a synthetic metadata with a zero session ID;
	// surface that as a nil pointer so the event omits session_id entirely.
	var sessionID *uuid.UUID
	if metadata.SessionID != uuid.Nil {
		id := metadata.SessionID
		sessionID = &id
	}
	if err := s.streams.PublishUpdated(ctx, metadata.OrgID, models.CodeReviewUpdatedEvent{
		OrgID:     metadata.OrgID,
		SessionID: sessionID,
		Status:    metadata.Status,
		Decision:  metadata.Decision,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		s.logger.Warn().Err(err).
			Str("org_id", metadata.OrgID.String()).
			Str("session_id", metadata.SessionID.String()).
			Msg("failed to publish code review update to Redis")
	}
}

const codeReviewPolicyColumns = `id, org_id, repository_id, active, version, enabled, approval_mode,
		review_instructions, automated_approval_policy, description_policy, risk_policy, agent_roster, inline_comment_limit, inheritance, created_by_user_id, created_at`

const codeReviewMetadataColumns = `id, org_id, session_id, repository_id, pull_request_id, policy_id,
	base_sha, head_sha, from_fork, trigger_source, status, decision, acceptable, stale, superseded_by_session_id,
	review_output_key, prompt_artifact_key, github_review_id, github_review_url, final_review_body,
	failure_reason, completed_at, created_at`

const codeReviewAgentResultColumns = `id, org_id, session_id, agent_provider, agent_model, role, status,
	raw_output, structured_result, created_at`

const codeReviewFindingColumns = `id, org_id, session_id, agent_result_id, dedupe_key, severity,
	confidence, path, start_line, end_line, summary, body, selected_for_inline, github_comment_id, created_at`

const codeReviewPromptArtifactColumns = `id, org_id, session_id, artifact_key, role, agent_provider,
	content, metadata, created_at`

const codeReviewGitHubTriggerSettingColumns = `id, org_id, repository_id, installation_id, active, version,
	team_slug, team_name, team_id, repo_permission, created_by_user_id, created_at`

type SaveCodeReviewGitHubTriggerParams struct {
	RepositoryID    uuid.UUID
	InstallationID  int64
	TeamSlug        string
	TeamName        string
	TeamID          int64
	RepoPermission  models.CodeReviewGitHubTriggerRepoPermission
	CreatedByUserID *uuid.UUID
}

func (s *CodeReviewStore) GetActiveGitHubTrigger(ctx context.Context, orgID, repositoryID uuid.UUID) (models.CodeReviewGitHubTriggerSetting, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewGitHubTriggerSettingColumns+`
		FROM code_review_github_trigger_settings
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND active = true`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
	})
	if err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("query code review GitHub trigger: %w", err)
	}
	return collectOneCodeReviewGitHubTriggerSetting(rows)
}

func (s *CodeReviewStore) SaveGitHubTrigger(ctx context.Context, orgID uuid.UUID, params SaveCodeReviewGitHubTriggerParams) (models.CodeReviewGitHubTriggerSetting, error) {
	if params.RepositoryID == uuid.Nil {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("repository_id is required")
	}
	if strings.TrimSpace(params.TeamSlug) == "" || strings.TrimSpace(params.TeamName) == "" {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("team slug and name are required")
	}
	if err := params.RepoPermission.Validate(); err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, err
	}
	if params.RepoPermission != models.DefaultCodeReviewGitHubTriggerRepoPerm {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("repo permission must be %q", models.DefaultCodeReviewGitHubTriggerRepoPerm)
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("save code review GitHub trigger requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("begin code review GitHub trigger tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM code_review_github_trigger_settings
		WHERE org_id = @org_id
		  AND repository_id = @repository_id`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": params.RepositoryID,
	}).Scan(&version); err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("select next code review GitHub trigger version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE code_review_github_trigger_settings
		SET active = false
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND active = true`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": params.RepositoryID,
	}); err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("inactivate code review GitHub trigger: %w", err)
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO code_review_github_trigger_settings (
			org_id, repository_id, installation_id, active, version, team_slug, team_name,
			team_id, repo_permission, created_by_user_id
		) VALUES (
			@org_id, @repository_id, @installation_id, true, @version, @team_slug, @team_name,
			@team_id, @repo_permission, @created_by_user_id
		)
		RETURNING `+codeReviewGitHubTriggerSettingColumns, pgx.NamedArgs{
		"org_id":             orgID,
		"repository_id":      params.RepositoryID,
		"installation_id":    params.InstallationID,
		"version":            version,
		"team_slug":          strings.TrimSpace(params.TeamSlug),
		"team_name":          strings.TrimSpace(params.TeamName),
		"team_id":            params.TeamID,
		"repo_permission":    params.RepoPermission,
		"created_by_user_id": params.CreatedByUserID,
	})
	if err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("insert code review GitHub trigger: %w", err)
	}
	record, err := collectOneCodeReviewGitHubTriggerSetting(rows)
	if err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, fmt.Errorf("commit code review GitHub trigger tx: %w", err)
	}
	return record, nil
}

func (s *CodeReviewStore) DeactivateGitHubTrigger(ctx context.Context, orgID, repositoryID uuid.UUID, createdByUserID *uuid.UUID) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("deactivate code review GitHub trigger requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin code review GitHub trigger deactivate tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var version int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM code_review_github_trigger_settings
		WHERE org_id = @org_id
		  AND repository_id = @repository_id`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
	}).Scan(&version); err != nil {
		return fmt.Errorf("select next code review GitHub trigger tombstone version: %w", err)
	}

	rows, err := tx.Query(ctx, `
		UPDATE code_review_github_trigger_settings
		SET active = false
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND active = true
		RETURNING `+codeReviewGitHubTriggerSettingColumns, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
	})
	if err != nil {
		return fmt.Errorf("deactivate code review GitHub trigger: %w", err)
	}
	previous, err := collectOneCodeReviewGitHubTriggerSetting(rows)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.Commit(ctx)
		}
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO code_review_github_trigger_settings (
			org_id, repository_id, installation_id, active, version, team_slug, team_name,
			team_id, repo_permission, created_by_user_id
		) VALUES (
			@org_id, @repository_id, @installation_id, false, @version, @team_slug, @team_name,
			@team_id, @repo_permission, @created_by_user_id
		)`, pgx.NamedArgs{
		"org_id":             orgID,
		"repository_id":      repositoryID,
		"installation_id":    previous.InstallationID,
		"version":            version,
		"team_slug":          previous.TeamSlug,
		"team_name":          previous.TeamName,
		"team_id":            previous.TeamID,
		"repo_permission":    previous.RepoPermission,
		"created_by_user_id": createdByUserID,
	}); err != nil {
		return fmt.Errorf("insert code review GitHub trigger tombstone: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *CodeReviewStore) ResolvePolicy(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID) (models.CodeReviewResolvedPolicy, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewPolicyColumns+`
		FROM code_review_policies
		WHERE org_id = @org_id
		  AND active = true
		  AND (repository_id IS NULL OR repository_id = @repository_id)
		ORDER BY CASE WHEN repository_id = @repository_id THEN 0 ELSE 1 END, created_at DESC, id DESC
		LIMIT 2`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID})
	if err != nil {
		return models.CodeReviewResolvedPolicy{}, fmt.Errorf("query code review policy: %w", err)
	}
	records, err := collectCodeReviewPolicies(rows)
	if err != nil {
		return models.CodeReviewResolvedPolicy{}, err
	}
	if len(records) == 0 {
		return models.CodeReviewResolvedPolicy{
			Config: models.DefaultCodeReviewPolicyConfig(),
			Source: "default",
		}, nil
	}
	var repoPolicy, orgPolicy *models.CodeReviewPolicyRecord
	for idx := range records {
		record := records[idx]
		if record.RepositoryID != nil && repositoryID != nil && *record.RepositoryID == *repositoryID {
			repoPolicy = &records[idx]
			continue
		}
		if record.RepositoryID == nil {
			orgPolicy = &records[idx]
		}
	}
	if repoPolicy != nil {
		config := repoPolicy.Config()
		var inherited *models.CodeReviewPolicyRecord
		if config.Inheritance.InheritOrgDefaults {
			base := models.DefaultCodeReviewPolicyConfig()
			if orgPolicy != nil {
				base = orgPolicy.Config()
				inherited = orgPolicy
			}
			config = models.MergeCodeReviewPolicyConfig(base, config)
		}
		return models.CodeReviewResolvedPolicy{
			Config:          config,
			Source:          "repository",
			Policy:          repoPolicy,
			InheritedPolicy: inherited,
		}, nil
	}
	if orgPolicy != nil {
		return models.CodeReviewResolvedPolicy{Config: orgPolicy.Config(), Source: "organization", Policy: orgPolicy}, nil
	}
	return models.CodeReviewResolvedPolicy{Config: models.DefaultCodeReviewPolicyConfig(), Source: "default"}, nil
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
	config.ReviewInstructions = strings.TrimSpace(config.ReviewInstructions)
	config.AutomatedApprovalPolicy = strings.TrimSpace(config.AutomatedApprovalPolicy)
	if err := config.ValidatePromptFields(); err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	config = models.ResolveCodeReviewPolicyConfig(&config)
	if repositoryID != nil {
		base, err := s.activeOrgPolicyConfig(ctx, orgID)
		if err != nil {
			return models.CodeReviewPolicyRecord{}, err
		}
		config.Inheritance = models.CodeReviewPolicyInheritance{
			InheritOrgDefaults: true,
			OverrideFields:     models.CodeReviewPolicyOverrideFields(base, config),
		}
	} else {
		config.Inheritance = models.CodeReviewPolicyInheritance{InheritOrgDefaults: false}
	}
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
	descriptionPolicy, riskPolicy, agentRoster, inheritance, err := marshalCodeReviewPolicyParts(config)
	if err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	rows, err := tx.Query(ctx, `
			INSERT INTO code_review_policies (
				org_id, repository_id, active, version, enabled, approval_mode, review_instructions, automated_approval_policy, description_policy,
				risk_policy, agent_roster, inline_comment_limit, inheritance, created_by_user_id
			) VALUES (
				@org_id, @repository_id, true, @version, @enabled, @approval_mode, @review_instructions, @automated_approval_policy, @description_policy,
				@risk_policy, @agent_roster, @inline_comment_limit, @inheritance, @created_by_user_id
			)
			RETURNING `+codeReviewPolicyColumns, pgx.NamedArgs{
		"org_id":                    orgID,
		"repository_id":             repositoryID,
		"version":                   version,
		"enabled":                   config.Enabled,
		"approval_mode":             config.ApprovalMode,
		"review_instructions":       config.ReviewInstructions,
		"automated_approval_policy": config.AutomatedApprovalPolicy,
		"description_policy":        descriptionPolicy,
		"risk_policy":               riskPolicy,
		"agent_roster":              agentRoster,
		"inline_comment_limit":      config.InlineCommentLimit,
		"inheritance":               inheritance,
		"created_by_user_id":        createdByUserID,
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
	logEvent := s.logger.Info().
		Str("org_id", orgID.String()).
		Str("policy_id", record.ID.String()).
		Int("policy_version", record.Version)
	if repositoryID != nil {
		logEvent = logEvent.Str("repository_id", repositoryID.String())
	}
	logEvent.
		Int("review_instructions_runes", utf8.RuneCountInString(record.ReviewInstructions)).
		Int("automated_approval_policy_runes", utf8.RuneCountInString(record.AutomatedApprovalPolicy)).
		Msg("saved code review policy version")
	return record, nil
}

func (s *CodeReviewStore) activeOrgPolicyConfig(ctx context.Context, orgID uuid.UUID) (models.CodeReviewPolicyConfig, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewPolicyColumns+`
		FROM code_review_policies
		WHERE org_id = @org_id
		  AND active = true
		  AND repository_id IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return models.CodeReviewPolicyConfig{}, fmt.Errorf("query active org code review policy: %w", err)
	}
	record, err := collectOneCodeReviewPolicy(rows)
	if err != nil {
		if err == pgx.ErrNoRows {
			return models.DefaultCodeReviewPolicyConfig(), nil
		}
		return models.CodeReviewPolicyConfig{}, err
	}
	return record.Config(), nil
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
	s.publishUpdated(ctx, created)
	return nil
}

func (s *CodeReviewStore) GetByOutputKey(ctx context.Context, orgID uuid.UUID, outputKey string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewMetadataColumns+`
		FROM code_review_session_metadata
		WHERE org_id = @org_id
		  AND review_output_key = @review_output_key`, pgx.NamedArgs{
		"org_id":            orgID,
		"review_output_key": outputKey,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("query code review by output key: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewMetadataColumns+`
		FROM code_review_session_metadata
		WHERE org_id = @org_id
		  AND session_id = @session_id`, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("query code review by session id: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
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

func (s *CodeReviewStore) GetLatestByPullRequestHead(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, policyID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewMetadataColumns+`
		FROM code_review_session_metadata
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha = @head_sha
		  AND policy_id = @policy_id
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"head_sha":        headSHA,
		"policy_id":       policyID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("query latest code review: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) MarkRunning(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'running'
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status IN ('queued', 'running')
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("mark code review running: %w", err)
	}
	metadata, err := collectOneCodeReviewMetadata(rows)
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}

func (s *CodeReviewStore) SetPromptArtifactKey(ctx context.Context, orgID, sessionID uuid.UUID, artifactKey string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET prompt_artifact_key = @prompt_artifact_key
		WHERE org_id = @org_id
		  AND session_id = @session_id
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":              orgID,
		"session_id":          sessionID,
		"prompt_artifact_key": artifactKey,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("set code review prompt artifact key: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) MarkStaleForPullRequestExceptHead(ctx context.Context, orgID, pullRequestID uuid.UUID, currentHeadSHA string, supersededBySessionID *uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'stale',
		    stale = true,
		    superseded_by_session_id = COALESCE(@superseded_by_session_id, superseded_by_session_id),
		    completed_at = COALESCE(completed_at, now())
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha <> @current_head_sha
		  AND status IN ('queued', 'running')`, pgx.NamedArgs{
		"org_id":                   orgID,
		"pull_request_id":          pullRequestID,
		"current_head_sha":         currentHeadSHA,
		"superseded_by_session_id": supersededBySessionID,
	})
	if err != nil {
		return 0, fmt.Errorf("mark stale code reviews: %w", err)
	}
	affected := tag.RowsAffected()
	if affected > 0 {
		// Batch update touches multiple rows; publish one org-scoped signal so
		// the list refreshes. SessionID is left zero — the frontend refetches
		// the whole list rather than reading individual fields off the event.
		s.publishUpdated(ctx, models.CodeReviewSessionMetadata{OrgID: orgID, Status: models.CodeReviewSessionStatusStale})
	}
	return affected, nil
}

func (s *CodeReviewStore) MarkStale(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'stale',
		    stale = true,
		    decision = 'blocked',
		    acceptable = false,
		    failure_reason = @failure_reason,
		    completed_at = COALESCE(completed_at, now())
		WHERE org_id = @org_id
		  AND session_id = @session_id
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"failure_reason": reason,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("mark code review stale: %w", err)
	}
	metadata, err := collectOneCodeReviewMetadata(rows)
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
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
	metadata, err := collectOneCodeReviewMetadata(rows)
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}

func (s *CodeReviewStore) RecordGitHubReview(ctx context.Context, orgID, sessionID uuid.UUID, githubReviewID int64, githubReviewURL string, finalReviewBody string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET github_review_id = @github_review_id,
		    github_review_url = @github_review_url,
		    final_review_body = @final_review_body
		WHERE org_id = @org_id
		  AND session_id = @session_id
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":            orgID,
		"session_id":        sessionID,
		"github_review_id":  githubReviewID,
		"github_review_url": githubReviewURL,
		"final_review_body": finalReviewBody,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("record code review GitHub review: %w", err)
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
		  AND status IN ('queued', 'running')
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"failure_reason": reason,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("fail code review: %w", err)
	}
	metadata, err := collectOneCodeReviewMetadata(rows)
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}

func (s *CodeReviewStore) CancelReview(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'cancelled',
		    failure_reason = @failure_reason,
		    completed_at = COALESCE(completed_at, now())
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status IN ('queued', 'running')
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"failure_reason": reason,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("cancel code review: %w", err)
	}
	metadata, err := collectOneCodeReviewMetadata(rows)
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}

type CodeReviewListFilters struct {
	RepositoryID *uuid.UUID
	Decision     *models.CodeReviewDecision
	Outcome      *models.CodeReviewListOutcome
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
			       s.title AS session_title, r.full_name AS repository_name, pr.github_repo, pr.github_pr_number,
			       pr.github_pr_url, pr.title AS pull_request_title,
			       COALESCE(NULLIF(s.revision_context->>'pull_request_author', ''), pr.authored_by::text) AS pull_request_author
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
	if filters.Outcome != nil {
		if err := filters.Outcome.Validate(); err != nil {
			return nil, err
		}
		switch *filters.Outcome {
		case models.CodeReviewListOutcomeAutomaticallyApproved:
			query += `
			  AND m.status = 'completed'
			  AND m.decision = 'approved'
			  AND m.github_review_id IS NOT NULL`
		case models.CodeReviewListOutcomeCompletedNotApproved:
			query += `
			  AND m.status = 'completed'
			  AND (m.decision IS DISTINCT FROM 'approved'
			       OR m.github_review_id IS NULL)`
		}
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

func (s *CodeReviewStore) UpdateAgentResultOutcome(ctx context.Context, orgID, resultID uuid.UUID, status models.CodeReviewAgentResultStatus, rawOutput *string, structuredResult json.RawMessage) (models.CodeReviewAgentResult, error) {
	if err := status.Validate(); err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_agent_results
		SET status = @status,
		    raw_output = @raw_output,
		    structured_result = @structured_result
		WHERE org_id = @org_id
		  AND id = @id
		RETURNING `+codeReviewAgentResultColumns, pgx.NamedArgs{
		"org_id":            orgID,
		"id":                resultID,
		"status":            status,
		"raw_output":        rawOutput,
		"structured_result": structuredResult,
	})
	if err != nil {
		return models.CodeReviewAgentResult{}, fmt.Errorf("update code review agent result: %w", err)
	}
	return collectOneCodeReviewAgentResult(rows)
}

func (s *CodeReviewStore) CreatePromptArtifact(ctx context.Context, artifact *models.CodeReviewPromptArtifact) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO code_review_prompt_artifacts (
			org_id, session_id, artifact_key, role, agent_provider, content, metadata
		) VALUES (
			@org_id, @session_id, @artifact_key, @role, @agent_provider, @content, COALESCE(@metadata, '{}'::jsonb)
		)
		ON CONFLICT (org_id, artifact_key) DO UPDATE
		SET content = EXCLUDED.content,
		    metadata = EXCLUDED.metadata
		RETURNING `+codeReviewPromptArtifactColumns, pgx.NamedArgs{
		"org_id":         artifact.OrgID,
		"session_id":     artifact.SessionID,
		"artifact_key":   artifact.ArtifactKey,
		"role":           artifact.Role,
		"agent_provider": artifact.AgentProvider,
		"content":        artifact.Content,
		"metadata":       artifact.Metadata,
	})
	if err != nil {
		return fmt.Errorf("create code review prompt artifact: %w", err)
	}
	created, err := collectOneCodeReviewPromptArtifact(rows)
	if err != nil {
		return err
	}
	*artifact = created
	return nil
}

func (s *CodeReviewStore) ListPromptArtifacts(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.CodeReviewPromptArtifact, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewPromptArtifactColumns+`
		FROM code_review_prompt_artifacts
		WHERE org_id = @org_id
		  AND session_id = @session_id
		ORDER BY created_at ASC, id ASC`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("list code review prompt artifacts: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewPromptArtifact])
}

func (s *CodeReviewStore) CreateFinding(ctx context.Context, finding *models.CodeReviewFinding) error {
	return s.upsertFinding(ctx, finding, false)
}

func (s *CodeReviewStore) ReplaceFinding(ctx context.Context, finding *models.CodeReviewFinding) error {
	return s.upsertFinding(ctx, finding, true)
}

func (s *CodeReviewStore) upsertFinding(ctx context.Context, finding *models.CodeReviewFinding, replaceOnConflict bool) error {
	if err := finding.Severity.Validate(); err != nil {
		return err
	}
	if err := finding.Confidence.Validate(); err != nil {
		return err
	}
	conflictSet := "selected_for_inline = EXCLUDED.selected_for_inline"
	if replaceOnConflict {
		conflictSet = `
			agent_result_id = EXCLUDED.agent_result_id,
			severity = EXCLUDED.severity,
			confidence = EXCLUDED.confidence,
			path = EXCLUDED.path,
			start_line = EXCLUDED.start_line,
			end_line = EXCLUDED.end_line,
			summary = EXCLUDED.summary,
			body = EXCLUDED.body,
			selected_for_inline = code_review_findings.selected_for_inline OR EXCLUDED.selected_for_inline,
			github_comment_id = COALESCE(code_review_findings.github_comment_id, EXCLUDED.github_comment_id)`
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
		SET `+conflictSet+`
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

func marshalCodeReviewPolicyParts(config models.CodeReviewPolicyConfig) ([]byte, []byte, []byte, []byte, error) {
	descriptionPolicy, err := json.Marshal(config.DescriptionPolicy)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal code review description policy: %w", err)
	}
	riskPolicy, err := json.Marshal(config.RiskPolicy)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal code review risk policy: %w", err)
	}
	agentRoster, err := json.Marshal(config.AgentRoster)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal code review agent roster: %w", err)
	}
	inheritance, err := json.Marshal(config.Inheritance)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal code review inheritance: %w", err)
	}
	return descriptionPolicy, riskPolicy, agentRoster, inheritance, nil
}

func collectOneCodeReviewPolicy(rows pgx.Rows) (models.CodeReviewPolicyRecord, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return models.CodeReviewPolicyRecord{}, err
		}
		return models.CodeReviewPolicyRecord{}, pgx.ErrNoRows
	}
	record, err := scanCodeReviewPolicy(rows)
	if err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	return record, rows.Err()
}

func collectOneCodeReviewGitHubTriggerSetting(rows pgx.Rows) (models.CodeReviewGitHubTriggerSetting, error) {
	defer rows.Close()
	setting, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewGitHubTriggerSetting])
	if err != nil {
		return models.CodeReviewGitHubTriggerSetting{}, err
	}
	return setting, nil
}

func collectCodeReviewPolicies(rows pgx.Rows) ([]models.CodeReviewPolicyRecord, error) {
	defer rows.Close()
	records := make([]models.CodeReviewPolicyRecord, 0)
	for rows.Next() {
		record, err := scanCodeReviewPolicy(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func scanCodeReviewPolicy(rows pgx.Rows) (models.CodeReviewPolicyRecord, error) {
	var record models.CodeReviewPolicyRecord
	var descriptionPolicy, riskPolicy, agentRoster, inheritance []byte
	if err := rows.Scan(&record.ID, &record.OrgID, &record.RepositoryID, &record.Active, &record.Version, &record.Enabled, &record.ApprovalMode,
		&record.ReviewInstructions, &record.AutomatedApprovalPolicy, &descriptionPolicy, &riskPolicy, &agentRoster, &record.InlineCommentLimit, &inheritance, &record.CreatedByUserID, &record.CreatedAt); err != nil {
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
	if len(inheritance) > 0 {
		if err := json.Unmarshal(inheritance, &record.Inheritance); err != nil {
			return models.CodeReviewPolicyRecord{}, fmt.Errorf("decode code review inheritance: %w", err)
		}
	}
	record.DescriptionPolicy = models.ResolveCodeReviewPolicyConfig(&models.CodeReviewPolicyConfig{DescriptionPolicy: record.DescriptionPolicy}).DescriptionPolicy
	return record, nil
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

func collectOneCodeReviewPromptArtifact(rows pgx.Rows) (models.CodeReviewPromptArtifact, error) {
	defer rows.Close()
	artifact, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewPromptArtifact])
	if err != nil {
		return models.CodeReviewPromptArtifact{}, err
	}
	return artifact, nil
}
