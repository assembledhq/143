package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestStagedPRContentFromPayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		payload   json.RawMessage
		expected  stagedPRContent
		wantError bool
	}{
		{name: "empty payload", expected: stagedPRContent{}},
		{name: "payload without staged text", payload: json.RawMessage(`{"session_id":"s","team_reviewers":["acme/team"]}`), expected: stagedPRContent{}},
		{name: "title and body", payload: json.RawMessage(`{"title":"feature-brief: Plan exports","body":"Source plan: docs/plans/exports.md"}`), expected: stagedPRContent{Title: new("feature-brief: Plan exports"), Body: new("Source plan: docs/plans/exports.md")}},
		{name: "blank values are unstaged", payload: json.RawMessage(`{"title":"  ","body":"\n"}`), expected: stagedPRContent{}},
		{name: "malformed payload", payload: json.RawMessage(`{"title":`), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := stagedPRContentFromPayload(tt.payload)
			if tt.wantError {
				require.Error(t, err, "an undecodable durable payload must fail publication rather than drop staged text")
				return
			}
			require.NoError(t, err, "durable payload should decode")
			require.Equal(t, tt.expected, actual, "only non-blank staged text should be returned")
		})
	}
}

func TestSelectPRContent(t *testing.T) {
	t.Parallel()
	const footer = "[143.dev](https://143.dev) | [session abcdef01](https://143.dev/sessions/abcdef01)"
	sessionID := uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")
	linearSession := func() *models.Session {
		source := models.IssueSourceLinear
		return &models.Session{ID: sessionID, LinkedIssues: []models.SessionIssueLink{{IssueSource: &source, Role: models.SessionIssueLinkRolePrimary, ExternalID: new("VIR-75")}}}
	}
	tests := []struct {
		name          string
		run           *models.Session
		changeset     *models.SessionChangeset
		staged        stagedPRContent
		generated     *generatedPR
		expectedTitle string
		expectedBody  string
	}{
		{
			name:          "staged text replaces generated content and keeps the footer",
			run:           &models.Session{ID: sessionID},
			staged:        stagedPRContent{Title: new("feature-brief: Plan exports"), Body: new("Source plan: docs/plans/exports.md\n")},
			generated:     &generatedPR{Title: "Generated title", Body: "Generated body"},
			expectedTitle: "feature-brief: Plan exports",
			expectedBody:  "Source plan: docs/plans/exports.md\n\n" + footer,
		},
		{
			name:          "staged title receives Linear key prefixes",
			run:           linearSession(),
			staged:        stagedPRContent{Title: new("feat: Plan exports")},
			generated:     &generatedPR{Title: "Generated title", Body: "Generated body"},
			expectedTitle: "feat: [VIR-75] Plan exports",
			expectedBody:  "Generated body",
		},
		{
			name:          "Linear prefix precedes a non-conventional title",
			run:           linearSession(),
			staged:        stagedPRContent{Title: new("feature-brief: Plan exports")},
			expectedTitle: "[VIR-75] feature-brief: Plan exports",
		},
		{
			name:          "Linear prefix truncation keeps multibyte titles valid",
			run:           linearSession(),
			staged:        stagedPRContent{Title: new(strings.Repeat("é", models.MaxStagedPRTitleBytes/2))},
			expectedTitle: "[VIR-75] " + strings.Repeat("é", (maxPRTitleLen-len("[VIR-75] "))/2),
		},
		{
			name:          "staged title overrides an explicit changeset title",
			run:           &models.Session{ID: sessionID},
			changeset:     &models.SessionChangeset{IsPrimary: true, Title: "Changeset title"},
			staged:        stagedPRContent{Title: new("feature-brief: Plan exports")},
			expectedTitle: "feature-brief: Plan exports",
		},
		{
			name:          "staged body with generated title",
			run:           &models.Session{ID: sessionID},
			staged:        stagedPRContent{Body: new("Source plan: docs/plans/exports.md")},
			generated:     &generatedPR{Title: "Generated title", Body: "Generated body"},
			expectedTitle: "Generated title",
			expectedBody:  "Source plan: docs/plans/exports.md\n\n" + footer,
		},
		{
			name:          "no staged text keeps generated content",
			run:           &models.Session{ID: sessionID},
			generated:     &generatedPR{Title: "Generated title", Body: "Generated body"},
			expectedTitle: "Generated title",
			expectedBody:  "Generated body",
		},
		{
			name:          "no staged text keeps the changeset title rule",
			run:           &models.Session{ID: sessionID},
			changeset:     &models.SessionChangeset{IsPrimary: true, Title: "Changeset title"},
			generated:     &generatedPR{Title: "Generated title", Body: "Generated body"},
			expectedTitle: "Changeset title",
			expectedBody:  "Generated body",
		},
		{
			name:          "no staged text and no generation leaves the static fallback to the caller",
			run:           &models.Session{ID: sessionID},
			expectedTitle: "Session abcdef01",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			title, body := selectPRContent(tt.run, nil, tt.changeset, tt.staged, tt.generated, footer)
			require.Equal(t, tt.expectedTitle, title, "PR title should follow staged, generated, then static precedence")
			require.True(t, utf8.ValidString(title), "PR title must remain valid UTF-8 after prefixing and truncation")
			require.Equal(t, tt.expectedBody, body, "PR body should follow staged, then generated precedence")
		})
	}
}

// TestCreatePRUsesStagedContent drives CreatePR end to end with a durable
// publication payload and captures the GitHub create request.
func TestCreatePRUsesStagedContent(t *testing.T) {
	t.Parallel()
	const generatedResponse = "<pr_title>Generated title</pr_title>\n<pr_body>\nGenerated body\n</pr_body>"
	tests := []struct {
		name           string
		payload        string
		expectLLMCall  bool
		expectedTitle  string
		expectedPrefix string
	}{
		{
			name:           "staged title and body skip generation",
			payload:        `{"title":"feature-brief: Plan exports","body":"## Summary\n\nSource plan: docs/plans/exports.md"}`,
			expectedTitle:  "feature-brief: Plan exports",
			expectedPrefix: "## Summary\n\nSource plan: docs/plans/exports.md\n\n",
		},
		{
			name:           "staged title only generates the body",
			payload:        `{"title":"feature-brief: Plan exports"}`,
			expectLLMCall:  true,
			expectedTitle:  "feature-brief: Plan exports",
			expectedPrefix: "Generated body\n\n",
		},
		{
			name:           "no staged text is unchanged",
			payload:        `{"session_id":"unused"}`,
			expectLLMCall:  true,
			expectedTitle:  "Generated title",
			expectedPrefix: "Generated body\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			llm := &mockLLMClient{response: generatedResponse}
			run, created, svc := runStagedContentCreatePR(t, llm, json.RawMessage(tt.payload))
			require.Equal(t, tt.expectLLMCall, llm.lastUserPrompt != "", "LLM generation should run only when some PR text is not staged")
			require.Equal(t, tt.expectedTitle, created["title"], "GitHub should receive the selected title")
			require.Equal(t, tt.expectedPrefix+svc.formatPRFooterLinks(run), created["body"], "GitHub should receive the selected body plus the standard footer")
		})
	}
}

func runStagedContentCreatePR(t *testing.T, llm *mockLLMClient, payload json.RawMessage) (*models.Session, map[string]any, *PRService) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	t.Cleanup(mock.Close)
	mock.MatchExpectationsInOrder(false)

	now := time.Now()
	orgID, runID, repoID, userID, integrationID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	snapshotKey := "snapshots/session.tar"
	wantPendingKey := fmt.Sprintf("snapshots/%s/%s/post-pr.tar.zst", orgID, runID)

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			repoID, orgID, integrationID, int64(12345), "owner/repo", "main",
			false, nil, nil, "https://github.com/owner/repo.git", int64(99),
			"active", nil, nil, json.RawMessage(`{}`), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestOrganizationColumns).AddRow(
			orgID, "Test Org", json.RawMessage(`{"pr_authorship":"app_only","pr_draft_default":false}`), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestUserColumns).AddRow(
			userID, orgID, "alice@example.com", "Alice", "member", nil, nil, nil, nil, nil, nil, now,
		))
	mock.ExpectQuery("INSERT INTO pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectExec("UPDATE session_diff_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE sessions[\\s\\S]*pending_snapshot_key = @key").
		WithArgs(wantPendingKey, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("UPDATE sessions[\\s\\S]*snapshot_key = pending_snapshot_key").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), wantPendingKey).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(2), time.Now().UTC()))
	mock.ExpectQuery("UPDATE sessions SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prHealthSessionColumns).AddRow(newPRHealthSessionRow(runID, orgID, now, models.SessionStatusPRCreated)...))

	var mu sync.Mutex
	created := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/pulls":
			mu.Lock()
			decodeErr := json.NewDecoder(r.Body).Decode(&created)
			mu.Unlock()
			require.NoError(t, decodeErr, "GitHub create request should decode")
			w.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"number": 42, "html_url": "https://github.com/owner/repo/pull/42"}), "mock server should encode the created PR")
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/42/labels":
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]string{{"name": "143-generated"}}), "mock server should encode label response")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	svc := &PRService{
		tokenProvider:   &Service{cache: map[int64]*cachedToken{99: {Token: "app-token", ExpiresAt: time.Now().Add(time.Hour)}}},
		pullRequests:    db.NewPullRequestStore(mock),
		sessions:        db.NewSessionStore(mock),
		issues:          db.NewIssueStore(mock),
		repos:           db.NewRepositoryStore(mock),
		users:           db.NewUserStore(mock),
		orgs:            db.NewOrganizationStore(mock),
		sandboxProvider: &prTestSandboxProvider{execStdout: pushHeadSHASentinel + "abc1234567890abcdef1234567890abcdef12345\n"},
		snapshots:       &prTestSnapshotStore{payload: []byte("snapshot")},
		sandboxAuth:     &fakeSandboxAuth{socketPath: "/tmp/fake.sock"},
		llmClient:       llm,
		baseURL:         server.URL,
		httpClient:      server.Client(),
		logger:          zerolog.Nop(),
	}
	run := &models.Session{
		ID: runID, OrgID: orgID, RepositoryID: &repoID, TriggeredByUserID: &userID,
		SnapshotKey: &snapshotKey, ResultSummary: new("Implemented the fix"),
	}
	pr, err := svc.CreatePR(context.Background(), run, CreatePRParams{PublicationRequestPayload: payload})
	require.NoError(t, err, "CreatePR should succeed for a snapshot-backed session")
	require.Equal(t, 42, pr.GitHubPRNumber, "CreatePR should store the returned PR number")
	svc.WaitForPostPRSnapshotUploads()
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	mu.Lock()
	defer mu.Unlock()
	return run, created, svc
}
