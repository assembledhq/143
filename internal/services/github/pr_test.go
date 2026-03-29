package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

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
			name:    "linear source uses external ID prefix",
			session: models.Session{ID: uuid.New()},
			issue: &models.Issue{
				Source:     models.IssueSourceLinear,
				ExternalID: "ENG-1234",
				Title:      "Fix null pointer in user API",
			},
			expect: "ENG-1234: Fix null pointer in user API",
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
			name:    "nil issue with no title or summary uses session ID",
			session: models.Session{ID: uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")},
			issue:   nil,
			expect:  "Session abcdef01",
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

func TestParseDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		diff   string
		expect []diffFile
	}{
		{
			name: "single file modification",
			diff: `diff --git a/main.go b/main.go
index abc1234..def5678 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main

+import "fmt"
 func main() {}`,
			expect: []diffFile{
				{Path: "main.go", Content: "package main\n\nimport \"fmt\"\nfunc main() {}\n"},
			},
		},
		{
			name: "multiple files",
			diff: `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1 +1 @@
+package a
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1 +1 @@
+package b`,
			expect: []diffFile{
				{Path: "a.go", Content: "package a\n"},
				{Path: "b.go", Content: "package b\n"},
			},
		},
		{
			name: "path traversal rejected",
			diff: `diff --git a/../../../etc/passwd b/../../../etc/passwd
--- a/../../../etc/passwd
+++ b/../../../etc/passwd
@@ -0,0 +1 @@
+evil content`,
			expect: nil,
		},
		{
			name: "absolute path rejected",
			diff: `diff --git a//etc/shadow b//etc/shadow
--- a//etc/shadow
+++ b//etc/shadow
@@ -0,0 +1 @@
+evil content`,
			expect: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseDiff(tt.diff)
			if tt.expect == nil {
				require.Empty(t, result, "path traversal diffs should be rejected and produce empty result")
			} else {
				require.Len(t, result, len(tt.expect), "parsed diff should have expected number of files")
				for i, f := range tt.expect {
					require.Equal(t, f.Path, result[i].Path, "diff file path should match at index %d", i)
					require.Equal(t, f.Content, result[i].Content, "diff file content should match at index %d", i)
					require.Equal(t, f.Deleted, result[i].Deleted, "diff file deleted flag should match at index %d", i)
				}
			}
		})
	}
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

// TestGitHubAPIFlow tests the HTTP interactions with a mock GitHub API server.
func TestGitHubAPIFlow(t *testing.T) {
	t.Parallel()

	baseSHA := "abc123base"
	blobSHA := "blob456"
	treeSHA := "tree789"
	commitSHA := "commit012"

	var requestPaths []string

	mux := http.NewServeMux()

	// GET /repos/:owner/:repo/git/ref/heads/main
	mux.HandleFunc("GET /repos/testorg/testrepo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		err := json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]string{"sha": baseSHA},
		})
		require.NoError(t, err, "mock server should encode getRef response")
	})

	// POST /repos/:owner/:repo/git/refs (create branch)
	mux.HandleFunc("POST /repos/testorg/testrepo/git/refs", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"ref": "refs/heads/143/fix/test"})
		require.NoError(t, err, "mock server should encode createRef response")
	})

	// POST /repos/:owner/:repo/git/blobs
	mux.HandleFunc("POST /repos/testorg/testrepo/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": blobSHA})
		require.NoError(t, err, "mock server should encode createBlob response")
	})

	// POST /repos/:owner/:repo/git/trees
	mux.HandleFunc("POST /repos/testorg/testrepo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": treeSHA})
		require.NoError(t, err, "mock server should encode createTree response")
	})

	// POST /repos/:owner/:repo/git/commits
	mux.HandleFunc("POST /repos/testorg/testrepo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": commitSHA})
		require.NoError(t, err, "mock server should encode createCommit response")
	})

	// POST /repos/:owner/:repo/pulls
	mux.HandleFunc("POST /repos/testorg/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/testorg/testrepo/pull/42",
		})
		require.NoError(t, err, "mock server should encode create pull request response")
	})

	// POST /repos/:owner/:repo/issues/:number/labels
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

	// Test getRef.
	sha, err := svc.getRef(ctx, "test-token", "testorg", "testrepo", "refs/heads/main")
	require.NoError(t, err, "getRef should not return an error")
	require.Equal(t, baseSHA, sha, "getRef should return the base SHA from the mock server")

	// Test createRef.
	err = svc.createRef(ctx, "test-token", "testorg", "testrepo", "refs/heads/143/fix/test", baseSHA)
	require.NoError(t, err, "createRef should not return an error")

	// Test createBlob.
	blob, err := svc.createBlob(ctx, "test-token", "testorg", "testrepo", "package main\n")
	require.NoError(t, err, "createBlob should not return an error")
	require.Equal(t, blobSHA, blob, "createBlob should return the blob SHA from the mock server")

	// Test createTree.
	tree, err := svc.createTree(ctx, "test-token", "testorg", "testrepo", baseSHA, []treeEntry{
		{Path: "main.go", Mode: "100644", Type: "blob", SHA: &blobSHA},
	})
	require.NoError(t, err, "createTree should not return an error")
	require.Equal(t, treeSHA, tree, "createTree should return the tree SHA from the mock server")

	// Test createCommit.
	commit, err := svc.createCommit(ctx, "test-token", "testorg", "testrepo", "fix: test", treeSHA, baseSHA)
	require.NoError(t, err, "createCommit should not return an error")
	require.Equal(t, commitSHA, commit, "createCommit should return the commit SHA from the mock server")

	// Test createPullRequest.
	prNum, prURL, err := svc.createPullRequest(ctx, "test-token", "testorg", "testrepo", "fix: test PR", "body", "143/fix/test", "main")
	require.NoError(t, err, "createPullRequest should not return an error")
	require.Equal(t, 42, prNum, "createPullRequest should return PR number 42")
	require.Equal(t, "https://github.com/testorg/testrepo/pull/42", prURL, "createPullRequest should return the correct PR URL")

	// Test addLabels.
	err = svc.addLabels(ctx, "test-token", "testorg", "testrepo", 42, []string{"143-generated"})
	require.NoError(t, err, "addLabels should not return an error")

	// Verify all expected API calls were made.
	require.Len(t, requestPaths, 7, "should have made exactly 7 API calls")
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
