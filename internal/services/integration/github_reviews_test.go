package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubCodeReviewSource_Name(t *testing.T) {
	t.Parallel()
	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{Token: "test"})
	require.Equal(t, "github", src.Name())
}

func TestGitHubCodeReviewSource_ListRecentPRs_Merged(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		require.Equal(t, "closed", r.URL.Query().Get("state"))
		require.Equal(t, "/repos/octocat/hello-world/pulls", r.URL.Path)

		mergedAt := "2025-06-15T12:00:00Z"
		resp := []map[string]any{
			{
				"number":          1,
				"title":           "Add feature A",
				"state":           "closed",
				"html_url":        "https://github.com/octocat/hello-world/pull/1",
				"user":            map[string]any{"login": "alice"},
				"created_at":      "2025-06-10T10:00:00Z",
				"merged_at":       mergedAt,
				"review_comments": 3,
			},
			{
				// Closed but not merged — should be filtered out.
				"number":          2,
				"title":           "Abandoned PR",
				"state":           "closed",
				"html_url":        "https://github.com/octocat/hello-world/pull/2",
				"user":            map[string]any{"login": "bob"},
				"created_at":      "2025-06-11T10:00:00Z",
				"merged_at":       nil,
				"review_comments": 0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL,
		Token:   "test-token",
		Owner:   "octocat",
		Repo:    "hello-world",
	})

	results, err := src.ListRecentPRs(context.Background(), PRFilter{State: "merged", Limit: 10})
	require.NoError(t, err)
	require.Len(t, results, 1, "should filter out non-merged PRs")
	require.Equal(t, 1, results[0].Number)
	require.Equal(t, "merged", results[0].State)
	require.Equal(t, "alice", results[0].Author)
	require.Equal(t, "has_reviews", results[0].ReviewStatus)
}

func TestGitHubCodeReviewSource_ListRecentPRs_Open(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "open", r.URL.Query().Get("state"))

		resp := []map[string]any{
			{
				"number":          3,
				"title":           "WIP feature",
				"state":           "open",
				"html_url":        "https://github.com/octocat/hello-world/pull/3",
				"user":            map[string]any{"login": "carol"},
				"created_at":      "2025-06-12T10:00:00Z",
				"merged_at":       nil,
				"review_comments": 0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL,
		Token:   "test-token",
		Owner:   "octocat",
		Repo:    "hello-world",
	})

	results, err := src.ListRecentPRs(context.Background(), PRFilter{State: "open"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "open", results[0].State)
	require.Equal(t, "pending", results[0].ReviewStatus)
}

func TestGitHubCodeReviewSource_ListRecentPRs_DefaultLimit(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "20", r.URL.Query().Get("per_page"), "default limit should be 20")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL,
		Token:   "t",
		Owner:   "o",
		Repo:    "r",
	})

	results, err := src.ListRecentPRs(context.Background(), PRFilter{Limit: 0})
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestGitHubCodeReviewSource_ListRecentPRs_APIError(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL,
		Token:   "t",
		Owner:   "o",
		Repo:    "r",
	})

	_, err := src.ListRecentPRs(context.Background(), PRFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestGitHubCodeReviewSource_GetPRReviews(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()

	mux.HandleFunc("/repos/octocat/hello-world/pulls/42/reviews", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		resp := []map[string]any{
			{
				"id":           int64(100),
				"user":         map[string]any{"login": "reviewer1"},
				"state":        "APPROVED",
				"body":         "LGTM",
				"submitted_at": "2025-06-15T12:00:00Z",
			},
			{
				"id":           int64(101),
				"user":         map[string]any{"login": "reviewer2"},
				"state":        "CHANGES_REQUESTED",
				"body":         "Needs fix",
				"submitted_at": "2025-06-15T13:00:00Z",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/repos/octocat/hello-world/pulls/42/comments", func(w http.ResponseWriter, r *http.Request) {
		resp := []map[string]any{
			{
				"id":                      int64(200),
				"pull_request_review_id":  int64(101),
				"path":                    "main.go",
				"line":                    42,
				"body":                    "This needs error handling",
				"diff_hunk":              "@@ -40,3 +40,5 @@",
				"user":                    map[string]any{"login": "reviewer2"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL,
		Token:   "test-token",
		Owner:   "octocat",
		Repo:    "hello-world",
	})

	reviews, err := src.GetPRReviews(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, reviews, 2)

	// First review: approved, no inline comments.
	require.Equal(t, "reviewer1", reviews[0].Author)
	require.Equal(t, "APPROVED", reviews[0].State)
	require.Empty(t, reviews[0].Comments)

	// Second review: changes requested, with an inline comment.
	require.Equal(t, "reviewer2", reviews[1].Author)
	require.Equal(t, "CHANGES_REQUESTED", reviews[1].State)
	require.Len(t, reviews[1].Comments, 1)
	require.Equal(t, "main.go", reviews[1].Comments[0].Path)
	require.Equal(t, 42, reviews[1].Comments[0].Line)
	require.Equal(t, "This needs error handling", reviews[1].Comments[0].Body)
}

func TestGitHubCodeReviewSource_GetPRReviews_Empty(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{})
	})
	mux.HandleFunc("/repos/o/r/pulls/1/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL,
		Token:   "t",
		Owner:   "o",
		Repo:    "r",
	})

	reviews, err := src.GetPRReviews(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, reviews)
}

func TestGitHubCodeReviewSource_GetPRReviews_APIError(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL,
		Token:   "t",
		Owner:   "o",
		Repo:    "r",
	})

	_, err := src.GetPRReviews(context.Background(), 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
}

func TestParseLinkNext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "standard next link",
			header:   `<https://api.github.com/repos/o/r/pulls/1/comments?per_page=100&page=2>; rel="next", <https://api.github.com/repos/o/r/pulls/1/comments?per_page=100&page=5>; rel="last"`,
			expected: "https://api.github.com/repos/o/r/pulls/1/comments?per_page=100&page=2",
		},
		{
			name:     "no next link",
			header:   `<https://api.github.com/repos/o/r/pulls/1/comments?per_page=100&page=1>; rel="first"`,
			expected: "",
		},
		{
			name:     "empty header",
			header:   "",
			expected: "",
		},
		{
			name:     "next only",
			header:   `<https://example.com/page2>; rel="next"`,
			expected: "https://example.com/page2",
		},
		{
			name:     "next at end",
			header:   `<https://example.com/first>; rel="first", <https://example.com/next>; rel="next"`,
			expected: "https://example.com/next",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseLinkNext(tt.header)
			require.Equal(t, tt.expected, result, "parseLinkNext should extract the correct URL")
		})
	}
}

func TestGetPRReviews_PaginatesComments(t *testing.T) {
	t.Parallel()

	commentPage := 0
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":           int64(100),
				"user":         map[string]any{"login": "rev"},
				"state":        "COMMENTED",
				"body":         "",
				"submitted_at": "2025-06-15T12:00:00Z",
			},
		})
	})

	mux.HandleFunc("/repos/o/r/pulls/1/comments", func(w http.ResponseWriter, r *http.Request) {
		commentPage++
		if commentPage == 1 {
			// First page — include Link header pointing to page 2.
			page2URL := "http://" + r.Host + "/repos/o/r/pulls/1/comments?per_page=100&page=2"
			w.Header().Set("Link", `<`+page2URL+`>; rel="next"`)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"id": int64(200), "pull_request_review_id": int64(100),
					"path": "a.go", "line": 1, "body": "comment1",
					"diff_hunk": "@@", "user": map[string]any{"login": "rev"},
				},
			})
		} else {
			// Second page — no Link header.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"id": int64(201), "pull_request_review_id": int64(100),
					"path": "b.go", "line": 2, "body": "comment2",
					"diff_hunk": "@@", "user": map[string]any{"login": "rev"},
				},
			})
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL, Token: "t", Owner: "o", Repo: "r",
	})

	reviews, err := src.GetPRReviews(context.Background(), 1)
	require.NoError(t, err, "GetPRReviews should succeed")
	require.Len(t, reviews, 1, "should return one review")
	require.Len(t, reviews[0].Comments, 2, "should have paginated comments from both pages")
	require.Equal(t, "a.go", reviews[0].Comments[0].Path, "first comment from page 1")
	require.Equal(t, "b.go", reviews[0].Comments[1].Path, "second comment from page 2")
}

func TestGetPRReviews_PaginationSafetyCap(t *testing.T) {
	t.Parallel()

	pageCount := 0
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	mux.HandleFunc("/repos/o/r/pulls/1/comments", func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		// Always return a next link to simulate infinite pagination.
		nextURL := fmt.Sprintf("http://%s/repos/o/r/pulls/1/comments?page=%d", r.Host, pageCount+1)
		w.Header().Set("Link", `<`+nextURL+`>; rel="next"`)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": int64(pageCount), "pull_request_review_id": int64(0),
				"path": "f.go", "line": pageCount, "body": "c",
				"diff_hunk": "@@", "user": map[string]any{"login": "u"},
			},
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	src := NewGitHubCodeReviewSource(GitHubCodeReviewConfig{
		BaseURL: server.URL, Token: "t", Owner: "o", Repo: "r",
	})

	_, err := src.GetPRReviews(context.Background(), 1)
	require.NoError(t, err, "GetPRReviews should succeed even at safety cap")
	require.Equal(t, maxPaginationPages, pageCount, "should stop at maxPaginationPages")
}

func TestReviewDecision(t *testing.T) {
	t.Parallel()

	require.Equal(t, "has_reviews", reviewDecision(5))
	require.Equal(t, "has_reviews", reviewDecision(1))
	require.Equal(t, "pending", reviewDecision(0))
}
