package db

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStore_ResolvePolicyPrefersRepository(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	policyID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	config := models.DefaultCodeReviewPolicyConfig()
	config.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	descriptionPolicy, riskPolicy, agentRoster := mustCodeReviewPolicyJSON(t, config)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM code_review_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "final_review_template", "created_by_user_id", "created_at",
		}).AddRow(policyID, orgID, &repoID, true, 3, config.Enabled, config.ApprovalMode, descriptionPolicy, riskPolicy, agentRoster, config.InlineCommentLimit, config.FinalReviewTemplate, &userID, now))

	resolved, err := NewCodeReviewStore(mock).ResolvePolicy(context.Background(), orgID, &repoID)

	require.NoError(t, err, "ResolvePolicy should load active code review policy")
	require.Equal(t, "repository", resolved.Source, "repository override should win over org default")
	require.NotNil(t, resolved.Policy, "resolved policy should include the backing record")
	require.Equal(t, 3, resolved.Policy.Version, "resolved policy should scan version")
	require.Equal(t, models.CodeReviewApprovalModeApproveAcceptable, resolved.Config.ApprovalMode, "resolved config should include approval mode")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_ResolvePolicyUsesDefaultWhenMissing(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM code_review_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "final_review_template", "created_by_user_id", "created_at",
		}))

	resolved, err := NewCodeReviewStore(mock).ResolvePolicy(context.Background(), orgID, &repoID)

	require.NoError(t, err, "ResolvePolicy should not error when no policy exists")
	require.Equal(t, "default", resolved.Source, "missing policy should use built-in defaults")
	require.Nil(t, resolved.Policy, "default policy should not pretend to have a DB record")
	require.Equal(t, models.DefaultCodeReviewPolicyConfig(), resolved.Config, "default resolved config should match built-in policy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_GetPolicyByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	policyID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	config := models.DefaultCodeReviewPolicyConfig()
	config.FinalReviewTemplate = "final review template"
	descriptionPolicy, riskPolicy, agentRoster := mustCodeReviewPolicyJSON(t, config)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("WHERE org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "final_review_template", "created_by_user_id", "created_at",
		}).AddRow(policyID, orgID, &repoID, true, 2, config.Enabled, config.ApprovalMode, descriptionPolicy, riskPolicy, agentRoster, config.InlineCommentLimit, config.FinalReviewTemplate, &userID, now))

	record, err := NewCodeReviewStore(mock).GetPolicyByID(context.Background(), orgID, policyID)

	require.NoError(t, err, "GetPolicyByID should load captured policy version")
	require.Equal(t, policyID, record.ID, "GetPolicyByID should return requested policy")
	require.Equal(t, config.FinalReviewTemplate, record.FinalReviewTemplate, "GetPolicyByID should scan final review template")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_SavePolicyVersionsInsertOnly(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	policyID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	config := models.DefaultCodeReviewPolicyConfig()
	config.FinalReviewTemplate = "custom final review template"
	descriptionPolicy, riskPolicy, agentRoster := mustCodeReviewPolicyJSON(t, config)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(4))
	mock.ExpectExec("UPDATE code_review_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO code_review_policies").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "final_review_template", "created_by_user_id", "created_at",
		}).AddRow(policyID, orgID, &repoID, true, 4, config.Enabled, config.ApprovalMode, descriptionPolicy, riskPolicy, agentRoster, config.InlineCommentLimit, config.FinalReviewTemplate, &userID, now))
	mock.ExpectCommit()

	record, err := NewCodeReviewStore(mock).SavePolicy(context.Background(), orgID, &repoID, config, &userID)

	require.NoError(t, err, "SavePolicy should insert a new active version")
	require.Equal(t, 4, record.Version, "SavePolicy should increment from the current scope max version")
	require.Equal(t, policyID, record.ID, "SavePolicy should return inserted policy")
	require.Equal(t, config.FinalReviewTemplate, record.Config().FinalReviewTemplate, "SavePolicy should preserve final review template")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_CreateSessionMetadataReusesOutputKey(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO code_review_session_metadata")).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
		}).AddRow(metadataID, orgID, sessionID, repoID, prID, policyID, "base", "head", models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusQueued, nil, nil, false, nil, "pr:head:policy", nil, nil, nil, nil, nil, nil, now))

	metadata := &models.CodeReviewSessionMetadata{
		OrgID:           orgID,
		SessionID:       sessionID,
		RepositoryID:    repoID,
		PullRequestID:   prID,
		PolicyID:        policyID,
		BaseSHA:         "base",
		HeadSHA:         "head",
		TriggerSource:   models.CodeReviewTriggerSourceAppReviewer,
		Status:          models.CodeReviewSessionStatusQueued,
		ReviewOutputKey: "pr:head:policy",
	}

	err = NewCodeReviewStore(mock).CreateSessionMetadata(context.Background(), metadata)

	require.NoError(t, err, "CreateSessionMetadata should upsert by stable output key")
	require.Equal(t, metadataID, metadata.ID, "CreateSessionMetadata should scan metadata id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_MarkStaleForPullRequestExceptHead(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectExec("UPDATE code_review_session_metadata").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	count, err := NewCodeReviewStore(mock).MarkStaleForPullRequestExceptHead(context.Background(), orgID, prID, "head-new")

	require.NoError(t, err, "MarkStaleForPullRequestExceptHead should stale older queued/running reviews")
	require.Equal(t, int64(2), count, "MarkStaleForPullRequestExceptHead should return affected row count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_CompleteReviewStoresGitHubReviewEvidence(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	metadataID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	reviewID := int64(123)
	reviewURL := "https://github.com/acme/repo/pull/42#pullrequestreview-123"
	body := "143 Code Reviewer approved this PR"
	decision := models.CodeReviewDecisionApproved
	acceptable := true
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("UPDATE code_review_session_metadata").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
		}).AddRow(metadataID, orgID, sessionID, repoID, prID, policyID, "base", "head", models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusCompleted, &decision, &acceptable, false, nil, "key", nil, &reviewID, &reviewURL, &body, nil, &now, now))

	metadata, err := NewCodeReviewStore(mock).CompleteReview(context.Background(), orgID, CompleteCodeReviewParams{
		SessionID:       sessionID,
		Decision:        models.CodeReviewDecisionApproved,
		Acceptable:      true,
		GitHubReviewID:  &reviewID,
		GitHubReviewURL: &reviewURL,
		FinalReviewBody: body,
	})

	require.NoError(t, err, "CompleteReview should persist final review evidence")
	require.Equal(t, models.CodeReviewSessionStatusCompleted, metadata.Status, "CompleteReview should mark review complete")
	require.Equal(t, &reviewURL, metadata.GitHubReviewURL, "CompleteReview should scan GitHub review URL")
	require.Equal(t, &body, metadata.FinalReviewBody, "CompleteReview should scan final review body")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_ListReviewsAppliesDesignFilters(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	metadataID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	decision := models.CodeReviewDecisionCommentOnly
	status := models.CodeReviewSessionStatusCompleted
	acceptable := false
	title := "Code review for acme/repo#42"
	repoName := "acme/repo"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("m.decision = @decision").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
			"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at", "session_title", "repository_name", "github_repo",
			"github_pr_number", "github_pr_url", "pull_request_title", "pull_request_author",
		}).AddRow(
			metadataID, orgID, sessionID, repoID, prID, policyID,
			"base", "head", models.CodeReviewTriggerSourceAppReviewer, status, &decision, &acceptable, false,
			nil, "key", nil, nil, nil, nil, nil, &now, now, &title, &repoName, "acme/repo",
			42, "https://github.com/acme/repo/pull/42", "Fix auth bug", "devin",
		))

	reviews, err := NewCodeReviewStore(mock).ListReviews(context.Background(), orgID, CodeReviewListFilters{
		RepositoryID: &repoID,
		Decision:     &decision,
		Status:       &status,
		Acceptable:   &acceptable,
		Search:       "auth",
		Limit:        25,
	})

	require.NoError(t, err, "ListReviews should return filtered code reviews")
	require.Len(t, reviews, 1, "ListReviews should scan matching rows")
	require.Equal(t, "Fix auth bug", reviews[0].PullRequestTitle, "ListReviews should return pull request metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func mustCodeReviewPolicyJSON(t *testing.T, config models.CodeReviewPolicyConfig) ([]byte, []byte, []byte) {
	t.Helper()
	descriptionPolicy, err := json.Marshal(config.DescriptionPolicy)
	require.NoError(t, err, "description policy should marshal")
	riskPolicy, err := json.Marshal(config.RiskPolicy)
	require.NoError(t, err, "risk policy should marshal")
	agentRoster, err := json.Marshal(config.AgentRoster)
	require.NoError(t, err, "agent roster should marshal")
	return descriptionPolicy, riskPolicy, agentRoster
}
