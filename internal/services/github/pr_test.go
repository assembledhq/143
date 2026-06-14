package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/assembledhq/143/internal/services/storage"
)

// mockLLMClient implements llm.Client for testing.
type mockLLMClient struct {
	response         string
	err              error
	lastSystemPrompt string
	lastUserPrompt   string
}

func (m *mockLLMClient) Complete(_ context.Context, systemPrompt, userPrompt string) (string, error) {
	m.lastSystemPrompt = systemPrompt
	m.lastUserPrompt = userPrompt
	return m.response, m.err
}

type fakeSessionThreadLister struct {
	threads []models.SessionThread
	err     error
}

func (f fakeSessionThreadLister) ListBySessionWithOptions(context.Context, uuid.UUID, uuid.UUID, bool) ([]models.SessionThread, error) {
	return f.threads, f.err
}

var prTestIssueColumns = []string{
	"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
	"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
	"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
	"created_at", "updated_at", "deleted_at",
}

var prTestRepoColumns = []string{
	"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
	"private", "language", "description", "clone_url", "installation_id", "status",
	"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
}

var prTestOrganizationColumns = []string{
	"id", "name", "settings", "created_at", "updated_at",
}

var prTestUserColumns = []string{
	"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at",
}

var prTestPullRequestColumns = []string{
	"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
	"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
	"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
	"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
	"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
	"merge_when_ready_updated_at", "merged_at", "created_at", "updated_at",
}

var prTestPreviewTargetColumns = []string{
	"id", "org_id", "repository_id", "branch", "commit_sha",
	"preview_config_name", "resolved_config_digest", "source_type", "source_id", "source_url",
	"created_by_user_id", "request_id", "created_at",
}

var prTestPreviewLinkColumns = []string{
	"id", "org_id", "preview_target_id", "link_type", "slug",
	"repository_id", "pr_number", "created_at", "updated_at",
}

func ptrInt(v int) *int {
	return &v
}

func newPRTestRow(prID uuid.UUID, sessionID *uuid.UUID, orgID uuid.UUID, repo string, now time.Time, body *string) []any {
	return newPRTestRowWithTitle(prID, sessionID, orgID, repo, now, body, "Fix bug")
}

func newPRTestRowWithTitle(prID uuid.UUID, sessionID *uuid.UUID, orgID uuid.UUID, repo string, now time.Time, body *string, title string) []any {
	return []any{
		prID,
		sessionID,
		orgID,
		42,
		"https://github.com/" + repo + "/pull/42",
		repo,
		title,
		body,
		"open",
		"pending",
		"app",
		"",
		nil,
		nil,
		nil,
		models.PullRequestMergeStateUnknown,
		false,
		0,
		false,
		nil,
		int64(0),
		models.PullRequestMergeWhenReadyStateOff,
		(*uuid.UUID)(nil),
		(*time.Time)(nil),
		"",
		(*int64)(nil),
		"",
		(*time.Time)(nil),
		(*time.Time)(nil),
		now,
		now,
	}
}

type prTestSnapshotStore struct {
	payload   []byte
	loadErr   error
	deleted   []string
	deleteErr error
}

func (s *prTestSnapshotStore) Save(context.Context, string, io.Reader) error { return nil }
func (s *prTestSnapshotStore) Load(_ context.Context, _ string, w io.Writer) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	_, err := w.Write(s.payload)
	return err
}
func (s *prTestSnapshotStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return s.deleteErr
}

type prTestSandboxProvider struct {
	lastConfig    agent.SandboxConfig
	lastExecCmd   string
	writes        map[string][]byte
	execExit      int
	execErr       error
	execStderr    string
	execStdout    string
	execSequence  []prTestExecResponse
	execCallCount int
	createErr     error
	restoreErr    error
	writeErrs     map[string]error
	destroyErr    error
	destroyed     int
	snapshotErr   error
}

// prTestExecResponse drives a multi-call Exec sequence: each provider Exec()
// pops the next response (capped at the slice length, after which the static
// execExit/execStdout/execStderr/execErr fields take over). Lets tests assert
// behavior that depends on the order of two or more sandbox executions —
// e.g. "first push attempt is rejected, second succeeds".
type prTestExecResponse struct {
	exit   int
	stdout string
	stderr string
	err    error
}

func (p *prTestSandboxProvider) Name() string { return "test" }

func (p *prTestSandboxProvider) Create(_ context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	if p.createErr != nil {
		return nil, p.createErr
	}
	p.lastConfig = cfg
	return &agent.Sandbox{ID: "sandbox-1", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
}

func (p *prTestSandboxProvider) CloneRepo(context.Context, *agent.Sandbox, string, string, string) error {
	return nil
}

func (p *prTestSandboxProvider) Exec(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	p.lastExecCmd = cmd
	idx := p.execCallCount
	p.execCallCount++
	if idx < len(p.execSequence) {
		r := p.execSequence[idx]
		if r.stdout != "" {
			_, _ = io.WriteString(stdout, r.stdout)
		}
		if r.stderr != "" {
			_, _ = io.WriteString(stderr, r.stderr)
		}
		return r.exit, r.err
	}
	if p.execStdout != "" {
		_, _ = io.WriteString(stdout, p.execStdout)
	}
	if p.execStderr != "" {
		_, _ = io.WriteString(stderr, p.execStderr)
	}
	return p.execExit, p.execErr
}

func (p *prTestSandboxProvider) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	return nil, nil
}

func (p *prTestSandboxProvider) WriteFile(_ context.Context, _ *agent.Sandbox, path string, data []byte) error {
	if err := p.writeErrs[path]; err != nil {
		return err
	}
	if p.writes == nil {
		p.writes = make(map[string][]byte)
	}
	p.writes[path] = append([]byte(nil), data...)
	return nil
}

type stubPRAppUserAuth struct {
	getValidCredentialFunc func(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error)
}

func (s *stubPRAppUserAuth) GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error) {
	return s.getValidCredentialFunc(ctx, orgID, userID)
}

func (p *prTestSandboxProvider) Destroy(context.Context, *agent.Sandbox) error {
	p.destroyed++
	return p.destroyErr
}

func (p *prTestSandboxProvider) IsAlive(context.Context, *agent.Sandbox) (bool, error) {
	return true, nil
}

func (p *prTestSandboxProvider) ConnectionInfo(context.Context, *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return nil, nil
}

func (p *prTestSandboxProvider) Snapshot(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
	if p.snapshotErr != nil {
		return nil, p.snapshotErr
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (p *prTestSandboxProvider) Restore(_ context.Context, _ *agent.Sandbox, reader io.Reader) error {
	_, _ = io.Copy(io.Discard, reader)
	return p.restoreErr
}

func (p *prTestSandboxProvider) ExecStream(context.Context, *agent.Sandbox, string, func([]byte), io.Writer) (int, error) {
	return 0, nil
}

func TestFormatPRTitle(t *testing.T) {
	t.Parallel()

	sessionTitle := "Refactor auth middleware"
	summaryText := "Updated the login flow\nwith multiple lines"

	tests := []struct {
		name    string
		session models.Session
		issue   *models.Issue
		expect  string
	}{
		{
			name:    "linear source uses bracket key prefix",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source:     models.IssueSourceLinear,
				ExternalID: "ENG-1234",
				Title:      "Fix null pointer in user API",
			},
			expect: "[ENG-1234] Fix null pointer in user API",
		},
		{
			name:    "sentry source uses fix prefix",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source: models.IssueSourceSentry,
				Title:  "TypeError in payment handler",
			},
			expect: "fix: TypeError in payment handler",
		},
		{
			name:    "support source uses fix prefix",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source: models.IssueSource("support"),
				Title:  "Login button not working",
			},
			expect: "fix: Login button not working",
		},
		{
			name:    "unknown source uses fix prefix",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source: models.IssueSource("other"),
				Title:  "Some issue",
			},
			expect: "fix: Some issue",
		},
		{
			name:    "nil issue uses session title",
			session: models.Session{ID: uuid.New(), Title: &sessionTitle},
			issue:   nil,
			expect:  "Refactor auth middleware",
		},
		{
			name:    "nil issue falls back to result summary first line",
			session: models.Session{ID: uuid.New(), ResultSummary: &summaryText},
			issue:   nil,
			expect:  "Updated the login flow",
		},
		{
			name: "non linear issue uses result summary when available",
			session: models.Session{
				ID:            uuid.New(),
				ResultSummary: func() *string { s := "Aligned file ordering between file detail view and Changes sidebar."; return &s }(),
			},
			issue: &models.Issue{
				Source: models.IssueSource("support"),
				Title:  "please make sure the ordering of the files in the file detail view and the files in the \"Changes\" section of the side menu match",
			},
			expect: "fix: Aligned file ordering between file detail view and Changes sidebar",
		},
		{
			name: "issueless session prefers canonical session title over result summary",
			session: models.Session{
				ID: uuid.New(),
				Title: func() *string {
					s := "please make sure the ordering of the files in the file detail view and the files in the \"Changes\" section of the side menu match"
					return &s
				}(),
				ResultSummary: func() *string { s := "Aligned file ordering between file detail view and Changes sidebar."; return &s }(),
			},
			issue:  nil,
			expect: "please make sure the ordering of the files in the file detail view and the files in the \"Changes\" section of the side",
		},
		{
			name: "issueless session uses minimally sanitized title when no summary exists",
			session: models.Session{
				ID: uuid.New(),
				Title: func() *string {
					s := "  \"Keep file ordering consistent between detail view and Changes sidebar\"  "
					return &s
				}(),
			},
			issue:  nil,
			expect: "Keep file ordering consistent between detail view and Changes sidebar",
		},
		{
			name: "non linear issue preserves existing fix prefix",
			session: models.Session{
				ID:            uuid.New(),
				ResultSummary: func() *string { s := "fix: Keep file ordering consistent"; return &s }(),
			},
			issue: &models.Issue{
				Source: models.IssueSource("support"),
				Title:  "Ordering mismatch",
			},
			expect: "fix: Keep file ordering consistent",
		},
		{
			name: "issueless session trims quotes and whitespace from title",
			session: models.Session{
				ID:    uuid.New(),
				Title: func() *string { s := "  \"Refactor auth middleware\"  "; return &s }(),
			},
			issue:  nil,
			expect: "Refactor auth middleware",
		},
		{
			name:    "nil issue with no title or summary uses session ID",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue:   nil,
			expect:  "Session abcdef01",
		},
		{
			name:    "non linear issue with empty derived title falls back to session id",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue: &models.Issue{
				Source: models.IssueSource("support"),
				Title:  "   ",
			},
			expect: "fix: Session abcdef01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatPRTitle(&tt.session, tt.issue)
			require.Equal(t, tt.expect, result, "PR title should match expected format")
		})
	}
}

func TestPRService_SettersAndCheckRunHandler(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	service := NewPRService(nil, db.NewPullRequestStore(mock), nil, nil, nil, nil, nil, zerolog.Nop())
	sessionMessages := db.NewSessionMessageStore(mock)
	service.SetSessionMessageStore(sessionMessages)
	require.Same(t, sessionMessages, service.sessionMessages, "SetSessionMessageStore should store the session message dependency")

	streams := &cache.PullRequestStreams{}
	service.SetPullRequestStreams(streams)
	require.Same(t, streams, service.prHealthStreams, "SetPullRequestStreams should store the stream dependency")

	repoName := "assembledhq/143"
	prID := uuid.New()
	orgID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(
			newPRTestRow(prID, nil, orgID, repoName, time.Now(), nil)...,
		))
	err = service.HandleCheckRunEvent(context.Background(), CheckRunEvent{
		Action: "completed",
		CheckRun: struct {
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		}{
			PullRequests: []struct {
				Number int `json:"number"`
			}{{Number: 42}},
		},
		Repository: struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		}{
			FullName: repoName,
		},
	})
	require.NoError(t, err, "HandleCheckRunEvent should enqueue state sync for completed check runs")

	err = service.HandleCheckRunEvent(context.Background(), CheckRunEvent{Action: "created"})
	require.NoError(t, err, "HandleCheckRunEvent should ignore non-completed actions")

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
	err = service.HandleCheckRunEvent(context.Background(), CheckRunEvent{
		Action: "completed",
		CheckRun: struct {
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		}{
			PullRequests: []struct {
				Number int `json:"number"`
			}{{Number: 99}},
		},
		Repository: struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		}{
			FullName: repoName,
		},
	})
	require.NoError(t, err, "HandleCheckRunEvent should ignore check runs for unmanaged pull requests")
	require.NoError(t, mock.ExpectationsWereMet(), "all check_run expectations should be met")
}

// TestPRService_IdentityResolverCachedAndInvalidated verifies the lazy-build
// + invalidate-on-Set* contract: the resolver is built once on first use
// and reused on subsequent calls, but a Set* mutator that changes a
// resolver-relevant dependency (e.g. SetUserStore) must invalidate the
// cache so the next call rebuilds.
func TestPRService_IdentityResolverCachedAndInvalidated(t *testing.T) {
	t.Parallel()

	service := NewPRService(nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())

	first := service.identityResolver()
	require.NotNil(t, first, "identityResolver should build on first use")
	require.Same(t, first, service.identityResolver(), "identityResolver should reuse the cached resolver across hot-path calls")

	// SetUserStore changes a resolver dependency; the cache must rebuild
	// on the next call so the new wiring takes effect.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should construct a pgxmock pool")
	defer mock.Close()
	service.SetUserStore(db.NewUserStore(mock))
	rebuilt := service.identityResolver()
	require.NotSame(t, first, rebuilt, "SetUserStore should invalidate the cached resolver")
	require.Same(t, rebuilt, service.identityResolver(), "post-rebuild calls should hit the new cache entry")

	// SetBaseURL is the URL override seam used in tests; it must also
	// invalidate so a test override doesn't leak the production base URL.
	service.SetBaseURL("https://api.example.com")
	require.NotSame(t, rebuilt, service.identityResolver(), "SetBaseURL should invalidate the cached resolver")
}

func TestFormatBranchName(t *testing.T) {
	t.Parallel()

	issueTitle := "Fix null pointer"
	sessionTitle := "Refactor auth"
	longTitle := "This is a very long issue title that should be truncated at some reasonable point to avoid creating overly long branch names"

	tests := []struct {
		name    string
		session models.Session
		issue   *models.Issue
		expect  string
		maxLen  bool // if true, verify length constraints
	}{
		{
			name:    "issue-based branch name",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue:   &models.Issue{Title: "Fix null pointer"},
			expect:  "143/abcdef01/fix-null-pointer",
		},
		{
			name:    "special characters are slugified",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue:   &models.Issue{Title: "Fix: TypeError in payment_handler (v2)"},
			expect:  "143/abcdef01/fix-typeerror-in-payment-handler-v2",
		},
		{
			name:    "long title is truncated",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue:   &models.Issue{Title: longTitle},
			maxLen:  true,
		},
		{
			name:    "nil issue uses session title",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"), Title: &sessionTitle},
			issue:   nil,
			expect:  "143/abcdef01/refactor-auth",
		},
		{
			name:    "nil issue with nil title uses session title from issue",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"), Title: &issueTitle},
			issue:   nil,
			expect:  "143/abcdef01/fix-null-pointer",
		},
		{
			name:    "nil issue with no title falls back to changes",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue:   nil,
			expect:  "143/abcdef01/changes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatBranchName(&tt.session, tt.issue)
			if tt.maxLen {
				// The slug portion (after "143/{8chars}/") shouldn't exceed maxBranchSlugLen
				parts := strings.SplitN(result, "/", 3)
				require.Len(t, parts, 3, "branch name should have 3 path segments")
				require.LessOrEqual(t, len(parts[2]), maxBranchSlugLen, "slug portion should not exceed max branch slug length")
			} else {
				require.Equal(t, tt.expect, result, "branch name should match expected format")
			}
			// Branch name should never contain spaces.
			require.NotContains(t, result, " ", "branch name should not contain spaces")
		})
	}
}

func TestFormatCommitMessage(t *testing.T) {
	t.Parallel()

	sessionTitle := "Refactor auth middleware"

	tests := []struct {
		name    string
		session models.Session
		issue   *models.Issue
		expect  string
	}{
		{
			name:    "linear issue includes Fixes reference",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source:     models.IssueSourceLinear,
				ExternalID: "ENG-1234",
				Title:      "Fix null pointer",
			},
			expect: "fix: Fix null pointer\n\nFixes #ENG-1234",
		},
		{
			name:    "sentry issue includes Resolves reference",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source:     models.IssueSourceSentry,
				ExternalID: "SENTRY-5678",
				Title:      "TypeError in handler",
			},
			expect: "fix: TypeError in handler\n\nResolves SENTRY-5678",
		},
		{
			name:    "support issue has no reference",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source: models.IssueSource("support"),
				Title:  "Login broken",
			},
			expect: "fix: Login broken",
		},
		{
			name:    "nil issue uses session title",
			session: models.Session{ID: uuid.New(), Title: &sessionTitle},
			issue:   nil,
			expect:  "Refactor auth middleware",
		},
		{
			name:    "nil issue with no title uses session ID",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue:   nil,
			expect:  "Session abcdef01",
		},
		{
			name:    "nil issue falls back to result summary first line",
			session: models.Session{ID: uuid.New(), ResultSummary: func() *string { s := "Updated the login flow\n\nAdded coverage"; return &s }()},
			issue:   nil,
			expect:  "Updated the login flow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatCommitMessage(&tt.session, tt.issue)
			require.Equal(t, tt.expect, result, "commit message should match expected format")
		})
	}
}

func TestBuildLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issue  *models.Issue
		expect []string
	}{
		{
			name: "all labels",
			issue: &models.Issue{
				Severity: "high",
				Source:   models.IssueSourceSentry,
			},
			expect: []string{"143-generated", "severity:high", "source:sentry"},
		},
		{
			name: "no severity",
			issue: &models.Issue{
				Source: models.IssueSourceLinear,
			},
			expect: []string{"143-generated", "source:linear"},
		},
		{
			name:   "minimal issue",
			issue:  &models.Issue{},
			expect: []string{"143-generated"},
		},
		{
			name:   "nil issue returns only base label",
			issue:  nil,
			expect: []string{"143-generated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := buildLabels(tt.issue)
			require.Equal(t, tt.expect, result, "labels should match expected set")
		})
	}
}

func TestFormatPRBody(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := &PRService{logger: logger}

	summary := "Fixed the null pointer dereference"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}
	issue := &models.Issue{
		Source:                models.IssueSourceSentry,
		Severity:              "high",
		AffectedCustomerCount: 42,
		OccurrenceCount:       100,
		Title:                 "Null pointer in user API",
	}

	body := svc.formatPRBody(context.Background(), run, issue)

	require.Contains(t, body, "## Summary", "PR body should contain Summary heading")
	require.Contains(t, body, summary, "PR body should contain the result summary text")
	require.Contains(t, body, "sentry", "PR body should contain the issue source")
	require.Contains(t, body, "high", "PR body should contain the severity level")
	require.Contains(t, body, "## Test plan", "PR body should contain Test plan heading")
	require.Contains(t, body, "143.dev", "PR body should contain the 143.dev branding")
}

func TestFormatPRBody_NilIssue(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := &PRService{logger: logger}

	summary := "Refactored the auth middleware for clarity"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}

	body := svc.formatPRBody(context.Background(), run, nil)

	require.Contains(t, body, "## Summary", "PR body should contain Summary heading")
	require.Contains(t, body, summary, "PR body should contain the result summary text")
	require.NotContains(t, body, "**Issue**", "PR body should not contain Issue section when issue is nil")
	require.Contains(t, body, "## Test plan", "PR body should contain Test plan heading")
	require.Contains(t, body, "143.dev", "PR body should contain the 143.dev branding")
}

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input  string
		expect string
	}{
		{"Fix null pointer", "fix-null-pointer"},
		{"Fix: TypeError (v2)", "fix-typeerror-v2"},
		{"UPPERCASE TITLE", "uppercase-title"},
		{"  spaces  around  ", "spaces-around"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expect, slugify(tt.input), "slugify should produce expected slug")
		})
	}
}

func TestSplitRepo(t *testing.T) {
	t.Parallel()

	owner, repo := splitRepo("myorg/myrepo")
	require.Equal(t, "myorg", owner, "owner should be parsed from org/repo format")
	require.Equal(t, "myrepo", repo, "repo should be parsed from org/repo format")

	owner, repo = splitRepo("noslash")
	require.Equal(t, "noslash", owner, "owner should equal input when no slash present")
	require.Equal(t, "noslash", repo, "repo should equal input when no slash present")
}

// TestGitHubAPIFlow tests the HTTP interactions with a mock GitHub API server
// for the remaining REST helpers (createPullRequest, addLabels). Branch/tree
// creation is now performed by git push from the sandbox.
func TestGitHubAPIFlow(t *testing.T) {
	t.Parallel()

	var requestPaths []string

	mux := http.NewServeMux()

	mux.HandleFunc("POST /repos/testorg/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/testorg/testrepo/pull/42",
		})
		require.NoError(t, err, "mock server should encode create pull request response")
	})

	mux.HandleFunc("POST /repos/testorg/testrepo/issues/42/labels", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		err := json.NewEncoder(w).Encode([]map[string]string{{"name": "143-generated"}})
		require.NoError(t, err, "mock server should encode set labels response")
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	ctx := context.Background()

	prNum, prURL, err := svc.createPullRequest(ctx, "test-token", "testorg", "testrepo", "fix: test PR", "body", "143/fix/test", "main")
	require.NoError(t, err, "createPullRequest should not return an error")
	require.Equal(t, 42, prNum, "createPullRequest should return PR number 42")
	require.Equal(t, "https://github.com/testorg/testrepo/pull/42", prURL, "createPullRequest should return the correct PR URL")

	err = svc.addLabels(ctx, "test-token", "testorg", "testrepo", 42, []string{"143-generated"})
	require.NoError(t, err, "addLabels should not return an error")

	require.Len(t, requestPaths, 2, "should have made exactly 2 API calls")
}

func TestHandlePullRequestEvent_Merged(t *testing.T) {
	t.Parallel()

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = true
	event.PR.Head.SHA = "abc123"
	event.Repository.FullName = "testorg/testrepo"

	// Verify event structure.
	data, err := json.Marshal(event)
	require.NoError(t, err, "marshaling PullRequestEvent should not return an error")

	var decoded PullRequestEvent
	require.NoError(t, json.Unmarshal(data, &decoded), "unmarshaling PullRequestEvent should not return an error")
	require.Equal(t, "closed", decoded.Action, "decoded action should be closed")
	require.True(t, decoded.PR.Merged, "decoded PR should be marked as merged")
	require.Equal(t, "abc123", decoded.PR.Head.SHA, "decoded PR head SHA should match")
	require.Equal(t, "testorg/testrepo", decoded.Repository.FullName, "decoded repository full name should match")
	require.Equal(t, 42, decoded.Number, "decoded PR number should be 42")
}

func TestHandlePullRequestEvent_ClosedWithoutMerge(t *testing.T) {
	t.Parallel()

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = false
	event.Repository.FullName = "testorg/testrepo"

	data, err := json.Marshal(event)
	require.NoError(t, err, "marshaling PullRequestEvent should not return an error")

	var decoded PullRequestEvent
	require.NoError(t, json.Unmarshal(data, &decoded), "unmarshaling PullRequestEvent should not return an error")
	require.Equal(t, "closed", decoded.Action, "decoded action should be closed")
	require.False(t, decoded.PR.Merged, "decoded PR should not be marked as merged when closed without merge")
}

func TestHandlePullRequestReviewEvent_Approved(t *testing.T) {
	t.Parallel()

	event := PullRequestReviewEvent{
		Action: "submitted",
	}
	event.Review.State = "approved"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	data, err := json.Marshal(event)
	require.NoError(t, err, "marshaling PullRequestReviewEvent should not return an error")

	var decoded PullRequestReviewEvent
	require.NoError(t, json.Unmarshal(data, &decoded), "unmarshaling PullRequestReviewEvent should not return an error")
	require.Equal(t, "submitted", decoded.Action, "decoded action should be submitted")
	require.Equal(t, "approved", decoded.Review.State, "decoded review state should be approved")
	require.Equal(t, 42, decoded.PullRequest.Number, "decoded PR number should be 42")
}

func TestHandlePullRequestReviewEvent_ChangesRequested(t *testing.T) {
	t.Parallel()

	event := PullRequestReviewEvent{
		Action: "submitted",
	}
	event.Review.State = "changes_requested"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	data, err := json.Marshal(event)
	require.NoError(t, err, "marshaling PullRequestReviewEvent should not return an error")

	var decoded PullRequestReviewEvent
	require.NoError(t, json.Unmarshal(data, &decoded), "unmarshaling PullRequestReviewEvent should not return an error")
	require.Equal(t, "changes_requested", decoded.Review.State, "decoded review state should be changes_requested")
}

func TestFirstLine(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hello", firstLine("hello\nworld"), "firstLine should return first line")
	require.Equal(t, "single", firstLine("single"), "firstLine should handle single line")
	require.Equal(t, "", firstLine(""), "firstLine should handle empty string")
	require.Equal(t, "trimmed", firstLine("  trimmed  \nsecond"), "firstLine should trim whitespace")
}

func TestHasRepoScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		scope  string
		expect bool
	}{
		{"repo", true},
		{"repo read:org", true},
		{"read:user,repo,user:email", true},
		{"repo,read:org", true},
		{"read:user,user:email", false},
		{"", false},
		{"public_repo", false}, // public_repo is not the same as repo
	}

	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expect, hasRepoScope(tt.scope), "hasRepoScope(%q)", tt.scope)
		})
	}
}

func TestDoGitHubRequest_ErrorResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, err := w.Write([]byte(`{"message":"Not Found"}`))
		require.NoError(t, err, "test server should write not found response body")
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	_, err := svc.doGitHubRequest(context.Background(), "test-token", http.MethodGet, "/repos/test/test/git/ref/heads/main", nil)
	require.Error(t, err, "doGitHubRequest should return an error for 404 response")
	require.Contains(t, err.Error(), "404", "error should contain the HTTP status code")
	require.Contains(t, err.Error(), "Not Found", "error should contain the response message")
}

func TestDoGitHubRequest_SetsHeaders(t *testing.T) {
	t.Parallel()

	var capturedAuth string
	var capturedAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedAccept = r.Header.Get("Accept")
		_, err := w.Write([]byte(`{}`))
		require.NoError(t, err, "test server should write empty JSON response body")
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	_, err := svc.doGitHubRequest(context.Background(), "my-token", http.MethodGet, "/test", nil)
	require.NoError(t, err, "doGitHubRequest should not return an error for valid request")
	require.Equal(t, "token my-token", capturedAuth, "Authorization header should be set with token prefix")
	require.Equal(t, "application/vnd.github+json", capturedAccept, "Accept header should be set to GitHub JSON media type")
}

func TestListRepositoryTree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		handlers func(t *testing.T) *http.ServeMux
		want     []models.RepositoryTreeEntry
		wantErr  string
	}{
		{
			name: "returns recursive repository tree",
			handlers: func(t *testing.T) *http.ServeMux {
				t.Helper()

				mux := http.NewServeMux()
				mux.HandleFunc("GET /repos/testorg/testrepo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
					err := json.NewEncoder(w).Encode(map[string]any{
						"object": map[string]string{"sha": "commit-sha"},
					})
					require.NoError(t, err, "mock server should encode branch ref response")
				})
				mux.HandleFunc("GET /repos/testorg/testrepo/git/commits/commit-sha", func(w http.ResponseWriter, r *http.Request) {
					err := json.NewEncoder(w).Encode(map[string]any{
						"tree": map[string]string{"sha": "tree-sha"},
					})
					require.NoError(t, err, "mock server should encode commit response")
				})
				mux.HandleFunc("GET /repos/testorg/testrepo/git/trees/tree-sha", func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, "recursive=1", r.URL.RawQuery, "tree requests should request a recursive listing")
					err := json.NewEncoder(w).Encode(map[string]any{
						"tree": []map[string]string{
							{"path": "internal/api/handlers/sessions.go", "type": "blob"},
							{"path": "frontend/src/app", "type": "tree"},
						},
					})
					require.NoError(t, err, "mock server should encode tree response")
				})
				return mux
			},
			want: []models.RepositoryTreeEntry{
				{Path: "internal/api/handlers/sessions.go", Type: models.RepositoryTreeEntryTypeFile},
				{Path: "frontend/src/app", Type: models.RepositoryTreeEntryTypeDirectory},
			},
		},
		{
			name: "fails when commit tree sha is missing",
			handlers: func(t *testing.T) *http.ServeMux {
				t.Helper()

				mux := http.NewServeMux()
				mux.HandleFunc("GET /repos/testorg/testrepo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
					err := json.NewEncoder(w).Encode(map[string]any{
						"object": map[string]string{"sha": "commit-sha"},
					})
					require.NoError(t, err, "mock server should encode branch ref response")
				})
				mux.HandleFunc("GET /repos/testorg/testrepo/git/commits/commit-sha", func(w http.ResponseWriter, r *http.Request) {
					err := json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{}})
					require.NoError(t, err, "mock server should encode empty commit tree response")
				})
				return mux
			},
			wantErr: "commit tree sha missing",
		},
		{
			name: "fails when tree payload is invalid",
			handlers: func(t *testing.T) *http.ServeMux {
				t.Helper()

				mux := http.NewServeMux()
				mux.HandleFunc("GET /repos/testorg/testrepo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
					err := json.NewEncoder(w).Encode(map[string]any{
						"object": map[string]string{"sha": "commit-sha"},
					})
					require.NoError(t, err, "mock server should encode branch ref response")
				})
				mux.HandleFunc("GET /repos/testorg/testrepo/git/commits/commit-sha", func(w http.ResponseWriter, r *http.Request) {
					err := json.NewEncoder(w).Encode(map[string]any{
						"tree": map[string]string{"sha": "tree-sha"},
					})
					require.NoError(t, err, "mock server should encode commit response")
				})
				mux.HandleFunc("GET /repos/testorg/testrepo/git/trees/tree-sha", func(w http.ResponseWriter, r *http.Request) {
					_, err := w.Write([]byte("{"))
					require.NoError(t, err, "mock server should write malformed tree payload")
				})
				return mux
			},
			wantErr: "decode tree: unexpected end of JSON input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handlers(t))
			defer server.Close()

			svc := &PRService{
				baseURL:    server.URL,
				httpClient: server.Client(),
				logger:     zerolog.Nop(),
			}

			tree, err := svc.ListRepositoryTree(context.Background(), "test-token", "testorg", "testrepo", "main")
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "ListRepositoryTree should surface the expected failure")
				return
			}

			require.NoError(t, err, "ListRepositoryTree should return the repository tree")
			require.Equal(t, tt.want, tree, "ListRepositoryTree should map GitHub tree entries into repository tree entries")
		})
	}
}

func TestFormatPRBody_WithIssue(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := &PRService{logger: logger}

	summary := "Fixed the bug"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}
	issue := &models.Issue{
		Source:   models.IssueSourceLinear,
		Severity: "critical",
		Title:    "Null pointer in user handler",
	}

	body := svc.formatPRBody(context.Background(), run, issue)

	require.Contains(t, body, "## Summary", "PR body should contain Summary heading")
	require.Contains(t, body, "Fixed the bug", "PR body should contain the result summary")
	require.Contains(t, body, "**Issue**: linear", "PR body should contain issue source")
	require.Contains(t, body, "critical", "PR body should contain the severity")
	require.Contains(t, body, "## Test plan", "PR body should contain Test plan heading")
}

func TestFormatPRBody_NilSummary(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := &PRService{logger: logger}

	run := &models.Session{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		AgentType: "claude-code",
	}

	body := svc.formatPRBody(context.Background(), run, nil)
	require.Contains(t, body, "Automated changes generated by 143.dev", "PR body with nil summary should contain default text")
}

func TestDecodeBase64Content(t *testing.T) {
	t.Parallel()

	// Standard base64 encoding of "## PR Template\n\nDescribe your changes."
	encoded := "IyMgUFIgVGVtcGxhdGUKCkRlc2NyaWJlIHlvdXIgY2hhbmdlcy4="
	decoded, err := decodeBase64Content(encoded)
	require.NoError(t, err, "should decode valid base64")
	require.Equal(t, "## PR Template\n\nDescribe your changes.", decoded)

	// With embedded newlines (GitHub-style).
	withNewlines := "IyMgUFIg\nVGVtcGxhdGU="
	decoded2, err := decodeBase64Content(withNewlines)
	require.NoError(t, err, "should handle base64 with newlines")
	require.Contains(t, decoded2, "PR")

	// Invalid base64.
	_, err = decodeBase64Content("not-valid-base64!!!")
	require.Error(t, err, "should return error for invalid base64")
}

func TestFetchPRTemplate_NoTemplate(t *testing.T) {
	t.Parallel()

	// Server returns 404 for all paths.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	content, path := svc.fetchPRTemplate(context.Background(), "token", "owner", "repo", "main")
	require.Empty(t, content, "should return empty when no template found")
	require.Empty(t, path, "should return empty path when no template found")
}

func TestFetchPRTemplate_FoundTemplate(t *testing.T) {
	t.Parallel()

	// Encode "## Description\n\nWhat changed?"
	templateContent := "## Description\n\nWhat changed?"
	encoded := "IyMgRGVzY3JpcHRpb24KCldoYXQgY2hhbmdlZD8="

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "pull_request_template") {
			err := json.NewEncoder(w).Encode(map[string]string{
				"content":  encoded,
				"encoding": "base64",
			})
			require.NoError(t, err)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	content, path := svc.fetchPRTemplate(context.Background(), "token", "owner", "repo", "main")
	require.Equal(t, templateContent, content, "should return decoded template content")
	require.NotEmpty(t, path, "should return the matched template path")
}

func TestFetchPRTemplate_FallsBackToDirectoryTemplate(t *testing.T) {
	t.Parallel()

	templateContent := "## Default Template\n\nExplain the change."
	encoded := base64.StdEncoding.EncodeToString([]byte(templateContent))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/contents/.github/PULL_REQUEST_TEMPLATE") && !strings.Contains(r.URL.Path, "default.md"):
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]string{
				{"name": "default.md", "path": ".github/PULL_REQUEST_TEMPLATE/default.md", "type": "file"},
				{"name": "extra.txt", "path": ".github/PULL_REQUEST_TEMPLATE/extra.txt", "type": "file"},
			}), "directory listing should encode successfully")
		case strings.Contains(r.URL.Path, "/contents/.github/PULL_REQUEST_TEMPLATE/default.md"):
			require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
				"content":  encoded,
				"encoding": "base64",
			}), "default template should encode successfully")
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	content, path := svc.fetchPRTemplate(context.Background(), "token", "owner", "repo", "main")
	require.Equal(t, templateContent, content, "should return the default template from the directory fallback")
	require.Equal(t, ".github/PULL_REQUEST_TEMPLATE/default.md", path, "should return the selected directory template path")
}

func TestFetchFileContent_RequestError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	content, ok := svc.fetchFileContent(context.Background(), "token", "owner", "repo", "main", ".github/pull_request_template.md")
	require.False(t, ok, "fetchFileContent should report failure when the GitHub request fails")
	require.Empty(t, content, "fetchFileContent should return empty content on request failure")
}

func TestGetOrFetchPRTemplate_NilCache(t *testing.T) {
	t.Parallel()

	// Server returns 404 — no template.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:     server.URL,
		httpClient:  server.Client(),
		prTemplates: nil, // no cache store
		logger:      zerolog.Nop(),
	}

	content := svc.getOrFetchPRTemplate(context.Background(), "token", "owner", "repo", "main", uuid.New(), uuid.New())
	require.Empty(t, content, "should return empty when no template and no cache")
}

func TestGeneratePRContent_NoLLMClient(t *testing.T) {
	t.Parallel()

	summary := "Fixed the auth bug"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}

	svc := &PRService{
		logger: zerolog.Nop(),
	}

	_, err := svc.generatePRContent(context.Background(), "token", "owner", "repo", "main", uuid.New(), uuid.New(), run, nil)
	require.Error(t, err, "should fail without LLM client")
	require.Contains(t, err.Error(), "no LLM client")
}

func TestGeneratePRContent_WithLLM(t *testing.T) {
	t.Parallel()

	// Server returns 404 for template lookup (no repo template).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	summary := "Fixed the auth bug by adding null check"
	diff := "+++ b/auth.go\n+if user == nil {\n+  return ErrUnauthorized\n+}\n"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
		Diff:          &diff,
	}

	mockLLM := &mockLLMClient{
		response: "<pr_title>Fix null pointer in auth middleware</pr_title>\n<pr_body>\n## Summary\n\nAdded a nil check for the user object in the auth middleware to prevent panics when unauthenticated requests hit protected endpoints.\n\n## Changes\n\n- Added nil guard in auth.go\n\n## Test plan\n\n- Validated by automated agent run\n</pr_body>",
	}

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		llmClient:  mockLLM,
		logger:     zerolog.Nop(),
	}

	result, err := svc.generatePRContent(context.Background(), "token", "owner", "repo", "main", uuid.New(), uuid.New(), run, nil)
	require.NoError(t, err)
	require.Equal(t, "Fix null pointer in auth middleware", result.Title)
	require.Contains(t, result.Body, "## Summary")
	require.Contains(t, result.Body, "nil check")
	require.Contains(t, result.Body, "[143.dev](https://143.dev)", "generated PR body should include the 143.dev link footer")
	require.Contains(t, result.Body, "[session ", "generated PR body should include the session link footer")
	require.NotContains(t, result.Body, "Generated by [143.dev]", "generated PR body should not include branded footer text")
	require.NotContains(t, result.Body, "\n---\n", "generated PR body should not append a separator outside the body content")
}

func TestGeneratePRContent_MinimallySanitizesLLMTitle(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	summary := "Aligned file ordering between file detail view and Changes sidebar."
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}

	mockLLM := &mockLLMClient{
		response: "<pr_title>  \"Keep file ordering consistent between detail view and Changes sidebar\"  </pr_title>\n<pr_body>\n## Summary\n\nAligned file ordering between the two views.\n</pr_body>",
	}

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		llmClient:  mockLLM,
		logger:     zerolog.Nop(),
	}

	result, err := svc.generatePRContent(context.Background(), "token", "owner", "repo", "main", uuid.New(), uuid.New(), run, nil)
	require.NoError(t, err, "generatePRContent should succeed with a verbose LLM title")
	require.Equal(t, "Keep file ordering consistent between detail view and Changes sidebar", result.Title, "generatePRContent should only apply minimal cleanup to LLM titles")
}

func TestGeneratePRContent_WithRepoTemplate(t *testing.T) {
	t.Parallel()

	templateContent := "## What\n\n## Why\n\n## Testing\n"
	encodedTemplate := base64.StdEncoding.EncodeToString([]byte(templateContent))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "pull_request_template") {
			resp, _ := json.Marshal(map[string]string{
				"content":  encodedTemplate,
				"encoding": "base64",
			})
			_, _ = w.Write(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	summary := "Refactored the login flow"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}

	mockLLM := &mockLLMClient{
		response: "<pr_title>Refactor login flow for clarity</pr_title>\n<pr_body>\n## What\n\nSimplified the login handler.\n\n## Why\n\nReduce complexity and improve readability.\n\n## Testing\n\nUnit tests pass.\n</pr_body>",
	}

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		llmClient:  mockLLM,
		logger:     zerolog.Nop(),
	}

	result, err := svc.generatePRContent(context.Background(), "token", "owner", "repo", "main", uuid.New(), uuid.New(), run, nil)
	require.NoError(t, err)
	require.Equal(t, "Refactor login flow for clarity", result.Title)
	require.Contains(t, result.Body, "## What")
	require.Contains(t, result.Body, "[143.dev](https://143.dev)", "template-backed PR body should append the 143.dev link footer")
	require.Contains(t, result.Body, "[session ", "template-backed PR body should append the session link footer")
	require.NotContains(t, result.Body, "Generated by [143.dev]", "template-backed PR body should not append branded footer text")
	require.NotContains(t, result.Body, "\n---\n", "template-backed PR body should not append a separator outside the template")

	// Verify the template was passed to the LLM.
	require.Contains(t, mockLLM.lastUserPrompt, "## What", "LLM prompt should contain the repo template")
	require.Contains(t, mockLLM.lastUserPrompt, "<pr_template>", "LLM prompt should wrap template in pr_template tags")
}

func TestGeneratePRContent_IncludesAllThreadSummaries(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	primaryThreadID := uuid.New()
	reviewThreadID := uuid.New()
	now := time.Now()
	// The session ResultSummary matches the review thread (last to complete), simulating
	// a review-loop session where the review thread's summary populated run.ResultSummary.
	reviewSummary := "Review loop: checked the code and requested naming cleanup."
	diff := "+++ b/internal/services/github/pr.go\n+func buildPRThreadContext() string { return \"\" }\n"
	run := &models.Session{
		ID:              sessionID,
		PrimaryThreadID: &primaryThreadID,
		OrgID:           orgID,
		AgentType:       "claude-code",
		ResultSummary:   &reviewSummary,
		Diff:            &diff,
	}

	mockLLM := &mockLLMClient{
		response: "<pr_title>Use session threads for PR context</pr_title>\n<pr_body>\n## Summary\n\nUses all session thread summaries.\n</pr_body>",
	}
	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		llmClient:  mockLLM,
		logger:     zerolog.Nop(),
	}
	svc.SetSessionThreadStore(fakeSessionThreadLister{threads: []models.SessionThread{
		{
			ID:            primaryThreadID,
			SessionID:     sessionID,
			OrgID:         orgID,
			Label:         "Main",
			Status:        models.ThreadStatusCompleted,
			CreatedAt:     now,
			ResultSummary: ptrString("Implemented PR context generation from thread summaries."),
		},
		{
			ID:                reviewThreadID,
			SessionID:         sessionID,
			OrgID:             orgID,
			Label:             "Review",
			Status:            models.ThreadStatusCompleted,
			CreatedAt:         now.Add(time.Minute),
			CreatedBySource:   models.ThreadCreatedBySourceAgentTool,
			CreatedByThreadID: &primaryThreadID,
			ResultSummary:     ptrString(reviewSummary),
		},
	}})

	_, err := svc.generatePRContent(context.Background(), "token", "owner", "repo", "main", uuid.New(), orgID, run, nil)
	require.NoError(t, err, "generatePRContent should succeed when thread context loads")
	require.Contains(t, mockLLM.lastUserPrompt, "<session_threads>", "LLM prompt should include session thread context")
	require.Contains(t, mockLLM.lastUserPrompt, "Main (primary)", "prompt should identify the primary implementation thread")
	require.Contains(t, mockLLM.lastUserPrompt, "Implemented PR context generation from thread summaries.", "prompt should include primary thread summary")
	// The review thread's summary matches run.ResultSummary and is already in <agent_summary>;
	// it must not be duplicated inside <session_threads>.
	require.Equal(t, 1, strings.Count(mockLLM.lastUserPrompt, reviewSummary),
		"review summary should appear exactly once (in agent_summary only, not duplicated in session_threads)")
	agentSummaryIdx := strings.Index(mockLLM.lastUserPrompt, "<agent_summary>")
	sessionThreadsIdx := strings.Index(mockLLM.lastUserPrompt, "<session_threads>")
	require.Greater(t, sessionThreadsIdx, agentSummaryIdx, "session_threads block should follow agent_summary block")
	require.Contains(t, mockLLM.lastSystemPrompt, "Use the code diff and files changed as the source of truth", "system prompt should make the code changes the primary source of truth")
	require.Contains(t, mockLLM.lastSystemPrompt, "Use the session title, issue, and session threads only as supporting context", "system prompt should keep conversation context secondary to the code changes")
}

func TestGeneratePRContent_ThreadStoreError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	orgID := uuid.New()
	summary := "Agent finished."
	diff := "+++ b/x.go\n+func f() {}\n"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         orgID,
		AgentType:     "claude-code",
		ResultSummary: &summary,
		Diff:          &diff,
	}

	mockLLM := &mockLLMClient{
		response: "<pr_title>Title</pr_title>\n<pr_body>Body</pr_body>",
	}
	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		llmClient:  mockLLM,
		logger:     zerolog.Nop(),
	}
	svc.SetSessionThreadStore(fakeSessionThreadLister{err: errors.New("db unavailable")})

	_, err := svc.generatePRContent(context.Background(), "token", "owner", "repo", "main", uuid.New(), orgID, run, nil)
	require.NoError(t, err, "generatePRContent should degrade gracefully when thread store errors")
	require.NotContains(t, mockLLM.lastUserPrompt, "<session_threads>", "prompt should omit session_threads tag when thread store fails")
}

func TestGeneratePRContent_ThreadCapAndEmptySummaries(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	diff := "+++ b/x.go\n+func f() {}\n"
	run := &models.Session{
		ID:        sessionID,
		OrgID:     orgID,
		AgentType: "claude-code",
		Diff:      &diff,
	}

	// Build 10 threads: one with no summary, nine with distinct summaries.
	// The no-summary thread should be skipped; at most 8 of the 9 summaries should appear.
	threads := make([]models.SessionThread, 0, 10)
	threads = append(threads, models.SessionThread{
		ID:        uuid.New(),
		SessionID: sessionID,
		OrgID:     orgID,
		Label:     "Empty",
		Status:    models.ThreadStatusCompleted,
		CreatedAt: now,
		// ResultSummary intentionally nil
	})
	for i := range 9 {
		s := fmt.Sprintf("Thread summary number %d with distinct content.", i+1)
		threads = append(threads, models.SessionThread{
			ID:            uuid.New(),
			SessionID:     sessionID,
			OrgID:         orgID,
			Label:         fmt.Sprintf("Thread%d", i+1),
			Status:        models.ThreadStatusCompleted,
			CreatedAt:     now.Add(time.Duration(i+1) * time.Minute),
			ResultSummary: ptrString(s),
		})
	}

	mockLLM := &mockLLMClient{
		response: "<pr_title>Title</pr_title>\n<pr_body>Body</pr_body>",
	}
	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		llmClient:  mockLLM,
		logger:     zerolog.Nop(),
	}
	svc.SetSessionThreadStore(fakeSessionThreadLister{threads: threads})

	_, err := svc.generatePRContent(context.Background(), "token", "owner", "repo", "main", uuid.New(), orgID, run, nil)
	require.NoError(t, err)
	// Threads 1-8 should appear; thread 9 should be capped out.
	for i := range 8 {
		require.Contains(t, mockLLM.lastUserPrompt, fmt.Sprintf("Thread summary number %d", i+1))
	}
	require.NotContains(t, mockLLM.lastUserPrompt, "Thread summary number 9", "9th summary should be excluded by the 8-thread cap")
	require.NotContains(t, mockLLM.lastUserPrompt, "Empty", "thread with nil summary should be excluded")
}

func TestParsePRContentResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		response  string
		wantTitle string
		wantBody  string
	}{
		{
			name:      "valid XML tags",
			response:  "<pr_title>Fix auth bug</pr_title>\n<pr_body>\n## Summary\nFixed it.\n</pr_body>",
			wantTitle: "Fix auth bug",
			wantBody:  "## Summary\nFixed it.",
		},
		{
			name:      "XML with extra whitespace",
			response:  "  <pr_title>  Fix auth bug  </pr_title>\n<pr_body>\n  Body here.\n</pr_body>  ",
			wantTitle: "Fix auth bug",
			wantBody:  "Body here.",
		},
		{
			name:      "empty response",
			response:  "",
			wantTitle: "",
			wantBody:  "",
		},
		{
			name:      "title only",
			response:  "<pr_title>Just a title</pr_title>",
			wantTitle: "Just a title",
			wantBody:  "",
		},
		{
			name:      "body only",
			response:  "<pr_body>## Summary\nJust a body.</pr_body>",
			wantTitle: "",
			wantBody:  "## Summary\nJust a body.",
		},
		{
			name:      "no tags fallback treats as body",
			response:  "## Summary\nJust a body.",
			wantTitle: "",
			wantBody:  "## Summary\nJust a body.",
		},
		{
			name:      "multiline body with markdown",
			response:  "<pr_title>Add user validation</pr_title>\n<pr_body>\n## Summary\n\nAdded validation.\n\n## Changes\n\n- Added `validateUser()` in `auth.go`\n- Updated tests\n\n## Test plan\n\nUnit tests pass.\n</pr_body>",
			wantTitle: "Add user validation",
			wantBody:  "## Summary\n\nAdded validation.\n\n## Changes\n\n- Added `validateUser()` in `auth.go`\n- Updated tests\n\n## Test plan\n\nUnit tests pass.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parsePRContentResponse(tt.response)
			require.Equal(t, tt.wantTitle, result.Title, "title mismatch")
			require.Equal(t, tt.wantBody, result.Body, "body mismatch")
		})
	}
}

func TestSummarizeDiff(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
@@ -10,6 +10,9 @@
+if user == nil {
+  return ErrUnauthorized
+}
-old line removed
diff --git a/handler.go b/handler.go
--- a/handler.go
+++ b/handler.go
@@ -5,3 +5,4 @@
+newLine`

	summary, truncated := summarizeDiff(diff, 10000)
	require.Contains(t, summary, "auth.go")
	require.Contains(t, summary, "handler.go")
	require.Contains(t, summary, "4 additions")
	require.Contains(t, summary, "1 deletions")
	require.Equal(t, diff, truncated, "diff under maxChars should not be truncated")

	// Test truncation.
	_, truncatedShort := summarizeDiff(diff, 50)
	require.Contains(t, truncatedShort, "... (truncated)")
	require.LessOrEqual(t, len(truncatedShort), 70)
}

func TestResolveToken_AppOnly(t *testing.T) {
	t.Parallel()

	tokenSvc := &Service{cache: make(map[int64]*cachedToken)}
	tokenSvc.cache[1] = &cachedToken{
		Token:     "app-token-123",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		logger:        zerolog.Nop(),
	}

	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipAppOnly}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	resolution, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.NoError(t, err)
	require.Equal(t, "app-token-123", resolution.Token)
	require.False(t, resolution.IsUserToken())
}

func TestResolveToken_UserPreferred_NoUser(t *testing.T) {
	t.Parallel()

	tokenSvc := &Service{cache: make(map[int64]*cachedToken)}
	tokenSvc.cache[1] = &cachedToken{
		Token:     "app-token-fallback",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		logger:        zerolog.Nop(),
	}

	repo := &models.Repository{InstallationID: 1}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()} // no TriggeredByUserID

	resolution, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.NoError(t, err)
	require.Equal(t, "app-token-fallback", resolution.Token)
	require.False(t, resolution.IsUserToken(), "should fall back to app token when no user")
}

func TestResolveToken_UserRequired_NoUser(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}

	repo := &models.Repository{InstallationID: 1}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserRequired}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	_, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.ErrorIs(t, err, ErrGitHubUserAuthRequired, "should fail with a typed auth-required error when user auth is required")
}

func TestPRService_SetAppUserAuth(t *testing.T) {
	t.Parallel()

	svc := &PRService{}
	auth := &stubPRAppUserAuth{}
	svc.SetAppUserAuth(auth)
	require.Same(t, auth, svc.appUserAuth, "SetAppUserAuth should store the provided auth service")
}

func TestPRService_ConfigurationAccessors(t *testing.T) {
	t.Parallel()

	integrationStore := db.NewIntegrationStore(nil)
	userStore := db.NewUserStore(nil)
	orgStore := db.NewOrganizationStore(nil)
	prTemplateStore := db.NewPRTemplateStore(nil)
	llmClient := &mockLLMClient{}
	auth := &stubPRAppUserAuth{}

	svc := &PRService{}
	svc.SetIntegrationStore(integrationStore)
	svc.SetAppUserAuth(auth)
	svc.SetLLMClient(llmClient)
	svc.SetUserStore(userStore)
	svc.SetOrgStore(orgStore)
	svc.SetPRTemplateStore(prTemplateStore)

	require.Same(t, integrationStore, svc.IntegrationStore(), "IntegrationStore should return the configured integration store")
	require.True(t, svc.HasAppUserAuth(), "HasAppUserAuth should report true when app user auth is configured")
	require.Same(t, llmClient, svc.LLMClient(), "LLMClient should return the configured client")
	require.Same(t, userStore, svc.UserStore(), "UserStore should return the configured user store")
	require.Same(t, orgStore, svc.OrgStore(), "OrgStore should return the configured org store")
	require.Same(t, prTemplateStore, svc.PRTemplateStore(), "PRTemplateStore should return the configured PR template store")
}

func TestPRService_HasAppUserAuth_FalseWhenUnset(t *testing.T) {
	t.Parallel()

	svc := &PRService{}
	require.False(t, svc.HasAppUserAuth(), "HasAppUserAuth should report false when app user auth is not configured")
}

func TestResolveToken_AuthorModeAppUsesInstallationToken(t *testing.T) {
	t.Parallel()

	tokenSvc := &Service{cache: make(map[int64]*cachedToken)}
	tokenSvc.cache[1] = &cachedToken{
		Token:     "app-token",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}
	svc := &PRService{
		tokenProvider: tokenSvc,
		logger:        zerolog.Nop(),
	}

	resolution, err := svc.resolveToken(context.Background(), &models.Session{ID: uuid.New(), OrgID: uuid.New()}, &models.Repository{InstallationID: 1}, models.OrgSettings{}, "app")
	require.NoError(t, err, "resolveToken should accept explicit app author mode")
	require.Equal(t, "app-token", resolution.Token, "resolveToken should use the installation token in app mode")
	require.False(t, resolution.IsUserToken(), "app mode should not report a user token")
}

func TestResolveToken_FallsBackToIntegrationInstallationWhenRepoInstallationMissing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	integrationID := uuid.New()
	mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
			AddRow(integrationID, uuid.New(), "github", []byte(`{"installation_id":2}`), "active", nil, time.Now()))

	tokenSvc := &Service{cache: make(map[int64]*cachedToken)}
	tokenSvc.cache[2] = &cachedToken{
		Token:     "fallback-token",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		integrations:  db.NewIntegrationStore(mock),
		logger:        zerolog.Nop(),
	}

	repo := &models.Repository{InstallationID: 0, IntegrationID: integrationID}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	resolution, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.NoError(t, err, "resolveToken should recover when the repo row is missing installation_id")
	require.Equal(t, "fallback-token", resolution.Token, "resolveToken should use the repository integration installation token")
	require.False(t, resolution.IsUserToken(), "fallback installation token should still be treated as an app token")
	require.NoError(t, mock.ExpectationsWereMet(), "all integration fallback expectations should be met")
}

func TestResolveToken_FallsBackToIntegrationInstallationWhenRepoInstallationIsStale(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
			AddRow(integrationID, orgID, "github", []byte(`{"installation_id":2}`), "active", nil, time.Now()))

	tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "should create a GitHub service with a test private key")
	tokenSvc.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/app/installations/1/access_tokens":
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader(`{"message":"Not Found"}`)),
					Header:     make(http.Header),
				}, nil
			case "/app/installations/2/access_tokens":
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(`{"token":"fallback-token","expires_at":"2030-01-01T00:00:00Z"}`)),
					Header:     make(http.Header),
				}, nil
			default:
				return nil, errors.New("unexpected installation token request path")
			}
		}),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		integrations:  db.NewIntegrationStore(mock),
		logger:        zerolog.Nop(),
	}

	repo := &models.Repository{InstallationID: 1, IntegrationID: integrationID}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: orgID}

	resolution, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.NoError(t, err, "resolveToken should recover when the repo installation_id is stale")
	require.Equal(t, "fallback-token", resolution.Token, "resolveToken should retry with the repository integration installation token")
	require.False(t, resolution.IsUserToken(), "fallback installation token should still be treated as an app token")
	require.NoError(t, mock.ExpectationsWereMet(), "all integration fallback expectations should be met")
}

func TestGetInstallationTokenForRepo_ErrorPaths(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoName := "acme/repo"

	tests := []struct {
		name      string
		repo      *models.Repository
		setupSvc  func(t *testing.T) *PRService
		wantError string
	}{
		{
			name: "missing repo installation without integration store",
			repo: &models.Repository{FullName: repoName},
			setupSvc: func(t *testing.T) *PRService {
				t.Helper()
				return &PRService{logger: zerolog.Nop()}
			},
			wantError: "has no github installation_id",
		},
		{
			name: "primary non 404 error returns immediately",
			repo: &models.Repository{FullName: repoName, InstallationID: 1},
			setupSvc: func(t *testing.T) *PRService {
				t.Helper()
				tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
				require.NoError(t, err, "should create a GitHub service with a test private key")
				tokenSvc.httpClient = &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusUnauthorized,
							Body:       io.NopCloser(strings.NewReader(`{"message":"bad credentials"}`)),
							Header:     make(http.Header),
						}, nil
					}),
				}
				return &PRService{tokenProvider: tokenSvc, logger: zerolog.Nop()}
			},
			wantError: "returned 401",
		},
		{
			name: "missing integration id after stale 404",
			repo: &models.Repository{FullName: repoName, InstallationID: 1},
			setupSvc: func(t *testing.T) *PRService {
				t.Helper()
				tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
				require.NoError(t, err, "should create a GitHub service with a test private key")
				tokenSvc.httpClient = &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusNotFound,
							Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
							Header:     make(http.Header),
						}, nil
					}),
				}
				return &PRService{tokenProvider: tokenSvc, integrations: db.NewIntegrationStore(newMockPool(t)), logger: zerolog.Nop()}
			},
			wantError: "returned 404",
		},
		{
			name: "integration lookup failure returns primary error",
			repo: &models.Repository{FullName: repoName, InstallationID: 1, IntegrationID: uuid.New()},
			setupSvc: func(t *testing.T) *PRService {
				t.Helper()
				mock := newMockPool(t)
				mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)

				tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
				require.NoError(t, err, "should create a GitHub service with a test private key")
				tokenSvc.httpClient = &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusNotFound,
							Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
							Header:     make(http.Header),
						}, nil
					}),
				}
				return &PRService{tokenProvider: tokenSvc, integrations: db.NewIntegrationStore(mock), logger: zerolog.Nop()}
			},
			wantError: "returned 404",
		},
		{
			name: "integration config parse failure returns primary error",
			repo: &models.Repository{FullName: repoName, InstallationID: 1, IntegrationID: uuid.New()},
			setupSvc: func(t *testing.T) *PRService {
				t.Helper()
				mock := newMockPool(t)
				mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
						AddRow(uuid.New(), orgID, "github", []byte(`{`), "active", nil, time.Now()))

				tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
				require.NoError(t, err, "should create a GitHub service with a test private key")
				tokenSvc.httpClient = &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusNotFound,
							Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
							Header:     make(http.Header),
						}, nil
					}),
				}
				return &PRService{tokenProvider: tokenSvc, integrations: db.NewIntegrationStore(mock), logger: zerolog.Nop()}
			},
			wantError: "returned 404",
		},
		{
			name: "same installation id returns primary error",
			repo: &models.Repository{FullName: repoName, InstallationID: 1, IntegrationID: uuid.New()},
			setupSvc: func(t *testing.T) *PRService {
				t.Helper()
				mock := newMockPool(t)
				mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
						AddRow(uuid.New(), orgID, "github", []byte(`{"installation_id":1}`), "active", nil, time.Now()))

				tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
				require.NoError(t, err, "should create a GitHub service with a test private key")
				tokenSvc.httpClient = &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusNotFound,
							Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
							Header:     make(http.Header),
						}, nil
					}),
				}
				return &PRService{tokenProvider: tokenSvc, integrations: db.NewIntegrationStore(mock), logger: zerolog.Nop()}
			},
			wantError: "returned 404",
		},
		{
			name: "fallback installation token failure is returned",
			repo: &models.Repository{FullName: repoName, InstallationID: 1, IntegrationID: uuid.New()},
			setupSvc: func(t *testing.T) *PRService {
				t.Helper()
				mock := newMockPool(t)
				mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
						AddRow(uuid.New(), orgID, "github", []byte(`{"installation_id":2}`), "active", nil, time.Now()))

				tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
				require.NoError(t, err, "should create a GitHub service with a test private key")
				tokenSvc.httpClient = &http.Client{
					Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						switch req.URL.Path {
						case "/app/installations/1/access_tokens":
							return &http.Response{
								StatusCode: http.StatusNotFound,
								Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
								Header:     make(http.Header),
							}, nil
						case "/app/installations/2/access_tokens":
							return &http.Response{
								StatusCode: http.StatusUnauthorized,
								Body:       io.NopCloser(strings.NewReader(`{"message":"bad credentials"}`)),
								Header:     make(http.Header),
							}, nil
						default:
							return nil, errors.New("unexpected installation token request path")
						}
					}),
				}
				return &PRService{tokenProvider: tokenSvc, integrations: db.NewIntegrationStore(mock), logger: zerolog.Nop()}
			},
			wantError: "installation 2",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := tt.setupSvc(t)
			_, err := svc.getInstallationTokenForRepo(context.Background(), orgID, tt.repo)
			require.Error(t, err, "getInstallationTokenForRepo should return an error for this path")
			require.Contains(t, err.Error(), tt.wantError, "getInstallationTokenForRepo should preserve the expected error context")
		})
	}

}

func TestResolveToken_UsesGitHubAppUserCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			require.Equal(t, "token ghu_user_token", r.Header.Get("Authorization"), "repo access probe should use the user token")
			_, _ = w.Write([]byte(`{"full_name":"owner/repo"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	orgID := uuid.New()
	userID := uuid.New()

	svc := &PRService{
		appUserAuth: &stubPRAppUserAuth{
			getValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
				return &models.GitHubAppUserConfig{
					AccessToken:           "ghu_user_token",
					RefreshToken:          "ghr_refresh",
					ExpiresAt:             time.Now().Add(time.Hour),
					RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
				}, nil
			},
		},
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}

	resolution, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.NoError(t, err, "resolveToken should use the GitHub App user credential when available")
	require.Equal(t, "ghu_user_token", resolution.Token, "resolveToken should return the user access token")
	require.True(t, resolution.IsUserToken(), "resolveToken should mark GitHub App user tokens as user-authored")
}

func TestResolveToken_UserPreferredFallsBackWhenUserTokenCannotAccessRepo(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			require.Equal(t, "token ghu_user_token", r.Header.Get("Authorization"), "repo access probe should use the user token")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tokenSvc := &Service{cache: make(map[int64]*cachedToken)}
	tokenSvc.cache[1] = &cachedToken{
		Token:     "app-token-fallback",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	orgID := uuid.New()
	userID := uuid.New()
	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}
	svc := &PRService{
		tokenProvider: tokenSvc,
		appUserAuth: &stubPRAppUserAuth{
			getValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
				return &models.GitHubAppUserConfig{
					AccessToken:           "ghu_user_token",
					RefreshToken:          "ghr_refresh",
					ExpiresAt:             time.Now().Add(time.Hour),
					RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
				}, nil
			},
		},
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	resolution, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.NoError(t, err, "user_preferred should fall back to the app token when the user token cannot access the repo")
	require.Equal(t, "app-token-fallback", resolution.Token, "resolveToken should fall back to the installation token")
	require.False(t, resolution.IsUserToken(), "resolveToken should mark the fallback token as app-authored")
}

func TestResolveToken_UserRequiredErrorsWhenUserTokenCannotAccessRepo(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserRequired}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}
	svc := &PRService{
		appUserAuth: &stubPRAppUserAuth{
			getValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
				return &models.GitHubAppUserConfig{
					AccessToken:           "ghu_user_token",
					RefreshToken:          "ghr_refresh",
					ExpiresAt:             time.Now().Add(time.Hour),
					RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
				}, nil
			},
		},
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	_, err := svc.resolveToken(context.Background(), run, repo, settings, "")
	require.Error(t, err, "user_required should fail when the user token cannot access the target repo")
	require.ErrorIs(t, err, ErrGitHubUserAuthRepoAccessDenied, "user_required should surface a typed repo-access-denied sentinel")
	require.Contains(t, err.Error(), "cannot access repo", "resolveToken should surface repo access failures for user-required auth")
}

func TestValidateUserToken_ValidToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/user", r.URL.Path)
		_, _ = w.Write([]byte(`{"id": 1, "login": "testuser"}`))
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	require.True(t, svc.validateUserToken(context.Background(), "valid-token"))
}

func TestValidateUserToken_RevokedToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	require.False(t, svc.validateUserToken(context.Background(), "revoked-token"))
}

func TestFillRepoTemplate_NoLLMClient(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	_, err := svc.fillRepoTemplate(context.Background(), "## Template", run, nil)
	require.Error(t, err, "should fail without LLM client")
	require.Contains(t, err.Error(), "no LLM client")
}

func TestFormatPRBody_SessionLink(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}
	summary := "Fixed a bug"
	run := &models.Session{
		ID:            uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"),
		OrgID:         uuid.New(),
		ResultSummary: &summary,
	}

	body := svc.formatPRBody(context.Background(), run, nil)
	require.Contains(t, body, "session abcdef01", "should contain short session ID in footer")
	require.Contains(t, body, "https://143.dev/sessions/", "should contain session link with the public app base URL")
	require.Contains(t, body, "[143.dev](https://143.dev)", "fallback PR body should include the 143.dev link footer")
	require.NotContains(t, body, "https://app.143.dev/sessions/", "should not use the deprecated app subdomain in the footer")
	require.NotContains(t, body, "Generated by [143.dev]", "fallback PR body should not include branded footer text")
	require.NotContains(t, body, "\n---\n", "fallback PR body should not append a separator outside the body content")
}

func TestFormatPRBody_SessionLinkUsesConfiguredAppBaseURL(t *testing.T) {
	t.Parallel()

	svc := &PRService{
		logger:     zerolog.Nop(),
		appBaseURL: "https://frontend.example.com/",
	}
	summary := "Fixed a bug"
	run := &models.Session{
		ID:            uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"),
		OrgID:         uuid.New(),
		ResultSummary: &summary,
	}

	body := svc.formatPRBody(context.Background(), run, nil)
	require.Contains(t, body, "https://frontend.example.com/sessions/abcdef01-2345-6789-abcd-ef0123456789", "should use the configured app base URL for the session link")
	require.NotContains(t, body, "//sessions/", "should trim trailing slashes when building the session link")
}

func TestPRPreviewURLCreatesDurablePreviewOriginTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	targetID := uuid.New()
	repo := &models.Repository{
		ID:       repoID,
		OrgID:    orgID,
		FullName: "owner/repo",
	}
	run := &models.Session{
		ID:                sessionID,
		OrgID:             orgID,
		RepositoryID:      &repoID,
		TriggeredByUserID: &userID,
	}

	mock.ExpectQuery("INSERT INTO preview_targets").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestPreviewTargetColumns).AddRow(
				targetID, orgID, repoID, "143/abc123/changes", "abc1234567890abcdef1234567890abcdef12345",
				"", "", string(models.PreviewSourceTypePullRequest), "owner/repo#42@abc1234567890abcdef1234567890abcdef12345", "https://github.com/owner/repo/pull/42",
				userID, nil, now,
			),
		)
	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestPreviewLinkColumns).AddRow(
				uuid.New(), orgID, targetID, string(models.PreviewLinkTypePullRequest), "github/owner/repo/pull/42",
				&repoID, ptrInt(42), now, now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
			"provider", "worker_node_id", "preview_handle", "primary_service", "port",
			"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
			"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
			"source_workspace_revision", "source_workspace_revision_updated_at", "runtime_workspace_revision", "runtime_workspace_revision_updated_at", "runtime_workspace_revision_source", "preview_holding_container",
		}))

	svc := &PRService{
		previews:              db.NewPreviewStore(mock),
		previewOriginTemplate: "https://{id}.preview.143.dev",
		logger:                zerolog.Nop(),
	}

	url := svc.prPreviewURL(context.Background(), run, repo, "owner", "repo", 42, "143/abc123/changes", "abc1234567890abcdef1234567890abcdef12345", "https://github.com/owner/repo/pull/42")

	require.Equal(t, "https://143.dev/previews/github/owner/repo/pull/42", url, "PR preview URL should point at the stable app launch route")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestFormatPRBody_WithIssueContext(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}
	summary := "Fixed null ptr"
	run := &models.Session{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		ResultSummary: &summary,
	}
	issue := &models.Issue{
		Source:   models.IssueSourceSentry,
		Title:    "NullPointerException in handler",
		Severity: "critical",
	}

	body := svc.formatPRBody(context.Background(), run, issue)
	require.Contains(t, body, "**Issue**: sentry", "should contain issue source")
	require.Contains(t, body, "NullPointerException in handler", "should contain issue title")
	require.Contains(t, body, "(critical)", "should contain severity")
}

func TestCreatePullRequest_WithDraft(t *testing.T) {
	t.Parallel()

	var capturedPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedPayload)
		require.NoError(t, err)
		w.WriteHeader(http.StatusCreated)
		err = json.NewEncoder(w).Encode(map[string]any{
			"number":   99,
			"html_url": "https://github.com/owner/repo/pull/99",
		})
		require.NoError(t, err)
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	num, url, err := svc.createPullRequest(context.Background(), "token", "owner", "repo", "title", "body", "head", "main", withDraft(true))
	require.NoError(t, err)
	require.Equal(t, 99, num)
	require.Equal(t, "https://github.com/owner/repo/pull/99", url)
	require.Equal(t, true, capturedPayload["draft"], "should set draft=true in payload")
}

func TestSyncSessionTitle_UpdatesExistingPullRequest(t *testing.T) {
	t.Parallel()

	mock := newMockPool(t)
	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	prID := uuid.New()
	updatedTitle := "Updated session title"
	body := "body"

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
		)

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestRepoColumns).
				AddRow(uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
					"https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now),
		)

	mock.ExpectExec("UPDATE pull_requests SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "should create GitHub service with test private key")
	tokenSvc.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/app/installations/123/access_tokens":
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token"}`)),
					Header:     make(http.Header),
				}, nil
			case "/repos/acme/repo/pulls/42":
				require.Equal(t, http.MethodPatch, req.Method, "sync should PATCH the existing PR")
				var body map[string]any
				err := json.NewDecoder(req.Body).Decode(&body)
				require.NoError(t, err, "sync should send valid JSON")
				require.Equal(t, updatedTitle, body["title"], "sync should send the updated title to GitHub")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"number":42}`)),
					Header:     make(http.Header),
				}, nil
			default:
				return nil, fmt.Errorf("unexpected request path %s", req.URL.Path)
			}
		}),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		httpClient:    tokenSvc.httpClient,
		logger:        zerolog.Nop(),
	}

	err = svc.SyncSessionTitle(context.Background(), &models.Session{
		ID:    sessionID,
		OrgID: orgID,
		Title: &updatedTitle,
	})
	require.NoError(t, err, "syncing the session title should succeed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSyncSessionTitle_UpdatesExistingPullRequestForDisconnectedRepo(t *testing.T) {
	t.Parallel()

	mock := newMockPool(t)
	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	prID := uuid.New()
	updatedTitle := "Updated session title"
	body := "body"

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
		)

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestRepoColumns).
				AddRow(uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
					"https://github.com/acme/repo.git", int64(123), "disconnected", nil, nil, []byte(`{}`), now, now),
		)

	mock.ExpectExec("UPDATE pull_requests SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "should create GitHub service with test private key")
	tokenSvc.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/app/installations/123/access_tokens":
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token"}`)),
					Header:     make(http.Header),
				}, nil
			case "/repos/acme/repo/pulls/42":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"number":42}`)),
					Header:     make(http.Header),
				}, nil
			default:
				return nil, fmt.Errorf("unexpected request path %s", req.URL.Path)
			}
		}),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		httpClient:    tokenSvc.httpClient,
		logger:        zerolog.Nop(),
	}

	err = svc.SyncSessionTitle(context.Background(), &models.Session{
		ID:    sessionID,
		OrgID: orgID,
		Title: &updatedTitle,
	})
	require.NoError(t, err, "syncing the session title should succeed for disconnected repos")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSyncSessionTitle_UsesEditedSessionTitleForLinearIssue(t *testing.T) {
	t.Parallel()

	mock := newMockPool(t)
	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	prID := uuid.New()
	updatedTitle := "Updated session title"
	body := "body"

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(newPRTestRowWithTitle(prID, &sessionID, orgID, "acme/repo", now, &body, "ENG-123: Old PR title")...),
		)

	mock.ExpectQuery("SELECT .+ FROM issues").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestIssueColumns).
				AddRow(issueID, orgID, "ENG-123", "linear", nil, nil,
					"Original Linear title", nil, json.RawMessage(`{}`), "open", now, now,
					1, 1, "high", []string{"bug"}, "fp", now, now, nil),
		)

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestRepoColumns).
				AddRow(uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
					"https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now),
		)

	mock.ExpectExec("UPDATE pull_requests SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
	require.NoError(t, err, "should create GitHub service with test private key")
	tokenSvc.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/app/installations/123/access_tokens":
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token"}`)),
					Header:     make(http.Header),
				}, nil
			case "/repos/acme/repo/pulls/42":
				require.Equal(t, http.MethodPatch, req.Method, "sync should PATCH the existing PR")
				var body map[string]any
				err := json.NewDecoder(req.Body).Decode(&body)
				require.NoError(t, err, "sync should send valid JSON")
				require.Equal(t, "[ENG-123] Updated session title", body["title"], "sync should keep the Linear prefix and use the edited session title")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"number":42}`)),
					Header:     make(http.Header),
				}, nil
			default:
				return nil, fmt.Errorf("unexpected request path %s", req.URL.Path)
			}
		}),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		pullRequests:  db.NewPullRequestStore(mock),
		repos:         db.NewRepositoryStore(mock),
		issues:        db.NewIssueStore(mock),
		httpClient:    tokenSvc.httpClient,
		logger:        zerolog.Nop(),
	}

	err = svc.SyncSessionTitle(context.Background(), &models.Session{
		ID:             sessionID,
		OrgID:          orgID,
		PrimaryIssueID: &issueID,
		Title:          &updatedTitle,
	})
	require.NoError(t, err, "syncing the session title should succeed for Linear issues")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSyncSessionTitle_NoOpAndErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when session has no usable edited title", func(t *testing.T) {
		t.Parallel()

		title := "   "
		svc := &PRService{}

		err := svc.SyncSessionTitle(context.Background(), nil)
		require.NoError(t, err, "nil session should be ignored")

		err = svc.SyncSessionTitle(context.Background(), &models.Session{Title: nil})
		require.NoError(t, err, "session with nil title should be ignored")

		err = svc.SyncSessionTitle(context.Background(), &models.Session{Title: &title})
		require.NoError(t, err, "session with blank title should be ignored")
	})

	t.Run("returns error when required dependencies are missing", func(t *testing.T) {
		t.Parallel()

		title := "Updated session title"
		err := (&PRService{}).SyncSessionTitle(context.Background(), &models.Session{Title: &title})
		require.Error(t, err, "missing dependencies should return an error")
		require.Contains(t, err.Error(), "title sync dependencies not configured", "error should explain the missing sync dependencies")
	})

	t.Run("returns nil when no pull request exists", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		title := "Updated session title"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(handlerPRColumns))

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:        sessionID,
			OrgID:     orgID,
			Title:     &title,
			CreatedAt: now,
		})
		require.NoError(t, err, "missing pull request should be treated as a no-op")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns nil when formatted title becomes empty", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		prID := uuid.New()
		emptyTitle := "\"...\""
		body := "body"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(handlerPRColumns).
					AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
			)

		mock.ExpectQuery("SELECT .+ FROM issues").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(prTestIssueColumns).
					AddRow(issueID, orgID, "", "linear", nil, nil,
						"", nil, json.RawMessage(`{}`), "open", now, now,
						1, 1, "high", []string{"bug"}, "fp", now, now, nil),
			)

		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(prTestRepoColumns).
					AddRow(uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
						"https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now),
			)

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			issues:        db.NewIssueStore(mock),
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:             sessionID,
			OrgID:          orgID,
			PrimaryIssueID: &issueID,
			Title:          &emptyTitle,
		})
		require.NoError(t, err, "empty formatted title should skip the sync")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns wrapped error when pull request lookup fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		orgID := uuid.New()
		sessionID := uuid.New()
		title := "Updated session title"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:    sessionID,
			OrgID: orgID,
			Title: &title,
		})
		require.Error(t, err, "pull request lookup failures should be returned")
		require.Contains(t, err.Error(), "load pull request", "error should preserve the pull request lookup context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("continues when issue lookup fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		prID := uuid.New()
		title := "Updated session title"
		body := "body"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(handlerPRColumns).
					AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
			)

		mock.ExpectQuery("SELECT .+ FROM issues").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("issue lookup failed"))

		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(prTestRepoColumns).
					AddRow(uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
						"https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now),
			)

		mock.ExpectExec("UPDATE pull_requests SET title").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")
		tokenSvc.httpClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/app/installations/123/access_tokens":
					return &http.Response{
						StatusCode: http.StatusCreated,
						Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token"}`)),
						Header:     make(http.Header),
					}, nil
				case "/repos/acme/repo/pulls/42":
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"number":42}`)),
						Header:     make(http.Header),
					}, nil
				default:
					return nil, fmt.Errorf("unexpected request path %s", req.URL.Path)
				}
			}),
		}

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			issues:        db.NewIssueStore(mock),
			httpClient:    tokenSvc.httpClient,
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:             sessionID,
			OrgID:          orgID,
			PrimaryIssueID: &issueID,
			Title:          &title,
		})
		require.NoError(t, err, "issue lookup failures should only warn and continue")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns wrapped error when repository lookup fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		prID := uuid.New()
		title := "Updated session title"
		body := "body"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(handlerPRColumns).
					AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
			)

		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("repo lookup failed"))

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:    sessionID,
			OrgID: orgID,
			Title: &title,
		})
		require.Error(t, err, "repository lookup failures should be returned")
		require.Contains(t, err.Error(), "get repository", "error should preserve the repository lookup context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns wrapped error when installation token lookup fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		prID := uuid.New()
		title := "Updated session title"
		body := "body"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(handlerPRColumns).
					AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
			)

		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(prTestRepoColumns).
					AddRow(uuid.New(), orgID, uuid.Nil, int64(1001), "acme/repo", "main", false, nil, nil,
						"https://github.com/acme/repo.git", int64(0), "active", nil, nil, []byte(`{}`), now, now),
			)

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:    sessionID,
			OrgID: orgID,
			Title: &title,
		})
		require.Error(t, err, "token lookup failures should be returned")
		require.Contains(t, err.Error(), "get installation token", "error should preserve the installation token context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns wrapped error when GitHub title update fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		prID := uuid.New()
		title := "Updated session title"
		body := "body"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(handlerPRColumns).
					AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
			)

		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(prTestRepoColumns).
					AddRow(uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
						"https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now),
			)

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")
		tokenSvc.httpClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/app/installations/123/access_tokens":
					return &http.Response{
						StatusCode: http.StatusCreated,
						Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token"}`)),
						Header:     make(http.Header),
					}, nil
				case "/repos/acme/repo/pulls/42":
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Body:       io.NopCloser(strings.NewReader(`{"message":"boom"}`)),
						Header:     make(http.Header),
					}, nil
				default:
					return nil, fmt.Errorf("unexpected request path %s", req.URL.Path)
				}
			}),
		}

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			httpClient:    tokenSvc.httpClient,
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:    sessionID,
			OrgID: orgID,
			Title: &title,
		})
		require.Error(t, err, "GitHub update failures should be returned")
		require.Contains(t, err.Error(), "update pull request title", "error should preserve the GitHub update context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns wrapped error when stored title update fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockPool(t)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		prID := uuid.New()
		title := "Updated session title"
		body := "body"

		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(handlerPRColumns).
					AddRow(newPRTestRow(prID, &sessionID, orgID, "acme/repo", now, &body)...),
			)

		mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id =").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(prTestRepoColumns).
					AddRow(uuid.New(), orgID, uuid.New(), int64(1001), "acme/repo", "main", false, nil, nil,
						"https://github.com/acme/repo.git", int64(123), "active", nil, nil, []byte(`{}`), now, now),
			)

		mock.ExpectExec("UPDATE pull_requests SET title").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("store update failed"))

		tokenSvc, err := NewService(143, testPrivateKeyPEM(t))
		require.NoError(t, err, "should create GitHub service with test private key")
		tokenSvc.httpClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/app/installations/123/access_tokens":
					return &http.Response{
						StatusCode: http.StatusCreated,
						Body:       io.NopCloser(strings.NewReader(`{"token":"installation-token"}`)),
						Header:     make(http.Header),
					}, nil
				case "/repos/acme/repo/pulls/42":
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"number":42}`)),
						Header:     make(http.Header),
					}, nil
				default:
					return nil, fmt.Errorf("unexpected request path %s", req.URL.Path)
				}
			}),
		}

		svc := &PRService{
			tokenProvider: tokenSvc,
			pullRequests:  db.NewPullRequestStore(mock),
			repos:         db.NewRepositoryStore(mock),
			httpClient:    tokenSvc.httpClient,
			logger:        zerolog.Nop(),
		}

		err = svc.SyncSessionTitle(context.Background(), &models.Session{
			ID:    sessionID,
			OrgID: orgID,
			Title: &title,
		})
		require.Error(t, err, "stored title update failures should be returned")
		require.Contains(t, err.Error(), "store pull request title", "error should preserve the stored title context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestPRTemplatePaths(t *testing.T) {
	t.Parallel()

	// Verify the template paths list contains the most common locations.
	require.Contains(t, prTemplatePaths, ".github/pull_request_template.md")
	require.Contains(t, prTemplatePaths, ".github/PULL_REQUEST_TEMPLATE.md")
	require.GreaterOrEqual(t, len(prTemplatePaths), 5, "should check at least 5 conventional paths")
}

func TestFirstLine_ReturnsFullLine(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 100)
	result := firstLine(long)
	require.Len(t, result, 100, "firstLine should return the full first non-empty line")
}

func TestNormalizePRTitleCandidate_TruncatesLongTitle(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 140)
	result := normalizePRTitleCandidate(long)
	require.Len(t, result, 120, "normalizePRTitleCandidate should cap PR titles at 120 chars")
}

func TestIssueWithLinearHumanKey_ReturnsCopyWithHumanKey(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	humanKey := "VIR-75"
	original := &models.Issue{
		Source:     models.IssueSourceLinear,
		ExternalID: "321100d2-6427-4026-b163-625d953798a6",
	}
	links := []models.SessionIssueLink{{
		Role:        models.SessionIssueLinkRolePrimary,
		IssueSource: &source,
		ExternalID:  &humanKey,
	}}
	got := issueWithLinearHumanKey(original, links)
	require.Equal(t, "VIR-75", got.ExternalID, "returned issue should carry the human Linear key from the primary link")
	require.Equal(t, "321100d2-6427-4026-b163-625d953798a6", original.ExternalID, "original issue's canonical UUID must be preserved — Linear API writes still need it")
	require.NotSame(t, original, got, "should return a fresh issue rather than mutating the input when a rewrite happens")
}

func TestIssueWithLinearHumanKey_Idempotent(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	humanKey := "VIR-75"
	links := []models.SessionIssueLink{{
		Role:        models.SessionIssueLinkRolePrimary,
		IssueSource: &source,
		ExternalID:  &humanKey,
	}}
	first := issueWithLinearHumanKey(&models.Issue{
		Source:     models.IssueSourceLinear,
		ExternalID: "321100d2-6427-4026-b163-625d953798a6",
	}, links)
	second := issueWithLinearHumanKey(first, links)
	require.Same(t, first, second, "calling on a result that already carries the human key should short-circuit without allocating")
	require.Equal(t, "VIR-75", second.ExternalID)
}

func TestIssueWithLinearHumanKey_ReturnsInputWhenLinkAlsoUUID(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	uuidStr := "321100d2-6427-4026-b163-625d953798a6"
	original := &models.Issue{
		Source:     models.IssueSourceLinear,
		ExternalID: uuidStr,
	}
	links := []models.SessionIssueLink{{
		Role:        models.SessionIssueLinkRolePrimary,
		IssueSource: &source,
		ExternalID:  &uuidStr,
	}}
	got := issueWithLinearHumanKey(original, links)
	require.Same(t, original, got, "should return the original pointer unchanged when no link carries the human key (provider_state.identifier not yet written)")
}

func TestIssueWithLinearHumanKey_SkipsRelatedLinks(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	relatedKey := "VIR-99"
	original := &models.Issue{
		Source:     models.IssueSourceLinear,
		ExternalID: "321100d2-6427-4026-b163-625d953798a6",
	}
	links := []models.SessionIssueLink{{
		Role:        models.SessionIssueLinkRoleRelated,
		IssueSource: &source,
		ExternalID:  &relatedKey,
	}}
	got := issueWithLinearHumanKey(original, links)
	require.Same(t, original, got, "related links must not seed the primary identifier — only the primary link's key belongs in the title prefix anchor position")
}

func TestIssueWithLinearHumanKey_NoOpForNonLinearIssue(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	humanKey := "VIR-75"
	original := &models.Issue{
		Source:     models.IssueSourceSentry,
		ExternalID: "sentry-event-1",
	}
	links := []models.SessionIssueLink{{
		Role:        models.SessionIssueLinkRolePrimary,
		IssueSource: &source,
		ExternalID:  &humanKey,
	}}
	got := issueWithLinearHumanKey(original, links)
	require.Same(t, original, got, "non-Linear issues must not be touched")
}

func TestIssueWithLinearHumanKey_NilIssueIsSafe(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() {
		got := issueWithLinearHumanKey(nil, nil)
		require.Nil(t, got)
	})
}

func TestApplyLinearKeyPrefixes_SinglePrimary(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id := "ACS-1"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{{
			Role:        models.SessionIssueLinkRolePrimary,
			IssueSource: &source,
			ExternalID:  &id,
		}},
	}
	got := applyLinearKeyPrefixes(session, "Fix null pointer", nil)
	require.Equal(t, "[ACS-1] Fix null pointer", got)
}

func TestApplyLinearKeyPrefixes_MultipleOrderedPrimaryFirst(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id1, id2 := "ACS-1234", "ACS-1277"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{
			{Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id1},
			{Role: models.SessionIssueLinkRoleRelated, IssueSource: &source, ExternalID: &id2, Position: 1},
		},
	}
	got := applyLinearKeyPrefixes(session, "Add OAuth callback handler", nil)
	require.Equal(t, "[ACS-1234] [ACS-1277] Add OAuth callback handler", got)
}

func TestApplyLinearKeyPrefixes_PreservesConventionalCommit(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id1, id2 := "ACS-1234", "ACS-1277"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{
			{Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id1},
			{Role: models.SessionIssueLinkRoleRelated, IssueSource: &source, ExternalID: &id2, Position: 1},
		},
	}
	got := applyLinearKeyPrefixes(session, "feat: Add OAuth callback handler", nil)
	require.Equal(t, "feat: [ACS-1234] [ACS-1277] Add OAuth callback handler", got)
}

func TestApplyLinearKeyPrefixes_DoesNotDoublePrefix(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id := "ACS-1"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{{
			Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id,
		}},
	}
	// Title already has the bracket prefix from a prior resync — strip,
	// then re-prefix.
	got := applyLinearKeyPrefixes(session, "[ACS-1] feat: Add OAuth callback handler", nil)
	require.Equal(t, "feat: [ACS-1] Add OAuth callback handler", got)
}

func TestApplyLinearKeyPrefixes_NoLinkedIssuesIsPassthrough(t *testing.T) {
	t.Parallel()
	got := applyLinearKeyPrefixes(&models.Session{}, "fix: something", nil)
	require.Equal(t, "fix: something", got)
}

func TestApplyLinearKeyPrefixes_SkipsAlreadyEmbeddedKeys(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id := "ACS-1"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{{
			Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id,
		}},
	}
	// User-typed title contains the key already; we shouldn't double up.
	got := applyLinearKeyPrefixes(session, "fix something for ACS-1 specifically", nil)
	require.Equal(t, "fix something for ACS-1 specifically", got)
}

// TestApplyLinearKeyPrefixes_SkipsEmbeddedKeysCaseInsensitively pins the
// case-insensitive dedup behavior. A user who typed "acs-1" in their commit
// subject still embedded the same Linear reference; double-prefixing
// `[ACS-1] feat: ... acs-1 ...` would land both casings in one title.
func TestApplyLinearKeyPrefixes_SkipsEmbeddedKeysCaseInsensitively(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id := "ACS-1"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{{
			Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id,
		}},
	}
	got := applyLinearKeyPrefixes(session, "fix something for acs-1 specifically", nil)
	require.Equal(t, "fix something for acs-1 specifically", got, "lowercase embed of the canonical key must be treated as already-present")
}

func TestStripLeadingBracketPrefixes(t *testing.T) {
	t.Parallel()
	got := stripLeadingBracketPrefixes("[ACS-1] [ACS-2] feat: x")
	require.Equal(t, "feat: x", got)
	got = stripLeadingBracketPrefixes("feat: x")
	require.Equal(t, "feat: x", got)
}

// TestApplyLinearKeyPrefixes_DropsRemovedLinkOnResync locks the contract
// that title resync drops a Linear key when the underlying link was
// removed between turns. design 62 §"Title resync" mandates resync from
// the *current* linked-issue set, not the title's history.
func TestApplyLinearKeyPrefixes_DropsRemovedLinkOnResync(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id := "ACS-1234"
	// Session now has only one link (ACS-1234). The title still carries
	// the old [ACS-1277] prefix from a prior resync.
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{{
			Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id,
		}},
	}
	got := applyLinearKeyPrefixes(session, "[ACS-1234] [ACS-1277] feat: Add OAuth callback handler", nil)
	// ACS-1277 should be gone — it's no longer linked.
	require.Equal(t, "feat: [ACS-1234] Add OAuth callback handler", got)
}

// TestApplyLinearKeyPrefixes_ReordersOnPrimaryChange covers the case where
// the primary issue swaps between resyncs — e.g. a session originally
// linked to ACS-1 has its primary reassigned to ACS-2 mid-flight. The
// previous title's `[ACS-1] [ACS-2]` ordering must rebuild to
// `[ACS-2] [ACS-1]` so the PR's most prominent key matches the current
// primary.
func TestApplyLinearKeyPrefixes_ReordersOnPrimaryChange(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	primaryKey := "ACS-2"
	relatedKey := "ACS-1"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{
			{Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &primaryKey},
			{Role: models.SessionIssueLinkRoleRelated, IssueSource: &source, ExternalID: &relatedKey},
		},
	}
	got := applyLinearKeyPrefixes(session, "[ACS-1] [ACS-2] feat: x", nil)
	require.Equal(t, "feat: [ACS-2] [ACS-1] x", got, "primary must lead even when prior title had inverse order")
}

// TestApplyLinearKeyPrefixes_PathologicalPrefixesAreCapped verifies the
// 20-iteration safety cap on stripLeadingBracketPrefixes — a malformed or
// adversarial title with absurd nesting must not lock up the PR pipeline.
// On input far past the cap, the strip loop intentionally bails with
// residual prefixes (a malformed title is malformed; we just refuse to
// spin). The contract under test is bounded time, not perfect cleanup.
func TestApplyLinearKeyPrefixes_PathologicalPrefixesAreCapped(t *testing.T) {
	t.Parallel()
	source := models.IssueSourceLinear
	id := "ACS-1"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{{
			Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id,
		}},
	}
	// 50 stacked prefixes — well past the 20-iteration cap.
	pathological := strings.Repeat("[ACS-1] ", 50) + "feat: x"
	done := make(chan string, 1)
	go func() { done <- applyLinearKeyPrefixes(session, pathological, nil) }()
	select {
	case got := <-done:
		require.NotEmpty(t, got, "should return a non-empty title on pathological input")
		require.Contains(t, got, "[ACS-1]", "result should still carry the linked key")
	case <-time.After(2 * time.Second):
		t.Fatal("applyLinearKeyPrefixes did not return within 2s on pathological input — strip-loop cap may be missing")
	}
}

func TestApplyLinearKeyPrefixes_OverlongPrefixSetDoesNotPanic(t *testing.T) {
	t.Parallel()

	source := models.IssueSourceLinear
	links := make([]models.SessionIssueLink, 0, 30)
	for i := 1; i <= 30; i++ {
		id := fmt.Sprintf("ACS-%d", i)
		role := models.SessionIssueLinkRoleRelated
		if i == 1 {
			role = models.SessionIssueLinkRolePrimary
		}
		links = append(links, models.SessionIssueLink{
			Role:        role,
			IssueSource: &source,
			ExternalID:  &id,
			Position:    i - 1,
		})
	}
	session := &models.Session{LinkedIssues: links}

	require.NotPanics(t, func() {
		got := applyLinearKeyPrefixes(session, "feat: x", nil)
		require.NotEmpty(t, got, "overlong Linear prefixes should still produce a PR title")
		require.LessOrEqual(t, len(got), maxPRTitleLen, "overlong Linear prefixes should be clamped to the GitHub title budget")
		require.Contains(t, got, "[ACS-1]", "overlong Linear prefixes should keep the primary identifier")
		// Subject must survive: with the half-budget cap on prefixes the
		// descriptive title body keeps room. The conventional-commit path
		// places brackets between `feat: ` and the rest, so check for both
		// landmarks separately.
		require.True(t, strings.HasPrefix(got, "feat: "), "conventional commit prefix must be preserved: %q", got)
		require.True(t, strings.HasSuffix(got, " x"), "subject must remain at the tail: %q", got)
		// Sanity: at least one related identifier must be dropped — the
		// 30-link input cannot fit inside a 120-char title with budget left
		// for the subject.
		require.NotContains(t, got, "[ACS-30]", "tail identifiers must be dropped under the prefix-budget cap")
	}, "overlong Linear prefix sets should not panic when the subject budget is exhausted")
}

func TestApplyLinearKeyPrefixes_LongConventionalScopeReservesSubject(t *testing.T) {
	t.Parallel()

	source := models.IssueSourceLinear
	id := "ACS-1"
	session := &models.Session{
		LinkedIssues: []models.SessionIssueLink{{
			Role: models.SessionIssueLinkRolePrimary, IssueSource: &source, ExternalID: &id,
		}},
	}
	// A conventional commit scope long enough that conv + primary brackets
	// alone fill most of the title budget. Without the subject-budget guard,
	// secondaries would still be appended and the descriptive subject would
	// be silently dropped.
	longConv := "feat(" + strings.Repeat("a", 80) + "): "
	got := applyLinearKeyPrefixes(session, longConv+"add new endpoint", nil)
	require.LessOrEqual(t, len(got), maxPRTitleLen, "title must respect maxPRTitleLen even with a long scope")
	require.Contains(t, got, "[ACS-1]", "primary identifier must always survive")
	require.True(t, strings.HasPrefix(got, "feat("), "conventional commit prefix must be preserved")
}

func TestFormatBranchName_ResultSummaryFallback(t *testing.T) {
	t.Parallel()

	summary := "Fixed the auth middleware"
	session := &models.Session{
		ID:            uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"),
		ResultSummary: &summary,
	}
	// When issue is nil and title is nil, branch name uses "changes" fallback
	// because ResultSummary is not used for branch names.
	result := formatBranchName(session, nil)
	require.Equal(t, "143/abcdef01/changes", result)
}

func TestPRServiceSetters(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()

	t.Run("SetUserCredentialStore", func(t *testing.T) {
		t.Parallel()
		svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, logger)
		svc.SetUserCredentialStore(nil)
		require.Nil(t, svc.userCredentials)
	})

	t.Run("SetLLMClient", func(t *testing.T) {
		t.Parallel()
		svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, logger)
		svc.SetLLMClient(nil)
		require.Nil(t, svc.llmClient)
	})

	t.Run("SetUserStore", func(t *testing.T) {
		t.Parallel()
		svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, logger)
		svc.SetUserStore(nil)
		require.Nil(t, svc.users)
	})

	t.Run("SetOrgStore", func(t *testing.T) {
		t.Parallel()
		svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, logger)
		svc.SetOrgStore(nil)
		require.Nil(t, svc.orgs)
	})

	t.Run("SetPRTemplateStore", func(t *testing.T) {
		t.Parallel()
		svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, logger)
		svc.SetPRTemplateStore(nil)
		require.Nil(t, svc.prTemplates)
	})

	t.Run("SetSandboxPushDeps", func(t *testing.T) {
		t.Parallel()
		svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, logger)
		svc.SetSandboxPushDeps(nil, nil)
		require.Nil(t, svc.SandboxProvider(), "sandbox push deps setter should update the sandbox provider")
		require.Nil(t, svc.SnapshotStore(), "sandbox push deps setter should update the snapshot store")
	})
}

func TestCreatePR_ReturnsExistingPRBeforeCheckingSandboxDeps(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	body := "body"

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestPullRequestColumns).AddRow(
				newPRTestRow(uuid.New(), &sessionID, orgID, "testorg/testrepo", now, &body)...,
			),
		)

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.Nop(),
	}

	pr, err := svc.CreatePR(context.Background(), &models.Session{ID: sessionID, OrgID: orgID})
	require.NoError(t, err, "CreatePR should return an existing PR before checking sandbox push deps")
	require.Equal(t, 42, pr.GitHubPRNumber, "CreatePR should return the existing PR row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPushChangesToPR_ReturnsErrNoPullRequestWhenSessionHasNoPR(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()

	// PR lookup returns ErrNoRows — no PR row exists for this session.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.Nop(),
	}

	_, err = svc.PushChangesToPR(context.Background(), &models.Session{ID: sessionID, OrgID: orgID})
	require.ErrorIs(t, err, ErrNoPullRequest, "PushChangesToPR should return ErrNoPullRequest when no PR row exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPushChangesToPR_ReturnsErrPRClosedForClosedOrMergedPR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
	}{
		{name: "merged PR", status: "merged"},
		{name: "closed PR", status: "closed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool")
			defer mock.Close()

			now := time.Now()
			orgID := uuid.New()
			sessionID := uuid.New()
			prID := uuid.New()
			body := "body"

			row := newPRTestRow(prID, &sessionID, orgID, "testorg/testrepo", now, &body)
			row[8] = tt.status // status column index in prTestPullRequestColumns
			mock.ExpectQuery("SELECT .+ FROM pull_requests").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))

			svc := &PRService{
				pullRequests: db.NewPullRequestStore(mock),
				logger:       zerolog.Nop(),
			}

			_, err = svc.PushChangesToPR(context.Background(), &models.Session{ID: sessionID, OrgID: orgID})
			require.ErrorIs(t, err, ErrPRClosed, "PushChangesToPR should refuse to push to a non-open PR")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPushChangesToPR_ReturnsErrSnapshotNotCapturedWhenSnapshotKeyMissing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	prID := uuid.New()
	body := "body"

	prRow := newPRTestRow(prID, &sessionID, orgID, "testorg/testrepo", now, &body)
	// head_ref must be set so we don't short-circuit on ErrLegacyPRMissingHeadRef
	// before reaching the snapshot check this test exercises.
	headRef := "143/session/changes"
	prRow[13] = &headRef
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestPullRequestColumns).AddRow(prRow...),
		)

	svc := &PRService{
		pullRequests:    db.NewPullRequestStore(mock),
		sandboxProvider: &prTestSandboxProvider{},
		snapshots:       &prTestSnapshotStore{},
		logger:          zerolog.Nop(),
	}

	// SnapshotKey is nil — push must refuse to hydrate.
	_, err = svc.PushChangesToPR(context.Background(), &models.Session{ID: sessionID, OrgID: orgID})
	require.ErrorIs(t, err, ErrSnapshotNotCaptured, "PushChangesToPR should refuse to push without a captured snapshot")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPushChangesToPR_ReturnsErrSnapshotPendingWhenUploadInFlight(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	prID := uuid.New()
	body := "body"
	snapshotKey := "snapshots/session.tar"
	pendingSnapshotKey := "snapshots/session/post-pr.tar.zst"

	prRow := newPRTestRow(prID, &sessionID, orgID, "testorg/testrepo", now, &body)
	// See TestPushChangesToPR_ReturnsErrSnapshotNotCapturedWhenSnapshotKeyMissing
	// for why head_ref must be set on this fixture.
	headRef := "143/session/changes"
	prRow[13] = &headRef
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestPullRequestColumns).AddRow(prRow...),
		)

	svc := &PRService{
		pullRequests:    db.NewPullRequestStore(mock),
		sandboxProvider: &prTestSandboxProvider{},
		snapshots:       &prTestSnapshotStore{},
		logger:          zerolog.Nop(),
	}

	_, err = svc.PushChangesToPR(context.Background(), &models.Session{
		ID:                 sessionID,
		OrgID:              orgID,
		SnapshotKey:        &snapshotKey,
		PendingSnapshotKey: &pendingSnapshotKey,
	})
	require.ErrorIs(t, err, agent.ErrSnapshotPending, "PushChangesToPR should wait for pending snapshot uploads before hydrating")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPushChangesToPR_EnqueuesHealthSyncAfterSuccessfulPush(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	prID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	body := "body"
	snapshotKey := "snapshots/session.tar"
	headRef := "143/session/changes"
	headSHA := "abc1234567890abcdef1234567890abcdef12345"

	prRow := newPRTestRow(prID, &sessionID, orgID, "owner/repo", now, &body)
	prRow[13] = &headRef
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(prRow...))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestRepoColumns).AddRow(
				repoID, orgID, integrationID, int64(12345), "owner/repo", "main",
				false, nil, nil, "https://github.com/owner/repo.git", int64(99),
				"active", nil, nil, json.RawMessage(`{}`), now, now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestOrganizationColumns).AddRow(
				orgID, "Test Org", json.RawMessage(`{"pr_authorship":"app_only"}`), now, now,
			),
		)
	mock.ExpectExec("UPDATE pull_requests SET head_sha").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      "default",
			"job_type":   "sync_pull_request_state",
			"payload":    pgxmock.AnyArg(),
			"priority":   6,
			"dedupe_key": pgxmock.AnyArg(),
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	provider := &prTestSandboxProvider{
		execStdout:  pushHeadSHASentinel + headSHA + "\n",
		snapshotErr: errors.New("test: skip post-push snapshot"),
	}
	svc := &PRService{
		tokenProvider: &Service{
			cache: map[int64]*cachedToken{
				99: {Token: "app-token", ExpiresAt: time.Now().Add(time.Hour)},
			},
		},
		pullRequests:    db.NewPullRequestStore(mock),
		sessions:        db.NewSessionStore(mock),
		repos:           db.NewRepositoryStore(mock),
		orgs:            db.NewOrganizationStore(mock),
		jobs:            db.NewJobStore(mock),
		sandboxProvider: provider,
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		logger:          zerolog.Nop(),
	}

	pr, err := svc.PushChangesToPR(context.Background(), &models.Session{
		ID:           sessionID,
		OrgID:        orgID,
		RepositoryID: &repoID,
		SnapshotKey:  &snapshotKey,
	})
	require.NoError(t, err, "PushChangesToPR should succeed for a snapshot-backed session with an open PR")
	require.Equal(t, headSHA, *pr.HeadSHA, "PushChangesToPR should return the just-pushed head SHA")
	require.Contains(t, provider.lastExecCmd, "HEAD:refs/heads/'"+headRef+"'", "PushChangesToPR should push to the persisted PR head ref")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// PRs created before migration 107 don't have head_ref persisted. The push
// code refuses to recompute via formatBranchName because the result is
// sensitive to the session's title and Linear identifier — both of which can
// drift after CreatePR runs — so a guess could land the push on a branch the
// PR doesn't track. Surface ErrLegacyPRMissingHeadRef instead and let the
// caller dead-letter / show a "create a new PR" message.
func TestPushChangesToPR_RefusesLegacyPRWithoutHeadRef(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	prID := uuid.New()
	body := "body"
	snapshotKey := "snapshots/session.tar"

	prRow := newPRTestRow(prID, &sessionID, orgID, "owner/repo", now, &body)
	// prRow[13] (head_ref) is nil by default — that's the legacy condition we
	// refuse rather than guess past.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(prRow...))

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		// sandboxProvider/snapshots intentionally nil — the legacy refusal must
		// fire before any sandbox or token work runs.
		logger: zerolog.Nop(),
	}

	_, err = svc.PushChangesToPR(context.Background(), &models.Session{
		ID:          sessionID,
		OrgID:       orgID,
		SnapshotKey: &snapshotKey,
	})
	require.ErrorIs(t, err, ErrLegacyPRMissingHeadRef, "PushChangesToPR should refuse legacy PRs without a persisted head_ref")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met (no sandbox/token work should run)")
}

func TestCreatePR_ReturnsPRLookupErrorBeforeCheckingSandboxDeps(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.Canceled)

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.Nop(),
	}

	_, err = svc.CreatePR(context.Background(), &models.Session{ID: sessionID, OrgID: orgID})
	require.Error(t, err, "CreatePR should surface PR lookup failures before attempting a push")
	require.ErrorIs(t, err, context.Canceled, "CreatePR should preserve the underlying PR lookup error")
	require.NotContains(t, err.Error(), "sandbox push dependencies not configured", "CreatePR should not mask PR lookup failures with a config error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCreateOrGetPullRequest_ReusesExistingGitHubPROnConflict(t *testing.T) {
	t.Parallel()

	var listQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/pulls":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, err := w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for owner:143/abc123/changes."}]}`))
			require.NoError(t, err, "mock server should return an existing-PR validation error")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls":
			listQuery = r.URL.RawQuery
			err := json.NewEncoder(w).Encode([]map[string]any{
				{
					"number":   42,
					"html_url": "https://github.com/owner/repo/pull/42",
				},
			})
			require.NoError(t, err, "mock server should encode the existing pull request")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	prNum, prURL, err := svc.createOrGetPullRequest(
		context.Background(),
		"token",
		"owner",
		"repo",
		"title",
		"body",
		"143/abc123/changes",
		"main",
	)
	require.NoError(t, err, "createOrGetPullRequest should recover the existing GitHub PR on conflict")
	require.Equal(t, 42, prNum, "createOrGetPullRequest should return the existing GitHub PR number")
	require.Equal(t, "https://github.com/owner/repo/pull/42", prURL, "createOrGetPullRequest should return the existing GitHub PR URL")
	require.Equal(t, "head=owner%3A143%2Fabc123%2Fchanges&state=open", listQuery, "createOrGetPullRequest should look up the conflicting branch head")
}

func TestShellQuote(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		in, want string
	}{
		{"plain", "main", `'main'`},
		{"with space", "branch name", `'branch name'`},
		{"empty", "", `''`},
		{"single quote", "it's", `'it'\''s'`},
		{"multiple quotes", "a'b'c", `'a'\''b'\''c'`},
		{"newline", "line1\nline2", "'line1\nline2'"},
		{"shell metachars", "$(whoami); rm -rf /", `'$(whoami); rm -rf /'`},
		{"backslash", `c:\foo`, `'c:\foo'`},
		{"double quote", `a"b`, `'a"b'`},
		{"only quote", "'", `''\'''`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, c.want, shellQuote(c.in))
		})
	}
}

func TestBuildPushScript_Structure(t *testing.T) {
	t.Parallel()

	script := buildPushScript(
		"/home/user/repo",
		"/home/user/.143-pr-commit-msg",
		"Test User",
		"test@example.com",
		"143/abc123/fix-typo",
		"https://github.com/owner/repo.git",
	)

	// Only the commit-msg file is created by the script; auth comes from
	// the per-push credential socket, so there's nothing else to clean up.
	require.Contains(t, script, "cleanup() { rm -f '/home/user/.143-pr-commit-msg'; }")
	require.Contains(t, script, "trap cleanup EXIT")

	// Author identity is set via git config with quoted values.
	require.Contains(t, script, "git config user.name 'Test User'")
	require.Contains(t, script, "git config user.email 'test@example.com'")

	// WorkDir is cd'd into.
	require.Contains(t, script, "cd '/home/user/repo'")

	// Hydrated snapshots may have been captured while the repo was on a stale
	// branch. The PR push path must normalize the current branch before the
	// pre-push guard runs.
	require.Contains(t, script, "git checkout -B '143/abc123/fix-typo'")

	// Commit message is read from file (not argv).
	require.Contains(t, script, "git commit -F '/home/user/.143-pr-commit-msg'")

	// Push uses --force-with-lease keyed on the remote SHA we just observed
	// via ls-remote. Auth flows through credential.helper=!143-tools
	// git-credential talking to the per-push host socket — no userinfo in
	// the URL.
	require.Contains(t, script, "git ls-remote 'https://github.com/owner/repo.git' refs/heads/'143/abc123/fix-typo'")
	require.Contains(t, script, `git push --force-with-lease=refs/heads/'143/abc123/fix-typo':"${remote_sha}" 'https://github.com/owner/repo.git' HEAD:refs/heads/'143/abc123/fix-typo'`)

	// No-changes sentinel exit code is present in the upstream-ancestor branch.
	require.Contains(t, script, "exit 77")

	// Defense in depth: the script must not embed userinfo in the URL or
	// reference any GIT_ASKPASS-style helper. Both auth mechanisms are gone.
	require.NotContains(t, script, "x-access-token")
	require.NotContains(t, script, "GIT_ASKPASS")
	require.NotContains(t, script, "GIT_TERMINAL_PROMPT")
}

func TestBuildPushScript_QuotesHostileBranchName(t *testing.T) {
	t.Parallel()

	// A branch name containing a single quote would break naive quoting;
	// shellQuote's '\'' trick must keep the script well-formed.
	script := buildPushScript(
		"/home/user/repo",
		"/home/user/.143-pr-commit-msg",
		"Bot",
		"bot@example.com",
		"143/abc/it's-fine",
		"https://github.com/o/r.git",
	)
	require.Contains(t, script, `HEAD:refs/heads/'143/abc/it'\''s-fine'`)
	// The branch is interpolated four times now (checkout, ls-remote, lease
	// ref, push ref); each must round-trip through shellQuote so the embedded
	// quote can't break out and corrupt the script.
	require.Contains(t, script, `git checkout -B '143/abc/it'\''s-fine'`)
	require.Contains(t, script, `git ls-remote 'https://github.com/o/r.git' refs/heads/'143/abc/it'\''s-fine'`)
	require.Contains(t, script, `--force-with-lease=refs/heads/'143/abc/it'\''s-fine':"${remote_sha}"`)
}

func TestIsPushRejection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "non-fast-forward",
			in:   "To https://github.com/o/r.git\n ! [rejected]   HEAD -> b (non-fast-forward)\nerror: failed to push some refs",
			want: true,
		},
		{
			name: "stale info from force-with-lease",
			in:   " ! [rejected]   HEAD -> b (stale info)\nerror: failed to push some refs to 'https://github.com/o/r.git'",
			want: true,
		},
		{
			name: "uppercase still matches",
			in:   "REJECTED",
			want: true,
		},
		{
			name: "unrelated network error",
			in:   "fatal: unable to access 'https://github.com/o/r.git/': Could not resolve host",
			want: false,
		},
		{
			name: "empty stderr",
			in:   "",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, c.want, isPushRejection(c.in))
		})
	}
}

func TestFormatBranchName_PrefersSessionWorkingBranch(t *testing.T) {
	t.Parallel()

	workingBranch := "143/abc123/fix-typo"
	session := &models.Session{
		ID:            uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		WorkingBranch: &workingBranch,
	}

	require.Equal(t, workingBranch, formatBranchName(session, &models.Issue{Title: "Ignored"}), "formatBranchName should reuse the session working branch when present")
}

func TestGithubAPIError_IsNoCommitsBetween(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  *GitHubAPIError
		want bool
	}{
		{
			name: "matches 422 no-commits",
			err: &GitHubAPIError{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       []byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"No commits between main and feature"}]}`),
			},
			want: true,
		},
		{
			name: "wrong status",
			err: &GitHubAPIError{
				StatusCode: http.StatusNotFound,
				Body:       []byte(`{"errors":[{"message":"No commits between"}]}`),
			},
			want: false,
		},
		{
			name: "different 422",
			err: &GitHubAPIError{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       []byte(`{"errors":[{"message":"A pull request already exists"}]}`),
			},
			want: false,
		},
		{
			name: "malformed body",
			err: &GitHubAPIError{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       []byte(`not json`),
			},
			want: false,
		},
		{
			name: "nil receiver",
			err:  nil,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, c.want, c.err.IsNoCommitsBetween())
		})
	}
}

func TestGithubAPIError_IsExistingPullRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  *GitHubAPIError
		want bool
	}{
		{
			name: "matches existing-pr conflict",
			err: &GitHubAPIError{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       []byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for owner:branch."}]}`),
			},
			want: true,
		},
		{
			name: "different 422",
			err: &GitHubAPIError{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       []byte(`{"errors":[{"message":"No commits between main and feature"}]}`),
			},
			want: false,
		},
		{
			name: "wrong status",
			err: &GitHubAPIError{
				StatusCode: http.StatusConflict,
				Body:       []byte(`{"errors":[{"message":"A pull request already exists"}]}`),
			},
			want: false,
		},
		{
			name: "malformed body",
			err: &GitHubAPIError{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       []byte(`not json`),
			},
			want: false,
		},
		{
			name: "nil receiver",
			err:  nil,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, c.want, c.err.IsExistingPullRequest(), "IsExistingPullRequest should classify GitHub validation errors correctly")
		})
	}
}

func TestGithubAPIError_HTTPStatus(t *testing.T) {
	t.Parallel()

	var nilErr *GitHubAPIError
	require.Equal(t, 0, nilErr.HTTPStatus(), "HTTPStatus should return 0 for a nil receiver")
	require.Equal(t, http.StatusConflict, (&GitHubAPIError{StatusCode: http.StatusConflict}).HTTPStatus(), "HTTPStatus should expose the wrapped status code")
}

func TestPushSessionBranch(t *testing.T) {
	t.Parallel()

	const fakeHeadSHA = "abc1234567890abcdef1234567890abcdef12345"
	successStdout := pushHeadSHASentinel + fakeHeadSHA + "\n"
	commitMsgPath := pushCommitMsgPath(agent.DefaultSandboxConfig().HomeDir)

	tests := []struct {
		name                 string
		snapshots            *prTestSnapshotStore
		provider             *prTestSandboxProvider
		wantErrIs            error
		wantErrSubstr        string
		wantDestroyCnt       int
		wantHeadSHA          string
		wantSnapshotCaptured bool
		wantSnapshotErr      bool
	}{
		{
			name:           "missing snapshot object maps to unavailable",
			snapshots:      &prTestSnapshotStore{loadErr: storage.ErrSnapshotNotFound},
			provider:       &prTestSandboxProvider{},
			wantErrIs:      ErrSnapshotUnavailable,
			wantDestroyCnt: 1,
		},
		{
			name:           "write commit message failure returns error",
			snapshots:      &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:       &prTestSandboxProvider{writeErrs: map[string]error{commitMsgPath: errors.New("disk full")}},
			wantErrSubstr:  "write commit message to sandbox",
			wantDestroyCnt: 1,
		},
		{
			name:           "exec failure returns error",
			snapshots:      &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:       &prTestSandboxProvider{execErr: errors.New("sandbox exec failed")},
			wantErrSubstr:  "exec push script",
			wantDestroyCnt: 1,
		},
		{
			name:           "no changes sentinel maps to ErrNoChanges",
			snapshots:      &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:       &prTestSandboxProvider{execExit: pushExitNoChanges},
			wantErrIs:      ErrNoChanges,
			wantDestroyCnt: 1,
		},
		{
			name:           "nonzero exit with non-rejection stderr surfaces raw error",
			snapshots:      &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:       &prTestSandboxProvider{execExit: 12, execStderr: "fatal: unable to access remote"},
			wantErrSubstr:  "git push failed (exit 12): fatal: unable to access remote",
			wantDestroyCnt: 1,
		},
		{
			name:           "non-fast-forward rejection maps to ErrPushRejected",
			snapshots:      &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:       &prTestSandboxProvider{execExit: 1, execStderr: "! [rejected]   HEAD -> b (non-fast-forward)\nerror: failed to push some refs"},
			wantErrIs:      ErrPushRejected,
			wantDestroyCnt: 1,
		},
		{
			name:           "stale info from force-with-lease maps to ErrPushRejected",
			snapshots:      &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:       &prTestSandboxProvider{execExit: 1, execStderr: "! [rejected]   HEAD -> b (stale info)"},
			wantErrIs:      ErrPushRejected,
			wantDestroyCnt: 1,
		},
		{
			name:           "missing head sha sentinel returns parse error",
			snapshots:      &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:       &prTestSandboxProvider{execStdout: "no sentinel here\n"},
			wantErrSubstr:  "head sha sentinel",
			wantDestroyCnt: 1,
		},
		{
			name:                 "success returns head sha and captured snapshot",
			snapshots:            &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:             &prTestSandboxProvider{execStdout: successStdout},
			wantDestroyCnt:       1,
			wantHeadSHA:          fakeHeadSHA,
			wantSnapshotCaptured: true,
		},
		{
			name:            "snapshot capture failure does not fail the push",
			snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
			provider:        &prTestSandboxProvider{execStdout: successStdout, snapshotErr: errors.New("snapshot exec died")},
			wantDestroyCnt:  1,
			wantHeadSHA:     fakeHeadSHA,
			wantSnapshotErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auth := &fakeSandboxAuth{socketPath: "/tmp/fake.sock"}
			svc := &PRService{
				sandboxProvider: tt.provider,
				snapshots:       tt.snapshots,
				sandboxAuth:     auth,
				logger:          zerolog.Nop(),
			}
			run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
			repo := &models.Repository{FullName: "owner/repo"}

			result, err := svc.pushSessionBranch(
				context.Background(),
				run,
				repo,
				models.OrgSettings{},
				"snapshots/key.tar",
				"143/abc123/fix",
				"commit message",
				"Test User",
				"test@example.com",
			)
			t.Cleanup(func() {
				if result != nil && result.CapturedSnapshotPath != "" {
					_ = os.Remove(result.CapturedSnapshotPath)
				}
			})

			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs, "pushSessionBranch should return the expected sentinel error")
			} else if tt.wantErrSubstr != "" {
				require.Error(t, err, "pushSessionBranch should return an error")
				require.Contains(t, err.Error(), tt.wantErrSubstr, "pushSessionBranch should include the expected error text")
			} else {
				require.NoError(t, err, "pushSessionBranch should succeed")
				require.NotNil(t, result, "pushSessionBranch should return a non-nil result on success")
				require.Equal(t, []byte("commit message"), tt.provider.writes[commitMsgPath], "pushSessionBranch should write the commit message file under the sandbox home directory")
				require.NotContains(t, tt.provider.lastExecCmd, "GIT_ASKPASS", "pushSessionBranch should auth via the credential socket, not askpass")
				require.NotContains(t, tt.provider.lastExecCmd, "x-access-token", "pushSessionBranch should not embed credentials in the push URL")
				require.Equal(t, tt.wantHeadSHA, result.HeadSHA, "pushSessionBranch should return the parsed HEAD SHA")
				if tt.wantSnapshotCaptured {
					require.NotEmpty(t, result.CapturedSnapshotPath, "pushSessionBranch should spool the post-push snapshot to a temp file")
					require.NoError(t, result.CapturedSnapshotErr, "snapshot capture should succeed when provider.Snapshot returns no error")
				}
				if tt.wantSnapshotErr {
					require.Error(t, result.CapturedSnapshotErr, "snapshot capture failure should be surfaced on the result, not as a function error")
					require.Empty(t, result.CapturedSnapshotPath, "no temp file should remain when snapshot capture fails")
				}
			}

			require.Equal(t, tt.wantDestroyCnt, tt.provider.destroyed, "pushSessionBranch should always destroy the hydrated sandbox")
			// Whatever the outcome, the per-push auth listener must be closed
			// — leaking it would strand the listener until orchestrator shutdown.
			require.Equal(t, 1, auth.listenCount, "pushSessionBranch should open exactly one auth listener")
			require.Equal(t, 1, auth.closeCount, "pushSessionBranch should close the auth listener on every exit path")
		})
	}
}

// fakeSandboxAuth records Listen/Close calls so pushSessionBranch tests can
// assert the per-push listener is opened and closed exactly once across every
// success and failure path.
type fakeSandboxAuth struct {
	socketPath    string
	listenErr     error
	listenCount   int
	closeCount    int
	lastListenKey uuid.UUID
}

func (f *fakeSandboxAuth) Listen(_ context.Context, sessionID uuid.UUID, _ *models.Session, _ *models.Repository, _ models.OrgSettings) (string, error) {
	f.listenCount++
	f.lastListenKey = sessionID
	if f.listenErr != nil {
		return "", f.listenErr
	}
	return f.socketPath, nil
}

func (f *fakeSandboxAuth) Close(_ uuid.UUID) {
	f.closeCount++
}

// TestPushSessionBranch_RetryOnRejection_Succeeds locks in the self-healing
// behavior: if the first push attempt is rejected (race between our
// ls-remote and our push, only failure mode --force-with-lease can
// surface when 143 owns the branch namespace), the service runs the
// idempotent push script a second time, picks up the new remote SHA,
// and succeeds — without surfacing ErrPushRejected.
func TestPushSessionBranch_RetryOnRejection_Succeeds(t *testing.T) {
	t.Parallel()

	const headSHA = "abc1234567890abcdef1234567890abcdef12345"
	provider := &prTestSandboxProvider{
		execSequence: []prTestExecResponse{
			{exit: 1, stderr: "! [rejected]   HEAD -> b (stale info)\nerror: failed to push some refs"},
			{exit: 0, stdout: pushHeadSHASentinel + headSHA + "\n"},
		},
	}
	svc := &PRService{
		sandboxProvider: provider,
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		logger:          zerolog.Nop(),
	}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	repo := &models.Repository{FullName: "owner/repo"}

	result, err := svc.pushSessionBranch(
		context.Background(),
		run,
		repo,
		models.OrgSettings{},
		"snapshots/key.tar",
		"143/abc/fix",
		"commit message",
		"Bot",
		"bot@example.com",
	)
	t.Cleanup(func() {
		if result != nil && result.CapturedSnapshotPath != "" {
			_ = os.Remove(result.CapturedSnapshotPath)
		}
	})

	require.NoError(t, err, "self-heal: a single rejection followed by success must NOT surface ErrPushRejected")
	require.Equal(t, headSHA, result.HeadSHA)
	require.Equal(t, 2, provider.execCallCount, "pushSessionBranch must execute the script exactly twice — once rejected, once retried")
}

// TestPushSessionBranch_RetryOnRejection_PersistentRejection asserts the
// retry is one-shot: a second rejection bubbles up as ErrPushRejected so
// the worker can surface it (instead of looping silently).
func TestPushSessionBranch_RetryOnRejection_PersistentRejection(t *testing.T) {
	t.Parallel()

	rejectStderr := "! [rejected]   HEAD -> b (non-fast-forward)\nerror: failed to push some refs"
	provider := &prTestSandboxProvider{
		execSequence: []prTestExecResponse{
			{exit: 1, stderr: rejectStderr},
			{exit: 1, stderr: rejectStderr},
		},
	}
	svc := &PRService{
		sandboxProvider: provider,
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		logger:          zerolog.Nop(),
	}

	_, err := svc.pushSessionBranch(
		context.Background(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{FullName: "owner/repo"},
		models.OrgSettings{},
		"snapshots/key.tar",
		"143/abc/fix",
		"commit message",
		"Bot",
		"bot@example.com",
	)
	require.ErrorIs(t, err, ErrPushRejected, "persistent rejection must still surface ErrPushRejected")
	require.Equal(t, 2, provider.execCallCount, "retry budget is one — at most two total attempts")
}

// TestPushSessionBranch_NoRetryOnNonRejection asserts the retry is gated
// on isPushRejection: non-rejection failures (e.g. exec errors, network)
// are NOT retried, since they aren't the race --force-with-lease detects.
func TestPushSessionBranch_NoRetryOnNonRejection(t *testing.T) {
	t.Parallel()

	provider := &prTestSandboxProvider{execExit: 1, execStderr: "fatal: unable to access remote"}
	svc := &PRService{
		sandboxProvider: provider,
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		logger:          zerolog.Nop(),
	}

	_, err := svc.pushSessionBranch(
		context.Background(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{FullName: "owner/repo"},
		models.OrgSettings{},
		"snapshots/key.tar",
		"143/abc/fix",
		"commit message",
		"Bot",
		"bot@example.com",
	)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPushRejected)
	require.Equal(t, 1, provider.execCallCount, "non-rejection failure must NOT be retried")
}

// TestPushSessionBranch_AuthSocketWired locks in the regression fix: the
// pr_push sandbox MUST get a credential socket wired before HydrateSandboxFromSnapshot
// runs, otherwise the snapshot's baked-in `credential.helper=!143-tools git-credential`
// will exit non-zero with `_143_AUTH_SOCK is not set` and the push fails.
func TestPushSessionBranch_AuthSocketWired(t *testing.T) {
	t.Parallel()

	provider := &prTestSandboxProvider{execStdout: pushHeadSHASentinel + "abc1234567890abcdef1234567890abcdef12345\n"}
	auth := &fakeSandboxAuth{socketPath: "/host/socket-dir/sock"}
	svc := &PRService{
		sandboxProvider: provider,
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     auth,
		logger:          zerolog.Nop(),
	}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	repo := &models.Repository{FullName: "owner/repo"}

	result, err := svc.pushSessionBranch(
		context.Background(),
		run,
		repo,
		models.OrgSettings{},
		"snapshots/key.tar",
		"143/abc/fix",
		"commit message",
		"Bot",
		"bot@example.com",
	)
	t.Cleanup(func() {
		if result != nil && result.CapturedSnapshotPath != "" {
			_ = os.Remove(result.CapturedSnapshotPath)
		}
	})
	require.NoError(t, err)

	// Listener keyed by a fresh per-push UUID, NOT the session ID — that's
	// what keeps a still-active agent listener (e.g. preview holding the
	// agent container alive) from being yanked.
	require.Equal(t, 1, auth.listenCount)
	require.NotEqual(t, run.ID, auth.lastListenKey, "auth listener must be keyed by a per-push UUID, not the session ID")

	// The sandbox config the provider saw must carry the host socket path
	// + the in-container env var that 143-tools git-credential reads.
	require.Equal(t, "/host/socket-dir/sock", provider.lastConfig.AuthSocketPath)
	require.Equal(t, sandboxauth.SandboxSocketPath, provider.lastConfig.Env[sandboxauth.SocketEnvVar])
	// GitNameEnvVar / GitEmailEnvVar are deliberately absent: they're
	// consumed only by `143-tools git-bootstrap`, which the pr_push sandbox
	// never runs. The push script sets identity directly via `git config`.
	require.NotContains(t, provider.lastConfig.Env, sandboxauth.GitNameEnvVar)
	require.NotContains(t, provider.lastConfig.Env, sandboxauth.GitEmailEnvVar)
}

func TestPushSessionBranch_RequiresSandboxAuth(t *testing.T) {
	t.Parallel()

	svc := &PRService{
		sandboxProvider: &prTestSandboxProvider{},
		snapshots:       &prTestSnapshotStore{},
		logger:          zerolog.Nop(),
	}
	_, err := svc.pushSessionBranch(
		context.Background(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{FullName: "o/r"},
		models.OrgSettings{},
		"k", "b", "m", "n", "e@x",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox auth socket not configured")
}

func TestParsePushHeadSHA(t *testing.T) {
	t.Parallel()

	const fakeSHA = "abc1234567890abcdef1234567890abcdef12345"
	tests := []struct {
		name      string
		stdout    string
		wantSHA   string
		wantErrIs string
	}{
		{
			name:    "single line success",
			stdout:  pushHeadSHASentinel + fakeSHA + "\n",
			wantSHA: fakeSHA,
		},
		{
			name:    "sentinel preceded by git push noise",
			stdout:  "Counting objects: 5, done.\nWriting objects: 100% (5/5), done.\n" + pushHeadSHASentinel + fakeSHA + "\n",
			wantSHA: fakeSHA,
		},
		{
			name:    "trailing whitespace is trimmed",
			stdout:  pushHeadSHASentinel + fakeSHA + "  \r\n",
			wantSHA: fakeSHA,
		},
		{
			name:      "missing sentinel returns error",
			stdout:    "no sentinel here\nanother line\n",
			wantErrIs: "head sha sentinel",
		},
		{
			name:      "empty SHA after sentinel rejected",
			stdout:    pushHeadSHASentinel + "\n",
			wantErrIs: "not a 40-char hex SHA",
		},
		{
			name:      "uppercase SHA rejected",
			stdout:    pushHeadSHASentinel + "ABC1234567890ABCDEF1234567890ABCDEF12345\n",
			wantErrIs: "not a 40-char hex SHA",
		},
		{
			name:      "short SHA rejected",
			stdout:    pushHeadSHASentinel + "abc123\n",
			wantErrIs: "not a 40-char hex SHA",
		},
		{
			name:      "trailing junk after SHA rejected",
			stdout:    pushHeadSHASentinel + fakeSHA + " unexpected\n",
			wantErrIs: "not a 40-char hex SHA",
		},
		{
			name:      "empty stdout returns error",
			stdout:    "",
			wantErrIs: "head sha sentinel",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sha, err := parsePushHeadSHA(tt.stdout)
			if tt.wantErrIs != "" {
				require.Error(t, err, "parsePushHeadSHA should return an error")
				require.Contains(t, err.Error(), tt.wantErrIs, "parsePushHeadSHA error should mention the failure mode")
				return
			}
			require.NoError(t, err, "parsePushHeadSHA should succeed for valid stdout")
			require.Equal(t, tt.wantSHA, sha, "parsePushHeadSHA should return the SHA verbatim from the sentinel line")
		})
	}
}

// blockingSnapshotStore.Save blocks on the saveStarted channel before
// returning. Tests use it to assert that PendingSnapshotKey is set and
// SnapshotKey is unchanged while an upload is in flight, then unblock and
// verify the atomic promotion.
type blockingSnapshotStore struct {
	saveStarted chan struct{}
	saveRelease chan struct{}
	saveErr     error
	saveKey     string
}

func (s *blockingSnapshotStore) Save(_ context.Context, key string, _ io.Reader) error {
	s.saveKey = key
	close(s.saveStarted)
	<-s.saveRelease
	return s.saveErr
}
func (s *blockingSnapshotStore) Load(context.Context, string, io.Writer) error { return nil }
func (s *blockingSnapshotStore) Delete(context.Context, string) error          { return nil }

func TestDispatchPostPRSnapshotUpload_PromotesOnSuccess(t *testing.T) {
	t.Parallel()

	tarFile, err := os.CreateTemp("", "143-pr-snapshot-test-*.tar.zst")
	require.NoError(t, err, "test setup: create temp tar file")
	_, err = tarFile.WriteString("synthetic-tar-content")
	require.NoError(t, err, "test setup: write to temp tar file")
	require.NoError(t, tarFile.Close(), "test setup: close temp tar file")
	tarPath := tarFile.Name()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created without error")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	const newKey = "snapshots/org/session/post-pr.tar.zst"

	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).
			AddRow(int64(2), time.Now().UTC()))

	store := &blockingSnapshotStore{
		saveStarted: make(chan struct{}),
		saveRelease: make(chan struct{}),
	}
	svc := &PRService{
		sessions:  db.NewSessionStore(mock),
		snapshots: store,
		logger:    zerolog.Nop(),
	}

	svc.dispatchPostPRSnapshotUpload(orgID, sessionID, newKey, tarPath, int64(len("synthetic-tar-content")))

	<-store.saveStarted
	require.Equal(t, newKey, store.saveKey, "Save should be invoked with the post-PR snapshot key")

	close(store.saveRelease)
	svc.WaitForPostPRSnapshotUploads()

	_, statErr := os.Stat(tarPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "the temp tar file must be removed once the upload goroutine finishes")
	require.NoError(t, mock.ExpectationsWereMet(), "PromotePendingSnapshot should be the only DB call on the success path")
}

func TestDispatchPostPRSnapshotUpload_ClearsOnSaveFailure(t *testing.T) {
	t.Parallel()

	tarFile, err := os.CreateTemp("", "143-pr-snapshot-test-*.tar.zst")
	require.NoError(t, err, "test setup: create temp tar file")
	require.NoError(t, tarFile.Close(), "test setup: close temp tar file")
	tarPath := tarFile.Name()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created without error")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := &blockingSnapshotStore{
		saveStarted: make(chan struct{}),
		saveRelease: make(chan struct{}),
		saveErr:     errors.New("upload exploded"),
	}
	svc := &PRService{
		sessions:  db.NewSessionStore(mock),
		snapshots: store,
		logger:    zerolog.Nop(),
	}

	svc.dispatchPostPRSnapshotUpload(orgID, sessionID, "snapshots/whatever", tarPath, 0)

	<-store.saveStarted
	close(store.saveRelease)
	svc.WaitForPostPRSnapshotUploads()

	_, statErr := os.Stat(tarPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "the temp tar file must be removed even when Save fails")
	require.NoError(t, mock.ExpectationsWereMet(), "ClearPendingSnapshot should be the only DB call on the failure path")
}

// TestDispatchPostPRSnapshotUpload_ClearsOnOpenFailure pins the
// open-error branch of the upload goroutine: if the captured tar can't be
// reopened (e.g. it was reaped by another process between capture and
// dispatch), the goroutine still has to clear pending_snapshot_key so
// continue_session can resume rather than blocking forever on the gate.
func TestDispatchPostPRSnapshotUpload_ClearsOnOpenFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created without error")
	defer mock.Close()

	// ClearPendingSnapshot is the only DB call expected — Save is never
	// reached because the tar can't be opened.
	mock.ExpectExec("UPDATE sessions[\\s\\S]*pending_snapshot_key = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	svc := &PRService{
		sessions:  db.NewSessionStore(mock),
		snapshots: &prTestSnapshotStore{},
		logger:    zerolog.Nop(),
	}

	// A path that does not exist forces os.Open to fail. /dev/null/missing
	// is portable: the open syscall returns ENOTDIR on every Unix.
	svc.dispatchPostPRSnapshotUpload(uuid.New(), uuid.New(), "snapshots/whatever", "/dev/null/does-not-exist", 0)
	svc.WaitForPostPRSnapshotUploads()

	require.NoError(t, mock.ExpectationsWereMet(), "open failure must trigger ClearPendingSnapshot exactly once")
}

// TestDispatchPostPRSnapshotUpload_LogsPromoteFailure pins the
// promote-error branch: when Save succeeds but the atomic promote write
// fails, the goroutine logs and returns without falling through to a
// success log. The pending_snapshot_key stays set; the reaper handles it.
func TestDispatchPostPRSnapshotUpload_LogsPromoteFailure(t *testing.T) {
	t.Parallel()

	tarFile, err := os.CreateTemp("", "143-pr-snapshot-test-*.tar.zst")
	require.NoError(t, err, "test setup: create temp tar file")
	require.NoError(t, tarFile.Close(), "test setup: close temp tar file")
	tarPath := tarFile.Name()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created without error")
	defer mock.Close()

	// Promote returns an error from the DB; no follow-up Clear call.
	mock.ExpectQuery("UPDATE sessions[\\s\\S]*snapshot_key = pending_snapshot_key").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("connection reset"))

	svc := &PRService{
		sessions:  db.NewSessionStore(mock),
		snapshots: &prTestSnapshotStore{},
		logger:    zerolog.Nop(),
	}

	svc.dispatchPostPRSnapshotUpload(uuid.New(), uuid.New(), "snapshots/whatever", tarPath, 42)
	svc.WaitForPostPRSnapshotUploads()

	_, statErr := os.Stat(tarPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "the temp tar file must still be removed when Promote fails")
	require.NoError(t, mock.ExpectationsWereMet(), "Promote-failure path must not retry or fall through to extra DB calls")
}

func TestCreatePR_ConfigChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		run           *models.Session
		configureDeps func(svc *PRService)
		wantErrIs     error
		wantErrSubstr string
	}{
		{
			name: "missing sandbox deps fails fast",
			run: &models.Session{
				ID:          uuid.New(),
				OrgID:       uuid.New(),
				SnapshotKey: func() *string { s := "snap"; return &s }(),
			},
			configureDeps: func(*PRService) {},
			wantErrSubstr: "sandbox push dependencies not configured",
		},
		{
			name: "missing snapshot key maps to not captured",
			run: &models.Session{
				ID:    uuid.New(),
				OrgID: uuid.New(),
			},
			configureDeps: func(svc *PRService) {
				svc.sandboxProvider = &prTestSandboxProvider{}
				svc.snapshots = &prTestSnapshotStore{}
			},
			wantErrIs: ErrSnapshotNotCaptured,
		},
		{
			name: "missing repository returns error",
			run: &models.Session{
				ID:          uuid.New(),
				OrgID:       uuid.New(),
				SnapshotKey: func() *string { s := "snap"; return &s }(),
			},
			configureDeps: func(svc *PRService) {
				svc.sandboxProvider = &prTestSandboxProvider{}
				svc.snapshots = &prTestSnapshotStore{}
			},
			wantErrSubstr: "has no repository",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool")
			defer mock.Close()

			mock.ExpectQuery("SELECT .+ FROM pull_requests").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))

			svc := &PRService{
				pullRequests: db.NewPullRequestStore(mock),
				logger:       zerolog.Nop(),
			}
			tt.configureDeps(svc)

			_, err = svc.CreatePR(context.Background(), tt.run)
			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs, "CreatePR should return the expected sentinel error")
			} else {
				require.Error(t, err, "CreatePR should return an error")
				require.Contains(t, err.Error(), tt.wantErrSubstr, "CreatePR should include the expected error text")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCreatePR_SuccessPushesSnapshotBranchAndStoresPR(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	integrationID := uuid.New()
	snapshotKey := "snapshots/session.tar"
	body := ""

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM issues").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestIssueColumns).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, &repoID,
				"Broken button", nil, json.RawMessage(`{}`), "open", now, now,
				1, 1, "high", []string{"bug"}, "fp",
				now, now, nil,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestRepoColumns).AddRow(
				repoID, orgID, integrationID, int64(12345), "owner/repo", "main",
				false, nil, nil, "https://github.com/owner/repo.git", int64(99),
				"active", nil, nil, json.RawMessage(`{}`), now, now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestOrganizationColumns).AddRow(
				orgID, "Test Org", json.RawMessage(`{"pr_authorship":"app_only","pr_draft_default":true}`), now, now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestUserColumns).AddRow(
				userID, orgID, "alice@example.com", "Alice", "member", nil, nil, nil, nil, nil, nil, now,
			),
		)
	mock.ExpectQuery("INSERT INTO pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectExec("UPDATE session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("UPDATE sessions SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "primary_issue_id", "created_at", "last_activity_at"}).
				AddRow(runID, orgID, &issueID, now, now),
		)
	mock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	var createPRPayload map[string]any
	labelCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/pulls":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&createPRPayload), "mock server should decode the create PR payload")
			w.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"number":   42,
				"html_url": "https://github.com/owner/repo/pull/42",
			}), "mock server should encode the created PR")
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/42/labels":
			labelCalls++
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]string{{"name": "143-generated"}}), "mock server should encode label response")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// snapshotErr makes pushSessionBranch's post-push snapshot capture fail
	// (best-effort), which short-circuits the upload-goroutine path —
	// avoiding mock-expectation interleaving with the synchronous DB writes
	// that follow CreatePR's PR row insert. The snapshot upload path itself
	// is exercised separately in TestDispatchPostPRSnapshotUpload.
	provider := &prTestSandboxProvider{
		execStdout:  pushHeadSHASentinel + "abc1234567890abcdef1234567890abcdef12345\n",
		snapshotErr: errors.New("test: skip post-pr snapshot"),
	}
	svc := &PRService{
		tokenProvider: &Service{
			cache: map[int64]*cachedToken{
				99: {Token: "app-token", ExpiresAt: time.Now().Add(time.Hour)},
			},
		},
		pullRequests:    db.NewPullRequestStore(mock),
		sessions:        db.NewSessionStore(mock),
		issues:          db.NewIssueStore(mock),
		repos:           db.NewRepositoryStore(mock),
		users:           db.NewUserStore(mock),
		orgs:            db.NewOrganizationStore(mock),
		sandboxProvider: provider,
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		baseURL:         server.URL,
		httpClient:      server.Client(),
		logger:          zerolog.Nop(),
	}

	run := &models.Session{
		ID:                runID,
		OrgID:             orgID,
		PrimaryIssueID:    &issueID,
		RepositoryID:      &repoID,
		TriggeredByUserID: &userID,
		SnapshotKey:       &snapshotKey,
		ResultSummary:     func() *string { s := "Implemented the fix"; return &s }(),
	}

	pr, err := svc.CreatePR(context.Background(), run)
	require.NoError(t, err, "CreatePR should succeed for a snapshot-backed session")
	require.Equal(t, 42, pr.GitHubPRNumber, "CreatePR should store the returned PR number")
	require.Equal(t, "https://github.com/owner/repo/pull/42", pr.GitHubPRURL, "CreatePR should store the returned PR URL")
	require.Equal(t, "owner/repo", pr.GitHubRepo, "CreatePR should store the repository name")
	require.Equal(t, 1, labelCalls, "CreatePR should add labels to the created PR")
	require.Equal(t, true, createPRPayload["draft"], "CreatePR should honor the org default draft setting")
	require.Contains(t, string(provider.writes[pushCommitMsgPath(agent.DefaultSandboxConfig().HomeDir)]), "Co-authored-by: Alice <alice@example.com>", "CreatePR should include a co-author trailer when using the app token for a user-triggered run")
	require.Contains(t, provider.lastExecCmd, "HEAD:refs/heads/", "CreatePR should push the restored branch before opening the PR")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	_ = body
}

// TestCreatePR_SuccessDispatchesPostPRSnapshotUpload covers the end-to-end
// wiring that the sibling TestCreatePR_SuccessPushesSnapshotBranchAndStoresPR
// deliberately short-circuits via snapshotErr: that on the happy path
// CreatePR persists pending_snapshot_key, hands the captured tar to the
// upload goroutine, and the goroutine eventually promotes the key into
// snapshot_key. Mock expectations run with MatchExpectationsInOrder(false)
// because the goroutine's PromotePendingSnapshot interleaves with the
// post-Create UpdateStatus calls from the main flow.
func TestCreatePR_SuccessDispatchesPostPRSnapshotUpload(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()
	mock.MatchExpectationsInOrder(false)

	now := time.Now()
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	integrationID := uuid.New()
	snapshotKey := "snapshots/session.tar"
	wantPendingKey := fmt.Sprintf("snapshots/%s/%s/post-pr.tar.zst", orgID, runID)

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM issues").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestIssueColumns).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, &repoID,
				"Broken button", nil, json.RawMessage(`{}`), "open", now, now,
				1, 1, "high", []string{"bug"}, "fp",
				now, now, nil,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestRepoColumns).AddRow(
				repoID, orgID, integrationID, int64(12345), "owner/repo", "main",
				false, nil, nil, "https://github.com/owner/repo.git", int64(99),
				"active", nil, nil, json.RawMessage(`{}`), now, now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestOrganizationColumns).AddRow(
				orgID, "Test Org", json.RawMessage(`{"pr_authorship":"app_only","pr_draft_default":true}`), now, now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestUserColumns).AddRow(
				userID, orgID, "alice@example.com", "Alice", "member", nil, nil, nil, nil, nil, nil, now,
			),
		)
	mock.ExpectQuery("INSERT INTO pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectExec("UPDATE session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// SetPendingSnapshotKey: the synchronous write that records the
	// post-PR upload in flight. pgx NamedArgs expand to positional args by
	// @-placeholder order: SET pending_snapshot_key = @key first, then
	// WHERE id = @id, org_id = @org_id, so the key is arg #0.
	mock.ExpectExec("UPDATE sessions[\\s\\S]*pending_snapshot_key = @key").
		WithArgs(wantPendingKey, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// PromotePendingSnapshot: the async write fired by the upload
	// goroutine after Save() succeeds. SQL order is @id, @org_id,
	// @expected_key, so the key guard is arg #2.
	mock.ExpectQuery("UPDATE sessions[\\s\\S]*snapshot_key = pending_snapshot_key").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), wantPendingKey).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).
			AddRow(int64(2), time.Now().UTC()))
	mock.ExpectQuery("UPDATE sessions SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "primary_issue_id", "created_at", "last_activity_at"}).
				AddRow(runID, orgID, &issueID, now, now),
		)
	mock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/pulls":
			w.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"number":   42,
				"html_url": "https://github.com/owner/repo/pull/42",
			}), "mock server should encode the created PR")
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/42/labels":
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]string{{"name": "143-generated"}}), "mock server should encode label response")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Real success path: no snapshotErr, real Snapshot reader. The
	// dispatch goroutine spools an empty tar, calls
	// prTestSnapshotStore.Save (no-op success), and promotes.
	provider := &prTestSandboxProvider{
		execStdout: pushHeadSHASentinel + "abc1234567890abcdef1234567890abcdef12345\n",
	}
	svc := &PRService{
		tokenProvider: &Service{
			cache: map[int64]*cachedToken{
				99: {Token: "app-token", ExpiresAt: time.Now().Add(time.Hour)},
			},
		},
		pullRequests:    db.NewPullRequestStore(mock),
		sessions:        db.NewSessionStore(mock),
		issues:          db.NewIssueStore(mock),
		repos:           db.NewRepositoryStore(mock),
		users:           db.NewUserStore(mock),
		orgs:            db.NewOrganizationStore(mock),
		sandboxProvider: provider,
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		baseURL:         server.URL,
		httpClient:      server.Client(),
		logger:          zerolog.Nop(),
	}

	run := &models.Session{
		ID:                runID,
		OrgID:             orgID,
		PrimaryIssueID:    &issueID,
		RepositoryID:      &repoID,
		TriggeredByUserID: &userID,
		SnapshotKey:       &snapshotKey,
		ResultSummary:     func() *string { s := "Implemented the fix"; return &s }(),
	}

	pr, err := svc.CreatePR(context.Background(), run)
	require.NoError(t, err, "CreatePR should succeed for a snapshot-backed session")
	require.Equal(t, 42, pr.GitHubPRNumber, "CreatePR should store the returned PR number")

	// Drain the upload goroutine before checking expectations — Promote
	// is async and would otherwise race the assertion.
	svc.WaitForPostPRSnapshotUploads()
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations (including async PromotePendingSnapshot) should be met")
}

func TestCreatePR_NoCommitsBetweenMapsToErrNoChanges(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	runID := uuid.New()
	repoID := uuid.New()
	snapshotKey := "snapshots/session.tar"
	integrationID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prTestRepoColumns).AddRow(
				repoID, orgID, integrationID, int64(12345), "owner/repo", "main",
				false, nil, nil, "https://github.com/owner/repo.git", int64(99),
				"active", nil, nil, json.RawMessage(`{}`), now, now,
			),
		)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, writeErr := w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"No commits between main and 143/abc123/changes"}]}`))
		require.NoError(t, writeErr, "mock server should return the GitHub no-commits validation error")
	}))
	defer server.Close()

	svc := &PRService{
		tokenProvider: &Service{
			cache: map[int64]*cachedToken{
				99: {Token: "app-token", ExpiresAt: time.Now().Add(time.Hour)},
			},
		},
		pullRequests: db.NewPullRequestStore(mock),
		repos:        db.NewRepositoryStore(mock),
		sandboxProvider: &prTestSandboxProvider{
			execStdout: pushHeadSHASentinel + "abc1234567890abcdef1234567890abcdef12345\n",
		},
		snapshots:   &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth: &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		baseURL:     server.URL,
		httpClient:  server.Client(),
		logger:      zerolog.Nop(),
	}

	run := &models.Session{
		ID:           runID,
		OrgID:        orgID,
		RepositoryID: &repoID,
		SnapshotKey:  &snapshotKey,
	}

	_, err = svc.CreatePR(context.Background(), run)
	require.ErrorIs(t, err, ErrNoChanges, "CreatePR should translate GitHub's no-commits 422 into ErrNoChanges")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCreateOrGetPullRequest_AdditionalPaths(t *testing.T) {
	t.Parallel()

	t.Run("returns created pull request directly", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"number":   7,
				"html_url": "https://github.com/owner/repo/pull/7",
			}), "mock server should encode the created PR")
		}))
		defer server.Close()

		svc := &PRService{baseURL: server.URL, httpClient: server.Client(), logger: zerolog.Nop()}
		prNumber, prURL, err := svc.createOrGetPullRequest(context.Background(), "token", "owner", "repo", "title", "body", "head", "main")
		require.NoError(t, err, "createOrGetPullRequest should return a created PR without fallback lookup")
		require.Equal(t, 7, prNumber, "createOrGetPullRequest should return the created PR number")
		require.Equal(t, "https://github.com/owner/repo/pull/7", prURL, "createOrGetPullRequest should return the created PR URL")
	})

	t.Run("passthroughs non-conflict error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, writeErr := w.Write([]byte(`{"message":"upstream down"}`))
			require.NoError(t, writeErr, "mock server should return a non-conflict error")
		}))
		defer server.Close()

		svc := &PRService{baseURL: server.URL, httpClient: server.Client(), logger: zerolog.Nop()}
		_, _, err := svc.createOrGetPullRequest(context.Background(), "token", "owner", "repo", "title", "body", "head", "main")
		require.Error(t, err, "createOrGetPullRequest should return non-conflict errors directly")
		require.Contains(t, err.Error(), "returned 502", "createOrGetPullRequest should preserve the original GitHub API error")
	})
}

func TestFindOpenPullRequestByHead_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		handler       http.HandlerFunc
		wantErrSubstr string
	}{
		{
			name: "request failure wraps lookup context",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
			},
			wantErrSubstr: "find existing pull request by head",
		},
		{
			name: "decode failure wraps decode context",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`not json`))
			},
			wantErrSubstr: "decode existing pull request lookup",
		},
		{
			name: "empty result returns descriptive error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode([]map[string]any{}), "mock server should encode an empty PR list")
			},
			wantErrSubstr: "no open pull request found for head-branch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handler)
			defer server.Close()

			svc := &PRService{baseURL: server.URL, httpClient: server.Client(), logger: zerolog.Nop()}
			_, _, err := svc.findOpenPullRequestByHead(context.Background(), "token", "owner", "repo", "head-branch")
			require.Error(t, err, "findOpenPullRequestByHead should return an error for %s", tt.name)
			require.Contains(t, err.Error(), tt.wantErrSubstr, "findOpenPullRequestByHead should include the expected error context")
		})
	}
}

// TestCollectLinearIdentifiers locks the order contract: primary first then
// related by position, deduplicating, with the primary-issue fallback used
// only when LinkedIssues is empty.
func TestCollectLinearIdentifiers(t *testing.T) {
	t.Parallel()

	src := models.IssueSourceLinear
	link := func(extID string, role models.SessionIssueLinkRole, pos int) models.SessionIssueLink {
		ext := extID
		return models.SessionIssueLink{
			IssueSource: &src,
			ExternalID:  &ext,
			Role:        role,
			Position:    pos,
		}
	}

	t.Run("primary first then related", func(t *testing.T) {
		t.Parallel()
		s := &models.Session{
			LinkedIssues: []models.SessionIssueLink{
				link("ENG-1", models.SessionIssueLinkRolePrimary, 0),
				link("ENG-2", models.SessionIssueLinkRoleRelated, 1),
			},
		}
		require.Equal(t, []string{"ENG-1", "ENG-2"}, collectLinearIdentifiers(s, nil))
	})

	t.Run("dedupes identical externals", func(t *testing.T) {
		t.Parallel()
		s := &models.Session{
			LinkedIssues: []models.SessionIssueLink{
				link("ENG-1", models.SessionIssueLinkRolePrimary, 0),
				link("ENG-1", models.SessionIssueLinkRoleRelated, 1),
			},
		}
		require.Equal(t, []string{"ENG-1"}, collectLinearIdentifiers(s, nil))
	})

	t.Run("non-linear sources are skipped", func(t *testing.T) {
		t.Parallel()
		sentry := models.IssueSourceSentry
		ext := "SEN-1"
		s := &models.Session{
			LinkedIssues: []models.SessionIssueLink{
				{IssueSource: &sentry, ExternalID: &ext, Role: models.SessionIssueLinkRolePrimary},
				link("ENG-1", models.SessionIssueLinkRoleRelated, 1),
			},
		}
		require.Equal(t, []string{"ENG-1"}, collectLinearIdentifiers(s, nil))
	})

	t.Run("falls back to primaryIssue when LinkedIssues is unhydrated", func(t *testing.T) {
		t.Parallel()
		got := collectLinearIdentifiers(&models.Session{}, &models.Issue{
			Source:     models.IssueSourceLinear,
			ExternalID: "ENG-7",
		})
		require.Equal(t, []string{"ENG-7"}, got)
	})

	t.Run("ignores non-linear primary fallback", func(t *testing.T) {
		t.Parallel()
		got := collectLinearIdentifiers(&models.Session{}, &models.Issue{
			Source:     models.IssueSourceSentry,
			ExternalID: "SEN-1",
		})
		require.Nil(t, got)
	})

	t.Run("nil session with nil primary returns nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, collectLinearIdentifiers(nil, nil))
	})

	t.Run("rejects non-key-shaped external_ids (UUID leak guard)", func(t *testing.T) {
		t.Parallel()
		// Simulates the COALESCE fall-through to issues.external_id when
		// provider_state.identifier hasn't been written yet — the link
		// row carries Linear's UUID instead of the human key. We must not
		// emit it as a prefix; once baked in, linearBracketPrefixRE wouldn't
		// strip it on resync.
		s := &models.Session{
			LinkedIssues: []models.SessionIssueLink{
				link("ENG-1", models.SessionIssueLinkRolePrimary, 0),
				link("01a2b3c4-d5e6-7890-abcd-ef1234567890", models.SessionIssueLinkRoleRelated, 1),
			},
		}
		require.Equal(t, []string{"ENG-1"}, collectLinearIdentifiers(s, nil))
	})

	t.Run("rejects non-key-shaped primaryIssue fallback", func(t *testing.T) {
		t.Parallel()
		got := collectLinearIdentifiers(&models.Session{}, &models.Issue{
			Source:     models.IssueSourceLinear,
			ExternalID: "01a2b3c4-d5e6-7890-abcd-ef1234567890",
		})
		require.Nil(t, got)
	})
}
