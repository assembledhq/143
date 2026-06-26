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
