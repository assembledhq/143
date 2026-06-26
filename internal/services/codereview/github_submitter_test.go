package codereview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubSubmitter_SubmitReview(t *testing.T) {
	t.Parallel()

	var (
		mu         sync.Mutex
		gotAuth    string
		gotPath    string
		gotPayload map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			require.Equal(t, "/repos/acme/repo/pulls/42/reviews/123/comments", r.URL.Path, "SubmitReview should fetch created review comments")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[{"id":456,"path":"src/auth/session.go","line":42,"body":"Check this edge case."}]`))
			require.NoError(t, err, "test response should write comments")
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPayload), "request body should decode")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":123,"html_url":"https://github.com/acme/repo/pull/42#pullrequestreview-123"}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	result, err := submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
		HeadSHA:        "abc123",
		Decision:       "approved",
		Body:           "143 Code Reviewer approved this PR",
		Comments: []SubmitReviewComment{
			{Path: "src/auth/session.go", Line: 42, Body: "Check this edge case."},
		},
	})

	require.NoError(t, err, "SubmitReview should post a GitHub review")
	require.Equal(t, int64(123), result.ID, "SubmitReview should return review id")
	require.Equal(t, "https://github.com/acme/repo/pull/42#pullrequestreview-123", result.URL, "SubmitReview should return review URL")
	require.Equal(t, []SubmitReviewPostedComment{
		{ID: 456, Path: "src/auth/session.go", Line: 42, Body: "Check this edge case."},
	}, result.Comments, "SubmitReview should recover posted inline comment ids")
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "Bearer ghs_token", gotAuth, "SubmitReview should use installation token")
	require.Equal(t, "/repos/acme/repo/pulls/42/reviews", gotPath, "SubmitReview should call pull review endpoint")
	require.Equal(t, "APPROVE", gotPayload["event"], "approved decision should map to GitHub approval event")
	require.Equal(t, "abc123", gotPayload["commit_id"], "SubmitReview should pin reviewed head SHA")
	comments, ok := gotPayload["comments"].([]any)
	require.True(t, ok, "comments should be an array")
	require.Len(t, comments, 1, "one inline comment should be submitted")
}

func TestGitHubSubmitter_SubmitReviewReturnsExistingMarkedReview(t *testing.T) {
	t.Parallel()

	postCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews":
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode([]map[string]any{{
				"id":       123,
				"html_url": "https://github.com/acme/repo/pull/42#pullrequestreview-123",
				"body":     "Done.\n\n" + codeReviewOutputMarker("review-output-key"),
			}})
			require.NoError(t, err, "test response should write existing review")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews/123/comments":
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode([]map[string]any{{
				"id":   456,
				"path": "src/auth/session.go",
				"line": 42,
				"body": withCodeReviewFindingMarker("Check this edge case.", "finding-key"),
			}})
			require.NoError(t, err, "test response should write existing comments")
		case r.Method == http.MethodPost:
			postCalled = true
			http.Error(w, "unexpected post", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	result, err := submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
		HeadSHA:        "abc123",
		OutputKey:      "review-output-key",
		Decision:       SubmitReviewDecisionApproved,
		Body:           "Done.",
		Comments: []SubmitReviewComment{
			{Path: "src/auth/session.go", Line: 42, Body: "Check this edge case.", DedupeKey: "finding-key"},
		},
	})

	require.NoError(t, err, "SubmitReview should reuse an existing marked review")
	require.False(t, postCalled, "SubmitReview should not create a duplicate review when the output marker already exists")
	require.Equal(t, int64(123), result.ID, "SubmitReview should return the existing review id")
	require.Equal(t, []SubmitReviewPostedComment{
		{ID: 456, Path: "src/auth/session.go", Line: 42, Body: withCodeReviewFindingMarker("Check this edge case.", "finding-key")},
	}, result.Comments, "SubmitReview should recover comments attached to the existing review")
}

func TestGitHubSubmitter_SubmitReviewUpdatesExistingMarkedInlineComment(t *testing.T) {
	t.Parallel()

	var (
		patchBody map[string]string
		postBody  map[string]any
	)
	outputKey := "review-output-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews":
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[]`))
			require.NoError(t, err, "test response should write no existing reviews")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/comments":
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode([]map[string]any{{
				"id":   456,
				"path": "src/auth/session.go",
				"line": 42,
				"body": withCodeReviewFindingMarker("Old body.", codeReviewFindingMarkerKey(outputKey, "finding-key")),
			}})
			require.NoError(t, err, "test response should write pull comments")
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/repo/pulls/comments/456":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&patchBody), "patch body should decode")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":456}`))
			require.NoError(t, err, "test response should write patch response")
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/repo/pulls/42/reviews":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&postBody), "post body should decode")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":123,"html_url":"https://github.com/acme/repo/pull/42#pullrequestreview-123"}`))
			require.NoError(t, err, "test response should write review")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews/123/comments":
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[]`))
			require.NoError(t, err, "test response should write no new comments")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	result, err := submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
		HeadSHA:        "abc123",
		OutputKey:      outputKey,
		Decision:       SubmitReviewDecisionCommentOnly,
		Body:           "Review complete.",
		Comments: []SubmitReviewComment{
			{Path: "src/auth/session.go", Line: 42, Body: "New body.", DedupeKey: "finding-key"},
		},
	})

	require.NoError(t, err, "SubmitReview should update a matching marked inline comment")
	require.Equal(t, int64(123), result.ID, "SubmitReview should still submit the final review summary")
	require.Equal(t, withCodeReviewFindingMarker("New body.", codeReviewFindingMarkerKey(outputKey, "finding-key")), patchBody["body"], "SubmitReview should update the stale inline comment body")
	require.NotContains(t, postBody, "comments", "SubmitReview should not post a duplicate inline comment when an existing marker was updated")
	require.Equal(t, []SubmitReviewPostedComment{
		{ID: 456, Path: "src/auth/session.go", Line: 42, Body: withCodeReviewFindingMarker("New body.", codeReviewFindingMarkerKey(outputKey, "finding-key")), DedupeKey: "finding-key"},
	}, result.Comments, "SubmitReview should return the existing updated comment id with the original finding key")
}

func TestGitHubSubmitter_SubmitReviewDoesNotReuseMarkedInlineCommentFromDifferentOutput(t *testing.T) {
	t.Parallel()

	var (
		patchCalled bool
		postBody    map[string]any
	)
	outputKey := "new-output-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews":
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[]`))
			require.NoError(t, err, "test response should write no existing reviews")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/comments":
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode([]map[string]any{{
				"id":   456,
				"path": "src/auth/session.go",
				"line": 42,
				"body": withCodeReviewFindingMarker("Old body.", codeReviewFindingMarkerKey("old-output-key", "finding-key")),
			}})
			require.NoError(t, err, "test response should write pull comments")
		case r.Method == http.MethodPatch:
			patchCalled = true
			http.Error(w, "unexpected patch", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/repo/pulls/42/reviews":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&postBody), "post body should decode")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":123,"html_url":"https://github.com/acme/repo/pull/42#pullrequestreview-123"}`))
			require.NoError(t, err, "test response should write review")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews/123/comments":
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode([]map[string]any{{
				"id":   789,
				"path": "src/auth/session.go",
				"line": 42,
				"body": withCodeReviewFindingMarker("New body.", codeReviewFindingMarkerKey(outputKey, "finding-key")),
			}})
			require.NoError(t, err, "test response should write created review comments")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	result, err := submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
		HeadSHA:        "abc123",
		OutputKey:      outputKey,
		Decision:       SubmitReviewDecisionCommentOnly,
		Body:           "Review complete.",
		Comments: []SubmitReviewComment{
			{Path: "src/auth/session.go", Line: 42, Body: "New body.", DedupeKey: "finding-key"},
		},
	})

	require.NoError(t, err, "SubmitReview should post a fresh inline comment when only old-output markers exist")
	require.False(t, patchCalled, "SubmitReview should not patch comments marked for a different review output")
	comments, ok := postBody["comments"].([]any)
	require.True(t, ok, "new review payload should include inline comments")
	require.Len(t, comments, 1, "new review payload should include the finding for the new output")
	require.Equal(t, []SubmitReviewPostedComment{
		{ID: 789, Path: "src/auth/session.go", Line: 42, Body: withCodeReviewFindingMarker("New body.", codeReviewFindingMarkerKey(outputKey, "finding-key")), DedupeKey: "finding-key"},
	}, result.Comments, "SubmitReview should return the newly posted comment with the original finding key")
}

func TestGitHubSubmitter_ListPullRequestFiles(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`[
			{"filename":"internal/api/router.go","additions":10,"deletions":2,"status":"modified"},
			{"filename":"go.mod","additions":1,"deletions":0,"status":"modified"}
		]`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	files, err := submitter.ListPullRequestFiles(context.Background(), PullRequestFilesRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
	})

	require.NoError(t, err, "ListPullRequestFiles should load changed files from GitHub")
	require.Equal(t, "/repos/acme/repo/pulls/42/files?per_page=100", gotPath, "ListPullRequestFiles should call the GitHub files endpoint")
	require.Equal(t, []PullRequestFile{
		{Filename: "internal/api/router.go", Additions: 10, Deletions: 2, Status: "modified"},
		{Filename: "go.mod", Additions: 1, Deletions: 0, Status: "modified"},
	}, files, "ListPullRequestFiles should decode file stats")
}

func TestGitHubSubmitter_PublishCommitStatus(t *testing.T) {
	t.Parallel()

	var (
		gotPath    string
		gotPayload map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPayload), "request body should decode")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":321}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	err := submitter.PublishCommitStatus(context.Background(), CommitStatusRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		SHA:            "abc123",
		State:          CommitStatusStatePending,
		Context:        "143 Code Reviewer",
		Description:    "Review is running",
		TargetURL:      "https://143.dev/sessions/sess_123",
	})

	require.NoError(t, err, "PublishCommitStatus should post commit status")
	require.Equal(t, "/repos/acme/repo/statuses/abc123", gotPath, "PublishCommitStatus should call the commit status endpoint")
	require.Equal(t, "pending", gotPayload["state"], "PublishCommitStatus should send requested state")
	require.Equal(t, "143 Code Reviewer", gotPayload["context"], "PublishCommitStatus should send status context")
	require.Equal(t, "Review is running", gotPayload["description"], "PublishCommitStatus should send description")
	require.Equal(t, "https://143.dev/sessions/sess_123", gotPayload["target_url"], "PublishCommitStatus should include target URL")
}

func TestGitHubSubmitter_RemoveRequestedReviewers(t *testing.T) {
	t.Parallel()

	var (
		gotPath    string
		gotPayload map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPayload), "request body should decode")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	err := submitter.RemoveRequestedReviewers(context.Background(), RequestedReviewersRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
		Reviewers:      []string{"143-code-reviewer"},
		TeamReviewers:  []string{"ai-reviewers"},
	})

	require.NoError(t, err, "RemoveRequestedReviewers should remove stale reviewer requests")
	require.Equal(t, "/repos/acme/repo/pulls/42/requested_reviewers", gotPath, "RemoveRequestedReviewers should call requested reviewers endpoint")
	require.Equal(t, []any{"143-code-reviewer"}, gotPayload["reviewers"], "RemoveRequestedReviewers should include user reviewers")
	require.Equal(t, []any{"ai-reviewers"}, gotPayload["team_reviewers"], "RemoveRequestedReviewers should include team reviewers")
}

func TestGitHubSubmitter_SubmitReviewRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  SubmitReviewRequest
	}{
		{
			name: "missing installation id",
			req: SubmitReviewRequest{
				Repository: "acme/repo",
				PullNumber: 42,
				HeadSHA:    "abc123",
				Decision:   SubmitReviewDecisionCommentOnly,
			},
		},
		{
			name: "missing head sha",
			req: SubmitReviewRequest{
				InstallationID: 99,
				Repository:     "acme/repo",
				PullNumber:     42,
				Decision:       SubmitReviewDecisionCommentOnly,
			},
		},
		{
			name: "unknown decision",
			req: SubmitReviewRequest{
				InstallationID: 99,
				Repository:     "acme/repo",
				PullNumber:     42,
				HeadSHA:        "abc123",
				Decision:       "dismiss",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"})
			_, err := submitter.SubmitReview(context.Background(), tt.req)
			require.Error(t, err, "SubmitReview should reject invalid input before calling GitHub")
		})
	}
}

type tokenStub struct {
	token string
}

func (s *tokenStub) GetInstallationToken(context.Context, int64) (string, error) {
	return s.token, nil
}
