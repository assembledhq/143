package db

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStore_ResolvePolicyUsesOrganizationPolicy(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	policyID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	config := models.DefaultCodeReviewPolicyConfig()
	config.ReviewInstructions = "historic review guidance"
	config.AutomatedApprovalPolicy = "historic approval guidance"
	config.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	descriptionPolicy, riskPolicy, agentRoster := mustCodeReviewPolicyJSON(t, config)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM code_review_policies").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"review_instructions", "automated_approval_policy",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at",
		}).AddRow(policyID, orgID, nil, true, 3, config.Enabled, config.ApprovalMode, config.ReviewInstructions, config.AutomatedApprovalPolicy, descriptionPolicy, riskPolicy, agentRoster, config.InlineCommentLimit, &userID, now))

	resolved, err := NewCodeReviewStore(mock).ResolvePolicy(context.Background(), orgID)

	require.NoError(t, err, "ResolvePolicy should load active code review policy")
	require.Equal(t, "organization", resolved.Source, "the active organization policy should apply to every repository")
	require.NotNil(t, resolved.Policy, "resolved policy should include the backing record")
	require.Equal(t, 3, resolved.Policy.Version, "resolved policy should scan version")
	require.Equal(t, models.CodeReviewApprovalModeApproveAcceptable, resolved.Config.ApprovalMode, "resolved config should include approval mode")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_ResolvePolicyUsesDefaultWhenMissing(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM code_review_policies").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"review_instructions", "automated_approval_policy",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at",
		}))

	resolved, err := NewCodeReviewStore(mock).ResolvePolicy(context.Background(), orgID)

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
	config.ReviewInstructions = "historic review guidance"
	config.AutomatedApprovalPolicy = "historic approval guidance"
	descriptionPolicy, riskPolicy, agentRoster := mustCodeReviewPolicyJSON(t, config)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("WHERE org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"review_instructions", "automated_approval_policy",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at",
		}).AddRow(policyID, orgID, &repoID, true, 2, config.Enabled, config.ApprovalMode, config.ReviewInstructions, config.AutomatedApprovalPolicy, descriptionPolicy, riskPolicy, agentRoster, config.InlineCommentLimit, &userID, now))

	record, err := NewCodeReviewStore(mock).GetPolicyByID(context.Background(), orgID, policyID)

	require.NoError(t, err, "GetPolicyByID should load captured policy version")
	require.Equal(t, policyID, record.ID, "GetPolicyByID should return requested policy")
	require.Equal(t, config.ReviewInstructions, record.ReviewInstructions, "GetPolicyByID should return captured historic review instructions")
	require.Equal(t, config.AutomatedApprovalPolicy, record.AutomatedApprovalPolicy, "GetPolicyByID should return captured historic approval policy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_SavePolicyVersionsInsertOnly(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	policyID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	config := models.DefaultCodeReviewPolicyConfig()
	config.ReviewInstructions = "new review guidance"
	config.AutomatedApprovalPolicy = "new approval guidance"
	descriptionPolicy, riskPolicy, agentRoster := mustCodeReviewPolicyJSON(t, config)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs("code_review_policy:" + orgID.String()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(4))
	mock.ExpectExec("UPDATE code_review_policies").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO code_review_policies").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			config.ReviewInstructions, config.AutomatedApprovalPolicy,
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"review_instructions", "automated_approval_policy",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at",
		}).AddRow(policyID, orgID, nil, true, 4, config.Enabled, config.ApprovalMode, config.ReviewInstructions, config.AutomatedApprovalPolicy, descriptionPolicy, riskPolicy, agentRoster, config.InlineCommentLimit, &userID, now))
	mock.ExpectCommit()

	var logOutput bytes.Buffer
	store := NewCodeReviewStore(mock)
	store.SetLogger(zerolog.New(&logOutput))
	record, err := store.SavePolicy(context.Background(), orgID, config, &userID)

	require.NoError(t, err, "SavePolicy should insert a new active version")
	require.Equal(t, 4, record.Version, "SavePolicy should increment the organization policy version")
	require.Equal(t, policyID, record.ID, "SavePolicy should return inserted policy")
	require.Equal(t, config.ReviewInstructions, record.ReviewInstructions, "SavePolicy should persist the complete review instructions in the new version")
	require.Equal(t, config.AutomatedApprovalPolicy, record.AutomatedApprovalPolicy, "SavePolicy should persist the complete approval policy in the new version")
	require.Contains(t, logOutput.String(), `"review_instructions_runes":19`, "policy logs should record review-instruction rune count")
	require.Contains(t, logOutput.String(), `"automated_approval_policy_runes":21`, "policy logs should record approval-policy rune count")
	require.NotContains(t, logOutput.String(), config.ReviewInstructions, "policy logs should never contain review-instruction text")
	require.NotContains(t, logOutput.String(), config.AutomatedApprovalPolicy, "policy logs should never contain approval-policy text")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_CreatePromptArtifactPreservesEffectivePrompt(t *testing.T) {
	t.Parallel()
	orgID, sessionID, artifactID := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	content := "/review\n\n<organization_review_instructions>\ncaptured guidance\n</organization_review_instructions>"
	metadata := json.RawMessage(`{"policy_version":3}`)
	artifact := &models.CodeReviewPromptArtifact{OrgID: orgID, SessionID: sessionID, ArtifactKey: "prompts/reviewer", Role: "reviewer", AgentProvider: "codex", Content: content, Metadata: metadata}
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectQuery("INSERT INTO code_review_prompt_artifacts").WithArgs(
		orgID, sessionID, artifact.ArtifactKey, artifact.Role, artifact.AgentProvider, content, metadata,
	).WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "artifact_key", "role", "agent_provider", "content", "metadata", "created_at"}).
		AddRow(artifactID, orgID, sessionID, artifact.ArtifactKey, artifact.Role, artifact.AgentProvider, content, metadata, now))

	err = NewCodeReviewStore(mock).CreatePromptArtifact(context.Background(), artifact)

	require.NoError(t, err, "CreatePromptArtifact should persist the exact effective prompt")
	require.Equal(t, artifactID, artifact.ID, "CreatePromptArtifact should return the persisted artifact identity")
	require.Equal(t, content, artifact.Content, "prompt artifact should preserve captured instructions byte-for-byte")
	require.Equal(t, metadata, artifact.Metadata, "prompt artifact should preserve captured policy-version metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all prompt artifact expectations should be met")
}

func TestCodeReviewStore_GetActiveGitHubTriggerFiltersByOrgAndRepo(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	triggerID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("FROM code_review_github_trigger_settings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(codeReviewGitHubTriggerColumns()).AddRow(
			triggerID, orgID, repoID, int64(123), true, 2, "143-code-reviewer", "143 Code Reviewer", int64(143), "pull", &userID, now,
		))

	setting, err := NewCodeReviewStore(mock).GetActiveGitHubTrigger(context.Background(), orgID, repoID)

	require.NoError(t, err, "GetActiveGitHubTrigger should load active trigger settings")
	require.Equal(t, triggerID, setting.ID, "GetActiveGitHubTrigger should return the matching trigger")
	require.Equal(t, repoID, setting.RepositoryID, "GetActiveGitHubTrigger should scope by repository")
	require.Equal(t, "143-code-reviewer", setting.TeamSlug, "GetActiveGitHubTrigger should scan team slug")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_SaveGitHubTriggerVersionsInsertOnly(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	triggerID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(3))
	mock.ExpectExec("UPDATE code_review_github_trigger_settings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO code_review_github_trigger_settings").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(codeReviewGitHubTriggerColumns()).AddRow(
			triggerID, orgID, repoID, int64(123), true, 3, "143-code-reviewer", "143 Code Reviewer", int64(143), "pull", &userID, now,
		))
	mock.ExpectCommit()

	setting, err := NewCodeReviewStore(mock).SaveGitHubTrigger(context.Background(), orgID, SaveCodeReviewGitHubTriggerParams{
		RepositoryID:    repoID,
		InstallationID:  123,
		TeamSlug:        "143-code-reviewer",
		TeamName:        "143 Code Reviewer",
		TeamID:          143,
		RepoPermission:  "pull",
		CreatedByUserID: &userID,
	})

	require.NoError(t, err, "SaveGitHubTrigger should insert a new active version")
	require.Equal(t, 3, setting.Version, "SaveGitHubTrigger should increment from the current scope max version")
	require.Equal(t, models.CodeReviewGitHubTriggerRepoPermissionPull, setting.RepoPermission, "SaveGitHubTrigger should persist pull access")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_DeactivateGitHubTriggerWritesInactiveVersion(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	triggerID := uuid.New()
	userID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(4))
	mock.ExpectQuery("UPDATE code_review_github_trigger_settings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(codeReviewGitHubTriggerColumns()).AddRow(
			triggerID, orgID, repoID, int64(123), false, 3, "143-code-reviewer", "143 Code Reviewer", int64(143), "pull", &userID, now,
		))
	mock.ExpectExec("INSERT INTO code_review_github_trigger_settings").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = NewCodeReviewStore(mock).DeactivateGitHubTrigger(context.Background(), orgID, repoID, &userID)

	require.NoError(t, err, "DeactivateGitHubTrigger should write an inactive tombstone version")
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
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
		}).AddRow(metadataID, orgID, sessionID, repoID, prID, policyID, "base", "head", true, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusQueued, nil, nil, false, nil, "pr:head:policy", nil, nil, nil, nil, nil, nil, now))

	metadata := &models.CodeReviewSessionMetadata{
		OrgID:           orgID,
		SessionID:       sessionID,
		RepositoryID:    repoID,
		PullRequestID:   prID,
		PolicyID:        policyID,
		BaseSHA:         "base",
		HeadSHA:         "head",
		FromFork:        true,
		TriggerSource:   models.CodeReviewTriggerSourceAppReviewer,
		Status:          models.CodeReviewSessionStatusQueued,
		ReviewOutputKey: "pr:head:policy",
	}

	err = NewCodeReviewStore(mock).CreateSessionMetadata(context.Background(), metadata)

	require.NoError(t, err, "CreateSessionMetadata should upsert by stable output key")
	require.Equal(t, metadataID, metadata.ID, "CreateSessionMetadata should scan metadata id")
	require.True(t, metadata.FromFork, "CreateSessionMetadata should persist fork source evidence")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestCodeReviewStore_CompleteReviewPublishesUpdate verifies the store fans a
// lifecycle event out to the org-scoped SSE stream after a status transition,
// so the live code reviews list refreshes. miniredis-backed, mirroring the
// SessionStore publish tests.
func TestCodeReviewStore_CompleteReviewPublishesUpdate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	decisionApproved := models.CodeReviewDecisionApproved
	acceptableTrue := true
	finalBody := "final review body"
	completedAt := now

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize against miniredis")
	streams := cache.NewCodeReviewStreams(client, zerolog.Nop())
	sub, err := streams.Subscribe(orgID)
	require.NoError(t, err, "subscribe should succeed against miniredis")
	defer sub.Close()

	mock.ExpectQuery(regexp.QuoteMeta("UPDATE code_review_session_metadata")).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
		}).AddRow(
			metadataID, orgID, sessionID, uuid.New(), uuid.New(), uuid.New(),
			"base", "head", false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusCompleted, &decisionApproved, &acceptableTrue, false,
			nil, "pr:head:policy", nil, nil, nil, &finalBody, nil, &completedAt, now,
		))

	store := NewCodeReviewStore(mock)
	store.SetStreams(streams)
	store.SetLogger(zerolog.Nop())

	_, err = store.CompleteReview(context.Background(), orgID, CompleteCodeReviewParams{
		SessionID:       sessionID,
		Decision:        models.CodeReviewDecisionApproved,
		Acceptable:      true,
		FinalReviewBody: "final review body",
	})
	require.NoError(t, err, "CompleteReview should persist the completed transition")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")

	require.Eventually(t, func() bool {
		select {
		case event := <-sub.C:
			return event.SessionID != nil && *event.SessionID == sessionID && event.Status == models.CodeReviewSessionStatusCompleted
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "CompleteReview should publish a code review update event to subscribers")
}

// TestCodeReviewStore_MarkStaleForPullRequestExceptHeadPublishesUpdate covers
// the batch transition: it touches many rows at once, so it publishes a single
// org-scoped event with no session ID (session_id omitted) when any rows change.
func TestCodeReviewStore_MarkStaleForPullRequestExceptHeadPublishesUpdate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	pullRequestID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize against miniredis")
	streams := cache.NewCodeReviewStreams(client, zerolog.Nop())
	sub, err := streams.Subscribe(orgID)
	require.NoError(t, err, "subscribe should succeed against miniredis")
	defer sub.Close()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE code_review_session_metadata")).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	store := NewCodeReviewStore(mock)
	store.SetStreams(streams)
	store.SetLogger(zerolog.Nop())

	affected, err := store.MarkStaleForPullRequestExceptHead(context.Background(), orgID, pullRequestID, "newhead", nil)
	require.NoError(t, err, "MarkStaleForPullRequestExceptHead should run the batch update")
	require.Equal(t, int64(2), affected, "MarkStaleForPullRequestExceptHead should report rows affected")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")

	require.Eventually(t, func() bool {
		select {
		case event := <-sub.C:
			return event.SessionID == nil && event.OrgID == orgID && event.Status == models.CodeReviewSessionStatusStale
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "batch stale transition should publish one org-scoped event with no session id")
}

func TestCodeReviewStore_MarkStaleForPullRequestExceptHeadSkipsPublishWhenNoRows(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	pullRequestID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize against miniredis")
	streams := cache.NewCodeReviewStreams(client, zerolog.Nop())
	sub, err := streams.Subscribe(orgID)
	require.NoError(t, err, "subscribe should succeed against miniredis")
	defer sub.Close()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE code_review_session_metadata")).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	store := NewCodeReviewStore(mock)
	store.SetStreams(streams)
	store.SetLogger(zerolog.Nop())

	affected, err := store.MarkStaleForPullRequestExceptHead(context.Background(), orgID, pullRequestID, "newhead", nil)
	require.NoError(t, err, "MarkStaleForPullRequestExceptHead should run the batch update")
	require.Equal(t, int64(0), affected, "no rows should be affected")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")

	require.Never(t, func() bool {
		select {
		case <-sub.C:
			return true
		default:
			return false
		}
	}, 150*time.Millisecond, 20*time.Millisecond, "a no-op batch update should not publish an event")
}

func TestCodeReviewStore_GetByOutputKeyFiltersByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	outputKey := "pr:head:policy"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("review_output_key = @review_output_key").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
		}).AddRow(metadataID, orgID, sessionID, repoID, prID, policyID, "base", "head", false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusCompleted, nil, nil, false, nil, outputKey, nil, nil, nil, nil, nil, nil, now))

	metadata, err := NewCodeReviewStore(mock).GetByOutputKey(context.Background(), orgID, outputKey)

	require.NoError(t, err, "GetByOutputKey should load metadata by stable output key")
	require.Equal(t, metadataID, metadata.ID, "GetByOutputKey should return the matching metadata")
	require.Equal(t, orgID, metadata.OrgID, "GetByOutputKey should preserve org-scoped metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_GetLatestByPullRequestHeadFiltersByOrgAndPolicy(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	prID := uuid.New()
	policyID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 7, 16, 22, 55, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery("pull_request_id = @pull_request_id[\\s\\S]+head_sha = @head_sha[\\s\\S]+policy_id = @policy_id[\\s\\S]+ORDER BY created_at DESC").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
		}).AddRow(metadataID, orgID, sessionID, repoID, prID, policyID, "base", "head", false, models.CodeReviewTriggerSourceTeamReviewer, models.CodeReviewSessionStatusFailed, nil, nil, false, nil, "output", nil, nil, nil, nil, nil, &now, now))

	metadata, err := NewCodeReviewStore(mock).GetLatestByPullRequestHead(context.Background(), orgID, prID, "head", policyID)
	require.NoError(t, err, "latest review lookup should succeed")
	require.Equal(t, metadataID, metadata.ID, "latest review lookup should return the newest matching attempt")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_GetLatestByPullRequestFiltersByOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		load  func(context.Context, *CodeReviewStore, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error)
	}{
		{
			name:  "latest assessment",
			query: "WHERE org_id = @org_id[\\s\\S]+pull_request_id = @pull_request_id[\\s\\S]+ORDER BY created_at DESC",
			load: func(ctx context.Context, store *CodeReviewStore, orgID, prID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
				return store.GetLatestByPullRequest(ctx, orgID, prID)
			},
		},
		{
			name:  "latest submitted assessment",
			query: "WHERE org_id = @org_id[\\s\\S]+pull_request_id = @pull_request_id[\\s\\S]+github_review_id IS NOT NULL[\\s\\S]+ORDER BY created_at DESC",
			load: func(ctx context.Context, store *CodeReviewStore, orgID, prID uuid.UUID) (models.CodeReviewSessionMetadata, error) {
				return store.GetLatestSubmittedByPullRequest(ctx, orgID, prID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			sessionID := uuid.New()
			repoID := uuid.New()
			prID := uuid.New()
			policyID := uuid.New()
			metadataID := uuid.New()
			reviewID := int64(143)
			reviewURL := "https://github.com/acme/repo/pull/42#pullrequestreview-143"
			now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			expected := models.CodeReviewSessionMetadata{
				ID: metadataID, OrgID: orgID, SessionID: sessionID, RepositoryID: repoID,
				PullRequestID: prID, PolicyID: policyID, BaseSHA: "base", HeadSHA: "head",
				TriggerSource: models.CodeReviewTriggerSourceTeamReviewer, Status: models.CodeReviewSessionStatusCompleted,
				ReviewOutputKey: "output", GitHubReviewID: &reviewID, GitHubReviewURL: &reviewURL, CreatedAt: now,
			}

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			mock.ExpectQuery(tt.query).
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
					"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
					"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
				}).AddRow(metadataID, orgID, sessionID, repoID, prID, policyID, "base", "head", false,
					models.CodeReviewTriggerSourceTeamReviewer, models.CodeReviewSessionStatusCompleted, nil, nil, false,
					nil, "output", nil, &reviewID, &reviewURL, nil, nil, nil, now))

			actual, err := tt.load(context.Background(), NewCodeReviewStore(mock), orgID, prID)

			require.NoError(t, err, "pull request review history lookup should succeed")
			require.Equal(t, expected, actual, "pull request review history lookup should return exact org-scoped metadata")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCodeReviewStore_HasApprovedByPullRequestFiltersByOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		exists   bool
		expected bool
	}{
		{name: "has submitted approval", exists: true, expected: true},
		{name: "has no submitted approval", exists: false, expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			prID := uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			mock.ExpectQuery("SELECT EXISTS[\\s\\S]+org_id = @org_id[\\s\\S]+pull_request_id = @pull_request_id[\\s\\S]+decision = 'approved'[\\s\\S]+github_review_id IS NOT NULL").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(tt.exists))

			actual, err := NewCodeReviewStore(mock).HasApprovedByPullRequest(context.Background(), orgID, prID)

			require.NoError(t, err, "approval history lookup should succeed")
			require.Equal(t, tt.expected, actual, "approval history lookup should return the exact submitted approval state")
			require.NoError(t, mock.ExpectationsWereMet(), "approval history lookup should remain org scoped")
		})
	}
}

func TestCodeReviewStore_MarkStaleForPullRequestExceptHead(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	supersededBy := uuid.New()
	mock.ExpectExec("UPDATE code_review_session_metadata").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	count, err := NewCodeReviewStore(mock).MarkStaleForPullRequestExceptHead(context.Background(), orgID, prID, "head-new", &supersededBy)

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
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
		}).AddRow(metadataID, orgID, sessionID, repoID, prID, policyID, "base", "head", false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusCompleted, &decision, &acceptable, false, nil, "key", nil, &reviewID, &reviewURL, &body, nil, &now, now))

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
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
			"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at", "session_title", "repository_name", "github_repo",
			"github_pr_number", "github_pr_url", "pull_request_title", "pull_request_author",
		}).AddRow(
			metadataID, orgID, sessionID, repoID, prID, policyID,
			"base", "head", false, models.CodeReviewTriggerSourceAppReviewer, status, &decision, &acceptable, false,
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
	require.Equal(t, "devin", reviews[0].PullRequestAuthor, "ListReviews should return the GitHub pull request author")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_ListReviewsAppliesOutcomeFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		outcome         models.CodeReviewListOutcome
		expectedPattern string
	}{
		{
			name:            "automatically approved requires a completed posted approval",
			outcome:         models.CodeReviewListOutcomeAutomaticallyApproved,
			expectedPattern: `m\.status = 'completed'\s+AND m\.decision = 'approved'\s+AND m\.github_review_id IS NOT NULL`,
		},
		{
			name:            "completed not approved includes approval decisions that were not posted",
			outcome:         models.CodeReviewListOutcomeCompletedNotApproved,
			expectedPattern: `m\.status = 'completed'\s+AND \(m\.decision IS DISTINCT FROM 'approved'\s+OR m\.github_review_id IS NULL\)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize for each outcome filter")
			defer mock.Close()

			mock.ExpectQuery(tt.expectedPattern).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{
					"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
					"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
					"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
					"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at", "session_title",
					"repository_name", "github_repo", "github_pr_number", "github_pr_url", "pull_request_title", "pull_request_author",
				}))

			reviews, err := NewCodeReviewStore(mock).ListReviews(context.Background(), orgID, CodeReviewListFilters{
				Outcome: &tt.outcome,
			})

			require.NoError(t, err, "ListReviews should accept the selected outcome filter")
			require.Equal(t, []models.CodeReviewListItem{}, reviews, "ListReviews should return the mocked empty outcome result")
			require.NoError(t, mock.ExpectationsWereMet(), "the outcome filter should add the expected SQL conditions")
		})
	}
}

func TestCodeReviewStore_SavePolicyExpectingVersionConflict(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	config := models.DefaultCodeReviewPolicyConfig()
	config.ReviewInstructions = "agent-tuned guidance"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs("code_review_policy:" + orgID.String()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(3))
	mock.ExpectRollback()

	_, err = NewCodeReviewStore(mock).SavePolicyExpectingVersion(context.Background(), orgID, config, 2, nil)

	require.ErrorIs(t, err, ErrCodeReviewPolicyVersionConflict, "stale expected version should return the typed conflict error")
	require.Contains(t, err.Error(), "active version is 3", "conflict error should report the current version")
	require.NoError(t, mock.ExpectationsWereMet(), "conflict should abort before deactivating or inserting")
}

func TestCodeReviewStore_SavePolicyExpectingVersionIncrementsFromCurrent(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	policyID := uuid.New()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	config := models.DefaultCodeReviewPolicyConfig()
	config.ReviewInstructions = "agent-tuned guidance"
	descriptionPolicy, riskPolicy, agentRoster := mustCodeReviewPolicyJSON(t, config)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs("code_review_policy:" + orgID.String()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(3))
	mock.ExpectExec("UPDATE code_review_policies").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO code_review_policies").
		WithArgs(
			pgxmock.AnyArg(), 4, pgxmock.AnyArg(), pgxmock.AnyArg(),
			config.ReviewInstructions, config.AutomatedApprovalPolicy,
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode",
			"review_instructions", "automated_approval_policy",
			"description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "created_by_user_id", "created_at",
		}).AddRow(policyID, orgID, nil, true, 4, config.Enabled, config.ApprovalMode, config.ReviewInstructions, config.AutomatedApprovalPolicy, descriptionPolicy, riskPolicy, agentRoster, config.InlineCommentLimit, nil, now))
	mock.ExpectCommit()

	record, err := NewCodeReviewStore(mock).SavePolicyExpectingVersion(context.Background(), orgID, config, 3, nil)

	require.NoError(t, err, "matching expected version should save the next version")
	require.Equal(t, 4, record.Version, "the new version should be current+1")
	require.Nil(t, record.CreatedByUserID, "agent-authored versions have no created_by user")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_ListReviewsAppliesCursorAndTimeFilters(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	cursor := uuid.New()
	createdAfter := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	createdBefore := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	// Named args: org_id, limit, created_after, created_before, cursor.
	mock.ExpectQuery(`\(m\.created_at, m\.id\) < \(\s+SELECT created_at, id FROM code_review_session_metadata`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
			"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at", "session_title",
			"repository_name", "github_repo", "github_pr_number", "github_pr_url", "pull_request_title", "pull_request_author",
		}))

	reviews, err := NewCodeReviewStore(mock).ListReviews(context.Background(), orgID, CodeReviewListFilters{
		CreatedAfter:  &createdAfter,
		CreatedBefore: &createdBefore,
		Cursor:        &cursor,
	})

	require.NoError(t, err, "ListReviews should accept cursor and time filters")
	require.Empty(t, reviews, "ListReviews should return the mocked empty result")
	require.NoError(t, mock.ExpectationsWereMet(), "cursor pagination should add the keyset comparison")
}

func TestCodeReviewStore_GetListItemBySessionIDScopesByOrgAndSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	title := "Code review for acme/repo#42"
	repoName := "acme/repo"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`m\.session_id = @session_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
			"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable", "stale",
			"superseded_by_session_id", "review_output_key", "prompt_artifact_key", "github_review_id",
			"github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at", "session_title",
			"repository_name", "github_repo", "github_pr_number", "github_pr_url", "pull_request_title", "pull_request_author",
		}).AddRow(
			metadataID, orgID, sessionID, repoID, uuid.New(), uuid.New(),
			"base", "head", false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusRunning, nil, nil, false,
			nil, "key", nil, nil, nil, nil, nil, nil, now, &title, &repoName, "acme/repo",
			42, "https://github.com/acme/repo/pull/42", "Fix auth bug", "devin",
		))

	item, err := NewCodeReviewStore(mock).GetListItemBySessionID(context.Background(), orgID, sessionID)

	require.NoError(t, err, "GetListItemBySessionID should scan the joined review row")
	require.Equal(t, sessionID, item.SessionID, "GetListItemBySessionID should return the requested review")
	require.Equal(t, "Fix auth bug", item.PullRequestTitle, "GetListItemBySessionID should include pull request context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_ListFindingsRanksSeverityExplicitly(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	// severity is a text enum; the query must rank it via CASE rather than
	// sorting alphabetically (which would put medium above critical).
	mock.ExpectQuery(`CASE severity\s+WHEN 'critical' THEN 5\s+WHEN 'high' THEN 4\s+WHEN 'medium' THEN 3\s+WHEN 'low' THEN 2\s+WHEN 'info' THEN 1\s+ELSE 0\s+END DESC`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "agent_result_id", "dedupe_key", "severity",
			"confidence", "path", "start_line", "end_line", "summary", "body", "selected_for_inline", "github_comment_id", "created_at",
		}))

	findings, err := NewCodeReviewStore(mock).ListFindings(context.Background(), orgID, sessionID, false)

	require.NoError(t, err, "ListFindings should order by explicit severity rank")
	require.Empty(t, findings, "ListFindings should return the mocked empty result")
	require.NoError(t, mock.ExpectationsWereMet(), "the severity rank ORDER BY should be present")
}

func TestCodeReviewStore_MarkFindingsSelectedForInlineFiltersByOrgAndSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	findingIDs := []uuid.UUID{uuid.New(), uuid.New()}

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectExec("UPDATE code_review_findings").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	count, err := NewCodeReviewStore(mock).MarkFindingsSelectedForInline(context.Background(), orgID, sessionID, findingIDs)

	require.NoError(t, err, "MarkFindingsSelectedForInline should mark selected findings")
	require.Equal(t, int64(2), count, "MarkFindingsSelectedForInline should return affected row count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewStore_ReplaceFindingUpdatesConflictContent(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	resultID := uuid.New()
	findingID := uuid.New()
	path := "internal/worker/code_review_handler.go"
	startLine := 42
	endLine := 42
	now := time.Date(2026, 6, 30, 6, 30, 0, 0, time.UTC)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	mock.ExpectQuery(`(?s)ON CONFLICT \(org_id, session_id, dedupe_key\) DO UPDATE\s+SET\s+agent_result_id = EXCLUDED.agent_result_id.*summary = EXCLUDED.summary.*body = EXCLUDED.body.*github_comment_id = COALESCE`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "agent_result_id", "dedupe_key", "severity",
			"confidence", "path", "start_line", "end_line", "summary", "body",
			"selected_for_inline", "github_comment_id", "created_at",
		}).AddRow(findingID, orgID, sessionID, &resultID, "internal/worker/code_review_handler.go:42:42:missing coverage",
			models.CodeReviewFindingSeverityMedium, models.CodeReviewFindingConfidenceHigh,
			&path, &startLine, &endLine, "Missing coverage", "Use the orchestrator wording.", false, nil, now))

	finding := &models.CodeReviewFinding{
		OrgID:         orgID,
		SessionID:     sessionID,
		AgentResultID: &resultID,
		DedupeKey:     "internal/worker/code_review_handler.go:42:42:missing coverage",
		Severity:      models.CodeReviewFindingSeverityMedium,
		Confidence:    models.CodeReviewFindingConfidenceHigh,
		Path:          &path,
		StartLine:     &startLine,
		EndLine:       &endLine,
		Summary:       "Missing coverage",
		Body:          "Use the orchestrator wording.",
	}

	err = NewCodeReviewStore(mock).ReplaceFinding(context.Background(), finding)

	require.NoError(t, err, "ReplaceFinding should replace existing finding content on dedupe conflicts")
	require.Equal(t, findingID, finding.ID, "ReplaceFinding should scan the returned finding")
	require.Equal(t, "Use the orchestrator wording.", finding.Body, "ReplaceFinding should expose the replacement body")
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

func codeReviewGitHubTriggerColumns() []string {
	return []string{
		"id", "org_id", "repository_id", "installation_id", "active", "version",
		"team_slug", "team_name", "team_id", "repo_permission", "created_by_user_id", "created_at",
	}
}
