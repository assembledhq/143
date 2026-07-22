package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// TestFetchRepoMergeSettings verifies the helper decodes GitHub's repo
// response shape and forwards the auth header.
func TestFetchRepoMergeSettings(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/assembledhq/143", r.URL.Path)
		require.Equal(t, "token install-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"name":"143","allow_squash_merge":true,"allow_merge_commit":false,"allow_rebase_merge":false}`))
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	settings, err := service.fetchRepoMergeSettings(context.Background(), "install-token", "assembledhq", "143")
	require.NoError(t, err)
	require.NotNil(t, settings.AllowSquashMerge)
	require.True(t, *settings.AllowSquashMerge, "squash should be allowed")
	require.NotNil(t, settings.AllowMergeCommit)
	require.False(t, *settings.AllowMergeCommit, "merge commit should be disallowed")
	require.NotNil(t, settings.AllowRebaseMerge)
	require.False(t, *settings.AllowRebaseMerge, "rebase should be disallowed")
}

func TestFetchRepoMergeSettingsRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/assembledhq/143", r.URL.Path, "fetchRepoMergeSettings should call the repository endpoint")
		_, _ = w.Write([]byte(`{"allow_squash_merge":`))
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	_, err := service.fetchRepoMergeSettings(context.Background(), "install-token", "assembledhq", "143")
	require.Error(t, err, "fetchRepoMergeSettings should reject malformed GitHub JSON")
	require.Contains(t, err.Error(), "decode GitHub repo merge settings", "fetchRepoMergeSettings should wrap decode failures")
}

// TestFetchRepoMergeSettingsSurfacesHTTPError covers the early-return path
// where the GitHub /repos/{owner}/{repo} call fails before we ever try to
// decode the body.
func TestFetchRepoMergeSettingsSurfacesHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"Server Error"}`))
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	_, err := service.fetchRepoMergeSettings(context.Background(), "install-token", "assembledhq", "143")
	require.Error(t, err, "fetchRepoMergeSettings should surface upstream HTTP failures")
	var apiErr *GitHubAPIError
	require.True(t, errors.As(err, &apiErr), "fetchRepoMergeSettings should wrap upstream errors as *GitHubAPIError")
	require.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
}

func TestPRServiceEnqueueSlackSessionReactionUsesLifecycleEmoji(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()

	tests := []struct {
		name      string
		reaction  string
		dedupeKey string
	}{
		{
			name:      "session archived",
			reaction:  models.SlackReactionSessionArchived,
			dedupeKey: "slack_reaction:" + sessionID.String() + ":" + models.SlackReactionSessionArchived,
		},
		{
			name:      "pull request merged",
			reaction:  models.SlackReactionPRMerged,
			dedupeKey: "slack_reaction:" + sessionID.String() + ":" + models.SlackReactionPRMerged,
		},
		{
			name:      "pull request closed without merge",
			reaction:  models.SlackReactionPRClosed,
			dedupeKey: "slack_reaction:" + sessionID.String() + ":" + models.SlackReactionPRClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create jobs mock")
			defer mock.Close()

			mock.ExpectQuery("INSERT INTO jobs").
				WithArgs(pgx.NamedArgs{
					"org_id":     orgID,
					"queue":      "default",
					"job_type":   "slack_add_session_reaction",
					"payload":    pgxmock.AnyArg(),
					"priority":   3,
					"dedupe_key": &tt.dedupeKey,
				}).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

			service := &PRService{
				jobs:   db.NewJobStore(mock),
				logger: zerolog.New(io.Discard),
			}

			service.enqueueSlackSessionReaction(context.Background(), orgID, sessionID, tt.reaction)

			require.NoError(t, mock.ExpectationsWereMet(), "reaction enqueue should use the expected Slack lifecycle emoji")
		})
	}
}

// TestMergePullRequestOnGitHubSuccess covers the happy path: GitHub returns 200
// with merged=true and we forward the response intact.
func TestMergePullRequestOnGitHubSuccess(t *testing.T) {
	t.Parallel()

	var capturedBody gitHubMergeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "/repos/assembledhq/143/pulls/42/merge", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		_, _ = w.Write([]byte(`{"sha":"merge-sha","merged":true,"message":"Pull Request successfully merged"}`))
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	resp, err := service.mergePullRequestOnGitHub(context.Background(), "install-token", "assembledhq", "143", 42, gitHubMergeRequest{
		SHA:         "head-sha",
		MergeMethod: "squash",
	})
	require.NoError(t, err)
	require.True(t, resp.Merged)
	require.Equal(t, "merge-sha", resp.SHA)
	require.Equal(t, "head-sha", capturedBody.SHA, "head SHA should be sent so GitHub can guard against races")
	require.Equal(t, "squash", capturedBody.MergeMethod, "selected merge method should be sent")
}

// TestMergePullRequestOnGitHubHeadSHAMismatch verifies that GitHub's 409 for a
// head SHA mismatch surfaces as a *GitHubAPIError that callers can match on.
func TestMergePullRequestOnGitHubHeadSHAMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Head branch was modified. Review and try the merge again."}`))
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	_, err := service.mergePullRequestOnGitHub(context.Background(), "install-token", "assembledhq", "143", 42, gitHubMergeRequest{
		SHA: "stale-head",
	})
	require.Error(t, err)
	var apiErr *GitHubAPIError
	require.True(t, errors.As(err, &apiErr), "GitHub conflicts should be wrapped as GitHubAPIError")
	require.Equal(t, http.StatusConflict, apiErr.StatusCode)
	require.Contains(t, apiErr.Message(), "Head branch was modified")
}

func TestMergePullRequestOnGitHubRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/assembledhq/143/pulls/42/merge", r.URL.Path, "mergePullRequestOnGitHub should call the merge endpoint")
		_, _ = w.Write([]byte(`{"sha":`))
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	_, err := service.mergePullRequestOnGitHub(context.Background(), "install-token", "assembledhq", "143", 42, gitHubMergeRequest{})
	require.Error(t, err, "mergePullRequestOnGitHub should reject malformed GitHub JSON")
	require.Contains(t, err.Error(), "decode GitHub merge response", "mergePullRequestOnGitHub should wrap decode failures")
}

func TestValidateExpectedMergeHead(t *testing.T) {
	t.Parallel()

	expected := "queued-head"
	tests := []struct {
		name        string
		currentHead string
		expected    *string
		expectErr   bool
	}{
		{name: "allows matching expected head", currentHead: "queued-head", expected: &expected},
		{name: "allows direct merge without expected head", currentHead: "new-head", expected: nil},
		{name: "allows empty expected head", currentHead: "new-head", expected: ptrString("")},
		{name: "rejects changed head", currentHead: "new-head", expected: &expected, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateExpectedMergeHead(tt.currentHead, tt.expected)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrPullRequestHeadChanged, "validateExpectedMergeHead should reject a changed queued head")
				return
			}
			require.NoError(t, err, "validateExpectedMergeHead should allow safe head states")
		})
	}
}

// TestGitHubAPIErrorMessage covers the Message() helper, which the HTTP
// handler relies on to surface a useful message in the toast.
func TestGitHubAPIErrorMessage(t *testing.T) {
	t.Parallel()

	t.Run("parses standard GitHub error envelope", func(t *testing.T) {
		t.Parallel()
		err := &GitHubAPIError{
			StatusCode: http.StatusUnprocessableEntity,
			Body:       []byte(`{"message":"Validation Failed","errors":[]}`),
		}
		require.Equal(t, "Validation Failed", err.Message())
	})

	t.Run("falls back to raw body when not JSON", func(t *testing.T) {
		t.Parallel()
		err := &GitHubAPIError{
			StatusCode: http.StatusInternalServerError,
			Body:       []byte("upstream timeout"),
		}
		require.Equal(t, "upstream timeout", err.Message())
	})

	t.Run("falls back to status when body is empty", func(t *testing.T) {
		t.Parallel()
		err := &GitHubAPIError{StatusCode: http.StatusForbidden}
		require.Equal(t, "GitHub API returned 403", err.Message())
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		t.Parallel()
		var err *GitHubAPIError
		require.Equal(t, "", err.Message())
	})
}

// guardrails: ensure timeouts are respected so slow GitHub responses do not
// hang the merge mutation indefinitely. The httpClient injected into PRService
// already enforces 30s in production; this test confirms a configurable
// deadline propagates through.
func TestMergePullRequestOnGitHubRespectsContextTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			_, _ = w.Write([]byte(`{"sha":"x","merged":true,"message":"ok"}`))
		}
	}))
	defer server.Close()

	service := &PRService{
		logger:     zerolog.New(io.Discard),
		baseURL:    server.URL,
		httpClient: server.Client(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := service.mergePullRequestOnGitHub(ctx, "install-token", "assembledhq", "143", 42, gitHubMergeRequest{})
	require.Error(t, err, "merge should propagate context deadline")
}

func TestPRServiceMergePullRequestRunsMergedFollowUps(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	summaryJSON := json.RawMessage(`{"merge_state":"clean","has_conflicts":false,"failing_test_count":0,"needs_agent_action":false,"checks":[{"name":"unit tests","category":"test","status":"passed"}]}`)
	headSHA := "head-merge"
	baseSHA := "base-merge"
	snapshotKey := "snap-merge"
	mergeCommitSHA := "merge-commit-sha"

	prMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pull request mock")
	defer prMock.Close()

	repoMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create repository mock")
	defer repoMock.Close()

	sessionMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create session mock")
	defer sessionMock.Close()

	issueMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create issue mock")
	defer issueMock.Close()

	deployMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create deploy mock")
	defer deployMock.Close()

	jobMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create job mock")
	defer jobMock.Close()

	snapshotStore := &prTestSnapshotStore{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/assembledhq/143/pulls/42":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/assembledhq/143/pull/42","state":"open","mergeable":true,"mergeable_state":"clean","head":{"ref":"feature","sha":"head-merge"},"base":{"ref":"main","sha":"base-merge"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/assembledhq/143/commits/head-merge/check-runs":
			_, _ = w.Write([]byte(`{"check_runs":[{"id":7,"name":"unit tests","conclusion":"success","status":"completed","app":{"slug":"github-actions"},"output":{}}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/assembledhq/143":
			_, _ = w.Write([]byte(`{"allow_squash_merge":true,"allow_merge_commit":true,"allow_rebase_merge":false}`))
		case r.Method == http.MethodPut && r.URL.Path == "/repos/assembledhq/143/pulls/42/merge":
			var req gitHubMergeRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req), "merge request should decode")
			require.Equal(t, "head-merge", req.SHA, "merge should send the latest head SHA")
			require.Equal(t, "squash", req.MergeMethod, "merge should prefer squash when allowed")
			_, _ = w.Write([]byte(`{"sha":"merge-commit-sha","merged":true,"message":"Pull Request successfully merged"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	prStore := db.NewPullRequestStore(prMock)
	repoStore := db.NewRepositoryStore(repoMock)
	sessionStore := db.NewSessionStore(sessionMock)
	issueStore := db.NewIssueStore(issueMock)
	deployStore := db.NewDeployStore(deployMock)
	jobStore := db.NewJobStore(jobMock)

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			prID, &sessionID, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			prID, &sessionID, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
			models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))

	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	expectReserveCheckStateVersion(prMock, orgID, prID, 0)
	prMock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))

	prMock.ExpectBegin()
	prMock.ExpectExec("SELECT id[\\s\\S]+FROM pull_requests[\\s\\S]+FOR UPDATE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	prMock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	prMock.ExpectExec("INSERT INTO pull_request_health_snapshots").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":   prID,
			"org_id":            orgID,
			"version":           int64(1),
			"head_sha":          "head-merge",
			"base_sha":          "base-merge",
			"summary_json":      pgxmock.AnyArg(),
			"enrichment_status": models.PullRequestHealthEnrichmentStatusNotRequested,
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	prMock.ExpectExec("UPDATE pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID, "version": int64(1), "head_sha": "head-merge"}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	prMock.ExpectExec("INSERT INTO pull_request_health_current").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":      prID,
			"org_id":               orgID,
			"version":              int64(1),
			"head_sha":             "head-merge",
			"base_sha":             "base-merge",
			"summary_json":         pgxmock.AnyArg(),
			"summary_preview_json": pgxmock.AnyArg(),
			"enrichment_status":    models.PullRequestHealthEnrichmentStatusNotRequested,
			"enriched_at":          (*time.Time)(nil),
			"check_state_version":  int64(0),
			"created_at":           pgxmock.AnyArg(),
			"updated_at":           pgxmock.AnyArg(),
		}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	prMock.ExpectExec("UPDATE pull_requests").
		WithArgs(pgx.NamedArgs{
			"pull_request_id":    prID,
			"org_id":             orgID,
			"head_sha":           "head-merge",
			"base_sha":           "base-merge",
			"merge_state":        models.PullRequestMergeStateClean,
			"has_conflicts":      false,
			"failing_test_count": 0,
			"needs_agent_action": false,
			"version":            int64(1),
			"mark_github_synced": true,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	prMock.ExpectCommit()
	prMock.ExpectExec("UPDATE pull_requests SET ci_status").
		WithArgs(pgx.NamedArgs{"id": prID, "org_id": orgID, "ci_status": models.PullRequestCIStatusSuccess}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			prID, &sessionID, orgID, 42, "https://github.com/assembledhq/143/pull/42", "assembledhq/143",
			"Fix bug", (*string)(nil), "open", "pending", "app", "success", &headSHA, nil, &baseSHA,
			models.PullRequestMergeStateClean, false, 0, false, &now, int64(1), models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil), (*time.Time)(nil), now, now,
		))
	prMock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			prID, orgID, int64(1), "head-merge", "base-merge", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, (*time.Time)(nil), int64(0), now, now,
		))
	prMock.ExpectQuery("SELECT .+ FROM pull_request_repair_runs").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID, "head_sha": "head-merge"}).
		WillReturnRows(pgxmock.NewRows(prRepairRunTestColumns))
	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(1), "assembledhq/143", "main", false, nil, nil, "https://github.com/assembledhq/143.git", int64(123), "active", nil, nil, []byte(`{}`), now, now,
		))
	prMock.ExpectExec("UPDATE pull_requests SET status = @status, merged_at = now\\(\\), updated_at = now\\(\\) WHERE id = @id AND org_id = @org_id").
		WithArgs(pgx.NamedArgs{"id": prID, "org_id": orgID, "status": models.PullRequestStatusMerged}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	sessionRow := newPRHealthSessionRow(sessionID, orgID, now, models.SessionStatusCompleted)
	setPRHealthSessionRowValue(sessionRow, "primary_issue_id", &issueID)
	setPRHealthSessionRowValue(sessionRow, "agent_type", "codex")
	setPRHealthSessionRowValue(sessionRow, "autonomy_level", "full")
	setPRHealthSessionRowValue(sessionRow, "sandbox_state", "snapshot")
	setPRHealthSessionRowValue(sessionRow, "snapshot_key", &snapshotKey)

	sessionMock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prHealthSessionColumns).AddRow(sessionRow...),
		)
	issueMock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgx.NamedArgs{"id": issueID, "org_id": orgID, "status": models.IssueStatusFixed}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	sessionMock.ExpectExec("UPDATE sessions\n\t\tSET snapshot_key = NULL, sandbox_state = 'destroyed'").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	deployMock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
		WithArgs(pgx.NamedArgs{"pull_request_id": prID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}))
	deployMock.ExpectQuery("INSERT INTO deploys").
		WithArgs(pgx.NamedArgs{
			"pull_request_id": prID,
			"org_id":          orgID,
			"environment":     "production",
			"commit_sha":      &mergeCommitSHA,
		}).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}).
				AddRow(uuid.New(), now, now),
		)

	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	service := &PRService{
		tokenProvider: &Service{cache: map[int64]*cachedToken{
			123: {Token: "install-token", ExpiresAt: time.Now().Add(time.Hour)},
		}},
		pullRequests: prStore,
		repos:        repoStore,
		sessions:     sessionStore,
		issues:       issueStore,
		deploys:      deployStore,
		jobs:         jobStore,
		snapshots:    snapshotStore,
		logger:       zerolog.New(io.Discard),
		baseURL:      server.URL,
		httpClient:   server.Client(),
	}

	resp, err := service.MergePullRequest(context.Background(), orgID, prID, uuid.New())
	require.NoError(t, err, "MergePullRequest should succeed")
	require.True(t, resp.Merged, "MergePullRequest should report merged=true")
	require.Equal(t, models.PullRequestMergeMethodSquash, resp.MergeMethod, "MergePullRequest should report the selected merge method")
	require.Equal(t, []string{"snap-merge"}, snapshotStore.deleted, "MergePullRequest should clean up the session snapshot")
	require.NoError(t, prMock.ExpectationsWereMet(), "pull request expectations should be met")
	require.NoError(t, repoMock.ExpectationsWereMet(), "repository expectations should be met")
	require.NoError(t, sessionMock.ExpectationsWereMet(), "session expectations should be met")
	require.NoError(t, issueMock.ExpectationsWereMet(), "issue expectations should be met")
	require.NoError(t, deployMock.ExpectationsWereMet(), "deploy expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "job expectations should be met")
}

// TestPRServiceMergePullRequestErrorsWhenInitialLoadFails covers the very
// first failure mode in MergePullRequest: the initial pullRequests.GetByID
// returning an error (DB outage, deleted PR, etc.) wrapped with the
// "load pull request" prefix so callers can attribute the failure.
func TestPRServiceMergePullRequestErrorsWhenInitialLoadFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()

	prMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pull request mock")
	defer prMock.Close()

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("pr load failed"))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		logger:       zerolog.New(io.Discard),
	}

	_, err = service.MergePullRequest(context.Background(), orgID, prID, uuid.New())
	require.Error(t, err, "MergePullRequest should surface initial load failures")
	require.Contains(t, err.Error(), "load pull request", "MergePullRequest should wrap initial load errors with the load prefix")
	require.Contains(t, err.Error(), "pr load failed", "MergePullRequest should preserve the underlying load error")
	require.NoError(t, prMock.ExpectationsWereMet(), "pull request expectations should be met")
}

func TestPRServiceMergePullRequestRejectsNonOpenPullRequests(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()

	prMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pull request mock")
	defer prMock.Close()

	row := newPRTestRow(prID, nil, orgID, "assembledhq/143", now, nil)
	row[8] = "closed"

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		logger:       zerolog.New(io.Discard),
	}

	_, err = service.MergePullRequest(context.Background(), orgID, prID, uuid.New())
	require.ErrorIs(t, err, ErrPullRequestNotMergeable, "MergePullRequest should reject pull requests that are not open")
	require.Contains(t, err.Error(), `pull request status is "closed"`, "MergePullRequest should explain why the pull request is not mergeable")
	require.NoError(t, prMock.ExpectationsWereMet(), "pull request expectations should be met")
}

func TestPRServiceMergePullRequestReturnsRefreshFailureWhenSyncFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()

	prMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pull request mock")
	defer prMock.Close()

	repoMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create repository mock")
	defer repoMock.Close()

	openRow := newPRTestRow(prID, nil, orgID, "assembledhq/143", now, nil)
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(openRow...))
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(openRow...))

	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnError(errors.New("repo lookup failed"))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		repos:        db.NewRepositoryStore(repoMock),
		logger:       zerolog.New(io.Discard),
	}

	_, err = service.MergePullRequest(context.Background(), orgID, prID, uuid.New())
	require.ErrorIs(t, err, ErrMergeStateRefreshFailed, "MergePullRequest should surface sync failures as ErrMergeStateRefreshFailed")
	require.Contains(t, err.Error(), "repo lookup failed", "MergePullRequest should preserve the underlying sync failure")
	require.NoError(t, prMock.ExpectationsWereMet(), "pull request expectations should be met")
	require.NoError(t, repoMock.ExpectationsWereMet(), "repository expectations should be met")
}

func TestMaybeAutoArchiveSessionOnPRCloseEmitsArchiveAuditOnce(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()

	sessionMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create session mock")
	defer sessionMock.Close()

	orgMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create organization mock")
	defer orgMock.Close()

	auditMock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create audit mock")
	defer auditMock.Close()

	sessionStore := db.NewSessionStore(sessionMock)
	orgStore := db.NewOrganizationStore(orgMock)
	auditEmitter := db.NewAuditEmitter(db.NewAuditLogStore(auditMock), zerolog.Nop())

	service := &PRService{
		sessions: sessionStore,
		orgs:     orgStore,
		audit:    auditEmitter,
		logger:   zerolog.New(io.Discard),
	}

	pr := models.PullRequest{
		ID:             prID,
		SessionID:      &sessionID,
		OrgID:          orgID,
		GitHubPRNumber: 42,
		GitHubRepo:     "assembledhq/143",
	}

	orgMock.ExpectQuery("SELECT .+ FROM organizations WHERE id = @id").
		WithArgs(pgx.NamedArgs{"id": orgID}).
		WillReturnRows(
			pgxmock.NewRows(prTestOrganizationColumns).
				AddRow(orgID, "Acme", []byte(`{"auto_archive_on_pr_close":true}`), now, now),
		)
	sessionMock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	auditMock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

	orgMock.ExpectQuery("SELECT .+ FROM organizations WHERE id = @id").
		WithArgs(pgx.NamedArgs{"id": orgID}).
		WillReturnRows(
			pgxmock.NewRows(prTestOrganizationColumns).
				AddRow(orgID, "Acme", []byte(`{"auto_archive_on_pr_close":true}`), now, now),
		)
	sessionMock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	service.maybeAutoArchiveSessionOnPRClose(context.Background(), pr, nil, true)
	service.maybeAutoArchiveSessionOnPRClose(context.Background(), pr, nil, true)

	require.NoError(t, sessionMock.ExpectationsWereMet(), "session archive expectations should be met")
	require.NoError(t, orgMock.ExpectationsWereMet(), "organization expectations should be met")
	require.NoError(t, auditMock.ExpectationsWereMet(), "auto-archive should emit one audit row even if invoked twice")
}
