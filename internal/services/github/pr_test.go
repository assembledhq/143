package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestFormatPRTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issue  models.Issue
		expect string
	}{
		{
			name: "linear source uses external ID prefix",
			issue: models.Issue{
				Source:     "linear",
				ExternalID: "ENG-1234",
				Title:      "Fix null pointer in user API",
			},
			expect: "ENG-1234: Fix null pointer in user API",
		},
		{
			name: "sentry source uses fix prefix",
			issue: models.Issue{
				Source: "sentry",
				Title:  "TypeError in payment handler",
			},
			expect: "fix: TypeError in payment handler",
		},
		{
			name: "support source uses fix prefix",
			issue: models.Issue{
				Source: "support",
				Title:  "Login button not working",
			},
			expect: "fix: Login button not working",
		},
		{
			name: "unknown source uses fix prefix",
			issue: models.Issue{
				Source: "other",
				Title:  "Some issue",
			},
			expect: "fix: Some issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatPRTitle(&tt.issue)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestFormatBranchName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		runID   uuid.UUID
		title   string
		expect  string
		maxLen  bool // if true, verify length constraints
	}{
		{
			name:   "basic branch name",
			runID:  uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"),
			title:  "Fix null pointer",
			expect: "143/fix/abcdef01/fix-null-pointer",
		},
		{
			name:   "special characters are slugified",
			runID:  uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"),
			title:  "Fix: TypeError in payment_handler (v2)",
			expect: "143/fix/abcdef01/fix-typeerror-in-payment-handler-v2",
		},
		{
			name:   "long title is truncated",
			runID:  uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"),
			title:  "This is a very long issue title that should be truncated at some reasonable point to avoid creating overly long branch names",
			maxLen: true,
		},
		{
			name:   "empty title falls back to fix",
			runID:  uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789"),
			title:  "",
			expect: "143/fix/abcdef01/fix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatBranchName(tt.runID, tt.title)
			if tt.maxLen {
				// The slug portion (after "143/fix/{8chars}/") shouldn't exceed maxBranchSlugLen
				parts := strings.SplitN(result, "/", 4)
				require.Len(t, parts, 4)
				assert.LessOrEqual(t, len(parts[3]), maxBranchSlugLen)
			} else {
				assert.Equal(t, tt.expect, result)
			}
			// Branch name should never contain spaces.
			assert.NotContains(t, result, " ")
		})
	}
}

func TestFormatCommitMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issue  models.Issue
		expect string
	}{
		{
			name: "linear issue includes Fixes reference",
			issue: models.Issue{
				Source:     "linear",
				ExternalID: "ENG-1234",
				Title:      "Fix null pointer",
			},
			expect: "fix: Fix null pointer\n\nFixes #ENG-1234",
		},
		{
			name: "sentry issue includes Resolves reference",
			issue: models.Issue{
				Source:     "sentry",
				ExternalID: "SENTRY-5678",
				Title:      "TypeError in handler",
			},
			expect: "fix: TypeError in handler\n\nResolves SENTRY-5678",
		},
		{
			name: "support issue has no reference",
			issue: models.Issue{
				Source: "support",
				Title:  "Login broken",
			},
			expect: "fix: Login broken",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatCommitMessage(&tt.issue)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestBuildLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issue  models.Issue
		expect []string
	}{
		{
			name: "all labels",
			issue: models.Issue{
				Severity: "high",
				Source:   "sentry",
			},
			expect: []string{"143-generated", "severity:high", "source:sentry"},
		},
		{
			name: "no severity",
			issue: models.Issue{
				Source: "linear",
			},
			expect: []string{"143-generated", "source:linear"},
		},
		{
			name:   "minimal",
			issue:  models.Issue{},
			expect: []string{"143-generated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := buildLabels(&tt.issue)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestFormatPRBody(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := &PRService{logger: logger}

	summary := "Fixed the null pointer dereference"
	run := &models.AgentRun{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}
	issue := &models.Issue{
		Source:                "sentry",
		Severity:              "high",
		AffectedCustomerCount: 42,
		OccurrenceCount:       100,
	}

	body := svc.formatPRBody(context.Background(), run, issue)

	assert.Contains(t, body, "## Summary")
	assert.Contains(t, body, summary)
	assert.Contains(t, body, "sentry")
	assert.Contains(t, body, "high")
	assert.Contains(t, body, "42")
	assert.Contains(t, body, "100")
	assert.Contains(t, body, "claude-code")
	assert.Contains(t, body, "143.dev")
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseDiff(tt.diff)
			if tt.expect == nil {
				assert.Empty(t, result)
			} else {
				require.Len(t, result, len(tt.expect))
				for i, f := range tt.expect {
					assert.Equal(t, f.Path, result[i].Path)
					assert.Equal(t, f.Content, result[i].Content)
					assert.Equal(t, f.Deleted, result[i].Deleted)
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
			assert.Equal(t, tt.expect, slugify(tt.input))
		})
	}
}

func TestSplitRepo(t *testing.T) {
	t.Parallel()

	owner, repo := splitRepo("myorg/myrepo")
	assert.Equal(t, "myorg", owner)
	assert.Equal(t, "myrepo", repo)

	owner, repo = splitRepo("noslash")
	assert.Equal(t, "noslash", owner)
	assert.Equal(t, "noslash", repo)
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
		json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]string{"sha": baseSHA},
		})
	})

	// POST /repos/:owner/:repo/git/refs (create branch)
	mux.HandleFunc("POST /repos/testorg/testrepo/git/refs", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"ref": "refs/heads/143/fix/test"})
	})

	// POST /repos/:owner/:repo/git/blobs
	mux.HandleFunc("POST /repos/testorg/testrepo/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"sha": blobSHA})
	})

	// POST /repos/:owner/:repo/git/trees
	mux.HandleFunc("POST /repos/testorg/testrepo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"sha": treeSHA})
	})

	// POST /repos/:owner/:repo/git/commits
	mux.HandleFunc("POST /repos/testorg/testrepo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"sha": commitSHA})
	})

	// POST /repos/:owner/:repo/pulls
	mux.HandleFunc("POST /repos/testorg/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"number":   42,
			"html_url": "https://github.com/testorg/testrepo/pull/42",
		})
	})

	// POST /repos/:owner/:repo/issues/:number/labels
	mux.HandleFunc("POST /repos/testorg/testrepo/issues/42/labels", func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.Method+" "+r.URL.Path)
		json.NewEncoder(w).Encode([]map[string]string{{"name": "143-generated"}})
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
	require.NoError(t, err)
	assert.Equal(t, baseSHA, sha)

	// Test createRef.
	err = svc.createRef(ctx, "test-token", "testorg", "testrepo", "refs/heads/143/fix/test", baseSHA)
	require.NoError(t, err)

	// Test createBlob.
	blob, err := svc.createBlob(ctx, "test-token", "testorg", "testrepo", "package main\n")
	require.NoError(t, err)
	assert.Equal(t, blobSHA, blob)

	// Test createTree.
	tree, err := svc.createTree(ctx, "test-token", "testorg", "testrepo", baseSHA, []treeEntry{
		{Path: "main.go", Mode: "100644", Type: "blob", SHA: &blobSHA},
	})
	require.NoError(t, err)
	assert.Equal(t, treeSHA, tree)

	// Test createCommit.
	commit, err := svc.createCommit(ctx, "test-token", "testorg", "testrepo", "fix: test", treeSHA, baseSHA)
	require.NoError(t, err)
	assert.Equal(t, commitSHA, commit)

	// Test createPullRequest.
	prNum, prURL, err := svc.createPullRequest(ctx, "test-token", "testorg", "testrepo", "fix: test PR", "body", "143/fix/test", "main")
	require.NoError(t, err)
	assert.Equal(t, 42, prNum)
	assert.Equal(t, "https://github.com/testorg/testrepo/pull/42", prURL)

	// Test addLabels.
	err = svc.addLabels(ctx, "test-token", "testorg", "testrepo", 42, []string{"143-generated"})
	require.NoError(t, err)

	// Verify all expected API calls were made.
	assert.Len(t, requestPaths, 7)
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
	require.NoError(t, err)

	var decoded PullRequestEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "closed", decoded.Action)
	assert.True(t, decoded.PR.Merged)
	assert.Equal(t, "abc123", decoded.PR.Head.SHA)
	assert.Equal(t, "testorg/testrepo", decoded.Repository.FullName)
	assert.Equal(t, 42, decoded.Number)
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
	require.NoError(t, err)

	var decoded PullRequestEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "closed", decoded.Action)
	assert.False(t, decoded.PR.Merged)
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
	require.NoError(t, err)

	var decoded PullRequestReviewEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "submitted", decoded.Action)
	assert.Equal(t, "approved", decoded.Review.State)
	assert.Equal(t, 42, decoded.PullRequest.Number)
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
	require.NoError(t, err)

	var decoded PullRequestReviewEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "changes_requested", decoded.Review.State)
}

func TestCheckEmoji(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "pass", checkEmoji("pass"))
	assert.Equal(t, "pass", checkEmoji("passed"))
	assert.Equal(t, "fail", checkEmoji("fail"))
	assert.Equal(t, "fail", checkEmoji("failed"))
	assert.Equal(t, "skip", checkEmoji("skip"))
	assert.Equal(t, "skip", checkEmoji("skipped"))
	assert.Equal(t, "pending", checkEmoji("pending"))
}

func TestDoGitHubRequest_ErrorResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	_, err := svc.doGitHubRequest(context.Background(), "test-token", http.MethodGet, "/repos/test/test/git/ref/heads/main", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), "Not Found")
}

func TestDoGitHubRequest_SetsHeaders(t *testing.T) {
	t.Parallel()

	var capturedAuth string
	var capturedAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedAccept = r.Header.Get("Accept")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	_, err := svc.doGitHubRequest(context.Background(), "my-token", http.MethodGet, "/test", nil)
	require.NoError(t, err)
	assert.Equal(t, "token my-token", capturedAuth)
	assert.Equal(t, "application/vnd.github+json", capturedAccept)
}

func TestFormatPRBody_WithValidation(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := &PRService{logger: logger}

	now := time.Now()
	started := now.Add(-5 * time.Minute)
	summary := "Fixed the bug"
	run := &models.AgentRun{
		ID:            uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "claude-code",
		ResultSummary: &summary,
		StartedAt:     &started,
		CompletedAt:   &now,
	}
	issue := &models.Issue{
		Source:                "linear",
		Severity:              "critical",
		AffectedCustomerCount: 10,
		OccurrenceCount:       50,
	}

	body := svc.formatPRBody(context.Background(), run, issue)

	assert.Contains(t, body, "## Summary")
	assert.Contains(t, body, "Fixed the bug")
	assert.Contains(t, body, "linear")
	assert.Contains(t, body, "critical")
	assert.Contains(t, body, "## Agent Details")
	assert.Contains(t, body, "5m0s")
}

func TestFormatPRBody_NilSummary(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := &PRService{logger: logger}

	run := &models.AgentRun{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		AgentType: "claude-code",
	}
	issue := &models.Issue{
		Source:   "sentry",
		Severity: "low",
	}

	body := svc.formatPRBody(context.Background(), run, issue)
	assert.Contains(t, body, "Automated fix generated by 143.dev")
}

