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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
)

var ErrCodeReviewActiveHeadConflict = errors.New("another code review is active for this pull request head and policy")

const codeReviewActiveHeadConstraint = "idx_code_review_metadata_active_head"

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
		review_instructions, automated_approval_policy, description_policy, risk_policy, agent_roster, inline_comment_limit, created_by_user_id, created_at`

const codeReviewMetadataColumns = `id, org_id, session_id, repository_id, pull_request_id, policy_id,
	base_sha, head_sha, from_fork, trigger_source, status, phase, status_code, status_message, retry_at,
	last_error_at, retryable_failure, decision, acceptable, stale, superseded_by_session_id,
	review_output_key, prompt_record_key, github_review_id, github_review_url, final_review_body,
	failure_reason, completed_at, created_at`

const codeReviewAgentResultColumns = `id, org_id, session_id, agent_provider, agent_model, role, status,
	raw_output, structured_result, created_at`

const codeReviewFindingColumns = `id, org_id, session_id, agent_result_id, dedupe_key, severity,
	confidence, path, start_line, end_line, summary, body, selected_for_inline, github_comment_id, created_at`

const codeReviewPromptRecordColumns = `id, org_id, session_id, record_key, role, agent_provider,
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

func (s *CodeReviewStore) ListActiveGitHubTriggers(ctx context.Context, orgID uuid.UUID) ([]models.CodeReviewGitHubTriggerSetting, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewGitHubTriggerSettingColumns+`
		FROM code_review_github_trigger_settings
		WHERE org_id = @org_id
		  AND active = true
		ORDER BY repository_id ASC`, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query active code review GitHub triggers: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewGitHubTriggerSetting])
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

func (s *CodeReviewStore) ResolvePolicy(ctx context.Context, orgID uuid.UUID) (models.CodeReviewResolvedPolicy, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewPolicyColumns+`
		FROM code_review_policies
		WHERE org_id = @org_id
		  AND active = true
		  AND repository_id IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return models.CodeReviewResolvedPolicy{}, fmt.Errorf("query code review policy: %w", err)
	}
	record, err := collectOneCodeReviewPolicy(rows)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.CodeReviewResolvedPolicy{
				Config: models.DefaultCodeReviewPolicyConfig(),
				Source: "default",
			}, nil
		}
		return models.CodeReviewResolvedPolicy{}, err
	}
	return models.CodeReviewResolvedPolicy{Config: record.Config(), Source: "organization", Policy: &record}, nil
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

// ErrCodeReviewPolicyVersionConflict is returned by SavePolicyExpectingVersion
// when the active policy version no longer matches the caller's expectation —
// someone else (human or agent) saved a newer version first.
var ErrCodeReviewPolicyVersionConflict = errors.New("code review policy version conflict")

func (s *CodeReviewStore) SavePolicy(ctx context.Context, orgID uuid.UUID, config models.CodeReviewPolicyConfig, createdByUserID *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	return s.savePolicy(ctx, orgID, config, createdByUserID, nil)
}

// SavePolicyExpectingVersion saves a new policy version only if the current
// active version still equals expectedVersion (0 when the org has never saved
// a policy). The check runs inside the same advisory-locked transaction as the
// insert, so concurrent writers cannot both pass it. Agent-driven policy
// updates use this so a stale agent never silently clobbers a newer human (or
// agent) edit.
func (s *CodeReviewStore) SavePolicyExpectingVersion(ctx context.Context, orgID uuid.UUID, config models.CodeReviewPolicyConfig, expectedVersion int, createdByUserID *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	return s.savePolicy(ctx, orgID, config, createdByUserID, &expectedVersion)
}

func (s *CodeReviewStore) savePolicy(ctx context.Context, orgID uuid.UUID, config models.CodeReviewPolicyConfig, createdByUserID *uuid.UUID, expectedVersion *int) (models.CodeReviewPolicyRecord, error) {
	config.ReviewInstructions = strings.TrimSpace(config.ReviewInstructions)
	config.AutomatedApprovalPolicy = strings.TrimSpace(config.AutomatedApprovalPolicy)
	if err := config.ValidatePromptFields(); err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
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

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"code_review_policy:"+orgID.String(),
	); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("acquire code review policy lock: %w", err)
	}

	var currentVersion int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0)
		FROM code_review_policies
		WHERE org_id = @org_id
		  AND repository_id IS NULL`, pgx.NamedArgs{
		"org_id": orgID,
	}).Scan(&currentVersion); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("select current code review policy version: %w", err)
	}
	if expectedVersion != nil && *expectedVersion != currentVersion {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("%w: active version is %d, expected %d", ErrCodeReviewPolicyVersionConflict, currentVersion, *expectedVersion)
	}
	version := currentVersion + 1
	if _, err := tx.Exec(ctx, `
		UPDATE code_review_policies
		SET active = false
		WHERE org_id = @org_id
		  AND active = true
		  AND repository_id IS NULL`, pgx.NamedArgs{
		"org_id": orgID,
	}); err != nil {
		return models.CodeReviewPolicyRecord{}, fmt.Errorf("inactivate code review policy: %w", err)
	}
	descriptionPolicy, riskPolicy, agentRoster, err := marshalCodeReviewPolicyParts(config)
	if err != nil {
		return models.CodeReviewPolicyRecord{}, err
	}
	rows, err := tx.Query(ctx, `
			INSERT INTO code_review_policies (
				org_id, repository_id, active, version, enabled, approval_mode, review_instructions, automated_approval_policy, description_policy,
				risk_policy, agent_roster, inline_comment_limit, created_by_user_id
			) VALUES (
				@org_id, NULL, true, @version, @enabled, @approval_mode, @review_instructions, @automated_approval_policy, @description_policy,
				@risk_policy, @agent_roster, @inline_comment_limit, @created_by_user_id
			)
			RETURNING `+codeReviewPolicyColumns, pgx.NamedArgs{
		"org_id":                    orgID,
		"version":                   version,
		"enabled":                   config.Enabled,
		"approval_mode":             config.ApprovalMode,
		"review_instructions":       config.ReviewInstructions,
		"automated_approval_policy": config.AutomatedApprovalPolicy,
		"description_policy":        descriptionPolicy,
		"risk_policy":               riskPolicy,
		"agent_roster":              agentRoster,
		"inline_comment_limit":      config.InlineCommentLimit,
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
	logEvent.
		Int("review_instructions_runes", utf8.RuneCountInString(record.ReviewInstructions)).
		Int("automated_approval_policy_runes", utf8.RuneCountInString(record.AutomatedApprovalPolicy)).
		Msg("saved code review policy version")
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
	if metadata.Phase != nil {
		if err := metadata.Phase.Validate(); err != nil {
			return err
		}
	}
	if metadata.StatusCode != nil {
		if err := metadata.StatusCode.Validate(); err != nil {
			return err
		}
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO code_review_session_metadata (
			org_id, session_id, repository_id, pull_request_id, policy_id, base_sha, head_sha,
			from_fork, trigger_source, status, phase, status_code, status_message, retry_at,
			last_error_at, retryable_failure, decision, acceptable, stale, superseded_by_session_id,
			review_output_key, prompt_record_key, github_review_id, github_review_url, final_review_body,
			failure_reason, completed_at
		) VALUES (
			@org_id, @session_id, @repository_id, @pull_request_id, @policy_id, @base_sha, @head_sha,
			@from_fork, @trigger_source, @status, @phase, @status_code, @status_message, @retry_at,
			@last_error_at, @retryable_failure, @decision, @acceptable, @stale, @superseded_by_session_id,
			@review_output_key, @prompt_record_key, @github_review_id, @github_review_url, @final_review_body,
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
		"phase":                    metadata.Phase,
		"status_code":              metadata.StatusCode,
		"status_message":           metadata.StatusMessage,
		"retry_at":                 metadata.RetryAt,
		"last_error_at":            metadata.LastErrorAt,
		"retryable_failure":        metadata.RetryableFailure,
		"decision":                 metadata.Decision,
		"acceptable":               metadata.Acceptable,
		"stale":                    metadata.Stale,
		"superseded_by_session_id": metadata.SupersededBySessionID,
		"review_output_key":        metadata.ReviewOutputKey,
		"prompt_record_key":        metadata.PromptRecordKey,
		"github_review_id":         metadata.GitHubReviewID,
		"github_review_url":        metadata.GitHubReviewURL,
		"final_review_body":        metadata.FinalReviewBody,
		"failure_reason":           metadata.FailureReason,
		"completed_at":             metadata.CompletedAt,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if isUniqueViolation(err) && errors.As(err, &pgErr) && pgErr.ConstraintName == codeReviewActiveHeadConstraint {
			return fmt.Errorf("%w: %v", ErrCodeReviewActiveHeadConflict, err)
		}
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

func (s *CodeReviewStore) GetLatestByPullRequest(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewMetadataColumns+`
		FROM code_review_session_metadata
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("query latest code review by pull request: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) GetLatestSubmittedByPullRequest(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewMetadataColumns+`
		FROM code_review_session_metadata
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND github_review_id IS NOT NULL
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("query latest submitted code review by pull request: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) HasApprovedByPullRequest(ctx context.Context, orgID, pullRequestID uuid.UUID) (bool, error) {
	var approved bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM code_review_session_metadata
			WHERE org_id = @org_id
			  AND pull_request_id = @pull_request_id
			  AND status = 'completed'
			  AND decision = 'approved'
			  AND github_review_id IS NOT NULL
		)`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
	}).Scan(&approved)
	if err != nil {
		return false, fmt.Errorf("query prior code review approval: %w", err)
	}
	return approved, nil
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
		SET status = 'running',
		    phase = CASE
		        WHEN status = 'queued' OR phase IS NULL OR phase = 'waiting_for_github' THEN 'syncing_github'
		        ELSE phase
		    END,
		    status_code = CASE WHEN status = 'queued' OR phase = 'waiting_for_github' THEN NULL ELSE status_code END,
		    status_message = CASE WHEN status = 'queued' OR phase = 'waiting_for_github' THEN NULL ELSE status_message END,
		    retry_at = CASE WHEN status = 'queued' OR phase = 'waiting_for_github' THEN NULL ELSE retry_at END,
		    last_error_at = CASE WHEN status = 'queued' OR phase = 'waiting_for_github' THEN NULL ELSE last_error_at END,
		    retryable_failure = CASE WHEN status = 'queued' OR phase = 'waiting_for_github' THEN false ELSE retryable_failure END
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status IN ('queued', 'running')
		  AND (
		      status = 'queued'
		      OR phase IS NULL
		      OR phase = 'waiting_for_github'
		      OR status_code IS NOT NULL
		      OR status_message IS NOT NULL
		      OR retry_at IS NOT NULL
		      OR last_error_at IS NOT NULL
		      OR retryable_failure = true
		  )
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("mark code review running: %w", err)
	}
	metadata, err := collectOneCodeReviewMetadata(rows)
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := s.GetBySessionID(ctx, orgID, sessionID)
		if getErr != nil {
			return models.CodeReviewSessionMetadata{}, getErr
		}
		if current.Status != models.CodeReviewSessionStatusQueued && current.Status != models.CodeReviewSessionStatusRunning {
			return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
		}
		return current, nil
	}
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}

// SetOperationalPhase records a non-terminal lifecycle transition and clears
// any prior automatic-wait or error state. A retrying worker therefore moves
// back to a healthy phase as soon as it resumes useful work.
func (s *CodeReviewStore) SetOperationalPhase(ctx context.Context, orgID, sessionID uuid.UUID, phase models.CodeReviewPhase) (models.CodeReviewSessionMetadata, error) {
	if err := phase.Validate(); err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	if phase == models.CodeReviewPhaseWaitingGitHub {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("waiting_for_github requires retry details")
	}
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET phase = @phase,
		    status_code = NULL,
		    status_message = NULL,
		    retry_at = NULL,
		    last_error_at = NULL,
		    retryable_failure = false
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status IN ('queued', 'running')
		  AND (
		      phase IS DISTINCT FROM @phase
		      OR status_code IS NOT NULL
		      OR status_message IS NOT NULL
		      OR retry_at IS NOT NULL
		      OR last_error_at IS NOT NULL
		      OR retryable_failure = true
		  )
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "phase": phase,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("set code review operational phase: %w", err)
	}
	metadata, err := collectOneCodeReviewMetadata(rows)
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := s.GetBySessionID(ctx, orgID, sessionID)
		if getErr != nil {
			return models.CodeReviewSessionMetadata{}, getErr
		}
		if current.Status != models.CodeReviewSessionStatusQueued && current.Status != models.CodeReviewSessionStatusRunning {
			return models.CodeReviewSessionMetadata{}, pgx.ErrNoRows
		}
		return current, nil
	}
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}

// SetWaitingForGitHub persists an automatic rate-limit wait. The worker owns
// retry scheduling; retryAt mirrors the delay it returned so the UI can show
// the same recovery time without offering a competing manual action.
func (s *CodeReviewStore) SetWaitingForGitHub(ctx context.Context, orgID, sessionID uuid.UUID, retryAt time.Time, message string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET phase = 'waiting_for_github',
		    status_code = 'github_rate_limited',
		    status_message = @status_message,
		    retry_at = @retry_at,
		    last_error_at = now(),
		    retryable_failure = true
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status IN ('queued', 'running')
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "retry_at": retryAt, "status_message": strings.TrimSpace(message),
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("set code review GitHub wait: %w", err)
	}
	metadata, err := collectOneCodeReviewMetadata(rows)
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}

func (s *CodeReviewStore) SetPromptRecordKey(ctx context.Context, orgID, sessionID uuid.UUID, recordKey string) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET prompt_record_key = @prompt_record_key
		WHERE org_id = @org_id
		  AND session_id = @session_id
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":            orgID,
		"session_id":        sessionID,
		"prompt_record_key": recordKey,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("set code review prompt record key: %w", err)
	}
	return collectOneCodeReviewMetadata(rows)
}

func (s *CodeReviewStore) MarkStaleForPullRequestExceptHead(ctx context.Context, orgID, pullRequestID uuid.UUID, currentHeadSHA string, supersededBySessionID *uuid.UUID) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'stale',
		    stale = true,
		    phase = NULL,
		    status_code = NULL,
		    status_message = NULL,
		    retry_at = NULL,
		    last_error_at = NULL,
		    retryable_failure = false,
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
		    phase = NULL,
		    decision = 'blocked',
		    acceptable = false,
		    failure_reason = @failure_reason,
		    status_code = NULL,
		    status_message = NULL,
		    retry_at = NULL,
		    last_error_at = NULL,
		    retryable_failure = false,
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
	SessionID         uuid.UUID
	Decision          models.CodeReviewDecision
	Acceptable        bool
	GitHubReviewID    *int64
	GitHubReviewURL   *string
	FinalReviewBody   string
	Additions         *int
	Deletions         *int
	RiskReasonDetails []models.CodeReviewRiskReason
}

func (s *CodeReviewStore) CompleteReview(ctx context.Context, orgID uuid.UUID, params CompleteCodeReviewParams) (models.CodeReviewSessionMetadata, error) {
	if err := params.Decision.Validate(); err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	if params.Additions != nil && *params.Additions < 0 {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("additions must be non-negative")
	}
	if params.Deletions != nil && *params.Deletions < 0 {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("deletions must be non-negative")
	}
	if (params.Additions == nil) != (params.Deletions == nil) {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("additions and deletions must be provided together")
	}
	reasonDetails := params.RiskReasonDetails
	if reasonDetails == nil {
		reasonDetails = []models.CodeReviewRiskReason{}
	}
	for _, reason := range reasonDetails {
		if err := reason.Code.Validate(); err != nil {
			return models.CodeReviewSessionMetadata{}, err
		}
	}
	encodedReasonDetails, err := json.Marshal(reasonDetails)
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("marshal code review risk reasons: %w", err)
	}
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'completed',
		    phase = NULL,
		    decision = @decision,
		    acceptable = @acceptable,
		    github_review_id = @github_review_id,
		    github_review_url = @github_review_url,
		    final_review_body = @final_review_body,
		    additions = @additions,
		    deletions = @deletions,
		    risk_reason_details = @risk_reason_details,
		    failure_reason = NULL,
		    status_code = NULL,
		    status_message = NULL,
		    retry_at = NULL,
		    last_error_at = NULL,
		    retryable_failure = false,
		    completed_at = now()
		WHERE org_id = @org_id
		  AND session_id = @session_id
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":              orgID,
		"session_id":          params.SessionID,
		"decision":            params.Decision,
		"acceptable":          params.Acceptable,
		"github_review_id":    params.GitHubReviewID,
		"github_review_url":   params.GitHubReviewURL,
		"final_review_body":   params.FinalReviewBody,
		"additions":           params.Additions,
		"deletions":           params.Deletions,
		"risk_reason_details": encodedReasonDetails,
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
	return s.FailReviewWithStatus(ctx, orgID, FailCodeReviewParams{
		SessionID: sessionID,
		Reason:    reason,
		Code:      models.CodeReviewStatusCodeWorkerFailed,
		Message:   "The code review stopped before it could finish.",
	})
}

type FailCodeReviewParams struct {
	SessionID uuid.UUID
	Reason    string
	Code      models.CodeReviewStatusCode
	Message   string
	Retryable bool
}

func (s *CodeReviewStore) FailReviewWithStatus(ctx context.Context, orgID uuid.UUID, params FailCodeReviewParams) (models.CodeReviewSessionMetadata, error) {
	if err := params.Code.Validate(); err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET status = 'failed',
		    phase = NULL,
		    decision = 'blocked',
		    acceptable = false,
		    failure_reason = @failure_reason,
		    status_code = @status_code,
		    status_message = @status_message,
		    retry_at = NULL,
		    last_error_at = now(),
		    retryable_failure = @retryable_failure,
		    completed_at = now()
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status IN ('queued', 'running')
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id":            orgID,
		"session_id":        params.SessionID,
		"failure_reason":    strings.TrimSpace(params.Reason),
		"status_code":       params.Code,
		"status_message":    strings.TrimSpace(params.Message),
		"retryable_failure": params.Retryable,
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

// MarkSupersededBy links a retryable terminal attempt to its replacement.
// The compare-and-set guard makes repeated or concurrent retry requests safe.
func (s *CodeReviewStore) MarkSupersededBy(ctx context.Context, orgID, sessionID, replacementSessionID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE code_review_session_metadata
		SET superseded_by_session_id = @replacement_session_id,
		    status_message = 'A replacement attempt was started for this review.'
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status = 'failed'
		  AND retryable_failure = true
		  AND superseded_by_session_id IS NULL
		RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "replacement_session_id": replacementSessionID,
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, fmt.Errorf("supersede code review attempt: %w", err)
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
		    phase = NULL,
		    failure_reason = @failure_reason,
		    status_code = NULL,
		    status_message = NULL,
		    retry_at = NULL,
		    last_error_at = NULL,
		    retryable_failure = false,
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

const codeReviewListItemSelect = `
			SELECT m.id, m.org_id, m.session_id, m.repository_id, m.pull_request_id, m.policy_id,
			       m.base_sha, m.head_sha, m.from_fork, m.trigger_source, m.status, m.phase, m.status_code,
			       m.status_message, m.retry_at, m.last_error_at, m.retryable_failure, m.decision, m.acceptable, m.stale,
			       m.superseded_by_session_id, m.review_output_key, m.prompt_record_key, m.github_review_id,
			       m.github_review_url, m.final_review_body, m.failure_reason, m.completed_at, m.created_at,
			       (
			           m.status = 'failed'
			           AND m.retryable_failure = true
			           AND m.superseded_by_session_id IS NULL
			           AND m.retry_at IS NULL
			           AND pr.status = 'open'
			           AND COALESCE(current_health.head_sha = m.head_sha, false)
			           AND NOT EXISTS (
			               SELECT 1
			               FROM code_review_session_metadata newer
			               WHERE newer.org_id = m.org_id
			                 AND newer.pull_request_id = m.pull_request_id
			                 AND (newer.created_at, newer.id) > (m.created_at, m.id)
			           )
			           AND NOT EXISTS (
			               SELECT 1
			               FROM code_review_session_metadata approved
			               WHERE approved.org_id = m.org_id
			                 AND approved.pull_request_id = m.pull_request_id
			                 AND approved.status = 'completed'
			                 AND approved.decision = 'approved'
			                 AND approved.github_review_id IS NOT NULL
			           )
			           AND COALESCE((
			               SELECT policy.enabled
			               FROM code_review_policies policy
			               WHERE policy.org_id = m.org_id
			                 AND policy.repository_id IS NULL
			                 AND policy.active = true
			               LIMIT 1
			           ), true)
			       ) AS retry_eligible,
			       s.title AS session_title, r.full_name AS repository_name, pr.github_repo, pr.github_pr_number,
			       pr.github_pr_url, pr.title AS pull_request_title,
			       COALESCE(NULLIF(s.revision_context->>'pull_request_author', ''), pr.authored_by::text) AS pull_request_author
		FROM code_review_session_metadata m
			JOIN sessions s ON s.id = m.session_id AND s.org_id = m.org_id
			JOIN repositories r ON r.id = m.repository_id AND r.org_id = m.org_id
			JOIN pull_requests pr ON pr.id = m.pull_request_id AND pr.org_id = m.org_id
			LEFT JOIN pull_request_health_current current_health
			       ON current_health.pull_request_id = m.pull_request_id AND current_health.org_id = m.org_id`

const codeReviewListCountFrom = `
		FROM code_review_session_metadata m
		JOIN sessions s ON s.id = m.session_id AND s.org_id = m.org_id
		JOIN repositories r ON r.id = m.repository_id AND r.org_id = m.org_id
		JOIN pull_requests pr ON pr.id = m.pull_request_id AND pr.org_id = m.org_id`

// GetListItemBySessionID returns one review with the same joined pull request
// and repository context as ListReviews rows. Used by the internal (sandbox)
// code review history API where agents need PR context alongside the review.
func (s *CodeReviewStore) GetListItemBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewListItem, error) {
	rows, err := s.db.Query(ctx, codeReviewListItemSelect+`
			WHERE m.org_id = @org_id
			  AND m.session_id = @session_id`, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.CodeReviewListItem{}, fmt.Errorf("query code review list item: %w", err)
	}
	defer rows.Close()
	item, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewListItem])
	if err != nil {
		return models.CodeReviewListItem{}, err
	}
	return item, nil
}

type CodeReviewListFilters struct {
	RepositoryID    *uuid.UUID
	Decision        *models.CodeReviewDecision
	Outcome         *models.CodeReviewListOutcome
	ActivityStatus  *models.CodeReviewActivityStatus
	Status          *models.CodeReviewSessionStatus
	Acceptable      *bool
	Reason          *models.CodeReviewRiskReasonCode
	Author          string
	Search          string
	Limit           int
	CreatedAfter    *time.Time
	CreatedBefore   *time.Time
	SortBy          string
	SortOrder       string
	CursorSortValue any
	CursorSortNull  bool
	// Cursor is the metadata row ID of the last item from the previous page.
	// In the default order, rows strictly after it in (created_at DESC, id DESC)
	// are returned, matching the session-history keyset pagination contract; both
	// anchors are immutable, so a page boundary never moves.
	//
	// An explicit SortBy anchors on CursorSortValue instead, and every sort
	// except repository and pull_request reads a column that changes as a review
	// progresses (queued -> running -> completed). A row whose sort value changes
	// mid-pagination can therefore be skipped or repeated across pages. That is
	// accepted here: the list polls live and the sorted views are for scanning,
	// not for exhaustive one-pass export.
	Cursor          *uuid.UUID
	CursorCreatedAt *time.Time
}

type CodeReviewPage struct {
	Items               []models.CodeReviewListItem
	NextCursor          string
	NextCursorCreatedAt time.Time
	TotalCount          int64
}

func codeReviewActivityStatusPredicate(status *models.CodeReviewActivityStatus) (string, error) {
	if status == nil || *status == models.CodeReviewActivityStatusAll {
		return "", nil
	}
	if err := status.Validate(); err != nil {
		return "", err
	}
	current := ` AND m.status <> 'stale' AND m.superseded_by_session_id IS NULL`
	switch *status {
	case models.CodeReviewActivityStatusCurrent:
		return current, nil
	case models.CodeReviewActivityStatusCompleted:
		return current + ` AND m.status = 'completed'`, nil
	case models.CodeReviewActivityStatusInProgress:
		return current + ` AND m.status IN ('queued', 'running')`, nil
	case models.CodeReviewActivityStatusFailed:
		return current + ` AND m.status = 'failed'`, nil
	case models.CodeReviewActivityStatusSuperseded:
		return ` AND (m.status = 'stale' OR m.superseded_by_session_id IS NOT NULL)`, nil
	default:
		return "", fmt.Errorf("unsupported code review activity status: %q", *status)
	}
}

func codeReviewListWhere(orgID uuid.UUID, filters CodeReviewListFilters, includeCursor bool) (string, pgx.NamedArgs, error) {
	args := pgx.NamedArgs{"org_id": orgID}
	query := ` WHERE m.org_id = @org_id`
	if filters.RepositoryID != nil {
		query += ` AND m.repository_id = @repository_id`
		args["repository_id"] = *filters.RepositoryID
	}
	if filters.Decision != nil {
		if err := filters.Decision.Validate(); err != nil {
			return "", nil, err
		}
		query += ` AND m.decision = @decision`
		args["decision"] = *filters.Decision
	}
	if filters.Outcome != nil {
		if err := filters.Outcome.Validate(); err != nil {
			return "", nil, err
		}
		switch *filters.Outcome {
		case models.CodeReviewListOutcomeAutomaticallyApproved:
			query += ` AND m.status = 'completed' AND m.decision = 'approved' AND m.github_review_id IS NOT NULL`
		case models.CodeReviewListOutcomeCompletedNotApproved:
			query += ` AND m.status = 'completed' AND (m.decision IS DISTINCT FROM 'approved' OR m.github_review_id IS NULL)`
		}
	}
	activityPredicate, err := codeReviewActivityStatusPredicate(filters.ActivityStatus)
	if err != nil {
		return "", nil, err
	}
	query += activityPredicate
	if filters.Status != nil {
		if err := filters.Status.Validate(); err != nil {
			return "", nil, err
		}
		query += ` AND m.status = @status`
		args["status"] = *filters.Status
	}
	if filters.Acceptable != nil {
		query += ` AND m.acceptable = @acceptable`
		args["acceptable"] = *filters.Acceptable
	}
	if filters.Reason != nil {
		if err := filters.Reason.Validate(); err != nil {
			return "", nil, err
		}
		query += ` AND EXISTS (
			SELECT 1
			FROM jsonb_array_elements(m.risk_reason_details) AS risk_reason
			WHERE risk_reason->>'code' = @reason
		)`
		args["reason"] = *filters.Reason
	}
	if author := strings.TrimSpace(filters.Author); author != "" {
		query += ` AND LOWER(COALESCE(NULLIF(s.revision_context->>'pull_request_author', ''), 'Unknown')) = LOWER(@author)`
		args["author"] = author
	}
	if filters.CreatedAfter != nil {
		query += ` AND m.created_at >= @created_after`
		args["created_after"] = *filters.CreatedAfter
	}
	if filters.CreatedBefore != nil {
		query += ` AND m.created_at <= @created_before`
		args["created_before"] = *filters.CreatedBefore
	}
	if includeCursor && filters.Cursor != nil {
		args["cursor"] = *filters.Cursor
		if filters.CursorCreatedAt != nil {
			query += ` AND (m.created_at, m.id) < (@cursor_created_at, @cursor)`
			args["cursor_created_at"] = *filters.CursorCreatedAt
		} else {
			// Internal callers predating the opaque HTTP cursor still pass only
			// the row ID. Keep that contract while public pagination uses the
			// immutable timestamp embedded in its cursor.
			query += ` AND (m.created_at, m.id) < (
				SELECT created_at, id FROM code_review_session_metadata
				WHERE id = @cursor AND org_id = @org_id
			)`
		}
	}
	if search := strings.TrimSpace(filters.Search); search != "" {
		query += ` AND (pr.title ILIKE @search OR pr.github_repo ILIKE @search OR pr.github_pr_number::text = @search_exact OR COALESCE(s.title, '') ILIKE @search)`
		args["search"] = "%" + search + "%"
		args["search_exact"] = strings.TrimPrefix(search, "#")
	}
	return query, args, nil
}

func (s *CodeReviewStore) listReviews(ctx context.Context, orgID uuid.UUID, filters CodeReviewListFilters, limit int) ([]models.CodeReviewListItem, error) {
	where, args, err := codeReviewListWhere(orgID, filters, filters.SortBy == "")
	if err != nil {
		return nil, err
	}
	if filters.SortBy != "" && filters.Cursor != nil {
		cursorWhere, cursorArgs, err := codeReviewSortedCursorPredicate(filters)
		if err != nil {
			return nil, err
		}
		where += cursorWhere
		for key, value := range cursorArgs {
			args[key] = value
		}
	}
	args["limit"] = limit
	orderBy, err := codeReviewListOrderBy(filters.SortBy, filters.SortOrder)
	if err != nil {
		return nil, err
	}
	query := codeReviewListItemSelect + where + orderBy + `
		LIMIT @limit`
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query code review list: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewListItem])
}

// codeReviewSupersededSQL mirrors the isSupersededReview() predicate the
// reviews table uses to blank out a row's outcome, risk, and run status.
const codeReviewSupersededSQL = `(m.stale OR m.status = 'stale' OR m.superseded_by_session_id IS NOT NULL)`

// The outcome, risk, and run status cells render labels derived from several
// columns rather than the columns themselves, so sorting on the raw column
// splits one displayed label across the result (a NULL and a false `acceptable`
// both read "Review needed") and scatters superseded rows through every group.
// These rank expressions order by the label the row actually shows;
// CodeReviewSortRankForItem computes the identical rank for the page cursor and
// is kept in lockstep by TestCodeReviewSortRankMatchesSQL.
const (
	codeReviewOutcomeRankSQL = `(CASE
			WHEN ` + codeReviewSupersededSQL + ` THEN 6
			WHEN m.status = 'completed' AND m.decision = 'approved' AND m.github_review_id IS NOT NULL THEN 0
			WHEN m.decision = 'approved' THEN 1
			WHEN m.decision = 'needs_human_review' THEN 2
			WHEN m.decision = 'blocked' THEN 3
			WHEN m.decision = 'comment_only' THEN 4
			ELSE 5
		END)`
	codeReviewRiskRankSQL = `(CASE
			WHEN ` + codeReviewSupersededSQL + ` THEN 2
			WHEN m.acceptable THEN 0
			ELSE 1
		END)`
	// Queued and running rows label themselves with their phase; the phase
	// labels live in the frontend, so those rows sort as one lifecycle group
	// and fall back to the row ID within it.
	codeReviewRunStatusRankSQL = `(CASE
			WHEN ` + codeReviewSupersededSQL + ` THEN 5
			WHEN m.status = 'queued' THEN 0
			WHEN m.status = 'running' THEN 1
			WHEN m.status = 'completed' THEN 2
			WHEN m.status = 'failed' THEN 3
			WHEN m.status = 'cancelled' THEN 4
			ELSE 6
		END)`
)

// codeReviewListSort is one allowlisted ordering: the expression it orders by,
// and whether that expression can be NULL. Nullability decides both the NULLS
// LAST clause and whether a page cursor may anchor on the null partition.
type codeReviewListSort struct {
	expression string
	nullable   bool
}

// pull_requests.github_pr_number and repositories.full_name are NOT NULL behind
// inner joins, and the rank expressions always return an integer, so only
// completed_at can order a null partition.
var codeReviewListSorts = map[string]codeReviewListSort{
	"pull_request": {expression: "pr.github_pr_number"},
	"outcome":      {expression: codeReviewOutcomeRankSQL},
	"risk":         {expression: codeReviewRiskRankSQL},
	"run_status":   {expression: codeReviewRunStatusRankSQL},
	"repository":   {expression: "r.full_name"},
	"completed":    {expression: "m.completed_at", nullable: true},
}

// CodeReviewListSortIsNullable reports whether a page cursor for this sort may
// anchor on a null value. Anchoring on a partition the column cannot produce
// would silently return an empty page and end pagination early.
func CodeReviewListSortIsNullable(sortBy string) (bool, error) {
	sort, err := codeReviewListSortFor(sortBy)
	if err != nil {
		return false, err
	}
	return sort.nullable, nil
}

func codeReviewListSortFor(sortBy string) (codeReviewListSort, error) {
	sort, ok := codeReviewListSorts[sortBy]
	if !ok {
		return codeReviewListSort{}, fmt.Errorf("unsupported code review sort: %q", sortBy)
	}
	return sort, nil
}

// CodeReviewSortRankForItem returns the displayed-label rank for the label-
// derived sorts, matching the CASE expressions above so a page cursor anchors
// on the same value the ORDER BY produced. ok is false for sorts that anchor on
// a plain column value instead.
func CodeReviewSortRankForItem(sortBy string, item models.CodeReviewListItem) (int, bool) {
	superseded := item.Stale || item.Status == models.CodeReviewSessionStatusStale || item.SupersededBySessionID != nil
	decision := ""
	if item.Decision != nil {
		decision = string(*item.Decision)
	}
	switch sortBy {
	case "outcome":
		switch {
		case superseded:
			return 6, true
		case item.Status == models.CodeReviewSessionStatusCompleted && decision == "approved" && item.GitHubReviewID != nil:
			return 0, true
		case decision == "approved":
			return 1, true
		case decision == "needs_human_review":
			return 2, true
		case decision == "blocked":
			return 3, true
		case decision == "comment_only":
			return 4, true
		default:
			return 5, true
		}
	case "risk":
		switch {
		case superseded:
			return 2, true
		case item.Acceptable != nil && *item.Acceptable:
			return 0, true
		default:
			return 1, true
		}
	case "run_status":
		switch {
		case superseded:
			return 5, true
		case item.Status == models.CodeReviewSessionStatusQueued:
			return 0, true
		case item.Status == models.CodeReviewSessionStatusRunning:
			return 1, true
		case item.Status == models.CodeReviewSessionStatusCompleted:
			return 2, true
		case item.Status == models.CodeReviewSessionStatusFailed:
			return 3, true
		case item.Status == models.CodeReviewSessionStatusCancelled:
			return 4, true
		default:
			return 6, true
		}
	default:
		return 0, false
	}
}

// codeReviewSortedRecency is the secondary ordering every explicit sort falls
// back to. The rank sorts have only a handful of distinct values, so without it
// the order within a group would come from the random UUID primary key instead
// of the newest-first order the list shows by default.
const codeReviewSortedRecency = `m.created_at DESC, m.id DESC`

func codeReviewListOrderBy(sortBy, sortOrder string) (string, error) {
	if sortBy == "" {
		return ` ORDER BY ` + codeReviewSortedRecency, nil
	}
	sort, err := codeReviewListSortFor(sortBy)
	if err != nil {
		return "", err
	}
	direction := "ASC"
	if sortOrder == "desc" {
		direction = "DESC"
	} else if sortOrder != "" && sortOrder != "asc" {
		return "", fmt.Errorf("unsupported code review sort order: %q", sortOrder)
	}
	nulls := ""
	if sort.nullable {
		nulls = " NULLS LAST"
	}
	return ` ORDER BY ` + sort.expression + ` ` + direction + nulls + `, ` + codeReviewSortedRecency, nil
}

func codeReviewSortedCursorPredicate(filters CodeReviewListFilters) (string, pgx.NamedArgs, error) {
	sort, err := codeReviewListSortFor(filters.SortBy)
	if err != nil {
		return "", nil, err
	}
	comparison := ">"
	if filters.SortOrder == "desc" {
		comparison = "<"
	} else if filters.SortOrder != "asc" {
		return "", nil, fmt.Errorf("unsupported code review sort order: %q", filters.SortOrder)
	}
	if filters.CursorCreatedAt == nil {
		return "", nil, errors.New("sorted code review cursor is missing its recency anchor")
	}
	args := pgx.NamedArgs{"cursor": *filters.Cursor, "cursor_created_at": *filters.CursorCreatedAt}
	// The secondary key is fixed at (created_at DESC, id DESC), so the tuple
	// comparison stays "<" whichever way the primary key is ordered.
	recency := `(m.created_at, m.id) < (@cursor_created_at, @cursor)`
	if filters.CursorSortNull {
		if !sort.nullable {
			return "", nil, fmt.Errorf("code review sort %q cannot anchor on a null value", filters.SortBy)
		}
		return ` AND (` + sort.expression + ` IS NULL AND ` + recency + `)`, args, nil
	}
	if filters.CursorSortValue == nil {
		return "", nil, errors.New("sorted code review cursor value is missing")
	}
	args["cursor_sort_value"] = filters.CursorSortValue
	predicate := ` AND ((` + sort.expression + ` ` + comparison + ` @cursor_sort_value)
		OR (` + sort.expression + ` = @cursor_sort_value AND ` + recency + `)`
	if sort.nullable {
		predicate += `
		OR ` + sort.expression + ` IS NULL`
	}
	return predicate + `)`, args, nil
}

type CodeReviewStatsFilters struct {
	RepositoryID   *uuid.UUID
	Decision       *models.CodeReviewDecision
	Outcome        *models.CodeReviewListOutcome
	ActivityStatus *models.CodeReviewActivityStatus
	Status         *models.CodeReviewSessionStatus
	Acceptable     *bool
	Reason         *models.CodeReviewRiskReasonCode
	Author         string
	Search         string
	CreatedAfter   *time.Time
	CreatedBefore  *time.Time
}

type CodeReviewAnalyticsFilters struct {
	RepositoryID    *uuid.UUID
	CreatedAfter    *time.Time
	CreatedBefore   *time.Time
	AuthorSortBy    string
	AuthorSortOrder string
}

func (s *CodeReviewStore) ListReviews(ctx context.Context, orgID uuid.UUID, filters CodeReviewListFilters) ([]models.CodeReviewListItem, error) {
	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return s.listReviews(ctx, orgID, filters, limit)
}

func (s *CodeReviewStore) ListReviewsPage(ctx context.Context, orgID uuid.UUID, filters CodeReviewListFilters) (CodeReviewPage, error) {
	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	where, args, err := codeReviewListWhere(orgID, filters, false)
	if err != nil {
		return CodeReviewPage{}, err
	}
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)`+codeReviewListCountFrom+where, args).Scan(&total); err != nil {
		return CodeReviewPage{}, fmt.Errorf("count code reviews: %w", err)
	}
	items, err := s.listReviews(ctx, orgID, filters, limit+1)
	if err != nil {
		return CodeReviewPage{}, err
	}
	page := CodeReviewPage{Items: items, TotalCount: total}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].ID.String()
		page.NextCursorCreatedAt = page.Items[len(page.Items)-1].CreatedAt
	}
	return page, nil
}

func (s *CodeReviewStore) GetReviewStats(ctx context.Context, orgID uuid.UUID, filters CodeReviewStatsFilters) (models.CodeReviewStats, error) {
	args := pgx.NamedArgs{"org_id": orgID}
	query := `
		SELECT
			COUNT(*) FILTER (WHERE m.status = 'completed')::bigint AS reviews_completed,
			COUNT(*) FILTER (
				WHERE m.status = 'completed'
				  AND m.decision = 'approved'
				  AND m.github_review_id IS NOT NULL
			)::bigint AS automatically_approved,
			COUNT(*) FILTER (
				WHERE m.status = 'completed'
				  AND m.decision = 'needs_human_review'
			)::bigint AS needs_human_review,
			COALESCE(
				percentile_cont(0.5) WITHIN GROUP (
					ORDER BY EXTRACT(EPOCH FROM (m.completed_at - m.created_at))
				) FILTER (
					WHERE m.status = 'completed'
					  AND m.completed_at IS NOT NULL
					  AND m.completed_at >= m.created_at
				),
				-1
			)::double precision AS median_turnaround_seconds
		FROM code_review_session_metadata m
		JOIN sessions s ON s.id = m.session_id AND s.org_id = m.org_id
		JOIN pull_requests pr ON pr.id = m.pull_request_id AND pr.org_id = m.org_id
		WHERE m.org_id = @org_id`
	if filters.RepositoryID != nil {
		query += `
		  AND m.repository_id = @repository_id`
		args["repository_id"] = *filters.RepositoryID
	}
	if filters.Decision != nil {
		if err := filters.Decision.Validate(); err != nil {
			return models.CodeReviewStats{}, err
		}
		query += `
		  AND m.decision = @decision`
		args["decision"] = *filters.Decision
	}
	if filters.Outcome != nil {
		if err := filters.Outcome.Validate(); err != nil {
			return models.CodeReviewStats{}, err
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
	activityPredicate, err := codeReviewActivityStatusPredicate(filters.ActivityStatus)
	if err != nil {
		return models.CodeReviewStats{}, err
	}
	query += activityPredicate
	if filters.Status != nil {
		if err := filters.Status.Validate(); err != nil {
			return models.CodeReviewStats{}, err
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
	if filters.Reason != nil {
		if err := filters.Reason.Validate(); err != nil {
			return models.CodeReviewStats{}, err
		}
		query += `
		  AND EXISTS (
			SELECT 1
			FROM jsonb_array_elements(m.risk_reason_details) AS risk_reason
			WHERE risk_reason->>'code' = @reason
		  )`
		args["reason"] = *filters.Reason
	}
	if author := strings.TrimSpace(filters.Author); author != "" {
		query += `
		  AND LOWER(COALESCE(NULLIF(s.revision_context->>'pull_request_author', ''), 'Unknown')) = LOWER(@author)`
		args["author"] = author
	}
	if filters.CreatedAfter != nil {
		query += `
		  AND m.created_at >= @created_after`
		args["created_after"] = *filters.CreatedAfter
	}
	if filters.CreatedBefore != nil {
		query += `
		  AND m.created_at <= @created_before`
		args["created_before"] = *filters.CreatedBefore
	}
	if search := strings.TrimSpace(filters.Search); search != "" {
		query += `
		  AND (pr.title ILIKE @search OR pr.github_repo ILIKE @search OR pr.github_pr_number::text = @search_exact OR COALESCE(s.title, '') ILIKE @search)`
		args["search"] = "%" + search + "%"
		args["search_exact"] = strings.TrimPrefix(search, "#")
	}

	var stats models.CodeReviewStats
	var medianTurnaroundSeconds float64
	if err := s.db.QueryRow(ctx, query, args).Scan(
		&stats.ReviewsCompleted,
		&stats.AutomaticallyApproved,
		&stats.NeedsHumanReview,
		&medianTurnaroundSeconds,
	); err != nil {
		return models.CodeReviewStats{}, fmt.Errorf("query code review stats: %w", err)
	}
	if medianTurnaroundSeconds >= 0 {
		stats.MedianTurnaroundSeconds = &medianTurnaroundSeconds
	}
	return stats, nil
}

func codeReviewOptionalMetric(value float64) *float64 {
	if value < 0 {
		return nil
	}
	return &value
}

// GetReviewAnalytics returns one observation per pull request. The cohort is
// selected by the first attempt's creation time; every later attempt is then
// considered when deriving the PR's eventual outcome.
func (s *CodeReviewStore) GetReviewAnalytics(ctx context.Context, orgID uuid.UUID, filters CodeReviewAnalyticsFilters) (models.CodeReviewAnalytics, error) {
	authorOrder, err := codeReviewAuthorAnalyticsOrder(filters.AuthorSortBy, filters.AuthorSortOrder)
	if err != nil {
		return models.CodeReviewAnalytics{}, err
	}
	args := pgx.NamedArgs{"org_id": orgID}
	// A pull request belongs to exactly one repository, so the repository
	// predicate can narrow every scan of code_review_session_metadata: it can
	// neither change which attempt is first nor drop an attempt belonging to a
	// cohort PR. Applying it to each base scan keeps
	// idx_code_review_metadata_reviews (org_id, repository_id, created_at)
	// usable instead of reading org-wide history and filtering afterwards.
	scanWhere := ""
	if filters.RepositoryID != nil {
		scanWhere += " AND m.repository_id = @repository_id"
		args["repository_id"] = *filters.RepositoryID
	}
	// The time bounds describe the first attempt itself, so they can only be
	// applied after the per-PR dedupe: an attempt inside the window does not
	// put its PR in the cohort when an earlier attempt fell outside it.
	cohortWhere := ""
	if filters.CreatedAfter != nil {
		cohortWhere += " AND first_attempt.first_requested_at >= @created_after"
		args["created_after"] = *filters.CreatedAfter
	}
	if filters.CreatedBefore != nil {
		cohortWhere += " AND first_attempt.first_requested_at <= @created_before"
		args["created_before"] = *filters.CreatedBefore
	}
	query := `
	WITH first_attempt AS MATERIALIZED (
		SELECT DISTINCT ON (m.pull_request_id)
			m.pull_request_id, m.repository_id, m.created_at AS first_requested_at
		FROM code_review_session_metadata m
		WHERE m.org_id = @org_id` + scanWhere + `
		ORDER BY m.pull_request_id, m.created_at, m.id
	),
	cohort AS MATERIALIZED (
		-- Resolve the captured author only for PRs that survive the cohort
		-- filters. Keeping this lookup out of first_attempt stops it from
		-- running once per attempt row across the whole organization.
		SELECT first_attempt.*, COALESCE(captured.author, 'Unknown') AS author
		FROM first_attempt
		JOIN pull_requests pr ON pr.id = first_attempt.pull_request_id AND pr.org_id = @org_id
		LEFT JOIN LATERAL (
			SELECT NULLIF(author_session.revision_context->>'pull_request_author', '') AS author
			FROM code_review_session_metadata author_attempt
			JOIN sessions author_session
			  ON author_session.id = author_attempt.session_id
			 AND author_session.org_id = author_attempt.org_id
			WHERE author_attempt.org_id = @org_id
			  AND author_attempt.pull_request_id = first_attempt.pull_request_id
			  AND NULLIF(author_session.revision_context->>'pull_request_author', '') IS NOT NULL
			ORDER BY author_attempt.created_at, author_attempt.id
			LIMIT 1
		) captured ON TRUE
		WHERE TRUE` + cohortWhere + `
	),
	attempts AS MATERIALIZED (
		SELECT m.*
		FROM code_review_session_metadata m
		JOIN cohort c ON c.pull_request_id = m.pull_request_id
		WHERE m.org_id = @org_id` + scanWhere + `
	),
	comment_request_candidates AS (
		SELECT a.pull_request_id,
			COALESCE(
				NULLIF(LOWER(BTRIM(s.revision_context #>> '{request_context,author_login}')), ''),
				'Unknown'
			) AS github_login,
			BTRIM(s.revision_context->>'github_delivery_id') AS request_key,
			a.created_at, a.id
		FROM attempts a
		JOIN sessions s ON s.id = a.session_id AND s.org_id = @org_id
		WHERE s.revision_context #>> '{request_context,source}' = 'issue_comment'
		  AND NULLIF(BTRIM(s.revision_context->>'github_delivery_id'), '') IS NOT NULL
	),
	comment_requests AS MATERIALIZED (
		-- A GitHub redelivery or internal retry must not turn one human comment
		-- into several request observations. Comment-triggered reviews always
		-- persist the delivery id, falling back to the globally unique comment id.
		SELECT DISTINCT ON (pull_request_id, request_key)
			pull_request_id, request_key, github_login
		FROM comment_request_candidates
		ORDER BY pull_request_id, request_key, created_at, id
	),
	comment_request_users AS (
		SELECT github_login, COUNT(*)::bigint AS requests
		FROM comment_requests
		GROUP BY github_login
	),
	attempt_flags AS (
		-- One grouped pass instead of a correlated EXISTS per cohort PR: an
		-- EXISTS sublink in a target list cannot become a semi-join, so it
		-- would rescan the whole materialized attempt set for every row.
		SELECT pull_request_id,
			bool_or(status = 'failed') AS had_failed,
			bool_or(status = 'stale') AS had_stale
		FROM attempts
		GROUP BY pull_request_id
	),
	completed_ranked AS (
		SELECT a.*,
			ROW_NUMBER() OVER (
				PARTITION BY a.pull_request_id, a.head_sha
				ORDER BY
					(a.decision = 'approved' AND a.github_review_id IS NOT NULL) IS TRUE DESC,
					CASE WHEN a.decision = 'approved' AND a.github_review_id IS NOT NULL THEN a.completed_at END ASC,
					a.completed_at DESC, a.id DESC
			) AS duplicate_rank
		FROM attempts a
		WHERE a.status = 'completed' AND a.completed_at IS NOT NULL
	),
	distinct_heads AS (
		SELECT r.*,
			ROW_NUMBER() OVER (
				PARTITION BY r.pull_request_id ORDER BY r.completed_at, r.id
			)::bigint AS round_number
		FROM completed_ranked r
		WHERE r.duplicate_rank = 1
	),
	first_approvals AS (
		SELECT DISTINCT ON (pull_request_id)
			pull_request_id, round_number, session_id
		FROM distinct_heads
		WHERE decision = 'approved' AND github_review_id IS NOT NULL
		ORDER BY pull_request_id, round_number
	),
	representatives AS (
		SELECT DISTINCT ON (h.pull_request_id) h.*
		FROM distinct_heads h
		LEFT JOIN first_approvals approval ON approval.pull_request_id = h.pull_request_id
		ORDER BY h.pull_request_id,
			(h.session_id = approval.session_id) DESC,
			h.completed_at DESC, h.id DESC
	),
	finding_rollup AS (
		SELECT f.session_id, COUNT(*)::bigint AS finding_count,
			COUNT(*) FILTER (WHERE f.severity IN ('critical', 'high'))::bigint AS blocking_finding_count
		FROM code_review_findings f
		JOIN representatives r ON r.session_id = f.session_id
		WHERE f.org_id = @org_id
		GROUP BY f.session_id
	),
	pr_facts AS MATERIALIZED (
		SELECT c.pull_request_id, c.author, r.session_id, r.decision, r.github_review_id,
			r.additions, r.deletions,
			approval.round_number AS approval_round,
			COALESCE(flags.had_failed, false) AS had_failed,
			COALESCE(flags.had_stale, false) AS had_stale,
			COALESCE(findings.finding_count, 0) AS finding_count,
			COALESCE(findings.blocking_finding_count, 0) AS blocking_finding_count
		FROM cohort c
		LEFT JOIN representatives r ON r.pull_request_id = c.pull_request_id
		LEFT JOIN first_approvals approval ON approval.pull_request_id = c.pull_request_id
		LEFT JOIN attempt_flags flags ON flags.pull_request_id = c.pull_request_id
		LEFT JOIN finding_rollup findings ON findings.session_id = r.session_id
	),
	summary AS (
		SELECT COUNT(*)::bigint AS prs_reviewed,
			COUNT(*) FILTER (WHERE session_id IS NOT NULL)::bigint AS prs_with_completed_round,
			COUNT(*) FILTER (WHERE approval_round IS NOT NULL)::bigint AS approved_by_143,
			COUNT(*) FILTER (WHERE session_id IS NOT NULL AND approval_round IS NULL)::bigint AS not_approved,
			COUNT(*) FILTER (WHERE approval_round = 1)::bigint AS approved_first_round,
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY approval_round)
				FILTER (WHERE approval_round IS NOT NULL), -1)::double precision AS median_rounds_to_approval,
			COUNT(*) FILTER (WHERE had_failed)::bigint AS prs_with_failed_attempt,
			COUNT(*) FILTER (WHERE had_stale)::bigint AS prs_with_stale_attempt,
			COUNT(*) FILTER (WHERE session_id IS NOT NULL AND additions IS NOT NULL AND deletions IS NOT NULL)::bigint AS prs_with_change_breakdown,
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY additions), -1)::double precision AS median_additions,
			COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY deletions), -1)::double precision AS median_deletions,
			COUNT(*) FILTER (WHERE finding_count > 0)::bigint AS prs_with_findings,
			COUNT(*) FILTER (WHERE blocking_finding_count > 0)::bigint AS prs_with_blocking_findings,
			COALESCE(SUM(finding_count), 0)::bigint AS total_findings,
			COUNT(*) FILTER (WHERE decision = 'needs_human_review')::bigint AS needs_human_review,
			COUNT(*) FILTER (WHERE decision = 'comment_only')::bigint AS comment_only,
			COUNT(*) FILTER (WHERE decision = 'blocked')::bigint AS blocked,
			COUNT(*) FILTER (WHERE decision = 'approved' AND github_review_id IS NULL)::bigint AS approval_not_posted
		FROM pr_facts
	),
	authors AS (
		SELECT author, COUNT(*)::bigint AS prs_reviewed,
			COUNT(*) FILTER (WHERE approval_round IS NOT NULL)::bigint AS approved_by_143,
			COUNT(*) FILTER (WHERE session_id IS NOT NULL AND approval_round IS NULL)::bigint AS not_approved,
			COUNT(*) FILTER (WHERE approval_round = 1)::bigint AS approved_first_round,
			(percentile_cont(0.5) WITHIN GROUP (ORDER BY approval_round)
				FILTER (WHERE approval_round IS NOT NULL))::double precision AS median_rounds_to_approval,
			(percentile_cont(0.5) WITHIN GROUP (ORDER BY additions))::double precision AS median_additions,
			(percentile_cont(0.5) WITHIN GROUP (ORDER BY deletions))::double precision AS median_deletions
		FROM pr_facts GROUP BY author
	),
	reasons AS (
		SELECT reason->>'code' AS code, COUNT(DISTINCT h.pull_request_id)::bigint AS prs
		FROM distinct_heads h
		LEFT JOIN first_approvals approval ON approval.pull_request_id = h.pull_request_id
		CROSS JOIN LATERAL jsonb_array_elements(h.risk_reason_details) reason
		WHERE (approval.round_number IS NULL OR h.round_number <= approval.round_number)
		  AND (h.decision IS DISTINCT FROM 'approved' OR h.github_review_id IS NULL)
		  AND NULLIF(reason->>'code', '') IS NOT NULL
		GROUP BY reason->>'code'
	),
	approval_rounds AS (
		-- Built as a literal array over one aggregate pass so the report always
		-- carries every mutually exclusive bucket, in order: the page renders one
		-- card per element, so a missing bucket would silently drop a card.
		SELECT jsonb_build_array(
			jsonb_build_object('bucket', 'round_1', 'prs', COUNT(*) FILTER (WHERE approval_round = 1)),
			jsonb_build_object('bucket', 'round_2', 'prs', COUNT(*) FILTER (WHERE approval_round = 2)),
			jsonb_build_object('bucket', 'round_3', 'prs', COUNT(*) FILTER (WHERE approval_round = 3)),
			jsonb_build_object('bucket', 'round_4_plus', 'prs', COUNT(*) FILTER (WHERE approval_round >= 4)),
			jsonb_build_object('bucket', 'not_yet_approved', 'prs', COUNT(*) FILTER (WHERE approval_round IS NULL))
		) AS buckets
		FROM pr_facts
	)
	SELECT s.*,
		(SELECT buckets FROM approval_rounds) AS approval_rounds,
		COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY ` + authorOrder + `) FROM authors a), '[]') AS authors,
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.prs DESC, r.code) FROM reasons r), '[]') AS reasons,
		(SELECT COUNT(*)::bigint FROM comment_requests) AS comment_requests_total,
		COALESCE((
			SELECT jsonb_agg(to_jsonb(requester) ORDER BY requester.requests DESC, requester.github_login)
			FROM comment_request_users requester
		), '[]') AS comment_requests_by_user
	FROM summary s`

	var analytics models.CodeReviewAnalytics
	var medianRounds, medianAdditions, medianDeletions float64
	var roundsJSON, authorsJSON, reasonsJSON, commentRequestUsersJSON []byte
	err = s.db.QueryRow(ctx, query, args).Scan(
		&analytics.Summary.PRsReviewed, &analytics.Summary.PRsWithCompletedRound,
		&analytics.Summary.ApprovedBy143, &analytics.Summary.NotApproved,
		&analytics.Summary.ApprovedFirstRound, &medianRounds,
		&analytics.Summary.PRsWithFailedAttempt, &analytics.Summary.PRsWithStaleAttempt,
		&analytics.Summary.PRsWithChangeBreakdown,
		&medianAdditions, &medianDeletions,
		&analytics.Summary.PRsWithFindings, &analytics.Summary.PRsWithBlockingFindings,
		&analytics.Summary.TotalFindings, &analytics.Summary.NeedsHumanReview,
		&analytics.Summary.CommentOnly, &analytics.Summary.Blocked,
		&analytics.Summary.ApprovalNotPosted, &roundsJSON, &authorsJSON,
		&reasonsJSON, &analytics.CommentRequestsTotal, &commentRequestUsersJSON,
	)
	if err != nil {
		return models.CodeReviewAnalytics{}, fmt.Errorf("query PR-centric code review analytics: %w", err)
	}
	analytics.Summary.MedianRoundsToApproval = codeReviewOptionalMetric(medianRounds)
	analytics.Summary.MedianAdditions = codeReviewOptionalMetric(medianAdditions)
	analytics.Summary.MedianDeletions = codeReviewOptionalMetric(medianDeletions)
	for _, section := range []struct {
		name    string
		payload []byte
		target  any
	}{
		{"approval rounds", roundsJSON, &analytics.ApprovalRounds},
		{"authors", authorsJSON, &analytics.Authors},
		{"non-approval reasons", reasonsJSON, &analytics.NonApprovalReasons},
		{"comment requests by user", commentRequestUsersJSON, &analytics.CommentRequestsByUser},
	} {
		if err := json.Unmarshal(section.payload, section.target); err != nil {
			return models.CodeReviewAnalytics{}, fmt.Errorf("decode PR-centric code review analytics %s: %w", section.name, err)
		}
	}
	// The page renders one card per bucket, so a partial or repeated set would
	// silently drop or double a card rather than fail.
	seenBuckets := make(map[models.CodeReviewApprovalRoundBucket]struct{}, len(analytics.ApprovalRounds))
	for _, bucket := range analytics.ApprovalRounds {
		if err := bucket.Bucket.Validate(); err != nil {
			return models.CodeReviewAnalytics{}, err
		}
		if _, duplicate := seenBuckets[bucket.Bucket]; duplicate {
			return models.CodeReviewAnalytics{}, fmt.Errorf("duplicate approval round bucket: %q", bucket.Bucket)
		}
		seenBuckets[bucket.Bucket] = struct{}{}
	}
	if len(seenBuckets) != len(models.CodeReviewApprovalRoundBuckets) {
		return models.CodeReviewAnalytics{}, fmt.Errorf(
			"approval round buckets incomplete: got %d of %d",
			len(seenBuckets), len(models.CodeReviewApprovalRoundBuckets),
		)
	}
	for _, reason := range analytics.NonApprovalReasons {
		if err := reason.Code.Validate(); err != nil {
			return models.CodeReviewAnalytics{}, err
		}
	}
	return analytics, nil
}

func codeReviewAuthorAnalyticsOrder(sortBy, sortOrder string) (string, error) {
	columns := map[string]string{
		"author": "a.author", "reviews": "a.prs_reviewed", "approved": "a.approved_by_143",
		"not_approved":     "a.not_approved",
		"approval_rate":    "(a.approved_by_143::double precision / NULLIF(a.prs_reviewed, 0))",
		"first_round":      "a.approved_first_round",
		"median_rounds":    "a.median_rounds_to_approval",
		"median_additions": "a.median_additions",
		"median_deletions": "a.median_deletions",
	}
	if sortBy == "" {
		return "a.prs_reviewed DESC, a.author ASC", nil
	}
	column, ok := columns[sortBy]
	if !ok {
		return "", fmt.Errorf("unsupported code review author sort: %q", sortBy)
	}
	direction := "ASC"
	if sortOrder == "desc" {
		direction = "DESC"
	} else if sortOrder != "" && sortOrder != "asc" {
		return "", fmt.Errorf("unsupported code review author sort order: %q", sortOrder)
	}
	return column + " " + direction + " NULLS LAST, a.author ASC", nil
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

func (s *CodeReviewStore) CreatePromptRecord(ctx context.Context, record *models.CodeReviewPromptRecord) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO code_review_prompt_records (
			org_id, session_id, record_key, role, agent_provider, content, metadata
		) VALUES (
			@org_id, @session_id, @record_key, @role, @agent_provider, @content, COALESCE(@metadata, '{}'::jsonb)
		)
		ON CONFLICT (org_id, record_key) DO UPDATE
		SET content = EXCLUDED.content,
		    metadata = EXCLUDED.metadata
		RETURNING `+codeReviewPromptRecordColumns, pgx.NamedArgs{
		"org_id":         record.OrgID,
		"session_id":     record.SessionID,
		"record_key":     record.RecordKey,
		"role":           record.Role,
		"agent_provider": record.AgentProvider,
		"content":        record.Content,
		"metadata":       record.Metadata,
	})
	if err != nil {
		return fmt.Errorf("create code review prompt record: %w", err)
	}
	created, err := collectOneCodeReviewPromptRecord(rows)
	if err != nil {
		return err
	}
	*record = created
	return nil
}

func (s *CodeReviewStore) ListPromptRecords(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.CodeReviewPromptRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewPromptRecordColumns+`
		FROM code_review_prompt_records
		WHERE org_id = @org_id
		  AND session_id = @session_id
		ORDER BY created_at ASC, id ASC`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("list code review prompt records: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewPromptRecord])
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
	// severity is a text enum, so a bare ORDER BY would sort alphabetically
	// (medium > low > info > high > critical); rank it explicitly instead.
	rows, err := s.db.Query(ctx, `
		SELECT `+codeReviewFindingColumns+`
		FROM code_review_findings
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND (@selected_only = false OR selected_for_inline = true)
		ORDER BY selected_for_inline DESC,
		         CASE severity
		           WHEN 'critical' THEN 5
		           WHEN 'high' THEN 4
		           WHEN 'medium' THEN 3
		           WHEN 'low' THEN 2
		           WHEN 'info' THEN 1
		           ELSE 0
		         END DESC,
		         created_at ASC, id ASC`, pgx.NamedArgs{
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

// RunWithGitHubPublicationLock serializes formal review body updates and rolling
// status comment writes for one pull request across workers. This prevents a
// delayed terminal sync from hiding the visible fallback for a newer review.
// The callback receives the transaction so its locked reads and writes do not
// acquire a second pool connection.
func (s *CodeReviewStore) RunWithGitHubPublicationLock(ctx context.Context, orgID, pullRequestID uuid.UUID, fn func(context.Context, DBTX) error) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("code review GitHub publication lock requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin code review GitHub publication lock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Keep the legacy key prefix so old and new workers coordinate during a
	// rolling deployment.
	lockKey := fmt.Sprintf("code_review_status_comment:%s:%s", orgID, pullRequestID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(@lock_key, 0))`, pgx.NamedArgs{"lock_key": lockKey}); err != nil {
		return fmt.Errorf("acquire code review GitHub publication lock: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit code review GitHub publication lock: %w", err)
	}
	return nil
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

func scanCodeReviewPolicy(rows pgx.Rows) (models.CodeReviewPolicyRecord, error) {
	var record models.CodeReviewPolicyRecord
	var descriptionPolicy, riskPolicy, agentRoster []byte
	if err := rows.Scan(&record.ID, &record.OrgID, &record.RepositoryID, &record.Active, &record.Version, &record.Enabled, &record.ApprovalMode,
		&record.ReviewInstructions, &record.AutomatedApprovalPolicy, &descriptionPolicy, &riskPolicy, &agentRoster, &record.InlineCommentLimit, &record.CreatedByUserID, &record.CreatedAt); err != nil {
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

func collectOneCodeReviewPromptRecord(rows pgx.Rows) (models.CodeReviewPromptRecord, error) {
	defer rows.Close()
	record, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewPromptRecord])
	if err != nil {
		return models.CodeReviewPromptRecord{}, err
	}
	return record, nil
}
