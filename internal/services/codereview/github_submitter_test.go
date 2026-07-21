package codereview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	ghservice "github.com/assembledhq/143/internal/services/github"
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

func TestGitHubSubmitter_SubmitReviewPreservesRateLimitResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "29")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, err := w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		require.NoError(t, err, "test response should write the rate-limit body")
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))
	_, err := submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
		HeadSHA:        "abc123",
		OutputKey:      "review-output",
		Decision:       SubmitReviewDecisionCommentOnly,
		Body:           "Review summary",
	})

	var apiErr *ghservice.GitHubAPIError
	require.ErrorAs(t, err, &apiErr, "SubmitReview should expose typed GitHub API errors through wrapped context")
	require.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode, "SubmitReview should preserve GitHub's response status")
	require.Equal(t, "29", apiErr.Header.Get("Retry-After"), "SubmitReview should preserve GitHub's retry delay")
	require.Equal(t, "0", apiErr.Header.Get("X-RateLimit-Remaining"), "SubmitReview should preserve GitHub's remaining budget")
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

func TestGitHubSubmitter_SubmitReviewUpdatesExistingAssessment(t *testing.T) {
	t.Parallel()

	var (
		reviewUpdate  map[string]string
		commentUpdate map[string]string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/comments":
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode([]map[string]any{{
				"id": 456, "path": "src/auth/session.go", "line": 42,
				"body": withCodeReviewFindingMarker("Old finding.", codeReviewFindingMarkerKey("prior-output", "finding-key")),
			}})
			require.NoError(t, err, "test response should write prior inline comment")
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/repo/pulls/comments/456":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&commentUpdate), "inline update should decode")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":456}`))
			require.NoError(t, err, "test response should write inline update")
		case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/repo/pulls/42/reviews/143":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&reviewUpdate), "review update should decode")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":143,"html_url":"https://github.com/acme/repo/pull/42#pullrequestreview-143"}`))
			require.NoError(t, err, "test response should write review update")
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))
	result, err := submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID:    99,
		Repository:        "acme/repo",
		PullNumber:        42,
		HeadSHA:           "new-head",
		OutputKey:         "updated-output",
		PreviousOutputKey: "prior-output",
		ExistingReviewID:  143,
		ExistingReviewURL: "https://github.com/acme/repo/pull/42#pullrequestreview-143",
		Decision:          SubmitReviewDecisionCommentOnly,
		Body:              "143 Code Reviewer did not approve this PR\n\nWhy: required checks are failing.",
		Comments: []SubmitReviewComment{{
			Path: "src/auth/session.go", Line: 42, Body: "Updated finding.", DedupeKey: "finding-key",
		}},
	})

	require.NoError(t, err, "SubmitReview should update an existing GitHub assessment")
	require.Equal(t, int64(143), result.ID, "updated assessment should retain the original review id")
	require.Equal(t, "https://github.com/acme/repo/pull/42#pullrequestreview-143", result.URL, "updated assessment should retain the original review URL")
	require.Contains(t, reviewUpdate["body"], "required checks are failing", "review summary should contain the updated pass assessment")
	require.Contains(t, reviewUpdate["body"], codeReviewOutputMarker("updated-output"), "review summary should carry the new idempotency marker")
	require.Equal(t, withCodeReviewFindingMarker("Updated finding.", "finding-key"), commentUpdate["body"], "matching prior inline finding should be updated in place with a stable reassessment marker")
	require.Equal(t, []SubmitReviewPostedComment{{
		ID: 456, Path: "src/auth/session.go", Line: 42,
		Body:      withCodeReviewFindingMarker("Updated finding.", "finding-key"),
		DedupeKey: "finding-key",
	}}, result.Comments, "updated assessment should return the reused inline comment")
}

func TestGitHubSubmitter_SubmitReviewPromotesUpdatedAssessmentWithFormalApproval(t *testing.T) {
	t.Parallel()

	var (
		reviewUpdate  map[string]string
		approvalPost  map[string]any
		approvalPosts int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/comments":
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[]`))
			require.NoError(t, err, "test response should write no inline comments")
		case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/repo/pulls/42/reviews/143":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&reviewUpdate), "sticky review update should decode")
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":143,"html_url":"https://github.com/acme/repo/pull/42#pullrequestreview-143"}`))
			require.NoError(t, err, "test response should write sticky review update")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews":
			w.Header().Set("Content-Type", "application/json")
			if approvalPosts == 0 {
				_, err := w.Write([]byte(`[]`))
				require.NoError(t, err, "test response should report no existing formal approval")
				break
			}
			err := json.NewEncoder(w).Encode([]map[string]any{{
				"id": 144, "html_url": "https://github.com/acme/repo/pull/42#pullrequestreview-144",
				"body": codeReviewOutputMarker("approved-output:formal-approval"),
			}})
			require.NoError(t, err, "test response should report the existing formal approval")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/pulls/42/reviews/144/comments":
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[]`))
			require.NoError(t, err, "test response should report no formal approval comments")
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/repo/pulls/42/reviews":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&approvalPost), "formal approval request should decode")
			approvalPosts++
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":144,"html_url":"https://github.com/acme/repo/pull/42#pullrequestreview-144"}`))
			require.NoError(t, err, "test response should write formal approval")
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))
	result, err := submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID:    99,
		Repository:        "acme/repo",
		PullNumber:        42,
		HeadSHA:           "new-head",
		OutputKey:         "approved-output",
		PreviousOutputKey: "prior-output",
		ExistingReviewID:  143,
		ExistingReviewURL: "https://github.com/acme/repo/pull/42#pullrequestreview-143",
		Decision:          SubmitReviewDecisionApproved,
		Body:              "143 Code Reviewer approved this PR.",
	})

	require.NoError(t, err, "passing reassessment should update the sticky assessment and submit a formal approval")
	require.Equal(t, int64(143), result.ID, "sticky assessment should remain the persisted review reference")
	require.Contains(t, reviewUpdate["body"], "approved this PR", "sticky assessment should show the current passing result")
	require.Equal(t, "APPROVE", approvalPost["event"], "passing reassessment should create a real GitHub approval")
	require.Equal(t, "new-head", approvalPost["commit_id"], "formal approval should target the reassessed head")
	require.Contains(t, approvalPost["body"], "143-code-review-output", "formal approval should carry a loop-suppression marker")
	_, err = submitter.SubmitReview(context.Background(), SubmitReviewRequest{
		InstallationID:    99,
		Repository:        "acme/repo",
		PullNumber:        42,
		HeadSHA:           "new-head",
		OutputKey:         "approved-output",
		PreviousOutputKey: "prior-output",
		ExistingReviewID:  143,
		ExistingReviewURL: "https://github.com/acme/repo/pull/42#pullrequestreview-143",
		Decision:          SubmitReviewDecisionApproved,
		Body:              "143 Code Reviewer approved this PR.",
	})
	require.NoError(t, err, "retry should reuse the marker-backed formal approval")
	require.Equal(t, 1, approvalPosts, "retry should not post a duplicate formal approval")
}

func TestIsCodeReviewAuthoredBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		expected bool
	}{
		{name: "review summary marker", body: "result\n\n" + codeReviewOutputMarker("output"), expected: true},
		{name: "inline finding marker", body: withCodeReviewFindingMarker("finding", "key"), expected: true},
		{name: "human review", body: "Please add a regression test.", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, IsCodeReviewAuthoredBody(tt.body), "marker detection should classify review authorship")
		})
	}
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

func TestGitHubSubmitter_ListReviewContextPaginatesGraphQLConnections(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		gotPayloads []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/graphql", r.URL.Path, "ListReviewContext should call the GraphQL endpoint")
		require.Equal(t, "Bearer ghs_token", r.Header.Get("Authorization"), "ListReviewContext should authenticate with installation token")

		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload), "GraphQL request body should decode")

		mu.Lock()
		gotPayloads = append(gotPayloads, payload.Variables)
		pageNumber := len(gotPayloads)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch pageNumber {
		case 1:
			require.Nil(t, payload.Variables["threadCursor"], "first page should not send a review thread cursor")
			require.Nil(t, payload.Variables["reviewCursor"], "first page should not send a reviews cursor")
			_, err := w.Write([]byte(`{
				"data": {"repository": {"pullRequest": {
					"reviewThreads": {
						"nodes": [
							{"isResolved": false, "comments": {"nodes": [
								{"author": {"login": "alice"}, "pullRequestReview": null}
							]}}
						],
						"pageInfo": {"hasNextPage": true, "endCursor": "thread-page-1"}
					},
					"reviews": {
						"nodes": [
							{"state": "CHANGES_REQUESTED", "author": {"login": "143-code-reviewer"}}
						],
						"pageInfo": {"hasNextPage": true, "endCursor": "review-page-1"}
					}
				}}}
			}`))
			require.NoError(t, err, "test response should write first page")
		case 2:
			require.Equal(t, "thread-page-1", payload.Variables["threadCursor"], "second page should advance review thread cursor")
			require.Equal(t, "review-page-1", payload.Variables["reviewCursor"], "second page should advance reviews cursor")
			_, err := w.Write([]byte(`{
				"data": {"repository": {"pullRequest": {
					"reviewThreads": {
						"nodes": [
							{"isResolved": true, "comments": {"nodes": [
								{"author": {"login": "bob"}, "pullRequestReview": null}
							]}},
							{"isResolved": false, "comments": {"nodes": [
								{"author": {"login": "143-code-reviewer"}, "pullRequestReview": null}
							]}}
						],
						"pageInfo": {"hasNextPage": false, "endCursor": "thread-page-2"}
					},
					"reviews": {
						"nodes": [
							{"state": "CHANGES_REQUESTED", "author": {"login": "bob"}},
							{"state": "APPROVED", "author": {"login": "alice"}}
						],
						"pageInfo": {"hasNextPage": false, "endCursor": "review-page-2"}
					}
				}}}
			}`))
			require.NoError(t, err, "test response should write second page")
		default:
			http.Error(w, "unexpected GraphQL page", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	submitter := NewGitHubSubmitter(&tokenStub{token: "ghs_token"}, WithGitHubSubmitterBaseURL(server.URL))

	ctx, err := submitter.ListReviewContext(context.Background(), ReviewContextRequest{
		InstallationID: 99,
		Repository:     "acme/repo",
		PullNumber:     42,
		BotLogins:      []string{"143-code-reviewer"},
	})

	require.NoError(t, err, "ListReviewContext should paginate GitHub review context")
	require.Equal(t, ReviewContext{UnresolvedHumanThreads: 1, BlockingHumanReviews: 1}, ctx, "ListReviewContext should aggregate only human blockers across all pages")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, gotPayloads, 2, "ListReviewContext should stop after both GraphQL connections are exhausted")
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

func TestGitHubSubmitter_RemoveRequestedReviewersIncludesEmptyReviewersForTeamOnlyRemoval(t *testing.T) {
	t.Parallel()

	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		TeamReviewers:  []string{"ai-reviewers"},
	})

	require.NoError(t, err, "RemoveRequestedReviewers should remove a team-only review request")
	require.Equal(t, []any{}, gotPayload["reviewers"], "RemoveRequestedReviewers should include the required empty reviewers array")
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
